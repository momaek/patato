package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/cli/internal/client"
)

// 二期命令：订单、优惠券、邀请返佣、工单、报表、Webhook、自动处置、审计导出、套餐历史。
// 涉及钱的写命令（退款、手动确认收款、发券、改返佣规则、解冻返佣）在服务端固定为敏感操作。

type order struct {
	ID             int64      `json:"id"`
	TradeNo        string     `json:"trade_no"`
	UserID         int64      `json:"user_id"`
	Email          string     `json:"email"`
	PlanName       string     `json:"plan_name"`
	Period         string     `json:"period"`
	PeriodName     string     `json:"period_name"`
	Kind           string     `json:"kind"`
	Amount         int64      `json:"amount"`
	Discount       int64      `json:"discount"`
	BalanceUsed    int64      `json:"balance_used"`
	Total          int64      `json:"total"`
	Status         string     `json:"status"`
	RefundPending  bool       `json:"refund_pending"`
	RefundApproval string     `json:"refund_approval_id"`
	Method         string     `json:"method"`
	GatewayTradeNo string     `json:"gateway_trade_no"`
	Days           int        `json:"days"`
	RefundAmount   int64      `json:"refund_amount"`
	RefundDays     int        `json:"refund_days"`
	Note           string     `json:"note"`
	Source         string     `json:"source"`
	CreatedAt      *time.Time `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at"`
	RefundedAt     *time.Time `json:"refunded_at"`
}

var orderStatusNames = map[string]string{"pending": "待支付", "paid": "已支付", "refunded": "已退款", "cancelled": "已取消"}
var orderKindNames = map[string]string{"new": "新购", "renew": "续费", "change": "换套餐", "reset_traffic": "流量重置"}
var commissionNames = map[string]string{"pending": "待入账", "frozen": "已冻结", "settled": "已入账", "cancelled": "已作废"}
var ticketStatusNames = map[string]string{"open": "待回复", "replied": "已回复", "closed": "已关闭"}

func orderStatus(o order) string {
	if o.RefundPending {
		return "退款审批中"
	}
	return label(orderStatusNames, o.Status)
}

// ---- order --------------------------------------------------------------

