package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

func findOrder(ctx context.Context, q *db.Queries, r Ref) (db.Order, error) {
	if r == "" {
		return db.Order{}, apierr.BadRequest("missing_order", "缺少订单").WithHint("传订单号或订单 ID；用 GET /api/v1/orders 查看")
	}
	o, err := q.GetOrderByTradeNo(ctx, string(r))
	if store.IsNotFound(err) {
		if id, ok := r.id(); ok {
			o, err = q.GetOrder(ctx, id)
		}
	}
	if store.IsNotFound(err) {
		return o, apierr.NotFound("order_not_found", "订单 %s 不存在", r).WithHint("用 GET /api/v1/orders?q=<订单号或邮箱> 搜索")
	}
	return o, err
}

func orderTarget(o db.Order) change.Target {
	return change.Target{Type: "order", ID: o.ID, Version: o.Version}
}

func (d *Deps) emit(ctx context.Context, q *db.Queries, e events.Event) {
	if d.Events != nil {
		d.Events.Emit(ctx, q, e)
	}
}

func dateText(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "长期有效"
	}
	return t.In(loc).Format("2006-01-02")
}

// ---- order.refund --------------------------------------------------------

// OrderRefund 退款固定为敏感操作。资金需要在支付平台原路退回，这里记录退款并扣回套餐时长。
type OrderRefund struct{ d *Deps }

func (*OrderRefund) Kind() string      { return "order.refund" }
func (*OrderRefund) Risk() change.Risk { return change.Sensitive }

type orderRefundParams struct {
	Order Ref `json:"order"`
	// Amount 是退款金额（分），默认实付全额。
	Amount *int64 `json:"amount"`
	// DeductDays 是扣回的天数，默认这笔订单顺延的天数。
	DeductDays *int `json:"deduct_days"`
	// ExpireNow 为 true 时直接让订阅到期（一次性套餐退款时用）。
	ExpireNow bool `json:"expire_now"`
	// KeepBalance 为 true 时不退回下单时抵扣的余额。
	KeepBalance bool   `json:"keep_balance"`
	Reason      string `json:"reason"`
}

type refundResolved struct {
	Order         string     `json:"order"`
	Amount        int64      `json:"amount"`
	DeductDays    int        `json:"deduct_days"`
	NewExpiresAt  *time.Time `json:"new_expires_at"`
	ChangeExpiry  bool       `json:"change_expiry"`
	ReturnBalance int64      `json:"return_balance"`
	Reason        string     `json:"reason"`
}

