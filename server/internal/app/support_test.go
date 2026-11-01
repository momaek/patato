package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/automation"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store/db"
)

// operatorCookie 模拟运营者已在 Admin 用 passkey 登录。
func (h *harness) operatorCookie() map[string]string {
	h.t.Helper()
	token, _, err := h.a.Auth.CreateSession(context.Background(), auth.KindOperator, h.op.ID, "test", "127.0.0.1")
	if err != nil {
		h.t.Fatal(err)
	}
	return map[string]string{"Cookie": "patato_admin=" + token, "Idempotency-Key": "idem-" + token[:12]}
}

func TestTickets(t *testing.T) {
	h := newHarness(t, nil)
	setup(h)
	u1 := h.login("u1@example.com", "password-123")

	r := h.do("POST", "/api/v1/me/tickets", map[string]any{"subject": "连不上", "body": "从昨天开始东京节点连不上"}, u1)
	expect(t, r, 200)
	id := r.str("ticket.id")

	// Agent 读工单，带上用户状态等上下文
	r = h.api("GET", "/api/v1/tickets/"+id, nil)
	expect(t, r, 200)
	if r.str("context.user.email") != "u1@example.com" || r.str("ticket.status") != "open" {
		t.Fatalf("ticket detail: %s", r.raw)
	}

	// Agent 只能保存草稿；请求里自报的草拟人会被覆盖
	r = h.api("POST", "/api/v1/tickets/"+id+":draft", map[string]any{"body": "东京-01 昨晚维护，已恢复，请刷新订阅后重试。",
		"basis": []string{"节点 东京-01：在线"}, "drafted_by": "老板本人"})
	expect(t, r, 200)
	r = h.api("GET", "/api/v1/tickets/"+id, nil)
	if by := r.str("draft.drafted_by"); !strings.Contains(by, "CLI token") || !strings.Contains(by, "Claude Code 会话") {
		t.Fatalf("drafted_by: %s", r.raw)
	}
	// 用户看不到草稿
	r = h.do("GET", "/api/v1/me/tickets/"+id, nil, u1)
	if strings.Contains(string(r.raw), "已恢复") {
		t.Fatalf("draft leaked to user: %s", r.raw)
	}

	// 发送回复只接受运营者会话
	r = h.api("POST", "/api/v1/tickets/"+id+":reply", map[string]any{"body": "x"})
	expect(t, r, 403)
	if r.str("error.code") != "operator_session_required" {
		t.Fatalf("token reply: %s", r.raw)
	}
	r = h.do("POST", "/api/v1/tickets/"+id+":reply", map[string]any{"body": "东京-01 昨晚维护，已恢复，请刷新订阅后重试。", "from_draft": true}, h.operatorCookie())
	expect(t, r, 200)
	if r.str("ticket.status") != "replied" || r.str("draft") != "<nil>" {
		t.Fatalf("reply: %s", r.raw)
	}
	msgs, _ := r.body["messages"].([]any)
	if len(msgs) != 2 || !strings.Contains(fmt.Sprint(msgs[1].(map[string]any)["drafted_by"]), "CLI token") {
		t.Fatalf("messages: %s", r.raw)
	}

	// 回答不了的问题标为「等你处理」，出现在总览
	expect(t, h.api("POST", "/api/v1/tickets/"+id+":draft", map[string]any{"needs_human": true}), 400)
	expect(t, h.api("POST", "/api/v1/tickets/"+id+":draft", map[string]any{"needs_human": true, "needs_human_reason": "用户要开发票"}), 200)
	r = h.api("GET", "/api/v1/status", nil)
	if !strings.Contains(string(r.raw), "用户要开发票") {
		t.Fatalf("status todo: %s", r.raw)
	}

	// 用户关闭后再回复会重新打开
	expect(t, h.do("POST", "/api/v1/me/tickets/"+id+":close", nil, u1), 200)
	r = h.do("POST", "/api/v1/me/tickets/"+id+"/messages", map[string]any{"body": "又不行了"}, u1)
	expect(t, r, 200)
	if r.str("ticket.status") != "open" {
		t.Fatalf("reopen: %s", r.raw)
	}
	// 别人的工单看不到
	u2 := h.login("u2@example.com", "password-123")
	expect(t, h.do("GET", "/api/v1/me/tickets/"+id, nil, u2), 404)
}

