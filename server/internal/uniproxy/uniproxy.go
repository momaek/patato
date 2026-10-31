// Package uniproxy 实现与 Xboard / V2board 兼容的节点接口（/api/v1/server/UniProxy/*），
// 让 V2bX、XrayR 等节点后端不改代码即可接入。
//
// 与 Xboard 的差异只在鉴权：每个节点有自己的密钥；旧的全局 token 只对标记了
// allow_legacy_token 的节点在迁移过渡期内有效。
package uniproxy

import (
	"bytes"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/metrics"
	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

const maxBody = 32 << 20

type Config struct {
	PushInterval int
	PullInterval int
	LegacyToken  string
	DeviceTTL    time.Duration
}

type Handler struct {
	st     *store.Store
	cfg    Config
	events *events.Recorder
	log    *slog.Logger
}

func New(st *store.Store, cfg Config, ev *events.Recorder, log *slog.Logger) *Handler {
	return &Handler{st: st, cfg: cfg, events: ev, log: log}
}

func (h *Handler) Register(r gin.IRouter) {
	g := r.Group("/api/v1/server/UniProxy", h.auth)
	g.GET("/config", h.config)
	g.GET("/user", h.users)
	g.POST("/push", h.push)
	g.POST("/alive", h.alive)
	g.GET("/alivelist", h.aliveList)
	g.POST("/status", h.status)
}

const (
	ctxNode = "uniproxy.node"
	ctxBody = "uniproxy.body"
)

// auth 校验 token + node_id（+ node_type）。参数可以在 query 里，也可以混在 JSON 请求体里，
// 后者是部分节点后端 POST 时的做法。
func (h *Handler) auth(c *gin.Context) {
	var body map[string]json.RawMessage
	if c.Request.Method == http.MethodPost {
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBody))
		if err != nil {
			fail(c, http.StatusBadRequest, "读取请求体失败")
			return
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				fail(c, http.StatusUnprocessableEntity, "Invalid data format")
				return
			}
		}
	}
	param := func(key string) string {
		if v := c.Query(key); v != "" {
			return v
		}
		if raw, ok := body[key]; ok {
			delete(body, key)
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return s
			}
			return strings.Trim(string(raw), `"`)
		}
		return ""
	}
	token, nodeIDStr, nodeType := param("token"), param("node_id"), param("node_type")
	if token == "" {
		fail(c, http.StatusForbidden, "token is null")
		return
	}
	nodeID, err := strconv.ParseInt(nodeIDStr, 10, 64)
	if err != nil {
		fail(c, http.StatusUnprocessableEntity, "node_id is invalid")
		return
	}
	n, err := h.st.GetNode(c, nodeID)
	if err != nil {
		fail(c, http.StatusForbidden, "server is not exist")
		return
	}
	if !h.tokenOK(n, token) {
		fail(c, http.StatusForbidden, "token is error")
		return
	}
	if nodeType != "" && proto.Normalize(nodeType) != n.Protocol {
		fail(c, http.StatusForbidden, "server is not exist")
		return
	}
	h.heartbeat(c, n)
	c.Set(ctxNode, n)
	c.Set(ctxBody, body)
	c.Next()
	if c.Writer.Status() < 400 {
		metrics.NodeRequests.WithLabelValues(endpoint(c), "ok").Inc()
	}
}

func (h *Handler) tokenOK(n db.Node, token string) bool {
	if len(n.KeyHash) > 0 && subtle.ConstantTimeCompare(secret.Hash(token), n.KeyHash) == 1 {
		return true
	}
	if n.AllowLegacyToken && h.cfg.LegacyToken != "" {
		return subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.LegacyToken)) == 1
	}
	return false
}

// heartbeat 记录心跳。待接入的节点第一次心跳后转为在线，开始出现在用户订阅里。
func (h *Handler) heartbeat(c *gin.Context, n db.Node) {
	prev, err := h.st.TouchNodeHeartbeat(c, n.ID)
	if err != nil {
		h.log.Error("node heartbeat", "node", n.ID, "err", err)
		return
	}
	switch prev {
	case "offline":
		h.events.Resolve(c, nil, fmt.Sprintf("%s:%d", events.NodeOffline, n.ID))
		h.events.Emit(c, nil, events.Event{Kind: events.NodeOnline, Subject: fmt.Sprintf("node:%d", n.ID),
			Message: fmt.Sprintf("%s 已恢复在线", n.Name)})
	case "pending":
		h.events.Emit(c, nil, events.Event{Kind: events.NodeOnline, Subject: fmt.Sprintf("node:%d", n.ID),
			Message: fmt.Sprintf("%s 收到首次心跳，已上线并下发到订阅", n.Name)})
	}
}

func node(c *gin.Context) db.Node { return c.MustGet(ctxNode).(db.Node) }

func body(c *gin.Context) map[string]json.RawMessage {
	b, _ := c.Get(ctxBody)
	m, _ := b.(map[string]json.RawMessage)
	return m
}