func (o *OrderRefund) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[orderRefundParams](raw)
	if err != nil {
		return nil, err
	}
	ord, err := findOrder(ctx, q, p.Order)
	if err != nil {
		return nil, err
	}
	if ord.Status != "paid" {
		return nil, apierr.Conflict("order_not_paid", "订单 %s 当前状态是 %s，只有已支付的订单可以退款", ord.TradeNo, ord.Status)
	}
	u, err := q.GetUser(ctx, ord.UserID)
	if err != nil {
		return nil, err
	}
	res := refundResolved{Order: ord.TradeNo, Amount: ord.Total, DeductDays: int(ord.Days), Reason: strings.TrimSpace(p.Reason)}
	if p.Amount != nil {
		if *p.Amount < 0 || *p.Amount > ord.Total {
			return nil, apierr.BadRequest("invalid_amount", "退款金额应在 0 到实付 %s 元之间", commerce.Yuan(ord.Total))
		}
		res.Amount = *p.Amount
	}
	if p.DeductDays != nil {
		if *p.DeductDays < 0 {
			return nil, apierr.BadRequest("invalid_days", "deduct_days 不能为负数")
		}
		res.DeductDays = *p.DeductDays
	}
	if !p.KeepBalance {
		res.ReturnBalance = ord.BalanceUsed
	}
	now := o.d.now()
	steps := []string{fmt.Sprintf("订单 %s（%s %s）标记为已退款，退款 %s 元；资金需要在支付平台原路退回",
		ord.TradeNo, ord.PlanName, commerce.PeriodNames[ord.Period], commerce.Yuan(res.Amount))}
	switch {
	case p.ExpireNow:
		t := now.UTC()
		res.NewExpiresAt, res.ChangeExpiry = &t, true
		steps = append(steps, fmt.Sprintf("用户 %s 的订阅立即到期（原到期 %s）", u.Email, dateText(u.ExpiresAt, o.d.Loc)))
	case res.DeductDays > 0 && u.ExpiresAt != nil:
		t := u.ExpiresAt.AddDate(0, 0, -res.DeductDays)
		if t.Before(now) {
			t = now
		}
		t = t.UTC()
		res.NewExpiresAt, res.ChangeExpiry = &t, true
		steps = append(steps, fmt.Sprintf("扣回 %d 天：用户 %s 的到期时间 %s → %s", res.DeductDays, u.Email,
			dateText(u.ExpiresAt, o.d.Loc), dateText(&t, o.d.Loc)))
	default:
		steps = append(steps, "不扣回套餐时长")
	}
	if res.ReturnBalance > 0 {
		steps = append(steps, fmt.Sprintf("退回下单时抵扣的余额 %s 元", commerce.Yuan(res.ReturnBalance)))
	}
	if c, err := q.GetCommissionByOrder(ctx, ord.ID); err == nil && c.Status != "cancelled" {
		if c.Status == "settled" {
			steps = append(steps, fmt.Sprintf("这笔订单的返佣 %s 元已入账，从邀请人余额扣回", commerce.Yuan(c.Amount)))
		} else {
			steps = append(steps, fmt.Sprintf("作废这笔订单的返佣 %s 元", commerce.Yuan(c.Amount)))
		}
	}
	diff := []change.DiffEntry{{Field: "status", Before: "paid", After: "refunded"}, {Field: "refund_amount", Before: 0, After: res.Amount}}
	if res.ChangeExpiry {
		diff = append(diff, change.DiffEntry{Field: "expires_at", Before: u.ExpiresAt, After: res.NewExpiresAt})
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("订单 %s 退款 %s 元", ord.TradeNo, commerce.Yuan(res.Amount)),
		Steps:    steps,
		Diff:     diff,
		Impact:   change.Impact{Users: 1},
		Targets:  []change.Target{orderTarget(ord), userTarget(u)},
		Resolved: res,
	}, nil
}

func (o *OrderRefund) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[refundResolved](raw)
	if err != nil {
		return nil, err
	}
	ord, err := q.LockOrderByTradeNo(ctx, r.Order)
	if err != nil {
		return nil, err
	}
	if r.ChangeExpiry {
		if err := q.SetUserExpiry(ctx, db.SetUserExpiryParams{ID: ord.UserID, ExpiresAt: r.NewExpiresAt}); err != nil {
			return nil, err
		}
	}
	if r.ReturnBalance > 0 {
		if _, err := q.AddUserBalance(ctx, db.AddUserBalanceParams{ID: ord.UserID, Delta: r.ReturnBalance}); err != nil {
			return nil, err
		}
	}
	if _, _, err := commerce.ReverseCommission(ctx, q, ord.ID); err != nil {
		return nil, err
	}
	ref, err := q.SetOrderRefunded(ctx, db.SetOrderRefundedParams{ID: ord.ID, RefundAmount: r.Amount, RefundDays: int32(r.DeductDays), Note: r.Reason})
	if err != nil {
		return nil, err
	}
	o.d.emit(ctx, q, events.Event{
		Kind: events.OrderRefunded, Subject: fmt.Sprintf("order:%d", ord.ID),
		Message: fmt.Sprintf("订单 %s 已退款 %s 元", ord.TradeNo, commerce.Yuan(r.Amount)),
		Payload: map[string]any{"order_id": ord.ID, "trade_no": ord.TradeNo, "user_id": ord.UserID, "amount": r.Amount},
	})
	return map[string]any{"trade_no": ref.TradeNo, "status": ref.Status, "refund_amount": ref.RefundAmount, "expires_at": r.NewExpiresAt}, nil
}

