package commerce

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// Emitter 记录事件，由 events.Recorder 实现。
type Emitter interface {
	Emit(ctx context.Context, q *db.Queries, e events.Event)
}

type Config struct {
	OrderTimeout time.Duration
	// NotifyBase 是支付回调的地址前缀（API 对外地址），ReturnBase 是支付完成后跳回的用户侧地址。
	NotifyBase string
	ReturnBase string
	Loc        *time.Location
}

type Service struct {
	st       *store.Store
	ev       Emitter
	cfg      Config
	gateways map[string]Gateway
	order    []string
	log      *slog.Logger
	Now      func() time.Time
}

func NewService(st *store.Store, ev Emitter, cfg Config, gateways []Gateway, log *slog.Logger) *Service {
	s := &Service{st: st, ev: ev, cfg: cfg, gateways: map[string]Gateway{}, log: log}
	for _, g := range gateways {
		s.gateways[g.Code()] = g
		s.order = append(s.order, g.Code())
	}
	if s.cfg.OrderTimeout <= 0 {
		s.cfg.OrderTimeout = 30 * time.Minute
	}
	if s.cfg.Loc == nil {
		s.cfg.Loc = time.UTC
	}
	return s
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Method 是一种可选的支付方式。
type Method struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (s *Service) Methods() []Method {
	out := []Method{}
	for _, c := range s.order {
		out = append(out, Method{Code: c, Name: s.gateways[c].Name()})
	}
	return out
}

func (s *Service) Gateway(code string) (Gateway, bool) {
	g, ok := s.gateways[code]
	return g, ok
}

// Quote 预演一笔订单，不改动任何数据。
func (s *Service) Quote(ctx context.Context, u db.User, in QuoteInput) (*Quote, error) {
	return MakeQuote(ctx, s.st.Queries, u, in, s.now())
}

// Created 是下单结果：实付为 0 时订单已直接履约。
type Created struct {
	Order  db.Order
	PayURL string
}

// Create 下单：锁住用户核对余额，占用优惠券次数和余额；实付为 0 时直接完成。
func (s *Service) Create(ctx context.Context, userID int64, in QuoteInput, method string) (*Created, error) {
	now := s.now()
	var out Created
	var paid *Paid
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		u, err := q.LockUserForUpdate(ctx, userID)
		if err != nil {
			return err
		}
		qt, err := MakeQuote(ctx, q, u, in, now)
		if err != nil {
			return err
		}
		if qt.Total > 0 {
			if _, ok := s.gateways[method]; !ok {
				if len(s.order) == 0 {
					return apierr.Conflict("payment_unavailable", "暂未开通在线支付").WithHint("请联系客服续费")
				}
				return apierr.BadRequest("invalid_method", "不支持的支付方式 %q", method).WithHint("可选：%s", strings.Join(s.order, ", "))
			}
		}
		var couponID *int64
		if qt.Coupon != nil {
			// 锁住优惠券再核对一次总次数，避免并发下单超发
			cp, err := q.LockCoupon(ctx, qt.Coupon.ID)
			if err != nil {
				return err
			}
			if cp.MaxUses > 0 && cp.Used >= cp.MaxUses {
				return apierr.BadRequest("coupon_used_up", "优惠码 %s 已被领完", cp.Code)
			}
			if err := q.AddCouponUse(ctx, db.AddCouponUseParams{ID: cp.ID, Delta: 1}); err != nil {
				return err
			}
			couponID = &cp.ID
		}
		if qt.BalanceUsed > 0 {
			if _, err := q.AddUserBalance(ctx, db.AddUserBalanceParams{ID: u.ID, Delta: -qt.BalanceUsed}); err != nil {
				return err
			}
		}
		planID := qt.Plan.ID
		if qt.Total == 0 {
			method = "balance"
		}
		o, err := q.CreateOrder(ctx, db.CreateOrderParams{
			TradeNo: NewTradeNo(now), UserID: u.ID, PlanID: &planID, PlanName: qt.PlanName, Period: qt.Period, Kind: qt.Kind,
			Amount: qt.Amount, Discount: qt.Discount, BalanceUsed: qt.BalanceUsed, Total: qt.Total, CouponID: couponID,
			Method: method, Source: "portal", ExpiresAt: now.Add(s.cfg.OrderTimeout),
		})
		if err != nil {
			return err
		}
		if qt.Total == 0 {
			paid, err = Fulfill(ctx, q, o, FulfillInput{Method: "balance", Note: "余额或优惠全额抵扣"}, now, s.cfg.Loc)
			if err != nil {
				return err
			}
			o = paid.Order
		}
		out.Order = o
		return audit(ctx, q, "user", u.ID, u.Email, "portal", "order.create", o, map[string]any{
			"summary": fmt.Sprintf("下单 %s（%s），实付 %s 元", o.PlanName, orKey(PeriodNames, o.Period), Yuan(o.Total)),
		})
	})
	if err != nil {
		return nil, err
	}
	if paid != nil {
		s.afterPaid(ctx, paid)
		return &out, nil
	}
	out.PayURL, err = s.PayURL(out.Order)
	return &out, err
}

