// Package relay 是中转线路的领域逻辑：机器的网络属性（对外地址、端口段、NAT 映射）、端口分配、
// 每跳转发的下一跳、入口可用性和链路发现。这里只做计算，不读写数据库，调用方负责在同一事务里校验和落库。
package relay

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/store/db"
)

// DefaultRange 是机器没有配置端口段时的可分配端口段，与现网手写规则的 60000-62000、63000-63005 不重叠。
var DefaultRange = PortRange{From: 40000, To: 49999}

// PortRange 是一段本机端口。PublicFrom 非零时表示 NAT 映射：外网 PublicFrom 对应本机 From。
type PortRange struct {
	From       int `json:"-"`
	To         int `json:"-"`
	PublicFrom int `json:"-"`
}

type rangeJSON struct {
	Local      string `json:"local"`
	PublicFrom int    `json:"public_from,omitempty"`
}

func (r PortRange) String() string {
	if r.From == r.To {
		return strconv.Itoa(r.From)
	}
	return fmt.Sprintf("%d-%d", r.From, r.To)
}

func (r PortRange) MarshalJSON() ([]byte, error) {
	return json.Marshal(rangeJSON{Local: r.String(), PublicFrom: r.PublicFrom})
}

func (r *PortRange) UnmarshalJSON(b []byte) error {
	var v rangeJSON
	if err := json.Unmarshal(b, &v); err != nil {
		// 也接受只写一个字符串的形式，例如 "40000-49999"
		var s string
		if json.Unmarshal(b, &s) != nil {
			return fmt.Errorf("端口段格式应为 {\"local\": \"40000-49999\", \"public_from\": 30000}")
		}
		v.Local = s
	}
	p, err := ParseRange(v.Local)
	if err != nil {
		return err
	}
	p.PublicFrom = v.PublicFrom
	*r = p
	return r.validate()
}

func (r PortRange) validate() error {
	if r.From < 1 || r.To > 65535 || r.From > r.To {
		return fmt.Errorf("端口段 %s 不合法", r)
	}
	if r.PublicFrom != 0 && (r.PublicFrom < 1 || r.PublicFrom+(r.To-r.From) > 65535) {
		return fmt.Errorf("端口段 %s 的外网起点 %d 超出范围", r, r.PublicFrom)
	}
	return nil
}

// Contains 判断本机端口是否在段内。
func (r PortRange) Contains(p int) bool { return p >= r.From && p <= r.To }

// Public 返回本机端口对应的外网端口。
func (r PortRange) Public(local int) int {
	if r.PublicFrom == 0 {
		return local
	}
	return r.PublicFrom + (local - r.From)
}

// ParseRange 解析 "40000-49999" 或单个端口 "41010"。
func ParseRange(s string) (PortRange, error) {
	s = strings.TrimSpace(s)
	a, b, found := strings.Cut(s, "-")
	from, err1 := strconv.Atoi(strings.TrimSpace(a))
	to := from
	var err2 error
	if found {
		to, err2 = strconv.Atoi(strings.TrimSpace(b))
	}
	if err1 != nil || err2 != nil {
		return PortRange{}, fmt.Errorf("端口段 %q 格式应为 40000-49999", s)
	}
	r := PortRange{From: from, To: to}
	return r, r.validate()
}

// ParseRanges 解析机器的 port_ranges / reserved_ports 列，并检查段之间不重叠。
func ParseRanges(raw json.RawMessage) ([]PortRange, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rs []PortRange
	if err := json.Unmarshal(raw, &rs); err != nil {
		return nil, err
	}
	sorted := slices.Clone(rs)
	slices.SortFunc(sorted, func(a, b PortRange) int { return a.From - b.From })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].From <= sorted[i-1].To {
			return nil, fmt.Errorf("端口段 %s 与 %s 重叠", sorted[i-1], sorted[i])
		}
	}
	return rs, nil
}

// Ranges 返回机器可分配的端口段：配置了就用配置的，否则用默认段（NAT 机器没有默认段）。
func Ranges(m db.Machine) []PortRange {
	rs, err := ParseRanges(m.PortRanges)
	if err != nil || len(rs) == 0 {
		if m.Nat {
			return nil
		}
		return []PortRange{DefaultRange}
	}
	return rs
}

// PublicPort 返回机器上本机端口对应的外网端口；不在任何段内时外网端口等于本机端口。
func PublicPort(m db.Machine, local int) int {
	for _, r := range Ranges(m) {
		if r.Contains(local) {
			return r.Public(local)
		}
	}
	return local
}

// Address 是别的机器和客户端连这台机器用的地址：运营者填写的对外地址，否则 tolato 上报的 IP，最后是机器名。
func Address(m db.Machine) string {
	for _, s := range []string{m.PublicAddress, m.TolatoIp} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return m.Host
}

// Occupied 是一台机器上不能再分配的本机端口。
type Occupied struct {
	ports    map[int]string
	reserved []PortRange
}

func NewOccupied() *Occupied { return &Occupied{ports: map[int]string{}} }

// Add 记录一个被占用的端口及原因。
func (o *Occupied) Add(port int, why string) {
	if _, ok := o.ports[port]; !ok {
		o.ports[port] = why
	}
}

// Reserve 记录一段被现有规则占用的端口（链路发现读到的手写 DNAT 段等）。
func (o *Occupied) Reserve(r PortRange) { o.reserved = append(o.reserved, r) }