// ---- order.mark_paid -----------------------------------------------------

// OrderMarkPaid 是支付回调丢失时的兜底：运营者核对到账后手动确认，按订单开通。
type OrderMarkPaid struct{ d *Deps }

func (*OrderMarkPaid) Kind() string      { return "order.mark_paid" }
func (*OrderMarkPaid) Risk() change.Risk { return change.Sensitive }

type orderMarkPaidParams struct {
	Order          Ref    `json:"order"`
	GatewayTradeNo string `json:"gateway_trade_no"`
	Note           string `json:"note"`
}

func (o *OrderMarkPaid) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[orderMarkPaidParams](raw)
	if err != nil {
		return nil, err
	}
	ord, err := findOrder(ctx, q, p.Order)
	if err != nil {
		return nil, err
	}
	if ord.Status != "pending" && ord.Status != "cancelled" {
		return nil, apierr.Conflict("order_not_pending", "订单 %s 当前状态是 %s，不能标记为已支付", ord.TradeNo, ord.Status)
	}
	u, err := q.GetUser(ctx, ord.UserID)
	if err != nil {
		return nil, err
	}
	steps := []string{fmt.Sprintf("确认订单 %s 已收款 %s 元（%s）", ord.TradeNo, commerce.Yuan(ord.Total), orDash(p.GatewayTradeNo))}
	switch ord.Kind {
	case "reset_traffic":
		steps = append(steps, fmt.Sprintf("清零用户 %s 的已用流量", u.Email))
	case "renew":
		steps = append(steps, fmt.Sprintf("用户 %s 续费 %s，从当前到期日 %s 接着顺延", u.Email, commerce.PeriodNames[ord.Period], dateText(u.ExpiresAt, o.d.Loc)))
	default:
		steps = append(steps, fmt.Sprintf("用户 %s 开通 %s（%s），到期时间从现在算", u.Email, ord.PlanName, commerce.PeriodNames[ord.Period]))
	}
	return &change.Draft{
		Summary: fmt.Sprintf("手动确认订单 %s 已支付", ord.TradeNo),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "status", Before: ord.Status, After: "paid"}},
		Impact:  change.Impact{Users: 1},
		Targets: []change.Target{orderTarget(ord), userTarget(u)},
		Resolved: map[string]any{"order": ord.TradeNo, "gateway_trade_no": strings.TrimSpace(p.GatewayTradeNo),
			"note": strings.TrimSpace("手动确认收款 " + p.Note)},
	}, nil
}

func (o *OrderMarkPaid) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[orderMarkPaidParams](raw)
	if err != nil {
		return nil, err
	}
	ord, err := q.LockOrderByTradeNo(ctx, string(p.Order))
	if err != nil {
		return nil, err
	}
	paid, err := commerce.Fulfill(ctx, q, ord, commerce.FulfillInput{Method: firstNonEmpty(ord.Method, "manual"), GatewayTradeNo: p.GatewayTradeNo, Note: p.Note}, o.d.now(), o.d.Loc)
	if err != nil {
		return nil, err
	}
	for _, e := range paid.Events {
		o.d.emit(ctx, q, e)
	}
	return map[string]any{"trade_no": paid.Order.TradeNo, "status": paid.Order.Status, "expires_at": paid.User.ExpiresAt}, nil
}

// ---- order.cancel --------------------------------------------------------

type OrderCancel struct{ d *Deps }

func (*OrderCancel) Kind() string      { return "order.cancel" }
func (*OrderCancel) Risk() change.Risk { return change.Normal }

type orderRefParams struct {
	Order Ref `json:"order"`
}

