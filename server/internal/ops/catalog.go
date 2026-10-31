package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/store/db"
)

var protocols = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "shadowsocks": true, "hysteria": true, "tuic": true, "anytls": true,
}

// ---- template.create / template.update --------------------------------

type TemplateCreate struct{ d *Deps }

func (*TemplateCreate) Kind() string      { return "template.create" }
func (*TemplateCreate) Risk() change.Risk { return change.Normal }

type templateParams struct {
	Code     string          `json:"code"`
	Name     string          `json:"name"`
	Region   string          `json:"region"`
	Protocol string          `json:"protocol"`
	PortMin  int             `json:"port_min"`
	PortMax  int             `json:"port_max"`
	Settings json.RawMessage `json:"settings"`
	Rate     float64         `json:"rate"`
}

func (p *templateParams) validate() error {
	if !codePattern.MatchString(p.Code) {
		return apierr.BadRequest("invalid_code", "模板代号 %q 只能包含小写字母、数字和连字符", p.Code)
	}
	if strings.TrimSpace(p.Name) == "" {
		return apierr.BadRequest("missing_name", "缺少模板名称")
	}
	if !protocols[p.Protocol] {
		return apierr.BadRequest("invalid_protocol", "不支持的协议 %q", p.Protocol).
			WithHint("可选：vless、vmess、trojan、shadowsocks、hysteria、tuic、anytls")
	}
	if p.PortMin <= 0 || p.PortMax < p.PortMin || p.PortMax > 65535 {
		return apierr.BadRequest("invalid_port_range", "端口范围 %d-%d 不合法", p.PortMin, p.PortMax)
	}
	if p.Rate == 0 {
		p.Rate = 1
	}
	if p.Rate < 0 {
		return apierr.BadRequest("invalid_rate", "倍率必须大于 0")
	}
	if len(p.Settings) == 0 {
		p.Settings = json.RawMessage("{}")
	}
	var m map[string]any
	if json.Unmarshal(p.Settings, &m) != nil {
		return apierr.BadRequest("invalid_settings", "settings 必须是 JSON 对象")
	}
	return nil
}

func (o *TemplateCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[templateParams](raw)
	if err != nil {
		return nil, err
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if _, err := q.GetTemplateByCode(ctx, p.Code); err == nil {
		return nil, apierr.Conflict("template_exists", "模板 %s 已存在", p.Code)
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("新建节点模板 %s", p.Name),
		Steps:    []string{fmt.Sprintf("新建模板 %s（%s），协议 %s，端口 %d-%d", p.Name, p.Code, p.Protocol, p.PortMin, p.PortMax)},
		Diff:     []change.DiffEntry{{Field: "模板", Before: nil, After: p}},
		Resolved: p,
	}, nil
}

func (o *TemplateCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[templateParams](raw)
	if err != nil {
		return nil, err
	}
	t, err := q.CreateTemplate(ctx, db.CreateTemplateParams{
		Code: p.Code, Name: p.Name, Region: p.Region, Protocol: p.Protocol,
		PortMin: int32(p.PortMin), PortMax: int32(p.PortMax), Settings: p.Settings, Rate: p.Rate,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("template_exists", "模板 %s 已存在", p.Code)
		}
		return nil, err
	}
	return map[string]any{"template_id": t.ID, "code": t.Code}, nil
}

type TemplateUpdate struct{ d *Deps }

func (*TemplateUpdate) Kind() string      { return "template.update" }
func (*TemplateUpdate) Risk() change.Risk { return change.Normal }

type templateUpdateParams struct {
	Template Ref             `json:"template"`
	Name     *string         `json:"name"`
	Region   *string         `json:"region"`
	PortMin  *int            `json:"port_min"`
	PortMax  *int            `json:"port_max"`
	Settings json.RawMessage `json:"settings"`
	Rate     *float64        `json:"rate"`
}

func (o *TemplateUpdate) merged(ctx context.Context, q *db.Queries, raw json.RawMessage) (db.NodeTemplate, templateParams, error) {
	p, err := decode[templateUpdateParams](raw)
	if err != nil {
		return db.NodeTemplate{}, templateParams{}, err
	}
	t, err := findTemplate(ctx, q, p.Template)
	if err != nil {
		return t, templateParams{}, err
	}
	m := templateParams{Code: t.Code, Name: t.Name, Region: t.Region, Protocol: t.Protocol,
		PortMin: int(t.PortMin), PortMax: int(t.PortMax), Settings: t.Settings, Rate: t.Rate}
	if p.Name != nil {
		m.Name = *p.Name
	}
	if p.Region != nil {
		m.Region = *p.Region
	}
	if p.PortMin != nil {
		m.PortMin = *p.PortMin
	}
	if p.PortMax != nil {
		m.PortMax = *p.PortMax
	}
	if len(p.Settings) > 0 {
		m.Settings = p.Settings
	}
	if p.Rate != nil {
		m.Rate = *p.Rate
	}
	return t, m, m.validate()
}

func (o *TemplateUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	t, m, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("修改节点模板 %s", t.Name),
		Steps:   []string{"更新模板；已创建的节点不受影响，只影响之后用这个模板上的节点"},
		Diff: []change.DiffEntry{{Field: "模板", Before: templateParams{Code: t.Code, Name: t.Name, Region: t.Region,
			Protocol: t.Protocol, PortMin: int(t.PortMin), PortMax: int(t.PortMax), Settings: t.Settings, Rate: t.Rate}, After: m}},
		Targets: []change.Target{templateTarget(t)},
	}, nil
}

