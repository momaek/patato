// Package change 实现变更计划引擎：每个写操作先生成计划（dry_run），执行时只认计划。
//
// 计划记录受影响对象及其版本号，执行时加锁核对；版本变了就作废，要求重新预演。
// 风险等级由操作声明的最低等级和本次影响面共同决定，敏感操作转为审批。
package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/metrics"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type Risk string

const (
	Read      Risk = "read"
	Normal    Risk = "normal"
	Sensitive Risk = "sensitive"
	Forbidden Risk = "forbidden"
)

func (r Risk) rank() int {
	switch r {
	case Read:
		return 0
	case Normal:
		return 1
	case Sensitive:
		return 2
	case Forbidden:
		return 3
	}
	return 3
}

// Target 是计划涉及的对象及预演时的版本号。
type Target struct {
	Type    string `json:"type"`
	ID      int64  `json:"id"`
	Version int64  `json:"version"`
}

type DiffEntry struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type Impact struct {
	Users int64 `json:"users"`
	Nodes int   `json:"nodes"`
}

// Draft 是操作在预演阶段给出的结果。
type Draft struct {
	Summary string
	Steps   []string
	Diff    []DiffEntry
	Impact  Impact
	Targets []Target
	// Escalate 非空时无论影响面多大都按敏感处理，例如涉及价格。
	Escalate string
	// Resolved 非空时替换原始参数保存进计划，执行阶段按它执行，
	// 用来固定预演时推断出的值（端口、名称、密钥等）。
	Resolved any
}

// Operation 是一种写操作。Plan 只读，Apply 在目标已加锁、版本已核对的事务里执行。
type Operation interface {
	Kind() string
	Risk() Risk
	Plan(ctx context.Context, q *db.Queries, params json.RawMessage) (*Draft, error)
	Apply(ctx context.Context, q *db.Queries, params json.RawMessage) (any, error)
}

// Actor 是发起方。Kind 为 operator（网页会话）、token（CLI / Agent）或 system。
type Actor struct {
	Kind        string
	ID          int64
	Label       string
	Claim       string
	Entry       string
	Instruction string
	CanWrite    bool
}

// Plan 是对外返回的变更计划。
type Plan struct {
	ID           string      `json:"change_id"`
	Kind         string      `json:"kind"`
	Summary      string      `json:"summary"`
	Steps        []string    `json:"plan"`
	Impact       Impact      `json:"impact"`
	Diff         []DiffEntry `json:"diff"`
	Targets      []Target    `json:"targets"`
	DeclaredRisk Risk        `json:"declared_risk"`
	Risk         Risk        `json:"risk"`
	RiskReason   string      `json:"risk_reason,omitempty"`
	Status       string      `json:"status"`
	ExpiresAt    time.Time   `json:"expires_at"`
	ApprovalID   string      `json:"approval_id,omitempty"`
	Result       any         `json:"result,omitempty"`
	Error        string      `json:"error,omitempty"`
}

type Thresholds struct {
	Users int
	Nodes int
}

// ApprovalPolicy 决定一种操作的审批要求：需要几名运营者批准，批准时是否还要一次性码。
type ApprovalPolicy func(kind string) (required int, needsCode bool)

// ApprovalNotifier 在敏感操作生成审批后被调用，用于推送审批卡片。
type ApprovalNotifier interface {
	ApprovalCreated(ctx context.Context, approvalID string)
}

type Engine struct {
	st          *store.Store
	ops         map[string]Operation
	thresholds  Thresholds
	planTTL     time.Duration
	approvalTTL time.Duration
	notifier    ApprovalNotifier
	policy      ApprovalPolicy
	log         *slog.Logger
}

func NewEngine(st *store.Store, th Thresholds, planTTL, approvalTTL time.Duration, log *slog.Logger) *Engine {
	return &Engine{st: st, ops: map[string]Operation{}, thresholds: th, planTTL: planTTL, approvalTTL: approvalTTL, log: log}
}

func (e *Engine) Register(ops ...Operation) {
	for _, op := range ops {
		e.ops[op.Kind()] = op
	}
}

func (e *Engine) SetNotifier(n ApprovalNotifier) { e.notifier = n }

func (e *Engine) SetPolicy(p ApprovalPolicy) { e.policy = p }

func (e *Engine) approvalPolicy(kind string) (int32, bool) {
	if e.policy == nil {
		return 1, false
	}
	n, code := e.policy(kind)
	if n < 1 {
		n = 1
	}
	return int32(n), code
}

func (e *Engine) Operation(kind string) (Operation, bool) {
	op, ok := e.ops[kind]
	return op, ok
}

// Kinds 列出已注册的操作，供 skill 与工具清单使用。
func (e *Engine) Kinds() map[string]Risk {
	m := map[string]Risk{}
	for k, op := range e.ops {
		m[k] = op.Risk()
	}
	return m
}