func (o *OrderCancel) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[orderRefParams](raw)
	if err != nil {
		return nil, err
	}
	ord, err := findOrder(ctx, q, p.Order)
	if err != nil {
		return nil, err
	}
	if ord.Status != "pending" {
		return nil, apierr.Conflict("order_not_pending", "订单 %s 当前状态是 %s，只能取消待支付的订单", ord.TradeNo, ord.Status)
	}
	steps := []string{fmt.Sprintf("取消待支付订单 %s", ord.TradeNo)}
	if ord.BalanceUsed > 0 {
		steps = append(steps, fmt.Sprintf("退回占用的余额 %s 元", commerce.Yuan(ord.BalanceUsed)))
	}
	if ord.CouponID != nil {
		steps = append(steps, "退回占用的优惠码次数")
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("取消订单 %s", ord.TradeNo),
		Steps:    steps,
		Diff:     []change.DiffEntry{{Field: "status", Before: "pending", After: "cancelled"}},
		Impact:   change.Impact{Users: 1},
		Targets:  []change.Target{orderTarget(ord)},
		Resolved: map[string]any{"order": ord.TradeNo},
	}, nil
}

func (o *OrderCancel) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[orderRefParams](raw)
	if err != nil {
		return nil, err
	}
	ord, err := q.LockOrderByTradeNo(ctx, string(p.Order))
	if err != nil {
		return nil, err
	}
	c, err := commerce.Cancel(ctx, q, ord)
	if err != nil {
		return nil, err
	}
	return map[string]any{"trade_no": c.TradeNo, "status": c.Status}, nil
}

// ---- coupon.create / coupon.update --------------------------------------

var couponCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

type couponFields struct {
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`
	Value    int64      `json:"value"`
	PlanIDs  []int64    `json:"plan_ids"`
	Periods  []string   `json:"periods"`
	MaxUses  int        `json:"max_uses"`
	PerUser  int        `json:"per_user"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
	Enabled  bool       `json:"enabled"`
}

func (f *couponFields) validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return apierr.BadRequest("missing_name", "缺少优惠券名称")
	}
	switch f.Kind {
	case "amount":
		if f.Value <= 0 {
			return apierr.BadRequest("invalid_value", "减免金额（分）必须大于 0")
		}
	case "percent":
		if f.Value <= 0 || f.Value > 100 {
			return apierr.BadRequest("invalid_value", "减免百分比应在 1–100 之间")
		}
	default:
		return apierr.BadRequest("invalid_kind", "不支持的优惠类型 %q", f.Kind).WithHint("可选：amount（减免金额，单位分）、percent（减免百分比）")
	}
	for _, p := range f.Periods {
		if !slices.Contains(commerce.Periods, p) {
			return apierr.BadRequest("invalid_period", "不支持的周期 %q", p)
		}
	}
	if f.MaxUses < 0 || f.PerUser < 0 {
		return apierr.BadRequest("invalid_limit", "使用次数不能为负数")
	}
	if f.StartsAt != nil && f.EndsAt != nil && !f.EndsAt.After(*f.StartsAt) {
		return apierr.BadRequest("invalid_window", "结束时间应晚于开始时间")
	}
	if f.PlanIDs == nil {
		f.PlanIDs = []int64{}
	}
	if f.Periods == nil {
		f.Periods = []string{}
	}
	return nil
}

func couponFieldsOf(c db.Coupon) couponFields {
	return couponFields{Name: c.Name, Kind: c.Kind, Value: c.Value, PlanIDs: c.PlanIds, Periods: c.Periods, MaxUses: int(c.MaxUses),
		PerUser: int(c.PerUser), StartsAt: c.StartsAt, EndsAt: c.EndsAt, Enabled: c.Enabled}
}

func describeCoupon(f couponFields) string {
	if f.Kind == "percent" {
		return fmt.Sprintf("减免 %d%%", f.Value)
	}
	return fmt.Sprintf("减免 %s 元", commerce.Yuan(f.Value))
}

func resolvePlanIDs(ctx context.Context, q *db.Queries, refs []Ref) ([]int64, error) {
	ids := []int64{}
	for _, r := range refs {
		pl, err := findPlan(ctx, q, r)
		if err != nil {
			return nil, err
		}
		ids = append(ids, pl.ID)
	}
	return ids, nil
}

// CouponCreate 发券涉及钱，固定为敏感操作。
type CouponCreate struct{ d *Deps }

