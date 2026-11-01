package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// 自动处置规则：某类事件发生时，系统自动执行一个动作。动作以系统身份走变更计划，
// 风险等级和审批照常生效：动作被升为敏感时只生成审批，由人批准。
const (
	ActionEmailUser          = "email_user"
	ActionOperation          = "operation"
	ActionReplaceOfflineNode = "replace_offline_node"
)

// UserEvents 是主体为用户（subject 形如 user:<id>）的事件，可以给用户发邮件。
var UserEvents = []string{events.UserExpired, events.UserExpiring, events.UserOverQuota, events.TrafficAnomaly,
	events.OrderPaid, events.OrderRefunded, events.TicketReplied}

type EmailParams struct {
	Subject string `json:"subject"`
	// Body 可以用 {{email}}、{{message}}、{{service}}、{{portal_url}} 占位。
	Body string `json:"body"`
}

type OperationParams struct {
	Kind string `json:"kind"`
	// Params 里的字符串可以用 {{subject_id}}（事件主体的数字 ID）和 {{payload.<字段>}} 占位。
	Params map[string]any `json:"params"`
}

type ReplaceParams struct {
	// StandbyTag 是备用节点的标签：在同地区找一个带这个标签、在线的节点，挂到掉线节点所在的套餐上。
	StandbyTag string `json:"standby_tag"`
}

func (d *Deps) validateRule(event, action string, raw json.RawMessage) (json.RawMessage, error) {
	if !slices.Contains(events.Kinds, event) {
		return nil, apierr.BadRequest("invalid_event_kind", "不支持的事件类型 %q", event).WithHint("可选：%s", strings.Join(events.Kinds, ", "))
	}
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	switch action {
	case ActionEmailUser:
		if !slices.Contains(UserEvents, event) {
			return nil, apierr.BadRequest("invalid_rule", "%s 事件没有对应的用户，不能发邮件", event).WithHint("可用于：%s", strings.Join(UserEvents, ", "))
		}
		p, err := decode[EmailParams](raw)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Subject) == "" || strings.TrimSpace(p.Body) == "" {
			return nil, apierr.BadRequest("invalid_rule", "邮件需要 subject 和 body")
		}
		return mustJSON(p), nil
	case ActionOperation:
		p, err := decode[OperationParams](raw)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(p.Kind, "automation.") || strings.HasPrefix(p.Kind, "webhook.") {
			return nil, apierr.BadRequest("invalid_rule", "自动处置不能修改规则和 Webhook 本身")
		}
		if d.Operations != nil {
			risk, ok := d.Operations()[p.Kind]
			if !ok {
				return nil, apierr.BadRequest("unknown_operation", "不支持的操作 %s", p.Kind).WithHint("用 GET /api/v1/tools 查看操作")
			}
			if risk == change.Forbidden {
				return nil, apierr.BadRequest("invalid_rule", "%s 是禁止级操作", p.Kind)
			}
		}
		if p.Params == nil {
			p.Params = map[string]any{}
		}
		return mustJSON(p), nil
	case ActionReplaceOfflineNode:
		if event != events.NodeOffline {
			return nil, apierr.BadRequest("invalid_rule", "替换掉线节点只能用于 node_offline 事件")
		}
		p, err := decode[ReplaceParams](raw)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.StandbyTag) == "" {
			p.StandbyTag = "standby"
		}
		return mustJSON(p), nil
	}
	return nil, apierr.BadRequest("invalid_action", "不支持的动作 %q", action).
		WithHint("可选：email_user（给用户发邮件）、operation（执行一个变更操作）、replace_offline_node（用备用节点替换掉线节点）")
}

func describeRule(event, action string, params json.RawMessage) string {
	switch action {
	case ActionEmailUser:
		var p EmailParams
		_ = json.Unmarshal(params, &p)
		return fmt.Sprintf("发生 %s 时给用户发邮件「%s」", event, p.Subject)
	case ActionOperation:
		var p OperationParams
		_ = json.Unmarshal(params, &p)
		return fmt.Sprintf("发生 %s 时以系统身份执行 %s（风险等级照常生效，敏感时生成审批）", event, p.Kind)
	case ActionReplaceOfflineNode:
		var p ReplaceParams
		_ = json.Unmarshal(params, &p)
		return fmt.Sprintf("节点掉线时，把同地区带 %s 标签的在线节点挂到它所在的套餐", p.StandbyTag)
	}
	return action
}

// ---- automation.create ---------------------------------------------------

// AutomationCreate 新增一条常驻的自动处置规则，按敏感处理。
type AutomationCreate struct{ d *Deps }

func (*AutomationCreate) Kind() string      { return "automation.create" }
func (*AutomationCreate) Risk() change.Risk { return change.Sensitive }

type automationCreateParams struct {
	Name    string          `json:"name"`
	Event   string          `json:"event"`
	Action  string          `json:"action"`
	Params  json.RawMessage `json:"params"`
	Enabled *bool           `json:"enabled"`
}

