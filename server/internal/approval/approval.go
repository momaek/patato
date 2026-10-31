// Package approval 管理敏感操作的审批：批准、拒绝、过期。
//
// 批准信号只能来自运营者会话（网页 + passkey）或绑定过的 Telegram 账号；
// CLI / Agent token 只能发起审批，永远不能完成审批。
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// 批准渠道
const (
	ViaWeb      = "web"
	ViaCLI      = "cli_passkey"
	ViaTelegram = "telegram"
)

// Notifier 在审批状态变化时被调用，用于更新 Telegram 卡片。
type Notifier interface {
	ApprovalDecided(ctx context.Context, approvalID string)
}

type Service struct {
	st       *store.Store
	eng      *change.Engine
	notifier Notifier
	log      *slog.Logger
}

func New(st *store.Store, eng *change.Engine, log *slog.Logger) *Service {
	return &Service{st: st, eng: eng, log: log}
}

func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// Detail 是审批卡片需要的全部信息。
type Detail struct {
	ID          string       `json:"approval_id"`
	Status      string       `json:"status"`
	ExpiresAt   time.Time    `json:"expires_at"`
	CreatedAt   time.Time    `json:"created_at"`
	DecidedBy   string       `json:"decided_by,omitempty"`
	DecidedVia  string       `json:"decided_via,omitempty"`
	DecidedAt   *time.Time   `json:"decided_at,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	Change      *change.Plan `json:"change"`
	Requester   Requester    `json:"requester"`
	Instruction string       `json:"instruction,omitempty"`
}

type Requester struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Claim string `json:"actor_claim,omitempty"`
	Entry string `json:"entry"`
}

func (s *Service) Get(ctx context.Context, id string) (*Detail, error) {
	a, err := s.st.GetApproval(ctx, id)
	if store.IsNotFound(err) {
		return nil, apierr.NotFound("approval_not_found", "审批 %s 不存在", id)
	}
	if err != nil {
		return nil, err
	}
	row, err := s.st.GetChangePlan(ctx, a.ChangeID)
	if err != nil {
		return nil, err
	}
	plan, err := s.eng.Get(ctx, a.ChangeID)
	if err != nil {
		return nil, err
	}
	d := &Detail{
		ID: a.ID, Status: effectiveStatus(a), ExpiresAt: a.ExpiresAt, CreatedAt: a.CreatedAt,
		DecidedVia: a.DecidedVia, DecidedAt: a.DecidedAt, Reason: a.Reason, Change: plan,
		Requester:   Requester{Kind: row.SubjectKind, Label: row.SubjectLabel, Claim: row.ActorClaim, Entry: row.Entry},
		Instruction: row.Instruction,
	}
	if a.DecidedBy != nil {
		if op, err := s.st.GetOperator(ctx, *a.DecidedBy); err == nil {
			d.DecidedBy = op.Name
		}
	}
	return d, nil
}

// effectiveStatus 把已过期但还没被后台任务处理的审批显示为过期。
func effectiveStatus(a db.Approval) string {
	if a.Status == "pending" && time.Now().After(a.ExpiresAt) {
		return "expired"
	}
	return a.Status
}

// Approve 批准并执行变更。执行时数据已变化（版本不一致）则作废审批。
func (s *Service) Approve(ctx context.Context, id string, op db.Operator, via string) (*Detail, error) {
	actor := change.Actor{Kind: "operator", ID: op.ID, Label: op.Name, Entry: via, CanWrite: true}
	var changeID string
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		a, err := s.lockPending(ctx, q, id)
		if err != nil {
			return err
		}
		changeID = a.ChangeID
		plan, err := s.eng.ApplyApproved(ctx, q, actor, a.ChangeID)
		if err != nil {
			return err
		}
		status, reason := "approved", ""
		if plan.Status == "stale" {
			status, reason = "voided", plan.Error
		}
		_, err = q.DecideApproval(ctx, db.DecideApprovalParams{ID: id, Status: status, DecidedBy: &op.ID, DecidedVia: via, Reason: reason})
		return err
	})
	var opErr *change.OpError
	if errors.As(err, &opErr) {
		// 执行失败：审批本身仍记为批准，变更标记为失败并记录原因。
		_, derr := s.st.DecideApproval(ctx, db.DecideApprovalParams{ID: id, Status: "approved", DecidedBy: &op.ID, DecidedVia: via,
			Reason: "执行失败：" + apierr.From(opErr.Err).Message})
		if derr != nil {
			s.log.Error("record failed approval", "approval", id, "err", derr)
		}
		s.eng.MarkFailed(ctx, actor, changeID, opErr.Err)
		s.decided(ctx, id)
		return nil, opErr.Err
	}
	if err != nil {
		return nil, err
	}
	s.decided(ctx, id)
	return s.Get(ctx, id)
}

// Reject 拒绝变更，计划随之失效。
func (s *Service) Reject(ctx context.Context, id string, op db.Operator, via, reason string) (*Detail, error) {
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		a, err := s.lockPending(ctx, q, id)
		if err != nil {
			return err
		}
		row, err := q.LockChangePlan(ctx, a.ChangeID)
		if err != nil {
			return err
		}
		if _, err := q.DecideApproval(ctx, db.DecideApprovalParams{ID: id, Status: "rejected", DecidedBy: &op.ID, DecidedVia: via, Reason: reason}); err != nil {
			return err
		}
		if err := q.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: a.ChangeID, Status: "rejected"}); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"summary": row.Summary, "reason": reason})
		return q.InsertAudit(ctx, db.InsertAuditParams{
			SubjectKind: "operator", SubjectID: op.ID, SubjectLabel: op.Name, Entry: via, Action: row.Kind,
			Target: "", Risk: row.Risk, ChangeID: row.ID, Result: "rejected", Detail: detail,
		})
	})
	if err != nil {
		return nil, err
	}
	s.decided(ctx, id)
	return s.Get(ctx, id)
}

func (s *Service) lockPending(ctx context.Context, q *db.Queries, id string) (db.Approval, error) {
	a, err := q.LockApproval(ctx, id)
	if store.IsNotFound(err) {
		return a, apierr.NotFound("approval_not_found", "审批 %s 不存在", id)
	}
	if err != nil {
		return a, err
	}
	if a.Status != "pending" {
		return a, apierr.Conflict("approval_closed", "审批 %s 已经是 %s 状态", id, a.Status)
	}
	if time.Now().After(a.ExpiresAt) {
		return a, apierr.Conflict("approval_expired", "审批 %s 已过期", id).WithHint("重新预演并发起")
	}
	return a, nil
}

// ExpireDue 把超时未处理的审批标记为过期，对应的变更计划随之过期。
func (s *Service) ExpireDue(ctx context.Context) error {
	rows, err := s.st.ExpireApprovals(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := s.st.SetChangePlanStatus(ctx, db.SetChangePlanStatusParams{ID: r.ChangeID, Status: "expired"}); err != nil {
			return err
		}
		s.decided(ctx, r.ID)
	}
	return nil
}

func (s *Service) decided(ctx context.Context, id string) {
	if s.notifier != nil {
		s.notifier.ApprovalDecided(context.WithoutCancel(ctx), id)
	}
}

// Describe 把审批渲染成纯文本卡片，Telegram 和日志共用。
func Describe(d *Detail, loc *time.Location) string {
	c := d.Change
	text := fmt.Sprintf("【待审批】%s\n\n", c.Summary)
	for i, step := range c.Steps {
		text += fmt.Sprintf("%d. %s\n", i+1, step)
	}
	text += fmt.Sprintf("\n影响：%d 名用户，%d 个节点\n", c.Impact.Users, c.Impact.Nodes)
	if c.RiskReason != "" {
		text += "升级原因：" + c.RiskReason + "\n"
	}
	who := d.Requester.Label
	if d.Requester.Claim != "" {
		who += "（自报：" + d.Requester.Claim + "）"
	}
	text += "发起方：" + who + "\n"
	if d.Instruction != "" {
		text += "原始指令：" + d.Instruction + "\n"
	}
	text += fmt.Sprintf("有效期至 %s\n变更计划 %s", d.ExpiresAt.In(loc).Format("01-02 15:04"), c.ID)
	return text
}
