// Package agent 是 patato-node 的调度：从控制面同步节点和用户、应用到内核、定时上报。
package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/momaek/patato/node/internal/control"
	"github.com/momaek/patato/node/internal/kernel"
	"github.com/momaek/patato/node/internal/meter"
	"github.com/momaek/patato/node/internal/spool"
	"github.com/momaek/patato/nodeapi"
)

// RebuildGap 是同一节点两次「为新增用户重建 inbound」之间的最短间隔。重建会断开 UDP / QUIC 会话，
// 间隔内的新增合并到下一次；被移除的用户不受影响，meter 立即拒绝。
const RebuildGap = 30 * time.Second

type Agent struct {
	src     control.Source
	kernel  *kernel.Kernel
	meter   *meter.Meter
	spool   *spool.Spool
	log     *slog.Logger
	version string

	mu       sync.Mutex
	nodes    map[int64]*local
	settings nodeapi.Settings
	syncNow  chan struct{}
	report   chan struct{}
	now      func() time.Time
	gap      time.Duration // 即 RebuildGap，测试里调小
}

// local 是一个节点在本地已应用的状态。
type local struct {
	spec        nodeapi.Node
	configRev   string
	usersRev    string
	users       map[int64]nodeapi.User
	lastRebuild time.Time
	pending     bool // 有新增用户等待重建
}

func New(src control.Source, k *kernel.Kernel, m *meter.Meter, sp *spool.Spool, version string, log *slog.Logger) *Agent {
	return &Agent{
		src: src, kernel: k, meter: m, spool: sp, log: log, version: version,
		nodes:    map[int64]*local{},
		settings: nodeapi.Settings{ReportInterval: 60, SyncInterval: 60, DeviceTTL: 300},
		syncNow:  make(chan struct{}, 1), report: make(chan struct{}, 1), now: time.Now, gap: RebuildGap,
	}
}

// TriggerSync 让调度尽快同步一次，面板推送 changed 时调用。
func (a *Agent) TriggerSync() {
	select {
	case a.syncNow <- struct{}{}:
	default:
	}
}

func (a *Agent) triggerReport() {
	select {
	case a.report <- struct{}{}:
	default:
	}
}

// Run 运行到 ctx 结束；退出前把最后一段流量写进本地队列并尽量发出去。
func (a *Agent) Run(ctx context.Context) error {
	go a.src.Watch(ctx, a.TriggerSync)
	a.syncOnce(ctx)
	a.flush(ctx)

	syncT := time.NewTicker(a.interval(a.settings.SyncInterval))
	reportT := time.NewTicker(a.interval(a.settings.ReportInterval))
	maintT := time.NewTicker(10 * time.Second)
	defer syncT.Stop()
	defer reportT.Stop()
	defer maintT.Stop()
	var reportSoon <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			a.collect()
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			a.flush(flushCtx)
			cancel()
			return nil
		case <-a.syncNow:
			before := a.settings
			a.syncOnce(ctx)
			if a.settings != before {
				syncT.Reset(a.interval(a.settings.SyncInterval))
				reportT.Reset(a.interval(a.settings.ReportInterval))
			}
		case <-syncT.C:
			a.syncOnce(ctx)
		case <-a.report:
			// 节点刚启动或变更后，稍等片刻再上报状态，让面板尽快把节点转为在线
			if reportSoon == nil {
				reportSoon = time.After(time.Second)
			}
		case <-reportSoon:
			reportSoon = nil
			a.collect()
			a.flush(ctx)
		case <-reportT.C:
			a.collect()
			a.flush(ctx)
		case <-maintT.C:
			a.maintain()
		}
	}
}

