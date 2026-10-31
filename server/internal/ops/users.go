package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/password"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- user.create -------------------------------------------------------

type UserCreate struct{ d *Deps }

func (*UserCreate) Kind() string      { return "user.create" }
func (*UserCreate) Risk() change.Risk { return change.Normal }

type userCreateParams struct {
	Email          string     `json:"email"`
	Password       string     `json:"password"`
	Plan           Ref        `json:"plan"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Days           int        `json:"days"`
	TransferGB     *float64   `json:"transfer_gb"`
	DeviceLimit    *int       `json:"device_limit"`
	SpeedLimitMbps *int       `json:"speed_limit_mbps"`
}

// 预演阶段把密码换成哈希，明文不进入变更计划。
type userCreateResolved struct {
	Email          string     `json:"email"`
	PasswordHash   string     `json:"password_hash"`
	PlanID         int64      `json:"plan_id"`
	ExpiresAt      *time.Time `json:"expires_at"`
	TransferBytes  int64      `json:"transfer_bytes"`
	DeviceLimit    *int32     `json:"device_limit"`
	SpeedLimitMbps *int32     `json:"speed_limit_mbps"`
}

func (o *UserCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[userCreateParams](raw)
	if err != nil {
		return nil, err
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(p.Email))
	if err != nil {
		return nil, apierr.BadRequest("invalid_email", "邮箱 %q 格式不对", p.Email)
	}
	if _, err := q.GetUserByEmail(ctx, addr.Address); err == nil {
		return nil, apierr.Conflict("email_taken", "邮箱 %s 已注册", addr.Address)
	}
	pl, err := findPlan(ctx, q, p.Plan)
	if err != nil {
		return nil, err
	}
	res := userCreateResolved{Email: addr.Address, PlanID: pl.ID, TransferBytes: pl.TransferBytes}
	if p.Password != "" {
		if err := password.Validate(p.Password); err != nil {
			return nil, apierr.BadRequest("weak_password", "%v", err)
		}
		res.PasswordHash = password.Hash(p.Password)
	}
	switch {
	case p.ExpiresAt != nil:
		t := p.ExpiresAt.UTC()
		res.ExpiresAt = &t
	case p.Days > 0:
		t := o.d.now().AddDate(0, 0, p.Days).UTC()
		res.ExpiresAt = &t
	}
	if p.TransferGB != nil {
		res.TransferBytes = billing.GB(*p.TransferGB)
	}
	res.DeviceLimit = int32Ptr(p.DeviceLimit)
	res.SpeedLimitMbps = int32Ptr(p.SpeedLimitMbps)
	expiry := "长期有效"
	if res.ExpiresAt != nil {
		expiry = res.ExpiresAt.In(o.d.Loc).Format("2006-01-02") + " 到期"
	}
	steps := []string{fmt.Sprintf("新建用户 %s，套餐 %s，%s，流量 %s", res.Email, pl.Name, expiry, gb(res.TransferBytes))}
	if res.PasswordHash == "" {
		steps = append(steps, "未设置密码，用户需要用「找回密码」设置后登录用户侧")
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("新建用户 %s", res.Email),
		Steps:    steps,
		Diff:     []change.DiffEntry{{Field: "用户", Before: nil, After: map[string]any{"email": res.Email, "plan": pl.Code, "expires_at": res.ExpiresAt}}},
		Impact:   change.Impact{Users: 1},
		Targets:  []change.Target{planTarget(pl)},
		Resolved: res,
	}, nil
}

func (o *UserCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[userCreateResolved](raw)
	if err != nil {
		return nil, err
	}
	pl, err := q.GetPlan(ctx, r.PlanID)
	if err != nil {
		return nil, err
	}
	now := o.d.now()
	planID := r.PlanID
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		Email: r.Email, PasswordHash: r.PasswordHash, PasswordAlgo: password.Argon2id,
		Uuid: secret.UUID(), SubToken: secret.HexToken(16), PlanID: &planID, TransferBytes: r.TransferBytes,
		ExpiresAt: r.ExpiresAt, DeviceLimit: r.DeviceLimit, SpeedLimitMbps: r.SpeedLimitMbps,
		ResetAnchor: &now, NextResetAt: billing.NextReset(pl.ResetMethod, &now, now, o.d.Loc),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("email_taken", "邮箱 %s 已注册", r.Email)
		}
		return nil, err
	}
	return map[string]any{"user_id": u.ID, "email": u.Email}, nil
}

// ---- user.update -------------------------------------------------------

type UserUpdate struct{ d *Deps }

func (*UserUpdate) Kind() string      { return "user.update" }
func (*UserUpdate) Risk() change.Risk { return change.Normal }

type userUpdateParams struct {
	User           Ref        `json:"user"`
	Plan           *Ref       `json:"plan"`
	ExpiresAt      *time.Time `json:"expires_at"`
	NeverExpire    bool       `json:"never_expire"`
	ExtendDays     int        `json:"extend_days"`
	TransferGB     *float64   `json:"transfer_gb"`
	DeviceLimit    *int       `json:"device_limit"`
	SpeedLimitMbps *int       `json:"speed_limit_mbps"`
	Banned         *bool      `json:"banned"`
}

type userState struct {
	PlanID         *int64     `json:"plan_id"`
	PlanCode       string     `json:"plan"`
	ExpiresAt      *time.Time `json:"expires_at"`
	TransferBytes  int64      `json:"transfer_bytes"`
	DeviceLimit    *int32     `json:"device_limit"`
	SpeedLimitMbps *int32     `json:"speed_limit_mbps"`
	Banned         bool       `json:"banned"`
	ResetAnchor    *time.Time `json:"-"`
	NextResetAt    *time.Time `json:"next_reset_at"`
}

// next 计算修改后的用户状态。续期从当前到期日接着算，已过期则从现在算。
func (o *UserUpdate) next(ctx context.Context, q *db.Queries, p *userUpdateParams, u db.User) (userState, userState, error) {
	cur := userState{PlanID: u.PlanID, ExpiresAt: u.ExpiresAt, TransferBytes: u.TransferBytes, DeviceLimit: u.DeviceLimit,
		SpeedLimitMbps: u.SpeedLimitMbps, Banned: u.Banned, ResetAnchor: u.ResetAnchor, NextResetAt: u.NextResetAt}
	if u.PlanID != nil {
		if pl, err := q.GetPlan(ctx, *u.PlanID); err == nil {
			cur.PlanCode = pl.Code
		}
	}
	nxt := cur
	now := o.d.now()
	if p.Plan != nil {
		pl, err := findPlan(ctx, q, *p.Plan)
		if err != nil {
			return cur, nxt, err
		}
		if cur.PlanID == nil || *cur.PlanID != pl.ID {
			id := pl.ID
			nxt.PlanID, nxt.PlanCode = &id, pl.Code
			nxt.TransferBytes = pl.TransferBytes
			nxt.ResetAnchor = &now
			nxt.NextResetAt = billing.NextReset(pl.ResetMethod, &now, now, o.d.Loc)
		}
	}
	switch {
	case p.NeverExpire:
		nxt.ExpiresAt = nil
	case p.ExpiresAt != nil:
		t := p.ExpiresAt.UTC()
		nxt.ExpiresAt = &t
	case p.ExtendDays > 0:
		base := now
		if cur.ExpiresAt != nil && cur.ExpiresAt.After(now) {
			base = *cur.ExpiresAt
		}
		t := base.AddDate(0, 0, p.ExtendDays).UTC()
		nxt.ExpiresAt = &t
	case p.ExtendDays < 0:
		return cur, nxt, apierr.BadRequest("invalid_days", "extend_days 不能为负数")
	}
	if p.TransferGB != nil {
		nxt.TransferBytes = billing.GB(*p.TransferGB)
	}
	if p.DeviceLimit != nil {
		nxt.DeviceLimit = int32Ptr(p.DeviceLimit)
	}
	if p.SpeedLimitMbps != nil {
		nxt.SpeedLimitMbps = int32Ptr(p.SpeedLimitMbps)
	}
	if p.Banned != nil {
		nxt.Banned = *p.Banned
	}
	return cur, nxt, nil
}

func (o *UserUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[userUpdateParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	cur, nxt, err := o.next(ctx, q, p, u)
	if err != nil {
		return nil, err
	}
	diff := fieldDiff(cur, nxt)
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	steps := []string{fmt.Sprintf("更新用户 %s 的 %d 项设置", u.Email, len(diff))}
	if p.ExtendDays > 0 && nxt.ExpiresAt != nil {
		steps = append(steps, fmt.Sprintf("续期 %d 天，到期时间变为 %s", p.ExtendDays, nxt.ExpiresAt.In(o.d.Loc).Format("2006-01-02")))
	}
	return &change.Draft{
		Summary: fmt.Sprintf("修改用户 %s", u.Email),
		Steps:   steps,
		Diff:    diff,
		Impact:  change.Impact{Users: 1},
		Targets: []change.Target{userTarget(u)},
		Resolved: map[string]any{
			"user": fmt.Sprint(u.ID), "state": nxt, "reset_anchor": nxt.ResetAnchor,
		},
	}, nil
}

func (o *UserUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	var r struct {
		User        Ref        `json:"user"`
		State       userState  `json:"state"`
		ResetAnchor *time.Time `json:"reset_anchor"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, r.User)
	if err != nil {
		return nil, err
	}
	s := r.State
	updated, err := q.UpdateUserAdmin(ctx, db.UpdateUserAdminParams{
		ID: u.ID, PlanID: s.PlanID, TransferBytes: s.TransferBytes, ExpiresAt: s.ExpiresAt, DeviceLimit: s.DeviceLimit,
		SpeedLimitMbps: s.SpeedLimitMbps, Banned: s.Banned, ResetAnchor: r.ResetAnchor, NextResetAt: s.NextResetAt,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"user_id": updated.ID, "expires_at": updated.ExpiresAt, "version": updated.Version}, nil
}

