// Package automation 处理事件发生后的自动动作：推送 Webhook、执行自动处置规则。
//
// 自动处置以系统身份走变更计划：风险等级、按影响面升级和审批照常生效，
// 动作被判为敏感时只生成审批，由人批准后才执行。
package automation

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/notify"
	"github.com/momaek/patato/server/internal/ops"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type Config struct {
	ServiceName string
	PortalURL   string
}

type Dispatcher struct {
	st     *store.Store
	box    *secret.Box
	eng    *change.Engine
	mailer notify.Mailer
	cfg    Config
	client *http.Client
	log    *slog.Logger
}

func New(st *store.Store, box *secret.Box, eng *change.Engine, mailer notify.Mailer, cfg Config, log *slog.Logger) *Dispatcher {
	return &Dispatcher{st: st, box: box, eng: eng, mailer: mailer, cfg: cfg, log: log,
		client: &http.Client{Timeout: 10 * time.Second}}
}

// Payload 是推送给 Webhook 的内容。
type Payload struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Severity  string          `json:"severity"`
	Subject   string          `json:"subject"`
	Message   string          `json:"message"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// Dispatch 执行匹配这个事件的自动处置规则，返回需要推送的 Webhook。
func (d *Dispatcher) Dispatch(ctx context.Context, eventID int64) ([]int64, error) {
	e, err := d.st.GetEvent(ctx, eventID)
	if store.IsNotFound(err) {
		return nil, fmt.Errorf("event %d not visible yet", eventID) // 事件所在事务还没提交，稍后重试
	}
	if err != nil {
		return nil, err
	}
	// 先读出全部要做的事再执行规则：规则执行后不再返回错误，避免任务重试时重复执行。
	rules, err := d.st.RulesForEvent(ctx, e.Kind)
	if err != nil {
		return nil, err
	}
	hooks, err := d.st.WebhooksForEvent(ctx, e.Kind)
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		result := d.run(ctx, r, e)
		if err := d.st.SetAutomationResult(ctx, db.SetAutomationResultParams{ID: r.ID, LastResult: truncate(result, 500)}); err != nil {
			d.log.Error("record automation result", "rule", r.ID, "err", err)
		}
	}
	ids := make([]int64, 0, len(hooks))
	for _, h := range hooks {
		ids = append(ids, h.ID)
	}
	return ids, nil
}

func (d *Dispatcher) run(ctx context.Context, r db.AutomationRule, e db.Event) string {
	actor := change.Actor{Kind: "system", Label: "自动处置：" + r.Name, Entry: "automation", CanWrite: true,
		Instruction: fmt.Sprintf("事件 #%d：%s", e.ID, e.Message)}
	switch r.Action {
	case ops.ActionEmailUser:
		return d.emailUser(ctx, r, e)
	case ops.ActionOperation:
		var p ops.OperationParams
		if err := json.Unmarshal(r.Params, &p); err != nil {
			return "规则参数无效：" + err.Error()
		}
		params, _ := json.Marshal(fill(p.Params, e))
		plan, err := d.eng.Execute(ctx, actor, p.Kind, params)
		return describe(plan, err)
	case ops.ActionReplaceOfflineNode:
		return d.replaceNode(ctx, actor, r, e)
	}
	return "不支持的动作 " + r.Action
}

func describe(plan *change.Plan, err error) string {
	if err != nil {
		return "失败：" + apierr.From(err).Message
	}
	switch plan.Status {
	case "pending_approval":
		return fmt.Sprintf("已生成审批 %s：%s", plan.ApprovalID, plan.Summary)
	case "applied":
		return "已执行：" + plan.Summary
	}
	return plan.Status + "：" + plan.Summary
}

var userSubject = regexp.MustCompile(`^user:(\d+)$`)

func subjectID(subject string) string {
	if i := strings.LastIndex(subject, ":"); i >= 0 {
		return subject[i+1:]
	}
	return subject
}

func (d *Dispatcher) emailUser(ctx context.Context, r db.AutomationRule, e db.Event) string {
	var p ops.EmailParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return "规则参数无效：" + err.Error()
	}
	var uid int64
	if m := userSubject.FindStringSubmatch(e.Subject); m != nil {
		uid, _ = strconv.ParseInt(m[1], 10, 64)
	} else {
		var pl struct {
			UserID int64 `json:"user_id"`
		}
		_ = json.Unmarshal(e.Payload, &pl)
		uid = pl.UserID
	}
	if uid == 0 {
		return "事件没有对应的用户"
	}
	u, err := d.st.GetUser(ctx, uid)
	if err != nil {
		return "找不到用户：" + err.Error()
	}
	repl := strings.NewReplacer("{{email}}", u.Email, "{{message}}", e.Message, "{{service}}", d.cfg.ServiceName, "{{portal_url}}", d.cfg.PortalURL)
	if err := d.mailer.Send(ctx, u.Email, repl.Replace(p.Subject), repl.Replace(p.Body)); err != nil {
		return "发送失败：" + err.Error()
	}
	return "已发邮件给 " + u.Email
}

// replaceNode 找一个同地区、带备用标签、在线的节点，挂到掉线节点所在的每个套餐上。
func (d *Dispatcher) replaceNode(ctx context.Context, actor change.Actor, r db.AutomationRule, e db.Event) string {
	var p ops.ReplaceParams
	_ = json.Unmarshal(r.Params, &p)
	var pl struct {
		NodeID int64 `json:"node_id"`
	}
	_ = json.Unmarshal(e.Payload, &pl)
	down, err := d.st.GetNode(ctx, pl.NodeID)
	if err != nil {
		return "找不到掉线节点"
	}
	nodes, err := d.st.ListNodes(ctx)
	if err != nil {
		return "读取节点失败：" + err.Error()
	}
	var standby *db.Node
	for i, n := range nodes {
		if n.ID != down.ID && n.State == "online" && slices.Contains(n.Tags, p.StandbyTag) && (down.Region == "" || n.Region == down.Region) {
			standby = &nodes[i]
			break
		}
	}
	if standby == nil {
		return fmt.Sprintf("没有可用的备用节点（地区 %s，标签 %s）", down.Region, p.StandbyTag)
	}
	plans, err := d.st.ListNodePlans(ctx, down.ID)
	if err != nil {
		return "读取套餐失败：" + err.Error()
	}
	var out []string
	for _, plan := range plans {
		if attached, _ := d.st.IsNodeAttached(ctx, db.IsNodeAttachedParams{PlanID: plan.ID, NodeID: standby.ID}); attached {
			continue
		}
		params, _ := json.Marshal(map[string]any{"node": standby.Code, "plan": plan.Code})
		res, err := d.eng.Execute(ctx, actor, "node.attach", params)
		out = append(out, describe(res, err))
	}
	if len(out) == 0 {
		return fmt.Sprintf("备用节点 %s 已经在 %s 的全部套餐里", standby.Code, down.Code)
	}
	return strings.Join(out, "；")
}

var placeholder = regexp.MustCompile(`\{\{\s*(subject_id|payload\.[A-Za-z0-9_]+)\s*\}\}`)

// fill 替换参数里的占位：{{subject_id}} 是事件主体的 ID，{{payload.x}} 是事件内容里的字段。
// 整个字符串就是一个占位时保留原值的类型（例如数字 ID）。
func fill(v any, e db.Event) any {
	var payload map[string]any
	_ = json.Unmarshal(e.Payload, &payload)
	lookup := func(key string) any {
		if key == "subject_id" {
			return subjectID(e.Subject)
		}
		return payload[strings.TrimPrefix(key, "payload.")]
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, x := range t {
				out[k] = walk(x)
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, x := range t {
				out[i] = walk(x)
			}
			return out
		case string:
			if m := placeholder.FindStringSubmatch(t); m != nil && m[0] == strings.TrimSpace(t) {
				return lookup(m[1])
			}
			return placeholder.ReplaceAllStringFunc(t, func(s string) string {
				return fmt.Sprint(lookup(placeholder.FindStringSubmatch(s)[1]))
			})
		}
		return v
	}
	return walk(v)
}

// Sign 计算 Webhook 签名：HMAC-SHA256(secret, timestamp + "." + body)。接收方用同样的方法校验，并拒绝过旧的时间戳。
func Sign(secret []byte, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(timestamp + "."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Deliver 把事件推送到一个 Webhook。返回错误时由任务队列重试。
func (d *Dispatcher) Deliver(ctx context.Context, webhookID, eventID int64) error {
	w, err := d.st.GetWebhook(ctx, webhookID)
	if store.IsNotFound(err) {
		return nil // 已删除
	}
	if err != nil {
		return err
	}
	if !w.Enabled {
		return nil
	}
	e, err := d.st.GetEvent(ctx, eventID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(Payload{ID: e.ID, Kind: e.Kind, Severity: e.Severity, Subject: e.Subject, Message: e.Message, Payload: e.Payload, CreatedAt: e.CreatedAt})
	key, err := d.box.Open(w.SecretCipher)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Patato-Webhook/1")
	req.Header.Set("X-Patato-Event", e.Kind)
	req.Header.Set("X-Patato-Delivery", strconv.FormatInt(e.ID, 10))
	req.Header.Set("X-Patato-Timestamp", ts)
	req.Header.Set("X-Patato-Signature", Sign(key, ts, body))
	res, err := d.client.Do(req)
	status := ""
	if err != nil {
		status = "失败：" + err.Error()
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		status = fmt.Sprintf("HTTP %d", res.StatusCode)
		if res.StatusCode >= 300 {
			err = fmt.Errorf("webhook %d returned %d", w.ID, res.StatusCode)
		}
	}
	if serr := d.st.SetWebhookDelivery(ctx, db.SetWebhookDeliveryParams{ID: w.ID, LastStatus: truncate(status, 200)}); serr != nil {
		d.log.Error("record webhook delivery", "webhook", w.ID, "err", serr)
	}
	return err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