func TestWebhookAndAutomation(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	_, _, userIDs, _ := setup(h)

	type got struct {
		sig, ts string
		body    []byte
	}
	recv := make(chan got, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recv <- got{r.Header.Get("X-Patato-Signature"), r.Header.Get("X-Patato-Timestamp"), b}
	}))
	defer srv.Close()

	// Webhook 外发数据，是敏感操作
	h.approve(h.sensitive("POST", "/api/v1/webhooks", map[string]any{"name": "agent-runner", "url": srv.URL, "kinds": []string{events.TrafficAnomaly}}), h.op, "")
	hooks, _ := h.a.Store.ListWebhooks(ctx)
	if len(hooks) != 1 {
		t.Fatal("webhook not created")
	}
	// 密钥只给运营者会话
	expect(t, h.api("GET", fmt.Sprintf("/api/v1/webhooks/%d/secret", hooks[0].ID), nil), 403)
	r := h.do("GET", fmt.Sprintf("/api/v1/webhooks/%d/secret", hooks[0].ID), nil, h.operatorCookie())
	expect(t, r, 200)
	secretKey := r.str("secret")

	// 自动处置：流量异常时重置这个用户的订阅 token（以系统身份走变更计划）
	expect(t, h.api("POST", "/api/v1/automations", map[string]any{"name": "x", "event": "node_offline", "action": "email_user", "params": map[string]any{"subject": "a", "body": "b"}}), 400)
	h.approve(h.sensitive("POST", "/api/v1/automations", map[string]any{"name": "异常即重置链接", "event": events.TrafficAnomaly, "action": "operation",
		"params": map[string]any{"kind": "user.reset_token", "params": map[string]any{"user": "{{subject_id}}"}}}), h.op, "")
	h.approve(h.sensitive("POST", "/api/v1/automations", map[string]any{"name": "提醒用户", "event": events.TrafficAnomaly, "action": "email_user",
		"params": map[string]any{"subject": "{{service}} 用量异常", "body": "{{email}}：{{message}}"}}), h.op, "")

	before, _ := h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	h.a.Events.Emit(ctx, nil, events.Event{Kind: events.TrafficAnomaly, Severity: "warning", Subject: "user:" + userIDs[0], Message: "测试异常", DedupKey: "t1"})
	evs, _ := h.a.Store.ListEventsAfter(ctx, db.ListEventsAfterParams{ID: 0, Limit: 100})
	ev := evs[len(evs)-1]
	ids, err := h.a.Automate.Dispatch(ctx, ev.ID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("dispatch: %v %v", ids, err)
	}
	after, _ := h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if after.SubToken == before.SubToken {
		t.Fatal("automation should have reset the token")
	}
	rules, _ := h.a.Store.ListAutomationRules(ctx)
	for _, rule := range rules {
		if !strings.HasPrefix(rule.LastResult, "已执行") && !strings.HasPrefix(rule.LastResult, "已发邮件") {
			t.Fatalf("rule %s result: %s", rule.Name, rule.LastResult)
		}
	}
	audit, _ := h.a.Store.ListAudit(ctx, db.ListAuditParams{RowLimit: 50})
	found := false
	for _, a := range audit {
		found = found || (a.Action == "user.reset_token" && a.Entry == "automation" && a.SubjectKind == "system")
	}
	if !found {
		t.Fatal("automation must be audited as system")
	}

	// 推送带签名
	if err := h.a.Automate.Deliver(ctx, ids[0], ev.ID); err != nil {
		t.Fatal(err)
	}
	g := <-recv
	if g.sig != automation.Sign([]byte(secretKey), g.ts, g.body) {
		t.Fatalf("bad signature %s", g.sig)
	}
	var p automation.Payload
	if json.Unmarshal(g.body, &p) != nil || p.Kind != events.TrafficAnomaly || p.Message != "测试异常" {
		t.Fatalf("payload: %s", g.body)
	}

	// 审计导出
	r = h.api("GET", "/api/v1/audit/export?action=user.reset_token", nil)
	expect(t, r, 200)
	if !strings.Contains(string(r.raw), "automation") || !strings.HasPrefix(r.header.Get("Content-Type"), "text/csv") {
		t.Fatalf("export: %s", r.raw)
	}
}