func (a *app) order(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("order list", "patato order list [--status pending|paid|refunding|refunded|cancelled] [--user <邮箱或 ID>] [--q <订单号或邮箱>]")
		status := c.fs.String("status", "", "按状态筛选；refunding 是退款审批中的订单")
		usr := c.fs.String("user", "", "只看某个用户")
		q := c.fs.String("q", "", "搜索订单号或邮箱")
		limit := c.fs.Int("limit", 50, "最多显示多少条")
		if err := c.parse(rest); err != nil {
			return err
		}
		var l list[order]
		if printed, err := a.get(epOrders, query("status", *status, "user", *usr, "q", *q, "limit", strconv.Itoa(*limit)), &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("没有订单\n")
			return nil
		}
		rows := [][]string{}
		for _, o := range l.Items {
			rows = append(rows, []string{o.TradeNo, truncate(o.Email, 28), o.PlanName + " " + orDash(o.PeriodName), label(orderKindNames, o.Kind),
				moneyText(o.Total), orderStatus(o), timeText(o.CreatedAt)})
		}
		table(a.out, []string{"订单号", "用户", "套餐", "类型", "实付（元）", "状态", "下单时间"}, rows, 4)
		return nil
	case "show", "get":
		c := newCommand("order show", "patato order show <订单号>")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "订单")
		if err != nil {
			return err
		}
		var d struct {
			Order      order `json:"order"`
			Commission *struct {
				Amount int64  `json:"amount"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"commission"`
			Coupon *struct {
				Code string `json:"code"`
			} `json:"coupon"`
			User struct {
				ExpiresAt *time.Time `json:"expires_at"`
				Balance   int64      `json:"balance"`
			} `json:"user"`
			Callbacks []struct {
				Verified   bool       `json:"verified"`
				Result     string     `json:"result"`
				ReceivedAt *time.Time `json:"received_at"`
			} `json:"callbacks"`
		}
		if printed, err := a.get(client.Path(epOrder, ref), nil, &d); printed || err != nil {
			return err
		}
		o := d.Order
		a.printf("订单 %s\n\n", o.TradeNo)
		pairs := [][2]string{
			{"状态", orderStatus(o)}, {"用户", fmt.Sprintf("%s（u_%d）", o.Email, o.UserID)},
			{"套餐", o.PlanName + " " + orDash(o.PeriodName)}, {"类型", label(orderKindNames, o.Kind)},
			{"原价", moneyText(o.Amount) + " 元"}, {"优惠", moneyText(o.Discount) + " 元"}, {"余额抵扣", moneyText(o.BalanceUsed) + " 元"},
			{"实付", moneyText(o.Total) + " 元"}, {"支付方式", orDash(o.Method)}, {"支付流水", orDash(o.GatewayTradeNo)},
			{"顺延天数", strconv.Itoa(o.Days)}, {"下单时间", timeText(o.CreatedAt)}, {"支付时间", timeText(o.PaidAt)},
			{"来源", o.Source}, {"用户当前到期", dateText(d.User.ExpiresAt)},
		}
		if d.Coupon != nil {
			pairs = append(pairs, [2]string{"优惠码", d.Coupon.Code})
		}
		if o.Status == "refunded" {
			pairs = append(pairs, [2]string{"退款", fmt.Sprintf("%s 元，扣回 %d 天，%s", moneyText(o.RefundAmount), o.RefundDays, timeText(o.RefundedAt))})
		}
		if d.Commission != nil {
			pairs = append(pairs, [2]string{"返佣", fmt.Sprintf("%s 元，%s %s", moneyText(d.Commission.Amount), label(commissionNames, d.Commission.Status), d.Commission.Reason)})
		}
		if o.Note != "" {
			pairs = append(pairs, [2]string{"备注", o.Note})
		}
		kv(a.out, pairs)
		if len(d.Callbacks) > 0 {
			a.printf("\n支付回调\n")
			for _, cb := range d.Callbacks {
				ok := "验签通过"
				if !cb.Verified {
					ok = "验签失败"
				}
				a.printf("  %s  %s  %s\n", timeText(cb.ReceivedAt), ok, cb.Result)
			}
		}
		if o.RefundPending {
			a.printf("\n退款正在等待审批：patato approval show %s\n", o.RefundApproval)
		}
		return nil
	case "refund":
		c := newCommand("order refund", `patato order refund <订单号> [--amount <元>] [--deduct-days N] [--expire-now] [--keep-balance] [--reason ...]

退款（敏感操作，需要审批）。默认退实付全额、扣回这笔订单顺延的天数、退回下单时抵扣的余额，并撤销返佣。
资金需要在支付平台原路退回；建议先加 --dry-run 看清金额和要扣回的时长。`)
		amount := c.fs.String("amount", "", "退款金额（元），默认实付全额")
		days := c.fs.Int("deduct-days", 0, "扣回多少天，默认这笔订单顺延的天数")
		expire := c.fs.Bool("expire-now", false, "订阅立即到期（一次性套餐退款时用）")
		keep := c.fs.Bool("keep-balance", false, "不退回下单时抵扣的余额")
		reason := c.fs.String("reason", "", "退款原因")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "订单")
		if err != nil {
			return err
		}
		body := map[string]any{}
		c.copy(body, map[string]any{"deduct_days": *days, "expire_now": *expire, "keep_balance": *keep, "reason": *reason})
		if c.set["amount"] {
			cents, err := parseYuan(*amount)
			if err != nil {
				return fmt.Errorf("--amount %w", err)
			}
			body["amount"] = cents
		}
		return a.mutate("POST", client.Path(epOrderRefund, ref), body, w)
	case "mark-paid":
		c := newCommand("order mark-paid", `patato order mark-paid <订单号> [--gateway-trade-no ...] [--note ...]

支付回调丢失时的兜底：核对支付平台确实到账后，手动确认收款并按订单开通（敏感操作，需要审批）。`)
		gtn := c.fs.String("gateway-trade-no", "", "支付平台的流水号")
		note := c.fs.String("note", "", "备注")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "订单")
		if err != nil {
			return err
		}
		body := map[string]any{}
		c.copy(body, map[string]any{"gateway_trade_no": *gtn, "note": *note})
		return a.mutate("POST", client.Path(epOrderMarkPaid, ref), body, w)
	case "cancel":
		return a.nodeRef(rest, "order cancel", "patato order cancel <订单号>\n\n取消待支付订单，退回占用的余额和优惠码次数。", "POST", epOrderCancel)
	}
	return fmt.Errorf("未知命令 order %s\n\n%s", sub, usage)
}

// ---- coupon -------------------------------------------------------------

type coupon struct {
	ID       int64      `json:"id"`
	Code     string     `json:"code"`
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`
	Value    int64      `json:"value"`
	PlanIDs  []int64    `json:"plan_ids"`
	Periods  []string   `json:"periods"`
	MaxUses  int        `json:"max_uses"`
	PerUser  int        `json:"per_user"`
	Used     int        `json:"used"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
	State    string     `json:"state"`
}

var couponStateNames = map[string]string{"active": "可用", "disabled": "已停用", "expired": "已过期", "scheduled": "未开始", "used_up": "已用完"}

func couponValue(c coupon) string {
	if c.Kind == "percent" {
		return fmt.Sprintf("减 %d%%", c.Value)
	}
	return "减 " + moneyText(c.Value) + " 元"
}

