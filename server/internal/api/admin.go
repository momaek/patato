package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// mutate 是所有写接口的入口：请求体就是操作参数；dry_run 为 true 时只预演，
// instruction 记录用户的原始指令（显示在审批卡片上）。
func (s *Server) mutate(kind string, extra func(c *gin.Context) map[string]any) gin.HandlerFunc {
	return func(c *gin.Context) {
		body := map[string]json.RawMessage{}
		if err := bindJSON(c, &body); err != nil {
			fail(c, err)
			return
		}
		dry := c.Query("dry_run") == "true" || c.Query("dry_run") == "1"
		if raw, ok := body["dry_run"]; ok {
			var b bool
			_ = json.Unmarshal(raw, &b)
			dry = dry || b
			delete(body, "dry_run")
		}
		var instruction string
		if raw, ok := body["instruction"]; ok {
			_ = json.Unmarshal(raw, &instruction)
			delete(body, "instruction")
		}
		if extra != nil {
			for k, v := range extra(c) {
				b, _ := json.Marshal(v)
				body[k] = b
			}
		}
		params, _ := json.Marshal(body)
		actor := getPrincipal(c).actor()
		actor.Instruction = truncate(instruction, 500)
		if dry {
			p, err := s.d.Engine.Preview(c, actor, kind, params)
			if err != nil {
				fail(c, err)
				return
			}
			ok(c, p)
			return
		}
		p, err := s.d.Engine.Execute(c, actor, kind, params)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(change.StatusCode(p), p)
	}
}

func pathRef(name string) func(c *gin.Context) map[string]any {
	return func(c *gin.Context) map[string]any { return map[string]any{name: c.Param("id")} }
}

func (s *Server) changeGet(c *gin.Context) {
	p, err := s.d.Engine.Get(c, c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, p)
}