// effectiveRisk 按影响面升级：声明为常规的操作，影响超过阈值时升为敏感。
func (e *Engine) effectiveRisk(declared Risk, d *Draft) (Risk, string) {
	if declared.rank() >= Sensitive.rank() {
		return declared, ""
	}
	if d.Escalate != "" {
		return Sensitive, d.Escalate
	}
	if declared == Normal {
		if d.Impact.Users > int64(e.thresholds.Users) {
			return Sensitive, fmt.Sprintf("受影响用户 %d 人，超过 %d 人的阈值，常规操作升为敏感", d.Impact.Users, e.thresholds.Users)
		}
		if d.Impact.Nodes > e.thresholds.Nodes {
			return Sensitive, fmt.Sprintf("涉及节点 %d 个，超过 %d 个的阈值，常规操作升为敏感", d.Impact.Nodes, e.thresholds.Nodes)
		}
	}
	return declared, ""
}

// Preview 生成并保存变更计划，不改动任何业务数据。
func (e *Engine) Preview(ctx context.Context, actor Actor, kind string, params json.RawMessage) (*Plan, error) {
	op, ok := e.ops[kind]
	if !ok {
		return nil, apierr.BadRequest("unknown_operation", "不支持的操作 %s", kind)
	}
	if op.Risk() == Forbidden && actor.Kind == "token" {
		return nil, apierr.Forbidden("forbidden_operation", "%s 是禁止级操作，CLI / Agent token 无权调用", kind)
	}
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	draft, err := op.Plan(ctx, e.st.Queries, params)
	if err != nil {
		return nil, err
	}
	risk, reason := e.effectiveRisk(op.Risk(), draft)
	if draft.Resolved != nil {
		params = mustJSON(draft.Resolved)
	}
	p := &Plan{
		ID:           secret.ID("chg"),
		Kind:         kind,
		Summary:      draft.Summary,
		Steps:        nonNil(draft.Steps),
		Impact:       draft.Impact,
		Diff:         nonNilDiff(draft.Diff),
		Targets:      nonNilTargets(draft.Targets),
		DeclaredRisk: op.Risk(),
		Risk:         risk,
		RiskReason:   reason,
		Status:       "planned",
		ExpiresAt:    time.Now().Add(e.planTTL).UTC().Truncate(time.Second),
	}
	err = e.st.InsertChangePlan(ctx, db.InsertChangePlanParams{
		ID:           p.ID,
		Kind:         kind,
		Params:       params,
		Steps:        mustJSON(p.Steps),
		Diff:         mustJSON(p.Diff),
		Impact:       mustJSON(p.Impact),
		Targets:      mustJSON(p.Targets),
		Summary:      p.Summary,
		DeclaredRisk: string(p.DeclaredRisk),
		Risk:         string(p.Risk),
		RiskReason:   p.RiskReason,
		SubjectKind:  actor.Kind,
		SubjectID:    actor.ID,
		SubjectLabel: actor.Label,
		ActorClaim:   actor.Claim,
		Entry:        actor.Entry,
		Instruction:  actor.Instruction,
		ExpiresAt:    p.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Execute 等价于预演后立即执行。
func (e *Engine) Execute(ctx context.Context, actor Actor, kind string, params json.RawMessage) (*Plan, error) {
	p, err := e.Preview(ctx, actor, kind, params)
	if err != nil {
		return nil, err
	}
	return e.Apply(ctx, actor, p.ID)
}

// Apply 执行一份预演好的计划。敏感计划不会执行，而是生成审批。
func (e *Engine) Apply(ctx context.Context, actor Actor, changeID string) (*Plan, error) {
	if !actor.CanWrite {
		return nil, apierr.Forbidden("read_only_token", "当前 token 只有只读权限").
			WithHint("用 patato auth login --scope write 重新授权")
	}
	var out *Plan
	var createdApproval string
	err := e.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		row, err := q.LockChangePlan(ctx, changeID)
		if store.IsNotFound(err) {
			return apierr.NotFound("change_not_found", "变更计划 %s 不存在", changeID)
		}
		if err != nil {
			return err
		}
		if row.Status != "planned" {
			return apierr.Conflict("change_not_planned", "变更计划 %s 当前状态是 %s，不能执行", changeID, row.Status).
				WithHint("用 GET /api/v1/changes/%s 查看状态；需要的话重新预演", changeID)
		}
		if time.Now().After(row.ExpiresAt) {
			if err := q.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: changeID, Status: "expired"}); err != nil {
				return err
			}
			out = fromRow(row)
			out.Status = "expired"
			return nil
		}
		if Risk(row.Risk) == Sensitive {
			required, needsCode := e.approvalPolicy(row.Kind)
			apr, err := q.CreateApproval(ctx, db.CreateApprovalParams{
				ID:        secret.ID("apr"),
				ChangeID:  changeID,
				ExpiresAt: time.Now().Add(e.approvalTTL).UTC(),
				Required:  required,
				NeedsCode: needsCode,
			})
			if err != nil {
				return err
			}
			if err := q.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: changeID, Status: "pending_approval"}); err != nil {
				return err
			}
			createdApproval = apr.ID
			out = fromRow(row)
			out.Status = "pending_approval"
			out.ApprovalID = apr.ID
			return audit(ctx, q, actor, row, "pending_approval", map[string]any{"approval_id": apr.ID})
		}
		out, err = e.applyLocked(ctx, q, actor, row)
		return err
	})
	var opErr *OpError
	if errors.As(err, &opErr) {
		e.MarkFailed(ctx, actor, changeID, opErr.Err)
		return nil, opErr.Err
	}
	if err != nil {
		return nil, err
	}
	if createdApproval != "" && e.notifier != nil {
		e.notifier.ApprovalCreated(context.WithoutCancel(ctx), createdApproval)
	}
	return out, nil
}

