package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/approval"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/testutil"
)

type harness struct {
	t      *testing.T
	a      *App
	token  string
	op     db.Operator
	cookie string
}

func newHarness(t *testing.T, mutate func(*config.Config)) *harness {
	t.Helper()
	st := testutil.Store(t)
	dsn := st.Pool.Config().ConnString()
	st.Close()
	cfg := config.Default()
	cfg.DatabaseURL = dsn
	cfg.SecretKey = strings.Repeat("k", 40)
	cfg.PublicURL = "http://patato.test"
	cfg.Session.Secure = false
	if mutate != nil {
		mutate(cfg)
	}
	ctx := context.Background()
	a, err := New(ctx, cfg, testutil.Logger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Store.Close)
	op, err := a.Store.CreateOperator(ctx, db.CreateOperatorParams{Name: "运营者", WebauthnUserID: []byte("op-1")})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := a.Auth.IssueToken(ctx, op.ID, "test", "write")
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, a: a, token: token, op: op}
}

type resp struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (r resp) str(path string) string { return fmt.Sprint(dig(r.body, path)) }

func dig(v any, path string) any {
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func (h *harness) do(method, path string, body any, headers map[string]string) resp {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.a.HTTP.ServeHTTP(w, req)
	r := resp{status: w.Code, header: w.Header(), raw: w.Body.Bytes()}
	_ = json.Unmarshal(r.raw, &r.body)
	return r
}

// api 以 CLI token 身份调用管理接口，写请求自动带新的 Idempotency-Key。
func (h *harness) api(method, path string, body any) resp {
	hdr := map[string]string{"Authorization": "Bearer " + h.token, "X-Actor": "Claude Code 会话"}
	if method != http.MethodGet {
		hdr["Idempotency-Key"] = secret.ID("idem")
	}
	return h.do(method, path, body, hdr)
}

func expect(t *testing.T, r resp, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status %d, want %d: %s", r.status, status, r.raw)
	}
}

func setup(h *harness) (nodeID string, nodeKey string, userIDs []string, subTokens []string) {
	t := h.t
	ctx := context.Background()
	expect(t, h.api("POST", "/api/v1/templates", map[string]any{
		"code": "jp-premium", "name": "日本高级", "region": "东京", "protocol": "vless", "port_min": 24000, "port_max": 24100,
		"settings": map[string]any{"tls": 2, "flow": "xtls-rprx-vision", "network": "tcp",
			"reality_settings": map[string]any{"server_name": "www.apple.com", "server_port": 443}},
	}), 200)
	expect(t, h.api("POST", "/api/v1/plans", map[string]any{"code": "premium", "name": "高级", "transfer_gb": 300, "device_limit": 3}), 200)
	for i := 1; i <= 3; i++ {
		r := h.api("POST", "/api/v1/users", map[string]any{"email": fmt.Sprintf("u%d@example.com", i), "plan": "premium",
			"password": "password-123", "days": 30})
		expect(t, r, 200)
		userIDs = append(userIDs, r.str("result.user_id"))
		u, err := h.a.Store.GetUserByEmail(ctx, fmt.Sprintf("u%d@example.com", i))
		if err != nil {
			t.Fatal(err)
		}
		subTokens = append(subTokens, u.SubToken)
	}
	return
}

