// Package billing 负责流量计费：聚合节点上报、扣减用户流量、周期重置，以及到期和超额检测。
package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/metrics"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

type Processor struct {
	st    *store.Store
	ev    *events.Recorder
	loc   *time.Location
	log   *slog.Logger
	batch int32
	now   func() time.Time
}

func NewProcessor(st *store.Store, ev *events.Recorder, loc *time.Location, log *slog.Logger) *Processor {
	return &Processor{st: st, ev: ev, loc: loc, log: log, batch: 500, now: time.Now}
}

type usage struct{ up, down int64 }

type dayKey struct {
	id  int64
	day time.Time
}

// ProcessReports 循环处理未入账的流量上报，直到处理完。返回处理的上报条数。
//
// 每批在一个事务里完成：领取（FOR UPDATE SKIP LOCKED）、扣减、写日统计、标记已处理，
// 多实例并发运行也不会重复入账。
func (p *Processor) ProcessReports(ctx context.Context) (int, error) {
	total := 0
	for {
		n, pending, err := p.processBatch(ctx)
		if err != nil {
			return total, err
		}
		total += n
		metrics.TrafficProcessed.Add(float64(n))
		for _, e := range pending {
			p.ev.Emit(ctx, nil, e)
		}
		if n < int(p.batch) {
			return total, nil
		}
	}
}

func (p *Processor) processBatch(ctx context.Context) (int, []events.Event, error) {
	var count int
	var pending []events.Event
	err := p.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		reports, err := q.ClaimTrafficReports(ctx, p.batch)
		if err != nil {
			return err
		}
		count = len(reports)
		if count == 0 {
			return nil
		}
		perUser := map[int64]*usage{}
		perUserDay := map[dayKey]*usage{}
		perNodeDay := map[dayKey]*usage{}
		ids := make([]int64, 0, len(reports))
		for _, r := range reports {
			ids = append(ids, r.ID)
			var data map[string][2]float64
			if err := json.Unmarshal(r.Payload, &data); err != nil {
				p.log.Warn("skip malformed traffic report", "report", r.ID, "err", err)
				continue
			}
			day := dayOf(r.ReceivedAt, p.loc)
			for uidStr, v := range data {
				uid, err := strconv.ParseInt(uidStr, 10, 64)
				if err != nil {
					continue
				}
				up := int64(math.Round(v[0] * r.Rate))
				down := int64(math.Round(v[1] * r.Rate))
				if up == 0 && down == 0 {
					continue
				}
				add(perUser, uid, up, down)
				addKey(perUserDay, dayKey{uid, day}, up, down)
				addKey(perNodeDay, dayKey{r.NodeID, day}, up, down)
			}
		}
		now := p.now()
		for _, uid := range sortedIDs(perUser) {
			u := perUser[uid]
			row, err := q.AddUserTraffic(ctx, db.AddUserTrafficParams{ID: uid, Up: u.up, Down: u.down})
			if store.IsNotFound(err) {
				continue // 用户已删除
			}
			if err != nil {
				return err
			}
			if row.UsedUp+row.UsedDown >= row.TransferBytes && row.OverQuotaAt == nil {
				if err := q.SetUserOverQuota(ctx, db.SetUserOverQuotaParams{ID: uid, OverQuotaAt: &now}); err != nil {
					return err
				}
				pending = append(pending, events.Event{
					Kind: events.UserOverQuota, Severity: "info", Subject: fmt.Sprintf("user:%d", uid),
					Message:  fmt.Sprintf("%s 流量已用完，已自动停用到下次重置", row.Email),
					DedupKey: fmt.Sprintf("%s:%d", events.UserOverQuota, uid),
				})
			}
		}
		for k, u := range perUserDay {
			if err := q.AddUserDailyTraffic(ctx, db.AddUserDailyTrafficParams{UserID: k.id, Day: k.day, Up: u.up, Down: u.down}); err != nil {
				return err
			}
		}
		for k, u := range perNodeDay {
			if err := q.AddNodeDailyTraffic(ctx, db.AddNodeDailyTrafficParams{NodeID: k.id, Day: k.day, Up: u.up, Down: u.down}); err != nil {
				return err
			}
		}
		return q.MarkTrafficReportsProcessed(ctx, ids)
	})
	return count, pending, err
}