// PayURL 生成待支付订单的付款链接。
func (s *Service) PayURL(o db.Order) (string, error) {
	if o.Status != "pending" || o.Total == 0 {
		return "", nil
	}
	g, ok := s.gateways[o.Method]
	if !ok {
		return "", apierr.Conflict("payment_unavailable", "支付方式 %s 已不可用", o.Method).WithHint("取消这笔订单后重新下单")
	}
	return g.PayURL(o, s.cfg.NotifyBase+"/api/v1/payment/"+g.Code()+"/notify", s.cfg.ReturnBase+"/orders/"+o.TradeNo)
}

// NewTradeNo 生成订单号：时间加随机串，便于人工对账。
func NewTradeNo(now time.Time) string {
	return now.UTC().Format("20060102150405") + strings.ToUpper(secret.HexToken(4))
}

// Cancel 取消待支付订单，退回占用的优惠券次数和余额。
func Cancel(ctx context.Context, q *db.Queries, o db.Order) (db.Order, error) {
	c, err := q.SetOrderCancelled(ctx, o.ID)
	if store.IsNotFound(err) {
		return o, apierr.Conflict("order_not_pending", "订单 %s 当前状态是 %s，不能取消", o.TradeNo, o.Status)
	}
	if err != nil {
		return o, err
	}
	if o.CouponID != nil {
		if err := q.AddCouponUse(ctx, db.AddCouponUseParams{ID: *o.CouponID, Delta: -1}); err != nil {
			return o, err
		}
	}
	if o.BalanceUsed > 0 {
		if _, err := q.AddUserBalance(ctx, db.AddUserBalanceParams{ID: o.UserID, Delta: o.BalanceUsed}); err != nil {
			return o, err
		}
	}
	return c, nil
}

// CancelByUser 由用户本人取消待支付订单。
func (s *Service) CancelByUser(ctx context.Context, u db.User, tradeNo string) (db.Order, error) {
	var out db.Order
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		o, err := q.LockOrderByTradeNo(ctx, tradeNo)
		if store.IsNotFound(err) || (err == nil && o.UserID != u.ID) {
			return apierr.NotFound("order_not_found", "订单 %s 不存在", tradeNo)
		}
		if err != nil {
			return err
		}
		if out, err = Cancel(ctx, q, o); err != nil {
			return err
		}
		return audit(ctx, q, "user", u.ID, u.Email, "portal", "order.cancel", o, map[string]any{"summary": "用户取消订单 " + o.TradeNo})
	})
	return out, err
}