func (*CouponCreate) Kind() string      { return "coupon.create" }
func (*CouponCreate) Risk() change.Risk { return change.Sensitive }

type couponCreateParams struct {
	Code     string     `json:"code"`
	Name     string     `json:"name"`
	Kind     string     `json:"kind"`
	Value    int64      `json:"value"`
	Plans    []Ref      `json:"plans"`
	Periods  []string   `json:"periods"`
	MaxUses  int        `json:"max_uses"`
	PerUser  int        `json:"per_user"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
	Enabled  *bool      `json:"enabled"`
}

type couponCreateResolved struct {
	Code string `json:"code"`
	couponFields
}

func (o *CouponCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[couponCreateParams](raw)
	if err != nil {
		return nil, err
	}
	if !couponCodePattern.MatchString(p.Code) {
		return nil, apierr.BadRequest("invalid_code", "优惠码 %q 只能包含字母、数字、下划线和连字符，3–32 位", p.Code)
	}
	if _, err := q.GetCouponByCode(ctx, p.Code); err == nil {
		return nil, apierr.Conflict("coupon_exists", "优惠码 %s 已存在", p.Code)
	}
	ids, err := resolvePlanIDs(ctx, q, p.Plans)
	if err != nil {
		return nil, err
	}
	f := couponFields{Name: p.Name, Kind: p.Kind, Value: p.Value, PlanIDs: ids, Periods: p.Periods, MaxUses: p.MaxUses,
		PerUser: p.PerUser, StartsAt: p.StartsAt, EndsAt: p.EndsAt, Enabled: p.Enabled == nil || *p.Enabled}
	if err := f.validate(); err != nil {
		return nil, err
	}
	limit := "不限次数"
	if f.MaxUses > 0 {
		limit = fmt.Sprintf("共 %d 次", f.MaxUses)
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("新建优惠码 %s（%s）", p.Code, describeCoupon(f)),
		Steps:    []string{fmt.Sprintf("新建优惠码 %s：%s，%s，每人 %s", p.Code, describeCoupon(f), limit, perUserText(f.PerUser))},
		Diff:     []change.DiffEntry{{Field: "优惠券", Before: nil, After: f}},
		Resolved: couponCreateResolved{Code: p.Code, couponFields: f},
	}, nil
}

func perUserText(n int) string {
	if n == 0 {
		return "不限"
	}
	return fmt.Sprintf("限 %d 次", n)
}

func (o *CouponCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[couponCreateResolved](raw)
	if err != nil {
		return nil, err
	}
	c, err := q.CreateCoupon(ctx, db.CreateCouponParams{Code: r.Code, Name: r.Name, Kind: r.Kind, Value: r.Value, PlanIds: r.PlanIDs,
		Periods: r.Periods, MaxUses: int32(r.MaxUses), PerUser: int32(r.PerUser), StartsAt: r.StartsAt, EndsAt: r.EndsAt, Enabled: r.Enabled})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("coupon_exists", "优惠码 %s 已存在", r.Code)
		}
		return nil, err
	}
	return map[string]any{"coupon_id": c.ID, "code": c.Code}, nil
}

// CouponUpdate 修改优惠券。只停用时是常规操作，其余改动涉及钱，按敏感处理。
type CouponUpdate struct{ d *Deps }

func (*CouponUpdate) Kind() string      { return "coupon.update" }
func (*CouponUpdate) Risk() change.Risk { return change.Normal }

type couponUpdateParams struct {
	Coupon   Ref        `json:"coupon"`
	Name     *string    `json:"name"`
	Kind     *string    `json:"kind"`
	Value    *int64     `json:"value"`
	Plans    []Ref      `json:"plans"`
	Periods  []string   `json:"periods"`
	MaxUses  *int       `json:"max_uses"`
	PerUser  *int       `json:"per_user"`
	StartsAt *time.Time `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
	Enabled  *bool      `json:"enabled"`
}

