package relay

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// 保留期：线路流量小时表与探测小时表保留 30 天，天表长期保留。
const (
	HourlyRetention = 30 * 24 * time.Hour
	// IdleAfter 是 route_idle 的判定窗口：线路第一跳连续这么久没有流量、但落地节点有流量。
	IdleAfter = 6 * time.Hour
)

// DirectoryMachine 是 tolato 里的一台机器。
type DirectoryMachine struct {
	Names  []string // 名称、别名、IP
	IP     string
	Online bool
}

// Directory 是 tolato 的只读机器列表；未配置 tolato 时为 nil。
type Directory interface {
	Machines(ctx context.Context) ([]DirectoryMachine, error)
}

// EventSink 记录和解决事件，由 events.Recorder 实现。
type EventSink interface {
	Emit(ctx context.Context, q *db.Queries, e events.Event)
	Resolve(ctx context.Context, q *db.Queries, dedupKey string)
}

// Monitor 跑中转线路的周期任务：每跳在线状态、流量汇总与清理、长时间无流量检测、tolato IP 刷新。
type Monitor struct {
	st           *store.Store
	ev           EventSink
	dir          Directory
	loc          *time.Location
	offlineAfter time.Duration
	log          *slog.Logger
	now          func() time.Time
}

func NewMonitor(st *store.Store, ev EventSink, dir Directory, loc *time.Location, offlineAfter time.Duration, log *slog.Logger) *Monitor {
	if loc == nil {
		loc = time.UTC
	}
	return &Monitor{st: st, ev: ev, dir: dir, loc: loc, offlineAfter: offlineAfter, log: log, now: time.Now}
}

// SetNow 替换时钟，测试用。
func (m *Monitor) SetNow(f func() time.Time) { m.now = f }

// HopState 判断一台中转机器的在线状态：接入了 patato-node 的看它最近一次同步或上报，
// 没接入的看 tolato；都不知道时为 unknown，不因此撤下入口。
func HopState(m db.Machine, now time.Time, offlineAfter time.Duration) string {
	if m.TokenHash != nil {
		if m.LastSeenAt != nil && now.Sub(*m.LastSeenAt) <= offlineAfter {
			return "online"
		}
		return "offline"
	}
	if m.TolatoOnline != nil {
		if *m.TolatoOnline {
			return "online"
		}
		return "offline"
	}
	return "unknown"
}

// CheckHops 刷新线路里每台机器的在线状态。状态变化时按机器聚合发一条事件，列出受影响的入口数和用户数。
func (m *Monitor) CheckHops(ctx context.Context) error {
	if m.dir != nil {
		if err := m.refreshDirectory(ctx); err != nil {
			m.log.Warn("refresh tolato machines", "err", err)
		}
	}
	machines, err := m.st.ListRelayMachines(ctx)
	if err != nil {
		return err
	}
	now := m.now()
	for _, mc := range machines {
		state := HopState(mc, now, m.offlineAfter)
		prev, err := m.st.SetMachineState(ctx, db.SetMachineStateParams{ID: mc.ID, State: state})
		if store.IsNotFound(err) {
			continue // 没有变化
		}
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s:%d", events.RouteHopOffline, mc.ID)
		switch {
		case state == "offline":
			entries, users, grouped, err := m.affected(ctx, mc.ID)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("中转机器 %s 掉线，影响 %d 个入口、%d 名用户；这些入口已从订阅中撤下", mc.Host, entries, users)
			if grouped > 0 {
				msg = fmt.Sprintf("中转机器 %s 掉线，经过它的 %d 个入口、%d 名用户受影响；其中 %d 个在入口组里，入口域名指向它时才撤下，"+
					"把域名解析切到入口组里的其他机器即可恢复", mc.Host, entries, users, grouped)
			}
			m.ev.Emit(ctx, nil, events.Event{
				Kind: events.RouteHopOffline, Severity: "critical", Subject: fmt.Sprintf("machine:%d", mc.ID),
				Message:  msg,
				Payload:  map[string]any{"machine": mc.Host, "machine_id": mc.ID, "entries": entries, "users": users, "entry_group_entries": grouped},
				DedupKey: key,
			})
		case state == "online" && prev == "offline":
			m.ev.Resolve(ctx, nil, key)
			m.ev.Emit(ctx, nil, events.Event{Kind: events.RouteHopOnline, Subject: fmt.Sprintf("machine:%d", mc.ID),
				Message: fmt.Sprintf("中转机器 %s 已恢复在线，经过它的入口重新下发", mc.Host)})
		}
	}
	return nil
}