func (s *Server) changeApply(c *gin.Context) {
	p, err := s.d.Engine.Apply(c, getPrincipal(c).actor(), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(change.StatusCode(p), p)
}

// ---- 视图 ---------------------------------------------------------------

type ref struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type nodeView struct {
	ID              int64           `json:"id"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	Region          string          `json:"region"`
	Host            string          `json:"host"`
	Address         string          `json:"address"`
	Protocol        string          `json:"protocol"`
	Port            int32           `json:"port"`
	State           string          `json:"state"`
	InSubscription  bool            `json:"in_subscription"`
	Rate            float64         `json:"rate"`
	Tags            []string        `json:"tags"`
	Plans           []ref           `json:"plans"`
	OnlineUsers     int32           `json:"online_users"`
	TodayBytes      int64           `json:"today_bytes"`
	LastHeartbeatAt *time.Time      `json:"last_heartbeat_at"`
	LastPushAt      *time.Time      `json:"last_push_at"`
	Load            json.RawMessage `json:"load,omitempty"`
	Settings        map[string]any  `json:"settings,omitempty"`
	LegacyToken     bool            `json:"allow_legacy_token"`
	Version         int64           `json:"version"`
	CreatedAt       time.Time       `json:"created_at"`
}

// redact 去掉设置里的私钥和密码；它们只通过节点接口下发给节点后端。
func redact(raw json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, x := range t {
				lk := strings.ToLower(k)
				if lk == "private_key" || lk == "server_key" || strings.Contains(lk, "password") {
					if s, _ := x.(string); s != "" {
						t[k] = "***"
					}
					continue
				}
				t[k] = walk(x)
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		}
		return v
	}
	walk(m)
	return m
}

func (s *Server) nodeViews(c *gin.Context, nodes []db.Node, detail bool) ([]nodeView, error) {
	pairs, err := s.d.Store.ListPlanNodePairs(c)
	if err != nil {
		return nil, err
	}
	plansByNode := map[int64][]ref{}
	for _, p := range pairs {
		plansByNode[p.NodeID] = append(plansByNode[p.NodeID], ref{ID: p.PlanID, Code: p.PlanCode, Name: p.PlanName})
	}
	online := map[int64]int32{}
	if rows, err := s.d.Store.CountOnlineUsersByNode(c, time.Now().Add(-s.d.Cfg.Node.DeviceTTL.D())); err == nil {
		for _, r := range rows {
			online[r.NodeID] = r.Users
		}
	}
	today := map[int64]int64{}
	if rows, err := s.d.Store.NodeTrafficOnDay(c, s.today()); err == nil {
		for _, r := range rows {
			today[r.NodeID] = r.Up + r.Down
		}
	}
	out := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		v := nodeView{
			ID: n.ID, Code: n.Code, Name: n.Name, Region: n.Region, Host: n.Host, Address: n.Address, Protocol: n.Protocol,
			Port: n.Port, State: n.State, InSubscription: n.State != "pending", Rate: n.Rate, Tags: n.Tags,
			Plans: nonNilRefs(plansByNode[n.ID]), OnlineUsers: online[n.ID], TodayBytes: today[n.ID],
			LastHeartbeatAt: n.LastHeartbeatAt, LastPushAt: n.LastPushAt, LegacyToken: n.AllowLegacyToken,
			Version: n.Version, CreatedAt: n.CreatedAt,
		}
		if detail {
			v.Load = n.Load
			v.Settings = redact(n.Settings)
		}
		out = append(out, v)
	}
	return out, nil
}

func nonNilRefs(r []ref) []ref {
	if r == nil {
		return []ref{}
	}
	return r
}

func (s *Server) today() time.Time {
	t := time.Now().In(s.loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Server) nodeList(c *gin.Context) {
	nodes, err := s.d.Store.ListNodes(c)
	if err != nil {
		fail(c, err)
		return
	}
	if st := c.Query("state"); st != "" {
		filtered := nodes[:0]
		for _, n := range nodes {
			if n.State == st {
				filtered = append(filtered, n)
			}
		}
		nodes = filtered
	}
	views, err := s.nodeViews(c, nodes, false)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"items": views})
}

func (s *Server) findNode(c *gin.Context) (db.Node, bool) {
	key := c.Param("id")
	var n db.Node
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		n, err = s.d.Store.GetNode(c, id)
	} else {
		n, err = s.d.Store.GetNodeByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("node_not_found", "节点 %s 不存在", key).WithHint("用 GET /api/v1/nodes 查看全部节点"))
		return n, false
	}
	if err != nil {
		fail(c, err)
		return n, false
	}
	return n, true
}

func (s *Server) nodeGet(c *gin.Context) {
	n, found := s.findNode(c)
	if !found {
		return
	}
	views, err := s.nodeViews(c, []db.Node{n}, true)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, views[0])
}

// nodeInstallCmd 生成在节点机器上安装并配置 V2bX 的命令。命令里含节点密钥，所以要求可写权限。
func (s *Server) nodeInstallCmd(c *gin.Context) {
	n, found := s.findNode(c)
	if !found {
		return
	}
	if len(n.KeyCipher) == 0 {
		fail(c, apierr.Conflict("no_node_key", "节点 %s 没有密钥", n.Code).WithHint("用 POST /api/v1/nodes/%s:rotate-key 生成", n.Code))
		return
	}
	key, err := s.d.Box.Open(n.KeyCipher)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, V2bXInstall(s.d.Cfg.PublicURL, n, string(key)))
}

// ---- 模板 / 套餐 --------------------------------------------------------

type templateView struct {
	ID       int64          `json:"id"`
	Code     string         `json:"code"`
	Name     string         `json:"name"`
	Region   string         `json:"region"`
	Protocol string         `json:"protocol"`
	PortMin  int32          `json:"port_min"`
	PortMax  int32          `json:"port_max"`
	Rate     float64        `json:"rate"`
	Settings map[string]any `json:"settings"`
	Version  int64          `json:"version"`
}

func templateViewOf(t db.NodeTemplate) templateView {
	return templateView{ID: t.ID, Code: t.Code, Name: t.Name, Region: t.Region, Protocol: t.Protocol,
		PortMin: t.PortMin, PortMax: t.PortMax, Rate: t.Rate, Settings: redact(t.Settings), Version: t.Version}
}

func (s *Server) templateList(c *gin.Context) {
	ts, err := s.d.Store.ListTemplates(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]templateView, 0, len(ts))
	for _, t := range ts {
		items = append(items, templateViewOf(t))
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) templateGet(c *gin.Context) {
	key := c.Param("id")
	var t db.NodeTemplate
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		t, err = s.d.Store.GetTemplate(c, id)
	} else {
		t, err = s.d.Store.GetTemplateByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("template_not_found", "模板 %s 不存在", key))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, templateViewOf(t))
}

type planView struct {
	ID             int64            `json:"id"`
	Code           string           `json:"code"`
	Name           string           `json:"name"`
	TransferGB     float64          `json:"transfer_gb"`
	ResetMethod    string           `json:"reset_method"`
	DeviceLimit    int32            `json:"device_limit"`
	SpeedLimitMbps int32            `json:"speed_limit_mbps"`
	Prices         map[string]int64 `json:"prices"`
	Sellable       bool             `json:"sellable"`
	UserCount      int64            `json:"user_count"`
	NodeCount      int64            `json:"node_count"`
	Nodes          []ref            `json:"nodes,omitempty"`
	Version        int64            `json:"version"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

func planViewOf(p db.Plan, users, nodes int64) planView {
	v := planView{ID: p.ID, Code: p.Code, Name: p.Name, TransferGB: billing.ToGB(p.TransferBytes), ResetMethod: p.ResetMethod,
		DeviceLimit: p.DeviceLimit, SpeedLimitMbps: p.SpeedLimitMbps, Sellable: p.Sellable, UserCount: users, NodeCount: nodes,
		Version: p.Version, UpdatedAt: p.UpdatedAt, Prices: map[string]int64{}}
	_ = json.Unmarshal(p.Prices, &v.Prices)
	return v
}

func (s *Server) planList(c *gin.Context) {
	rows, err := s.d.Store.ListPlans(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]planView, 0, len(rows))
	for _, r := range rows {
		p := db.Plan{ID: r.ID, Code: r.Code, Name: r.Name, TransferBytes: r.TransferBytes, ResetMethod: r.ResetMethod,
			DeviceLimit: r.DeviceLimit, SpeedLimitMbps: r.SpeedLimitMbps, Prices: r.Prices, Sellable: r.Sellable,
			Version: r.Version, UpdatedAt: r.UpdatedAt}
		items = append(items, planViewOf(p, r.UserCount, r.NodeCount))
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) planGet(c *gin.Context) {
	key := c.Param("id")
	var p db.Plan
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		p, err = s.d.Store.GetPlan(c, id)
	} else {
		p, err = s.d.Store.GetPlanByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("plan_not_found", "套餐 %s 不存在", key).WithHint("用 GET /api/v1/plans 查看全部"))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	users, _ := s.d.Store.CountUsersInPlan(c, &p.ID)
	nodes, err := s.d.Store.ListPlanNodes(c, p.ID)
	if err != nil {
		fail(c, err)
		return
	}
	v := planViewOf(p, users, int64(len(nodes)))
	v.Nodes = []ref{}
	for _, n := range nodes {
		v.Nodes = append(v.Nodes, ref{ID: n.ID, Code: n.Code, Name: n.Name})
	}
	ok(c, v)
}

// ---- 用户 ---------------------------------------------------------------

type userView struct {
	ID             int64      `json:"id"`
	Email          string     `json:"email"`
	Plan           *ref       `json:"plan"`
	State          string     `json:"state"`
	TransferBytes  int64      `json:"transfer_bytes"`
	UsedBytes      int64      `json:"used_bytes"`
	UsedUp         int64      `json:"used_up"`
	UsedDown       int64      `json:"used_down"`
	ExpiresAt      *time.Time `json:"expires_at"`
	NextResetAt    *time.Time `json:"next_reset_at"`
	DeviceLimit    *int32     `json:"device_limit"`
	SpeedLimitMbps *int32     `json:"speed_limit_mbps"`
	DevicesOnline  int32      `json:"devices_online"`
	TokenResetAt   *time.Time `json:"token_reset_at"`
	LastActiveAt   *time.Time `json:"last_active_at"`
	CreatedAt      time.Time  `json:"created_at"`
	Version        int64      `json:"version"`
}

func userState(u db.User, now time.Time) string {
	switch {
	case u.Banned:
		return "banned"
	case u.ExpiresAt != nil && !u.ExpiresAt.After(now):
		return "expired"
	case u.UsedUp+u.UsedDown >= u.TransferBytes:
		return "over_quota"
	}
	return "active"
}

func userViewOf(u db.User, plan *ref) userView {
	return userView{ID: u.ID, Email: u.Email, Plan: plan, State: userState(u, time.Now()), TransferBytes: u.TransferBytes,
		UsedBytes: u.UsedUp + u.UsedDown, UsedUp: u.UsedUp, UsedDown: u.UsedDown, ExpiresAt: u.ExpiresAt, NextResetAt: u.NextResetAt,
		DeviceLimit: u.DeviceLimit, SpeedLimitMbps: u.SpeedLimitMbps, TokenResetAt: u.TokenResetAt, LastActiveAt: u.LastActiveAt,
		CreatedAt: u.CreatedAt, Version: u.Version}
}

func (s *Server) userList(c *gin.Context) {
	limit := queryInt(c, "limit", 50, 1, 200)
	params := db.ListUsersParams{AfterID: int64(queryInt(c, "cursor", 0, 0, 1<<62)), RowLimit: int32(limit + 1)}
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		params.Q = &q
	}
	if st := c.Query("state"); st != "" {
		params.State = &st
	}
	if d := queryInt(c, "expiring_within_days", 0, 0, 3650); d > 0 {
		t := time.Now().AddDate(0, 0, d)
		params.ExpiringBefore = &t
	}
	if p := c.Query("plan"); p != "" {
		if id, err := strconv.ParseInt(p, 10, 64); err == nil {
			params.PlanID = &id
		} else if pl, err := s.d.Store.GetPlanByCode(c, p); err == nil {
			params.PlanID = &pl.ID
		}
	}
	rows, err := s.d.Store.ListUsers(c, params)
	if err != nil {
		fail(c, err)
		return
	}
	var next *int64
	if len(rows) > limit {
		rows = rows[:limit]
		id := rows[len(rows)-1].ID
		next = &id
	}
	items := make([]userView, 0, len(rows))
	for _, r := range rows {
		u := db.User{ID: r.ID, Email: r.Email, PlanID: r.PlanID, Banned: r.Banned, TransferBytes: r.TransferBytes,
			UsedUp: r.UsedUp, UsedDown: r.UsedDown, ExpiresAt: r.ExpiresAt, NextResetAt: r.NextResetAt, DeviceLimit: r.DeviceLimit,
			SpeedLimitMbps: r.SpeedLimitMbps, TokenResetAt: r.TokenResetAt, LastActiveAt: r.LastActiveAt, CreatedAt: r.CreatedAt, Version: r.Version}
		var plan *ref
		if r.PlanID != nil && r.PlanCode != nil {
			plan = &ref{ID: *r.PlanID, Code: *r.PlanCode, Name: deref(r.PlanName)}
		}
		items = append(items, userViewOf(u, plan))
	}
	ok(c, gin.H{"items": items, "next_cursor": next})
}

