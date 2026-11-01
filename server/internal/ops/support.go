package ops

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

func findTicket(ctx context.Context, q *db.Queries, r Ref) (db.Ticket, error) {
	id, ok := Ref(strings.TrimPrefix(string(r), "t_")).id()
	if !ok {
		return db.Ticket{}, apierr.BadRequest("missing_ticket", "工单编号 %q 不对", r).WithHint("用 GET /api/v1/tickets 查看")
	}
	t, err := q.GetTicket(ctx, id)
	if store.IsNotFound(err) {
		return t, apierr.NotFound("ticket_not_found", "工单 %s 不存在", r)
	}
	return t, err
}

func ticketTarget(t db.Ticket) change.Target {
	return change.Target{Type: "ticket", ID: t.ID, Version: t.Version}
}

// ---- ticket.draft --------------------------------------------------------

// TicketDraft 给工单保存回复草稿。草稿不会发给用户：发送只接受运营者会话（POST /tickets/{id}:reply）。
type TicketDraft struct{ d *Deps }

func (*TicketDraft) Kind() string      { return "ticket.draft" }
func (*TicketDraft) Risk() change.Risk { return change.Normal }

type ticketDraftParams struct {
	Ticket Ref    `json:"ticket"`
	Body   string `json:"body"`
	// Basis 写明草稿依据了哪些数据，例如「用户状态：已到期 3 天」「订单 2026…：已支付」。
	Basis []string `json:"basis"`
	// NeedsHuman 标为「等你处理」：Agent 回答不了的问题（例如开发票），不编造答复。
	NeedsHuman       bool   `json:"needs_human"`
	NeedsHumanReason string `json:"needs_human_reason"`
	// DraftedBy 由服务端按调用方填写，请求里传了也会被覆盖。
	DraftedBy string `json:"drafted_by"`
}

func (o *TicketDraft) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ticketDraftParams](raw)
	if err != nil {
		return nil, err
	}
	t, err := findTicket(ctx, q, p.Ticket)
	if err != nil {
		return nil, err
	}
	if t.Status == "closed" {
		return nil, apierr.Conflict("ticket_closed", "工单 #%d 已关闭", t.ID)
	}
	p.Body = strings.TrimSpace(p.Body)
	if p.Body == "" && !p.NeedsHuman {
		return nil, apierr.BadRequest("missing_body", "草稿内容为空").WithHint("回答不了时传 needs_human: true 和原因，标为「等你处理」")
	}
	if len([]rune(p.Body)) > 5000 {
		return nil, apierr.BadRequest("body_too_long", "草稿不能超过 5000 字")
	}
	if p.NeedsHuman && strings.TrimSpace(p.NeedsHumanReason) == "" {
		return nil, apierr.BadRequest("missing_reason", "标为「等你处理」时要写明原因")
	}
	steps := []string{}
	if p.Body != "" {
		steps = append(steps, fmt.Sprintf("为工单 #%d「%s」保存回复草稿，不会发给用户；运营者在 Admin 确认后才发送", t.ID, t.Subject))
	}
	if p.NeedsHuman {
		steps = append(steps, "标为「等你处理」："+p.NeedsHumanReason)
	}
	p.Basis = slices.DeleteFunc(p.Basis, func(s string) bool { return strings.TrimSpace(s) == "" })
	if p.Basis == nil {
		p.Basis = []string{}
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("草拟工单 #%d 的回复", t.ID),
		Steps:    steps,
		Diff:     []change.DiffEntry{{Field: "draft", Before: nil, After: truncateRunes(p.Body, 200)}},
		Targets:  []change.Target{ticketTarget(t)},
		Resolved: p,
	}, nil
}

func (o *TicketDraft) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[ticketDraftParams](raw)
	if err != nil {
		return nil, err
	}
	t, err := findTicket(ctx, q, p.Ticket)
	if err != nil {
		return nil, err
	}
	if p.Body != "" {
		if _, err := q.UpsertTicketDraft(ctx, db.UpsertTicketDraftParams{TicketID: t.ID, Body: p.Body, DraftedBy: p.DraftedBy, Basis: mustJSON(p.Basis)}); err != nil {
			return nil, err
		}
	}
	needs, reason := t.NeedsHuman, t.NeedsHumanReason
	if p.NeedsHuman {
		needs, reason = true, p.NeedsHumanReason
	}
	if _, err := q.SetTicketStatus(ctx, db.SetTicketStatusParams{ID: t.ID, Status: t.Status, NeedsHuman: needs, NeedsHumanReason: reason}); err != nil {
		return nil, err
	}
	return map[string]any{"ticket_id": t.ID, "draft_saved": p.Body != "", "needs_human": needs}, nil
}

// ---- ticket.close --------------------------------------------------------

type TicketClose struct{ d *Deps }

func (*TicketClose) Kind() string      { return "ticket.close" }
func (*TicketClose) Risk() change.Risk { return change.Normal }

type ticketRefParams struct {
	Ticket Ref `json:"ticket"`
}

func (o *TicketClose) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ticketRefParams](raw)
	if err != nil {
		return nil, err
	}
	t, err := findTicket(ctx, q, p.Ticket)
	if err != nil {
		return nil, err
	}
	if t.Status == "closed" {
		return nil, apierr.Conflict("ticket_closed", "工单 #%d 已经关闭", t.ID)
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("关闭工单 #%d", t.ID),
		Steps:    []string{fmt.Sprintf("关闭工单 #%d「%s」，未发送的草稿一并删除；用户回复后会重新打开", t.ID, t.Subject)},
		Diff:     []change.DiffEntry{{Field: "status", Before: t.Status, After: "closed"}},
		Targets:  []change.Target{ticketTarget(t)},
		Resolved: map[string]any{"ticket": fmt.Sprint(t.ID)},
	}, nil
}

