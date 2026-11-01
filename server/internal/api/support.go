package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type ticketView struct {
	ID               int64      `json:"id"`
	UserID           int64      `json:"user_id,omitempty"`
	Email            string     `json:"email,omitempty"`
	Subject          string     `json:"subject"`
	Status           string     `json:"status"`
	NeedsHuman       bool       `json:"needs_human,omitempty"`
	NeedsHumanReason string     `json:"needs_human_reason,omitempty"`
	HasDraft         bool       `json:"has_draft,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	LastMessageAt    time.Time  `json:"last_message_at"`
	ClosedAt         *time.Time `json:"closed_at"`
	Version          int64      `json:"version,omitempty"`
}

type ticketMessageView struct {
	ID        int64     `json:"id"`
	Author    string    `json:"author"`
	Operator  string    `json:"operator,omitempty"`
	DraftedBy string    `json:"drafted_by,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func ticketViewOf(t db.Ticket) ticketView {
	return ticketView{ID: t.ID, UserID: t.UserID, Subject: t.Subject, Status: t.Status, NeedsHuman: t.NeedsHuman,
		NeedsHumanReason: t.NeedsHumanReason, CreatedAt: t.CreatedAt, LastMessageAt: t.LastMessageAt, ClosedAt: t.ClosedAt, Version: t.Version}
}

func (s *Server) ticketMessages(c *gin.Context, id int64, forUser bool) []ticketMessageView {
	rows, _ := s.d.Store.ListTicketMessages(c, id)
	out := make([]ticketMessageView, 0, len(rows))
	for _, m := range rows {
		v := ticketMessageView{ID: m.ID, Author: m.Author, Body: m.Body, CreatedAt: m.CreatedAt}
		if !forUser {
			v.Operator, v.DraftedBy = deref(m.OperatorName), m.DraftedBy
		}
		out = append(out, v)
	}
	return out
}

func parseTicketID(key string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(key, "t_"), 10, 64)
	return id, err == nil
}

func (s *Server) ticketList(c *gin.Context) {
	limit := queryInt(c, "limit", 50, 1, 200)
	p := db.ListTicketsParams{RowLimit: int32(limit + 1)}
	if v := strings.TrimSpace(c.Query("status")); v != "" {
		p.Status = &v
	}
	if c.Query("needs_human") == "true" {
		b := true
		p.NeedsHuman = &b
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
	rows, err := s.d.Store.ListTickets(c, p)
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
	items := make([]ticketView, 0, len(rows))
	for _, r := range rows {
		t := db.Ticket{ID: r.ID, UserID: r.UserID, Subject: r.Subject, Status: r.Status, NeedsHuman: r.NeedsHuman,
			NeedsHumanReason: r.NeedsHumanReason, CreatedAt: r.CreatedAt, LastMessageAt: r.LastMessageAt, ClosedAt: r.ClosedAt, Version: r.Version}
		v := ticketViewOf(t)
		v.Email, v.HasDraft = r.Email, r.HasDraft
		items = append(items, v)
	}
	ok(c, gin.H{"items": items, "next_cursor": next})
}

func (s *Server) adminTicket(c *gin.Context) (db.Ticket, bool) {
	id, valid := parseTicketID(c.Param("id"))
	if !valid {
		fail(c, apierr.BadRequest("invalid_ticket", "工单编号不对"))
		return db.Ticket{}, false
	}
	t, err := s.d.Store.GetTicket(c, id)
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("ticket_not_found", "工单 %d 不存在", id))
		return t, false
	}
	if err != nil {
		fail(c, err)
		return t, false
	}
	return t, true
}