func (s *Server) userGet(c *gin.Context) {
	key := c.Param("id")
	var u db.User
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		u, err = s.d.Store.GetUser(c, id)
	} else {
		u, err = s.d.Store.GetUserByEmail(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("user_not_found", "用户 %s 不存在", key).WithHint("用 GET /api/v1/users?q=<邮箱> 搜索"))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	v := userViewOf(u, s.planRef(c, u.PlanID))
	v.DevicesOnline, _ = s.d.Store.UserDeviceCount(c, db.UserDeviceCountParams{UserID: u.ID, UpdatedAt: time.Now().Add(-s.d.Cfg.Node.DeviceTTL.D())})
	target := fmt.Sprintf("user:%d", u.ID)
	recent, _ := s.d.Store.ListAudit(c, db.ListAuditParams{Target: &target, RowLimit: 5})
	daily, _ := s.d.Store.UserTrafficSince(c, db.UserTrafficSinceParams{UserID: u.ID, Day: s.today().AddDate(0, 0, -30)})
	ok(c, gin.H{"user": v, "recent_audit": auditViews(recent), "daily_traffic": daily})
}

func (s *Server) planRef(c *gin.Context, id *int64) *ref {
	if id == nil {
		return nil
	}
	p, err := s.d.Store.GetPlan(c, *id)
	if err != nil {
		return nil
	}
	return &ref{ID: p.ID, Code: p.Code, Name: p.Name}
}