// OpError 表示操作本身在执行阶段失败（区别于计划不存在、已过期等）。
type OpError struct{ Err error }

func (e *OpError) Error() string { return e.Err.Error() }
func (e *OpError) Unwrap() error { return e.Err }

// MarkFailed 在事务回滚后把计划标记为失败并写审计，失败原因对外可见。
func (e *Engine) MarkFailed(ctx context.Context, actor Actor, changeID string, cause error) {
	msg := apierr.From(cause).Message
	if ae := apierr.From(cause); ae == apierr.ErrInternal {
		e.log.Error("apply change failed", "change_id", changeID, "err", cause)
	}
	_ = e.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		row, err := q.LockChangePlan(ctx, changeID)
		if err != nil {
			return err
		}
		if err := q.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: changeID, Status: "failed", Error: msg}); err != nil {
			return err
		}
		return audit(ctx, q, actor, row, "failed", map[string]any{"error": msg})
	})
}

// ApplyApproved 在审批通过后执行计划。调用方负责在同一事务里更新审批状态。
func (e *Engine) ApplyApproved(ctx context.Context, q *db.Queries, actor Actor, changeID string) (*Plan, error) {
	row, err := q.LockChangePlan(ctx, changeID)
	if err != nil {
		return nil, err
	}
	if row.Status != "pending_approval" {
		return nil, apierr.Conflict("change_not_pending", "变更计划 %s 当前状态是 %s", changeID, row.Status)
	}
	return e.applyLocked(ctx, q, actor, row)
}

func (e *Engine) applyLocked(ctx context.Context, q *db.Queries, actor Actor, row db.ChangePlan) (*Plan, error) {
	op, ok := e.ops[row.Kind]
	if !ok {
		return nil, apierr.BadRequest("unknown_operation", "不支持的操作 %s", row.Kind)
	}
	var targets []Target
	if err := json.Unmarshal(row.Targets, &targets); err != nil {
		return nil, err
	}
	out := fromRow(row)
	if stale := checkTargets(ctx, q, targets); stale != "" {
		if err := q.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: row.ID, Status: "stale", Error: stale}); err != nil {
			return nil, err
		}
		out.Status = "stale"
		out.Error = stale
		return out, audit(ctx, q, actor, row, "voided", map[string]any{"reason": stale})
	}
	result, err := op.Apply(ctx, q, row.Params)
	if err != nil {
		// 事务会整体回滚，由调用方在事务外记录失败。
		return nil, &OpError{Err: err}
	}
	resJSON := mustJSON(result)
	if err := q.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: row.ID, Status: "applied", Result: resJSON}); err != nil {
		return nil, err
	}
	out.Status = "applied"
	out.Result = result
	return out, audit(ctx, q, actor, row, "success", nil)
}

