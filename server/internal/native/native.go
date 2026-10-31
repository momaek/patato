package native

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/metrics"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/uniproxy"
)

type Config struct {
	ReportInterval int
	SyncInterval   int
	DeviceTTL      time.Duration
}

type Handler struct {
	st     *store.Store
	cfg    Config
	events *events.Recorder
	log    *slog.Logger
	users  *userCache
	hub    *Hub
}

func New(st *store.Store, cfg Config, ev *events.Recorder, log *slog.Logger) *Handler {
	h := &Handler{st: st, cfg: cfg, events: ev, log: log, users: newUserCache()}
	h.hub = newHub(h)
	return h
}

// Hub 返回推送中心，由 app 启动它的后台循环。
func (h *Handler) Hub() *Hub { return h.hub }

const ctxMachine = "native.machine"

func machineOf(c *gin.Context) db.Machine { return c.MustGet(ctxMachine).(db.Machine) }

// Auth 校验机器 token（Authorization: Bearer）。
func (h *Handler) Auth(c *gin.Context) {
	token, found := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !found || token == "" {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	m, err := h.st.GetMachineByTokenHash(c, secret.Hash(token))
	if store.IsNotFound(err) {
		fail(c, apierr.New(http.StatusUnauthorized, "invalid_machine_token", "机器 token 无效或已吊销").
			WithHint("在面板上用 patato machine install-cmd <机器> 重新生成安装命令"))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	c.Set(ctxMachine, m)
	c.Next()
}

// Enroll 凭一次性注册码换取机器 token。
func (h *Handler) Enroll(c *gin.Context) {
	var req nodeapi.EnrollRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Code == "" {
		fail(c, apierr.BadRequest("invalid_params", "缺少注册码"))
		return
	}
	token := secret.Token("mt")
	m, err := h.st.ClaimMachineEnrollment(c, db.ClaimMachineEnrollmentParams{
		TokenHash: secret.Hash(token), Hostname: truncate(req.Hostname, 255), AgentVersion: truncate(req.Version, 64),
		EnrollHash: secret.Hash(req.Code),
	})
	if store.IsNotFound(err) {
		fail(c, apierr.New(http.StatusUnauthorized, "invalid_enroll_code", "注册码无效、已用过或已过期").
			WithHint("注册码一小时内有效且只能用一次；用 patato machine install-cmd <机器> 重新生成"))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	h.log.Info("machine enrolled", "machine", m.Host, "hostname", m.Hostname)
	c.JSON(http.StatusOK, nodeapi.EnrollResponse{Machine: m.Host, Token: token})
}

// nodeState 是一个节点当前应有的配置和用户。
type nodeState struct {
	node      db.Node
	spec      nodeapi.Node
	specErr   error
	configRev string
	users     []nodeapi.User
	usersRev  string
}

// machineState 计算一台机器上所有原生节点的当前状态。
func (h *Handler) machineState(ctx context.Context, host string) ([]nodeState, error) {
	nodes, err := h.st.ListNativeNodesOfHost(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]nodeState, 0, len(nodes))
	for _, n := range nodes {
		ns := nodeState{node: n}
		ns.spec, ns.specErr = BuildNode(n)
		if ns.specErr != nil {
			out = append(out, ns)
			continue
		}
		ns.configRev = nodeapi.Rev(ns.spec)
		rows, err := h.st.AvailableUsersForNode(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		ns.users = make([]nodeapi.User, 0, len(rows))
		for _, r := range rows {
			ns.users = append(ns.users, nodeapi.User{
				ID: r.ID, UUID: r.Uuid, Password: UserCredential(ns.spec, r.Uuid),
				SpeedLimit: int(r.SpeedLimit), DeviceLimit: int(r.DeviceLimit),
			})
		}
		ns.usersRev = nodeapi.UsersRev(ns.users)
		out = append(out, ns)
	}
	return out, nil
}

// fingerprint 概括一台机器的状态，用于判断要不要推送。
func fingerprint(states []nodeState) string {
	parts := make([]string, 0, len(states))
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%d:%s:%s:%v", s.node.ID, s.configRev, s.usersRev, s.specErr))
	}
	return strings.Join(parts, ",")
}

// Sync 返回有变化的节点配置和用户。用户列表能算出增量时只发增量。
func (h *Handler) Sync(c *gin.Context) {
	m := machineOf(c)
	var req nodeapi.SyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, apierr.BadRequest("invalid_json", "请求体不是合法 JSON：%v", err))
		return
	}
	_ = h.st.TouchMachine(c, db.TouchMachineParams{ID: m.ID, AgentVersion: truncate(req.Version, 64), Kernel: truncate(req.Kernel, 64)})
	states, err := h.machineState(c, m.Host)
	if err != nil {
		fail(c, err)
		return
	}
	resp := nodeapi.SyncResponse{
		Machine: m.Host, Nodes: []nodeapi.NodeSync{}, Removed: []int64{},
		Settings: nodeapi.Settings{
			ReportInterval: h.cfg.ReportInterval, SyncInterval: h.cfg.SyncInterval, DeviceTTL: int(h.cfg.DeviceTTL.Seconds()),
		},
	}
	current := map[int64]bool{}
	limited := []int64{}
	for _, s := range states {
		if s.specErr != nil {
			h.setRuntimeError(c, s.node, s.specErr)
			continue
		}
		current[s.node.ID] = true
		for _, u := range s.users {
			if u.DeviceLimit > 0 {
				limited = append(limited, u.ID)
			}
		}
		h.users.put(s.node.ID, s.usersRev, s.users)
		known := req.Nodes[s.node.ID]
		ns := nodeapi.NodeSync{ID: s.node.ID, ConfigRev: s.configRev, UsersRev: s.usersRev}
		changed := false
		if known.ConfigRev != s.configRev {
			spec := s.spec
			ns.Config = &spec
			changed = true
		}
		if known.UsersRev != s.usersRev {
			changed = true
			if prev, ok := h.users.get(s.node.ID, known.UsersRev); ok && known.ConfigRev == s.configRev {
				ns.Delta = diffUsers(prev, s.users)
			} else {
				ns.Users, ns.FullUsers = s.users, true
			}
		}
		if changed {
			resp.Nodes = append(resp.Nodes, ns)
		}
	}
	for id := range req.Nodes {
		if !current[id] {
			resp.Removed = append(resp.Removed, id)
		}
	}
	if len(limited) > 0 {
		rows, err := h.st.RemoteDeviceCounts(c, db.RemoteDeviceCountsParams{
			Since: time.Now().Add(-h.cfg.DeviceTTL), Host: m.Host, UserIds: limited,
		})
		if err == nil && len(rows) > 0 {
			resp.Remote = map[int64]int{}
			for _, r := range rows {
				resp.Remote[r.UserID] = int(r.Devices)
			}
		}
	}
	metrics.NodeRequests.WithLabelValues("native_sync", "ok").Inc()
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) setRuntimeError(ctx context.Context, n db.Node, err error) {
	b, _ := json.Marshal(nodeapi.NodeStatus{Running: false, Error: "配置无法由 patato-node 承载：" + err.Error(), AppliedAt: time.Now()})
	if string(b) != string(n.Runtime) {
		_ = h.st.SetNodeRuntime(ctx, db.SetNodeRuntimeParams{ID: n.ID, Runtime: b})
	}
}

