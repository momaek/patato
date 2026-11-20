package relay

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/momaek/patato/nodeapi"
)

// TableName 是 patato-node 独占维护的 nftables 表（family inet）。patato-node 只增删这张表，
// 不读写其他表和 iptables 规则，也不修改 sysctl。
const TableName = "patato"

// 规则集的中间表示：先按完整转发列表生成，再由平台相关的代码翻译成 netlink 表达式。
// 中间表示与平台无关，单元测试逐条对比它和 nft 语法的渲染结果。

// Chain 是表里的一条链。
type Chain string

const (
	ChainPre  Chain = "pre"  // type nat hook prerouting priority dstnat
	ChainPost Chain = "post" // type nat hook postrouting priority srcnat
	ChainFwd  Chain = "fwd"  // type filter hook forward priority filter
)

// Rule 是一条规则。字段按 nft 语法的顺序渲染。
type Rule struct {
	Chain Chain
	// 匹配
	IPv4      bool   // meta nfproto ipv4
	Proto     string // tcp / udp
	DPort     int    // th dport
	CtMark    uint32 // ct mark <n>
	Direction string // ct direction original / reply
	// 动作
	SetMark    uint32         // ct mark set <n>
	DNAT       netip.AddrPort // dnat ip to <addr:port>
	Masquerade bool
	Counter    string // counter name <name>
}

// Ruleset 是一次原子替换的完整内容。
type Ruleset struct {
	Counters []string
	Rules    []Rule
}

func counterUp(id int64) string   { return "f" + strconv.FormatInt(id, 10) + "_up" }
func counterDown(id int64) string { return "f" + strconv.FormatInt(id, 10) + "_down" }

// Mark 是转发在 conntrack 上的标记，即转发 ID。机器上其他程序也在用 ct mark 时需要约定取值范围（见 docs/relay.md 待定）。
func Mark(id int64) uint32 { return uint32(id) }

// Build 按完整转发列表生成规则集。每条转发：
//
//	pre:  meta nfproto ipv4 tcp dport P ct mark set ID dnat ip to T（udp 同样一条）
//	post: ct mark ID masquerade
//	fwd:  ct mark ID ct direction original counter name fID_up
//	      ct mark ID ct direction reply counter name fID_down
//
// 流量计数放在 forward 链：nat 链只处理每个连接的首包，在那里计数得到的是连接数 × 首包大小。
// 目标不是 IPv4 的转发不进规则集（返回在 skipped 里），由调用方改用 userspace。
func Build(fs []nodeapi.Forward) (rs Ruleset, skipped map[int64]error) {
	skipped = map[int64]error{}
	sorted := slices.Clone(fs)
	slices.SortFunc(sorted, func(a, b nodeapi.Forward) int { return int(a.ID - b.ID) })
	for _, f := range sorted {
		addr, err := netip.ParseAddr(f.TargetIP)
		if err != nil || !addr.Is4() {
			skipped[f.ID] = fmt.Errorf("nft 模式只支持 IPv4 下一跳（%s）", f.TargetIP)
			continue
		}
		if f.ListenPort < 1 || f.ListenPort > 65535 || f.TargetPort < 1 || f.TargetPort > 65535 {
			skipped[f.ID] = fmt.Errorf("端口不合法")
			continue
		}
		to := netip.AddrPortFrom(addr, uint16(f.TargetPort))
		mark := Mark(f.ID)
		rs.Counters = append(rs.Counters, counterUp(f.ID), counterDown(f.ID))
		for _, proto := range []string{"tcp", "udp"} {
			rs.Rules = append(rs.Rules, Rule{Chain: ChainPre, IPv4: true, Proto: proto, DPort: f.ListenPort, SetMark: mark, DNAT: to})
		}
		rs.Rules = append(rs.Rules,
			Rule{Chain: ChainPost, CtMark: mark, Masquerade: true},
			Rule{Chain: ChainFwd, CtMark: mark, Direction: "original", Counter: counterUp(f.ID)},
			Rule{Chain: ChainFwd, CtMark: mark, Direction: "reply", Counter: counterDown(f.ID)},
		)
	}
	return rs, skipped
}

// String 用 nft 语法渲染一条规则，便于测试对比和日志。
func (r Rule) String() string {
	var b []string
	if r.IPv4 {
		b = append(b, "meta nfproto ipv4")
	}
	if r.Proto != "" {
		b = append(b, r.Proto, "dport", strconv.Itoa(r.DPort))
	}
	if r.CtMark != 0 {
		b = append(b, "ct mark", strconv.FormatUint(uint64(r.CtMark), 10))
	}
	if r.Direction != "" {
		b = append(b, "ct direction", r.Direction)
	}
	if r.SetMark != 0 {
		b = append(b, "ct mark set", strconv.FormatUint(uint64(r.SetMark), 10))
	}
	if r.DNAT.IsValid() {
		b = append(b, "dnat ip to", r.DNAT.String())
	}
	if r.Masquerade {
		b = append(b, "masquerade")
	}
	if r.Counter != "" {
		b = append(b, "counter name", r.Counter)
	}
	return strings.Join(b, " ")
}

// Render 用 nft 语法渲染整张表，与 nft list table inet patato 的结构一致。
func (rs Ruleset) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", TableName)
	for _, c := range rs.Counters {
		fmt.Fprintf(&b, "\tcounter %s {}\n", c)
	}
	heads := []struct {
		c    Chain
		head string
	}{
		{ChainPre, "type nat hook prerouting priority dstnat; policy accept;"},
		{ChainPost, "type nat hook postrouting priority srcnat; policy accept;"},
		{ChainFwd, "type filter hook forward priority filter; policy accept;"},
	}
	for _, h := range heads {
		fmt.Fprintf(&b, "\tchain %s {\n\t\t%s\n", h.c, h.head)
		for _, r := range rs.Rules {
			if r.Chain == h.c {
				fmt.Fprintf(&b, "\t\t%s\n", r)
			}
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}