// ticketGet 返回工单、对话、草稿，以及草拟回复时常用的用户上下文：用户状态、最近订单、套餐节点状态。
func (s *Server) ticketGet(c *gin.Context) {
	t, found := s.adminTicket(c)
	if !found {
		return
	}
	u, err := s.d.Store.GetUser(c, t.UserID)
	if err != nil {
		fail(c, err)
		return
	}
	v := ticketViewOf(t)
	v.Email = u.Email
	var draft any
	if d, err := s.d.Store.GetTicketDraft(c, t.ID); err == nil {
		var basis []string
		_ = json.Unmarshal(d.Basis, &basis)
		draft = gin.H{"body": d.Body, "drafted_by": d.DraftedBy, "basis": basis, "updated_at": d.UpdatedAt}
	}
	orders, _ := s.d.Store.ListUserOrders(c, db.ListUserOrdersParams{UserID: u.ID, Limit: 5})
	ov := make([]orderView, 0, len(orders))
	for _, o := range orders {
		ov = append(ov, orderViewOf(o, ""))
	}
	nodes := []gin.H{}
	if u.PlanID != nil {
		if rows, err := s.d.Store.ListPlanNodes(c, *u.PlanID); err == nil {
			for _, n := range rows {
				nodes = append(nodes, gin.H{"code": n.Code, "name": n.Name, "state": n.State})
			}
		}
	}
	uv := userViewOf(u, s.planRef(c, u.PlanID))
	ok(c, gin.H{"ticket": v, "messages": s.ticketMessages(c, t.ID, false), "draft": draft,
		"context": gin.H{"user": uv, "balance": u.Balance, "orders": ov, "plan_nodes": nodes}})
}

