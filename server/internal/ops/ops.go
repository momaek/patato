// Package ops 实现所有业务写操作。每个操作都是 change.Operation：
// Plan 只读并给出计划，Apply 在目标已加锁、版本已核对的事务里执行。
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/rules"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// Machine 是从 tolato 查到的机器信息。
type Machine struct {
	Name    string
	IP      string
	Country string
	City    string
}

// MachineLookup 按机器名查询 tolato；未配置 tolato 时为 nil。
type MachineLookup interface {
	LookupMachine(ctx context.Context, name string) (*Machine, error)
}

// EventSink 记录和解决事件，由 events.Recorder 实现。
type EventSink interface {
	Emit(ctx context.Context, q *db.Queries, e events.Event)
	Resolve(ctx context.Context, q *db.Queries, dedupKey string)
}

type Deps struct {
	Box      *secret.Box
	Machines MachineLookup
	Events   EventSink
	Loc      *time.Location
	Now      func() time.Time
	// Operations 返回已注册的操作及其风险等级，自动处置规则校验动作时用；装配时由引擎提供。
	Operations func() map[string]change.Risk
	// Rules 拉取分流规则模板；未配置时只能使用粘贴的模板内容。
	Rules RuleFetcher
}

// RuleFetcher 下载并解析 ini 规则模板，由 rules.Service 实现。
type RuleFetcher interface {
	FetchProfile(ctx context.Context, url string) (string, *rules.Profile, error)
}

func (d *Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// All 返回全部操作，供引擎注册。
func All(d *Deps) []change.Operation {
	return []change.Operation{
		&NodeProvision{d}, &NodeUpdate{d}, &NodeAttach{d, true}, &NodeAttach{d, false},
		&NodeDelete{d}, &NodeRotateKey{d}, &MachineRevoke{d},
		&MachineSetNetwork{d}, &RouteCreate{d}, &RouteUpdate{d}, &RouteDelete{d}, &RouteApply{d},
		&EntryAdd{d}, &EntrySetEnabled{d, true}, &EntrySetEnabled{d, false}, &EntryDelete{d}, &EntryAdopt{d},
		&TemplateCreate{d}, &TemplateUpdate{d},
		&PlanCreate{d}, &PlanUpdate{d}, &PlanDelete{d},
		&RuleProfileCreate{d}, &RuleProfileUpdate{d}, &RuleProfileRefresh{d}, &RuleProfileDelete{d},
		&UserCreate{d}, &UserUpdate{d}, &UserResetTraffic{d}, &UserResetToken{d}, &UserDelete{d},
		&OrderRefund{d}, &OrderMarkPaid{d}, &OrderCancel{d}, &CouponCreate{d}, &CouponUpdate{d},
		&ReferralSettings{d}, &ReferralRelease{d},
		&TicketDraft{d}, &TicketClose{d},
		&WebhookCreate{d}, &WebhookDelete{d},
		&AutomationCreate{d}, &AutomationUpdate{d}, &AutomationDelete{d},
	}
}

func decode[T any](raw json.RawMessage) (*T, error) {
	var v T
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return nil, apierr.BadRequest("invalid_params", "参数格式不对：%v", err)
	}
	return &v, nil
}

// Ref 接受 ID（数字）或代号，例如节点的 tokyo-03、套餐的 premium、用户的邮箱。
type Ref string

func (r *Ref) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*r = Ref(n.String())
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("expected id or code")
	}
	*r = Ref(s)
	return nil
}

func (r Ref) id() (int64, bool) {
	n, err := strconv.ParseInt(string(r), 10, 64)
	return n, err == nil
}

func findNode(ctx context.Context, q *db.Queries, r Ref) (db.Node, error) {
	if r == "" {
		return db.Node{}, apierr.BadRequest("missing_node", "缺少节点").WithHint("传节点 ID 或代号；用 GET /api/v1/nodes 查看全部")
	}
	var n db.Node
	var err error
	if id, ok := r.id(); ok {
		n, err = q.GetNode(ctx, id)
	} else {
		n, err = q.GetNodeByCode(ctx, string(r))
	}
	if store.IsNotFound(err) {
		return n, apierr.NotFound("node_not_found", "节点 %s 不存在", r).WithHint("用 GET /api/v1/nodes 查看全部节点")
	}
	return n, err
}

