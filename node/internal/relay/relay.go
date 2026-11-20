// Package relay 是 patato-node 的中转转发：按面板下发的完整转发列表维护本机转发规则，统计每条转发的字节数，
// 并定期对下一跳做 TCP 探测。
//
// 默认用 nftables（内核转发，独占 table inet patato，每次原子替换）；机器没有 nftables、内核不支持、
// 下一跳不是 IPv4 或运营者指定时，用内置 sing-box 的 direct 入站在用户态转发（入站标签 r<转发 ID>）。
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/momaek/patato/nodeapi"
)

// Counters 是一条转发的累计字节读数。
type Counters struct{ Up, Down uint64 }

// Backend 是一种转发实现。Apply 用完整列表原子替换现有转发，返回每条转发的结果（nil 为成功）；
// Read 返回每条转发当前的累计计数。
type Backend interface {
	Name() string
	Apply(fs []nodeapi.Forward) map[int64]error
	Read() (map[int64]Counters, error)
	Close() error
}

// ErrUnavailable 表示这台机器上用不了某种实现（例如没有 nftables），调用方应退回其他实现。
var ErrUnavailable = errors.New("backend unavailable")

// Manager 维护本机转发。
type Manager struct {
	nft       Backend // 可能为 nil（不是 Linux，或没有 nftables，或前置条件不满足）
	nftErr    error   // nft 不可用（ErrUnavailable）或前置条件不满足（例如 ip_forward 没开）的原因
	newNFT    func() (Backend, error)
	userspace Backend
	log       *slog.Logger
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	now       func() time.Time

	mu       sync.Mutex
	mode     string // 面板下发的机器模式：auto / nft / userspace
	rev      string
	forwards map[int64]nodeapi.Forward
	state    map[int64]*fwState
	probing  bool
}

type fwState struct {
	backend string
	err     error
	at      time.Time
	last    Counters // 上次读到的累计值
	up      int64    // 尚未上报的差值
	down    int64
	probe   probeAcc
}

type probeAcc struct {
	ok, fail    int
	latencySum  time.Duration
	consecutive int
	last        bool
	at          time.Time
	seen        bool
}

// NewManager 创建转发管理器。newNFT 创建 nft 实现：返回 ErrUnavailable 时（没有 nftables）auto 模式退回用户态；
// 返回其他错误（前置条件不满足，例如 ip_forward 没开）时 nft 转发标记为失败并上报原因，不偷偷退回，
// 由运营者处理后在下一次 Recheck 时恢复。
func NewManager(newNFT func() (Backend, error), userspace Backend, log *slog.Logger) *Manager {
	m := &Manager{
		newNFT: newNFT, userspace: userspace, log: log,
		dial: (&net.Dialer{}).DialContext, now: time.Now,
		mode: "auto", forwards: map[int64]nodeapi.Forward{}, state: map[int64]*fwState{},
	}
	m.initNFT()
	return m
}

func (m *Manager) initNFT() {
	if m.newNFT == nil {
		m.nft, m.nftErr = nil, fmt.Errorf("%w: 没有 nft 实现", ErrUnavailable)
		return
	}
	b, err := m.newNFT()
	if err != nil {
		m.nft, m.nftErr = nil, err
		return
	}
	m.nft, m.nftErr = b, nil
}

// Recheck 在前置条件曾经不满足时重新检查（例如运营者已经开启 ip_forward），恢复后重新应用转发。
func (m *Manager) Recheck() {
	m.mu.Lock()
	if m.nft != nil || m.nftErr == nil || errors.Is(m.nftErr, ErrUnavailable) {
		m.mu.Unlock()
		return
	}
	m.initNFT()
	recovered := m.nft != nil
	list := m.list()
	m.mu.Unlock()
	if recovered {
		m.log.Info("nftables prerequisites satisfied, re-applying forwards")
		m.apply(list)
	}
}

// Rev 是本地已应用的转发列表版本，随 sync 报给面板。
func (m *Manager) Rev() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rev
}

// SetMode 设置机器的转发模式（面板在 sync 的 settings 里下发）。模式变化时按新模式重建。
func (m *Manager) SetMode(mode string) {
	if mode == "" {
		mode = "auto"
	}
	m.mu.Lock()
	changed := m.mode != mode
	m.mode = mode
	list := m.list()
	m.mu.Unlock()
	if changed && len(list) > 0 {
		m.apply(list)
	}
}