func (a *app) coupon(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		var l list[coupon]
		if printed, err := a.get(epCoupons, nil, &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("还没有优惠券：用 patato coupon create 创建\n")
			return nil
		}
		rows := [][]string{}
		for _, c := range l.Items {
			uses := strconv.Itoa(c.Used)
			if c.MaxUses > 0 {
				uses += " / " + strconv.Itoa(c.MaxUses)
			}
			rows = append(rows, []string{c.Code, c.Name, couponValue(c), uses, label(couponStateNames, c.State), dateText(c.EndsAt)})
		}
		table(a.out, []string{"优惠码", "名称", "优惠", "已用", "状态", "截止"}, rows, 3)
		return nil
	case "show", "get":
		c := newCommand("coupon show", "patato coupon show <优惠码>")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "优惠码")
		if err != nil {
			return err
		}
		var cp coupon
		if printed, err := a.get(client.Path(epCoupon, ref), nil, &cp); printed || err != nil {
			return err
		}
		per := "不限"
		if cp.PerUser > 0 {
			per = fmt.Sprintf("%d 次", cp.PerUser)
		}
		periods := "全部"
		if len(cp.Periods) > 0 {
			periods = strings.Join(cp.Periods, "、")
		}
		plans := "全部"
		if len(cp.PlanIDs) > 0 {
			ps := []string{}
			for _, id := range cp.PlanIDs {
				ps = append(ps, strconv.FormatInt(id, 10))
			}
			plans = "套餐 " + strings.Join(ps, "、")
		}
		kv(a.out, [][2]string{{"优惠码", cp.Code}, {"名称", cp.Name}, {"优惠", couponValue(cp)}, {"状态", label(couponStateNames, cp.State)},
			{"已用", fmt.Sprintf("%d / %s", cp.Used, map[bool]string{true: strconv.Itoa(cp.MaxUses), false: "不限"}[cp.MaxUses > 0])},
			{"每人", per}, {"适用套餐", plans}, {"适用周期", periods}, {"开始", timeText(cp.StartsAt)}, {"截止", timeText(cp.EndsAt)}})
		return nil
	case "create", "update":
		return a.couponWrite(sub, rest)
	}
	return fmt.Errorf("未知命令 coupon %s\n\n%s", sub, usage)
}

func (a *app) couponWrite(verb string, args []string) error {
	help := `patato coupon create --code <优惠码> --name <名称> (--amount <元> | --percent <1-100>) [选项]

新建优惠券（涉及钱，敏感操作，需要审批）。`
	if verb == "update" {
		help = `patato coupon update <优惠码> [选项]

只修改给出的字段。只停用（--disable）时是常规操作，其余改动需要审批。`
	}
	c := newCommand("coupon "+verb, help)
	code := c.fs.String("code", "", "优惠码")
	nm := c.fs.String("name", "", "名称")
	amount := c.fs.String("amount", "", "减免金额（元）")
	percent := c.fs.Int("percent", 0, "减免百分比")
	var plans, periods multi
	c.fs.Var(&plans, "plan", "限定套餐，可重复；不给表示全部")
	c.fs.Var(&periods, "period", "限定周期，可重复；不给表示全部")
	maxUses := c.fs.Int("max-uses", 0, "总次数，0 为不限")
	perUser := c.fs.Int("per-user", 0, "每人次数，0 为不限")
	starts := c.fs.String("starts", "", "开始时间（YYYY-MM-DD 或 RFC 3339）")
	ends := c.fs.String("ends", "", "截止时间（YYYY-MM-DD 或 RFC 3339）")
	disable := c.fs.Bool("disable", false, "停用")
	enable := c.fs.Bool("enable", false, "启用")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"name": *nm, "max_uses": *maxUses, "per_user": *perUser})
	switch {
	case c.set["amount"] && c.set["percent"]:
		return fmt.Errorf("--amount 和 --percent 只能给一个")
	case c.set["amount"]:
		cents, err := parseYuan(*amount)
		if err != nil {
			return fmt.Errorf("--amount %w", err)
		}
		body["kind"], body["value"] = "amount", cents
	case c.set["percent"]:
		body["kind"], body["value"] = "percent", *percent
	}
	if c.set["plan"] {
		body["plans"] = []string(plans)
	}
	if c.set["period"] {
		body["periods"] = []string(periods)
	}
	for flag, v := range map[string]string{"starts": *starts, "ends": *ends} {
		if !c.set[flag] {
			continue
		}
		t, err := parseTimeArg(v)
		if err != nil {
			return fmt.Errorf("--%s %w", flag, err)
		}
		body[flag+"_at"] = t
	}
	if *disable {
		body["enabled"] = false
	}
	if *enable {
		body["enabled"] = true
	}
	if verb == "create" {
		c.copy(body, map[string]any{"code": *code})
		return a.mutate("POST", epCoupons, body, w)
	}
	ref, err := c.arg(0, "优惠码")
	if err != nil {
		return err
	}
	return a.mutate("PATCH", client.Path(epCoupon, ref), body, w)
}

func parseTimeArg(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return t, fmt.Errorf("时间 %q 应写成 YYYY-MM-DD 或 RFC 3339", v)
	}
	return t, nil
}

// ---- referral -----------------------------------------------------------

