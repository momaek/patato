// Package meter 是 patato-node 与内核无关的计量与限制层：按用户统计流量、限速、限制设备数，
// 并记录每个连接以便用户被移除时立即断开。内核在每个新连接上调用 Open，在连接上调用 Session 的方法。
package meter

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/momaek/patato/nodeapi"
)

var (
	ErrUnknownUser   = errors.New("user is not allowed on this node")
	ErrDeviceLimit   = errors.New("device limit reached")
	ErrNodeNotServed = errors.New("node is not served")
)

type key struct{ node, user int64 }

type policy struct {
	speed   int // Mbps
	devices int
}

type counter struct{ up, down atomic.Int64 }

type userLimiter struct {
	mbps     int
	up, down *rate.Limiter
}

type Meter struct {
	mu        sync.Mutex
	policies  map[int64]map[int64]policy // 节点 → 用户 → 限制
	counters  map[key]*counter
	sessions  map[key]map[*Session]bool
	limiters  map[int64]*userLimiter            // 用户限速在整台机器上共享
	ips       map[int64]map[netip.Addr]*ipState // 用户 → IP（整台机器），用于设备数
	nodeIPs   map[key]map[netip.Addr]time.Time  // 节点 + 用户 → IP 最近活跃时间，用于上报在线 IP
	remote    map[int64]int                     // 用户在其他机器上的在线设备数
	deviceTTL time.Duration
	now       func() time.Time
}

type ipState struct {
	active   int
	lastSeen time.Time
}

func New(deviceTTL time.Duration) *Meter {
	return &Meter{
		policies: map[int64]map[int64]policy{}, counters: map[key]*counter{}, sessions: map[key]map[*Session]bool{},
		limiters: map[int64]*userLimiter{}, ips: map[int64]map[netip.Addr]*ipState{}, nodeIPs: map[key]map[netip.Addr]time.Time{},
		remote: map[int64]int{}, deviceTTL: deviceTTL, now: time.Now,
	}
}

// SetDeviceTTL 调整多久没有连接的 IP 不再算作在线设备，面板在 sync 时下发。
func (m *Meter) SetDeviceTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	m.mu.Lock()
	m.deviceTTL = d
	m.mu.Unlock()
}

// SetUsers 替换一个节点的用户。被移除的用户的现有连接立即断开。
func (m *Meter) SetUsers(node int64, users []nodeapi.User) {
	next := make(map[int64]policy, len(users))
	for _, u := range users {
		next[u.ID] = policy{speed: u.SpeedLimit, devices: u.DeviceLimit}
	}
	m.mu.Lock()
	prev := m.policies[node]
	m.policies[node] = next
	var kill []*Session
	for uid := range prev {
		if _, ok := next[uid]; !ok {
			for s := range m.sessions[key{node, uid}] {
				kill = append(kill, s)
			}
		}
	}
	m.refreshLimiters()
	m.mu.Unlock()
	for _, s := range kill {
		s.kill()
	}
}

// RemoveNode 停止计量一个节点并断开它的全部连接。
func (m *Meter) RemoveNode(node int64) {
	m.mu.Lock()
	delete(m.policies, node)
	var kill []*Session
	for k, set := range m.sessions {
		if k.node == node {
			for s := range set {
				kill = append(kill, s)
			}
		}
	}
	m.refreshLimiters()
	m.mu.Unlock()
	for _, s := range kill {
		s.kill()
	}
}

// SetRemote 更新用户在其他机器上的在线设备数。
func (m *Meter) SetRemote(remote map[int64]int) {
	m.mu.Lock()
	m.remote = remote
	if m.remote == nil {
		m.remote = map[int64]int{}
	}
	m.mu.Unlock()
}

// refreshLimiters 按当前策略为每个用户保留一个限速器；同一用户在不同节点上取最严格的限速。调用方持有锁。
func (m *Meter) refreshLimiters() {
	want := map[int64]int{}
	for _, users := range m.policies {
		for uid, p := range users {
			if p.speed <= 0 {
				continue
			}
			if cur, ok := want[uid]; !ok || p.speed < cur {
				want[uid] = p.speed
			}
		}
	}
	for uid, l := range m.limiters {
		if want[uid] != l.mbps {
			delete(m.limiters, uid)
		}
	}
	for uid, mbps := range want {
		if _, ok := m.limiters[uid]; !ok {
			bytes := mbps * 125000 // Mbps → 字节/秒
			burst := max(bytes, 64<<10)
			m.limiters[uid] = &userLimiter{mbps: mbps,
				up: rate.NewLimiter(rate.Limit(bytes), burst), down: rate.NewLimiter(rate.Limit(bytes), burst)}
		}
	}
}