func (o *TemplateUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	t, m, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	u, err := q.UpdateTemplate(ctx, db.UpdateTemplateParams{ID: t.ID, Name: m.Name, Region: m.Region, Protocol: m.Protocol,
		PortMin: int32(m.PortMin), PortMax: int32(m.PortMax), Settings: m.Settings, Rate: m.Rate})
	if err != nil {
		return nil, err
	}
	return map[string]any{"template_id": u.ID, "version": u.Version}, nil
}

// ---- plan.create / plan.update / plan.delete --------------------------

type planFields struct {
	Name           string           `json:"name"`
	TransferGB     float64          `json:"transfer_gb"`
	ResetMethod    string           `json:"reset_method"`
	DeviceLimit    int              `json:"device_limit"`
	SpeedLimitMbps int              `json:"speed_limit_mbps"`
	Prices         map[string]int64 `json:"prices"`
	Sellable       bool             `json:"sellable"`
}

func (f *planFields) validate() error {
	if strings.TrimSpace(f.Name) == "" {
		return apierr.BadRequest("missing_name", "缺少套餐名称")
	}
	if f.TransferGB < 0 {
		return apierr.BadRequest("invalid_transfer", "流量不能为负数")
	}
	if f.ResetMethod == "" {
		f.ResetMethod = billing.ResetPurchaseDay
	}
	switch f.ResetMethod {
	case billing.ResetPurchaseDay, billing.ResetCalendarMonth, billing.ResetNever:
	default:
		return apierr.BadRequest("invalid_reset_method", "不支持的重置方式 %q", f.ResetMethod).
			WithHint("可选：purchase_day（按购买日）、calendar_month（每月 1 日）、never（不重置）")
	}
	if f.DeviceLimit < 0 || f.SpeedLimitMbps < 0 {
		return apierr.BadRequest("invalid_limit", "设备数和限速不能为负数")
	}
	for k, v := range f.Prices {
		if v < 0 {
			return apierr.BadRequest("invalid_price", "价格 %s 不能为负数", k)
		}
	}
	if f.Prices == nil {
		f.Prices = map[string]int64{}
	}
	return nil
}

func planFieldsOf(p db.Plan) planFields {
	f := planFields{Name: p.Name, TransferGB: billing.ToGB(p.TransferBytes), ResetMethod: p.ResetMethod,
		DeviceLimit: int(p.DeviceLimit), SpeedLimitMbps: int(p.SpeedLimitMbps), Sellable: p.Sellable, Prices: map[string]int64{}}
	_ = json.Unmarshal(p.Prices, &f.Prices)
	return f
}