type commission struct {
	ID           int64      `json:"id"`
	InviterID    int64      `json:"inviter_id"`
	InviterEmail string     `json:"inviter_email"`
	InviteeEmail string     `json:"invitee_email"`
	TradeNo      string     `json:"trade_no"`
	Amount       int64      `json:"amount"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason"`
	ReleaseAt    *time.Time `json:"release_at"`
	CreatedAt    *time.Time `json:"created_at"`
}

func (a *app) referral(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("referral list", "patato referral list [--status pending|frozen|settled|cancelled] [--inviter <邮箱或 ID>]")
		status := c.fs.String("status", "", "按状态筛选；frozen 是被风控冻结、等你处理的")
		inviter := c.fs.String("inviter", "", "只看某个邀请人")
		if err := c.parse(rest); err != nil {
			return err
		}
		var l struct {
			Items  []commission `json:"items"`
			Totals struct {
				Pending int64 `json:"pending"`
				Frozen  int64 `json:"frozen"`
				Settled int64 `json:"settled"`
			} `json:"totals"`
		}
		if printed, err := a.get(epReferrals, query("status", *status, "inviter", *inviter), &l); printed || err != nil {
			return err
		}
		a.printf("待入账 %s 元，已冻结 %s 元，已入账 %s 元\n\n", moneyText(l.Totals.Pending), moneyText(l.Totals.Frozen), moneyText(l.Totals.Settled))
		if len(l.Items) == 0 {
			a.printf("没有返佣记录\n")
			return nil
		}
		rows := [][]string{}
		for _, cm := range l.Items {
			rows = append(rows, []string{strconv.FormatInt(cm.ID, 10), truncate(cm.InviterEmail, 24), truncate(cm.InviteeEmail, 24), moneyText(cm.Amount),
				label(commissionNames, cm.Status), truncate(cm.Reason, 40)})
		}
		table(a.out, []string{"编号", "邀请人", "被邀请人", "金额（元）", "状态", "说明"}, rows, 0, 3)
		return nil
	case "settings":
		c := newCommand("referral settings", `patato referral settings [--enabled true|false] [--rate N] [--wait-days N] [--first-order-only true|false] [--subnet-limit N]

不带选项时查看返佣规则；带选项时修改（涉及钱，敏感操作，需要审批）。`)
		enabled := c.fs.String("enabled", "", "是否开启返佣（true / false）")
		rate := c.fs.Int("rate", 0, "返佣比例（%），按被邀请人实付金额计算")
		wait := c.fs.Int("wait-days", 0, "多少天内没有退款才入账")
		first := c.fs.String("first-order-only", "", "只对首单返佣（true / false）")
		subnet := c.fs.Int("subnet-limit", 0, "24 小时内邀请的同网段账号超过这个数时自动冻结，0 不检查")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		body := map[string]any{}
		c.copy(body, map[string]any{"wait_days": *wait, "subnet_limit": *subnet})
		if c.set["rate"] {
			body["rate_percent"] = *rate
		}
		for flag, v := range map[string]string{"enabled": *enabled, "first-order-only": *first} {
			if !c.set[flag] {
				continue
			}
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("--%s 应为 true 或 false", flag)
			}
			body[strings.ReplaceAll(flag, "-", "_")] = b
		}
		if len(body) == 0 {
			var r struct {
				Enabled        bool `json:"enabled"`
				RatePercent    int  `json:"rate_percent"`
				WaitDays       int  `json:"wait_days"`
				FirstOrderOnly bool `json:"first_order_only"`
				SubnetLimit    int  `json:"subnet_limit"`
			}
			if printed, err := a.get(epReferralSettings, nil, &r); printed || err != nil {
				return err
			}
			kv(a.out, [][2]string{{"返佣", map[bool]string{true: "开启", false: "关闭"}[r.Enabled]}, {"比例", fmt.Sprintf("%d%%", r.RatePercent)},
				{"入账等待", fmt.Sprintf("%d 天内无退款", r.WaitDays)}, {"只返首单", map[bool]string{true: "是", false: "否"}[r.FirstOrderOnly]},
				{"同网段冻结阈值", map[bool]string{true: strconv.Itoa(r.SubnetLimit) + " 个 / 24 小时", false: "不检查"}[r.SubnetLimit > 0]}})
			return nil
		}
		return a.mutate("PUT", epReferralSettings, body, w)
	case "release":
		c := newCommand("referral release", "patato referral release <邀请人> [--commission <编号>]\n\n解冻邀请人被风控冻结的返佣（敏感操作，需要审批）。邀请人接受 ID 或邮箱。")
		cm := c.fs.Int64("commission", 0, "只解冻这一笔")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "邀请人")
		if err != nil {
			return err
		}
		body := map[string]any{}
		c.copy(body, map[string]any{"commission": *cm})
		return a.mutate("POST", client.Path(epReferralRelease, ref), body, w)
	}
	return fmt.Errorf("未知命令 referral %s\n\n%s", sub, usage)
}

// ---- ticket -------------------------------------------------------------