// Sync 应用面板下发的完整转发列表。
func (m *Manager) Sync(fs *nodeapi.ForwardSync) {
	if fs == nil {
		return
	}
	m.mu.Lock()
	m.rev = fs.Rev
	m.forwards = make(map[int64]nodeapi.Forward, len(fs.Items))
	for _, f := range fs.Items {
		m.forwards[f.ID] = f
	}
	list := m.list()
	m.mu.Unlock()
	m.apply(list)
}

func (m *Manager) list() []nodeapi.Forward {
	out := make([]nodeapi.Forward, 0, len(m.forwards))
	for _, f := range m.forwards {
		out = append(out, f)
	}
	return out
}

// split 决定每条转发用哪种实现：机器或转发指定 userspace、没有 nftables（auto 模式）、下一跳不是 IPv4 时走用户态。
// 运营者指定 nft 但用不了，或前置条件不满足时不退回，转发标记为失败。调用方持有锁。
func (m *Manager) split(fs []nodeapi.Forward) (nft, user []nodeapi.Forward) {
	missing := m.nft == nil && errors.Is(m.nftErr, ErrUnavailable)
	for _, f := range fs {
		useUser := m.mode == "userspace" || f.Mode == "userspace"
		if !useUser && m.mode != "nft" {
			if ip := net.ParseIP(f.TargetIP); missing || ip == nil || ip.To4() == nil {
				useUser = true
			}
		}
		if useUser {
			user = append(user, f)
		} else {
			nft = append(nft, f)
		}
	}
	return nft, user
}

func (m *Manager) apply(fs []nodeapi.Forward) {
	m.mu.Lock()
	// 替换前先读一次计数：被删除或重建的转发，替换前的流量不会丢
	m.readCounters()
	nftList, userList := m.split(fs)
	nftB, nftErr := m.nft, m.nftErr
	m.mu.Unlock()
	results := map[int64]error{}
	backendOf := map[int64]string{}
	if nftB != nil {
		for id, err := range nftB.Apply(nftList) {
			results[id], backendOf[id] = err, nftB.Name()
		}
	} else {
		for _, f := range nftList {
			results[f.ID], backendOf[f.ID] = unavailable(nftErr), "nft"
		}
	}
	if m.userspace != nil {
		for id, err := range m.userspace.Apply(userList) {
			results[id], backendOf[id] = err, m.userspace.Name()
		}
	} else {
		for _, f := range userList {
			results[f.ID], backendOf[f.ID] = errors.New("用户态转发不可用"), "userspace"
		}
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.state {
		if _, ok := m.forwards[id]; !ok {
			// 移除前的流量在下一次 Collect 时仍会上报；之后删掉状态
			if st := m.state[id]; st.up == 0 && st.down == 0 {
				delete(m.state, id)
			} else {
				st.err = errRemoved
			}
		}
	}
	for _, f := range fs {
		st := m.state[f.ID]
		if st == nil {
			st = &fwState{}
			m.state[f.ID] = st
		}
		if st.backend != backendOf[f.ID] || backendOf[f.ID] == "nft" {
			// 换了实现（例如退回用户态）时新实现的计数器从 0 开始；nft 每次整表重建，计数器也从 0 开始。
			// 替换前已经读过一次，所以这里清零不会丢流量。
			st.last = Counters{}
		}
		st.backend, st.err = backendOf[f.ID], results[f.ID]
		if st.err == nil {
			st.at = now
		} else {
			m.log.Error("apply forward", "forward", f.ID, "port", f.ListenPort, "backend", st.backend, "err", st.err)
		}
	}
}

var errRemoved = errors.New("removed")

func unavailable(err error) error {
	if err == nil {
		return errors.New("这台机器上不能使用 nftables")
	}
	return err
}

// Delta 计算计数器差值：读数变小视为归零（patato-node 重启、表被重建），从新读数开始累计。
func Delta(prev, cur uint64) int64 {
	if cur < prev {
		return int64(cur)
	}
	return int64(cur - prev)
}

// readCounters 读取各实现的计数并累加差值。调用方持有锁。
func (m *Manager) readCounters() {
	read := func(b Backend) map[int64]Counters {
		if b == nil {
			return nil
		}
		c, err := b.Read()
		if err != nil {
			m.log.Warn("read forward counters", "backend", b.Name(), "err", err)
			return nil
		}
		return c
	}
	for _, b := range []Backend{m.nft, m.userspace} {
		cs := read(b)
		for id, c := range cs {
			st := m.state[id]
			if st == nil || st.backend != b.Name() {
				continue
			}
			st.up += Delta(st.last.Up, c.Up)
			st.down += Delta(st.last.Down, c.Down)
			st.last = c
		}
	}
}

// Collect 返回自上次以来每条转发的流量差值、应用结果与探测结果，并清零差值。
func (m *Manager) Collect() []nodeapi.ForwardReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readCounters()
	out := make([]nodeapi.ForwardReport, 0, len(m.state))
	for id, st := range m.state {
		r := nodeapi.ForwardReport{ID: id, Up: st.up, Down: st.down, Applied: st.err == nil, Mode: st.backend}
		if st.err == errRemoved {
			delete(m.state, id)
			if r.Up == 0 && r.Down == 0 {
				continue
			}
			r.Applied = false
			r.Error = "已移除"
		} else if st.err != nil {
			r.Error = st.err.Error()
		} else {
			r.At = st.at
		}
		if st.probe.seen {
			p := st.probe
			pr := &nodeapi.ProbeResult{OK: p.ok, Fail: p.fail, Consecutive: p.consecutive, Last: p.last, At: p.at}
			if p.ok > 0 {
				pr.AvgMS = int((p.latencySum / time.Duration(p.ok)).Milliseconds())
				if pr.AvgMS == 0 {
					pr.AvgMS = 1
				}
			}
			r.Probe = pr
			st.probe = probeAcc{consecutive: p.consecutive, last: p.last}
		}
		st.up, st.down = 0, 0
		out = append(out, r)
	}
	return out
}