func TestEndToEnd(t *testing.T) {
	// 阈值调到 2：上节点影响 3 名用户，应因影响面升为敏感
	h := newHarness(t, func(c *config.Config) { c.Risk.UserThreshold = 2 })
	ctx := context.Background()
	_, _, userIDs, subTokens := setup(h)

	// 预演：返回计划、影响面和升级原因，不改动数据
	r := h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "203.0.113.10", "host": "tokyo-03", "plans": []string{"premium"},
		"dry_run": true, "instruction": "把刚上线的东京机器加到高级套餐",
	})
	expect(t, r, 200)
	if r.str("status") != "planned" || r.str("risk") != "sensitive" || r.str("declared_risk") != "normal" || r.str("impact.users") != "3" {
		t.Fatalf("unexpected plan: %s", r.raw)
	}
	if !strings.Contains(r.str("risk_reason"), "阈值") {
		t.Fatalf("missing escalation reason: %s", r.raw)
	}
	changeID := r.str("change_id")
	nodes, _ := h.a.Store.ListNodes(ctx)
	if len(nodes) != 0 {
		t.Fatal("dry run must not create nodes")
	}

	// 执行：敏感计划转为审批，返回 202
	r = h.api("POST", "/api/v1/changes/"+changeID+":apply", nil)
	expect(t, r, 202)
	approvalID := r.str("approval_id")
	if approvalID == "" || r.str("status") != "pending_approval" {
		t.Fatalf("expected approval: %s", r.raw)
	}

	// CLI / Agent token 不能批准
	r = h.api("POST", "/api/v1/approvals/"+approvalID+":approve", map[string]any{})
	expect(t, r, 403)
	if r.str("error.code") != "operator_session_required" {
		t.Fatalf("unexpected error: %s", r.raw)
	}

	// 审批详情带原始指令
	r = h.api("GET", "/api/v1/approvals/"+approvalID, nil)
	expect(t, r, 200)
	if r.str("instruction") != "把刚上线的东京机器加到高级套餐" || r.str("requester.actor_claim") != "Claude Code 会话" {
		t.Fatalf("approval detail: %s", r.raw)
	}

	// 总览里出现待审批
	r = h.api("GET", "/api/v1/status", nil)
	expect(t, r, 200)
	if todos, _ := r.body["todo"].([]any); len(todos) != 1 {
		t.Fatalf("status todo: %s", r.raw)
	}

	// 运营者批准（网页 passkey 校验之后的那一步）
	d, err := h.a.Approvals.Approve(ctx, approvalID, h.op, approval.ViaWeb, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != "approved" || d.Change.Status != "applied" {
		t.Fatalf("approve: %+v", d)
	}
	node, err := h.a.Store.GetNodeByCode(ctx, "tokyo-03")
	if err != nil {
		t.Fatal(err)
	}
	if node.State != "pending" || node.Port != 24000 || node.Name != "东京-01" {
		t.Fatalf("node: %+v", node)
	}
	nodeID := fmt.Sprint(node.ID)

	// 待接入的节点不下发到订阅
	sub := h.do("GET", "/api/v1/client/subscribe?token="+subTokens[0], nil, map[string]string{"User-Agent": "clash-verge/v1.7"})
	expect(t, sub, 200)
	if strings.Contains(string(sub.raw), "东京-01") {
		t.Fatal("pending node must not be in subscription")
	}

	// 取安装命令里的节点密钥
	r = h.api("GET", "/api/v1/nodes/"+nodeID+"/install-cmd", nil)
	expect(t, r, 200)
	key := r.str("config.Nodes")
	nodesCfg, _ := dig(r.body, "config.Nodes").([]any)
	key = fmt.Sprint(nodesCfg[0].(map[string]any)["ApiKey"])
	if !strings.HasPrefix(key, "nk_") || !strings.Contains(r.str("command"), "/etc/V2bX/config.json") {
		t.Fatalf("install cmd: %s", r.raw)
	}

	// 节点接口：错误密钥、正确密钥、ETag
	uni := func(method, path, token string, body any, hdr map[string]string) resp {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return h.do(method, "/api/v1/server/UniProxy/"+path+sep+"node_type=vless&node_id="+nodeID+"&token="+token, body, hdr)
	}
	expect(t, uni("GET", "user", "wrong", nil, nil), 403)
	r = uni("GET", "user", key, nil, nil)
	expect(t, r, 200)
	if users, _ := r.body["users"].([]any); len(users) != 3 {
		t.Fatalf("node users: %s", r.raw)
	}
	etag := r.header.Get("ETag")
	expect(t, uni("GET", "user", key, nil, map[string]string{"If-None-Match": etag}), 304)
	r = uni("GET", "config", key, nil, nil)
	expect(t, r, 200)
	if r.str("protocol") != "vless" || r.str("server_port") != "24000" || dig(r.body, "tls_settings.private_key") == nil {
		t.Fatalf("node config: %s", r.raw)
	}

	// 首次心跳后转为在线并下发到订阅
	node, _ = h.a.Store.GetNode(ctx, node.ID)
	if node.State != "online" {
		t.Fatalf("node should be online after heartbeat, got %s", node.State)
	}
	sub = h.do("GET", "/api/v1/client/subscribe?token="+subTokens[0], nil, map[string]string{"User-Agent": "clash-verge/v1.7"})
	if !strings.Contains(string(sub.raw), "东京-01") || !strings.Contains(sub.header.Get("subscription-userinfo"), "total=322122547200") {
		t.Fatalf("subscription: %s %v", sub.raw, sub.header)
	}

	// 流量上报：先落库，后台聚合后扣减；第二个用户超额
	gb := int64(1 << 30)
	r = uni("POST", "push", key, map[string]any{userIDs[0]: []int64{gb, 2 * gb}, userIDs[1]: []int64{0, 301 * gb}}, nil)
	expect(t, r, 200)
	if n, err := h.a.Billing.ProcessReports(ctx); err != nil || n != 1 {
		t.Fatalf("process reports: %d %v", n, err)
	}
	u1, _ := h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if u1.UsedUp != gb || u1.UsedDown != 2*gb {
		t.Fatalf("u1 usage: %d %d", u1.UsedUp, u1.UsedDown)
	}
	if n, _ := h.a.Billing.ProcessReports(ctx); n != 0 {
		t.Fatal("reports must not be counted twice")
	}
	r = uni("GET", "user", key, nil, nil)
	if users, _ := r.body["users"].([]any); len(users) != 2 {
		t.Fatalf("over-quota user must be removed from node: %s", r.raw)
	}
	expect(t, h.do("GET", "/api/v1/client/subscribe?token="+subTokens[1], nil, nil), 403)

	// 在线设备与设备数
	expect(t, uni("POST", "alive", key, map[string]any{userIDs[0]: []string{"198.51.100.1:5555", "198.51.100.1:6666", "198.51.100.2"}}, nil), 200)
	r = uni("GET", "alivelist", key, nil, nil)
	if r.str("alive."+userIDs[0]) != "2" {
		t.Fatalf("alivelist: %s", r.raw)
	}

	// 常规操作直接执行；预演后数据被改动，旧计划作废
	r = h.api("PATCH", "/api/v1/users/"+userIDs[0], map[string]any{"extend_days": 30, "dry_run": true})
	expect(t, r, 200)
	stale := r.str("change_id")
	expect(t, h.api("POST", "/api/v1/users/"+userIDs[0]+":reset-traffic", nil), 200)
	r = h.api("POST", "/api/v1/changes/"+stale+":apply", nil)
	expect(t, r, 409)
	if r.str("status") != "stale" {
		t.Fatalf("expected stale: %s", r.raw)
	}

	// 改价格固定为敏感，拒绝后计划失效
	r = h.api("PATCH", "/api/v1/plans/premium", map[string]any{"prices": map[string]int{"monthly": 2000}})
	expect(t, r, 202)
	if !strings.Contains(r.str("risk_reason"), "价格") {
		t.Fatalf("price change reason: %s", r.raw)
	}
	if _, err := h.a.Approvals.Reject(ctx, r.str("approval_id"), h.op, approval.ViaWeb, "价格还没定"); err != nil {
		t.Fatal(err)
	}
	r = h.api("GET", "/api/v1/changes/"+r.str("change_id"), nil)
	if r.str("status") != "rejected" {
		t.Fatalf("change after reject: %s", r.raw)
	}

	// 节点离线告警与恢复
	if err := h.a.Billing.CheckNodesOffline(ctx, -time.Second); err != nil {
		t.Fatal(err)
	}
	open, _ := h.a.Store.ListOpenEvents(ctx, 10)
	if !hasEvent(open, "node_offline") {
		t.Fatalf("expected node_offline event: %+v", open)
	}
	uni("GET", "config", key, nil, nil)
	open, _ = h.a.Store.ListOpenEvents(ctx, 10)
	if hasEvent(open, "node_offline") {
		t.Fatal("offline event should resolve after heartbeat")
	}

	// 审计里有完整的记录
	r = h.api("GET", "/api/v1/audit?limit=50", nil)
	items, _ := r.body["items"].([]any)
	results := map[string]bool{}
	for _, it := range items {
		results[fmt.Sprint(it.(map[string]any)["result"])] = true
	}
	for _, want := range []string{"success", "pending_approval", "voided", "rejected"} {
		if !results[want] {
			t.Errorf("audit missing result %s", want)
		}
	}
}