// Open 登记一个新连接。返回错误时内核应当拒绝这个连接。closer 用于在用户被移除时断开连接。
func (m *Meter) Open(node, user int64, ip netip.Addr, closer io.Closer) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	users, ok := m.policies[node]
	if !ok {
		return nil, ErrNodeNotServed
	}
	p, ok := users[user]
	if !ok {
		return nil, ErrUnknownUser
	}
	ip = ip.Unmap()
	now := m.now()
	ips := m.ips[user]
	if ips == nil {
		ips = map[netip.Addr]*ipState{}
		m.ips[user] = ips
	}
	st := ips[ip]
	if st == nil {
		if p.devices > 0 && m.devicesLocked(user, now)+m.remote[user] >= p.devices {
			return nil, ErrDeviceLimit
		}
		st = &ipState{}
		ips[ip] = st
	}
	st.active++
	st.lastSeen = now
	k := key{node, user}
	if m.nodeIPs[k] == nil {
		m.nodeIPs[k] = map[netip.Addr]time.Time{}
	}
	m.nodeIPs[k][ip] = now
	c := m.counters[k]
	if c == nil {
		c = &counter{}
		m.counters[k] = c
	}
	s := &Session{m: m, k: k, ip: ip, counter: c, closer: closer}
	if l := m.limiters[user]; l != nil {
		s.up, s.down = l.up, l.down
	}
	if m.sessions[k] == nil {
		m.sessions[k] = map[*Session]bool{}
	}
	m.sessions[k][s] = true
	return s, nil
}

// devicesLocked 返回用户当前在这台机器上的在线设备数：有活跃连接，或最近 deviceTTL 内有过连接的 IP。
func (m *Meter) devicesLocked(user int64, now time.Time) int {
	n := 0
	for _, st := range m.ips[user] {
		if st.active > 0 || now.Sub(st.lastSeen) < m.deviceTTL {
			n++
		}
	}
	return n
}

// Collect 取出自上次以来的流量并清零，同时给出每个节点上的在线 IP。
func (m *Meter) Collect() map[int64]*nodeapi.NodeReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := map[int64]*nodeapi.NodeReport{}
	get := func(node int64) *nodeapi.NodeReport {
		r := out[node]
		if r == nil {
			r = &nodeapi.NodeReport{ID: node}
			out[node] = r
		}
		return r
	}
	for k, c := range m.counters {
		up, down := c.up.Swap(0), c.down.Swap(0)
		if up > 0 || down > 0 {
			r := get(k.node)
			if r.Traffic == nil {
				r.Traffic = map[int64][2]int64{}
			}
			r.Traffic[k.user] = [2]int64{up, down}
		}
		if up == 0 && down == 0 && len(m.sessions[k]) == 0 {
			delete(m.counters, k)
		}
	}
	for k, ips := range m.nodeIPs {
		var alive []string
		for ip, seen := range ips {
			active := m.ips[k.user][ip] != nil && m.ips[k.user][ip].active > 0
			if active || now.Sub(seen) < m.deviceTTL {
				alive = append(alive, ip.String())
			} else {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(m.nodeIPs, k)
		}
		if len(alive) > 0 {
			slices.Sort(alive)
			r := get(k.node)
			if r.Alive == nil {
				r.Alive = map[int64][]string{}
			}
			r.Alive[k.user] = alive
		}
	}
	for uid, ips := range m.ips {
		for ip, st := range ips {
			if st.active == 0 && now.Sub(st.lastSeen) >= m.deviceTTL {
				delete(ips, ip)
			}
		}
		if len(ips) == 0 {
			delete(m.ips, uid)
		}
	}
	return out
}

// Connections 返回节点当前的连接数。
func (m *Meter) Connections(node int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, set := range m.sessions {
		if k.node == node {
			n += len(set)
		}
	}
	return n
}

// Session 是一个连接在计量层的登记。
type Session struct {
	m        *Meter
	k        key
	ip       netip.Addr
	counter  *counter
	up, down *rate.Limiter
	closer   io.Closer
	closed   atomic.Bool
}

// Up 记录上行（客户端发往节点）字节数，限速时会等待。
func (s *Session) Up(n int) {
	s.counter.up.Add(int64(n))
	wait(s.up, n)
}

// Down 记录下行字节数，限速时会等待。
func (s *Session) Down(n int) {
	s.counter.down.Add(int64(n))
	wait(s.down, n)
}

// Limited 说明这个连接是否限速；不限速的连接内核可以走更快的拷贝路径。
func (s *Session) Limited() bool { return s.up != nil }

func wait(l *rate.Limiter, n int) {
	if l == nil || n <= 0 {
		return
	}
	burst := l.Burst()
	for n > 0 {
		step := min(n, burst)
		_ = l.WaitN(context.Background(), step)
		n -= step
	}
}

// Close 注销连接，连接关闭时调用。
func (s *Session) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	m := s.m
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions[s.k], s)
	if len(m.sessions[s.k]) == 0 {
		delete(m.sessions, s.k)
	}
	if st := m.ips[s.k.user][s.ip]; st != nil {
		st.active--
		st.lastSeen = m.now()
	}
	if ips := m.nodeIPs[s.k]; ips != nil {
		ips[s.ip] = m.now()
	}
}

func (s *Session) kill() {
	if s.closer != nil {
		_ = s.closer.Close()
	}
	s.Close()
}
