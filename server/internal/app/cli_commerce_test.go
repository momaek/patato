package app

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/momaek/patato/server/internal/approval"
)

// TestCLICommerce 用真实的 patato 二进制走一遍二期命令：发券、退款预演、工单草稿、报表、审计导出。
func TestCLICommerce(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the patato binary")
	}
	bin, err := buildCLI()
	if err != nil {
		t.Fatal(err)
	}
	h := commerceHarness(t)
	setup(h)
	srv := httptest.NewServer(h.a.HTTP)
	defer srv.Close()
	ctx := context.Background()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cli := &cliEnv{t: t, bin: bin, env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "PATATO_CONFIG=" + cfgPath,
		"PATATO_URL=" + srv.URL, "PATATO_TOKEN=" + h.token, "PATATO_ACTOR=Claude Code 会话"}}
	approve := func(id string) {
		t.Helper()
		if _, err := h.a.Approvals.Approve(ctx, id, h.op, approval.ViaCLI, ""); err != nil {
			t.Fatal(err)
		}
	}
	var pending struct {
		Status     string `json:"status"`
		ApprovalID string `json:"approval_id"`
	}

	// 改价（灰度）需要审批
	cli.json(&pending, "plan", "update", "premium", "--sellable", "--price", "monthly=19.9", "--rollout-price", "monthly=24.9", "--rollout-percent", "20")
	if pending.Status != "pending_approval" {
		t.Fatalf("plan update: %+v", pending)
	}
	approve(pending.ApprovalID)
	if out, _ := cli.run("plan", "history", "premium"); !strings.Contains(out, "v1") {
		t.Fatalf("plan history: %s", out)
	}

	// 发券：按元输入；需要两名运营者，这里只看到待审批
	cli.json(&pending, "coupon", "create", "--code", "NEW10", "--name", "新人立减", "--amount", "10", "--per-user", "1")
	if pending.Status != "pending_approval" {
		t.Fatalf("coupon create: %+v", pending)
	}
	if out, _ := cli.run("approval", "show", pending.ApprovalID); !strings.Contains(out, "需要 2 人") {
		t.Fatalf("multi approval hint: %s", out)
	}

	// 工单：Agent 只能存草稿
	u1 := h.login("u1@example.com", "password-123")
	r := h.do("POST", "/api/v1/me/tickets", map[string]any{"subject": "开发票", "body": "请帮我开一张发票"}, u1)
	expect(t, r, 200)
	id := r.str("ticket.id")
	if out, code := cli.run("ticket", "draft", id, "--needs-human", "--reason", "需要人工开票"); code != 0 || !strings.Contains(out, "已执行") {
		t.Fatalf("ticket draft: %d %s", code, out)
	}
	if out, _ := cli.run("ticket", "list", "--needs-human"); !strings.Contains(out, "等你处理") {
		t.Fatalf("ticket list: %s", out)
	}
	if out, _ := cli.run("ticket", "show", "t_"+id); !strings.Contains(out, "需要人工开票") || !strings.Contains(out, "u1@example.com") {
		t.Fatalf("ticket show: %s", out)
	}

	// 下单并付款后，退款预演
	r = h.do("POST", "/api/v1/me/orders", map[string]any{"plan": "premium", "period": "monthly", "method": "alipay"}, u1)
	expect(t, r, 200)
	tradeNo := r.str("order.trade_no")
	total := r.str("order.total")
	money := map[string]string{"1990": "19.90", "2490": "24.90"}[total]
	expect(t, epayNotify(h, tradeNo, money, epayKey), 200)
	if out, _ := cli.run("order", "list"); !strings.Contains(out, tradeNo) || !strings.Contains(out, "已支付") {
		t.Fatalf("order list: %s", out)
	}
	if out, code := cli.run("order", "refund", tradeNo, "--amount", "5", "--dry-run"); code != 0 || !strings.Contains(out, "退款 5.00 元") {
		t.Fatalf("refund dry run: %d %s", code, out)
	}

	// 报表与导出
	if out, _ := cli.run("report", "revenue", "--days", "7"); !strings.Contains(out, "1 笔订单") {
		t.Fatalf("revenue: %s", out)
	}
	if out, _ := cli.run("audit", "export", "--action", "order.paid"); !strings.Contains(out, tradeNo) {
		t.Fatalf("audit export: %s", out)
	}
}