func (o *TicketClose) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[ticketRefParams](raw)
	if err != nil {
		return nil, err
	}
	t, err := findTicket(ctx, q, p.Ticket)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteTicketDraft(ctx, t.ID); err != nil {
		return nil, err
	}
	c, err := q.SetTicketStatus(ctx, db.SetTicketStatusParams{ID: t.ID, Status: "closed"})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ticket_id": c.ID, "status": c.Status}, nil
}

// ---- webhook.create / webhook.delete ------------------------------------

// WebhookCreate 把事件推送到外部地址。事件里有用户邮箱等数据，外发数据按敏感处理。
type WebhookCreate struct{ d *Deps }

func (*WebhookCreate) Kind() string      { return "webhook.create" }
func (*WebhookCreate) Risk() change.Risk { return change.Sensitive }

type webhookCreateParams struct {
	Name  string   `json:"name"`
	URL   string   `json:"url"`
	Kinds []string `json:"kinds"`
}

type webhookResolved struct {
	webhookCreateParams
	// SecretSealed 是预演时生成、加密后的签名密钥；明文只能由运营者在 Admin 查看。
	SecretSealed string `json:"secret_sealed"`
}

func validKinds(kinds []string) error {
	for _, k := range kinds {
		if !slices.Contains(events.Kinds, k) {
			return apierr.BadRequest("invalid_event_kind", "不支持的事件类型 %q", k).WithHint("可选：%s", strings.Join(events.Kinds, ", "))
		}
	}
	return nil
}

func (o *WebhookCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[webhookCreateParams](raw)
	if err != nil {
		return nil, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, apierr.BadRequest("missing_name", "缺少名称")
	}
	u, err := url.Parse(strings.TrimSpace(p.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, apierr.BadRequest("invalid_url", "地址 %q 不是有效的 http(s) 地址", p.URL)
	}
	p.URL = u.String()
	if err := validKinds(p.Kinds); err != nil {
		return nil, err
	}
	if p.Kinds == nil {
		p.Kinds = []string{}
	}
	if o.d.Box == nil {
		return nil, apierr.ErrInternal
	}
	which := "全部事件"
	if len(p.Kinds) > 0 {
		which = strings.Join(p.Kinds, "、")
	}
	sealed := base64.StdEncoding.EncodeToString(o.d.Box.Seal([]byte(secret.Token("whsec"))))
	return &change.Draft{
		Summary: fmt.Sprintf("新建 Webhook %s", p.Name),
		Steps: []string{
			fmt.Sprintf("把%s推送到 %s（事件内容含用户邮箱等数据）", which, p.URL),
			"每次推送带 X-Patato-Signature（HMAC-SHA256），签名密钥在 Admin 的「集成」页查看",
		},
		Diff:     []change.DiffEntry{{Field: "webhook", Before: nil, After: map[string]any{"name": p.Name, "url": p.URL, "kinds": p.Kinds}}},
		Resolved: webhookResolved{webhookCreateParams: *p, SecretSealed: sealed},
	}, nil
}

func (o *WebhookCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[webhookResolved](raw)
	if err != nil {
		return nil, err
	}
	cipher, err := base64.StdEncoding.DecodeString(r.SecretSealed)
	if err != nil {
		return nil, err
	}
	w, err := q.CreateWebhook(ctx, db.CreateWebhookParams{Name: r.Name, Url: r.URL, SecretCipher: cipher, Kinds: r.Kinds, Enabled: true})
	if err != nil {
		return nil, err
	}
	return map[string]any{"webhook_id": w.ID, "name": w.Name}, nil
}

type WebhookDelete struct{ d *Deps }

func (*WebhookDelete) Kind() string      { return "webhook.delete" }
func (*WebhookDelete) Risk() change.Risk { return change.Normal }

type webhookRefParams struct {
	Webhook Ref `json:"webhook"`
}

func findWebhook(ctx context.Context, q *db.Queries, r Ref) (db.Webhook, error) {
	id, ok := r.id()
	if !ok {
		return db.Webhook{}, apierr.BadRequest("missing_webhook", "Webhook 编号 %q 不对", r)
	}
	w, err := q.GetWebhook(ctx, id)
	if store.IsNotFound(err) {
		return w, apierr.NotFound("webhook_not_found", "Webhook %s 不存在", r).WithHint("用 GET /api/v1/webhooks 查看")
	}
	return w, err
}

func (o *WebhookDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[webhookRefParams](raw)
	if err != nil {
		return nil, err
	}
	w, err := findWebhook(ctx, q, p.Webhook)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除 Webhook %s", w.Name),
		Steps:   []string{fmt.Sprintf("停止向 %s 推送事件", w.Url)},
		Diff:    []change.DiffEntry{{Field: "webhook", Before: w.Name, After: nil}},
		Targets: []change.Target{{Type: "webhook", ID: w.ID, Version: w.Version}},
	}, nil
}

func (o *WebhookDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[webhookRefParams](raw)
	if err != nil {
		return nil, err
	}
	w, err := findWebhook(ctx, q, p.Webhook)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteWebhook(ctx, w.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": w.Name}, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
