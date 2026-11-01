// Package approval 管理敏感操作的审批：批准、拒绝、过期。
//
// 批准信号只能来自运营者会话（网页 + passkey）或绑定过的 Telegram 账号；
// CLI / Agent token 只能发起审批，永远不能完成审批。
package approval

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/secret"
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

// CodeSender 把一次性码私下发给运营者（Telegram 私聊），不经过 Agent。
type CodeSender interface {
	SendCode(ctx context.Context, telegramUserID int64, text string) error
}

const (
	codeTTL         = 5 * time.Minute
	maxCodeAttempts = 5
)

type Service struct {
	st       *store.Store
	eng      *change.Engine
	notifier Notifier
	codes    CodeSender
	log      *slog.Logger
}

func New(st *store.Store, eng *change.Engine, log *slog.Logger) *Service {
	return &Service{st: st, eng: eng, log: log}
}

func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

func (s *Service) SetCodeSender(c CodeSender) { s.codes = c }

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
	// Required 是需要几名运营者批准；Votes 是已批准的人。
	Required  int    `json:"required"`
	Votes     []Vote `json:"votes"`
	NeedsCode bool   `json:"needs_code"`
}

type Vote struct {
	Operator string    `json:"operator"`
	Via      string    `json:"via"`
	At       time.Time `json:"at"`
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
		Required:    int(a.Required), NeedsCode: a.NeedsCode, Votes: []Vote{},
	}
	votes, err := s.st.ListApprovalVotes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, v := range votes {
		d.Votes = append(d.Votes, Vote{Operator: v.OperatorName, Via: v.Via, At: v.CreatedAt})
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

// Approve 记下一票批准；票数达到要求时执行变更。执行时数据已变化（版本不一致）则作废审批。
// 需要一次性码的审批只能在网页上批准，code 是经 Telegram 私聊收到的码。
func (s *Service) Approve(ctx context.Context, id string, op db.Operator, via, code string) (*Detail, error) {
	a, err := s.st.GetApproval(ctx, id)
	if store.IsNotFound(err) {
		return nil, apierr.NotFound("approval_not_found", "审批 %s 不存在", id)
	}
	if err != nil {
		return nil, err
	}
	if a.NeedsCode {
		if via == ViaTelegram {
			return nil, apierr.Forbidden("code_required", "这类审批需要在网页上输入一次性码并用 passkey 批准").
				WithHint("在 Admin 的审批页点「发送一次性码」")
		}
		if err := s.checkCode(ctx, id, op.ID, code); err != nil {
			return nil, err
		}
	}
	actor := change.Actor{Kind: "operator", ID: op.ID, Label: op.Name, Entry: via, CanWrite: true}
	var changeID string
	executed := false
	err = s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		a, err := s.lockPending(ctx, q, id)
		if err != nil {
			return err
		}
		changeID = a.ChangeID
		n, err := q.AddApprovalVote(ctx, db.AddApprovalVoteParams{ApprovalID: id, OperatorID: op.ID, Via: via})
		if err != nil {
			return err
		}
		if n == 0 {
			return apierr.Conflict("already_voted", "你已经批准过这个审批").WithHint("还需要其他运营者批准")
		}
		votes, err := q.ListApprovalVotes(ctx, id)
		if err != nil {
			return err
		}
		if len(votes) < int(a.Required) {
			row, err := q.LockChangePlan(ctx, a.ChangeID)
			if err != nil {
				return err
			}
			detail, _ := json.Marshal(map[string]any{"summary": row.Summary, "votes": len(votes), "required": a.Required})
			return q.InsertAudit(ctx, db.InsertAuditParams{
				SubjectKind: "operator", SubjectID: op.ID, SubjectLabel: op.Name, Entry: via, Action: row.Kind,
				Risk: row.Risk, ChangeID: row.ID, Result: "vote", Detail: detail,
			})
		}
		executed = true
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
		// 执行失败：审批本身仍记为批准，变更标记为失败并记录原因。投票随事务回滚，这里补记。
		_, _ = s.st.AddApprovalVote(ctx, db.AddApprovalVoteParams{ApprovalID: id, OperatorID: op.ID, Via: via})
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
	if executed {
		s.decided(ctx, id)
	}
	return s.Get(ctx, id)
}

// SendCode 给运营者绑定的 Telegram 私聊发一次性码，5 分钟内有效。
func (s *Service) SendCode(ctx context.Context, id string, op db.Operator) (time.Time, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	if d.Status != "pending" {
		return time.Time{}, apierr.Conflict("approval_closed", "审批 %s 已经是 %s 状态", id, d.Status)
	}
	if !d.NeedsCode {
		return time.Time{}, apierr.BadRequest("code_not_needed", "这个审批不需要一次性码")
	}
	if s.codes == nil {
		return time.Time{}, apierr.Conflict("telegram_not_configured", "没有配置 Telegram，发不了一次性码").
			WithHint("在服务端配置 telegram.bot_token，或把这类操作从 approval.code_for 里去掉")
	}
	if op.TelegramUserID == nil {
		return time.Time{}, apierr.Conflict("telegram_not_bound", "你的账号还没有绑定 Telegram").WithHint("在「账户与凭证」页生成绑定码")
	}
	code := secret.NumericCode(6)
	expires := time.Now().Add(codeTTL)
	if err := s.st.UpsertApprovalCode(ctx, db.UpsertApprovalCodeParams{ApprovalID: id, OperatorID: op.ID,
		CodeHash: codeHash(id, code), ExpiresAt: expires}); err != nil {
		return time.Time{}, err
	}
	text := fmt.Sprintf("一次性码：%s\n\n5 分钟内有效，只用于批准：%s\n只在 Patato Admin 的审批页输入，不要告诉任何人或 Agent。", code, d.Change.Summary)
	if err := s.codes.SendCode(ctx, *op.TelegramUserID, text); err != nil {
		return time.Time{}, fmt.Errorf("send code: %w", err)
	}
	return expires, nil
}

func codeHash(id, code string) []byte { return secret.Hash("approval:" + id + ":" + code) }

// checkCode 校验一次性码：错 5 次作废，用过即删。尝试次数单独提交，不随批准事务回滚。
func (s *Service) checkCode(ctx context.Context, id string, opID int64, code string) error {
	if strings.TrimSpace(code) == "" {
		return apierr.BadRequest("code_required", "需要一次性码").WithHint("点「发送一次性码」，在 Telegram 私聊里查收")
	}
	var bad error
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		c, err := q.GetApprovalCode(ctx, db.GetApprovalCodeParams{ApprovalID: id, OperatorID: opID})
		if store.IsNotFound(err) {
			bad = apierr.BadRequest("code_invalid", "一次性码不对或已过期").WithHint("重新发送一次性码")
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(c.ExpiresAt) || c.Attempts >= maxCodeAttempts {
			bad = apierr.BadRequest("code_invalid", "一次性码已过期或错误次数太多").WithHint("重新发送一次性码")
			return q.DeleteApprovalCode(ctx, db.DeleteApprovalCodeParams{ApprovalID: id, OperatorID: opID})
		}
		if subtle.ConstantTimeCompare(c.CodeHash, codeHash(id, strings.TrimSpace(code))) != 1 {
			bad = apierr.BadRequest("code_invalid", "一次性码不对")
			return q.BumpApprovalCodeAttempts(ctx, db.BumpApprovalCodeAttemptsParams{ApprovalID: id, OperatorID: opID})
		}
		return q.DeleteApprovalCode(ctx, db.DeleteApprovalCodeParams{ApprovalID: id, OperatorID: opID})
	})
	if err != nil {
		return err
	}
	return bad
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
	if d.Required > 1 {
		text += fmt.Sprintf("需要 %d 名运营者批准，已批准 %d 名\n", d.Required, len(d.Votes))
	}
	if d.NeedsCode {
		text += "这类操作需要在网页上用一次性码 + passkey 批准，不能在这里批准\n"
	}
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
