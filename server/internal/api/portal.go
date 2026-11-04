package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/password"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// meConfig 告诉用户侧登录前就需要知道的设置：是否开放注册、是否开通在线支付。
func (s *Server) meConfig(c *gin.Context) {
	ok(c, gin.H{
		"service_name":         s.d.Cfg.ServiceName,
		"registration":         s.d.Cfg.Registration.Enabled,
		"registration_invite":  s.d.Cfg.Registration.RequireInvite,
		"payment":              s.d.Commerce != nil && len(s.d.Commerce.Methods()) > 0,
		"tickets":              true,
		"referral_link_prefix": s.d.Cfg.PortalURL + "/register?invite=",
	})
}

// meRegister 注册新用户。未开放注册时返回 403；邀请码只在注册时绑定。
func (s *Server) meRegister(c *gin.Context) {
	if !s.d.Cfg.Registration.Enabled {
		fail(c, apierr.Forbidden("registration_closed", "暂未开放注册").WithHint("联系客服开通账号"))
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Invite   string `json:"invite_code"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Email))
	if err != nil {
		fail(c, apierr.BadRequest("invalid_email", "邮箱格式不对"))
		return
	}
	if err := password.Validate(req.Password); err != nil {
		fail(c, apierr.BadRequest("weak_password", "%v", err))
		return
	}
	if err := s.d.Auth.CheckRegisterRate(c, c.ClientIP()); err != nil {
		fail(c, err)
		return
	}
	var inviter *db.User
	if code := strings.ToLower(strings.TrimSpace(req.Invite)); code != "" {
		u, err := s.d.Store.GetUserByInviteCode(c, &code)
		if err != nil {
			fail(c, apierr.BadRequest("invalid_invite", "邀请码不对"))
			return
		}
		inviter = &u
	} else if s.d.Cfg.Registration.RequireInvite {
		fail(c, apierr.BadRequest("invite_required", "需要邀请码才能注册"))
		return
	}
	var u db.User
	err = s.d.Store.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		var err error
		u, err = q.CreateUser(c, db.CreateUserParams{Email: addr.Address, PasswordHash: password.Hash(req.Password),
			PasswordAlgo: password.Argon2id, Uuid: secret.UUID(), SubToken: secret.HexToken(16)})
		if store.IsUniqueViolation(err) {
			return apierr.Conflict("email_taken", "这个邮箱已注册").WithHint("直接登录，或用「找回密码」")
		}
		if err != nil {
			return err
		}
		var by *int64
		if inviter != nil {
			by = &inviter.ID
		}
		if err := q.SetUserReferral(c, db.SetUserReferralParams{ID: u.ID, InvitedBy: by, RegisterIp: c.ClientIP()}); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"summary": "用户自助注册", "invited_by": by})
		return q.InsertAudit(c, db.InsertAuditParams{SubjectKind: "user", SubjectID: u.ID, SubjectLabel: u.Email, Entry: "portal",
			Action: "user.register", Target: fmt.Sprintf("user:%d", u.ID), Risk: "normal", Result: "success", Detail: detail})
	})
	if err != nil {
		fail(c, err)
		return
	}
	token, exp, err := s.d.Auth.CreateSession(c, auth.KindUser, u.ID, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		fail(c, err)
		return
	}
	s.setCookie(c, cookiePortal, token, exp)
	ok(c, gin.H{"email": u.Email, "expires_at": exp})
}

func (s *Server) commerceOn(c *gin.Context) bool {
	if s.d.Commerce == nil {
		fail(c, apierr.Conflict("payment_unavailable", "暂未开通在线支付"))
		return false
	}
	return true
}

// mePlans 列出在售套餐和这个用户看到的价格（含灰度价格）、可用的支付方式和余额。
func (s *Server) mePlans(c *gin.Context) {
	u := meUser(c)
	rows, err := s.d.Store.ListPlans(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := []gin.H{}
	for _, r := range rows {
		if !r.Sellable {
			continue
		}
		p := db.Plan{ID: r.ID, Prices: r.Prices, RolloutPrices: r.RolloutPrices, RolloutPercent: r.RolloutPercent}
		prices := commerce.Prices(p, u.ID)
		if len(prices) == 0 {
			continue
		}
		items = append(items, gin.H{"code": r.Code, "name": r.Name, "transfer_bytes": r.TransferBytes, "reset_method": r.ResetMethod,
			"device_limit": r.DeviceLimit, "speed_limit_mbps": r.SpeedLimitMbps, "prices": commerce.PriceList(prices),
			"current": u.PlanID != nil && *u.PlanID == r.ID})
	}
	methods := []commerce.Method{}
	if s.d.Commerce != nil {
		methods = s.d.Commerce.Methods()
	}
	ok(c, gin.H{"items": items, "methods": methods, "balance": u.Balance})
}

func (s *Server) meQuote(c *gin.Context) {
	if !s.commerceOn(c) {
		return
	}
	var in commerce.QuoteInput
	if err := bindJSON(c, &in); err != nil {
		fail(c, err)
		return
	}
	q, err := s.d.Commerce.Quote(c, meUser(c), in)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, q)
}

func (s *Server) meOrderCreate(c *gin.Context) {
	if !s.commerceOn(c) {
		return
	}
	var req struct {
		commerce.QuoteInput
		Method string `json:"method"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	res, err := s.d.Commerce.Create(c.Request.Context(), meUser(c).ID, req.QuoteInput, req.Method)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"order": meOrderView(res.Order), "pay_url": res.PayURL})
}