// ---- user.reset_traffic / user.reset_token / user.delete ---------------

type userRefParams struct {
	User Ref `json:"user"`
}

type UserResetTraffic struct{ d *Deps }

func (*UserResetTraffic) Kind() string      { return "user.reset_traffic" }
func (*UserResetTraffic) Risk() change.Risk { return change.Normal }

func (o *UserResetTraffic) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[userRefParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("重置用户 %s 的流量", u.Email),
		Steps:   []string{fmt.Sprintf("已用流量从 %s 清零，额度 %s 不变", gb(u.UsedUp+u.UsedDown), gb(u.TransferBytes))},
		Diff:    []change.DiffEntry{{Field: "used_bytes", Before: u.UsedUp + u.UsedDown, After: 0}},
		Impact:  change.Impact{Users: 1},
		Targets: []change.Target{userTarget(u)},
	}, nil
}

func (o *UserResetTraffic) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[userRefParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	var next *time.Time
	if u.PlanID != nil {
		if pl, err := q.GetPlan(ctx, *u.PlanID); err == nil {
			next = billing.NextReset(pl.ResetMethod, u.ResetAnchor, o.d.now(), o.d.Loc)
		}
	}
	if err := q.ResetUserTraffic(ctx, db.ResetUserTrafficParams{ID: u.ID, NextResetAt: next}); err != nil {
		return nil, err
	}
	return map[string]any{"user_id": u.ID, "next_reset_at": next}, nil
}

