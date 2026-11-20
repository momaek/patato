package meter

// 落地访问统计：按「节点 × 用户 × 目标」累加流量与连接数，只保留聚合结果，不记连接级明细。
// 面板开启后（Settings.Destinations）才采集；条目数每节点每个上报周期有上限，超出的合并进每个用户的「其他」。

import (
	"hash/maphash"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/publicsuffix"

	"github.com/momaek/patato/nodeapi"
)

type destKey struct {
	node, user int64
	kind, dest string
}

type destCounter struct {
	up, down, conns atomic.Int64
	refs            int    // 正在使用它的连接数，持有 destinations.mu 时读写
	gen             uint64 // 创建时的开关代数；关闭再开启后，UDP 会话缓存的旧计数器据此作废
}

// maxOverflowTracked 是每个节点每个周期为计算「被合并的条目数」最多记住的被合并目标；超出后按连接计数（偏大）。
const maxOverflowTracked = 50000

type destinations struct {
	enabled  atomic.Bool
	gen      atomic.Uint64
	mu       sync.Mutex
	max      int
	counters map[destKey]*destCounter
	perNode  map[int64]int                 // 节点当前的普通条目数（不含「其他」）
	overflow map[int64]map[uint64]struct{} // 节点 → 这个周期被合并的目标（哈希），用于去重计数
	merged   map[[2]int64]int64            // 节点 + 用户 → 这个周期被合并的条目数
	seed     maphash.Seed
}

func newDestinations() *destinations {
	return &destinations{
		max: nodeapi.MaxDestinations, counters: map[destKey]*destCounter{}, perNode: map[int64]int{},
		overflow: map[int64]map[uint64]struct{}{}, merged: map[[2]int64]int64{}, seed: maphash.MakeSeed(),
	}
}

// SetDestinations 开启或关闭落地访问统计。关闭时丢弃尚未上报的统计，已有连接也不再计入。
func (m *Meter) SetDestinations(on bool) {
	d := m.dest
	if d.enabled.Swap(on) == on {
		return
	}
	if !on {
		d.mu.Lock()
		d.gen.Add(1)
		d.counters, d.perNode = map[destKey]*destCounter{}, map[int64]int{}
		d.overflow, d.merged = map[int64]map[uint64]struct{}{}, map[[2]int64]int64{}
		d.mu.Unlock()
	}
}

// DestinationsEnabled 说明是否在采集落地访问统计。
func (m *Meter) DestinationsEnabled() bool { return m.dest.enabled.Load() }

// acquire 取得目标的计数器并登记一个连接；条目数到上限时返回这个用户的「其他」。
func (d *destinations) acquire(node, user int64, kind, dest string) *destCounter {
	k := destKey{node, user, kind, dest}
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.counters[k]
	if c == nil {
		if d.perNode[node] >= d.max {
			d.markOverflow(node, user, kind, dest)
			k = destKey{node, user, nodeapi.DestinationOther, nodeapi.DestinationOtherName}
			c = d.counters[k]
		}
		if c == nil {
			c = &destCounter{gen: d.gen.Load()}
			d.counters[k] = c
			if k.kind != nodeapi.DestinationOther {
				d.perNode[node]++
			}
		}
	}
	c.refs++
	c.conns.Add(1)
	return c
}

func (d *destinations) markOverflow(node, user int64, kind, dest string) {
	set := d.overflow[node]
	if set == nil {
		set = map[uint64]struct{}{}
		d.overflow[node] = set
	}
	var h maphash.Hash
	h.SetSeed(d.seed)
	var b [16]byte
	for i := range 8 {
		b[i] = byte(user >> (8 * i))
	}
	_, _ = h.Write(b[:8])
	_, _ = h.WriteString(kind)
	_, _ = h.WriteString("\x00")
	_, _ = h.WriteString(dest)
	sum := h.Sum64()
	if _, seen := set[sum]; seen {
		return
	}
	if len(set) < maxOverflowTracked {
		set[sum] = struct{}{}
	}
	d.merged[[2]int64{node, user}]++
}

func (d *destinations) release(c *destCounter) {
	d.mu.Lock()
	c.refs--
	d.mu.Unlock()
}