func (o *AutomationCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[automationCreateParams](raw)
	if err != nil {
		return nil, err
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, apierr.BadRequest("missing_name", "缺少规则名称")
	}
	if p.Params, err = o.d.validateRule(p.Event, p.Action, p.Params); err != nil {
		return nil, err
	}
	enabled := p.Enabled == nil || *p.Enabled
	p.Enabled = &enabled
	return &change.Draft{
		Summary:  fmt.Sprintf("新建自动处置规则 %s", p.Name),
		Steps:    []string{describeRule(p.Event, p.Action, p.Params)},
		Diff:     []change.DiffEntry{{Field: "rule", Before: nil, After: map[string]any{"event": p.Event, "action": p.Action, "params": p.Params}}},
		Resolved: p,
	}, nil
}

func (o *AutomationCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[automationCreateParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := q.CreateAutomationRule(ctx, db.CreateAutomationRuleParams{Name: p.Name, EventKind: p.Event, Action: p.Action, Params: p.Params, Enabled: *p.Enabled})
	if err != nil {
		return nil, err
	}
	return map[string]any{"rule_id": r.ID, "name": r.Name}, nil
}

// ---- automation.update / automation.delete ------------------------------

// AutomationUpdate 修改规则。只停用时是常规操作，其余改动按敏感处理。
type AutomationUpdate struct{ d *Deps }

func (*AutomationUpdate) Kind() string      { return "automation.update" }
func (*AutomationUpdate) Risk() change.Risk { return change.Normal }

type automationUpdateParams struct {
	Rule    Ref             `json:"rule"`
	Name    *string         `json:"name"`
	Params  json.RawMessage `json:"params"`
	Enabled *bool           `json:"enabled"`
}

func findRule(ctx context.Context, q *db.Queries, r Ref) (db.AutomationRule, error) {
	id, ok := r.id()
	if !ok {
		return db.AutomationRule{}, apierr.BadRequest("missing_rule", "规则编号 %q 不对", r)
	}
	rule, err := q.GetAutomationRule(ctx, id)
	if store.IsNotFound(err) {
		return rule, apierr.NotFound("rule_not_found", "规则 %s 不存在", r).WithHint("用 GET /api/v1/automations 查看")
	}
	return rule, err
}

type ruleState struct {
	Name    string          `json:"name"`
	Params  json.RawMessage `json:"params"`
	Enabled bool            `json:"enabled"`
}

func (o *AutomationUpdate) merged(ctx context.Context, q *db.Queries, raw json.RawMessage) (db.AutomationRule, ruleState, ruleState, error) {
	p, err := decode[automationUpdateParams](raw)
	if err != nil {
		return db.AutomationRule{}, ruleState{}, ruleState{}, err
	}
	r, err := findRule(ctx, q, p.Rule)
	if err != nil {
		return r, ruleState{}, ruleState{}, err
	}
	before := ruleState{Name: r.Name, Params: r.Params, Enabled: r.Enabled}
	after := before
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		after.Name = strings.TrimSpace(*p.Name)
	}
	if len(p.Params) > 0 && string(p.Params) != "null" {
		if after.Params, err = o.d.validateRule(r.EventKind, r.Action, p.Params); err != nil {
			return r, before, after, err
		}
	}
	if p.Enabled != nil {
		after.Enabled = *p.Enabled
	}
	return r, before, after, nil
}

func (o *AutomationUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	r, before, after, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	diff := fieldDiff(before, after)
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	d := &change.Draft{
		Summary: fmt.Sprintf("修改自动处置规则 %s", r.Name),
		Steps:   []string{describeRule(r.EventKind, r.Action, after.Params)},
		Diff:    diff,
		Targets: []change.Target{{Type: "automation", ID: r.ID, Version: r.Version}},
	}
	if !(len(diff) == 1 && diff[0].Field == "enabled" && !after.Enabled) {
		d.Escalate = "修改常驻的自动处置规则，固定为敏感操作（只停用时不需要审批）"
	}
	return d, nil
}

func (o *AutomationUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, _, after, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	u, err := q.UpdateAutomationRule(ctx, db.UpdateAutomationRuleParams{ID: r.ID, Name: after.Name, Params: after.Params, Enabled: after.Enabled})
	if err != nil {
		return nil, err
	}
	return map[string]any{"rule_id": u.ID, "enabled": u.Enabled, "version": u.Version}, nil
}

type AutomationDelete struct{ d *Deps }

func (*AutomationDelete) Kind() string      { return "automation.delete" }
func (*AutomationDelete) Risk() change.Risk { return change.Normal }

type ruleRefParams struct {
	Rule Ref `json:"rule"`
}

func (o *AutomationDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ruleRefParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRule(ctx, q, p.Rule)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除自动处置规则 %s", r.Name),
		Steps:   []string{"删除规则：" + describeRule(r.EventKind, r.Action, r.Params)},
		Diff:    []change.DiffEntry{{Field: "rule", Before: r.Name, After: nil}},
		Targets: []change.Target{{Type: "automation", ID: r.ID, Version: r.Version}},
	}, nil
}

func (o *AutomationDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[ruleRefParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRule(ctx, q, p.Rule)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteAutomationRule(ctx, r.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": r.Name}, nil
}