// ResetDue 重置到期的用户流量，并排好下一次重置时间。
func (p *Processor) ResetDue(ctx context.Context) (int, error) {
	ids, err := p.st.DueTrafficResets(ctx, 500)
	if err != nil {
		return 0, err
	}
	now := p.now()
	for _, id := range ids {
		err := p.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
			u, err := q.GetUser(ctx, id)
			if err != nil {
				return err
			}
			var next *time.Time
			if u.PlanID != nil {
				if pl, err := q.GetPlan(ctx, *u.PlanID); err == nil {
					next = NextReset(pl.ResetMethod, u.ResetAnchor, now, p.loc)
				}
			}
			return q.ResetUserTraffic(ctx, db.ResetUserTrafficParams{ID: id, NextResetAt: next})
		})
		if err != nil {
			return 0, err
		}
		p.ev.Resolve(ctx, nil, fmt.Sprintf("%s:%d", events.UserOverQuota, id))
	}
	return len(ids), nil
}

const expiryCursorKey = "billing.expiry_cursor"

// CheckExpired 为上次检查以来到期的用户记录事件。停用本身是自动的：到期用户不再出现在节点用户列表里。
func (p *Processor) CheckExpired(ctx context.Context) error {
	now := p.now()
	since := now.Add(-time.Hour)
	if raw, err := p.st.GetSetting(ctx, expiryCursorKey); err == nil {
		var t time.Time
		if json.Unmarshal(raw, &t) == nil {
			since = t
		}
	}
	users, err := p.st.UsersExpiredSince(ctx, since)
	if err != nil {
		return err
	}
	for _, u := range users {
		p.ev.Emit(ctx, nil, events.Event{
			Kind: events.UserExpired, Subject: fmt.Sprintf("user:%d", u.ID),
			Message:  fmt.Sprintf("%s 已到期，订阅已停用", u.Email),
			DedupKey: fmt.Sprintf("%s:%d:%d", events.UserExpired, u.ID, u.ExpiresAt.Unix()),
		})
	}
	b, _ := json.Marshal(now)
	return p.st.SetSetting(ctx, db.SetSettingParams{Key: expiryCursorKey, Value: b})
}

// CheckNodesOffline 把心跳超时的节点标记为离线并发出告警。
func (p *Processor) CheckNodesOffline(ctx context.Context, after time.Duration) error {
	nodes, err := p.st.MarkStaleNodesOffline(ctx, ptr(p.now().Add(-after)))
	if err != nil {
		return err
	}
	for _, n := range nodes {
		users, _ := p.st.CountUsersOfNode(ctx, n.ID)
		last := ""
		if n.LastHeartbeatAt != nil {
			last = n.LastHeartbeatAt.In(p.loc).Format("15:04")
		}
		p.ev.Emit(ctx, nil, events.Event{
			Kind: events.NodeOffline, Severity: "critical", Subject: fmt.Sprintf("node:%d", n.ID),
			Message:  fmt.Sprintf("%s 已离线，最后心跳 %s，%d 名用户的订阅里包含这个节点", n.Name, last, users),
			Payload:  map[string]any{"node_id": n.ID, "code": n.Code, "users": users},
			DedupKey: fmt.Sprintf("%s:%d", events.NodeOffline, n.ID),
		})
	}
	return nil
}

// Cleanup 清理过期的中间数据。
func (p *Processor) Cleanup(ctx context.Context, deviceTTL time.Duration) error {
	now := p.now()
	steps := []func() error{
		func() error { return p.st.DeleteProcessedTrafficReports(ctx, ptr(now.AddDate(0, 0, -7))) },
		func() error { return p.st.DeleteStaleAlive(ctx, now.Add(-3*deviceTTL)) },
		func() error { return p.st.DeleteExpiredSessions(ctx) },
		func() error { return p.st.DeleteExpiredChallenges(ctx) },
		func() error { return p.st.DeleteOldIdempotencyKeys(ctx, now.Add(-24*time.Hour)) },
		func() error { return p.st.ExpireChangePlans(ctx) },
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

func dayOf(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

func add(m map[int64]*usage, id, up, down int64) {
	u := m[id]
	if u == nil {
		u = &usage{}
		m[id] = u
	}
	u.up += up
	u.down += down
}

func addKey(m map[dayKey]*usage, k dayKey, up, down int64) {
	u := m[k]
	if u == nil {
		u = &usage{}
		m[k] = u
	}
	u.up += up
	u.down += down
}

// sortedIDs 让同一批里的更新按用户 ID 顺序加锁，避免并发批次互相死锁。
func sortedIDs(m map[int64]*usage) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func ptr[T any](v T) *T { return &v }
