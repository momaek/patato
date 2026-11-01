package billing

import (
	"context"
	"fmt"

	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store/db"
)

// CheckExpiring 为 days 天内到期的用户各发一次「即将到期」事件，可以配合自动处置规则发提醒邮件。
func (p *Processor) CheckExpiring(ctx context.Context, days int) error {
	if days <= 0 {
		return nil
	}
	now := p.now()
	users, err := p.st.UsersExpiringBetween(ctx, db.UsersExpiringBetweenParams{FromAt: now, ToAt: now.AddDate(0, 0, days)})
	if err != nil {
		return err
	}
	for _, u := range users {
		left := int(u.ExpiresAt.Sub(now).Hours()/24) + 1
		p.ev.Emit(ctx, nil, events.Event{
			Kind: events.UserExpiring, Subject: fmt.Sprintf("user:%d", u.ID),
			Message:  fmt.Sprintf("%s 的订阅将在 %s 到期（约 %d 天后）", u.Email, u.ExpiresAt.In(p.loc).Format("2006-01-02 15:04"), left),
			Payload:  map[string]any{"user_id": u.ID, "expires_at": u.ExpiresAt, "days_left": left},
			DedupKey: fmt.Sprintf("%s:%d:%d", events.UserExpiring, u.ID, u.ExpiresAt.Unix()),
		})
	}
	return nil
}

// CheckAnomalies 检查昨天的用量：超过前 7 天日均 factor 倍、且不少于 minBytes 的用户发出流量异常告警，
// 常见于账号被共享或订阅链接泄露。
func (p *Processor) CheckAnomalies(ctx context.Context, factor int, minBytes int64) error {
	if factor <= 0 {
		return nil
	}
	day := dayOf(p.now(), p.loc).AddDate(0, 0, -1)
	rows, err := p.st.UserTrafficAnomalies(ctx, db.UserTrafficAnomaliesParams{Day: day, Factor: int64(factor), MinBytes: minBytes})
	if err != nil {
		return err
	}
	for _, r := range rows {
		base := "前 7 天几乎没有用量"
		if r.AvgBytes > 0 {
			base = fmt.Sprintf("是前 7 天日均 %.1f GB 的 %.0f 倍", ToGB(r.AvgBytes), float64(r.Bytes)/float64(r.AvgBytes))
		}
		p.ev.Emit(ctx, nil, events.Event{
			Kind: events.TrafficAnomaly, Severity: "warning", Subject: fmt.Sprintf("user:%d", r.UserID),
			Message:  fmt.Sprintf("%s 在 %s 用了 %.1f GB，%s，可能是账号共享或订阅链接泄露", r.Email, day.Format("01-02"), ToGB(r.Bytes), base),
			Payload:  map[string]any{"user_id": r.UserID, "day": day.Format("2006-01-02"), "bytes": r.Bytes, "avg_bytes": r.AvgBytes},
			DedupKey: fmt.Sprintf("%s:%d:%s", events.TrafficAnomaly, r.UserID, day.Format("20060102")),
		})
	}
	return nil
}
