package relay

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/momaek/patato/server/internal/store/db"
)

// ProbeFailLimit 是连续多少次 TCP 探测失败后，入口视为不可用、从订阅中撤下。
const ProbeFailLimit = 3

// Kind 是入口的类型。
type Kind string

const (
	KindDirect Kind = "direct" // 节点自己的地址
	KindCustom Kind = "custom" // 只填了地址和端口的入口（例如购买的 IPLC、Xboard 导入的子节点），状态只跟随节点
	KindRelay  Kind = "relay"  // 经过一条线路
)

// EntryKind 判断入口类型。
func EntryKind(e db.NodeEntry) Kind {
	switch {
	case e.RouteID != nil:
		return KindRelay
	case e.Address != "" || e.Port > 0:
		return KindCustom
	}
	return KindDirect
}

// Implicit 是没有任何入口的节点等价的那个启用的直连入口。
func Implicit(n db.Node) db.NodeEntry {
	return db.NodeEntry{NodeID: n.ID, Enabled: true, Sort: n.Sort}
}

// World 是计算入口可用性和订阅地址需要的全部数据，按 ID 索引。
type World struct {
	Routes   map[int64]db.Route
	Machines map[int64]db.Machine
	// MachinesByHost 用于找落地节点所在的机器
	MachinesByHost map[string]db.Machine
	// Forwards 是每个入口的转发，按跳排序
	Forwards map[int64][]db.Forward
}

func NewWorld(routes []db.Route, machines []db.Machine, forwards []db.Forward) *World {
	w := &World{Routes: map[int64]db.Route{}, Machines: map[int64]db.Machine{}, MachinesByHost: map[string]db.Machine{},
		Forwards: map[int64][]db.Forward{}}
	for _, r := range routes {
		w.Routes[r.ID] = r
	}
	for _, m := range machines {
		w.Machines[m.ID] = m
		w.MachinesByHost[m.Host] = m
	}
	for _, f := range forwards {
		w.Forwards[f.EntryID] = append(w.Forwards[f.EntryID], f)
	}
	for id := range w.Forwards {
		slices.SortFunc(w.Forwards[id], func(a, b db.Forward) int { return cmp.Compare(a.Hop, b.Hop) })
	}
	return w
}

// Endpoint 是订阅里一个入口的连接地址和名称。
type Endpoint struct {
	Entry     db.NodeEntry
	Name      string
	Host      string
	Port      int
	Available bool
	// Reason 是不可用的原因，给 Admin 和 CLI 展示
	Reason string
}

// Resolve 计算节点一个入口的订阅地址、名称和可用性。
//
// 入口可用 = 节点在线 ∧ 每一跳机器没有离线 ∧ 每一跳托管转发已生效 ∧ 没有连续 3 次探测失败。
// 手写（manual）转发没有生效状态，只看所在机器是否在线；机器状态未知（没接入 patato-node 也没有 tolato）时不撤下。
func (w *World) Resolve(n db.Node, e db.NodeEntry) Endpoint {
	ep := Endpoint{Entry: e, Name: EntryName(n, e, w.Routes), Host: n.Address, Port: int(n.Port), Available: n.State == "online"}
	if !ep.Available {
		ep.Reason = "节点" + stateLabel(n.State)
	}
	switch EntryKind(e) {
	case KindCustom:
		ep.Host = firstNonEmpty(e.Address, n.Address)
		if e.Port > 0 {
			ep.Port = int(e.Port)
		}
		return ep
	case KindDirect:
		return ep
	}
	route, ok := w.Routes[*e.RouteID]
	fs := w.Forwards[e.ID]
	if !ok || len(fs) != len(route.Hops) {
		ep.Available, ep.Reason = false, "转发还没有生成"
		return ep
	}
	first := w.Machines[fs[0].MachineID]
	ep.Host = firstNonEmpty(e.Address, Address(first))
	ep.Port = int(fs[0].PublicPort)
	if e.Port > 0 {
		ep.Port = int(e.Port)
	}
	if !ep.Available {
		return ep
	}
	for _, f := range fs {
		m := w.Machines[f.MachineID]
		if why := forwardProblem(m, f); why != "" {
			ep.Available, ep.Reason = false, why
			return ep
		}
	}
	return ep
}

func forwardProblem(m db.Machine, f db.Forward) string {
	if m.State == "offline" {
		return "中转机器 " + m.Host + " 离线"
	}
	if f.Mode == "manual" {
		return ""
	}
	switch f.Status {
	case "pending":
		return "机器 " + m.Host + " 上的转发还没有生效"
	case "failed":
		return "机器 " + m.Host + " 上的转发失败：" + f.Error
	}
	if f.ProbeFailures >= ProbeFailLimit {
		return "机器 " + m.Host + " 到下一跳连续 " + itoa(int(f.ProbeFailures)) + " 次探测失败"
	}
	return ""
}

// EntryName 是入口在订阅里的名称：运营者指定的名称；否则「节点名 · 后缀」，中转入口没有后缀时用线路名。
func EntryName(n db.Node, e db.NodeEntry, routes map[int64]db.Route) string {
	if e.Name != "" {
		return e.Name
	}
	suffix := e.Suffix
	if suffix == "" && e.RouteID != nil {
		if r, ok := routes[*e.RouteID]; ok {
			suffix = r.Name
		}
	}
	if suffix == "" {
		return n.Name
	}
	return n.Name + " · " + suffix
}

// Target 是一跳转发的下一跳。
type Target struct {
	Host string
	Port int
}

// Targets 计算入口每一跳的下一跳：中间跳指向下一台中转机器的对外地址和它的外网端口；
// 最后一跳指向落地节点所在机器的对外地址和节点的对外端口（NAT 落地的对外端口由商家映射到监听端口）。
// ports 是每跳已分配的端口，与 route.Hops 等长。
func Targets(n db.Node, landing *db.Machine, hops []db.Machine, ports []HopPorts) []Target {
	out := make([]Target, len(hops))
	for i := range hops {
		if i+1 < len(hops) {
			out[i] = Target{Host: Address(hops[i+1]), Port: ports[i+1].Public}
			continue
		}
		out[i] = Target{Host: LandingAddress(n, landing), Port: int(n.Port)}
	}
	return out
}

// LandingAddress 是最后一跳连落地节点用的地址：落地机器的对外地址或 tolato 上报的 IP，都没有时用节点地址。
func LandingAddress(n db.Node, landing *db.Machine) string {
	if landing != nil {
		if s := firstNonEmpty(landing.PublicAddress, landing.TolatoIp); s != "" {
			return s
		}
	}
	return n.Address
}

// SortEndpoints 按入口的排序值稳定排序，Xboard 导入的子节点与父节点交错时保持 Xboard 的顺序。
func SortEndpoints(eps []Endpoint) {
	slices.SortStableFunc(eps, func(a, b Endpoint) int { return cmp.Compare(a.Entry.Sort, b.Entry.Sort) })
}

func stateLabel(s string) string {
	switch s {
	case "offline":
		return "离线"
	case "pending":
		return "待接入"
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func itoa(i int) string { return strconv.Itoa(i) }
