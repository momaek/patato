package commerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// RulesKey 是返佣规则在 settings 表里的键。修改规则是敏感操作（referral.settings）。
const RulesKey = "referral.rules"

// Rules 是邀请返佣规则。
type Rules struct {
	Enabled bool `json:"enabled"`
	// RatePercent 是返佣比例，按被邀请人实付金额计算。
	RatePercent int `json:"rate_percent"`
	// WaitDays 天内没有退款才入账。
	WaitDays int `json:"wait_days"`
	// FirstOrderOnly 为 true 时只对被邀请人的首单返佣。
	FirstOrderOnly bool `json:"first_order_only"`
	// SubnetLimit：同一邀请人 24 小时内邀请的同一网段（IPv4 /24、IPv6 /48）账号超过这个数，返佣自动冻结。0 表示不检查。
	SubnetLimit int `json:"subnet_limit"`
}

func DefaultRules() Rules { return Rules{RatePercent: 10, WaitDays: 7, SubnetLimit: 5} }

func (r Rules) Validate() error {
	if r.RatePercent < 0 || r.RatePercent > 100 {
		return apierr.BadRequest("invalid_rate", "返佣比例应在 0–100 之间")
	}
	if r.WaitDays < 0 || r.WaitDays > 365 {
		return apierr.BadRequest("invalid_wait_days", "等待天数应在 0–365 之间")
	}
	if r.SubnetLimit < 0 {
		return apierr.BadRequest("invalid_subnet_limit", "同网段阈值不能为负数")
	}
	return nil
}

// LoadRules 读取返佣规则，没有设置过时返回默认值（未启用）。
func LoadRules(ctx context.Context, q *db.Queries) (Rules, error) {
	r := DefaultRules()
	raw, err := q.GetSetting(ctx, RulesKey)
	if store.IsNotFound(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}

// Subnet 返回 IP 所在网段的前缀，用于同网段统计：IPv4 取前三段，IPv6 取前三组。
func Subnet(ip string) string {
	p := net.ParseIP(strings.TrimSpace(ip))
	if p == nil {
		return ""
	}
	if v4 := p.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.", v4[0], v4[1], v4[2])
	}
	parts := strings.Split(p.String(), ":")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:3], ":") + ":"
}

// accrueCommission 在被邀请人付款后生成返佣：进入待入账；命中风控规则时冻结并发出事件。
func accrueCommission(ctx context.Context, q *db.Queries, o db.Order, invitee db.User, now time.Time) (*db.Commission, *events.Event, error) {
	if invitee.InvitedBy == nil || o.Total <= 0 {
		return nil, nil, nil
	}
	rules, err := LoadRules(ctx, q)
	if err != nil || !rules.Enabled || rules.RatePercent == 0 {
		return nil, nil, err
	}
	if rules.FirstOrderOnly {
		n, err := q.CountPaidOrdersOfUser(ctx, invitee.ID)
		if err != nil {
			return nil, nil, err
		}
		if n > 1 {
			return nil, nil, nil
		}
	}
	amount := o.Total * int64(rules.RatePercent) / 100
	if amount <= 0 {
		return nil, nil, nil
	}
	status, reason := "pending", ""
	if prefix := Subnet(invitee.RegisterIp); rules.SubnetLimit > 0 && prefix != "" {
		n, err := q.CountRecentInviteesInSubnet(ctx, db.CountRecentInviteesInSubnetParams{InviterID: *invitee.InvitedBy, Since: now.Add(-24 * time.Hour), Prefix: prefix})
		if err != nil {
			return nil, nil, err
		}
		if n > int64(rules.SubnetLimit) {
			status = "frozen"
			reason = fmt.Sprintf("24 小时内邀请了 %d 个同网段（%s*）账号，超过 %d 个", n, prefix, rules.SubnetLimit)
		}
	}
	c, err := q.CreateCommission(ctx, db.CreateCommissionParams{
		InviterID: *invitee.InvitedBy, InviteeID: &invitee.ID, OrderID: o.ID, Amount: amount, Status: status, Reason: reason,
		ReleaseAt: now.AddDate(0, 0, rules.WaitDays),
	})
	if store.IsNotFound(err) {
		return nil, nil, nil // 这笔订单已经有返佣
	}
	if err != nil {
		return nil, nil, err
	}
	if status != "frozen" {
		return &c, nil, nil
	}
	inviter, _ := q.GetUser(ctx, *invitee.InvitedBy)
	return &c, &events.Event{
		Kind: events.ReferralFrozen, Severity: "warning", Subject: fmt.Sprintf("user:%d", c.InviterID),
		Message:  fmt.Sprintf("%s 的返佣 %s 元已自动冻结：%s", inviter.Email, Yuan(amount), reason),
		Payload:  map[string]any{"commission_id": c.ID, "inviter_id": c.InviterID, "user_id": c.InviterID, "amount": amount},
		DedupKey: fmt.Sprintf("%s:%d", events.ReferralFrozen, c.InviterID),
	}, nil
}

// EnsureInviteCode 给用户分配邀请码（第一次查看邀请页时生成）。
func EnsureInviteCode(ctx context.Context, q *db.Queries, u db.User) (string, error) {
	if u.InviteCode != nil {
		return *u.InviteCode, nil
	}
	for i := 0; i < 5; i++ {
		code := strings.ToLower(secret.HexToken(4))
		err := q.SetUserInviteCode(ctx, db.SetUserInviteCodeParams{ID: u.ID, InviteCode: &code})
		if store.IsUniqueViolation(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		fresh, err := q.GetUser(ctx, u.ID)
		if err != nil || fresh.InviteCode == nil {
			return "", err
		}
		return *fresh.InviteCode, nil
	}
	return "", fmt.Errorf("allocate invite code")
}