type PlanCreate struct{ d *Deps }

func (*PlanCreate) Kind() string      { return "plan.create" }
func (*PlanCreate) Risk() change.Risk { return change.Normal }

type planCreateParams struct {
	Code string `json:"code"`
	planFields
}

func (o *PlanCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[planCreateParams](raw)
	if err != nil {
		return nil, err
	}
	if !codePattern.MatchString(p.Code) {
		return nil, apierr.BadRequest("invalid_code", "套餐代号 %q 只能包含小写字母、数字和连字符", p.Code)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if _, err := q.GetPlanByCode(ctx, p.Code); err == nil {
		return nil, apierr.Conflict("plan_exists", "套餐 %s 已存在", p.Code)
	}
	d := &change.Draft{
		Summary:  fmt.Sprintf("新建套餐 %s", p.Name),
		Steps:    []string{fmt.Sprintf("新建套餐 %s（%s），每月 %.0f GB", p.Name, p.Code, p.TransferGB)},
		Diff:     []change.DiffEntry{{Field: "套餐", Before: nil, After: p}},
		Resolved: p,
	}
	if len(p.Prices) > 0 {
		d.Escalate = "涉及价格，固定为敏感操作"
	}
	return d, nil
}

func (o *PlanCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[planCreateParams](raw)
	if err != nil {
		return nil, err
	}
	pl, err := q.CreatePlan(ctx, db.CreatePlanParams{
		Code: p.Code, Name: p.Name, TransferBytes: billing.GB(p.TransferGB), ResetMethod: p.ResetMethod,
		DeviceLimit: int32(p.DeviceLimit), SpeedLimitMbps: int32(p.SpeedLimitMbps), Prices: mustJSON(p.Prices), Sellable: p.Sellable,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("plan_exists", "套餐 %s 已存在", p.Code)
		}
		return nil, err
	}
	return map[string]any{"plan_id": pl.ID, "code": pl.Code}, nil
}

type PlanUpdate struct{ d *Deps }

func (*PlanUpdate) Kind() string      { return "plan.update" }
func (*PlanUpdate) Risk() change.Risk { return change.Normal }

type planUpdateParams struct {
	Plan           Ref              `json:"plan"`
	Name           *string          `json:"name"`
	TransferGB     *float64         `json:"transfer_gb"`
	ResetMethod    *string          `json:"reset_method"`
	DeviceLimit    *int             `json:"device_limit"`
	SpeedLimitMbps *int             `json:"speed_limit_mbps"`
	Prices         map[string]int64 `json:"prices"`
	Sellable       *bool            `json:"sellable"`
	// ApplyToUsers 为 true 时把新的流量额度同步给这个套餐下的所有用户。
	ApplyToUsers bool `json:"apply_to_users"`
}

func (o *PlanUpdate) merged(ctx context.Context, q *db.Queries, raw json.RawMessage) (*planUpdateParams, db.Plan, planFields, planFields, error) {
	p, err := decode[planUpdateParams](raw)
	if err != nil {
		return nil, db.Plan{}, planFields{}, planFields{}, err
	}
	pl, err := findPlan(ctx, q, p.Plan)
	if err != nil {
		return nil, pl, planFields{}, planFields{}, err
	}
	before := planFieldsOf(pl)
	after := planFieldsOf(pl)
	if p.Name != nil {
		after.Name = *p.Name
	}
	if p.TransferGB != nil {
		after.TransferGB = *p.TransferGB
	}
	if p.ResetMethod != nil {
		after.ResetMethod = *p.ResetMethod
	}
	if p.DeviceLimit != nil {
		after.DeviceLimit = *p.DeviceLimit
	}
	if p.SpeedLimitMbps != nil {
		after.SpeedLimitMbps = *p.SpeedLimitMbps
	}
	if p.Prices != nil {
		after.Prices = p.Prices
	}
	if p.Sellable != nil {
		after.Sellable = *p.Sellable
	}
	return p, pl, before, after, after.validate()
}

func (o *PlanUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, pl, before, after, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	diff := fieldDiff(before, after)
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	users, err := q.CountUsersInPlan(ctx, &pl.ID)
	if err != nil {
		return nil, err
	}
	steps := []string{fmt.Sprintf("更新套餐 %s 的 %d 项设置", pl.Name, len(diff))}
	if p.ApplyToUsers && before.TransferGB != after.TransferGB {
		steps = append(steps, fmt.Sprintf("把 %d 名用户的流量额度改为 %.0f GB", users, after.TransferGB))
	}
	steps = append(steps, "限速、设备数在节点下次拉取用户时生效")
	d := &change.Draft{
		Summary: fmt.Sprintf("修改套餐 %s", pl.Name),
		Steps:   steps,
		Diff:    diff,
		Impact:  change.Impact{Users: users},
		Targets: []change.Target{planTarget(pl)},
	}
	if !reflect.DeepEqual(before.Prices, after.Prices) {
		d.Escalate = "涉及价格，固定为敏感操作"
	}
	return d, nil
}

func (o *PlanUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, pl, before, after, err := o.merged(ctx, q, raw)
	if err != nil {
		return nil, err
	}
	u, err := q.UpdatePlan(ctx, db.UpdatePlanParams{
		ID: pl.ID, Name: after.Name, TransferBytes: billing.GB(after.TransferGB), ResetMethod: after.ResetMethod,
		DeviceLimit: int32(after.DeviceLimit), SpeedLimitMbps: int32(after.SpeedLimitMbps), Prices: mustJSON(after.Prices), Sellable: after.Sellable,
	})
	if err != nil {
		return nil, err
	}
	affected := int64(0)
	if p.ApplyToUsers && before.TransferGB != after.TransferGB {
		affected, err = q.SetPlanUsersTransfer(ctx, db.SetPlanUsersTransferParams{PlanID: &pl.ID, TransferBytes: billing.GB(after.TransferGB)})
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"plan_id": u.ID, "version": u.Version, "users_updated": affected}, nil
}

type PlanDelete struct{ d *Deps }

func (*PlanDelete) Kind() string      { return "plan.delete" }
func (*PlanDelete) Risk() change.Risk { return change.Sensitive }

type planRefParams struct {
	Plan Ref `json:"plan"`
}

func (o *PlanDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[planRefParams](raw)
	if err != nil {
		return nil, err
	}
	pl, err := findPlan(ctx, q, p.Plan)
	if err != nil {
		return nil, err
	}
	users, err := q.CountUsersInPlan(ctx, &pl.ID)
	if err != nil {
		return nil, err
	}
	if users > 0 {
		return nil, apierr.Conflict("plan_in_use", "套餐 %s 还有 %d 名用户", pl.Name, users).
			WithHint("先用 PATCH /api/v1/users/{id} 把这些用户换到其他套餐")
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除套餐 %s", pl.Name),
		Steps:   []string{fmt.Sprintf("删除套餐 %s（%s），并解除它和所有节点的挂载", pl.Name, pl.Code)},
		Diff:    []change.DiffEntry{{Field: "套餐", Before: pl.Name, After: nil}},
		Targets: []change.Target{planTarget(pl)},
	}, nil
}

func (o *PlanDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[planRefParams](raw)
	if err != nil {
		return nil, err
	}
	pl, err := findPlan(ctx, q, p.Plan)
	if err != nil {
		return nil, err
	}
	if err := q.DeletePlan(ctx, pl.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": pl.Code}, nil
}

// fieldDiff 逐字段比较两个结构体（按 JSON 字段名）。
func fieldDiff(before, after any) []change.DiffEntry {
	var b, a map[string]any
	_ = json.Unmarshal(mustJSON(before), &b)
	_ = json.Unmarshal(mustJSON(after), &a)
	var out []change.DiffEntry
	for _, k := range sortedKeys(a) {
		if !reflect.DeepEqual(b[k], a[k]) {
			out = append(out, change.DiffEntry{Field: k, Before: b[k], After: a[k]})
		}
	}
	return out
}