func findCoupon(ctx context.Context, q *db.Queries, r Ref) (db.Coupon, error) {
	if r == "" {
		return db.Coupon{}, apierr.BadRequest("missing_coupon", "缺少优惠券")
	}
	c, err := q.GetCouponByCode(ctx, string(r))
	if store.IsNotFound(err) {
		if id, ok := r.id(); ok {
			c, err = q.GetCoupon(ctx, id)
		}
	}
	if store.IsNotFound(err) {
		return c, apierr.NotFound("coupon_not_found", "优惠券 %s 不存在", r).WithHint("用 GET /api/v1/coupons 查看全部")
	}
	return c, err
}

func (o *CouponUpdate) merged(ctx context.Context, q *db.Queries, raw json.RawMessage) (db.Coupon, couponFields, couponFields, error) {
	p, err := decode[couponUpdateParams](raw)
	if err != nil {
		return db.Coupon{}, couponFields{}, couponFields{}, err
	}
	c, err := findCoupon(ctx, q, p.Coupon)
	if err != nil {
		return c, couponFields{}, couponFields{}, err
	}
	before, after := couponFieldsOf(c), couponFieldsOf(c)
	if p.Name != nil {
		after.Name = *p.Name
	}
	if p.Kind != nil {
		after.Kind = *p.Kind
	}
	if p.Value != nil {
		after.Value = *p.Value
	}
	if p.Plans != nil {
		if after.PlanIDs, err = resolvePlanIDs(ctx, q, p.Plans); err != nil {
			return c, before, after, err
		}
	}
	if p.Periods != nil {
		after.Periods = p.Periods
	}
	if p.MaxUses != nil {
		after.MaxUses = *p.MaxUses
	}
	if p.PerUser != nil {
		after.PerUser = *p.PerUser
	}
	if p.StartsAt != nil {
		after.StartsAt = p.StartsAt
	}
	if p.EndsAt != nil {
		after.EndsAt = p.EndsAt
	}
	if p.Enabled != nil {
		after.Enabled = *p.Enabled
	}
	return c, before, after, after.validate()
}

func (o *CouponUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	c, before, after, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	diff := fieldDiff(before, after)
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	d := &change.Draft{
		Summary: fmt.Sprintf("修改优惠码 %s", c.Code),
		Steps:   []string{fmt.Sprintf("更新优惠码 %s 的 %d 项设置", c.Code, len(diff))},
		Diff:    diff,
		Targets: []change.Target{{Type: "coupon", ID: c.ID, Version: c.Version}},
	}
	if !(len(diff) == 1 && diff[0].Field == "enabled" && !after.Enabled) {
		d.Escalate = "涉及优惠，固定为敏感操作（只停用优惠券时不需要审批）"
	}
	return d, nil
}

func (o *CouponUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	c, _, a, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	u, err := q.UpdateCoupon(ctx, db.UpdateCouponParams{ID: c.ID, Name: a.Name, Kind: a.Kind, Value: a.Value, PlanIds: a.PlanIDs,
		Periods: a.Periods, MaxUses: int32(a.MaxUses), PerUser: int32(a.PerUser), StartsAt: a.StartsAt, EndsAt: a.EndsAt, Enabled: a.Enabled})
	if err != nil {
		return nil, err
	}
	return map[string]any{"coupon_id": u.ID, "version": u.Version}, nil
}

// ---- referral.settings / referral.release -------------------------------

// ReferralSettings 修改返佣规则，涉及钱，固定为敏感操作。
type ReferralSettings struct{ d *Deps }

func (*ReferralSettings) Kind() string      { return "referral.settings" }
func (*ReferralSettings) Risk() change.Risk { return change.Sensitive }

type referralSettingsParams struct {
	Enabled        *bool `json:"enabled"`
	RatePercent    *int  `json:"rate_percent"`
	WaitDays       *int  `json:"wait_days"`
	FirstOrderOnly *bool `json:"first_order_only"`
	SubnetLimit    *int  `json:"subnet_limit"`
}