func (a *Agent) interval(sec int) time.Duration {
	if sec <= 0 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

// syncOnce 把本地已有的版本号报给控制面，应用返回的变化。
func (a *Agent) syncOnce(ctx context.Context) {
	a.mu.Lock()
	req := nodeapi.SyncRequest{Version: a.version, Kernel: kernel.Version(), Nodes: map[int64]nodeapi.KnownNode{}}
	for id, l := range a.nodes {
		req.Nodes[id] = nodeapi.KnownNode{ConfigRev: l.configRev, UsersRev: l.usersRev}
	}
	a.mu.Unlock()
	resp, err := a.src.Sync(ctx, req)
	if err != nil {
		if ctx.Err() == nil {
			if control.IsUnauthorized(err) {
				a.log.Error("machine token rejected; re-enroll with the install command from the panel", "err", err)
			} else {
				a.log.Warn("sync failed", "err", err)
			}
		}
		return
	}
	if a.apply(resp) {
		a.triggerReport()
	}
}

// apply 应用一次同步结果，返回是否有变化。
func (a *Agent) apply(resp *nodeapi.SyncResponse) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if resp.Settings.ReportInterval > 0 {
		a.settings = resp.Settings
		a.meter.SetDeviceTTL(time.Duration(resp.Settings.DeviceTTL) * time.Second)
	}
	a.meter.SetRemote(resp.Remote)
	changed := false
	resync := false
	for _, ns := range resp.Nodes {
		changed = true
		l := a.nodes[ns.ID]
		if l == nil {
			if ns.Config == nil {
				// 本地没有这个节点，却只收到了用户变化：本地状态和面板不一致，下次全量同步
				resync = true
				continue
			}
			l = &local{users: map[int64]nodeapi.User{}}
			a.nodes[ns.ID] = l
		}
		usersChanged := false
		switch {
		case ns.FullUsers:
			l.users = make(map[int64]nodeapi.User, len(ns.Users))
			for _, u := range ns.Users {
				l.users[u.ID] = u
			}
			usersChanged = true
		case ns.Delta != nil:
			for _, id := range ns.Delta.Remove {
				delete(l.users, id)
			}
			for _, u := range ns.Delta.Upsert {
				l.users[u.ID] = u
			}
			usersChanged = true
		}
		users := l.userList()
		if usersChanged && nodeapi.UsersRev(users) != ns.UsersRev {
			a.log.Warn("user delta does not match panel revision; requesting full list", "node", ns.ID)
			l.usersRev = ""
			resync = true
		} else {
			l.usersRev = ns.UsersRev
		}
		a.meter.SetUsers(ns.ID, users)
		if ns.Config != nil {
			l.spec = *ns.Config
			l.configRev = ns.ConfigRev
			l.lastRebuild = a.now()
			l.pending = false
			_ = a.kernel.Apply(l.spec, users)
			continue
		}
		if usersChanged {
			a.updateUsers(ns.ID, l, users)
		}
	}
	for _, id := range resp.Removed {
		changed = true
		a.kernel.Remove(id)
		a.meter.RemoveNode(id)
		delete(a.nodes, id)
		a.log.Info("node removed", "node", id)
	}
	if resync {
		a.TriggerSync()
	}
	return changed
}

// updateUsers 让内核认识新用户；距离上次重建太近时推迟到 maintain。调用方持有锁。
func (a *Agent) updateUsers(id int64, l *local, users []nodeapi.User) {
	if a.now().Sub(l.lastRebuild) < a.gap {
		l.pending = true
		return
	}
	rebuilt, err := a.kernel.SetUsers(id, users)
	if err != nil {
		a.log.Error("update users", "node", l.spec.Code, "err", err)
	}
	if rebuilt {
		l.lastRebuild = a.now()
	}
	l.pending = false
}

// maintain 处理推迟的重建，并重试启动失败的节点。
func (a *Agent) maintain() {
	a.mu.Lock()
	for id, l := range a.nodes {
		if l.pending && a.now().Sub(l.lastRebuild) >= a.gap {
			a.updateUsers(id, l, l.userList())
		}
	}
	a.mu.Unlock()
	a.kernel.Retry()
}

func (l *local) userList() []nodeapi.User {
	out := make([]nodeapi.User, 0, len(l.users))
	for _, u := range l.users {
		out = append(out, u)
	}
	return out
}

// collect 把这段时间的流量、在线 IP 和每个节点的状态写成一批上报放进本地队列。
func (a *Agent) collect() {
	data := a.meter.Collect()
	r := nodeapi.ReportRequest{At: a.now().UTC()}
	for _, id := range a.kernel.Nodes() {
		nr := data[id]
		if nr == nil {
			nr = &nodeapi.NodeReport{ID: id}
		}
		delete(data, id)
		nr.Status = a.kernel.Status(id)
		r.Nodes = append(r.Nodes, *nr)
	}
	// 已经移除的节点在移除前产生的流量也要上报
	for _, nr := range data {
		if len(nr.Traffic) > 0 {
			r.Nodes = append(r.Nodes, nodeapi.NodeReport{ID: nr.ID, Traffic: nr.Traffic})
		}
	}
	if _, err := a.spool.Append(r); err != nil {
		a.log.Error("write report to spool", "err", err)
	}
}

// flush 按顺序发出队列里的批次，遇到失败就停下，下次再从这一批开始。
func (a *Agent) flush(ctx context.Context) {
	pending, err := a.spool.Pending()
	if err != nil {
		a.log.Error("read spool", "err", err)
		return
	}
	for _, r := range pending {
		resp, err := a.src.Report(ctx, r)
		if err != nil {
			if ctx.Err() == nil {
				a.log.Warn("report failed, will retry", "seq", r.Seq, "pending", len(pending), "err", err)
			}
			return
		}
		if err := a.spool.Ack(resp.Ack); err != nil {
			a.log.Error("ack spool", "err", err)
			return
		}
	}
}
