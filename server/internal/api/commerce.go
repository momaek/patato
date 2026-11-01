package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- 订单 ---------------------------------------------------------------

type orderView struct {
	ID             int64      `json:"id"`
	TradeNo        string     `json:"trade_no"`
	UserID         int64      `json:"user_id"`
	Email          string     `json:"email,omitempty"`
	PlanID         *int64     `json:"plan_id"`
	PlanName       string     `json:"plan_name"`
	Period         string     `json:"period"`
	PeriodName     string     `json:"period_name"`
	Kind           string     `json:"kind"`
	Amount         int64      `json:"amount"`
	Discount       int64      `json:"discount"`
	BalanceUsed    int64      `json:"balance_used"`
	Total          int64      `json:"total"`
	Status         string     `json:"status"`
	RefundPending  bool       `json:"refund_pending,omitempty"`
	RefundApproval string     `json:"refund_approval_id,omitempty"`
	Method         string     `json:"method"`
	GatewayTradeNo string     `json:"gateway_trade_no,omitempty"`
	Days           int32      `json:"days"`
	RefundAmount   int64      `json:"refund_amount"`
	RefundDays     int32      `json:"refund_days"`
	Note           string     `json:"note,omitempty"`
	Source         string     `json:"source"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	PaidAt         *time.Time `json:"paid_at"`
	CancelledAt    *time.Time `json:"cancelled_at"`
	RefundedAt     *time.Time `json:"refunded_at"`
	Version        int64      `json:"version"`
}

func orderViewOf(o db.Order, email string) orderView {
	return orderView{ID: o.ID, TradeNo: o.TradeNo, UserID: o.UserID, Email: email, PlanID: o.PlanID, PlanName: o.PlanName,
		Period: o.Period, PeriodName: commerce.PeriodNames[o.Period], Kind: o.Kind, Amount: o.Amount, Discount: o.Discount,
		BalanceUsed: o.BalanceUsed, Total: o.Total, Status: o.Status, Method: o.Method, GatewayTradeNo: o.GatewayTradeNo,
		Days: o.Days, RefundAmount: o.RefundAmount, RefundDays: o.RefundDays, Note: o.Note, Source: o.Source,
		CreatedAt: o.CreatedAt, ExpiresAt: o.ExpiresAt, PaidAt: o.PaidAt, CancelledAt: o.CancelledAt, RefundedAt: o.RefundedAt,
		Version: o.Version}
}

// pendingRefunds 返回正在等待审批的退款：订单 ID → 审批 ID，用于显示「退款审批中」。
func (s *Server) pendingRefunds(c *gin.Context) map[int64]string {
	out := map[int64]string{}
	rows, err := s.d.Store.PendingRefundApprovals(c)
	if err != nil {
		return out
	}
	for _, r := range rows {
		var ts []change.Target
		if json.Unmarshal(r.Targets, &ts) != nil {
			continue
		}
		for _, t := range ts {
			if t.Type == "order" {
				apr := ""
				if a, err := s.d.Store.GetApprovalByChange(c, r.ID); err == nil {
					apr = a.ID
				}
				out[t.ID] = apr
			}
		}
	}
	return out
}

func (s *Server) orderList(c *gin.Context) {
	limit := queryInt(c, "limit", 50, 1, 200)
	p := db.ListOrdersParams{RowLimit: int32(limit + 1)}
	if v := strings.TrimSpace(c.Query("status")); v != "" {
		if v == "refunding" {
			v = "paid"
		}
		p.Status = &v
	}
	if v := strings.TrimSpace(c.Query("q")); v != "" {
		p.Q = &v
	}
	if v := c.Query("user"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			p.UserID = &id
		} else if u, err := s.d.Store.GetUserByEmail(c, v); err == nil {
			p.UserID = &u.ID
		}
	}
	if cur := queryInt(c, "cursor", 0, 0, 1<<62); cur > 0 {
		v := int64(cur)
		p.BeforeID = &v
	}
	rows, err := s.d.Store.ListOrders(c, p)
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
	refunds := s.pendingRefunds(c)
	items := make([]orderView, 0, len(rows))
	for _, r := range rows {
		o := db.Order{ID: r.ID, TradeNo: r.TradeNo, UserID: r.UserID, PlanID: r.PlanID, PlanName: r.PlanName, Period: r.Period,
			Kind: r.Kind, Amount: r.Amount, Discount: r.Discount, BalanceUsed: r.BalanceUsed, Total: r.Total, CouponID: r.CouponID,
			Status: r.Status, Method: r.Method, GatewayTradeNo: r.GatewayTradeNo, Days: r.Days, PrevExpiresAt: r.PrevExpiresAt,
			RefundAmount: r.RefundAmount, RefundDays: r.RefundDays, Note: r.Note, Source: r.Source, CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt, PaidAt: r.PaidAt, CancelledAt: r.CancelledAt, RefundedAt: r.RefundedAt, Version: r.Version}
		v := orderViewOf(o, r.Email)
		if apr, ok := refunds[o.ID]; ok {
			v.RefundPending, v.RefundApproval = true, apr
		}
		if c.Query("status") == "refunding" && !v.RefundPending {
			continue
		}
		items = append(items, v)
	}
	ok(c, gin.H{"items": items, "next_cursor": next})
}

func (s *Server) lookupOrder(c *gin.Context) (db.Order, bool) {
	key := c.Param("id")
	o, err := s.d.Store.GetOrderByTradeNo(c, key)
	if store.IsNotFound(err) {
		if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
			o, err = s.d.Store.GetOrder(c, id)
		}
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("order_not_found", "订单 %s 不存在", key).WithHint("用 GET /api/v1/orders?q=<订单号或邮箱> 搜索"))
		return o, false
	}
	if err != nil {
		fail(c, err)
		return o, false
	}
	return o, true
}

func (s *Server) orderGet(c *gin.Context) {
	o, found := s.lookupOrder(c)
	if !found {
		return
	}
	u, _ := s.d.Store.GetUser(c, o.UserID)
	v := orderViewOf(o, u.Email)
	if apr, ok := s.pendingRefunds(c)[o.ID]; ok {
		v.RefundPending, v.RefundApproval = true, apr
	}
	var coupon any
	if o.CouponID != nil {
		if cp, err := s.d.Store.GetCoupon(c, *o.CouponID); err == nil {
			coupon = gin.H{"id": cp.ID, "code": cp.Code, "name": cp.Name}
		}
	}
	var commission any
	if cm, err := s.d.Store.GetCommissionByOrder(c, o.ID); err == nil {
		commission = gin.H{"id": cm.ID, "inviter_id": cm.InviterID, "amount": cm.Amount, "status": cm.Status, "reason": cm.Reason, "release_at": cm.ReleaseAt}
	}
	callbacks, _ := s.d.Store.ListPaymentCallbacks(c, o.TradeNo)
	cbs := make([]gin.H, 0, len(callbacks))
	for _, cb := range callbacks {
		cbs = append(cbs, gin.H{"id": cb.ID, "method": cb.Method, "verified": cb.Verified, "result": cb.Result, "received_at": cb.ReceivedAt})
	}
	target := "order:" + strconv.FormatInt(o.ID, 10)
	recent, _ := s.d.Store.ListAudit(c, db.ListAuditParams{Target: &target, RowLimit: 20})
	ok(c, gin.H{"order": v, "coupon": coupon, "commission": commission, "callbacks": cbs, "audit": auditViews(recent),
		"user": gin.H{"id": u.ID, "email": u.Email, "expires_at": u.ExpiresAt, "balance": u.Balance}})
}

// ---- 优惠券 -------------------------------------------------------------

type couponView struct {
	ID        int64      `json:"id"`
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Value     int64      `json:"value"`
	PlanIDs   []int64    `json:"plan_ids"`
	Periods   []string   `json:"periods"`
	MaxUses   int32      `json:"max_uses"`
	PerUser   int32      `json:"per_user"`
	Used      int32      `json:"used"`
	StartsAt  *time.Time `json:"starts_at"`
	EndsAt    *time.Time `json:"ends_at"`
	Enabled   bool       `json:"enabled"`
	State     string     `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	Version   int64      `json:"version"`
}