// ---- 审批 ---------------------------------------------------------------

func (s *Server) approvalList(c *gin.Context) {
	params := db.ListApprovalsParams{Limit: int32(queryInt(c, "limit", 50, 1, 200))}
	if st := c.Query("status"); st != "" {
		params.Status = &st
	}
	rows, err := s.d.Store.ListApprovals(c, params)
	if err != nil {
		fail(c, err)
		return
	}
	type item struct {
		ID         string          `json:"approval_id"`
		ChangeID   string          `json:"change_id"`
		Status     string          `json:"status"`
		Summary    string          `json:"summary"`
		Kind       string          `json:"kind"`
		Risk       string          `json:"risk"`
		RiskReason string          `json:"risk_reason"`
		Impact     json.RawMessage `json:"impact"`
		Requester  string          `json:"requester"`
		ActorClaim string          `json:"actor_claim"`
		ExpiresAt  time.Time       `json:"expires_at"`
		CreatedAt  time.Time       `json:"created_at"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		st := r.Status
		if st == "pending" && time.Now().After(r.ExpiresAt) {
			st = "expired"
		}
		items = append(items, item{ID: r.ID, ChangeID: r.ChangeID, Status: st, Summary: r.Summary, Kind: r.Kind, Risk: r.Risk,
			RiskReason: r.RiskReason, Impact: r.Impact, Requester: r.SubjectLabel, ActorClaim: r.ActorClaim,
			ExpiresAt: r.ExpiresAt, CreatedAt: r.CreatedAt})
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) approvalGet(c *gin.Context) {
	d, err := s.d.Approvals.Get(c, c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, d)
}

func approvePurpose(id string) string { return "approve:" + id }

func (s *Server) approveBegin(c *gin.Context) {
	id := c.Param("id")
	if _, err := s.d.Approvals.Get(c, id); err != nil {
		fail(c, err)
		return
	}
	opts, challengeID, err := s.d.Auth.BeginStepUp(c, getPrincipal(c).operator, approvePurpose(id))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"challenge_id": challengeID, "options": opts})
}

func (s *Server) approve(c *gin.Context) {
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
		Via         string          `json:"via"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	id := c.Param("id")
	op := getPrincipal(c).operator
	if err := s.d.Auth.FinishStepUp(c, op, approvePurpose(id), req.ChallengeID, req.Credential); err != nil {
		fail(c, err)
		return
	}
	via := "web"
	if req.Via == "cli_passkey" {
		via = "cli_passkey"
	}
	d, err := s.d.Approvals.Approve(c, id, op, via)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, d)
}

