// Package subscribe 生成用户订阅。路径与 Xboard 兼容（/api/v1/client/subscribe?token=），
// 迁移过来的老链接无需改动；按 flag 参数或 User-Agent 选择格式。
package subscribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/rules"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// Server 是订阅里的一个节点，已经填好了这个用户的连接密码。
type Server struct {
	Name     string
	Type     string
	Region   string
	Host     string
	Port     int
	Password string
	Tags     []string
	S        proto.Settings
}

type Format string

const (
	FormatGeneral Format = "general"
	FormatClash   Format = "clash"
	FormatSingBox Format = "singbox"
	FormatSurge   Format = "surge"
	FormatQuanX   Format = "quantumultx"
	FormatLoon    Format = "loon"
)

// 关键字按长度从长到短匹配，避免 "clash" 抢先匹配 "clash-verge" 这类更具体的名字。
var formatFlags = []struct {
	flag   string
	format Format
}{
	{"clashmetaforandroid", FormatClash},
	{"quantumult", FormatQuanX},
	{"surfboard", FormatSurge},
	{"clash-verge", FormatClash},
	{"clash.meta", FormatClash},
	{"sing-box", FormatSingBox},
	{"singbox", FormatSingBox},
	{"hiddify", FormatSingBox},
	{"flclash", FormatClash},
	{"nekobox", FormatClash},
	{"mihomo", FormatClash},
	{"surge", FormatSurge},
	{"quanx", FormatQuanX},
	{"stash", FormatClash},
	{"verge", FormatClash},
	{"clash", FormatClash},
	{"meta", FormatClash},
	{"loon", FormatLoon},
	{"sfa", FormatSingBox},
	{"sfi", FormatSingBox},
	{"sfm", FormatSingBox},
	{"sft", FormatSingBox},
}

// DetectFormat 按 flag 或 User-Agent 判断客户端想要的格式，识别不了时返回通用格式。
func DetectFormat(flag, userAgent string) Format {
	s := strings.ToLower(flag)
	if s == "" {
		s = strings.ToLower(userAgent)
	}
	switch s {
	case "general", "v2rayn", "v2rayng", "shadowrocket", "base64":
		return FormatGeneral
	case "surge", "surfboard":
		return FormatSurge
	case "quantumultx", "quanx", "quantumult":
		return FormatQuanX
	case "loon":
		return FormatLoon
	}
	for _, f := range formatFlags {
		if strings.Contains(s, f.flag) {
			return f.format
		}
	}
	return FormatGeneral
}

type Config struct {
	ServiceName string
	Path        string
	// BaseURL 是订阅域名（subscribe_url），用于生成托管配置里的自更新地址。
	BaseURL string
	Loc     *time.Location
}

type Handler struct {
	st    *store.Store
	cfg   Config
	rules *rules.Service
	log   *slog.Logger
}

func New(st *store.Store, cfg Config, rs *rules.Service, log *slog.Logger) *Handler {
	return &Handler{st: st, cfg: cfg, rules: rs, log: log}
}

// rulesPath 是规则文件的下载路径，订阅里的规则集从这里拉取，不直连 GitHub。
const rulesPath = "/api/v1/client/rules/"

// Register 注册订阅路径。HEAD 与 GET 同样处理，部分客户端用 HEAD 读取流量信息头。
func (h *Handler) Register(r gin.IRouter) {
	byQuery := func(c *gin.Context) { h.serve(c, c.Query("token")) }
	byPath := func(c *gin.Context) { h.serve(c, c.Param("token")) }
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		r.Handle(m, "/api/v1/client/subscribe", byQuery)
		r.Handle(m, rulesPath+":file", h.serveList)
		if h.cfg.Path != "" && h.cfg.Path != "/api/v1/client/subscribe" {
			r.Handle(m, h.cfg.Path, byQuery)
			r.Handle(m, h.cfg.Path+"/:token", byPath)
		}
	}
}

