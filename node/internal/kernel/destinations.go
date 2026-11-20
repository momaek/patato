package kernel

// 落地访问统计的内核部分：一条只在开启时生效的嗅探规则，读出 TLS SNI、HTTP Host 和 QUIC 的域名。
//
// sing-box 的路由规则在启动后不能修改，但 clash_mode 规则项每次匹配时都会询问注册在上下文里的
// ClashServer 当前的模式。这里注册一个只提供模式的最小实现（不监听 Clash API），开关落地统计就是切换模式，
// 不重建内核、不断开连接。

import (
	"context"
	"net"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/observable"
)

const (
	modeDestinations = "patato-destinations"
	modeOff          = "patato-off"
	// sniffTimeout 是嗅探等待客户端首包的时间；读不到域名时按连接目标统计。
	sniffTimeout = "300ms"
)

// sniffRule 只在落地统计开启、且连接带用户（节点入站 n<id>）时嗅探；
// 没有用户的入站（例如中转的 r<id>）不受影响。
func sniffRule() map[string]any {
	return map[string]any{
		"type": "logical", "mode": "and",
		"rules": []map[string]any{
			{"clash_mode": modeDestinations},
			{"auth_user": []string{""}, "invert": true},
		},
		"action":  "sniff",
		"sniffer": []string{"tls", "http", "quic"},
		"timeout": sniffTimeout,
	}
}

// modeSwitch 实现 adapter.ClashServer 中 clash_mode 规则用到的部分。
type modeSwitch struct {
	on      atomic.Bool
	history *urltest.HistoryStorage
}

var _ adapter.ClashServer = (*modeSwitch)(nil)

func newModeSwitch() *modeSwitch { return &modeSwitch{history: urltest.NewHistoryStorage()} }

func (m *modeSwitch) Name() string                                       { return "patato-mode" }
func (m *modeSwitch) Start(adapter.StartStage) error                     { return nil }
func (m *modeSwitch) Close() error                                       { return nil }
func (m *modeSwitch) ModeList() []string                                 { return []string{modeOff, modeDestinations} }
func (m *modeSwitch) SetModeUpdateHook(*observable.Subscriber[struct{}]) {}
func (m *modeSwitch) HistoryStorage() adapter.URLTestHistoryStorage      { return m.history }

func (m *modeSwitch) Mode() string {
	if m.on.Load() {
		return modeDestinations
	}
	return modeOff
}

func (m *modeSwitch) RoutedConnection(_ context.Context, conn net.Conn, _ adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	return conn
}

func (m *modeSwitch) RoutedPacketConnection(_ context.Context, conn N.PacketConn, _ adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	return conn
}

// SetDestinations 开启或关闭落地访问统计：新连接开始（停止）嗅探，计量层开始（停止）按目标累加。
func (k *Kernel) SetDestinations(on bool) {
	k.mode.on.Store(on)
	if k.opts.Meter != nil {
		k.opts.Meter.SetDestinations(on)
	}
}
