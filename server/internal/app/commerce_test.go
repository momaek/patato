package app

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/approval"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/store/db"
)

const epayKey = "epay-test-key"

type fakeCodes struct{ last string }

func (f *fakeCodes) SendCode(_ context.Context, _ int64, text string) error {
	f.last = regexp.MustCompile(`\d{6}`).FindString(text)
	return nil
}

func commerceHarness(t *testing.T) *harness {
	return newHarness(t, func(c *config.Config) {
		c.PortalURL = "http://portal.test"
		c.Registration.Enabled = true
		c.Payment.Methods = []config.PaymentMethod{{Code: "alipay", Name: "支付宝", Driver: "epay", URL: "https://pay.test/", PID: "1001", Key: epayKey}}
		c.Approval.RequiredFor = map[string]int{"coupon.create": 2}
		c.Approval.CodeFor = []string{"order.refund"}
	})
}

// approve 让运营者批准一个审批，并要求它已经执行。
func (h *harness) approve(id string, op db.Operator, code string) *approval.Detail {
	h.t.Helper()
	d, err := h.a.Approvals.Approve(context.Background(), id, op, approval.ViaWeb, code)
	if err != nil {
		h.t.Fatalf("approve %s: %v", id, err)
	}
	return d
}

// sensitive 执行一个敏感写请求，返回生成的审批 ID。
func (h *harness) sensitive(method, path string, body any) string {
	h.t.Helper()
	r := h.api(method, path, body)
	expect(h.t, r, 202)
	return r.str("approval_id")
}

func (h *harness) login(email, password string) map[string]string {
	h.t.Helper()
	r := h.do("POST", "/api/v1/me/session", map[string]any{"email": email, "password": password}, nil)
	expect(h.t, r, 200)
	return map[string]string{"Cookie": strings.Split(r.header.Get("Set-Cookie"), ";")[0]}
}

func epayNotify(h *harness, tradeNo, money string, key string) resp {
	g := commerce.NewEPay("alipay", "支付宝", "https://pay.test/", "1001", key, "")
	v := url.Values{"pid": {"1001"}, "trade_no": {"EP" + tradeNo}, "out_trade_no": {tradeNo}, "type": {"alipay"},
		"name": {"x"}, "money": {money}, "trade_status": {"TRADE_SUCCESS"}}
	v.Set("sign", g.Sign(v))
	v.Set("sign_type", "MD5")
	return h.do("GET", "/api/v1/payment/alipay/notify?"+v.Encode(), nil, nil)
}