// Why 返回端口被占用的原因；空串表示空闲。
func (o *Occupied) Why(port int) string {
	if w, ok := o.ports[port]; ok {
		return w
	}
	for _, r := range o.reserved {
		if r.Contains(port) {
			return "机器上现有规则占用的端口段 " + r.String()
		}
	}
	return ""
}

// HopPorts 是一跳上分配到的端口。
type HopPorts struct {
	MachineID int64 `json:"machine_id"`
	Listen    int   `json:"listen_port"`
	Public    int   `json:"public_port"`
}

// Hop 是参与分配的一台机器及其已占用端口。同一台机器在一条线路里出现多次时共享同一个 Occupied。
type Hop struct {
	Machine  db.Machine
	Occupied *Occupied
}

// Allocate 为一个入口的每一跳分配端口：优先找一个在所有跳上都空闲的同号端口，找不到再各跳分别分配。
// 分配到的端口会记进各跳的 Occupied，方便同一个计划里继续分配下一个入口。
func Allocate(hops []Hop) ([]HopPorts, error) {
	if len(hops) == 0 {
		return nil, nil
	}
	free := func(h Hop, p int) bool {
		if h.Occupied.Why(p) != "" {
			return false
		}
		for _, r := range Ranges(h.Machine) {
			if r.Contains(p) {
				return true
			}
		}
		return false
	}
	out := make([]HopPorts, len(hops))
	// 同号：在第一跳的段里从小到大找，所有跳都空闲（同一台机器出现多次时同号不可能，直接跳过）
	distinct := true
	seen := map[int64]bool{}
	for _, h := range hops {
		if seen[h.Machine.ID] {
			distinct = false
		}
		seen[h.Machine.ID] = true
	}
	if distinct {
		for _, r := range Ranges(hops[0].Machine) {
			for p := r.From; p <= r.To; p++ {
				ok := true
				for _, h := range hops {
					if !free(h, p) {
						ok = false
						break
					}
				}
				if ok {
					for i, h := range hops {
						out[i] = HopPorts{MachineID: h.Machine.ID, Listen: p, Public: PublicPort(h.Machine, p)}
						h.Occupied.Add(p, "本次计划已分配")
					}
					return out, nil
				}
			}
		}
	}
	for i, h := range hops {
		found := 0
		for _, r := range Ranges(h.Machine) {
			for p := r.From; p <= r.To && found == 0; p++ {
				if free(h, p) {
					found = p
				}
			}
		}
		if found == 0 {
			return nil, Exhausted(h.Machine)
		}
		out[i] = HopPorts{MachineID: h.Machine.ID, Listen: found, Public: PublicPort(h.Machine, found)}
		h.Occupied.Add(found, "本次计划已分配")
	}
	return out, nil
}

// AllocateOne 在一台机器上分配一个端口：依次尝试 prefer 里的本机端口（在端口段内且空闲才用），都不行时取段内最小的空闲端口。
// 给已有入口的线路插入一跳时用它，让新的一跳沿用相邻跳或订阅里的端口号。
func AllocateOne(h Hop, prefer []int) (HopPorts, error) {
	inRange := func(p int) bool {
		for _, r := range Ranges(h.Machine) {
			if r.Contains(p) {
				return true
			}
		}
		return false
	}
	pick := func(p int) HopPorts {
		h.Occupied.Add(p, "本次计划已分配")
		return HopPorts{MachineID: h.Machine.ID, Listen: p, Public: PublicPort(h.Machine, p)}
	}
	for _, p := range prefer {
		if p > 0 && inRange(p) && h.Occupied.Why(p) == "" {
			return pick(p), nil
		}
	}
	for _, r := range Ranges(h.Machine) {
		for p := r.From; p <= r.To; p++ {
			if h.Occupied.Why(p) == "" {
				return pick(p), nil
			}
		}
	}
	return HopPorts{}, Exhausted(h.Machine)
}

// LocalPort 返回外网端口在这台机器上对应的本机端口；不在任何 NAT 段内时本机端口等于外网端口。
func LocalPort(m db.Machine, public int) int {
	for _, r := range Ranges(m) {
		if r.PublicFrom != 0 && public >= r.PublicFrom && public <= r.PublicFrom+(r.To-r.From) {
			return r.From + (public - r.PublicFrom)
		}
	}
	return public
}

// Exhausted 是端口段用尽的错误，指出哪台机器、哪几段。
func Exhausted(m db.Machine) error {
	rs := Ranges(m)
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.String())
	}
	if len(names) == 0 {
		return apierr.Conflict("port_exhausted", "NAT 机器 %s 没有配置可分配的端口段", m.Host).
			WithHint("用 patato machine set %s --port-range <本机段>[:外网起点] 填写商家映射的端口段", m.Host)
	}
	return apierr.Conflict("port_exhausted", "机器 %s 的端口段 %s 已用尽", m.Host, strings.Join(names, ", ")).
		WithHint("用 patato machine set %s --port-range 增加端口段，或删除不用的入口", m.Host)
}

// Check 校验一个已分配的本机端口仍然可用：在端口段内且没有被占用。执行阶段在事务里调用，
// 预演之后被别的变更或节点占用时返回 port_taken。
func Check(h Hop, port int) error {
	if why := h.Occupied.Why(port); why != "" {
		return apierr.Conflict("port_taken", "机器 %s 的端口 %d 在预演后被占用（%s）", h.Machine.Host, port, why).
			WithHint("重新预演一次，服务端会换一个空闲端口")
	}
	return nil
}
