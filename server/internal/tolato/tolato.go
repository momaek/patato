// Package tolato 是 tolato 的只读客户端：查询机器信息，以及拉取面板主机的执行记录。
// Patato 从不通过 tolato 在机器上执行命令。
package tolato

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/ops"
)

type Client struct {
	base   string
	apiKey string
	http   *http.Client
}

func New(base, apiKey string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), apiKey: apiKey, http: &http.Client{Timeout: 10 * time.Second}}
}

// ErrUnsupported 表示 tolato 还没有提供所需的接口。
var ErrUnsupported = errors.New("tolato endpoint not available")

type node struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Alias       *string `json:"alias"`
	IP          string  `json:"ip"`
	CountryCode string  `json:"country_code"`
	City        string  `json:"city"`
	Status      string  `json:"status"`
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tolato: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return ErrUnsupported
	case resp.StatusCode >= 300:
		return fmt.Errorf("tolato: %s %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) listNodes(ctx context.Context) ([]node, error) {
	var nodes []node
	if err := c.get(ctx, "/api/v1/nodes", &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// findNode 按名称、别名或 IP 匹配机器。
func (c *Client) findNode(ctx context.Context, name string) (*node, error) {
	nodes, err := c.listNodes(ctx)
	if err != nil {
		return nil, apierr.New(http.StatusBadGateway, "tolato_unavailable", "查询 tolato 失败：%v", err).WithHint("稍后重试，或直接传 address")
	}
	var matches []node
	for _, n := range nodes {
		if strings.EqualFold(n.Name, name) || n.IP == name || (n.Alias != nil && strings.EqualFold(*n.Alias, name)) {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 0:
		return nil, apierr.NotFound("machine_not_found", "tolato 中没有名为 %s 的机器", name).WithHint("用 tolato nodes list 查看机器名")
	case 1:
		return &matches[0], nil
	default:
		return nil, apierr.Conflict("machine_ambiguous", "tolato 中有 %d 台机器匹配 %s", len(matches), name).WithHint("传 tolato 中唯一的机器名或 IP")
	}
}

func (c *Client) LookupMachine(ctx context.Context, name string) (*ops.Machine, error) {
	n, err := c.findNode(ctx, name)
	if err != nil {
		return nil, err
	}
	return &ops.Machine{Name: n.Name, IP: n.IP, Country: n.CountryCode, City: n.City}, nil
}

// AttestedNode 是 tolato 核实过的机器身份。
type AttestedNode struct {
	ID    string
	Name  string
	Alias string
	IP    string
}

// Names 是这台机器在 Patato 里可能用的机器名：名称、别名、IP。
func (n AttestedNode) Names() []string {
	out := []string{n.Name}
	if n.Alias != "" {
		out = append(out, n.Alias)
	}
	if n.IP != "" {
		out = append(out, n.IP)
	}
	return out
}

// VerifyAttestation 让 tolato 核实 patato-node 交来的机器身份证明。证明只对 audience（面板的 API 地址）有效，
// 核实后即作废。证明无效、过期、已用过，或 API key 看不到这台机器时返回 ErrAttestationInvalid。
func (c *Client) VerifyAttestation(ctx context.Context, token, audience string) (*AttestedNode, error) {
	body, _ := json.Marshal(map[string]string{"token": token, "audience": audience})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/attestations/verify", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tolato: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrAttestationInvalid
	case resp.StatusCode == http.StatusMethodNotAllowed:
		return nil, ErrUnsupported
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("tolato: verify attestation: %s", resp.Status)
	}
	var out struct {
		Node node `json:"node"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Node.ID == "" {
		return nil, fmt.Errorf("tolato: unexpected attestation response")
	}
	n := &AttestedNode{ID: out.Node.ID, Name: out.Node.Name, IP: out.Node.IP}
	if out.Node.Alias != nil {
		n.Alias = *out.Node.Alias
	}
	return n, nil
}

// ErrAttestationInvalid 表示 tolato 不认这份证明。
var ErrAttestationInvalid = errors.New("tolato attestation invalid, expired or already used")

// Command 是 tolato 在某台机器上执行过的一条命令。
type Command struct {
	ID        uint64    `json:"id"`
	Command   string    `json:"command"`
	Source    string    `json:"source"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

// CommandsSince 拉取某台机器 since 之后的执行记录。
//
// tolato 目前只在网页会话下提供执行记录接口；这里按 /api/v1/nodes/{id}/commands 调用，
// 响应沿用 tolato 的分页格式（items、数字 id），tolato 侧补上之前返回 ErrUnsupported
// （https://github.com/momaek/tolato/issues/27）。带 since 时 tolato 按时间升序返回，
// 一次超过一页的部分留到下一轮，由调用方推进游标取到。
func (c *Client) CommandsSince(ctx context.Context, machine string, since time.Time) ([]Command, error) {
	n, err := c.findNode(ctx, machine)
	if err != nil {
		return nil, err
	}
	var page struct {
		Items []Command `json:"items"`
	}
	path := fmt.Sprintf("/api/v1/nodes/%s/commands?since=%s&page_size=100", url.PathEscape(n.ID), url.QueryEscape(since.UTC().Format(time.RFC3339)))
	if err := c.get(ctx, path, &page); err != nil {
		return nil, err
	}
	out := page.Items[:0]
	for _, cmd := range page.Items {
		if cmd.CreatedAt.After(since) {
			out = append(out, cmd)
		}
	}
	return out, nil
}

// DirectoryMachine 是 tolato 里的一台机器：可匹配的名字（名称、别名、IP）、IP 和是否在线。
type DirectoryMachine struct {
	Names  []string
	IP     string
	Online bool
}

// Machines 列出 tolato 中的全部机器，用于刷新中转与落地机器的 IP（动态 IP）和在线状态。只读。
func (c *Client) Machines(ctx context.Context) ([]DirectoryMachine, error) {
	nodes, err := c.listNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DirectoryMachine, 0, len(nodes))
	for _, n := range nodes {
		names := []string{n.Name}
		if n.Alias != nil && *n.Alias != "" {
			names = append(names, *n.Alias)
		}
		if n.IP != "" {
			names = append(names, n.IP)
		}
		out = append(out, DirectoryMachine{Names: names, IP: n.IP, Online: n.Status == "online"})
	}
	return out, nil
}