// CollectDestinations 取出这个周期的访问统计并清零：每个节点的条目和被合并的条目数。
// 没有连接在用、本周期也没有流量的条目被删掉，空出上限。
func (m *Meter) CollectDestinations() map[int64]*nodeapi.NodeReport {
	d := m.dest
	out := map[int64]*nodeapi.NodeReport{}
	if !d.enabled.Load() {
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	get := func(node int64) *nodeapi.NodeReport {
		r := out[node]
		if r == nil {
			r = &nodeapi.NodeReport{ID: node}
			out[node] = r
		}
		return r
	}
	for k, c := range d.counters {
		up, down, conns := c.up.Swap(0), c.down.Swap(0), c.conns.Swap(0)
		merged := int64(0)
		if k.kind == nodeapi.DestinationOther {
			merged = d.merged[[2]int64{k.node, k.user}]
		}
		if up > 0 || down > 0 || conns > 0 || merged > 0 {
			r := get(k.node)
			r.Destinations = append(r.Destinations, nodeapi.DestinationStat{
				User: k.user, Kind: k.kind, Dest: k.dest, Up: up, Down: down, Conns: conns, Merged: merged,
			})
			r.DestinationsMerged += int(merged)
		}
		if c.refs <= 0 {
			delete(d.counters, k)
			if k.kind != nodeapi.DestinationOther {
				d.perNode[k.node]--
			}
		}
	}
	for node, n := range d.perNode {
		if n <= 0 {
			delete(d.perNode, node)
		}
	}
	d.overflow, d.merged = map[int64]map[uint64]struct{}{}, map[[2]int64]int64{}
	return out
}

// Classify 把目标归一化为统计键：域名取可注册域名（www.google.com → google.com），IP 原样记录。
// 无法识别时返回空字符串。
func Classify(host string) (kind, dest string) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return "", ""
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return nodeapi.DestinationIP, ip.Unmap().WithZone("").String()
	}
	if len(host) > 253 {
		host = host[len(host)-253:]
	}
	if etld1, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return nodeapi.DestinationDomain, etld1
	}
	// 单标签名称或本身就是公共后缀（例如 localhost、com），原样记录
	return nodeapi.DestinationDomain, host
}

// SetDestination 登记 TCP 连接的目标；之后的读写在用户计数之外再计入这个目标。连接建立时调用一次。
func (s *Session) SetDestination(kind, dest string) {
	if dest == "" || !s.m.dest.enabled.Load() {
		return
	}
	c := s.m.dest.acquire(s.k.node, s.k.user, kind, dest)
	if old := s.tcpDest.Swap(c); old != nil {
		s.m.dest.release(old)
	}
}

// maxPacketDests 是一个 UDP 会话缓存的目标数；超出时清空重来。
const maxPacketDests = 16

// packetDest 返回 UDP 包的目标计数器，同一会话里第一次出现的目标算一个连接。
func (s *Session) packetDest(kind, dest string) *destCounter {
	if dest == "" || !s.m.dest.enabled.Load() {
		return nil
	}
	key := kind + "\x00" + dest
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if c := s.udpDests[key]; c != nil && c.gen == s.m.dest.gen.Load() {
		return c
	} else if c != nil {
		s.m.dest.release(c)
		delete(s.udpDests, key)
	}
	if len(s.udpDests) >= maxPacketDests {
		s.releasePacketDestsLocked()
	}
	if s.udpDests == nil {
		s.udpDests = map[string]*destCounter{}
	}
	c := s.m.dest.acquire(s.k.node, s.k.user, kind, dest)
	s.udpDests[key] = c
	return c
}

func (s *Session) releasePacketDestsLocked() {
	for k, c := range s.udpDests {
		s.m.dest.release(c)
		delete(s.udpDests, k)
	}
}

// UpTo 记录发往某个目标的 UDP 上行字节数，限速时会等待。
func (s *Session) UpTo(n int, kind, dest string) {
	if c := s.packetDest(kind, dest); c != nil {
		c.up.Add(int64(n))
	}
	s.Up(n)
}

// DownFrom 记录来自某个目标的 UDP 下行字节数，限速时会等待。
func (s *Session) DownFrom(n int, kind, dest string) {
	if c := s.packetDest(kind, dest); c != nil {
		c.down.Add(int64(n))
	}
	s.Down(n)
}

func (s *Session) releaseDests() {
	if c := s.tcpDest.Swap(nil); c != nil {
		s.m.dest.release(c)
	}
	s.dmu.Lock()
	s.releasePacketDestsLocked()
	s.dmu.Unlock()
}