type ticket struct {
	ID               int64      `json:"id"`
	Email            string     `json:"email"`
	Subject          string     `json:"subject"`
	Status           string     `json:"status"`
	NeedsHuman       bool       `json:"needs_human"`
	NeedsHumanReason string     `json:"needs_human_reason"`
	HasDraft         bool       `json:"has_draft"`
	LastMessageAt    *time.Time `json:"last_message_at"`
}

func (a *app) ticket(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("ticket list", "patato ticket list [--status open|replied|closed] [--needs-human] [--user <邮箱或 ID>]")
		status := c.fs.String("status", "", "按状态筛选")
		needs := c.fs.Bool("needs-human", false, "只看标为「等你处理」的")
		usr := c.fs.String("user", "", "只看某个用户")
		if err := c.parse(rest); err != nil {
			return err
		}
		nh := ""
		if *needs {
			nh = "true"
		}
		var l list[ticket]
		if printed, err := a.get(epTickets, query("status", *status, "needs_human", nh, "user", *usr), &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("没有工单\n")
			return nil
		}
		rows := [][]string{}
		for _, t := range l.Items {
			flags := []string{}
			if t.NeedsHuman {
				flags = append(flags, "等你处理")
			}
			if t.HasDraft {
				flags = append(flags, "有草稿")
			}
			rows = append(rows, []string{fmt.Sprintf("t_%d", t.ID), truncate(t.Subject, 30), truncate(t.Email, 26), label(ticketStatusNames, t.Status),
				orDash(strings.Join(flags, "、")), agoText(t.LastMessageAt)})
		}
		table(a.out, []string{"工单", "标题", "用户", "状态", "标记", "最近消息"}, rows)
		return nil
	case "show", "get":
		c := newCommand("ticket show", "patato ticket show <工单>\n\n显示对话、草稿，以及草拟回复要用到的用户状态、最近订单和套餐里的节点状态。")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "工单")
		if err != nil {
			return err
		}
		var d struct {
			Ticket   ticket `json:"ticket"`
			Messages []struct {
				Author    string     `json:"author"`
				Operator  string     `json:"operator"`
				DraftedBy string     `json:"drafted_by"`
				Body      string     `json:"body"`
				CreatedAt *time.Time `json:"created_at"`
			} `json:"messages"`
			Draft *struct {
				Body      string   `json:"body"`
				DraftedBy string   `json:"drafted_by"`
				Basis     []string `json:"basis"`
			} `json:"draft"`
			Context struct {
				User    user    `json:"user"`
				Balance int64   `json:"balance"`
				Orders  []order `json:"orders"`
				Nodes   []struct {
					Code  string `json:"code"`
					State string `json:"state"`
				} `json:"plan_nodes"`
			} `json:"context"`
		}
		if printed, err := a.get(client.Path(epTicket, strings.TrimPrefix(ref, "t_")), nil, &d); printed || err != nil {
			return err
		}
		t := d.Ticket
		a.printf("工单 t_%d「%s」，%s\n", t.ID, t.Subject, label(ticketStatusNames, t.Status))
		if t.NeedsHuman {
			a.printf("等你处理：%s\n", t.NeedsHumanReason)
		}
		a.printf("\n")
		for _, m := range d.Messages {
			who := "用户"
			if m.Author == "operator" {
				who = "运营者 " + m.Operator
				if m.DraftedBy != "" {
					who += "（草稿：" + m.DraftedBy + "）"
				}
			}
			a.printf("[%s] %s\n%s\n\n", timeText(m.CreatedAt), who, m.Body)
		}
		u := d.Context.User
		plan := "无"
		if u.Plan != nil {
			plan = u.Plan.Name
		}
		a.printf("用户上下文\n")
		kv(a.out, [][2]string{{"用户", fmt.Sprintf("%s（u_%d）", u.Email, u.ID)}, {"状态", label(userStateNames, u.State)}, {"套餐", plan},
			{"到期", dateText(u.ExpiresAt)}, {"流量", bytesText(u.UsedBytes) + " / " + bytesText(u.TransferBytes)}, {"余额", moneyText(d.Context.Balance) + " 元"}})
		if len(d.Context.Orders) > 0 {
			a.printf("\n最近订单\n")
			for _, o := range d.Context.Orders {
				a.printf("  %s  %s %s  %s 元  %s\n", o.TradeNo, o.PlanName, orDash(o.PeriodName), moneyText(o.Total), label(orderStatusNames, o.Status))
			}
		}
		if len(d.Context.Nodes) > 0 {
			ns := []string{}
			for _, n := range d.Context.Nodes {
				ns = append(ns, n.Code+" "+label(stateNames, n.State))
			}
			a.printf("\n套餐节点：%s\n", strings.Join(ns, "、"))
		}
		if d.Draft != nil {
			a.printf("\n草稿（%s，等运营者确认后发送）\n%s\n", d.Draft.DraftedBy, d.Draft.Body)
			if len(d.Draft.Basis) > 0 {
				a.printf("依据：%s\n", strings.Join(d.Draft.Basis, "；"))
			}
		}
		return nil
	case "draft":
		c := newCommand("ticket draft", `patato ticket draft <工单> --body <回复> [--basis <依据>]... | --needs-human --reason <原因>

保存回复草稿，不会发给用户；运营者在 Admin 工单页确认（可修改）后才发送。
回答不了的问题用 --needs-human 标为「等你处理」，不要编造答复。--body 也可以写 @文件。`)
		body := c.fs.String("body", "", "草稿内容，或 @文件")
		var basis multi
		c.fs.Var(&basis, "basis", "草稿依据了哪些数据，可重复")
		needs := c.fs.Bool("needs-human", false, "标为「等你处理」")
		reason := c.fs.String("reason", "", "--needs-human 的原因")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "工单")
		if err != nil {
			return err
		}
		text := *body
		if strings.HasPrefix(text, "@") {
			b, err := os.ReadFile(text[1:])
			if err != nil {
				return err
			}
			text = string(b)
		}
		req := map[string]any{"body": text}
		if c.set["basis"] {
			req["basis"] = []string(basis)
		}
		if *needs {
			req["needs_human"], req["needs_human_reason"] = true, *reason
		}
		return a.mutate("POST", client.Path(epTicketDraft, strings.TrimPrefix(ref, "t_")), req, w)
	case "close":
		c := newCommand("ticket close", "patato ticket close <工单>")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "工单")
		if err != nil {
			return err
		}
		return a.mutate("POST", client.Path(epTicketClose, strings.TrimPrefix(ref, "t_")), nil, w)
	}
	return fmt.Errorf("未知命令 ticket %s\n\n%s", sub, usage)
}