func (o *ReferralSettings) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[referralSettingsParams](raw)
	if err != nil {
		return nil, err
	}
	before, err := commerce.LoadRules(ctx, q)
	if err != nil {
		return nil, err
	}
	after := before
	if p.Enabled != nil {
		after.Enabled = *p.Enabled
	}
	if p.RatePercent != nil {
		after.RatePercent = *p.RatePercent
	}
	if p.WaitDays != nil {
		after.WaitDays = *p.WaitDays
	}
	if p.FirstOrderOnly != nil {
		after.FirstOrderOnly = *p.FirstOrderOnly
	}
	if p.SubnetLimit != nil {
		after.SubnetLimit = *p.SubnetLimit
	}
	if err := after.Validate(); err != nil {
		return nil, err
	}
	diff := fieldDiff(before, after)
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	t, err := change.SettingTarget(ctx, q, commerce.RulesKey)
	if err != nil {
		return nil, err
	}
	state := "关闭"
	if after.Enabled {
		state = fmt.Sprintf("开启，返佣 %d%%，%d 天内无退款入账", after.RatePercent, after.WaitDays)
	}
	return &change.Draft{
		Summary:  "修改邀请返佣规则",
		Steps:    []string{"返佣规则改为：" + state, "只影响之后产生的返佣，已有返佣不变"},
		Diff:     diff,
		Targets:  []change.Target{t},
		Resolved: after,
	}, nil
}

func (o *ReferralSettings) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	var r commerce.Rules
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if err := q.SetSetting(ctx, db.SetSettingParams{Key: commerce.RulesKey, Value: mustJSON(r)}); err != nil {
		return nil, err
	}
	return r, nil
}

// ReferralRelease 解冻邀请人被风控冻结的返佣，涉及钱，固定为敏感操作。
type ReferralRelease struct{ d *Deps }

func (*ReferralRelease) Kind() string      { return "referral.release" }
func (*ReferralRelease) Risk() change.Risk { return change.Sensitive }

type referralReleaseParams struct {
	User       Ref    `json:"user"`
	Commission *int64 `json:"commission"`
}

func frozenOf(ctx context.Context, q *db.Queries, inviter int64, only *int64) ([]db.Commission, error) {
	rows, err := q.FrozenCommissionsOf(ctx, inviter)
	if err != nil || only == nil {
		return rows, err
	}
	for _, c := range rows {
		if c.ID == *only {
			return []db.Commission{c}, nil
		}
	}
	return nil, apierr.NotFound("commission_not_found", "邀请人没有编号为 %d 的冻结返佣", *only)
}

func (o *ReferralRelease) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[referralReleaseParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	rows, err := frozenOf(ctx, q, u.ID, p.Commission)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, apierr.Conflict("nothing_frozen", "%s 没有被冻结的返佣", u.Email)
	}
	var total int64
	steps := []string{}
	for _, c := range rows {
		total += c.Amount
		steps = append(steps, fmt.Sprintf("解冻返佣 #%d：%s 元（冻结原因：%s）", c.ID, commerce.Yuan(c.Amount), orDash(c.Reason)))
	}
	steps = append(steps, "等待期已过的直接入账到邀请人余额，其余等待期满后入账")
	return &change.Draft{
		Summary: fmt.Sprintf("解冻 %s 的返佣 %s 元", u.Email, commerce.Yuan(total)),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "frozen", Before: total, After: 0}},
		Impact:  change.Impact{Users: 1},
		Targets: []change.Target{userTarget(u)},
		Resolved: map[string]any{
			"user": fmt.Sprint(u.ID), "commission": p.Commission,
		},
	}, nil
}

func (o *ReferralRelease) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[referralReleaseParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	rows, err := frozenOf(ctx, q, u.ID, p.Commission)
	if err != nil {
		return nil, err
	}
	now := o.d.now()
	for _, c := range rows {
		if err := commerce.Release(ctx, q, c, now); err != nil {
			return nil, err
		}
	}
	if o.d.Events != nil {
		o.d.Events.Resolve(ctx, q, fmt.Sprintf("%s:%d", events.ReferralFrozen, u.ID))
	}
	return map[string]any{"released": len(rows)}, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