// URL 返回用户的订阅链接。
func URL(base, path, token string) string {
	if path == "" {
		return base + "/api/v1/client/subscribe?token=" + url.QueryEscape(token)
	}
	return base + path + "/" + url.PathEscape(token)
}

func (h *Handler) serve(c *gin.Context, token string) {
	if token == "" {
		c.Status(http.StatusForbidden)
		return
	}
	u, err := h.st.GetUserBySubToken(c, token)
	if err != nil {
		c.Status(http.StatusForbidden)
		return
	}
	if !Available(u, time.Now()) {
		c.Data(http.StatusForbidden, "text/plain", nil)
		return
	}
	servers, err := ServersFor(c, h.st.Queries, u)
	if err != nil {
		h.log.Error("subscription servers", "user", u.ID, "err", err)
		c.Status(http.StatusInternalServerError)
		return
	}
	servers = filter(servers, c.Query("types"), c.Query("filter"))
	servers = byRegion(servers, c.Query("region"))

	expire := int64(0)
	if u.ExpiresAt != nil {
		expire = u.ExpiresAt.Unix()
	}
	c.Header("subscription-userinfo", fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", u.UsedUp, u.UsedDown, u.TransferBytes, expire))
	c.Header("profile-update-interval", "24")
	c.Header("content-disposition", "attachment; filename*=UTF-8''"+url.PathEscape(h.cfg.ServiceName))

	ruled, err := h.rulesFor(c, u)
	if err != nil {
		h.log.Error("subscription rules", "user", u.ID, "err", err)
	}
	switch DetectFormat(c.Query("flag"), c.GetHeader("User-Agent")) {
	case FormatClash:
		b, err := Clash(servers, ruled)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/yaml; charset=utf-8", b)
	case FormatSingBox:
		b, err := SingBox(servers, ruled)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", b)
	case FormatSurge:
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(Surge(servers, h.selfURL(c), ruled)))
	case FormatQuanX:
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(QuantumultX(servers)))
	case FormatLoon:
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(Loon(servers)))
	default:
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(General(servers)))
	}
}

// Available 判断用户当前能否使用：未封禁、未到期、流量未用完。
func Available(u db.User, now time.Time) bool {
	if u.Banned {
		return false
	}
	if u.ExpiresAt != nil && !u.ExpiresAt.After(now) {
		return false
	}
	return u.UsedUp+u.UsedDown < u.TransferBytes
}

// ServersFor 返回用户套餐里的节点。只下发在线节点；如果没有一个在线（例如心跳检测出了问题），
// 退回下发离线节点，避免用户的订阅被清空。待接入的节点永远不下发。
func ServersFor(ctx context.Context, q *db.Queries, u db.User) ([]Server, error) {
	if u.PlanID == nil {
		return nil, nil
	}
	nodes, err := q.ListPlanNodes(ctx, *u.PlanID)
	if err != nil {
		return nil, err
	}
	pick := func(state string) []Server {
		var out []Server
		for _, n := range nodes {
			if n.State != state {
				continue
			}
			s := proto.Parse(n.Settings)
			out = append(out, Server{
				Name: n.Name, Type: n.Protocol, Region: n.Region, Host: n.Address, Port: int(n.Port), Tags: n.Tags, S: s,
				Password: proto.UserPassword(n.Protocol, s, u.Uuid),
			})
		}
		return out
	}
	servers := pick("online")
	if len(servers) == 0 {
		servers = pick("offline")
	}
	return servers, nil
}

// selfURL 是这次请求的订阅链接，写进 Surge 托管配置供它自动更新。
func (h *Handler) selfURL(c *gin.Context) string {
	return h.base(c) + c.Request.URL.RequestURI()
}