// Status 是转发层的整体状态。
func (m *Manager) Status() *nodeapi.RelayStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.forwards) == 0 {
		return nil
	}
	st := &nodeapi.RelayStatus{Mode: "nft"}
	if m.mode == "userspace" || (m.nft == nil && errors.Is(m.nftErr, ErrUnavailable) && m.mode != "nft") {
		st.Mode = "userspace"
	}
	if m.nftErr != nil && m.mode != "userspace" {
		st.Error = m.nftErr.Error()
	}
	return st
}

// Probe 对每条转发的下一跳做一次 TCP 建连，记录成功与耗时。UDP 没有通用探测方式，只看流量与状态。
func (m *Manager) Probe(ctx context.Context) {
	m.mu.Lock()
	if m.probing {
		m.mu.Unlock()
		return
	}
	m.probing = true
	list := m.list()
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.probing = false
		m.mu.Unlock()
	}()
	type result struct {
		id  int64
		ok  bool
		dur time.Duration
	}
	results := make(chan result, len(list))
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, f := range list {
		wg.Add(1)
		go func(f nodeapi.Forward) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			start := m.now()
			conn, err := m.dial(c, "tcp", net.JoinHostPort(f.TargetIP, strconv.Itoa(f.TargetPort)))
			if err == nil {
				_ = conn.Close()
			}
			results <- result{id: f.ID, ok: err == nil, dur: m.now().Sub(start)}
		}(f)
	}
	wg.Wait()
	close(results)
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for r := range results {
		st := m.state[r.id]
		if st == nil {
			continue
		}
		p := &st.probe
		p.seen, p.last, p.at = true, r.ok, now
		if r.ok {
			p.ok++
			p.latencySum += r.dur
			p.consecutive = 0
		} else {
			p.fail++
			p.consecutive++
		}
	}
}

// Close 关闭各实现。nft 实现关闭时不删除表：patato-node 重启期间转发继续生效，启动后按面板列表原子替换。
func (m *Manager) Close() error {
	var errs []error
	for _, b := range []Backend{m.nft, m.userspace} {
		if b != nil {
			errs = append(errs, b.Close())
		}
	}
	return errors.Join(errs...)
}