// ---- report -------------------------------------------------------------

func (a *app) report(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	c := newCommand("report "+sub, "patato report traffic|revenue|nodes [--days N]")
	days := c.fs.Int("days", 30, "统计最近多少天")
	if err := c.parse(rest); err != nil {
		return err
	}
	q := query("days", strconv.Itoa(*days))
	switch sub {
	case "traffic":
		var r struct {
			TotalBytes int64 `json:"total_bytes"`
			Series     []struct {
				Day   string `json:"day"`
				Bytes int64  `json:"bytes"`
			} `json:"series"`
			TopUsers []struct {
				Email string `json:"email"`
				Bytes int64  `json:"bytes"`
			} `json:"top_users"`
		}
		if printed, err := a.get(epReportTraffic, q, &r); printed || err != nil {
			return err
		}
		a.printf("最近 %d 天共 %s\n\n", *days, bytesText(r.TotalBytes))
		rows := [][]string{}
		for _, d := range r.Series {
			rows = append(rows, []string{d.Day, bytesText(d.Bytes)})
		}
		table(a.out, []string{"日期", "流量"}, rows, 1)
		if len(r.TopUsers) > 0 {
			a.printf("\n用量最多的用户\n")
			rows = [][]string{}
			for _, u := range r.TopUsers {
				rows = append(rows, []string{u.Email, bytesText(u.Bytes)})
			}
			table(a.out, []string{"用户", "流量"}, rows, 1)
		}
		return nil
	case "revenue":
		var r struct {
			Revenue int64 `json:"revenue"`
			Refunds int64 `json:"refunds"`
			Net     int64 `json:"net"`
			Orders  int64 `json:"orders"`
			Series  []struct {
				Day     string `json:"day"`
				Orders  int64  `json:"orders"`
				Revenue int64  `json:"revenue"`
				Refunds int64  `json:"refunds"`
			} `json:"series"`
			ByPlan []struct {
				Plan    string `json:"plan"`
				Orders  int64  `json:"orders"`
				Revenue int64  `json:"revenue"`
			} `json:"by_plan"`
		}
		if printed, err := a.get(epReportRevenue, q, &r); printed || err != nil {
			return err
		}
		a.printf("最近 %d 天：收入 %s 元，退款 %s 元，净收入 %s 元，%d 笔订单\n\n", *days, moneyText(r.Revenue), moneyText(r.Refunds), moneyText(r.Net), r.Orders)
		rows := [][]string{}
		for _, d := range r.Series {
			if d.Orders == 0 && d.Refunds == 0 {
				continue
			}
			rows = append(rows, []string{d.Day, strconv.FormatInt(d.Orders, 10), moneyText(d.Revenue), moneyText(d.Refunds)})
		}
		if len(rows) > 0 {
			table(a.out, []string{"日期", "订单", "收入（元）", "退款（元）"}, rows, 1, 2, 3)
		}
		if len(r.ByPlan) > 0 {
			a.printf("\n按套餐\n")
			rows = [][]string{}
			for _, p := range r.ByPlan {
				rows = append(rows, []string{p.Plan, strconv.FormatInt(p.Orders, 10), moneyText(p.Revenue)})
			}
			table(a.out, []string{"套餐", "订单", "收入（元）"}, rows, 1, 2)
		}
		return nil
	case "nodes":
		var r struct {
			TotalCost  int64 `json:"total_cost"`
			TotalBytes int64 `json:"total_bytes"`
			Items      []struct {
				Code      string `json:"code"`
				Name      string `json:"name"`
				Cost      int64  `json:"cost"`
				Bytes     int64  `json:"bytes"`
				CostPerGB *int64 `json:"cost_per_gb"`
				Users     int64  `json:"users"`
			} `json:"items"`
		}
		if printed, err := a.get(epReportNodes, q, &r); printed || err != nil {
			return err
		}
		a.printf("最近 %d 天：成本 %s 元，流量 %s\n\n", *days, moneyText(r.TotalCost), bytesText(r.TotalBytes))
		rows := [][]string{}
		for _, n := range r.Items {
			per := "—"
			if n.CostPerGB != nil {
				per = moneyText(*n.CostPerGB)
			}
			rows = append(rows, []string{n.Code, n.Name, moneyText(n.Cost), bytesText(n.Bytes), per, strconv.FormatInt(n.Users, 10)})
		}
		table(a.out, []string{"代号", "名称", "成本（元）", "流量", "每 GB（元）", "用户"}, rows, 2, 3, 4, 5)
		a.printf("\n节点成本用 patato node update <节点> --monthly-cost <元> 设置\n")
		return nil
	}
	return fmt.Errorf("未知命令 report %s\n\n%s", sub, usage)
}

