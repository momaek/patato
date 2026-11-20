package relay

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// 链路发现：从入口的第一跳出发，读取每台机器的 DNAT 规则（iptables / nftables）和 socat 进程，逐跳追到落地节点。
//
// Patato 不调用 tolato 的远程执行（见设计文档「与 tolato 的集成」）。采集命令由 Patato 生成（DiscoverCommand），
// 只读，由 Agent 经 tolato exec 在每台机器上执行，把输出交回 Patato 解析。结果交给运营者确认后登记为 manual 转发。

// DiscoverCommand 是在机器上采集转发现状的只读命令：本机地址、nat 表、nftables 规则集、socat 进程。
// 每段输出前有一行 ### <段名>，便于解析；任何一步失败都不影响其他段。
const DiscoverCommand = `echo '### addr'; ip -o addr show 2>/dev/null | awk '{print $4}'; ` +
	`echo '### iptables'; (iptables-save -t nat 2>/dev/null || true); ` +
	`echo '### nft'; (nft list ruleset 2>/dev/null || true); ` +
	`echo '### socat'; (ps -eo args 2>/dev/null | grep '^socat' || true)`

// Rule 是机器上的一条转发：本机端口段（DNAT 时可能是一段）转到下一跳。TargetPort 为 0 表示端口不变（整段 DNAT）。
type Rule struct {
	Source     string `json:"source"` // iptables / nft / socat
	From       int    `json:"from"`
	To         int    `json:"to"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Line       string `json:"line"`
}

func (r Rule) match(port int) bool { return port >= r.From && port <= r.To }

func (r Rule) target(port int) int {
	if r.TargetPort == 0 {
		return port
	}
	if r.To > r.From {
		// 整段映射到另一段起点
		return r.TargetPort + (port - r.From)
	}
	return r.TargetPort
}

// Snapshot 是一台机器的转发现状。
type Snapshot struct {
	Machine string   `json:"machine"`
	Addrs   []string `json:"addrs"`
	Rules   []Rule   `json:"rules"`
}

var (
	reDport   = regexp.MustCompile(`--dport\s+(\d+)(?::(\d+))?`)
	reToDest  = regexp.MustCompile(`--to-destination\s+([0-9a-fA-F.:\[\]]+?)(?::(\d+)(?:-\d+)?)?(?:\s|$)`)
	reNftPort = regexp.MustCompile(`dport\s+(?:\{\s*)?(\d+)(?:-(\d+))?`)
	reNftDnat = regexp.MustCompile(`dnat\s+(?:ip\s+)?to\s+([0-9.]+)(?::(\d+))?`)
	reSocatIn = regexp.MustCompile(`(?i)(?:TCP|UDP)[46]?-LISTEN:(\d+)`)
	reSocatTo = regexp.MustCompile(`(?i)(?:TCP|UDP)[46]?:([^:,\s]+):(\d+)`)
)

// ParseSnapshot 解析 DiscoverCommand 的输出。
func ParseSnapshot(machine, out string) Snapshot {
	snap := Snapshot{Machine: machine}
	section := ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "### ") {
			section = strings.TrimPrefix(line, "### ")
			continue
		}
		if line == "" {
			continue
		}
		switch section {
		case "addr":
			if ip, _, err := net.ParseCIDR(line); err == nil {
				snap.Addrs = append(snap.Addrs, ip.String())
			}
		case "iptables":
			if r, ok := parseIptables(line); ok {
				snap.Rules = append(snap.Rules, r)
			}
		case "nft":
			if r, ok := parseNft(line); ok {
				snap.Rules = append(snap.Rules, r)
			}
		case "socat":
			if r, ok := parseSocat(line); ok {
				snap.Rules = append(snap.Rules, r)
			}
		}
	}
	return snap
}

func parseIptables(line string) (Rule, bool) {
	if !strings.HasPrefix(line, "-A ") || !strings.Contains(line, "DNAT") {
		return Rule{}, false
	}
	p := reDport.FindStringSubmatch(line)
	d := reToDest.FindStringSubmatch(line)
	if p == nil || d == nil {
		return Rule{}, false
	}
	from, _ := strconv.Atoi(p[1])
	to := from
	if p[2] != "" {
		to, _ = strconv.Atoi(p[2])
	}
	tp, _ := strconv.Atoi(d[2])
	return Rule{Source: "iptables", From: from, To: to, TargetHost: strings.Trim(d[1], "[]"), TargetPort: tp, Line: line}, true
}

func parseNft(line string) (Rule, bool) {
	if !strings.Contains(line, "dnat") {
		return Rule{}, false
	}
	p := reNftPort.FindStringSubmatch(line)
	d := reNftDnat.FindStringSubmatch(line)
	if p == nil || d == nil {
		return Rule{}, false
	}
	from, _ := strconv.Atoi(p[1])
	to := from
	if p[2] != "" {
		to, _ = strconv.Atoi(p[2])
	}
	tp, _ := strconv.Atoi(d[2])
	return Rule{Source: "nft", From: from, To: to, TargetHost: d[1], TargetPort: tp, Line: line}, true
}

func parseSocat(line string) (Rule, bool) {
	in := reSocatIn.FindStringSubmatch(line)
	if in == nil {
		return Rule{}, false
	}
	rest := line[strings.Index(line, in[0])+len(in[0]):]
	to := reSocatTo.FindStringSubmatch(rest)
	if to == nil {
		return Rule{}, false
	}
	port, _ := strconv.Atoi(in[1])
	tp, _ := strconv.Atoi(to[2])
	return Rule{Source: "socat", From: port, To: port, TargetHost: to[1], TargetPort: tp, Line: line}, true
}

// Landing 是链路发现的终点：落地节点的地址（可能有多个：节点地址、机器 IP）和端口（对外端口、监听端口）。
type Landing struct {
	Addrs []string
	Ports []int
}

// Hop 是追到的一跳。
type DiscoveredHop struct {
	Machine    string `json:"machine"`
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Rule       Rule   `json:"rule"`
}

// Trace 是一次链路发现的结果。Complete 为 true 表示追到了落地节点；否则 Problem 说明卡在哪里。
type Trace struct {
	Hops     []DiscoveredHop `json:"hops"`
	Complete bool            `json:"complete"`
	Problem  string          `json:"problem,omitempty"`
	// Reserved 是沿途机器上整段 DNAT 占用的端口段，分配新端口时要避开。
	Reserved map[string][]PortRange `json:"reserved"`
}

// HostResolver 把域名解析成 IP，用于匹配机器地址。
type HostResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// TraceRoute 从 start（入口地址:端口）出发，按 snapshots 逐跳追踪。machineAddrs 是 Patato 已知的机器地址
// （对外地址、tolato IP），与快照里的本机地址合并后用于判断下一跳是哪台机器。
func TraceRoute(ctx context.Context, r HostResolver, startHost string, startPort int, snapshots []Snapshot,
	machineAddrs map[string][]string, landing Landing) Trace {
	t := Trace{Reserved: map[string][]PortRange{}}
	owner := map[string]string{}
	for m, addrs := range machineAddrs {
		for _, a := range addrs {
			owner[a] = m
		}
	}
	byMachine := map[string]Snapshot{}
	for _, s := range snapshots {
		byMachine[s.Machine] = s
		owner[s.Machine] = s.Machine
		for _, a := range s.Addrs {
			owner[a] = s.Machine
		}
		for _, rule := range s.Rules {
			if rule.To > rule.From {
				t.Reserved[s.Machine] = append(t.Reserved[s.Machine], PortRange{From: rule.From, To: rule.To})
			}
		}
	}
	isLanding := func(host string, port int) bool {
		hostIPs := resolveAll(ctx, r, host)
		for _, a := range landing.Addrs {
			for _, la := range resolveAll(ctx, r, a) {
				for _, h := range hostIPs {
					if la == h {
						for _, p := range landing.Ports {
							if p == port {
								return true
							}
						}
					}
				}
			}
		}
		return false
	}
	whose := func(host string) string {
		if m, ok := owner[host]; ok {
			return m
		}
		for _, ip := range resolveAll(ctx, r, host) {
			if m, ok := owner[ip]; ok {
				return m
			}
		}
		return ""
	}
	host, port := startHost, startPort
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		if isLanding(host, port) {
			t.Complete = true
			return t
		}
		m := whose(host)
		if m == "" {
			t.Problem = fmt.Sprintf("%s:%d 不是任何已采集的机器，也不是落地节点", host, port)
			return t
		}
		key := fmt.Sprintf("%s:%d", m, port)
		if seen[key] {
			t.Problem = fmt.Sprintf("机器 %s 端口 %d 出现循环", m, port)
			return t
		}
		seen[key] = true
		snap, ok := byMachine[m]
		if !ok {
			t.Problem = fmt.Sprintf("机器 %s 没有采集快照", m)
			return t
		}
		var hit *Rule
		for j := range snap.Rules {
			if snap.Rules[j].match(port) {
				// 单端口规则优先于整段 DNAT
				if hit == nil || (hit.To > hit.From && snap.Rules[j].To == snap.Rules[j].From) {
					hit = &snap.Rules[j]
				}
			}
		}
		if hit == nil {
			t.Problem = fmt.Sprintf("机器 %s 上没有找到端口 %d 的转发规则", m, port)
			return t
		}
		next := hit.target(port)
		t.Hops = append(t.Hops, DiscoveredHop{Machine: m, ListenPort: port, TargetHost: hit.TargetHost, TargetPort: next, Rule: *hit})
		host, port = hit.TargetHost, next
	}
	t.Problem = "超过 8 跳仍未到达落地节点"
	return t
}

func resolveAll(ctx context.Context, r HostResolver, host string) []string {
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}
	}
	if r == nil {
		return []string{host}
	}
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return []string{host}
	}
	return append(addrs, host)
}