func couponViewOf(cp db.Coupon, now time.Time) couponView {
	state := "active"
	switch {
	case !cp.Enabled:
		state = "disabled"
	case cp.EndsAt != nil && !now.Before(*cp.EndsAt):
		state = "expired"
	case cp.StartsAt != nil && now.Before(*cp.StartsAt):
		state = "scheduled"
	case cp.MaxUses > 0 && cp.Used >= cp.MaxUses:
		state = "used_up"
	}
	return couponView{ID: cp.ID, Code: cp.Code, Name: cp.Name, Kind: cp.Kind, Value: cp.Value, PlanIDs: cp.PlanIds, Periods: cp.Periods,
		MaxUses: cp.MaxUses, PerUser: cp.PerUser, Used: cp.Used, StartsAt: cp.StartsAt, EndsAt: cp.EndsAt, Enabled: cp.Enabled,
		State: state, CreatedAt: cp.CreatedAt, Version: cp.Version}
}

func (s *Server) couponList(c *gin.Context) {
	rows, err := s.d.Store.ListCoupons(c)
	if err != nil {
		fail(c, err)
		return
	}
	now := time.Now()
	items := make([]couponView, 0, len(rows))
	for _, r := range rows {
		items = append(items, couponViewOf(r, now))
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) couponGet(c *gin.Context) {
	key := c.Param("id")
	cp, err := s.d.Store.GetCouponByCode(c, key)
	if store.IsNotFound(err) {
		if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
			cp, err = s.d.Store.GetCoupon(c, id)
		}
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("coupon_not_found", "优惠券 %s 不存在", key))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, couponViewOf(cp, time.Now()))
}

