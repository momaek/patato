package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
)

// nativeHandler 把原生节点协议的接口转给 native 包；放在闭包里，契约测试构造路由表时不需要真实依赖。
func (s *Server) nativeHandler(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := s.d.Native
		switch name {
		case "enroll":
			h.Enroll(c)
		case "sync":
			h.Sync(c)
		case "report":
			h.Report(c)
		case "ws":
			h.WS(c)
		}
	}
}

type machineView struct {
	Host         string     `json:"host"`
	Status       string     `json:"status"` // enrolled / pending_enroll / not_enrolled / revoked
	Connected    bool       `json:"connected"`
	Hostname     string     `json:"hostname"`
	AgentVersion string     `json:"agent_version"`
	Kernel       string     `json:"kernel"`
	EnrolledAt   *time.Time `json:"enrolled_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`
	Nodes        []ref      `json:"nodes"`
	Version      int64      `json:"version"`
}

func machineStatus(m db.Machine, now time.Time) string {
	switch {
	case m.TokenHash != nil:
		return "enrolled"
	case m.EnrollHash != nil && m.EnrollExpiresAt != nil && m.EnrollExpiresAt.After(now):
		return "pending_enroll"
	case m.EnrolledAt != nil:
		return "revoked"
	}
	return "not_enrolled"
}

// machineViews 合并 machines 表和节点的 host：还没接入过 patato-node、但已有原生节点的机器也列出来。
func (s *Server) machineViews(c *gin.Context, only string) ([]machineView, error) {
	machines, err := s.d.Store.ListMachines(c)
	if err != nil {
		return nil, err
	}
	nodes, err := s.d.Store.ListNodes(c)
	if err != nil {
		return nil, err
	}
	byHost := map[string][]ref{}
	for _, n := range nodes {
		if n.Backend == "native" && n.Host != "" {
			byHost[n.Host] = append(byHost[n.Host], ref{ID: n.ID, Code: n.Code, Name: n.Name})
		}
	}
	connected := s.d.Native.Hub().Connected()
	now := time.Now()
	out := []machineView{}
	seen := map[string]bool{}
	for _, m := range machines {
		seen[m.Host] = true
		if only != "" && m.Host != only {
			continue
		}
		out = append(out, machineView{
			Host: m.Host, Status: machineStatus(m, now), Connected: connected[m.ID], Hostname: m.Hostname,
			AgentVersion: m.AgentVersion, Kernel: m.Kernel, EnrolledAt: m.EnrolledAt, LastSeenAt: m.LastSeenAt,
			Nodes: nonNilRefs(byHost[m.Host]), Version: m.Version,
		})
	}
	for host, refs := range byHost {
		if seen[host] || (only != "" && host != only) {
			continue
		}
		out = append(out, machineView{Host: host, Status: "not_enrolled", Nodes: refs})
	}
	return out, nil
}

func (s *Server) machineList(c *gin.Context) {
	views, err := s.machineViews(c, "")
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"items": views})
}

func (s *Server) machineGet(c *gin.Context) {
	views, err := s.machineViews(c, c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if len(views) == 0 {
		fail(c, apierr.NotFound("machine_not_found", "机器 %s 没有接入 patato-node，也没有由它承载的节点", c.Param("id")).
			WithHint("用 patato machine install-cmd %s 接入", c.Param("id")))
		return
	}
	ok(c, views[0])
}

// machineInstallCmd 生成 patato-node 的安装命令。命令里含一次性注册码，所以要求可写权限。
func (s *Server) machineInstallCmd(c *gin.Context) {
	cmd, err := s.patatoNodeInstall(c, c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, cmd)
}

const enrollTTL = time.Hour

// patatoNodeInstall 给机器准备注册码（一小时内重复调用返回同一个）并生成安装命令。
func (s *Server) patatoNodeInstall(c *gin.Context, host string) (InstallCmd, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return InstallCmd{}, apierr.BadRequest("missing_machine", "缺少机器名")
	}
	m, err := s.d.Store.EnsureMachine(c, host)
	if err != nil {
		return InstallCmd{}, err
	}
	var code string
	if m.EnrollCipher != nil && m.EnrollExpiresAt != nil && time.Until(*m.EnrollExpiresAt) > 10*time.Minute {
		if b, err := s.d.Box.Open(m.EnrollCipher); err == nil {
			code = string(b)
		}
	}
	expires := time.Now().Add(enrollTTL)
	if code == "" {
		code = secret.Token("pe")
		if err := s.d.Store.SetMachineEnrollment(c, db.SetMachineEnrollmentParams{
			ID: m.ID, EnrollHash: secret.Hash(code), EnrollCipher: s.d.Box.Seal([]byte(code)), EnrollExpiresAt: &expires,
		}); err != nil {
			return InstallCmd{}, err
		}
	} else {
		expires = *m.EnrollExpiresAt
	}
	return PatatoNodeInstall(s.d.Cfg.PublicURL, s.d.Cfg.Node.BinaryURL, host, code, expires, m.TokenHash != nil), nil
}

// PatatoNodeInstall 生成 patato-node 的安装命令：下载二进制、凭注册码换取机器 token、装成 systemd 服务。
// 已安装时重复执行等于升级并重新接入。
func PatatoNodeInstall(panelURL, binaryURL, host, code string, expires time.Time, reenroll bool) InstallCmd {
	url := strings.ReplaceAll(binaryURL, "{arch}", "${arch}")
	cmd := strings.Join([]string{
		"set -e",
		`case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; esac`,
		fmt.Sprintf(`curl -fsSL -o /usr/local/bin/patato-node.new "%s"`, url),
		"chmod 755 /usr/local/bin/patato-node.new && mv /usr/local/bin/patato-node.new /usr/local/bin/patato-node",
		fmt.Sprintf("/usr/local/bin/patato-node enroll --panel %s --code %s", shellQuote(panelURL), shellQuote(code)),
		"/usr/local/bin/patato-node install-service",
	}, "\n")
	note := fmt.Sprintf("在 %s 上原样执行。注册码 %s 前有效且只能用一次；接入后，这台机器上 backend 为 native 的节点会自动启动。",
		host, expires.Format(time.RFC3339))
	if reenroll {
		note += "这台机器已经接入过，重新执行会换发机器 token，旧 token 立即失效。"
	}
	cfg, _ := json.Marshal(map[string]any{"panel": panelURL, "machine": host})
	return InstallCmd{Backend: "patato-node", Command: cmd, Config: json.RawMessage(cfg), Note: note}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
