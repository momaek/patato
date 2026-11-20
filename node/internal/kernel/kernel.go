// Package kernel 把 sing-box 作为库内嵌，承载这台机器上的全部节点：每个节点是一个 inbound，
// 节点增删改通过 inbound 管理器在运行中完成，不重启进程。
//
// 按用户的流量统计、限速和设备数限制不依赖 sing-box 的 API，由连接追踪器接到 meter 包上完成。
package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"

	"github.com/momaek/patato/node/internal/meter"
	"github.com/momaek/patato/nodeapi"
)

type Options struct {
	// DataDir 存放 ACME 证书、自签名证书等内核数据。
	DataDir string
	// LogLevel 是 sing-box 的日志级别：trace、debug、info、warn、error。
	LogLevel string
	// BlockPrivate 拒绝用户访问内网和本机地址，防止节点被当作跳板访问机器所在的内网。
	BlockPrivate bool
	Meter        *meter.Meter
	Log          *slog.Logger
}

type Kernel struct {
	opts  Options
	ctx   context.Context
	box   *box.Box
	mode  *modeSwitch // 落地访问统计的开关
	mu    sync.Mutex
	nodes map[int64]*applied
}

// applied 是一个节点在内核里的现状。
type applied struct {
	spec      nodeapi.Node
	users     map[int64]nodeapi.User // 构建 inbound 时用的用户
	err       error
	at        time.Time
	cert      *time.Time
	listening *bool // 启动后从本机探测一次；只对 TCP 协议有意义
}

// Version 是内核名称和版本，随 sync 上报给面板。
func Version() string {
	v := C.Version
	if v == "unknown" {
		// 作为库使用时 sing-box 的版本号没有写进常量，从模块信息里取
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, d := range info.Deps {
				if d.Path == "github.com/sagernet/sing-box" {
					v = strings.TrimPrefix(d.Version, "v")
				}
			}
		}
	}
	return "sing-box " + v
}

func New(opts Options) (*Kernel, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.LogLevel == "" {
		opts.LogLevel = "warn"
	}
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	ctx := service.ContextWithDefaultRegistry(include.Context(context.Background()))
	// 嗅探规则放最前：它不结束匹配，只在开启落地访问统计时读出目标域名
	rules := []map[string]any{sniffRule()}
	if opts.BlockPrivate {
		rules = append(rules, map[string]any{"ip_is_private": true, "action": "reject"})
	}
	// 使用 hosts / resolv.conf，不依赖可能未运行的 systemd-resolved D-Bus 服务。
	base := map[string]any{
		"log":       map[string]any{"level": opts.LogLevel, "timestamp": true},
		"dns":       map[string]any{"servers": []map[string]any{{"type": "local", "tag": "local", "prefer_go": true}}},
		"outbounds": []map[string]any{{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"rules": rules, "final": "direct", "default_domain_resolver": "local"},
	}
	raw, _ := json.Marshal(base)
	mode := newModeSwitch()
	service.MustRegister[adapter.ClashServer](ctx, mode)
	options, err := singjson.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("kernel options: %w", err)
	}
	b, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return nil, fmt.Errorf("create kernel: %w", err)
	}
	b.Router().AppendTracker(&tracker{meter: opts.Meter, mode: mode})
	if err := b.Start(); err != nil {
		_ = b.Close()
		return nil, fmt.Errorf("start kernel: %w", err)
	}
	return &Kernel{opts: opts, ctx: ctx, box: b, mode: mode, nodes: map[int64]*applied{}}, nil
}

func (k *Kernel) Close() error { return k.box.Close() }

func tag(id int64) string { return "n" + strconv.FormatInt(id, 10) }

func parseTag(t string) (int64, bool) {
	if len(t) < 2 || t[0] != 'n' {
		return 0, false
	}
	id, err := strconv.ParseInt(t[1:], 10, 64)
	return id, err == nil
}

// Apply 按配置和用户（重新）创建节点的 inbound。失败时节点不在运行，错误会出现在 Status 里。
func (k *Kernel) Apply(spec nodeapi.Node, users []nodeapi.User) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	a := &applied{spec: spec, users: map[int64]nodeapi.User{}, at: time.Now()}
	for _, u := range users {
		a.users[u.ID] = u
	}
	k.nodes[spec.ID] = a
	a.err = k.create(a, users)
	if a.err != nil {
		k.opts.Log.Error("start node", "node", spec.Code, "err", a.err)
	} else {
		k.opts.Log.Info("node running", "node", spec.Code, "protocol", spec.Protocol, "port", spec.Port, "users", len(users))
	}
	return a.err
}