// affected 统计经过一台机器的启用入口数和这些入口所在节点的用户数；grouped 是其中这台机器在入口组里承担第一跳的入口数
// （入口组的其他成员还能接住，入口不一定撤下）。
func (m *Monitor) affected(ctx context.Context, machineID int64) (int, int64, int, error) {
	fs, err := m.st.ListMachineForwards(ctx, machineID)
	if err != nil {
		return 0, 0, 0, err
	}
	ids := map[int64]bool{}
	firstHop := map[int64]bool{}
	for _, f := range fs {
		ids[f.EntryID] = true
		if f.Hop == 0 {
			firstHop[f.EntryID] = true
		}
	}
	var enabled []int64
	grouped := 0
	for id := range ids {
		e, err := m.st.GetEntry(ctx, id)
		if err != nil || !e.Enabled {
			continue
		}
		enabled = append(enabled, id)
		if firstHop[id] && e.RouteID != nil {
			if r, err := m.st.GetRoute(ctx, *e.RouteID); err == nil && len(r.EntryPeers) > 0 {
				grouped++
			}
		}
	}
	if len(enabled) == 0 {
		return 0, 0, 0, nil
	}
	users, err := m.st.CountUsersOfEntries(ctx, enabled)
	return len(enabled), users, grouped, err
}

// refreshDirectory 从 tolato 刷新机器的 IP 和在线状态；IP 变化时重算以它为下一跳的转发（动态 IP 落地）。
func (m *Monitor) refreshDirectory(ctx context.Context) error {
	list, err := m.dir.Machines(ctx)
	if err != nil {
		return err
	}
	machines, err := m.st.ListMachines(ctx)
	if err != nil {
		return err
	}
	for _, mc := range machines {
		var hit *DirectoryMachine
		for i := range list {
			for _, n := range list[i].Names {
				if strings.EqualFold(n, mc.Host) {
					hit = &list[i]
				}
			}
		}
		if hit == nil {
			continue
		}
		online := hit.Online
		if hit.IP == mc.TolatoIp && mc.TolatoOnline != nil && *mc.TolatoOnline == online {
			continue
		}
		if err := m.st.SetMachineTolato(ctx, db.SetMachineTolatoParams{ID: mc.ID, TolatoIp: hit.IP, TolatoOnline: &online}); err != nil {
			return err
		}
		if hit.IP != mc.TolatoIp && mc.PublicAddress == "" {
			n, err := RefreshForwardTargets(ctx, m.st.Queries, mc.ID)
			if err != nil {
				return err
			}
			if n > 0 {
				m.log.Info("machine address changed, forwards re-targeted", "machine", mc.Host, "from", mc.TolatoIp, "to", hit.IP, "forwards", n)
			}
		}
	}
	return nil
}

// Rollup 把今天和昨天（按配置的时区）的小时流量重新汇总进天表，可重复执行。
func (m *Monitor) Rollup(ctx context.Context) error {
	now := m.now().In(m.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.loc)
	for _, start := range []time.Time{today.AddDate(0, 0, -1), today} {
		day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		if _, err := m.st.RollupForwardTrafficDaily(ctx, db.RollupForwardTrafficDailyParams{
			Day: day, FromTs: start, ToTs: start.AddDate(0, 0, 1),
		}); err != nil {
			return err
		}
	}
	return nil
}

// Cleanup 删除超过保留期的小时数据。先汇总再删，避免删掉还没进天表的小时。
func (m *Monitor) Cleanup(ctx context.Context) error {
	if err := m.Rollup(ctx); err != nil {
		return err
	}
	before := m.now().Add(-HourlyRetention)
	if err := m.st.DeleteForwardTrafficHourlyBefore(ctx, before); err != nil {
		return err
	}
	return m.st.DeleteForwardProbeHourlyBefore(ctx, before)
}

// CheckIdle 发现长时间没有流量的线路入口：第一跳连续 IdleAfter 没有流量，但落地节点有流量，
// 可能是旁路或计数异常。线路恢复流量后事件自动解决。
func (m *Monitor) CheckIdle(ctx context.Context) error {
	since := m.now().Add(-IdleAfter)
	rows, err := m.st.RouteZeroTrafficHours(ctx, since)
	if err != nil {
		return err
	}
	for _, r := range rows {
		key := fmt.Sprintf("%s:%d", events.RouteIdle, r.EntryID)
		if r.RelayBytes > 0 {
			m.ev.Resolve(ctx, nil, key)
			continue
		}
		if r.NodeBytes == 0 {
			continue
		}
		fs, err := m.st.ListEntryForwards(ctx, r.EntryID)
		if err != nil {
			return err
		}
		active := len(fs) > 0
		for _, f := range fs {
			if f.Mode != "manual" && f.Status != "active" {
				active = false
			}
		}
		if !active {
			continue
		}
		route, err := m.st.GetRoute(ctx, r.RouteID)
		if err != nil {
			continue
		}
		m.ev.Emit(ctx, nil, events.Event{
			Kind: events.RouteIdle, Severity: "warning", Subject: fmt.Sprintf("entry:%d", r.EntryID),
			Message: fmt.Sprintf("线路 %s 上的入口 %d 已经 %d 小时没有流量，但落地节点有流量：可能没人用这个入口，或计数异常",
				route.Name, r.EntryID, int(IdleAfter.Hours())),
			Payload:  map[string]any{"entry_id": r.EntryID, "node_id": r.NodeID, "route": route.Code},
			DedupKey: key,
		})
	}
	return nil
}
