package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/rules"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- rule_profile.create / update / refresh / delete -----------------------

func findRuleProfile(ctx context.Context, q *db.Queries, r Ref) (db.RuleProfile, error) {
	if r == "" {
		return db.RuleProfile{}, apierr.BadRequest("missing_rule_profile", "缺少分流规则").WithHint("用 GET /api/v1/rule-profiles 查看全部")
	}
	var rp db.RuleProfile
	var err error
	if id, ok := r.id(); ok {
		rp, err = q.GetRuleProfile(ctx, id)
	} else {
		rp, err = q.GetRuleProfileByCode(ctx, string(r))
	}
	if store.IsNotFound(err) {
		e := apierr.NotFound("rule_profile_not_found", "分流规则 %s 不存在", r)
		all, lerr := q.ListRuleProfiles(ctx)
		if lerr == nil && len(all) > 0 {
			codes := make([]string, 0, len(all))
			for _, p := range all {
				codes = append(codes, p.Code)
			}
			return rp, e.WithHint("可用规则：%s", strings.Join(codes, ", "))
		}
		return rp, e.WithHint("还没有任何分流规则；用 POST /api/v1/rule-profiles 创建，GET /api/v1/rule-profiles/presets 查看可导入的 ACL4SSR 模板")
	}
	return rp, err
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ruleProfileFields 是规则模板可修改的字段。Content 在预演时拉取或校验好，执行时直接写入。
type ruleProfileFields struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	SourceURL      string `json:"source_url"`
	Content        string `json:"content"`
	UserSelectable bool   `json:"user_selectable"`
}

// ruleSummary 是模板的摘要，用在变更计划的差异里（ini 原文太长，不直接展示）。
type ruleSummary struct {
	Name           string   `json:"name"`
	SourceURL      string   `json:"source_url"`
	UserSelectable bool     `json:"user_selectable"`
	Groups         []string `json:"groups"`
	Rules          int      `json:"rules"`
	Lists          int      `json:"lists"`
}

func summarize(f ruleProfileFields) ruleSummary {
	s := ruleSummary{Name: f.Name, SourceURL: f.SourceURL, UserSelectable: f.UserSelectable, Groups: []string{}}
	if p, err := rules.Parse(f.Content); err == nil {
		for _, g := range p.Groups {
			s.Groups = append(s.Groups, g.Name)
		}
		s.Rules, s.Lists = len(p.Rulesets), len(p.Lists())
	}
	return s
}

// loadRuleContent 按来源准备模板内容：有地址时下载，否则校验粘贴的内容。返回解析结果供生成计划步骤。
func (d *Deps) loadRuleContent(ctx context.Context, f *ruleProfileFields) (*rules.Profile, error) {
	f.SourceURL = strings.TrimSpace(f.SourceURL)
	if f.SourceURL != "" {
		if !strings.HasPrefix(f.SourceURL, "http://") && !strings.HasPrefix(f.SourceURL, "https://") {
			return nil, apierr.BadRequest("invalid_source_url", "source_url 必须是 http(s) 地址")
		}
		if d.Rules == nil {
			return nil, apierr.BadRequest("rules_unavailable", "服务端没有启用规则拉取，请直接粘贴 ini 内容（content）")
		}
		content, p, err := d.Rules.FetchProfile(ctx, f.SourceURL)
		if err != nil {
			return nil, apierr.BadRequest("rule_fetch_failed", "%v", err).WithHint("检查地址能否从服务端访问；也可以直接粘贴 ini 内容（content）")
		}
		f.Content = content
		return p, nil
	}
	if strings.TrimSpace(f.Content) == "" {
		return nil, apierr.BadRequest("missing_rule_source", "需要 source_url（ini 地址）或 content（ini 内容）其中之一").
			WithHint("用 GET /api/v1/rule-profiles/presets 查看可导入的 ACL4SSR 模板")
	}
	p, err := rules.Parse(f.Content)
	if err != nil {
		return nil, apierr.BadRequest("invalid_rule_profile", "规则模板解析失败：%v", err)
	}
	return p, nil
}

func ruleSteps(name string, p *rules.Profile) []string {
	steps := []string{fmt.Sprintf("「%s」包含 %d 个策略组、%d 条规则，引用 %d 个规则文件", name, len(p.Groups), len(p.Rulesets), len(p.Lists()))}
	for _, w := range p.Warnings {
		steps = append(steps, "注意："+w)
	}
	return steps
}

type RuleProfileCreate struct{ d *Deps }