func diffUsers(prev, cur []nodeapi.User) *nodeapi.UserDelta {
	old := make(map[int64]nodeapi.User, len(prev))
	for _, u := range prev {
		old[u.ID] = u
	}
	d := &nodeapi.UserDelta{}
	for _, u := range cur {
		if o, ok := old[u.ID]; !ok || o != u {
			d.Upsert = append(d.Upsert, u)
		}
		delete(old, u.ID)
	}
	for id := range old {
		d.Remove = append(d.Remove, id)
	}
	return d
}

// Report 接收一批上报。序号去重和流量落库在同一个事务里：这一批要么完整入账一次，要么没有入账。
func (h *Handler) Report(c *gin.Context) {
	m := machineOf(c)
	var req nodeapi.ReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, apierr.BadRequest("invalid_json", "请求体不是合法 JSON：%v", err))
		return
	}
	if req.Spool == "" || len(req.Spool) > 64 || req.Seq <= 0 {
		fail(c, apierr.BadRequest("invalid_params", "spool 与 seq 必填，seq 从 1 开始"))
		return
	}
	nodes, err := h.st.ListNativeNodesOfHost(c, m.Host)
	if err != nil {
		fail(c, err)
		return
	}
	byID := make(map[int64]db.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	var accepted bool
	stored := 0
	err = h.st.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		var err error
		accepted, err = q.AdvanceMachineSpool(c, db.AdvanceMachineSpoolParams{MachineID: m.ID, Spool: req.Spool, LastSeq: req.Seq})
		if err != nil || !accepted {
			return err
		}
		for _, r := range req.Nodes {
			if len(r.Traffic) == 0 {
				continue
			}
			n, ok := byID[r.ID]
			if !ok {
				// 节点刚从这台机器移走（换了后端或机器）：流量仍按节点入账，节点已删除则丢弃
				if n, err = q.GetNode(c, r.ID); store.IsNotFound(err) {
					continue
				} else if err != nil {
					return err
				}
			}
			data := make(map[string][2]float64, len(r.Traffic))
			for uid, v := range r.Traffic {
				if v[0] < 0 || v[1] < 0 || (v[0] == 0 && v[1] == 0) {
					continue
				}
				data[strconv.FormatInt(uid, 10)] = [2]float64{float64(v[0]), float64(v[1])}
			}
			if len(data) == 0 {
				continue
			}
			payload, _ := json.Marshal(data)
			if err := q.InsertTrafficReport(c, db.InsertTrafficReportParams{NodeID: n.ID, Rate: n.Rate, Payload: payload}); err != nil {
				return err
			}
			stored++
		}
		return nil
	})
	if err != nil {
		h.log.Error("store native report", "machine", m.Host, "err", err)
		fail(c, err)
		return
	}
	_ = h.st.SeenMachine(c, m.ID)
	if !accepted {
		metrics.NodeRequests.WithLabelValues("native_report", "duplicate").Inc()
		c.JSON(http.StatusOK, nodeapi.ReportResponse{Ack: req.Seq, Duplicate: true})
		return
	}
	metrics.TrafficReports.Add(float64(stored))
	for _, r := range req.Nodes {
		n, ok := byID[r.ID]
		if !ok {
			continue
		}
		for uid, ips := range r.Alive {
			if err := h.st.ReplaceNodeAlive(c, db.ReplaceNodeAliveParams{NodeID: n.ID, UserID: uid, Ips: limitIPs(ips)}); err != nil {
				h.log.Error("store alive", "node", n.ID, "err", err)
			}
		}
		if len(r.Traffic) > 0 {
			_ = h.st.TouchNodePush(c, n.ID)
		}
		if r.Status != nil {
			b, _ := json.Marshal(r.Status)
			_ = h.st.SetNodeRuntime(c, db.SetNodeRuntimeParams{ID: n.ID, Runtime: b})
			// 只有内核里真的跑起来了才算心跳：启动失败的节点保持待接入或转为离线，不会下发到订阅。
			if r.Status.Running {
				uniproxy.Heartbeat(c, h.st, h.events, h.log, n)
			}
		}
	}
	metrics.NodeRequests.WithLabelValues("native_report", "ok").Inc()
	c.JSON(http.StatusOK, nodeapi.ReportResponse{Ack: req.Seq})
}

func limitIPs(ips []string) []string {
	if len(ips) > 64 {
		ips = ips[:64]
	}
	return ips
}

func fail(c *gin.Context, err error) {
	e := apierr.From(err)
	c.AbortWithStatusJSON(e.Status, gin.H{"error": e})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// userCache 记住每个节点最近几个版本的用户列表，用来计算增量。缓存不命中时发全量，多实例部署也正确。
type userCache struct {
	mu sync.Mutex
	m  map[int64][]cachedUsers
}

type cachedUsers struct {
	rev   string
	users []nodeapi.User
}

const userCacheDepth = 4

func newUserCache() *userCache { return &userCache{m: map[int64][]cachedUsers{}} }

func (c *userCache) put(node int64, rev string, users []nodeapi.User) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.m[node]
	for _, e := range list {
		if e.rev == rev {
			return
		}
	}
	list = append(list, cachedUsers{rev: rev, users: users})
	if len(list) > userCacheDepth {
		list = list[len(list)-userCacheDepth:]
	}
	c.m[node] = list
}

func (c *userCache) get(node int64, rev string) ([]nodeapi.User, bool) {
	if rev == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.m[node] {
		if e.rev == rev {
			return e.users, true
		}
	}
	return nil, false
}
