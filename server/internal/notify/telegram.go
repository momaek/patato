// Package notify 负责把审批卡片和告警推送给人：Telegram 与邮件。
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/momaek/patato/server/internal/approval"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// Telegram 推送审批卡片与告警，并处理审批按钮的回调。
//
// 回调数据带服务端签名，且只认已绑定的运营者 Telegram 账号；按钮被别人点了也不会生效。
type Telegram struct {
	b         *bot.Bot
	chatID    int64
	st        *store.Store
	approvals *approval.Service
	box       *secret.Box
	loc       *time.Location
	adminURL  string
	log       *slog.Logger

	bindMu sync.Mutex
	binds  map[string]bindCode
}

type bindCode struct {
	operatorID int64
	expires    time.Time
}

func NewTelegram(token string, chatID int64, st *store.Store, approvals *approval.Service, box *secret.Box,
	loc *time.Location, adminURL string, log *slog.Logger) (*Telegram, error) {
	t := &Telegram{chatID: chatID, st: st, approvals: approvals, box: box, loc: loc, adminURL: adminURL, log: log, binds: map[string]bindCode{}}
	b, err := bot.New(token,
		bot.WithSkipGetMe(),
		bot.WithCallbackQueryDataHandler("apr:", bot.MatchTypePrefix, t.onCallback),
		bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {}),
	)
	if err != nil {
		return nil, err
	}
	b.RegisterHandler(bot.HandlerTypeMessageText, "/bind", bot.MatchTypePrefix, t.onBind)
	t.b = b
	return t, nil
}

// Run 以长轮询方式接收回调，阻塞直到 ctx 结束。
func (t *Telegram) Run(ctx context.Context) { t.b.Start(ctx) }

// Send 发送一条纯文本消息到配置的会话。
func (t *Telegram) Send(ctx context.Context, text string) error {
	_, err := t.b.SendMessage(ctx, &bot.SendMessageParams{ChatID: t.chatID, Text: text})
	return err
}

// SendApproval 推送审批卡片，并记下消息 ID，方便审批结束后更新卡片。
func (t *Telegram) SendApproval(ctx context.Context, approvalID string) error {
	d, err := t.approvals.Get(ctx, approvalID)
	if err != nil {
		return err
	}
	text := approval.Describe(d, t.loc)
	if t.adminURL != "" {
		text += "\n\n详情：" + t.adminURL + "/approvals/" + approvalID
	}
	msg, err := t.b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: t.chatID,
		Text:   text,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "批准", CallbackData: t.callbackData(approvalID, "a")},
			{Text: "拒绝", CallbackData: t.callbackData(approvalID, "r")},
		}}},
	})
	if err != nil {
		return err
	}
	chat, mid := msg.Chat.ID, int64(msg.ID)
	return t.st.SetApprovalTelegram(ctx, db.SetApprovalTelegramParams{ID: approvalID, TelegramChatID: &chat, TelegramMessageID: &mid})
}

// UpdateApproval 审批结束后把卡片改成结果，去掉按钮。
func (t *Telegram) UpdateApproval(ctx context.Context, approvalID string) error {
	a, err := t.st.GetApproval(ctx, approvalID)
	if err != nil || a.TelegramChatID == nil || a.TelegramMessageID == nil {
		return err
	}
	d, err := t.approvals.Get(ctx, approvalID)
	if err != nil {
		return err
	}
	result := map[string]string{"approved": "已批准", "rejected": "已拒绝", "expired": "已过期", "voided": "已作废"}[d.Status]
	text := fmt.Sprintf("【%s】%s", result, d.Change.Summary)
	if d.DecidedBy != "" {
		text += "\n处理人：" + d.DecidedBy
	}
	if d.Reason != "" {
		text += "\n说明：" + d.Reason
	}
	text += "\n变更状态：" + d.Change.Status
	_, err = t.b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: *a.TelegramChatID, MessageID: int(*a.TelegramMessageID), Text: text})
	return err
}

// callbackData 形如 apr:<审批ID>:<a|r>:<签名>，总长度控制在 Telegram 的 64 字节以内。
func (t *Telegram) callbackData(id, action string) string {
	msg := id + ":" + action
	return "apr:" + msg + ":" + t.box.Sign("tg:"+msg, 16)
}

func (t *Telegram) onCallback(ctx context.Context, b *bot.Bot, u *models.Update) {
	cq := u.CallbackQuery
	answer := func(text string) {
		_, _ = b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: cq.ID, Text: text, ShowAlert: true})
	}
	parts := strings.Split(cq.Data, ":")
	if len(parts) != 4 || !t.box.Verify("tg:"+parts[1]+":"+parts[2], parts[3]) {
		answer("签名无效")
		return
	}
	op, err := t.st.GetOperatorByTelegram(ctx, &cq.From.ID)
	if err != nil {
		answer("这个 Telegram 账号没有绑定审批人")
		return
	}
	id := parts[1]
	switch parts[2] {
	case "a":
		d, err := t.approvals.Approve(ctx, id, op, approval.ViaTelegram)
		if err != nil {
			answer("批准失败：" + err.Error())
			return
		}
		answer("已批准，变更状态：" + d.Change.Status)
	case "r":
		if _, err := t.approvals.Reject(ctx, id, op, approval.ViaTelegram, "在 Telegram 拒绝"); err != nil {
			answer("拒绝失败：" + err.Error())
			return
		}
		answer("已拒绝")
	}
}

// NewBindCode 生成绑定码，运营者在 Telegram 里发送 /bind <码> 完成绑定。
func (t *Telegram) NewBindCode(operatorID int64) string {
	code := secret.UserCode()
	t.bindMu.Lock()
	defer t.bindMu.Unlock()
	t.binds[code] = bindCode{operatorID: operatorID, expires: time.Now().Add(10 * time.Minute)}
	return code
}

func (t *Telegram) onBind(ctx context.Context, b *bot.Bot, u *models.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(u.Message.Text, "/bind")))
	t.bindMu.Lock()
	bc, ok := t.binds[code]
	delete(t.binds, code)
	t.bindMu.Unlock()
	reply := "绑定码无效或已过期，请在 Admin 重新生成"
	if ok && time.Now().Before(bc.expires) {
		uid := u.Message.From.ID
		if err := t.st.SetOperatorTelegram(ctx, db.SetOperatorTelegramParams{ID: bc.operatorID, TelegramUserID: &uid}); err != nil {
			reply = "绑定失败：" + err.Error()
		} else {
			reply = "已绑定，之后可以在这里直接批准或拒绝审批"
		}
	}
	_, _ = b.SendMessage(ctx, &bot.SendMessageParams{ChatID: u.Message.Chat.ID, Text: reply})
}

// EventText 把事件渲染成告警消息。
func EventText(e db.Event) string {
	icon := map[string]string{"critical": "【告警】", "warning": "【注意】", "info": "【通知】"}[e.Severity]
	return icon + e.Message
}