func findPlan(ctx context.Context, q *db.Queries, r Ref) (db.Plan, error) {
	if r == "" {
		return db.Plan{}, apierr.BadRequest("missing_plan", "缺少套餐").WithHint("传套餐 ID 或代号；用 GET /api/v1/plans 查看全部")
	}
	var p db.Plan
	var err error
	if id, ok := r.id(); ok {
		p, err = q.GetPlan(ctx, id)
	} else {
		p, err = q.GetPlanByCode(ctx, string(r))
	}
	if store.IsNotFound(err) {
		return p, planNotFound(ctx, q, r)
	}
	return p, err
}

func planNotFound(ctx context.Context, q *db.Queries, r Ref) error {
	e := apierr.NotFound("plan_not_found", "套餐 %s 不存在", r)
	plans, err := q.ListPlans(ctx)
	if err != nil || len(plans) == 0 {
		return e.WithHint("还没有任何套餐；用 POST /api/v1/plans 创建")
	}
	codes := make([]string, 0, len(plans))
	for _, p := range plans {
		codes = append(codes, p.Code)
	}
	return e.WithHint("可用套餐：%s。用 GET /api/v1/plans 查看全部", strings.Join(codes, ", "))
}

func findTemplate(ctx context.Context, q *db.Queries, r Ref) (db.NodeTemplate, error) {
	if r == "" {
		return db.NodeTemplate{}, apierr.BadRequest("missing_template", "缺少节点模板").WithHint("用 GET /api/v1/templates 查看可用模板")
	}
	var t db.NodeTemplate
	var err error
	if id, ok := r.id(); ok {
		t, err = q.GetTemplate(ctx, id)
	} else {
		t, err = q.GetTemplateByCode(ctx, string(r))
	}
	if store.IsNotFound(err) {
		e := apierr.NotFound("template_not_found", "节点模板 %s 不存在", r)
		ts, lerr := q.ListTemplates(ctx)
		if lerr == nil && len(ts) > 0 {
			codes := make([]string, 0, len(ts))
			for _, t := range ts {
				codes = append(codes, t.Code)
			}
			return t, e.WithHint("可用模板：%s", strings.Join(codes, ", "))
		}
		return t, e.WithHint("还没有任何模板；用 POST /api/v1/templates 创建")
	}
	return t, err
}

func findUser(ctx context.Context, q *db.Queries, r Ref) (db.User, error) {
	if r == "" {
		return db.User{}, apierr.BadRequest("missing_user", "缺少用户").WithHint("传用户 ID 或邮箱")
	}
	var u db.User
	var err error
	if id, ok := r.id(); ok {
		u, err = q.GetUser(ctx, id)
	} else {
		u, err = q.GetUserByEmail(ctx, string(r))
	}
	if store.IsNotFound(err) {
		return u, apierr.NotFound("user_not_found", "用户 %s 不存在", r).WithHint("用 GET /api/v1/users?q=<邮箱> 搜索")
	}
	return u, err
}

func nodeTarget(n db.Node) change.Target {
	return change.Target{Type: "node", ID: n.ID, Version: n.Version}
}

func planTarget(p db.Plan) change.Target {
	return change.Target{Type: "plan", ID: p.ID, Version: p.Version}
}

func userTarget(u db.User) change.Target {
	return change.Target{Type: "user", ID: u.ID, Version: u.Version}
}

func ruleProfileTarget(r db.RuleProfile) change.Target {
	return change.Target{Type: "rule_profile", ID: r.ID, Version: r.Version}
}

func templateTarget(t db.NodeTemplate) change.Target {
	return change.Target{Type: "template", ID: t.ID, Version: t.Version}
}

func gb(b int64) string { return fmt.Sprintf("%.0f GB", float64(b)/(1<<30)) }

func isUniqueViolation(err error) bool { return store.IsUniqueViolation(err) }

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
