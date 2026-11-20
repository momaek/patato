package kernel

import (
	"context"
	"net"
	"strconv"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/momaek/patato/node/internal/meter"
)

// tracker 在每个路由后的连接上登记到 meter：统计流量、限速、限制设备数；被拒绝的连接直接关掉。
type tracker struct {
	meter *meter.Meter
	mode  *modeSwitch
}

// destOf 是连接用于落地访问统计的目标：优先用嗅探出的域名，其次用连接目标本身（域名或 IP）。
func destOf(md adapter.InboundContext) (string, string) {
	if md.Domain != "" {
		return meter.Classify(md.Domain)
	}
	if md.Destination.IsFqdn() {
		return meter.Classify(md.Destination.Fqdn)
	}
	if md.Destination.Addr.IsValid() {
		return meter.Classify(md.Destination.Addr.String())
	}
	return "", ""
}

func (t *tracker) session(md adapter.InboundContext, closer interface{ Close() error }) (*meter.Session, bool, error) {
	if t.meter == nil {
		return nil, false, nil
	}
	node, ok := parseTag(md.Inbound)
	if !ok {
		return nil, false, nil
	}
	user, err := strconv.ParseInt(md.User, 10, 64)
	if err != nil {
		return nil, true, meter.ErrUnknownUser
	}
	s, err := t.meter.Open(node, user, md.Source.Addr, closer)
	return s, true, err
}

func (t *tracker) RoutedConnection(ctx context.Context, conn net.Conn, md adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	s, ours, err := t.session(md, conn)
	if !ours {
		return conn
	}
	if err != nil {
		_ = conn.Close()
		return conn
	}
	if t.meter.DestinationsEnabled() {
		s.SetDestination(destOf(md))
	}
	// 嗅探时读出的首包缓存在 CachedConn 里；它的缓存读与关闭之间没有同步，包一层后会被并发调用，
	// 所以把缓存取出来自己管理。
	var pending []byte
	for {
		cc, ok := conn.(*bufio.CachedConn)
		if !ok {
			break
		}
		if b := cc.ReadCached(); b != nil {
			pending = append(append([]byte{}, b.Bytes()...), pending...)
			b.Release()
		}
		conn = cc.Upstream().(net.Conn)
	}
	return &trackedConn{ExtendedConn: bufio.NewExtendedConn(conn), s: s, pending: pending}
}

func (t *tracker) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, md adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	s, ours, err := t.session(md, conn)
	if !ours {
		return conn
	}
	if err != nil {
		_ = conn.Close()
		return conn
	}
	pc := &trackedPacketConn{PacketConn: conn, s: s, meter: t.meter}
	if t.meter.DestinationsEnabled() {
		// 嗅探出的域名（QUIC）属于连接的首个目标；之后发往同一地址的包都记到这个域名下
		pc.first = md.Destination
		pc.firstKind, pc.firstDest = destOf(md)
	}
	return pc
}

// trackedConn 包住客户端一侧的连接：从它读到的是上行，写进它的是下行。
type trackedConn struct {
	N.ExtendedConn
	s       *meter.Session
	mu      sync.Mutex
	pending []byte // 嗅探时已读出、还没交给上层的首包
}

func (c *trackedConn) takePending(p []byte) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n
}

func (c *trackedConn) hasPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) > 0
}

func (c *trackedConn) Read(p []byte) (int, error) {
	if c.hasPending() {
		n := c.takePending(p)
		c.s.Up(n)
		return n, nil
	}
	n, err := c.ExtendedConn.Read(p)
	c.s.Up(n)
	return n, err
}

func (c *trackedConn) ReadBuffer(b *buf.Buffer) error {
	if c.hasPending() {
		n := c.takePending(b.FreeBytes())
		b.Extend(n)
		c.s.Up(n)
		return nil
	}
	err := c.ExtendedConn.ReadBuffer(b)
	if err == nil {
		c.s.Up(b.Len())
	}
	return err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	n, err := c.ExtendedConn.Write(p)
	c.s.Down(n)
	return n, err
}

func (c *trackedConn) WriteBuffer(b *buf.Buffer) error {
	n := b.Len()
	err := c.ExtendedConn.WriteBuffer(b)
	if err == nil {
		c.s.Down(n)
	}
	return err
}

func (c *trackedConn) Close() error {
	c.s.Close()
	c.mu.Lock()
	c.pending = nil
	c.mu.Unlock()
	return c.ExtendedConn.Close()
}

func (c *trackedConn) Upstream() any { return c.ExtendedConn }

type trackedPacketConn struct {
	N.PacketConn
	s     *meter.Session
	meter *meter.Meter

	// 落地访问统计：首个目标与嗅探出的域名，以及最近见过的目标的归一化结果
	first                M.Socksaddr
	firstKind, firstDest string
	mu                   sync.Mutex
	seen                 map[M.Socksaddr][2]string
}

// classify 归一化 UDP 包的目标，结果按地址缓存，不在每个包上重新计算。
func (c *trackedPacketConn) classify(addr M.Socksaddr) (string, string) {
	if !c.meter.DestinationsEnabled() {
		return "", ""
	}
	if addr == c.first && c.firstDest != "" {
		return c.firstKind, c.firstDest
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.seen[addr]; ok {
		return v[0], v[1]
	}
	var kind, dest string
	if addr.IsFqdn() {
		kind, dest = meter.Classify(addr.Fqdn)
	} else if addr.Addr.IsValid() {
		kind, dest = meter.Classify(addr.Addr.String())
	}
	if c.seen == nil || len(c.seen) >= 64 {
		c.seen = map[M.Socksaddr][2]string{}
	}
	c.seen[addr] = [2]string{kind, dest}
	return kind, dest
}

func (c *trackedPacketConn) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	dst, err := c.PacketConn.ReadPacket(b)
	if err == nil {
		kind, dest := c.classify(dst)
		c.s.UpTo(b.Len(), kind, dest)
	}
	return dst, err
}

func (c *trackedPacketConn) WritePacket(b *buf.Buffer, dst M.Socksaddr) error {
	n := b.Len()
	err := c.PacketConn.WritePacket(b, dst)
	if err == nil {
		kind, d := c.classify(dst)
		c.s.DownFrom(n, kind, d)
	}
	return err
}

func (c *trackedPacketConn) Close() error {
	c.s.Close()
	return c.PacketConn.Close()
}

func (c *trackedPacketConn) Upstream() any { return c.PacketConn }
