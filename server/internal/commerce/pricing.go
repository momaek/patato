// Package commerce 实现售卖：报价（含灰度价格和优惠券）、下单、支付履约、取消、返佣入账。
//
// 涉及钱的原则不变：用户自助下单、支付回调验签后的履约由系统完成；
// 退款、手动标记已付、改价、发券、解冻返佣都是敏感操作，走变更计划和审批（见 ops 包）。
package commerce

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// 周期键沿用 Xboard。onetime 是不限时长的一次性套餐，reset_traffic 是当前套餐的流量重置包。
const (
	PeriodOnetime      = "onetime"
	PeriodResetTraffic = "reset_traffic"
)

var periodDays = map[string]int{
	"monthly": 30, "quarterly": 90, "half_yearly": 180, "yearly": 365, "two_yearly": 730, "three_yearly": 1095,
}

// Periods 是周期的展示顺序。
var Periods = []string{"monthly", "quarterly", "half_yearly", "yearly", "two_yearly", "three_yearly", PeriodOnetime, PeriodResetTraffic}

var PeriodNames = map[string]string{
	"monthly": "月付", "quarterly": "季付", "half_yearly": "半年付", "yearly": "年付", "two_yearly": "两年付",
	"three_yearly": "三年付", PeriodOnetime: "一次性", PeriodResetTraffic: "流量重置包",
}

// PeriodDays 返回周期对应的天数；一次性套餐和流量重置包返回 0。
func PeriodDays(p string) int { return periodDays[p] }

func validPeriod(p string) bool { return slices.Contains(Periods, p) }

// ValidatePrices 检查价格表：周期键合法，金额不为负。
func ValidatePrices(prices map[string]int64) error {
	for k, v := range prices {
		if !validPeriod(k) {
			return apierr.BadRequest("invalid_period", "不支持的周期 %q", k).WithHint("可选：%s", strings.Join(Periods, ", "))
		}
		if v < 0 {
			return apierr.BadRequest("invalid_price", "价格 %s 不能为负数", k)
		}
	}
	return nil
}

// InRollout 判断用户是否落在套餐灰度调价的范围内。按用户和套餐哈希分桶，同一用户结果稳定。
func InRollout(userID, planID int64, percent int32) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strconv.FormatInt(planID, 10) + ":" + strconv.FormatInt(userID, 10)))
	return int32(h.Sum32()%100) < percent
}

// Prices 是用户看到的价格：落在灰度范围内时，灰度价格覆盖同周期的原价。userID 为 0 时只看原价。
func Prices(p db.Plan, userID int64) map[string]int64 {
	out := map[string]int64{}
	_ = json.Unmarshal(p.Prices, &out)
	if userID != 0 && len(p.RolloutPrices) > 0 && InRollout(userID, p.ID, p.RolloutPercent) {
		var roll map[string]int64
		if json.Unmarshal(p.RolloutPrices, &roll) == nil {
			for k, v := range roll {
				out[k] = v
			}
		}
	}
	return out
}