func (s *Server) reject(c *gin.Context) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	d, err := s.d.Approvals.Reject(c, c.Param("id"), getPrincipal(c).operator, "web", truncate(req.Reason, 500))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, d)
}

// ---- 审计、事件、用量 ---------------------------------------------------

type auditView struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	Subject    string          `json:"subject"`
	ActorClaim string          `json:"actor_claim"`
	Entry      string          `json:"entry"`
	Action     string          `json:"action"`
	Target     string          `json:"target"`
	Risk       string          `json:"risk"`
	ChangeID   string          `json:"change_id"`
	Result     string          `json:"result"`
	Detail     json.RawMessage `json:"detail"`
}

func auditViews(rows []db.AuditLog) []auditView {
	out := make([]auditView, 0, len(rows))
	for _, r := range rows {
		out = append(out, auditView{ID: r.ID, At: r.At, Subject: r.SubjectLabel, ActorClaim: r.ActorClaim, Entry: r.Entry,
			Action: r.Action, Target: r.Target, Risk: r.Risk, ChangeID: r.ChangeID, Result: r.Result, Detail: r.Detail})
	}
	return out
}

func (s *Server) auditList(c *gin.Context) {
	limit := queryInt(c, "limit", 50, 1, 200)
	p := db.ListAuditParams{RowLimit: int32(limit + 1)}
	opt := func(key string) *string {
		if v := strings.TrimSpace(c.Query(key)); v != "" {
			return &v
		}
		return nil
	}
	p.Q, p.Entry, p.Result, p.Target = opt("q"), opt("entry"), opt("result"), opt("target")
	if cur := queryInt(c, "cursor", 0, 0, 1<<62); cur > 0 {
		v := int64(cur)
		p.BeforeID = &v
	}
	rows, err := s.d.Store.ListAudit(c, p)
	if err != nil {
		fail(c, err)
		return
	}
	var next *int64
	if len(rows) > limit {
		rows = rows[:limit]
		id := rows[len(rows)-1].ID
		next = &id
	}
	ok(c, gin.H{"items": auditViews(rows), "next_cursor": next})
}