// ---- 返佣 ---------------------------------------------------------------

type commissionView struct {
	ID           int64      `json:"id"`
	InviterID    int64      `json:"inviter_id"`
	InviterEmail string     `json:"inviter_email"`
	InviteeID    *int64     `json:"invitee_id"`
	InviteeEmail string     `json:"invitee_email"`
	TradeNo      string     `json:"trade_no"`
	Amount       int64      `json:"amount"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason"`
	ReleaseAt    time.Time  `json:"release_at"`
	SettledAt    *time.Time `json:"settled_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (s *Server) commissionList(c *gin.Context, inviter *int64, maskEmails bool) ([]commissionView, *int64, error) {
	limit := queryInt(c, "limit", 50, 1, 200)
	p := db.ListCommissionsParams{InviterID: inviter, RowLimit: int32(limit + 1)}
	if v := strings.TrimSpace(c.Query("status")); v != "" {
		p.Status = &v
	}
	if cur := queryInt(c, "cursor", 0, 0, 1<<62); cur > 0 {
		v := int64(cur)
		p.BeforeID = &v
	}
	rows, err := s.d.Store.ListCommissions(c, p)
	if err != nil {
		return nil, nil, err
	}
	var next *int64
	if len(rows) > limit {
		rows = rows[:limit]
		id := rows[len(rows)-1].ID
		next = &id
	}
	out := make([]commissionView, 0, len(rows))
	for _, r := range rows {
		v := commissionView{ID: r.ID, InviterID: r.InviterID, InviterEmail: r.InviterEmail, InviteeID: r.InviteeID, InviteeEmail: deref(r.InviteeEmail),
			TradeNo: r.TradeNo, Amount: r.Amount, Status: r.Status, Reason: r.Reason, ReleaseAt: r.ReleaseAt, SettledAt: r.SettledAt, CreatedAt: r.CreatedAt}
		if maskEmails {
			v.InviterEmail, v.InviteeEmail, v.TradeNo = "", maskEmail(v.InviteeEmail), ""
		}
		out = append(out, v)
	}
	return out, next, nil
}

func (s *Server) referralList(c *gin.Context) {
	var inviter *int64
	if v := c.Query("inviter"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			inviter = &id
		} else if u, err := s.d.Store.GetUserByEmail(c, v); err == nil {
			inviter = &u.ID
		}
	}
	items, next, err := s.commissionList(c, inviter, false)
	if err != nil {
		fail(c, err)
		return
	}
	totals, _ := s.d.Store.CommissionTotals(c, inviter)
	ok(c, gin.H{"items": items, "next_cursor": next, "totals": gin.H{"pending": totals.Pending, "frozen": totals.Frozen, "settled": totals.Settled}})
}

func (s *Server) referralSettingsGet(c *gin.Context) {
	r, err := commerce.LoadRules(c, s.d.Store.Queries)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, r)
}

// maskEmail 把 alice@example.com 显示成 al***@example.com，给邀请人看被邀请人时用。
func maskEmail(e string) string {
	at := strings.Index(e, "@")
	if at <= 0 {
		return e
	}
	name := []rune(e[:at])
	keep := min(2, len(name))
	return string(name[:keep]) + "***" + e[at:]
}