// ---- webhook / automation -----------------------------------------------

func (a *app) webhook(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		var l list[struct {
			ID             int64      `json:"id"`
			Name           string     `json:"name"`
			URL            string     `json:"url"`
			Kinds          []string   `json:"kinds"`
			LastStatus     string     `json:"last_status"`
			LastDeliveryAt *time.Time `json:"last_delivery_at"`
		}]
		if printed, err := a.get(epWebhooks, nil, &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("没有 Webhook\n")
			return nil
		}
		rows := [][]string{}
		for _, w := range l.Items {
			kinds := "全部事件"
			if len(w.Kinds) > 0 {
				kinds = strings.Join(w.Kinds, ",")
			}
			rows = append(rows, []string{strconv.FormatInt(w.ID, 10), w.Name, truncate(w.URL, 40), truncate(kinds, 30), orDash(w.LastStatus), agoText(w.LastDeliveryAt)})
		}
		table(a.out, []string{"编号", "名称", "地址", "事件", "最近推送", "时间"}, rows, 0)
		return nil
	case "create":
		c := newCommand("webhook create", `patato webhook create --name <名称> --url <地址> [--kind <事件类型>]...

把事件推送到外部地址（事件内容含用户邮箱，外发数据，敏感操作，需要审批）。
每次推送带 X-Patato-Signature: sha256=HMAC(密钥, 时间戳 + "." + 请求体) 和 X-Patato-Timestamp；密钥在 Admin 的「集成」页查看。`)
		nm := c.fs.String("name", "", "名称")
		u := c.fs.String("url", "", "接收地址（http/https）")
		var kinds multi
		c.fs.Var(&kinds, "kind", "只推送这些事件，可重复；不给表示全部")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		body := map[string]any{"name": *nm, "url": *u}
		if c.set["kind"] {
			body["kinds"] = []string(kinds)
		}
		return a.mutate("POST", epWebhooks, body, w)
	case "delete", "rm":
		return a.nodeRef(rest, "webhook delete", "patato webhook delete <编号>", "DELETE", epWebhook)
	}
	return fmt.Errorf("未知命令 webhook %s\n\n%s", sub, usage)
}