func (k *Kernel) create(a *applied, users []nodeapi.User) error {
	t := tag(a.spec.ID)
	mgr := k.box.Inbound()
	if _, ok := mgr.Get(t); ok {
		// 先关掉旧的再建新的：同一端口不能同时被两个监听占用
		if err := mgr.Remove(t); err != nil {
			k.opts.Log.Warn("remove inbound", "node", a.spec.Code, "err", err)
		}
	}
	certs := &certStore{dir: filepath.Join(k.opts.DataDir, "certs")}
	raw, expiry, err := buildInbound(a.spec, users, certs, filepath.Join(k.opts.DataDir, "acme"))
	if err != nil {
		return err
	}
	a.cert = expiry
	var in option.Inbound
	if err := in.UnmarshalJSONContext(k.ctx, raw); err != nil {
		return fmt.Errorf("inbound options: %w", err)
	}
	logger := k.box.LogFactory().NewLogger("inbound/" + in.Type + "[" + t + "]")
	if err := mgr.Create(k.ctx, k.box.Router(), logger, t, in.Type, in.Options); err != nil {
		return err
	}
	if a.spec.Protocol == "shadowsocks" {
		if err := k.updateSS(t, users); err != nil {
			return err
		}
	}
	if usesTCP(a.spec.Protocol) {
		listening := probe(a.spec.Port)
		a.listening = &listening
		if !listening {
			return fmt.Errorf("端口 %d 没有在监听", a.spec.Port)
		}
	}
	return nil
}

// updateSS 原地更新 Shadowsocks 的用户，不重建监听。
func (k *Kernel) updateSS(t string, users []nodeapi.User) error {
	in, ok := k.box.Inbound().Get(t)
	if !ok {
		return errors.New("inbound not found")
	}
	m, ok := in.(adapter.ManagedSSMServer)
	if !ok {
		return errors.New("shadowsocks inbound does not support user updates")
	}
	names := make([]string, 0, len(users))
	keys := make([]string, 0, len(users))
	for _, u := range sortedUsers(users) {
		names = append(names, strconv.FormatInt(u.ID, 10))
		keys = append(keys, u.Password)
	}
	return m.UpdateUsers(names, keys)
}

// SetUsers 更新节点的用户。返回 true 表示为了加入新用户重建了 inbound。
//
// 只是移除用户或改限速时不需要重建：meter 会拒绝已移除用户的新连接并断开现有连接。
// 有新用户（或用户换了凭证）时才需要让内核认识它：Shadowsocks 原地更新，其余协议重建 inbound，
// 重建会断开这个节点上的 UDP / QUIC 会话，所以由调用方合并短时间内的多次新增。
func (k *Kernel) SetUsers(id int64, users []nodeapi.User) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	a, ok := k.nodes[id]
	if !ok {
		return false, fmt.Errorf("node %d is not applied", id)
	}
	if !NeedsRebuild(a.users, users) && a.err == nil {
		return false, nil
	}
	next := map[int64]nodeapi.User{}
	for _, u := range users {
		next[u.ID] = u
	}
	if a.spec.Protocol == "shadowsocks" && a.err == nil {
		if err := k.updateSS(tag(id), users); err != nil {
			return false, err
		}
		a.users = next
		return false, nil
	}
	a.users = next
	a.at = time.Now()
	a.err = k.create(a, users)
	return true, a.err
}

// NeedsRebuild 判断内核是否需要知道新的凭证：有新用户，或已有用户换了凭证。
func NeedsRebuild(built map[int64]nodeapi.User, users []nodeapi.User) bool {
	for _, u := range users {
		b, ok := built[u.ID]
		if !ok || b.UUID != u.UUID || b.Password != u.Password {
			return true
		}
	}
	return false
}

// Remove 停掉节点。
func (k *Kernel) Remove(id int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.nodes, id)
	if _, ok := k.box.Inbound().Get(tag(id)); ok {
		_ = k.box.Inbound().Remove(tag(id))
	}
}

// Retry 重新启动上次启动失败的节点，例如端口当时被占用。
func (k *Kernel) Retry() {
	k.mu.Lock()
	var failed []*applied
	for _, a := range k.nodes {
		if a.err != nil {
			failed = append(failed, a)
		}
	}
	k.mu.Unlock()
	for _, a := range failed {
		users := make([]nodeapi.User, 0, len(a.users))
		for _, u := range a.users {
			users = append(users, u)
		}
		_ = k.Apply(a.spec, users)
	}
}

// Status 返回节点在内核里的运行状态。
func (k *Kernel) Status(id int64) *nodeapi.NodeStatus {
	k.mu.Lock()
	a, ok := k.nodes[id]
	k.mu.Unlock()
	if !ok {
		return nil
	}
	st := &nodeapi.NodeStatus{Running: a.err == nil, Users: len(a.users), AppliedAt: a.at, CertExpiresAt: a.cert, Listening: a.listening}
	if a.err != nil {
		st.Error = a.err.Error()
		return st
	}
	if k.opts.Meter != nil {
		st.Connections = k.opts.Meter.Connections(id)
	}
	st.Listening = a.listening
	return st
}

// Nodes 返回已应用的节点 ID。
func (k *Kernel) Nodes() []int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	ids := make([]int64, 0, len(k.nodes))
	for id := range k.nodes {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func usesTCP(protocol string) bool {
	switch protocol {
	case "hysteria2", "tuic":
		return false
	}
	return true
}

// probe 从本机连一次端口，确认监听确实在。
func probe(port int) bool {
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), time.Second)
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

func sortedUsers(users []nodeapi.User) []nodeapi.User {
	out := slices.Clone(users)
	slices.SortFunc(out, func(a, b nodeapi.User) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}