type eventView struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	Severity   string          `json:"severity"`
	Subject    string          `json:"subject"`
	Message    string          `json:"message"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
	ResolvedAt *time.Time      `json:"resolved_at"`
}

func eventViewOf(e db.Event) eventView {
	return eventView{ID: e.ID, Kind: e.Kind, Severity: e.Severity, Subject: e.Subject, Message: e.Message,
		Payload: e.Payload, CreatedAt: e.CreatedAt, ResolvedAt: e.ResolvedAt}
}

// events 返回事件；Accept: text/event-stream 时持续推送新事件（patato events watch）。
func (s *Server) events(c *gin.Context) {
	after := int64(queryInt(c, "after", 0, 0, 1<<62))
	if !strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		rows, err := s.d.Store.ListEventsAfter(c, db.ListEventsAfterParams{ID: after, Limit: int32(queryInt(c, "limit", 100, 1, 500))})
		if err != nil {
			fail(c, err)
			return
		}
		items := make([]eventView, 0, len(rows))
		for _, r := range rows {
			items = append(items, eventViewOf(r))
		}
		ok(c, gin.H{"items": items})
		return
	}
	if after == 0 {
		after, _ = s.d.Store.LatestEventID(c)
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	c.Status(http.StatusOK)
	c.Writer.Flush()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(c.Writer, ": keepalive\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		case <-tick.C:
			rows, err := s.d.Store.ListEventsAfter(c, db.ListEventsAfterParams{ID: after, Limit: 100})
			if err != nil {
				return
			}
			for _, r := range rows {
				b, _ := json.Marshal(eventViewOf(r))
				if _, err := fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", r.ID, r.Kind, b); err != nil {
					return
				}
				after = r.ID
			}
			c.Writer.Flush()
		}
	}
}

func (s *Server) usage(c *gin.Context) {
	day := s.today()
	if d := c.Query("day"); d != "" {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			fail(c, apierr.BadRequest("invalid_day", "day 格式应为 YYYY-MM-DD"))
			return
		}
		day = t
	}
	total, err := s.d.Store.TotalTrafficOnDay(c, day)
	if err != nil {
		fail(c, err)
		return
	}
	perNode, _ := s.d.Store.NodeTrafficOnDay(c, day)
	online, _ := s.d.Store.CountOnlineUsers(c, time.Now().Add(-s.d.Cfg.Node.DeviceTTL.D()))
	ok(c, gin.H{"day": day.Format("2006-01-02"), "total_bytes": total, "online_users": online, "nodes": perNode})
}

func queryInt(c *gin.Context, key string, def, min, max int) int {
	v, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