func (h *Handler) config(c *gin.Context) {
	cfg := BuildConfig(node(c))
	cfg["base_config"] = map[string]any{"push_interval": h.cfg.PushInterval, "pull_interval": h.cfg.PullInterval}
	writeETag(c, cfg)
}

type nodeUser struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int32  `json:"speed_limit"`
	DeviceLimit int32  `json:"device_limit"`
}

func (h *Handler) users(c *gin.Context) {
	rows, err := h.st.AvailableUsersForNode(c, node(c).ID)
	if err != nil {
		h.log.Error("list node users", "err", err)
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	users := make([]nodeUser, 0, len(rows))
	for _, r := range rows {
		users = append(users, nodeUser{ID: r.ID, UUID: r.Uuid, SpeedLimit: r.SpeedLimit, DeviceLimit: r.DeviceLimit})
	}
	writeETag(c, map[string]any{"users": users})
}

// push 接收流量上报：先原样落库再返回，聚合扣减由后台任务完成，缩短「已入账但节点以为失败」的窗口。
func (h *Handler) push(c *gin.Context) {
	n := node(c)
	data := map[string][2]float64{}
	for uid, raw := range body(c) {
		var pair []float64
		if json.Unmarshal(raw, &pair) != nil || len(pair) != 2 {
			continue
		}
		if _, err := strconv.ParseInt(uid, 10, 64); err != nil {
			continue
		}
		if pair[0] < 0 || pair[1] < 0 || math.IsNaN(pair[0]) || math.IsNaN(pair[1]) {
			continue
		}
		data[uid] = [2]float64{pair[0], pair[1]}
	}
	if len(data) > 0 {
		payload, _ := json.Marshal(data)
		if err := h.st.InsertTrafficReport(c, db.InsertTrafficReportParams{NodeID: n.ID, Rate: n.Rate, Payload: payload}); err != nil {
			h.log.Error("store traffic report", "node", n.ID, "err", err)
			fail(c, http.StatusInternalServerError, "internal error")
			return
		}
		metrics.TrafficReports.Inc()
	}
	_ = h.st.TouchNodePush(c, n.ID)
	c.JSON(http.StatusOK, gin.H{"data": true})
}

func (h *Handler) alive(c *gin.Context) {
	n := node(c)
	for uid, raw := range body(c) {
		userID, err := strconv.ParseInt(uid, 10, 64)
		if err != nil {
			continue
		}
		var ips []string
		if json.Unmarshal(raw, &ips) != nil {
			continue
		}
		if err := h.st.ReplaceNodeAlive(c, db.ReplaceNodeAliveParams{NodeID: n.ID, UserID: userID, Ips: normalizeIPs(ips)}); err != nil {
			h.log.Error("store alive", "node", n.ID, "err", err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": true})
}

func (h *Handler) aliveList(c *gin.Context) {
	rows, err := h.st.AvailableUsersForNode(c, node(c).ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	var ids []int64
	for _, r := range rows {
		if r.DeviceLimit > 0 {
			ids = append(ids, r.ID)
		}
	}
	alive := map[string]int32{}
	if len(ids) > 0 {
		counts, err := h.st.AliveDeviceCounts(c, db.AliveDeviceCountsParams{Since: time.Now().Add(-h.cfg.DeviceTTL), UserIds: ids})
		if err != nil {
			fail(c, http.StatusInternalServerError, "internal error")
			return
		}
		for _, r := range counts {
			if r.Devices > 0 {
				alive[strconv.FormatInt(r.UserID, 10)] = r.Devices
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"alive": alive})
}

func (h *Handler) status(c *gin.Context) {
	b := body(c)
	load, _ := json.Marshal(b)
	if err := h.st.SetNodeLoad(c, db.SetNodeLoadParams{ID: node(c).ID, Load: load}); err != nil {
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": true, "code": 0, "message": "success"})
}

// writeETag 返回 JSON 并带 ETag；内容未变时返回 304，与 Xboard 行为一致。
func writeETag(c *gin.Context, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fail(c, http.StatusInternalServerError, "internal error")
		return
	}
	sum := sha1.Sum(b)
	etag := hex.EncodeToString(sum[:])
	if strings.Contains(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Header("ETag", `"`+etag+`"`)
	c.Data(http.StatusOK, "application/json", b)
}

func fail(c *gin.Context, status int, msg string) {
	metrics.NodeRequests.WithLabelValues(endpoint(c), "error").Inc()
	c.AbortWithStatusJSON(status, gin.H{"message": msg})
}

func endpoint(c *gin.Context) string {
	p := c.FullPath()
	return p[strings.LastIndex(p, "/")+1:]
}

// normalizeIPs 去掉端口并去重，与 Xboard 的 DeviceStateService 一致。
func normalizeIPs(ips []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		} else if i := strings.LastIndex(ip, "_"); i > 0 {
			// 部分后端用 "ip_端口" 的格式
			ip = ip[:i]
		}
		ip = strings.Trim(ip, "[]")
		if ip != "" && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}