// checkTargets 锁住每个目标并核对版本，返回不一致的原因；一致时返回空串。
func checkTargets(ctx context.Context, q *db.Queries, targets []Target) string {
	for _, t := range targets {
		var v int64
		var err error
		switch t.Type {
		case "node":
			v, err = q.NodeVersionForUpdate(ctx, t.ID)
		case "plan":
			v, err = q.PlanVersionForUpdate(ctx, t.ID)
		case "user":
			v, err = q.UserVersionForUpdate(ctx, t.ID)
		case "template":
			v, err = q.TemplateVersionForUpdate(ctx, t.ID)
		case "rule_profile":
			v, err = q.RuleProfileVersionForUpdate(ctx, t.ID)
		case "machine":
			v, err = q.MachineVersionForUpdate(ctx, t.ID)
		case "order":
			v, err = q.OrderVersionForUpdate(ctx, t.ID)
		case "coupon":
			v, err = q.CouponVersionForUpdate(ctx, t.ID)
		case "ticket":
			v, err = q.TicketVersionForUpdate(ctx, t.ID)
		case "webhook":
			v, err = q.WebhookVersionForUpdate(ctx, t.ID)
		case "automation":
			v, err = q.AutomationRuleVersionForUpdate(ctx, t.ID)
		case "setting":
			// 设置没有数字 ID，用 SettingKeys 里的序号；版本是最后修改时间（微秒），没有记录时为 0
			v, err = q.SettingVersionForUpdate(ctx, SettingKeys[t.ID])
			if store.IsNotFound(err) {
				v, err = 0, nil
			}
		default:
			return fmt.Sprintf("未知的目标类型 %s", t.Type)
		}
		if store.IsNotFound(err) {
			return fmt.Sprintf("%s %d 已被删除", t.Type, t.ID)
		}
		if err != nil {
			return fmt.Sprintf("读取 %s %d 失败", t.Type, t.ID)
		}
		if v != t.Version {
			return fmt.Sprintf("%s %d 在预演后被修改过（版本 %d → %d）", t.Type, t.ID, t.Version, v)
		}
	}
	return ""
}

// SettingKeys 是可以作为变更目标的设置项，Target.ID 是这里的下标。
var SettingKeys = []string{"referral.rules"}

// SettingTarget 返回某个设置项当前版本的目标。
func SettingTarget(ctx context.Context, q *db.Queries, key string) (Target, error) {
	for i, k := range SettingKeys {
		if k != key {
			continue
		}
		v, err := q.SettingVersionForUpdate(ctx, key)
		if store.IsNotFound(err) {
			v, err = 0, nil
		}
		return Target{Type: "setting", ID: int64(i), Version: v}, err
	}
	return Target{}, fmt.Errorf("unknown setting %s", key)
}

// Get 读取一份计划。
func (e *Engine) Get(ctx context.Context, id string) (*Plan, error) {
	row, err := e.st.GetChangePlan(ctx, id)
	if store.IsNotFound(err) {
		return nil, apierr.NotFound("change_not_found", "变更计划 %s 不存在", id)
	}
	if err != nil {
		return nil, err
	}
	p := fromRow(row)
	if apr, err := e.st.GetApprovalByChange(ctx, id); err == nil {
		p.ApprovalID = apr.ID
	}
	return p, nil
}

// StatusCode 给出执行结果对应的 HTTP 状态：待审批返回 202。
func StatusCode(p *Plan) int {
	switch p.Status {
	case "pending_approval":
		return http.StatusAccepted
	case "stale", "expired":
		return http.StatusConflict
	}
	return http.StatusOK
}

func fromRow(r db.ChangePlan) *Plan {
	p := &Plan{
		ID:           r.ID,
		Kind:         r.Kind,
		Summary:      r.Summary,
		DeclaredRisk: Risk(r.DeclaredRisk),
		Risk:         Risk(r.Risk),
		RiskReason:   r.RiskReason,
		Status:       r.Status,
		ExpiresAt:    r.ExpiresAt,
		Error:        r.Error,
	}
	_ = json.Unmarshal(r.Steps, &p.Steps)
	_ = json.Unmarshal(r.Diff, &p.Diff)
	_ = json.Unmarshal(r.Impact, &p.Impact)
	_ = json.Unmarshal(r.Targets, &p.Targets)
	if len(r.Result) > 0 {
		var v any
		if json.Unmarshal(r.Result, &v) == nil {
			p.Result = v
		}
	}
	return p
}

func audit(ctx context.Context, q *db.Queries, a Actor, row db.ChangePlan, result string, detail map[string]any) error {
	metrics.Changes.WithLabelValues(row.Kind, result).Inc()
	if detail == nil {
		detail = map[string]any{}
	}
	detail["summary"] = row.Summary
	return q.InsertAudit(ctx, db.InsertAuditParams{
		SubjectKind:  a.Kind,
		SubjectID:    a.ID,
		SubjectLabel: a.Label,
		ActorClaim:   a.Claim,
		Entry:        a.Entry,
		Action:       row.Kind,
		Target:       primaryTarget(row.Targets),
		Risk:         row.Risk,
		ChangeID:     row.ID,
		Result:       result,
		Detail:       mustJSON(detail),
	})
}

func primaryTarget(raw json.RawMessage) string {
	var ts []Target
	if json.Unmarshal(raw, &ts) != nil || len(ts) == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", ts[0].Type, ts[0].ID)
}

func mustJSON(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilDiff(s []DiffEntry) []DiffEntry {
	if s == nil {
		return []DiffEntry{}
	}
	return s
}

func nonNilTargets(s []Target) []Target {
	if s == nil {
		return []Target{}
	}
	return s
}