// ticketReply 由运营者发送回复。只接受运营者会话：CLI / Agent token 只能保存草稿。
func (s *Server) ticketReply(c *gin.Context) {
	var req struct {
		Body      string `json:"body"`
		FromDraft bool   `json:"from_draft"`
		Close     bool   `json:"close"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	req.Body = strings.TrimSpace(req.Body)
	if req.Body == "" {
		fail(c, apierr.BadRequest("missing_body", "回复内容为空"))
		return
	}
	if len([]rune(req.Body)) > 5000 {
		fail(c, apierr.BadRequest("body_too_long", "回复不能超过 5000 字"))
		return
	}
	t, found := s.adminTicket(c)
	if !found {
		return
	}
	op := getPrincipal(c).operator
	var u db.User
	err := s.d.Store.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		var err error
		if u, err = q.GetUser(c, t.UserID); err != nil {
			return err
		}
		draftedBy := ""
		if req.FromDraft {
			if d, err := q.GetTicketDraft(c, t.ID); err == nil {
				draftedBy = d.DraftedBy
			}
		}
		if _, err := q.AddTicketMessage(c, db.AddTicketMessageParams{TicketID: t.ID, Author: "operator", OperatorID: &op.ID, Body: req.Body, DraftedBy: draftedBy}); err != nil {
			return err
		}
		if err := q.DeleteTicketDraft(c, t.ID); err != nil {
			return err
		}
		status := "replied"
		if req.Close {
			status = "closed"
		}
		if _, err := q.SetTicketStatus(c, db.SetTicketStatusParams{ID: t.ID, Status: status, Touch: true}); err != nil {
			return err
		}
		summary := fmt.Sprintf("回复工单 #%d「%s」", t.ID, t.Subject)
		if draftedBy != "" {
			summary += "（基于 " + draftedBy + " 的草稿）"
		}
		detail, _ := json.Marshal(map[string]any{"summary": summary})
		return q.InsertAudit(c, db.InsertAuditParams{SubjectKind: "operator", SubjectID: op.ID, SubjectLabel: op.Name, Entry: "web",
			Action: "ticket.reply", Target: fmt.Sprintf("ticket:%d", t.ID), Risk: "normal", Result: "success", Detail: detail})
	})
	if err != nil {
		fail(c, err)
		return
	}
	s.emit(events.Event{Kind: events.TicketReplied, Subject: fmt.Sprintf("user:%d", u.ID),
		Message: fmt.Sprintf("工单 #%d「%s」已回复 %s", t.ID, t.Subject, u.Email),
		Payload: map[string]any{"ticket_id": t.ID, "user_id": u.ID}})
	s.mailUser(u.Email, s.d.Cfg.ServiceName+" 工单有新回复",
		fmt.Sprintf("你好，\n\n你的工单「%s」有新回复：\n\n%s\n\n在这里查看并继续回复：%s/tickets/%d\n\n%s", t.Subject, req.Body, s.d.Cfg.PortalURL, t.ID, s.d.Cfg.ServiceName))
	c.Params = append(c.Params, gin.Param{Key: "id", Value: strconv.FormatInt(t.ID, 10)})
	s.ticketGet(c)
}

// mailUser 异步发邮件给用户，失败只记日志。
func (s *Server) mailUser(to, subject, body string) {
	if s.d.Mailer == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.d.Mailer.Send(ctx, to, subject, body); err != nil {
			s.d.Log.Error("send mail", "err", err)
		}
	}()
}

func (s *Server) emit(e events.Event) {
	if s.d.Events != nil {
		s.d.Events.Emit(context.Background(), nil, e)
	}
}

// ---- 用户侧工单 ---------------------------------------------------------

func (s *Server) meTickets(c *gin.Context) {
	rows, err := s.d.Store.ListUserTickets(c, meUser(c).ID)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]ticketView, 0, len(rows))
	for _, t := range rows {
		v := ticketViewOf(t)
		v.UserID, v.Version, v.NeedsHuman, v.NeedsHumanReason = 0, 0, false, ""
		items = append(items, v)
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) meTicket(c *gin.Context) (db.Ticket, bool) {
	id, valid := parseTicketID(c.Param("id"))
	t, err := s.d.Store.GetTicket(c, id)
	if !valid || store.IsNotFound(err) || (err == nil && t.UserID != meUser(c).ID) {
		fail(c, apierr.NotFound("ticket_not_found", "工单不存在"))
		return t, false
	}
	if err != nil {
		fail(c, err)
		return t, false
	}
	return t, true
}

func (s *Server) meTicketGet(c *gin.Context) {
	t, found := s.meTicket(c)
	if !found {
		return
	}
	v := ticketViewOf(t)
	v.UserID, v.Version, v.NeedsHuman, v.NeedsHumanReason = 0, 0, false, ""
	ok(c, gin.H{"ticket": v, "messages": s.ticketMessages(c, t.ID, true)})
}

func ticketBody(c *gin.Context, subject bool) (string, string, error) {
	var req struct {
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := bindJSON(c, &req); err != nil {
		return "", "", err
	}
	req.Subject, req.Body = strings.TrimSpace(req.Subject), strings.TrimSpace(req.Body)
	if subject && (req.Subject == "" || len([]rune(req.Subject)) > 100) {
		return "", "", apierr.BadRequest("invalid_subject", "标题不能为空，最多 100 字")
	}
	if req.Body == "" || len([]rune(req.Body)) > 5000 {
		return "", "", apierr.BadRequest("invalid_body", "内容不能为空，最多 5000 字")
	}
	return req.Subject, req.Body, nil
}

func (s *Server) meTicketCreate(c *gin.Context) {
	subject, body, err := ticketBody(c, true)
	if err != nil {
		fail(c, err)
		return
	}
	u := meUser(c)
	var t db.Ticket
	err = s.d.Store.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		var err error
		if t, err = q.CreateTicket(c, db.CreateTicketParams{UserID: u.ID, Subject: subject}); err != nil {
			return err
		}
		_, err = q.AddTicketMessage(c, db.AddTicketMessageParams{TicketID: t.ID, Author: "user", Body: body})
		return err
	})
	if err != nil {
		fail(c, err)
		return
	}
	s.emit(events.Event{Kind: events.TicketCreated, Subject: fmt.Sprintf("ticket:%d", t.ID),
		Message: fmt.Sprintf("%s 提交了工单 #%d「%s」", u.Email, t.ID, subject), Payload: map[string]any{"ticket_id": t.ID, "user_id": u.ID}})
	c.Params = append(c.Params, gin.Param{Key: "id", Value: strconv.FormatInt(t.ID, 10)})
	s.meTicketGet(c)
}

// meTicketMessage 用户追加回复；已关闭的工单随之重新打开。
func (s *Server) meTicketMessage(c *gin.Context) {
	_, body, err := ticketBody(c, false)
	if err != nil {
		fail(c, err)
		return
	}
	t, found := s.meTicket(c)
	if !found {
		return
	}
	err = s.d.Store.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		if _, err := q.AddTicketMessage(c, db.AddTicketMessageParams{TicketID: t.ID, Author: "user", Body: body}); err != nil {
			return err
		}
		_, err := q.SetTicketStatus(c, db.SetTicketStatusParams{ID: t.ID, Status: "open", NeedsHuman: t.NeedsHuman, NeedsHumanReason: t.NeedsHumanReason, Touch: true})
		return err
	})
	if err != nil {
		fail(c, err)
		return
	}
	s.meTicketGet(c)
}

func (s *Server) meTicketClose(c *gin.Context) {
	t, found := s.meTicket(c)
	if !found {
		return
	}
	if t.Status != "closed" {
		if _, err := s.d.Store.SetTicketStatus(c, db.SetTicketStatusParams{ID: t.ID, Status: "closed"}); err != nil {
			fail(c, err)
			return
		}
		_ = s.d.Store.DeleteTicketDraft(c, t.ID)
	}
	s.meTicketGet(c)
}