func (*RuleProfileCreate) Kind() string      { return "rule_profile.create" }
func (*RuleProfileCreate) Risk() change.Risk { return change.Normal }

type ruleProfileCreateParams struct {
	Code string `json:"code"`
	// Preset 是内置模板代号；给了以后 code、name、description、source_url 没填的取预设的值。
	Preset         string  `json:"preset,omitempty"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	SourceURL      string  `json:"source_url"`
	Content        string  `json:"content"`
	UserSelectable *bool   `json:"user_selectable"`
	FetchedAt      *string `json:"fetched_at,omitempty"`
}

func (o *RuleProfileCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ruleProfileCreateParams](raw)
	if err != nil {
		return nil, err
	}
	if p.Preset != "" {
		var pre *rules.Preset
		for i := range rules.Presets {
			if rules.Presets[i].Code == p.Preset {
				pre = &rules.Presets[i]
			}
		}
		if pre == nil {
			codes := []string{}
			for _, x := range rules.Presets {
				codes = append(codes, x.Code)
			}
			return nil, apierr.BadRequest("invalid_preset", "没有内置模板 %q", p.Preset).WithHint("可选：%s", strings.Join(codes, ", "))
		}
		if p.Code == "" {
			p.Code = pre.Code
		}
		if p.Name == "" {
			p.Name = pre.Name
		}
		if p.Description == "" {
			p.Description = pre.Description
		}
		if p.SourceURL == "" && p.Content == "" {
			p.SourceURL = pre.SourceURL
		}
		p.Preset = ""
	}
	if !codePattern.MatchString(p.Code) {
		return nil, apierr.BadRequest("invalid_code", "规则代号 %q 只能包含小写字母、数字和连字符", p.Code)
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, apierr.BadRequest("missing_name", "缺少规则名称")
	}
	if _, err := q.GetRuleProfileByCode(ctx, p.Code); err == nil {
		return nil, apierr.Conflict("rule_profile_exists", "分流规则 %s 已存在", p.Code)
	}
	if p.UserSelectable == nil {
		t := true
		p.UserSelectable = &t
	}
	f := ruleProfileFields{Name: p.Name, Description: p.Description, SourceURL: p.SourceURL, Content: p.Content, UserSelectable: *p.UserSelectable}
	prof, err := o.d.loadRuleContent(ctx, &f)
	if err != nil {
		return nil, err
	}
	p.SourceURL, p.Content = f.SourceURL, f.Content
	if f.SourceURL != "" {
		now := o.d.now().UTC().Format(time.RFC3339)
		p.FetchedAt = &now
	}
	steps := append([]string{fmt.Sprintf("新建分流规则 %s（%s）", p.Name, p.Code)}, ruleSteps(p.Name, prof)...)
	if f.UserSelectable {
		steps = append(steps, "用户可以在用户侧自选这份规则")
	}
	steps = append(steps, "新建后还没有套餐或用户使用；在套餐里设为默认规则后生效")
	return &change.Draft{
		Summary:  fmt.Sprintf("新建分流规则 %s", p.Name),
		Steps:    steps,
		Diff:     []change.DiffEntry{{Field: "分流规则", Before: nil, After: summarize(f)}},
		Resolved: p,
	}, nil
}

func (o *RuleProfileCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[ruleProfileCreateParams](raw)
	if err != nil {
		return nil, err
	}
	prof, err := rules.Parse(p.Content)
	if err != nil {
		return nil, apierr.BadRequest("invalid_rule_profile", "规则模板解析失败：%v", err)
	}
	var fetched *time.Time
	if p.FetchedAt != nil {
		if t, err := time.Parse(time.RFC3339, *p.FetchedAt); err == nil {
			fetched = &t
		}
	}
	selectable := p.UserSelectable == nil || *p.UserSelectable
	rp, err := q.CreateRuleProfile(ctx, db.CreateRuleProfileParams{Code: p.Code, Name: p.Name, Description: p.Description,
		SourceUrl: p.SourceURL, Content: p.Content, UserSelectable: selectable, FetchedAt: fetched})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("rule_profile_exists", "分流规则 %s 已存在", p.Code)
		}
		return nil, err
	}
	if err := rules.EnsureLists(ctx, q, prof); err != nil {
		return nil, err
	}
	return map[string]any{"rule_profile_id": rp.ID, "code": rp.Code}, nil
}

type RuleProfileUpdate struct{ d *Deps }

func (*RuleProfileUpdate) Kind() string      { return "rule_profile.update" }
func (*RuleProfileUpdate) Risk() change.Risk { return change.Normal }

type ruleProfileUpdateParams struct {
	Profile        Ref     `json:"rule_profile"`
	Name           *string `json:"name"`
	Description    *string `json:"description"`
	SourceURL      *string `json:"source_url"`
	Content        *string `json:"content"`
	UserSelectable *bool   `json:"user_selectable"`
}

// ruleProfileResolved 是更新和刷新预演后固定下来的参数。
type ruleProfileResolved struct {
	Profile int64 `json:"rule_profile"`
	ruleProfileFields
	Fetched bool `json:"fetched"`
}

func fieldsOf(rp db.RuleProfile) ruleProfileFields {
	return ruleProfileFields{Name: rp.Name, Description: rp.Description, SourceURL: rp.SourceUrl, Content: rp.Content, UserSelectable: rp.UserSelectable}
}

func (o *RuleProfileUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ruleProfileUpdateParams](raw)
	if err != nil {
		return nil, err
	}
	rp, err := findRuleProfile(ctx, q, p.Profile)
	if err != nil {
		return nil, err
	}
	before, after := fieldsOf(rp), fieldsOf(rp)
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return nil, apierr.BadRequest("missing_name", "规则名称不能为空")
		}
		after.Name = *p.Name
	}
	if p.Description != nil {
		after.Description = *p.Description
	}
	if p.UserSelectable != nil {
		after.UserSelectable = *p.UserSelectable
	}
	reload := false
	if p.Content != nil {
		after.Content, after.SourceURL, reload = *p.Content, "", true
	}
	if p.SourceURL != nil {
		after.SourceURL, reload = *p.SourceURL, true
	}
	var prof *rules.Profile
	if reload {
		if prof, err = o.d.loadRuleContent(ctx, &after); err != nil {
			return nil, err
		}
	}
	diff := fieldDiff(summarize(before), summarize(after))
	if after.Description != before.Description {
		diff = append(diff, change.DiffEntry{Field: "description", Before: before.Description, After: after.Description})
	}
	if after.Content != before.Content && len(diff) == 0 {
		diff = append(diff, change.DiffEntry{Field: "content", Before: "（原内容）", After: "（新内容）"})
	}
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	users, err := q.CountRuleProfileUsers(ctx, rp.Code)
	if err != nil {
		return nil, err
	}
	steps := []string{fmt.Sprintf("更新分流规则 %s", rp.Name)}
	if prof != nil {
		steps = append(steps, ruleSteps(after.Name, prof)...)
	}
	if before.UserSelectable && !after.UserSelectable {
		steps = append(steps, "不再允许用户自选；已经自选了它的用户改回跟随套餐的默认规则")
	}
	steps = append(steps, fmt.Sprintf("使用这份规则的 %d 名用户在客户端下次更新订阅时生效", users))
	return &change.Draft{
		Summary:  fmt.Sprintf("修改分流规则 %s", rp.Name),
		Steps:    steps,
		Diff:     diff,
		Impact:   change.Impact{Users: users},
		Targets:  []change.Target{ruleProfileTarget(rp)},
		Resolved: ruleProfileResolved{Profile: rp.ID, ruleProfileFields: after, Fetched: reload && after.SourceURL != ""},
	}, nil
}

func applyRuleProfile(ctx context.Context, q *db.Queries, raw json.RawMessage, now time.Time) (any, error) {
	r, err := decode[ruleProfileResolved](raw)
	if err != nil {
		return nil, err
	}
	rp, err := q.GetRuleProfile(ctx, r.Profile)
	if err != nil {
		return nil, err
	}
	prof, err := rules.Parse(r.Content)
	if err != nil {
		return nil, apierr.BadRequest("invalid_rule_profile", "规则模板解析失败：%v", err)
	}
	fetched := rp.FetchedAt
	if r.Fetched {
		fetched = &now
	}
	if r.SourceURL == "" {
		fetched = nil
	}
	u, err := q.UpdateRuleProfile(ctx, db.UpdateRuleProfileParams{ID: rp.ID, Name: r.Name, Description: r.Description,
		SourceUrl: r.SourceURL, Content: r.Content, UserSelectable: r.UserSelectable, FetchedAt: fetched})
	if err != nil {
		return nil, err
	}
	if err := rules.EnsureLists(ctx, q, prof); err != nil {
		return nil, err
	}
	return map[string]any{"rule_profile_id": u.ID, "version": u.Version}, nil
}

func (o *RuleProfileUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	return applyRuleProfile(ctx, q, raw, o.d.now())
}

// RuleProfileRefresh 立即重新拉取模板（平时每 6 小时自动拉取一次）。
type RuleProfileRefresh struct{ d *Deps }

func (*RuleProfileRefresh) Kind() string      { return "rule_profile.refresh" }
func (*RuleProfileRefresh) Risk() change.Risk { return change.Normal }

type ruleProfileRefParams struct {
	Profile Ref `json:"rule_profile"`
}

func (o *RuleProfileRefresh) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ruleProfileRefParams](raw)
	if err != nil {
		return nil, err
	}
	rp, err := findRuleProfile(ctx, q, p.Profile)
	if err != nil {
		return nil, err
	}
	if rp.SourceUrl == "" {
		return nil, apierr.BadRequest("no_source_url", "分流规则 %s 是直接粘贴的内容，没有可以拉取的地址", rp.Name).
			WithHint("用 PATCH /api/v1/rule-profiles/%s 修改 content", rp.Code)
	}
	before, after := fieldsOf(rp), fieldsOf(rp)
	prof, err := o.d.loadRuleContent(ctx, &after)
	if err != nil {
		return nil, err
	}
	users, err := q.CountRuleProfileUsers(ctx, rp.Code)
	if err != nil {
		return nil, err
	}
	steps := append([]string{fmt.Sprintf("从 %s 重新拉取", rp.SourceUrl)}, ruleSteps(rp.Name, prof)...)
	diff := fieldDiff(summarize(before), summarize(after))
	if after.Content == before.Content {
		steps = append(steps, "内容和上次拉取的一样")
	} else {
		steps = append(steps, fmt.Sprintf("使用这份规则的 %d 名用户在客户端下次更新订阅时生效", users))
		if len(diff) == 0 {
			diff = append(diff, change.DiffEntry{Field: "content", Before: "（原内容）", After: "（新内容）"})
		}
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("重新拉取分流规则 %s", rp.Name),
		Steps:    steps,
		Diff:     diff,
		Impact:   change.Impact{Users: users},
		Targets:  []change.Target{ruleProfileTarget(rp)},
		Resolved: ruleProfileResolved{Profile: rp.ID, ruleProfileFields: after, Fetched: true},
	}, nil
}

func (o *RuleProfileRefresh) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	return applyRuleProfile(ctx, q, raw, o.d.now())
}

type RuleProfileDelete struct{ d *Deps }

func (*RuleProfileDelete) Kind() string      { return "rule_profile.delete" }
func (*RuleProfileDelete) Risk() change.Risk { return change.Normal }

func (o *RuleProfileDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[ruleProfileRefParams](raw)
	if err != nil {
		return nil, err
	}
	rp, err := findRuleProfile(ctx, q, p.Profile)
	if err != nil {
		return nil, err
	}
	users, err := q.CountRuleProfileUsers(ctx, rp.Code)
	if err != nil {
		return nil, err
	}
	var plans []string
	all, err := q.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	for _, pl := range all {
		if pl.RuleProfile != nil && *pl.RuleProfile == rp.Code {
			plans = append(plans, pl.Name)
		}
	}
	steps := []string{fmt.Sprintf("删除分流规则 %s（%s）", rp.Name, rp.Code)}
	if len(plans) > 0 {
		steps = append(steps, fmt.Sprintf("套餐 %s 的默认规则清空，改用内置规则", strings.Join(plans, "、")))
	}
	if users > 0 {
		steps = append(steps, fmt.Sprintf("正在使用它的 %d 名用户改为跟随套餐默认规则（没有时用内置规则），客户端下次更新订阅时生效", users))
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除分流规则 %s", rp.Name),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "分流规则", Before: rp.Name, After: nil}},
		Impact:  change.Impact{Users: users},
		Targets: []change.Target{ruleProfileTarget(rp)},
	}, nil
}

func (o *RuleProfileDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[ruleProfileRefParams](raw)
	if err != nil {
		return nil, err
	}
	rp, err := findRuleProfile(ctx, q, p.Profile)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteRuleProfile(ctx, rp.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": rp.Code}, nil
}

// resolveRuleProfile 校验套餐上设置的默认规则代号；空字符串表示清除。
func resolveRuleProfile(ctx context.Context, q *db.Queries, code string) (string, error) {
	if code == "" {
		return "", nil
	}
	rp, err := findRuleProfile(ctx, q, Ref(code))
	if err != nil {
		return "", err
	}
	return rp.Code, nil
}
