package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- 报表：流量、收入、节点成本 ------------------------------------------

func (s *Server) reportDays(c *gin.Context) (int, time.Time) {
	days := queryInt(c, "days", 30, 1, 366)
	return days, s.today().AddDate(0, 0, -(days - 1))
}

func (s *Server) reportTraffic(c *gin.Context) {
	days, since := s.reportDays(c)
	rows, err := s.d.Store.DailyTrafficTotals(c, since)
	if err != nil {
		fail(c, err)
		return
	}
	byDay := map[string]db.DailyTrafficTotalsRow{}
	var total int64
	for _, r := range rows {
		byDay[r.Day.Format("2006-01-02")] = r
		total += r.Up + r.Down
	}
	series := make([]gin.H, 0, days)
	for i := 0; i < days; i++ {
		d := since.AddDate(0, 0, i).Format("2006-01-02")
		r := byDay[d]
		series = append(series, gin.H{"day": d, "up": r.Up, "down": r.Down, "bytes": r.Up + r.Down})
	}
	top, _ := s.d.Store.TopUsersByTraffic(c, db.TopUsersByTrafficParams{Day: since, Limit: 10})
	topUsers := make([]gin.H, 0, len(top))
	for _, t := range top {
		topUsers = append(topUsers, gin.H{"user_id": t.UserID, "email": t.Email, "bytes": t.Bytes})
	}
	ok(c, gin.H{"days": days, "since": since.Format("2006-01-02"), "total_bytes": total, "series": series, "top_users": topUsers})
}

func (s *Server) reportRevenue(c *gin.Context) {
	days, since := s.reportDays(c)
	start := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, s.loc)
	tz := s.loc.String()
	rev, err := s.d.Store.DailyRevenue(c, db.DailyRevenueParams{Tz: tz, Since: start})
	if err != nil {
		fail(c, err)
		return
	}
	refunds, err := s.d.Store.DailyRefunds(c, db.DailyRefundsParams{Tz: tz, Since: start})
	if err != nil {
		fail(c, err)
		return
	}
	type day struct {
		Day         string `json:"day"`
		Orders      int64  `json:"orders"`
		Revenue     int64  `json:"revenue"`
		Refunds     int64  `json:"refunds"`
		BalanceUsed int64  `json:"balance_used"`
		Discount    int64  `json:"discount"`
	}
	byDay := map[string]*day{}
	series := make([]*day, 0, days)
	for i := 0; i < days; i++ {
		d := &day{Day: since.AddDate(0, 0, i).Format("2006-01-02")}
		byDay[d.Day] = d
		series = append(series, d)
	}
	var totalRev, totalRefund, orders int64
	for _, r := range rev {
		if d := byDay[r.Day.Format("2006-01-02")]; d != nil {
			d.Orders, d.Revenue, d.BalanceUsed, d.Discount = r.Orders, r.Revenue, r.BalanceUsed, r.Discount
		}
		totalRev += r.Revenue
		orders += r.Orders
	}
	for _, r := range refunds {
		if d := byDay[r.Day.Format("2006-01-02")]; d != nil {
			d.Refunds = r.Amount
		}
		totalRefund += r.Amount
	}
	plans, _ := s.d.Store.RevenueByPlan(c, &start)
	byPlan := make([]gin.H, 0, len(plans))
	for _, p := range plans {
		byPlan = append(byPlan, gin.H{"plan": p.PlanName, "orders": p.Orders, "revenue": p.Revenue})
	}
	ok(c, gin.H{"days": days, "since": since.Format("2006-01-02"), "revenue": totalRev, "refunds": totalRefund,
		"net": totalRev - totalRefund, "orders": orders, "series": series, "by_plan": byPlan})
}

// reportNodes 按节点统计这段时间的流量和成本（月成本按天数折算），给出每 GB 成本。
func (s *Server) reportNodes(c *gin.Context) {
	days, since := s.reportDays(c)
	nodes, err := s.d.Store.ListNodes(c)
	if err != nil {
		fail(c, err)
		return
	}
	traffic := map[int64]int64{}
	if rows, err := s.d.Store.NodeTrafficSince(c, since); err == nil {
		for _, r := range rows {
			traffic[r.NodeID] = r.Bytes
		}
	}
	users := map[int64]int64{}
	items := make([]gin.H, 0, len(nodes))
	var totalCost, totalBytes int64
	for _, n := range nodes {
		if _, ok := users[n.ID]; !ok {
			users[n.ID], _ = s.d.Store.CountUsersOfNode(c, n.ID)
		}
		cost := n.MonthlyCost * int64(days) / 30
		bytes := traffic[n.ID]
		var perGB *int64
		if gbs := float64(bytes) / (1 << 30); gbs >= 1 && cost > 0 {
			v := int64(float64(cost) / gbs)
			perGB = &v
		}
		totalCost += cost
		totalBytes += bytes
		items = append(items, gin.H{"id": n.ID, "code": n.Code, "name": n.Name, "region": n.Region, "state": n.State,
			"monthly_cost": n.MonthlyCost, "cost": cost, "bytes": bytes, "cost_per_gb": perGB, "users": users[n.ID]})
	}
	ok(c, gin.H{"days": days, "since": since.Format("2006-01-02"), "total_cost": totalCost, "total_bytes": totalBytes, "items": items})
}