// base 是订阅域名：配置了 subscribe_url 时用它，否则用这次请求的地址。
func (h *Handler) base(c *gin.Context) string {
	if h.cfg.BaseURL != "" {
		return h.cfg.BaseURL
	}
	scheme := "https"
	if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	return scheme + "://" + c.Request.Host
}

// rulesFor 返回用户的分流规则；没有配置、模板还没拉取成功或解析失败时返回 nil，用内置规则，订阅不受影响。
func (h *Handler) rulesFor(c *gin.Context, u db.User) (*Rules, error) {
	rp, err := rules.ProfileFor(c, h.st.Queries, u)
	if err != nil || rp == nil || rp.Content == "" {
		return nil, err
	}
	p, err := rules.Parse(rp.Content)
	if err != nil {
		return nil, fmt.Errorf("rule profile %s: %w", rp.Code, err)
	}
	return &Rules{Profile: p, ListBase: h.base(c) + rulesPath}, nil
}

// serveList 下发规则文件，按扩展名转换成客户端需要的格式：.yaml（Clash）、.list（Surge）、.json（sing-box）。
func (h *Handler) serveList(c *gin.Context) {
	file := c.Param("file")
	key, ext, _ := strings.Cut(file, ".")
	if h.rules == nil || key == "" {
		c.Status(http.StatusNotFound)
		return
	}
	l, err := h.rules.List(c, key)
	if errors.Is(err, rules.ErrNotFound) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil || l.FetchedAt == nil {
		h.log.Warn("serve rule list", "key", key, "err", err)
		c.Status(http.StatusServiceUnavailable)
		return
	}
	entries := rules.ParseList(l.Content)
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Last-Modified", l.FetchedAt.UTC().Format(http.TimeFormat))
	switch ext {
	case "yaml":
		b, err := rules.Clash(entries)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/yaml; charset=utf-8", b)
	case "list":
		c.Data(http.StatusOK, "text/plain; charset=utf-8", rules.Surge(entries))
	case "json":
		b, err := rules.SingBox(entries)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", b)
	default:
		c.Status(http.StatusNotFound)
	}
}

// byRegion 按地区下发：region 参数可以用 | 或逗号分隔多个地区，按节点的地区精确匹配。
func byRegion(servers []Server, regions string) []Server {
	if strings.TrimSpace(regions) == "" {
		return servers
	}
	want := map[string]bool{}
	for _, r := range splitter.Split(regions, -1) {
		if r = strings.TrimSpace(r); r != "" {
			want[strings.ToLower(r)] = true
		}
	}
	out := servers[:0:0]
	for _, s := range servers {
		if want[strings.ToLower(s.Region)] {
			out = append(out, s)
		}
	}
	return out
}

var splitter = regexp.MustCompile(`[|,｜]+`)

// filter 实现 Xboard 的 types 与 filter 参数：按协议筛选、按名称或标签关键字筛选。
func filter(servers []Server, types, keywords string) []Server {
	allowed := map[string]bool{}
	if types != "" && types != "all" {
		for _, t := range splitter.Split(types, -1) {
			if t = proto.Normalize(t); t != "" {
				allowed[t] = true
			}
		}
	}
	var kws []string
	if keywords != "" && len([]rune(keywords)) <= 20 {
		for _, k := range splitter.Split(keywords, -1) {
			if k = strings.TrimSpace(k); k != "" {
				kws = append(kws, strings.ToLower(k))
			}
		}
	}
	if len(allowed) == 0 && len(kws) == 0 {
		return servers
	}
	out := servers[:0:0]
	for _, s := range servers {
		if len(allowed) > 0 && !allowed[s.Type] {
			continue
		}
		if len(kws) > 0 && !matchKeyword(s, kws) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func matchKeyword(s Server, kws []string) bool {
	name := strings.ToLower(s.Name)
	for _, k := range kws {
		if strings.Contains(name, k) {
			return true
		}
		for _, t := range s.Tags {
			if strings.EqualFold(t, k) {
				return true
			}
		}
	}
	return false
}
