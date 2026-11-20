package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/bufio"
	singjson "github.com/sagernet/sing/common/json"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/momaek/patato/nodeapi"
)

// Userspace 用内置 sing-box 的 direct 入站转发：每条转发一个入站（标签 r<转发 ID>，TCP 与 UDP 都监听），
// override_address / override_port 指向下一跳。
//
// 用一个独立的 sing-box 实例，而不是承载节点的那个：节点内核的「拒绝访问内网」规则不应拦住运营者配置的下一跳
// （同机房可能走内网），节点的计量层也就不会看到中转连接。计数用 sing 的计数连接，copy 时能解包到底层连接，
// Linux 上 TCP 仍走 splice 路径。
type Userspace struct {
	logLevel string

	mu      sync.Mutex
	box     *box.Box
	ctx     context.Context
	applied map[int64]nodeapi.Forward
	counts  map[int64]*pair
}

type pair struct{ up, down atomic.Int64 }

func NewUserspace(logLevel string) *Userspace {
	if logLevel == "" {
		logLevel = "warn"
	}
	return &Userspace{logLevel: logLevel, applied: map[int64]nodeapi.Forward{}, counts: map[int64]*pair{}}
}

func (u *Userspace) Name() string { return "userspace" }

func Tag(id int64) string { return "r" + strconv.FormatInt(id, 10) }

// ParseTag 解析中转入站的标签；节点入站（n 开头）返回 false。
func ParseTag(t string) (int64, bool) {
	if len(t) < 2 || t[0] != 'r' {
		return 0, false
	}
	id, err := strconv.ParseInt(t[1:], 10, 64)
	return id, err == nil
}

func (u *Userspace) start() error {
	if u.box != nil {
		return nil
	}
	ctx := service.ContextWithDefaultRegistry(include.Context(context.Background()))
	base := map[string]any{
		"log":       map[string]any{"level": u.logLevel, "timestamp": true},
		"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "direct"},
	}
	raw, _ := json.Marshal(base)
	options, err := singjson.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		return fmt.Errorf("relay kernel options: %w", err)
	}
	b, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return fmt.Errorf("create relay kernel: %w", err)
	}
	b.Router().AppendTracker(&counter{u: u})
	if err := b.Start(); err != nil {
		_ = b.Close()
		return fmt.Errorf("start relay kernel: %w", err)
	}
	u.box, u.ctx = b, ctx
	return nil
}

// Apply 按完整列表增删入站：没变的转发不动（不断开现有连接），变了的先删后建。
func (u *Userspace) Apply(fs []nodeapi.Forward) map[int64]error {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := map[int64]error{}
	if len(fs) == 0 && u.box == nil {
		return out
	}
	if err := u.start(); err != nil {
		for _, f := range fs {
			out[f.ID] = err
		}
		return out
	}
	want := map[int64]nodeapi.Forward{}
	for _, f := range fs {
		want[f.ID] = f
	}
	mgr := u.box.Inbound()
	for id, f := range u.applied {
		if w, ok := want[id]; !ok || w != f {
			_ = mgr.Remove(Tag(id))
			delete(u.applied, id)
		}
	}
	for id := range u.counts {
		if _, ok := want[id]; !ok {
			delete(u.counts, id)
		}
	}
	for _, f := range fs {
		if _, ok := u.applied[f.ID]; ok {
			out[f.ID] = nil
			continue
		}
		if u.counts[f.ID] == nil {
			u.counts[f.ID] = &pair{}
		}
		if err := u.create(f); err != nil {
			out[f.ID] = err
			continue
		}
		u.applied[f.ID] = f
		out[f.ID] = nil
	}
	return out
}

func (u *Userspace) create(f nodeapi.Forward) error {
	in := map[string]any{
		"type": "direct", "tag": Tag(f.ID), "listen": "::", "listen_port": f.ListenPort,
		"override_address": f.TargetIP, "override_port": f.TargetPort,
	}
	raw, _ := json.Marshal(in)
	var opt option.Inbound
	if err := opt.UnmarshalJSONContext(u.ctx, raw); err != nil {
		return fmt.Errorf("relay inbound options: %w", err)
	}
	logger := u.box.LogFactory().NewLogger("relay[" + Tag(f.ID) + "]")
	if err := u.box.Inbound().Create(u.ctx, u.box.Router(), logger, Tag(f.ID), opt.Type, opt.Options); err != nil {
		return fmt.Errorf("监听端口 %d 失败：%w", f.ListenPort, err)
	}
	return nil
}

// Read 返回累计计数。
func (u *Userspace) Read() (map[int64]Counters, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make(map[int64]Counters, len(u.counts))
	for id, p := range u.counts {
		out[id] = Counters{Up: uint64(p.up.Load()), Down: uint64(p.down.Load())}
	}
	return out, nil
}

func (u *Userspace) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.box == nil {
		return nil
	}
	err := u.box.Close()
	u.box = nil
	u.applied = map[int64]nodeapi.Forward{}
	return err
}

func (u *Userspace) counter(tag string) *pair {
	id, ok := ParseTag(tag)
	if !ok {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[id]
}

// counter 给中转连接加上计数：从客户端读到的是上行，写给客户端的是下行。
type counter struct{ u *Userspace }

func (c *counter) RoutedConnection(_ context.Context, conn net.Conn, md adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	p := c.u.counter(md.Inbound)
	if p == nil {
		return conn
	}
	return bufio.NewInt64CounterConn(conn, []*atomic.Int64{&p.up}, []*atomic.Int64{&p.down})
}

func (c *counter) RoutedPacketConnection(_ context.Context, conn N.PacketConn, md adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	p := c.u.counter(md.Inbound)
	if p == nil {
		return conn
	}
	return bufio.NewInt64CounterPacketConn(conn, []*atomic.Int64{&p.up}, nil, []*atomic.Int64{&p.down}, nil)
}