// ---- 审计检索与导出 ------------------------------------------------------

func (s *Server) auditParams(c *gin.Context, limit int) (db.ListAuditRangeParams, error) {
	p := db.ListAuditRangeParams{RowLimit: int32(limit)}
	opt := func(key string) *string {
		if v := strings.TrimSpace(c.Query(key)); v != "" {
			return &v
		}
		return nil
	}
	p.Q, p.Entry, p.Result, p.Target, p.Action = opt("q"), opt("entry"), opt("result"), opt("target"), opt("action")
	for key, dst := range map[string]**time.Time{"since": &p.Since, "until": &p.Until} {
		v := strings.TrimSpace(c.Query(key))
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			d, derr := time.ParseInLocation("2006-01-02", v, s.loc)
			if derr != nil {
				return p, apierr.BadRequest("invalid_time", "%s 应为 YYYY-MM-DD 或 RFC 3339 时间", key)
			}
			t = d
			if key == "until" {
				t = t.AddDate(0, 0, 1) // 按天筛选时包含当天
			}
		}
		*dst = &t
	}
	return p, nil
}

// auditExport 导出审计日志（CSV 或 JSON），最多 10,000 条。
func (s *Server) auditExport(c *gin.Context) {
	p, err := s.auditParams(c, 10000)
	if err != nil {
		fail(c, err)
		return
	}
	rows, err := s.d.Store.ListAuditRange(c, p)
	if err != nil {
		fail(c, err)
		return
	}
	name := "patato-audit-" + time.Now().In(s.loc).Format("20060102-1504")
	if c.Query("format") == "json" {
		c.Header("Content-Disposition", `attachment; filename="`+name+`.json"`)
		ok(c, gin.H{"items": auditViews(rows)})
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte("\xef\xbb\xbf")) // BOM，方便 Excel 识别 UTF-8
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"id", "time", "subject", "actor_claim", "entry", "action", "target", "risk", "change_id", "result", "summary"})
	for _, r := range rows {
		var d struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(r.Detail, &d)
		_ = w.Write([]string{strconv.FormatInt(r.ID, 10), r.At.In(s.loc).Format(time.RFC3339), r.SubjectLabel, r.ActorClaim, r.Entry,
			r.Action, r.Target, r.Risk, r.ChangeID, r.Result, d.Summary})
	}
	w.Flush()
}

// ---- Webhook 与自动处置 --------------------------------------------------

func (s *Server) webhookList(c *gin.Context) {
	rows, err := s.d.Store.ListWebhooks(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, w := range rows {
		items = append(items, gin.H{"id": w.ID, "name": w.Name, "url": w.Url, "kinds": w.Kinds, "enabled": w.Enabled,
			"last_status": w.LastStatus, "last_delivery_at": w.LastDeliveryAt, "created_at": w.CreatedAt, "version": w.Version})
	}
	ok(c, gin.H{"items": items})
}

// webhookSecret 返回签名密钥。只给运营者会话：拿到它就能伪造推送。
func (s *Server) webhookSecret(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, apierr.BadRequest("invalid_webhook", "Webhook 编号不对"))
		return
	}
	w, err := s.d.Store.GetWebhook(c, id)
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("webhook_not_found", "Webhook %d 不存在", id))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	secret, err := s.d.Box.Open(w.SecretCipher)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"id": w.ID, "secret": string(secret)})
}

func (s *Server) automationList(c *gin.Context) {
	rows, err := s.d.Store.ListAutomationRules(c)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{"id": r.ID, "name": r.Name, "event": r.EventKind, "action": r.Action, "params": r.Params,
			"enabled": r.Enabled, "last_fired_at": r.LastFiredAt, "last_result": r.LastResult, "created_at": r.CreatedAt, "version": r.Version})
	}
	ok(c, gin.H{"items": items})
}

// ---- 套餐版本 -----------------------------------------------------------

func (s *Server) planRevisions(c *gin.Context) {
	key := c.Param("id")
	var p db.Plan
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		p, err = s.d.Store.GetPlan(c, id)
	} else {
		p, err = s.d.Store.GetPlanByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("plan_not_found", "套餐 %s 不存在", key))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	rows, err := s.d.Store.ListPlanRevisions(c, p.ID)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		items = append(items, gin.H{"version": r.Version, "snapshot": r.Snapshot, "replaced_at": r.CreatedAt})
	}
	ok(c, gin.H{"plan": gin.H{"id": p.ID, "code": p.Code, "version": p.Version}, "items": items})
}

// ---- 审批一次性码 -------------------------------------------------------

func (s *Server) approvalSendCode(c *gin.Context) {
	exp, err := s.d.Approvals.SendCode(c, c.Param("id"), getPrincipal(c).operator)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"sent": true, "via": "telegram", "expires_at": exp, "note": fmt.Sprintf("一次性码已私聊发到你绑定的 Telegram，%d 分钟内有效", 5)})
}