type UserResetToken struct{ d *Deps }

func (*UserResetToken) Kind() string      { return "user.reset_token" }
func (*UserResetToken) Risk() change.Risk { return change.Normal }

func (o *UserResetToken) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[userRefParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("重置用户 %s 的订阅", u.Email),
		Steps: []string{
			"生成新的订阅 token 和连接凭证（UUID），旧订阅链接立即失效",
			"用户的所有设备需要用新链接重新导入",
		},
		Impact:  change.Impact{Users: 1},
		Targets: []change.Target{userTarget(u)},
	}, nil
}

func (o *UserResetToken) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[userRefParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	if err := q.ResetUserSubscription(ctx, db.ResetUserSubscriptionParams{ID: u.ID, SubToken: secret.HexToken(16), Uuid: secret.UUID()}); err != nil {
		return nil, err
	}
	return map[string]any{"user_id": u.ID}, nil
}

type UserDelete struct{ d *Deps }

func (*UserDelete) Kind() string      { return "user.delete" }
func (*UserDelete) Risk() change.Risk { return change.Sensitive }

func (o *UserDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[userRefParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除用户 %s", u.Email),
		Steps:   []string{fmt.Sprintf("删除用户 %s 及其订阅，不可恢复", u.Email)},
		Diff:    []change.DiffEntry{{Field: "用户", Before: u.Email, After: nil}},
		Impact:  change.Impact{Users: 1},
		Targets: []change.Target{userTarget(u)},
	}, nil
}

func (o *UserDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[userRefParams](raw)
	if err != nil {
		return nil, err
	}
	u, err := findUser(ctx, q, p.User)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteSubjectSessions(ctx, db.DeleteSubjectSessionsParams{Kind: "user", SubjectID: u.ID}); err != nil {
		return nil, err
	}
	if err := q.DeleteUser(ctx, u.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": u.Email}, nil
}

func int32Ptr(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v)
	return &n
}