// SortedPeriods 按展示顺序列出价格表里有的周期。
func SortedPeriods(prices map[string]int64) []string {
	var out []string
	for _, p := range Periods {
		if _, ok := prices[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// QuoteInput 是下单参数。Plan 接受套餐 ID 或代号。
type QuoteInput struct {
	Plan       string `json:"plan"`
	Period     string `json:"period"`
	Coupon     string `json:"coupon"`
	UseBalance bool   `json:"use_balance"`
}

// Quote 是报价结果。
type Quote struct {
	Plan        db.Plan    `json:"-"`
	Coupon      *db.Coupon `json:"-"`
	PlanCode    string     `json:"plan"`
	PlanName    string     `json:"plan_name"`
	Period      string     `json:"period"`
	Kind        string     `json:"kind"`
	Amount      int64      `json:"amount"`
	Discount    int64      `json:"discount"`
	BalanceUsed int64      `json:"balance_used"`
	Total       int64      `json:"total"`
	Days        int        `json:"days"`
	CouponCode  string     `json:"coupon,omitempty"`
	Notes       []string   `json:"notes"`
}

func findPlan(ctx context.Context, q *db.Queries, ref string) (db.Plan, error) {
	var p db.Plan
	var err error
	if id, perr := strconv.ParseInt(ref, 10, 64); perr == nil {
		p, err = q.GetPlan(ctx, id)
	} else {
		p, err = q.GetPlanByCode(ctx, ref)
	}
	if store.IsNotFound(err) || (err == nil && !p.Sellable) {
		return p, apierr.NotFound("plan_not_found", "套餐 %s 不存在或不在售", ref).WithHint("用 GET /api/v1/me/plans 查看在售套餐")
	}
	return p, err
}

// MakeQuote 计算一笔订单的价格。优惠先减，余额最后抵扣；实付不会为负。
func MakeQuote(ctx context.Context, q *db.Queries, u db.User, in QuoteInput, now time.Time) (*Quote, error) {
	if strings.TrimSpace(in.Plan) == "" {
		return nil, apierr.BadRequest("missing_plan", "缺少套餐")
	}
	pl, err := findPlan(ctx, q, strings.TrimSpace(in.Plan))
	if err != nil {
		return nil, err
	}
	prices := Prices(pl, u.ID)
	price, ok := prices[in.Period]
	if !ok {
		avail := SortedPeriods(prices)
		return nil, apierr.BadRequest("period_unavailable", "套餐 %s 没有 %s 的价格", pl.Name, orKey(PeriodNames, in.Period)).
			WithHint("可选周期：%s", strings.Join(avail, ", "))
	}
	qt := &Quote{Plan: pl, PlanCode: pl.Code, PlanName: pl.Name, Period: in.Period, Amount: price, Days: PeriodDays(in.Period), Notes: []string{}}
	active := u.ExpiresAt == nil || u.ExpiresAt.After(now)
	switch {
	case in.Period == PeriodResetTraffic:
		if u.PlanID == nil || *u.PlanID != pl.ID || !active {
			return nil, apierr.BadRequest("reset_not_allowed", "流量重置包只能用于当前生效中的套餐")
		}
		qt.Kind = "reset_traffic"
		qt.Notes = append(qt.Notes, "支付后已用流量清零，到期时间不变")
	case u.PlanID == nil:
		qt.Kind = "new"
	case *u.PlanID == pl.ID:
		qt.Kind = "renew"
		if active && u.ExpiresAt != nil {
			qt.Notes = append(qt.Notes, "从当前到期日接着顺延，订阅链接不变")
		}
	default:
		qt.Kind = "change"
		qt.Notes = append(qt.Notes, "更换套餐：支付后立即切换，到期时间从支付时重新计算，原套餐剩余时长不折算")
	}
	if in.Period == PeriodOnetime && qt.Kind != "reset_traffic" {
		qt.Notes = append(qt.Notes, "一次性套餐不限时长，流量用完为止")
	}
	if c := strings.TrimSpace(in.Coupon); c != "" {
		cp, discount, err := CheckCoupon(ctx, q, c, u.ID, pl.ID, in.Period, price, now)
		if err != nil {
			return nil, err
		}
		qt.Coupon, qt.CouponCode, qt.Discount = &cp, cp.Code, discount
	}
	rest := qt.Amount - qt.Discount
	if in.UseBalance && u.Balance > 0 && rest > 0 {
		qt.BalanceUsed = min(u.Balance, rest)
	}
	qt.Total = rest - qt.BalanceUsed
	return qt, nil
}

// CheckCoupon 校验优惠券能否用于这笔订单，返回减免金额。
func CheckCoupon(ctx context.Context, q *db.Queries, code string, userID, planID int64, period string, amount int64, now time.Time) (db.Coupon, int64, error) {
	cp, err := q.GetCouponByCode(ctx, code)
	if store.IsNotFound(err) {
		return cp, 0, apierr.BadRequest("coupon_invalid", "优惠码 %s 无效", code)
	}
	if err != nil {
		return cp, 0, err
	}
	switch {
	case !cp.Enabled:
		return cp, 0, apierr.BadRequest("coupon_invalid", "优惠码 %s 已停用", code)
	case cp.StartsAt != nil && now.Before(*cp.StartsAt):
		return cp, 0, apierr.BadRequest("coupon_not_started", "优惠码 %s 还没到可用时间", code)
	case cp.EndsAt != nil && !now.Before(*cp.EndsAt):
		return cp, 0, apierr.BadRequest("coupon_expired", "优惠码 %s 已过期", code)
	case len(cp.PlanIds) > 0 && !slices.Contains(cp.PlanIds, planID):
		return cp, 0, apierr.BadRequest("coupon_not_applicable", "优惠码 %s 不适用于这个套餐", code)
	case len(cp.Periods) > 0 && !slices.Contains(cp.Periods, period):
		return cp, 0, apierr.BadRequest("coupon_not_applicable", "优惠码 %s 不适用于%s", code, orKey(PeriodNames, period))
	case cp.MaxUses > 0 && cp.Used >= cp.MaxUses:
		return cp, 0, apierr.BadRequest("coupon_used_up", "优惠码 %s 已被领完", code)
	}
	if cp.PerUser > 0 && userID != 0 {
		n, err := q.CountUserCouponUses(ctx, db.CountUserCouponUsesParams{CouponID: &cp.ID, UserID: userID})
		if err != nil {
			return cp, 0, err
		}
		if n >= int64(cp.PerUser) {
			return cp, 0, apierr.BadRequest("coupon_limit", "每人限用 %d 次，你已经用过了", cp.PerUser)
		}
	}
	return cp, Discount(cp, amount), nil
}

// Discount 计算优惠金额，不超过原价。
func Discount(cp db.Coupon, amount int64) int64 {
	var d int64
	if cp.Kind == "percent" {
		d = amount * cp.Value / 100
	} else {
		d = cp.Value
	}
	return min(d, amount)
}

// Yuan 把分格式化成元，例如 1990 → 19.90。
func Yuan(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

func orKey(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}

// PlanPrice 是对外展示的一档价格。
type PlanPrice struct {
	Period string `json:"period"`
	Name   string `json:"name"`
	Price  int64  `json:"price"`
	Days   int    `json:"days"`
}

// PriceList 按展示顺序列出价格。
func PriceList(prices map[string]int64) []PlanPrice {
	out := []PlanPrice{}
	for _, p := range SortedPeriods(prices) {
		out = append(out, PlanPrice{Period: p, Name: PeriodNames[p], Price: prices[p], Days: PeriodDays(p)})
	}
	return out
}