// ExpireDue 取消超时未支付的订单。
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	nos, err := s.st.DueOrderTimeouts(ctx, 200)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, no := range nos {
		err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
			o, err := q.LockOrderByTradeNo(ctx, no)
			if err != nil || o.Status != "pending" {
				return err
			}
			if _, err := Cancel(ctx, q, o); err != nil {
				return err
			}
			return audit(ctx, q, "system", 0, "系统", "system", "order.cancel", o, map[string]any{"summary": "订单超时未支付，自动取消"})
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// FulfillInput 描述一次付款确认的来源。
type FulfillInput struct {
	Method         string
	GatewayTradeNo string
	Note           string
}

// Paid 是履约结果，包含需要在事务提交后发出的事件。
type Paid struct {
	Order      db.Order
	User       db.User
	Already    bool
	Commission *db.Commission
	Events     []events.Event
}

// Fulfill 确认订单已付款并开通：续费从当前到期日接着算，换套餐和新购从现在算，流量重置包只清零流量。
// 调用方负责在事务里锁住订单。已付款的订单再次确认时什么也不做（支付回调会重发）。
func Fulfill(ctx context.Context, q *db.Queries, o db.Order, in FulfillInput, now time.Time, loc *time.Location) (*Paid, error) {
	if o.Status == "paid" || o.Status == "refunded" {
		return &Paid{Order: o, Already: true}, nil
	}
	if o.Status == "cancelled" {
		// 超时取消后才收到付款：重新占用优惠券和余额，照常开通。
		if o.CouponID != nil {
			if err := q.AddCouponUse(ctx, db.AddCouponUseParams{ID: *o.CouponID, Delta: 1}); err != nil {
				return nil, err
			}
		}
		if o.BalanceUsed > 0 {
			if _, err := q.AddUserBalance(ctx, db.AddUserBalanceParams{ID: o.UserID, Delta: -o.BalanceUsed}); err != nil {
				return nil, err
			}
		}
	}
	u, err := q.LockUserForUpdate(ctx, o.UserID)
	if err != nil {
		return nil, err
	}
	prevExpiry := u.ExpiresAt
	res := &Paid{}
	days := 0
	var pl db.Plan
	planOK := false
	if o.PlanID != nil {
		pl, err = q.GetPlan(ctx, *o.PlanID)
		planOK = err == nil
	}
	if !planOK {
		// 套餐在付款前被删了：钱已经收到，先记为已付，由运营者手动处理。
		in.Note = strings.TrimSpace(in.Note + " 套餐已不存在，未自动开通，需要人工处理")
		res.Events = append(res.Events, events.Event{
			Kind: events.OrderNeedsAttention, Severity: "critical", Subject: fmt.Sprintf("order:%d", o.ID),
			Message:  fmt.Sprintf("订单 %s 已付款，但套餐「%s」已不存在，没有自动开通", o.TradeNo, o.PlanName),
			DedupKey: fmt.Sprintf("%s:%d", events.OrderNeedsAttention, o.ID),
		})
	} else {
		planID := pl.ID
		next := db.ApplyUserPurchaseParams{ID: u.ID, PlanID: &planID, TransferBytes: pl.TransferBytes, ExpiresAt: u.ExpiresAt,
			ResetAnchor: u.ResetAnchor, NextResetAt: u.NextResetAt}
		pd := PeriodDays(o.Period)
		switch o.Kind {
		case "reset_traffic":
			next.ResetUsed = true
			next.NextResetAt = billing.NextReset(pl.ResetMethod, u.ResetAnchor, now, loc)
		case "renew":
			if o.Period == PeriodOnetime {
				next.ExpiresAt = nil
			} else {
				base := now
				if u.ExpiresAt != nil && u.ExpiresAt.After(now) {
					base = *u.ExpiresAt
				}
				t := base.AddDate(0, 0, pd).UTC()
				next.ExpiresAt, days = &t, pd
			}
			if u.ExpiresAt != nil && !u.ExpiresAt.After(now) {
				// 已经到期后才续费：从现在起重新计算流量周期
				next.ResetUsed, next.ResetAnchor = true, &now
				next.NextResetAt = billing.NextReset(pl.ResetMethod, &now, now, loc)
			}
		default: // new、change
			next.ResetUsed, next.ResetAnchor = true, &now
			next.NextResetAt = billing.NextReset(pl.ResetMethod, &now, now, loc)
			if o.Period == PeriodOnetime {
				next.ExpiresAt = nil
			} else {
				t := now.AddDate(0, 0, pd).UTC()
				next.ExpiresAt, days = &t, pd
			}
		}
		if u, err = q.ApplyUserPurchase(ctx, next); err != nil {
			return nil, err
		}
	}
	paid, err := q.SetOrderPaid(ctx, db.SetOrderPaidParams{ID: o.ID, Method: in.Method, GatewayTradeNo: in.GatewayTradeNo,
		Days: int32(days), PrevExpiresAt: prevExpiry, Note: in.Note})
	if err != nil {
		return nil, err
	}
	res.Order, res.User = paid, u
	res.Events = append(res.Events, events.Event{
		Kind: events.OrderPaid, Subject: fmt.Sprintf("order:%d", o.ID),
		Message: fmt.Sprintf("%s 支付了订单 %s：%s %s，实付 %s 元", u.Email, o.TradeNo, o.PlanName, orKey(PeriodNames, o.Period), Yuan(o.Total)),
		Payload: map[string]any{"order_id": o.ID, "trade_no": o.TradeNo, "user_id": u.ID, "total": o.Total},
	})
	if c, ev, err := accrueCommission(ctx, q, paid, u, now); err != nil {
		return nil, err
	} else if c != nil {
		res.Commission = c
		if ev != nil {
			res.Events = append(res.Events, *ev)
		}
	}
	return res, nil
}

// HandleNotify 处理支付回调：验签、核对金额、履约。验签失败或金额不符都不开通。
func (s *Service) HandleNotify(ctx context.Context, code string, values map[string][]string) (string, error) {
	g, ok := s.gateways[code]
	if !ok {
		return "", apierr.NotFound("payment_method_not_found", "支付方式 %s 不存在", code)
	}
	payload, _ := json.Marshal(values)
	n, verr := g.Verify(values)
	record := func(verified bool, result string) {
		if err := s.st.InsertPaymentCallback(ctx, db.InsertPaymentCallbackParams{Method: code, TradeNo: n.TradeNo, Verified: verified, Result: result, Payload: payload}); err != nil {
			s.log.Error("record payment callback", "err", err)
		}
	}
	if verr != nil {
		record(false, verr.Error())
		return "", apierr.BadRequest("invalid_signature", "回调验签失败")
	}
	if !n.Paid {
		record(true, "未支付状态："+n.Status)
		return g.Ack(), nil
	}
	var paid *Paid
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		o, err := q.LockOrderByTradeNo(ctx, n.TradeNo)
		if store.IsNotFound(err) {
			return apierr.NotFound("order_not_found", "订单 %s 不存在", n.TradeNo)
		}
		if err != nil {
			return err
		}
		if n.Amount != o.Total {
			s.ev.Emit(ctx, nil, events.Event{
				Kind: events.OrderNeedsAttention, Severity: "critical", Subject: fmt.Sprintf("order:%d", o.ID),
				Message:  fmt.Sprintf("订单 %s 的支付回调金额 %s 元与应付 %s 元不符，没有开通", o.TradeNo, Yuan(n.Amount), Yuan(o.Total)),
				DedupKey: fmt.Sprintf("payment_mismatch:%d", o.ID),
			})
			return apierr.BadRequest("amount_mismatch", "金额不符")
		}
		if paid, err = Fulfill(ctx, q, o, FulfillInput{Method: code, GatewayTradeNo: n.GatewayTradeNo}, s.now(), s.cfg.Loc); err != nil {
			return err
		}
		if paid.Already {
			return nil
		}
		return audit(ctx, q, "system", 0, g.Name()+" 回调", "payment", "order.paid", o, map[string]any{
			"summary": fmt.Sprintf("订单 %s 支付成功（%s 流水 %s），实付 %s 元", o.TradeNo, g.Name(), n.GatewayTradeNo, Yuan(o.Total)),
		})
	})
	if err != nil {
		record(true, apierr.From(err).Message)
		return "", err
	}
	record(true, "ok")
	if !paid.Already {
		s.afterPaid(ctx, paid)
	}
	return g.Ack(), nil
}