func TestCommerce(t *testing.T) {
	h := commerceHarness(t)
	ctx := context.Background()
	setup(h)
	op2, err := h.a.Store.CreateOperator(ctx, db.CreateOperatorParams{Name: "第二位运营者", WebauthnUserID: []byte("op-2")})
	if err != nil {
		t.Fatal(err)
	}

	// 改价是敏感操作：开卖并定价
	apr := h.sensitive("PATCH", "/api/v1/plans/premium", map[string]any{"sellable": true,
		"prices": map[string]int64{"monthly": 1990, "quarterly": 4990, "reset_traffic": 500}})
	h.approve(apr, h.op, "")

	// 开启返佣：10%，没有等待期
	apr = h.sensitive("PUT", "/api/v1/referrals/settings", map[string]any{"enabled": true, "rate_percent": 10, "wait_days": 0, "subnet_limit": 3})
	h.approve(apr, h.op, "")

	// 发券需要两名运营者批准；同一个人不能批两次
	apr = h.sensitive("POST", "/api/v1/coupons", map[string]any{"code": "HALF", "name": "五折", "kind": "percent", "value": 50, "per_user": 1})
	if d := h.approve(apr, h.op, ""); d.Status != "pending" || len(d.Votes) != 1 || d.Required != 2 {
		t.Fatalf("first vote: %+v", d)
	}
	if _, err := h.a.Approvals.Approve(ctx, apr, h.op, approval.ViaWeb, ""); err == nil || !strings.Contains(err.Error(), "already_voted") {
		t.Fatalf("double vote: %v", err)
	}
	if d := h.approve(apr, op2, ""); d.Status != "approved" || d.Change.Status != "applied" {
		t.Fatalf("second vote: %+v", d)
	}

	// u1 的邀请码注册新用户
	u1 := h.login("u1@example.com", "password-123")
	r := h.do("GET", "/api/v1/me/referral", nil, u1)
	expect(t, r, 200)
	invite := r.str("invite_code")
	if invite == "" || !strings.Contains(r.str("invite_url"), invite) || r.str("rate_percent") != "10" {
		t.Fatalf("referral: %s", r.raw)
	}
	r = h.do("POST", "/api/v1/me/register", map[string]any{"email": "new@example.com", "password": "password-456", "invite_code": invite}, nil)
	expect(t, r, 200)
	newbie := map[string]string{"Cookie": strings.Split(r.header.Get("Set-Cookie"), ";")[0]}
	expect(t, h.do("POST", "/api/v1/me/register", map[string]any{"email": "new@example.com", "password": "password-456"}, nil), 409)

	// 在售套餐和价格
	r = h.do("GET", "/api/v1/me/plans", nil, newbie)
	expect(t, r, 200)
	if items, _ := r.body["items"].([]any); len(items) != 1 || !strings.Contains(string(r.raw), `"alipay"`) {
		t.Fatalf("plans: %s", r.raw)
	}

	// 试算：五折
	r = h.do("POST", "/api/v1/me/orders:quote", map[string]any{"plan": "premium", "period": "monthly", "coupon": "half"}, newbie)
	expect(t, r, 200)
	if r.str("amount") != "1990" || r.str("discount") != "995" || r.str("total") != "995" || r.str("kind") != "new" {
		t.Fatalf("quote: %s", r.raw)
	}
	expect(t, h.do("POST", "/api/v1/me/orders:quote", map[string]any{"plan": "premium", "period": "yearly"}, newbie), 400)
	// 流量重置包只能用于当前套餐
	expect(t, h.do("POST", "/api/v1/me/orders:quote", map[string]any{"plan": "premium", "period": "reset_traffic"}, newbie), 400)

	// 下单，拿到易支付链接
	r = h.do("POST", "/api/v1/me/orders", map[string]any{"plan": "premium", "period": "monthly", "coupon": "HALF", "method": "alipay"}, newbie)
	expect(t, r, 200)
	tradeNo := r.str("order.trade_no")
	pay, _ := url.Parse(r.str("pay_url"))
	if pay.Path != "/submit.php" || pay.Query().Get("money") != "9.95" || pay.Query().Get("sign") == "" ||
		pay.Query().Get("return_url") != "http://portal.test/orders/"+tradeNo {
		t.Fatalf("pay url: %s", r.raw)
	}
	// 每人限用一次：第二单不能再用
	expect(t, h.do("POST", "/api/v1/me/orders", map[string]any{"plan": "premium", "period": "monthly", "coupon": "HALF", "method": "alipay"}, newbie), 400)

	// 回调：签名不对、金额不对都不开通
	if r := epayNotify(h, tradeNo, "9.95", "wrong-key"); r.status != 400 || string(r.raw) != "fail" {
		t.Fatalf("bad sign: %d %s", r.status, r.raw)
	}
	if r := epayNotify(h, tradeNo, "1.00", epayKey); r.status != 400 {
		t.Fatalf("bad amount: %d %s", r.status, r.raw)
	}
	nu, _ := h.a.Store.GetUserByEmail(ctx, "new@example.com")
	if nu.PlanID != nil {
		t.Fatal("must not fulfil before a valid callback")
	}
	if r := epayNotify(h, tradeNo, "9.95", epayKey); r.status != 200 || string(r.raw) != "success" {
		t.Fatalf("notify: %d %s", r.status, r.raw)
	}
	// 平台重发回调不会重复开通
	expect(t, epayNotify(h, tradeNo, "9.95", epayKey), 200)
	nu, _ = h.a.Store.GetUserByEmail(ctx, "new@example.com")
	if nu.PlanID == nil || nu.ExpiresAt == nil || nu.ExpiresAt.Sub(time.Now()) < 29*24*time.Hour || nu.ExpiresAt.Sub(time.Now()) > 31*24*time.Hour {
		t.Fatalf("fulfil: %+v", nu)
	}
	r = h.do("GET", "/api/v1/me/orders/"+tradeNo, nil, newbie)
	if r.str("order.status") != "paid" || r.str("order.days") != "30" {
		t.Fatalf("order: %s", r.raw)
	}

	// 返佣：没有等待期，后台任务入账到 u1 余额
	if n, err := h.a.Commerce.SettleDue(ctx); err != nil || n != 1 {
		t.Fatalf("settle: %d %v", n, err)
	}
	inviter, _ := h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if inviter.Balance != 99 {
		t.Fatalf("commission balance: %d", inviter.Balance)
	}

	// u1 续费用余额抵扣，取消后余额退回
	r = h.do("POST", "/api/v1/me/orders", map[string]any{"plan": "premium", "period": "quarterly", "use_balance": true, "method": "alipay"}, u1)
	expect(t, r, 200)
	if r.str("order.kind") != "renew" || r.str("order.balance_used") != "99" || r.str("order.total") != "4891" {
		t.Fatalf("renew order: %s", r.raw)
	}
	renewNo := r.str("order.trade_no")
	inviter, _ = h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if inviter.Balance != 0 {
		t.Fatalf("balance held: %d", inviter.Balance)
	}
	expect(t, h.do("POST", "/api/v1/me/orders/"+renewNo+":cancel", nil, u1), 200)
	inviter, _ = h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if inviter.Balance != 99 {
		t.Fatalf("balance returned: %d", inviter.Balance)
	}

	// 超时未支付自动取消
	r = h.do("POST", "/api/v1/me/orders", map[string]any{"plan": "premium", "period": "monthly", "method": "alipay"}, u1)
	expect(t, r, 200)
	stale := r.str("order.trade_no")
	if _, err := h.a.Store.Pool.Exec(ctx, "UPDATE orders SET expires_at = now() - interval '1 minute' WHERE trade_no = $1", stale); err != nil {
		t.Fatal(err)
	}
	if n, err := h.a.Commerce.ExpireDue(ctx); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}

	// 退款：敏感操作，还要一次性码
	codes := &fakeCodes{}
	h.a.Approvals.SetCodeSender(codes)
	tg := int64(424242)
	if err := h.a.Store.SetOperatorTelegram(ctx, db.SetOperatorTelegramParams{ID: h.op.ID, TelegramUserID: &tg}); err != nil {
		t.Fatal(err)
	}
	h.op.TelegramUserID = &tg
	r = h.api("POST", "/api/v1/orders/"+tradeNo+":refund", map[string]any{"dry_run": true, "reason": "用户申请"})
	expect(t, r, 200)
	if r.str("risk") != "sensitive" || !strings.Contains(string(r.raw), "从邀请人余额扣回") {
		t.Fatalf("refund plan: %s", r.raw)
	}
	apr = h.sensitive("POST", "/api/v1/orders/"+tradeNo+":refund", map[string]any{"reason": "用户申请"})
	r = h.api("GET", "/api/v1/orders?status=refunding", nil)
	if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Fatalf("refunding filter: %s", r.raw)
	}
	if _, err := h.a.Approvals.Approve(ctx, apr, h.op, approval.ViaTelegram, ""); err == nil || !strings.Contains(err.Error(), "code_required") {
		t.Fatalf("telegram must not approve code-protected changes: %v", err)
	}
	if _, err := h.a.Approvals.SendCode(ctx, apr, h.op); err != nil || len(codes.last) != 6 {
		t.Fatalf("send code: %v %q", err, codes.last)
	}
	if _, err := h.a.Approvals.Approve(ctx, apr, h.op, approval.ViaWeb, "000000"); err == nil || !strings.Contains(err.Error(), "code_invalid") {
		t.Fatalf("wrong code: %v", err)
	}
	if d := h.approve(apr, h.op, codes.last); d.Change.Status != "applied" {
		t.Fatalf("refund: %+v", d.Change)
	}
	nu2, _ := h.a.Store.GetUserByEmail(ctx, "new@example.com")
	if nu2.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatalf("refund must deduct the 30 days: %v", nu2.ExpiresAt)
	}
	inviter, _ = h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	if inviter.Balance != 0 {
		t.Fatalf("commission must be clawed back: %d", inviter.Balance)
	}

	// 管理接口：订单详情、报表
	r = h.api("GET", "/api/v1/orders/"+tradeNo, nil)
	expect(t, r, 200)
	if r.str("order.status") != "refunded" || r.str("commission.status") != "cancelled" {
		t.Fatalf("order detail: %s", r.raw)
	}
	if cbs, _ := r.body["callbacks"].([]any); len(cbs) != 4 {
		t.Fatalf("callbacks: %s", r.raw)
	}
	r = h.api("GET", "/api/v1/reports/revenue?days=7", nil)
	expect(t, r, 200)
	if r.str("revenue") != "995" || r.str("refunds") != "995" || r.str("net") != "0" {
		t.Fatalf("revenue report: %s", r.raw)
	}
	expect(t, h.api("GET", "/api/v1/reports/traffic?days=7", nil), 200)
	expect(t, h.api("GET", "/api/v1/reports/nodes?days=7", nil), 200)

	// 套餐版本：改价前的设置留了快照
	r = h.api("GET", "/api/v1/plans/premium/revisions", nil)
	if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Fatalf("revisions: %s", r.raw)
	}
}