func (a *app) automation(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		var l list[struct {
			ID          int64           `json:"id"`
			Name        string          `json:"name"`
			Event       string          `json:"event"`
			Action      string          `json:"action"`
			Params      json.RawMessage `json:"params"`
			Enabled     bool            `json:"enabled"`
			LastFiredAt *time.Time      `json:"last_fired_at"`
			LastResult  string          `json:"last_result"`
		}]
		if printed, err := a.get(epAutomations, nil, &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("没有自动处置规则\n")
			return nil
		}
		rows := [][]string{}
		for _, r := range l.Items {
			st := "启用"
			if !r.Enabled {
				st = "停用"
			}
			rows = append(rows, []string{strconv.FormatInt(r.ID, 10), r.Name, r.Event, r.Action, st, agoText(r.LastFiredAt), truncate(r.LastResult, 40)})
		}
		table(a.out, []string{"编号", "名称", "事件", "动作", "状态", "最近触发", "结果"}, rows, 0)
		return nil
	case "create":
		c := newCommand("automation create", `patato automation create --name <名称> --event <事件类型> --action <动作> --params <JSON 或 @文件>

新建自动处置规则（常驻行为，敏感操作，需要审批）。动作以系统身份走变更计划，被判为敏感时只生成审批。
动作：
  email_user            给事件对应的用户发邮件，params: {"subject": "...", "body": "..."}，可用 {{email}} {{message}} {{service}} {{portal_url}}
  operation             执行一个变更操作，params: {"kind": "user.reset_token", "params": {"user": "{{subject_id}}"}}
  replace_offline_node  节点掉线时把同地区带备用标签的在线节点挂到它的套餐，params: {"standby_tag": "standby"}`)
		nm := c.fs.String("name", "", "名称")
		ev := c.fs.String("event", "", "事件类型，例如 node_offline、user_expiring、traffic_anomaly")
		act := c.fs.String("action", "", "动作")
		params := c.fs.String("params", "{}", "动作参数")
		disabled := c.fs.Bool("disabled", false, "先建成停用状态")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		p, err := jsonArg(*params)
		if err != nil {
			return fmt.Errorf("--params %w", err)
		}
		return a.mutate("POST", epAutomations, map[string]any{"name": *nm, "event": *ev, "action": *act, "params": p, "enabled": !*disabled}, w)
	case "update":
		c := newCommand("automation update", "patato automation update <编号> [--name ...] [--params ...] [--enable | --disable]\n\n只停用时是常规操作，其余改动需要审批。")
		nm := c.fs.String("name", "", "名称")
		params := c.fs.String("params", "", "动作参数")
		enable := c.fs.Bool("enable", false, "启用")
		disable := c.fs.Bool("disable", false, "停用")
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "规则编号")
		if err != nil {
			return err
		}
		body := map[string]any{}
		c.copy(body, map[string]any{"name": *nm})
		if c.set["params"] {
			p, err := jsonArg(*params)
			if err != nil {
				return fmt.Errorf("--params %w", err)
			}
			body["params"] = p
		}
		if *enable {
			body["enabled"] = true
		}
		if *disable {
			body["enabled"] = false
		}
		return a.mutate("PATCH", client.Path(epAutomation, ref), body, w)
	case "delete", "rm":
		return a.nodeRef(rest, "automation delete", "patato automation delete <编号>", "DELETE", epAutomation)
	}
	return fmt.Errorf("未知命令 automation %s\n\n%s", sub, usage)
}

// ---- audit export / plan history ----------------------------------------

func (a *app) auditExport(args []string) error {
	c := newCommand("audit export", `patato audit export [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--action ...] [--entry ...] [--format csv|json] [-o 文件]

导出审计日志，最多 10,000 条。默认 CSV 输出到标准输出。`)
	since := c.fs.String("since", "", "开始日期（含）")
	until := c.fs.String("until", "", "结束日期（含）")
	action := c.fs.String("action", "", "动作，例如 order.refund")
	entry := c.fs.String("entry", "", "入口：cli、web、telegram、portal、payment、automation、system")
	result := c.fs.String("result", "", "结果")
	target := c.fs.String("target", "", "对象，例如 user:12")
	q := c.fs.String("q", "", "搜索动作、对象或 change_id")
	format := c.fs.String("format", "csv", "csv 或 json")
	out := c.fs.String("o", "", "写到文件")
	if err := c.parse(args); err != nil {
		return err
	}
	resp, err := a.c.Do(a.ctx, "GET", epAuditExport, query("since", *since, "until", *until, "action", *action, "entry", *entry,
		"result", *result, "target", *target, "q", *q, "format", *format), nil, nil)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err := a.out.Write(resp.Body)
		return err
	}
	if err := os.WriteFile(*out, resp.Body, 0o600); err != nil {
		return err
	}
	a.printf("已导出到 %s（%s）\n", *out, bytesText(int64(len(resp.Body))))
	return nil
}

func (a *app) planHistory(args []string) error {
	c := newCommand("plan history", "patato plan history <套餐>\n\n套餐每次修改前的设置。")
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "套餐")
	if err != nil {
		return err
	}
	var r struct {
		Plan struct {
			Code    string `json:"code"`
			Version int64  `json:"version"`
		} `json:"plan"`
		Items []struct {
			Version    int64           `json:"version"`
			Snapshot   json.RawMessage `json:"snapshot"`
			ReplacedAt *time.Time      `json:"replaced_at"`
		} `json:"items"`
	}
	if printed, err := a.get(client.Path(epPlanRevisions, ref), nil, &r); printed || err != nil {
		return err
	}
	a.printf("套餐 %s 当前 v%d\n\n", r.Plan.Code, r.Plan.Version)
	if len(r.Items) == 0 {
		a.printf("还没有修改过\n")
		return nil
	}
	for _, it := range r.Items {
		var s struct {
			Name           string           `json:"name"`
			TransferGB     float64          `json:"transfer_gb"`
			Prices         map[string]int64 `json:"prices"`
			RolloutPrices  map[string]int64 `json:"rollout_prices"`
			RolloutPercent int              `json:"rollout_percent"`
			Sellable       bool             `json:"sellable"`
		}
		_ = json.Unmarshal(it.Snapshot, &s)
		line := fmt.Sprintf("v%d（%s 被替换）：%s，%s，价格 %s", it.Version, timeText(it.ReplacedAt), s.Name, gbText(s.TransferGB), pricesText(s.Prices))
		if s.RolloutPercent > 0 {
			line += fmt.Sprintf("，灰度 %d%%：%s", s.RolloutPercent, pricesText(s.RolloutPrices))
		}
		a.printf("%s\n", line)
	}
	return nil
}