func (s *Service) afterPaid(ctx context.Context, p *Paid) {
	for _, e := range p.Events {
		s.ev.Emit(ctx, nil, e)
	}
}

// SettleDue 把等待期已过、没有退款的返佣入账到邀请人余额。
func (s *Service) SettleDue(ctx context.Context) (int, error) {
	n := 0
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		rows, err := q.DueCommissions(ctx, 200)
		if err != nil {
			return err
		}
		for _, c := range rows {
			if err := settle(ctx, q, c, "等待期满，入账"); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func settle(ctx context.Context, q *db.Queries, c db.Commission, reason string) error {
	if err := q.SetCommissionStatus(ctx, db.SetCommissionStatusParams{ID: c.ID, Status: "settled", Reason: reason}); err != nil {
		return err
	}
	_, err := q.AddUserBalance(ctx, db.AddUserBalanceParams{ID: c.InviterID, Delta: c.Amount})
	return err
}

// Release 解冻一笔返佣：等待期已过直接入账，否则回到待入账。
func Release(ctx context.Context, q *db.Queries, c db.Commission, now time.Time) error {
	if !c.ReleaseAt.After(now) {
		return settle(ctx, q, c, "人工解冻，入账")
	}
	return q.SetCommissionStatus(ctx, db.SetCommissionStatusParams{ID: c.ID, Status: "pending", Reason: "人工解冻，等待期满后入账"})
}

// ReverseCommission 在订单退款时撤销返佣：未入账的直接作废，已入账的从邀请人余额扣回。
func ReverseCommission(ctx context.Context, q *db.Queries, orderID int64) (*db.Commission, bool, error) {
	c, err := q.GetCommissionByOrder(ctx, orderID)
	if store.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil || c.Status == "cancelled" {
		return nil, false, err
	}
	clawback := c.Status == "settled"
	reason := "订单退款，返佣作废"
	if clawback {
		reason = "订单退款，已从邀请人余额扣回"
		if _, err := q.AddUserBalance(ctx, db.AddUserBalanceParams{ID: c.InviterID, Delta: -c.Amount}); err != nil {
			return nil, false, err
		}
	}
	if err := q.SetCommissionStatus(ctx, db.SetCommissionStatusParams{ID: c.ID, Status: "cancelled", Reason: reason}); err != nil {
		return nil, false, err
	}
	return &c, clawback, nil
}

// audit 写一条非变更计划的审计（用户自助、支付回调、系统任务）。
func audit(ctx context.Context, q *db.Queries, kind string, id int64, label, entry, action string, o db.Order, detail map[string]any) error {
	b, _ := json.Marshal(detail)
	return q.InsertAudit(ctx, db.InsertAuditParams{
		SubjectKind: kind, SubjectID: id, SubjectLabel: label, Entry: entry, Action: action,
		Target: fmt.Sprintf("order:%d", o.ID), Risk: "normal", Result: "success", Detail: b,
	})
}