func hasEvent(evs []db.Event, kind string) bool {
	for _, e := range evs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func TestIdempotencyAndScopes(t *testing.T) {
	h := newHarness(t, nil)
	hdr := map[string]string{"Authorization": "Bearer " + h.token}
	body := map[string]any{"code": "basic", "name": "基础", "transfer_gb": 100}

	expect(t, h.do("POST", "/api/v1/plans", body, hdr), 400)

	hdr["Idempotency-Key"] = "k-1"
	first := h.do("POST", "/api/v1/plans", body, hdr)
	expect(t, first, 200)
	again := h.do("POST", "/api/v1/plans", body, hdr)
	expect(t, again, 200)
	if again.header.Get("Idempotent-Replayed") != "true" || again.str("change_id") != first.str("change_id") {
		t.Fatalf("replay should return the first result: %s", again.raw)
	}
	other := h.do("POST", "/api/v1/plans", map[string]any{"code": "other", "name": "别的", "transfer_gb": 1}, hdr)
	expect(t, other, 422)

	// 只读 token 不能写
	ro, _, err := h.a.Auth.IssueToken(context.Background(), h.op.ID, "ro", "read")
	if err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/api/v1/plans", body, map[string]string{"Authorization": "Bearer " + ro, "Idempotency-Key": "k-2"})
	expect(t, r, 403)
	expect(t, h.do("GET", "/api/v1/plans", nil, map[string]string{"Authorization": "Bearer " + ro}), 200)

	// 错误给出修正提示
	r = h.api("POST", "/api/v1/nodes:provision", map[string]any{"template": "nope", "address": "1.1.1.1", "plans": []string{"basic"}})
	expect(t, r, 404)
	if r.str("error.code") != "template_not_found" || r.str("error.hint") == "" {
		t.Fatalf("error should carry a hint: %s", r.raw)
	}
	r = h.api("POST", "/api/v1/users", map[string]any{"email": "x@example.com", "plan": "premium"})
	if r.str("error.code") != "plan_not_found" || !strings.Contains(r.str("error.hint"), "basic") {
		t.Fatalf("plan hint should list plans: %s", r.raw)
	}
}

func TestPortal(t *testing.T) {
	h := newHarness(t, nil)
	_, _, _, subTokens := setup(h)

	r := h.do("POST", "/api/v1/me/session", map[string]any{"email": "u1@example.com", "password": "wrong-pass"}, nil)
	expect(t, r, 401)
	r = h.do("POST", "/api/v1/me/session", map[string]any{"email": "U1@example.com", "password": "password-123"}, nil)
	expect(t, r, 200)
	cookie := r.header.Get("Set-Cookie")
	if !strings.HasPrefix(cookie, "patato_portal=") {
		t.Fatalf("cookie: %s", cookie)
	}
	cookie = strings.Split(cookie, ";")[0]
	auth := map[string]string{"Cookie": cookie}

	r = h.do("GET", "/api/v1/me/subscription", nil, auth)
	expect(t, r, 200)
	if !strings.Contains(r.str("subscription_url"), subTokens[0]) || r.str("plan.code") != "premium" || r.str("state") != "active" {
		t.Fatalf("subscription: %s", r.raw)
	}
	// 管理接口不认终端用户的会话
	expect(t, h.do("GET", "/api/v1/status", nil, auth), 401)

	r = h.do("POST", "/api/v1/me/subscription:reset-token", nil, auth)
	expect(t, r, 200)
	expect(t, h.do("GET", "/api/v1/client/subscribe?token="+subTokens[0], nil, nil), 403)
	newURL := r.str("subscription_url")
	newToken := newURL[strings.LastIndex(newURL, "=")+1:]
	expect(t, h.do("GET", "/api/v1/client/subscribe?token="+newToken, nil, nil), 200)

	// 连续输错 5 次锁定
	for i := 0; i < 5; i++ {
		h.do("POST", "/api/v1/me/session", map[string]any{"email": "u2@example.com", "password": "bad"}, nil)
	}
	r = h.do("POST", "/api/v1/me/session", map[string]any{"email": "u2@example.com", "password": "password-123"}, nil)
	expect(t, r, 429)

	// 找回密码对不存在的邮箱也返回成功
	expect(t, h.do("POST", "/api/v1/me/password:forgot", map[string]any{"email": "nobody@example.com"}, nil), 200)
}

func TestSkillAndTools(t *testing.T) {
	h := newHarness(t, nil)
	r := h.do("GET", "/skill.md", nil, nil)
	expect(t, r, 200)
	if !strings.Contains(string(r.raw), "/api/v1/nodes:provision") || !strings.Contains(string(r.raw), "超过 100 名用户") {
		t.Fatalf("skill.md: %s", r.raw)
	}
	r = h.do("GET", "/api/v1/tools", nil, nil)
	expect(t, r, 200)
	if tools, _ := r.body["tools"].([]any); len(tools) < 30 {
		t.Fatalf("tools: %d", len(tools))
	}
}

// TestXboardNodeV2 按 xboard-node 的调用方式走一遍 /api/v2/server：handshake 关闭 WebSocket，
// GET 的鉴权参数在 query 里，POST 的在 JSON 请求体里；report 合并上报流量、在线 IP 和负载。
func TestXboardNodeV2(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	_, _, userIDs, _ := setup(h)
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-premium", "address": "203.0.113.30", "host": "la-01", "plans": []string{"premium"}, "backend": "uniproxy",
	}), 200)
	n, err := h.a.Store.GetNodeByCode(ctx, "la-01")
	if err != nil {
		t.Fatal(err)
	}
	r := h.api("GET", "/api/v1/nodes/la-01/install-cmd", nil)
	expect(t, r, 200)
	nodesCfg, _ := dig(r.body, "config.Nodes").([]any)
	key := fmt.Sprint(nodesCfg[0].(map[string]any)["ApiKey"])
	id := fmt.Sprint(n.ID)
	q := "?token=" + key + "&node_id=" + id

	expect(t, h.do("POST", "/api/v2/server/handshake", map[string]any{"token": "wrong", "node_id": n.ID}, nil), 403)
	r = h.do("POST", "/api/v2/server/handshake", map[string]any{"token": key, "node_id": n.ID}, nil)
	expect(t, r, 200)
	if r.str("websocket.enabled") != "false" || r.str("settings.push_interval") == "" {
		t.Fatalf("handshake: %s", r.raw)
	}
	if n, _ = h.a.Store.GetNode(ctx, n.ID); n.State != "online" {
		t.Fatalf("handshake must count as heartbeat: %s", n.State)
	}
	r = h.do("GET", "/api/v2/server/config"+q, nil, nil)
	expect(t, r, 200)
	if r.str("protocol") != "vless" {
		t.Fatalf("config: %s", r.raw)
	}
	r = h.do("GET", "/api/v2/server/user"+q, nil, nil)
	expect(t, r, 200)
	if users, _ := r.body["users"].([]any); len(users) != 3 {
		t.Fatalf("users: %s", r.raw)
	}

	gb := int64(1 << 30)
	expect(t, h.do("POST", "/api/v2/server/report", map[string]any{
		"token": key, "node_id": n.ID,
		"traffic": map[string]any{userIDs[0]: []int64{gb, 2 * gb}},
		"alive":   map[string]any{userIDs[0]: []string{"198.51.100.1_5555", "198.51.100.2"}},
		"online":  map[string]any{userIDs[0]: 2},
		"status":  map[string]any{"cpu": 12.5, "mem": map[string]any{"total": 100, "used": 50}},
		"metrics": map[string]any{"active_connections": 3},
	}, nil), 200)
	if c, err := h.a.Billing.ProcessReports(ctx); err != nil || c != 1 {
		t.Fatalf("process reports: %d %v", c, err)
	}
	u1, _ := h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if u1.UsedUp != gb || u1.UsedDown != 2*gb {
		t.Fatalf("u1 usage: %d %d", u1.UsedUp, u1.UsedDown)
	}
	r = h.do("GET", "/api/v2/server/alivelist"+q, nil, nil)
	expect(t, r, 200)
	if r.str("alive."+userIDs[0]) != "2" {
		t.Fatalf("alivelist: %s", r.raw)
	}
	n, _ = h.a.Store.GetNode(ctx, n.ID)
	if !strings.Contains(string(n.Load), "12.5") {
		t.Fatalf("load: %s", n.Load)
	}
	// 空的 report 也算一次上报
	expect(t, h.do("POST", "/api/v2/server/report", map[string]any{"token": key, "node_id": n.ID}, nil), 200)
}