func TestRolloutPrices(t *testing.T) {
	h := commerceHarness(t)
	setup(h)
	apr := h.sensitive("PATCH", "/api/v1/plans/premium", map[string]any{"sellable": true, "prices": map[string]int64{"monthly": 1000},
		"rollout_prices": map[string]int64{"monthly": 1200}, "rollout_percent": 50})
	h.approve(apr, h.op, "")
	pl, _ := h.a.Store.GetPlanByCode(context.Background(), "premium")
	seen := map[int64]int{}
	for uid := int64(1); uid <= 200; uid++ {
		seen[commerce.Prices(pl, uid)["monthly"]]++
	}
	if seen[1000] < 60 || seen[1200] < 60 || seen[1000]+seen[1200] != 200 {
		t.Fatalf("rollout split: %v", seen)
	}
	apr = h.sensitive("PATCH", "/api/v1/plans/premium", map[string]any{"promote_rollout": true})
	h.approve(apr, h.op, "")
	pl, _ = h.a.Store.GetPlanByCode(context.Background(), "premium")
	if p := commerce.Prices(pl, 7); p["monthly"] != 1200 || pl.RolloutPercent != 0 {
		t.Fatalf("promote: %v %d", p, pl.RolloutPercent)
	}
}

func TestReferralFreeze(t *testing.T) {
	h := commerceHarness(t)
	ctx := context.Background()
	setup(h)
	h.approve(h.sensitive("PATCH", "/api/v1/plans/premium", map[string]any{"sellable": true, "prices": map[string]int64{"monthly": 1000}}), h.op, "")
	h.approve(h.sensitive("PUT", "/api/v1/referrals/settings", map[string]any{"enabled": true, "rate_percent": 20, "wait_days": 7, "subnet_limit": 1}), h.op, "")
	u1 := h.login("u1@example.com", "password-123")
	invite := h.do("GET", "/api/v1/me/referral", nil, u1).str("invite_code")
	var buyer map[string]string
	for i := 0; i < 2; i++ {
		r := h.do("POST", "/api/v1/me/register", map[string]any{"email": fmt.Sprintf("sub%d@example.com", i), "password": "password-456", "invite_code": invite},
			map[string]string{"X-Forwarded-For": "198.51.100." + fmt.Sprint(10+i)})
		expect(t, r, 200)
		buyer = map[string]string{"Cookie": strings.Split(r.header.Get("Set-Cookie"), ";")[0]}
	}
	r := h.do("POST", "/api/v1/me/orders", map[string]any{"plan": "premium", "period": "monthly", "method": "alipay"}, buyer)
	expect(t, r, 200)
	expect(t, epayNotify(h, r.str("order.trade_no"), "10.00", epayKey), 200)

	// 同网段第二个被邀请人超过阈值：返佣冻结，总览里出现「需要你处理」
	r = h.api("GET", "/api/v1/referrals?status=frozen", nil)
	if items, _ := r.body["items"].([]any); len(items) != 1 || r.str("totals.frozen") != "200" {
		t.Fatalf("frozen: %s", r.raw)
	}
	r = h.api("GET", "/api/v1/status", nil)
	if !strings.Contains(string(r.raw), "referral_frozen") {
		t.Fatalf("status todo: %s", r.raw)
	}
	inviter, _ := h.a.Store.GetUserByEmail(ctx, "u1@example.com")
	h.approve(h.sensitive("POST", fmt.Sprintf("/api/v1/referrals/%d:release", inviter.ID), nil), h.op, "")
	r = h.api("GET", "/api/v1/referrals", nil)
	if r.str("totals.frozen") != "0" || r.str("totals.pending") != "200" {
		t.Fatalf("released: %s", r.raw)
	}
}