// meOrderView 是给用户看的订单，不含内部备注和网关流水。
func meOrderView(o db.Order) gin.H {
	return gin.H{"trade_no": o.TradeNo, "plan_name": o.PlanName, "period": o.Period, "period_name": commerce.PeriodNames[o.Period],
		"kind": o.Kind, "amount": o.Amount, "discount": o.Discount, "balance_used": o.BalanceUsed, "total": o.Total, "status": o.Status,
		"method": o.Method, "days": o.Days, "refund_amount": o.RefundAmount, "created_at": o.CreatedAt, "expires_at": o.ExpiresAt,
		"paid_at": o.PaidAt, "refunded_at": o.RefundedAt}
}

func (s *Server) meOrders(c *gin.Context) {
	rows, err := s.d.Store.ListUserOrders(c, db.ListUserOrdersParams{UserID: meUser(c).ID, Limit: 100})
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, o := range rows {
		items = append(items, meOrderView(o))
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) meOrder(c *gin.Context) (db.Order, bool) {
	o, err := s.d.Store.GetOrderByTradeNo(c, c.Param("id"))
	if store.IsNotFound(err) || (err == nil && o.UserID != meUser(c).ID) {
		fail(c, apierr.NotFound("order_not_found", "订单不存在"))
		return o, false
	}
	if err != nil {
		fail(c, err)
		return o, false
	}
	return o, true
}

func (s *Server) meOrderGet(c *gin.Context) {
	o, found := s.meOrder(c)
	if !found {
		return
	}
	ok(c, gin.H{"order": meOrderView(o)})
}

// meOrderPay 重新获取待支付订单的付款链接，可以换一种支付方式。
func (s *Server) meOrderPay(c *gin.Context) {
	if !s.commerceOn(c) {
		return
	}
	var req struct {
		Method string `json:"method"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	o, found := s.meOrder(c)
	if !found {
		return
	}
	if o.Status != "pending" {
		fail(c, apierr.Conflict("order_not_pending", "订单当前状态是 %s，不需要支付", o.Status))
		return
	}
	if time.Now().After(o.ExpiresAt) {
		fail(c, apierr.Conflict("order_expired", "订单已超时").WithHint("重新下单"))
		return
	}
	if req.Method != "" && req.Method != o.Method {
		if _, ok := s.d.Commerce.Gateway(req.Method); !ok {
			fail(c, apierr.BadRequest("invalid_method", "不支持的支付方式 %q", req.Method))
			return
		}
		if err := s.d.Store.SetOrderMethod(c, db.SetOrderMethodParams{ID: o.ID, Method: req.Method}); err != nil {
			fail(c, err)
			return
		}
		o.Method = req.Method
	}
	url, err := s.d.Commerce.PayURL(c.Request.Context(), o)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"order": meOrderView(o), "pay_url": url})
}

func (s *Server) meOrderCancel(c *gin.Context) {
	if !s.commerceOn(c) {
		return
	}
	o, err := s.d.Commerce.CancelByUser(c, meUser(c), c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"order": meOrderView(o)})
}

// meReferral 是用户的邀请页：邀请链接、规则、邀请人数、返佣明细和余额。
func (s *Server) meReferral(c *gin.Context) {
	u := meUser(c)
	code, err := commerce.EnsureInviteCode(c, s.d.Store.Queries, u)
	if err != nil {
		fail(c, err)
		return
	}
	rules, err := commerce.LoadRules(c, s.d.Store.Queries)
	if err != nil {
		fail(c, err)
		return
	}
	invited, _ := s.d.Store.CountInvitees(c, &u.ID)
	totals, _ := s.d.Store.CommissionTotals(c, &u.ID)
	items, _, err := s.commissionList(c, &u.ID, true)
	if err != nil {
		fail(c, err)
		return
	}
	link := ""
	if s.d.Cfg.Registration.Enabled {
		link = s.d.Cfg.PortalURL + "/register?invite=" + code
	}
	ok(c, gin.H{"invite_code": code, "invite_url": link, "enabled": rules.Enabled, "rate_percent": rules.RatePercent,
		"wait_days": rules.WaitDays, "invited": invited, "balance": u.Balance,
		"pending": totals.Pending + totals.Frozen, "settled": totals.Settled, "items": items})
}

// meTraffic 是最近 30 天的每日用量（账单明细）。
func (s *Server) meTraffic(c *gin.Context) {
	since := s.today().AddDate(0, 0, -29)
	rows, err := s.d.Store.UserTrafficSince(c, db.UserTrafficSinceParams{UserID: meUser(c).ID, Day: since})
	if err != nil {
		fail(c, err)
		return
	}
	byDay := map[string][2]int64{}
	for _, r := range rows {
		byDay[r.Day.Format("2006-01-02")] = [2]int64{r.Up, r.Down}
	}
	items := make([]gin.H, 0, 30)
	for i := 0; i < 30; i++ {
		d := since.AddDate(0, 0, i).Format("2006-01-02")
		v := byDay[d]
		items = append(items, gin.H{"day": d, "up": v[0], "down": v[1], "bytes": v[0] + v[1]})
	}
	ok(c, gin.H{"items": items})
}

// ---- 支付回调 -----------------------------------------------------------

// paymentNotify 接收支付平台的异步回调：验签通过、金额一致才开通，返回平台要求的确认内容。
func (s *Server) paymentNotify(c *gin.Context) {
	if s.d.Commerce == nil {
		c.String(http.StatusNotFound, "fail")
		return
	}
	ack, err := s.d.Commerce.HandleNotify(c, c.Param("method"), c.Request)
	if err != nil {
		e := apierr.From(err)
		if e == apierr.ErrInternal {
			s.d.Log.Error("payment notify", "method", c.Param("method"), "err", err)
		}
		c.String(e.Status, "fail")
		return
	}
	c.String(http.StatusOK, ack)
}
