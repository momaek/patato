package ops

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/relay"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- route.update 修改已有入口的线路的跳 -----------------------------------
//
// 线路上已有入口时，增删跳就地改每个入口的转发，不重建入口：
//   - 保留下来的跳沿用原来的转发（端口、流量计数、patato-node 上的规则都不变），只在下一跳变化时改目标；
//   - 新增的跳分配端口：新的第一跳优先用订阅里的端口，中间跳优先与相邻跳同号，都不行时取段内最小的空闲端口；
//   - 删除的跳删掉它的转发，上一跳改为直接转到它的下一跳。
//
// 保留下来的机器必须保持原来的先后顺序；要调换顺序，新建一条线路。
// 第一跳换了机器时，入口覆盖的订阅地址和端口（为第一跳前面那层自管转发而设）失效，一并清空，订阅改用新第一跳。

// rehopForward 是改跳后入口在一跳上的转发。ForwardID 非零表示沿用原来的转发。
type rehopForward struct {
	ForwardID  int64  `json:"forward_id,omitempty"`
	MachineID  int64  `json:"machine_id"`
	Machine    string `json:"machine"`
	ListenPort int    `json:"listen_port"`
	PublicPort int    `json:"public_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Mode       string `json:"mode"`
}

// rehopEntry 是一个入口的改跳计划。
type rehopEntry struct {
	EntryID       int64          `json:"entry_id"`
	NodeID        int64          `json:"node_id"`
	Forwards      []rehopForward `json:"forwards"`
	Delete        []int64        `json:"delete,omitempty"`
	ClearOverride bool           `json:"clear_override,omitempty"`
}

// routeUpdateResolved 是 route.update 预演后固定下来的执行参数。Hops 为空表示不改跳。
type routeUpdateResolved struct {
	RouteID int64        `json:"route_id"`
	Name    string       `json:"name"`
	Hops    []int64      `json:"hops,omitempty"`
	Entries []rehopEntry `json:"entries,omitempty"`
}

// checkKeptOrder 要求两份跳列表里共同的机器先后顺序一致。
func checkKeptOrder(r db.Route, before, after []db.Machine) error {
	in := func(ms []db.Machine) map[int64]bool {
		out := map[int64]bool{}
		for _, m := range ms {
			out[m.ID] = true
		}
		return out
	}
	inBefore, inAfter := in(before), in(after)
	var a, b []int64
	for _, m := range before {
		if inAfter[m.ID] {
			a = append(a, m.ID)
		}
	}
	for _, m := range after {
		if inBefore[m.ID] {
			b = append(b, m.ID)
		}
	}
	if !slices.Equal(a, b) {
		return apierr.Conflict("route_hops_reordered", "线路 %s 上有入口，保留的机器不能调换顺序", r.Name).
			WithHint("只能增加或删除跳；要调换顺序，新建一条线路、给节点开新入口后再删除旧入口")
	}
	return nil
}

// planRehop 为线路上的每个入口规划改跳后的转发，返回执行计划和给人看的步骤。
func planRehop(ctx context.Context, q *db.Queries, r db.Route, before, after []db.Machine, entries []db.NodeEntry) ([]rehopEntry, []string, []int64, error) {
	if err := checkKeptOrder(r, before, after); err != nil {
		return nil, nil, nil, err
	}
	keptIDs := map[int64]bool{}
	for _, m := range before {
		keptIDs[m.ID] = true
	}
	for _, m := range after {
		if m.ID == 0 {
			return nil, nil, nil, apierr.Conflict("machine_not_enrolled", "机器 %s 还没有接入 patato-node，线路上已有入口时不能加进来", m.Host).
				WithHint("先用 patato machine install-cmd %s 接入", m.Host)
		}
		if !keptIDs[m.ID] && m.TokenHash == nil {
			return nil, nil, nil, apierr.Conflict("machine_not_enrolled", "机器 %s 还没有接入 patato-node，加进来的转发无法生效", m.Host).
				WithHint("先用 patato machine install-cmd %s 接入", m.Host)
		}
	}
	// 后面还有跳的机器会被上一跳当作下一跳；没有对外地址也没有 tolato 上报的 IP 时只剩机器名，转发会因解析失败撤下入口
	for i := 1; i < len(after); i++ {
		m := after[i]
		if strings.TrimSpace(m.PublicAddress) == "" && strings.TrimSpace(m.TolatoIp) == "" {
			return nil, nil, nil, apierr.Conflict("machine_no_address", "机器 %s 没有对外地址，%s 无法转发到它", m.Host, after[i-1].Host).
				WithHint("先用 patato machine set %s --public-address <IP 或域名> 登记", m.Host)
		}
	}
	occ := newOccupancy()
	var out []rehopEntry
	var steps []string
	var subChanged []int64
	for _, e := range entries {
		n, err := q.GetNode(ctx, e.NodeID)
		if err != nil {
			return nil, nil, nil, err
		}
		fs, err := q.ListEntryForwards(ctx, e.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		label := fmt.Sprintf("%s 的入口 %d", n.Name, e.ID)
		if len(fs) != len(before) {
			return nil, nil, nil, apierr.Conflict("entry_forwards_mismatch", "%s 有 %d 条转发，与线路的 %d 跳对不上", label, len(fs), len(before)).
				WithHint("删除这个入口后重新添加")
		}
		byMachine := map[int64]db.Forward{}
		for i, f := range fs {
			if f.Mode == "manual" {
				return nil, nil, nil, apierr.Conflict("entry_has_manual_forwards", "%s 是手写转发，不能就地改跳", label).
					WithHint("新建一条线路、给节点开托管入口后再删除这个入口")
			}
			if f.MachineID != before[i].ID {
				return nil, nil, nil, apierr.Conflict("entry_forwards_mismatch", "%s 第 %d 跳的机器与线路对不上", label, i+1).
					WithHint("删除这个入口后重新添加")
			}
			byMachine[f.MachineID] = f
		}
		subPort := int(fs[0].PublicPort)
		if e.Port > 0 {
			subPort = int(e.Port)
		}
		hostBefore := firstNonEmpty(e.Address, relay.Address(before[0]))

		ports := make([]relay.HopPorts, len(after))
		kept := make([]bool, len(after))
		for i, m := range after {
			if f, ok := byMachine[m.ID]; ok {
				kept[i] = true
				ports[i] = relay.HopPorts{MachineID: m.ID, Listen: int(f.ListenPort), Public: relay.PublicPort(m, int(f.ListenPort))}
			}
		}
		for i, m := range after {
			if kept[i] {
				continue
			}
			var prefer []int
			if i == 0 {
				prefer = append(prefer, relay.LocalPort(m, subPort))
			}
			if i+1 < len(after) && ports[i+1].Listen > 0 {
				prefer = append(prefer, ports[i+1].Listen)
			}
			if i > 0 && ports[i-1].Listen > 0 {
				prefer = append(prefer, ports[i-1].Listen)
			}
			oc, err := occ.of(ctx, q, m)
			if err != nil {
				return nil, nil, nil, err
			}
			if ports[i], err = relay.AllocateOne(relay.Hop{Machine: m, Occupied: oc}, prefer); err != nil {
				return nil, nil, nil, err
			}
		}
		var landing *db.Machine
		if m, err := q.GetMachineByHost(ctx, n.Host); err == nil {
			landing = &m
		}
		targets := relay.Targets(n, landing, after, ports)

		re := rehopEntry{EntryID: e.ID, NodeID: n.ID}
		parts := make([]string, len(after))
		for i, m := range after {
			rf := rehopForward{MachineID: m.ID, Machine: m.Host, ListenPort: ports[i].Listen, PublicPort: ports[i].Public,
				TargetHost: targets[i].Host, TargetPort: targets[i].Port, Mode: forwardModeOf(m)}
			note := "新增"
			if kept[i] {
				f := byMachine[m.ID]
				rf.ForwardID, rf.Mode = f.ID, f.Mode
				note = "不变"
				if f.TargetHost != rf.TargetHost || int(f.TargetPort) != rf.TargetPort {
					note = fmt.Sprintf("下一跳 %s:%d → %s:%d", f.TargetHost, f.TargetPort, rf.TargetHost, rf.TargetPort)
				}
			}
			re.Forwards = append(re.Forwards, rf)
			parts[i] = fmt.Sprintf("%s:%s → %s:%d（%s）", m.Host, portText(rf.ListenPort, rf.PublicPort), rf.TargetHost, rf.TargetPort, note)
		}
		inAfter := map[int64]bool{}
		for _, m := range after {
			inAfter[m.ID] = true
		}
		for _, f := range fs {
			if !inAfter[f.MachineID] {
				re.Delete = append(re.Delete, f.ID)
				parts = append(parts, fmt.Sprintf("删除 %s:%d 的转发", before[f.Hop].Host, f.ListenPort))
			}
		}
		addr, port := e.Address, int(e.Port)
		if after[0].ID != before[0].ID && (e.Address != "" || e.Port > 0) {
			re.ClearOverride, addr, port = true, "", 0
		}
		hostAfter := firstNonEmpty(addr, relay.Address(after[0]))
		portAfter := ports[0].Public
		if port > 0 {
			portAfter = port
		}
		sub := fmt.Sprintf("订阅地址 %s:%d 不变", hostAfter, portAfter)
		if hostAfter != hostBefore || portAfter != subPort {
			sub = fmt.Sprintf("订阅地址 %s:%d → %s:%d", hostBefore, subPort, hostAfter, portAfter)
			if e.Enabled {
				subChanged = append(subChanged, e.ID)
			}
		}
		if re.ClearOverride {
			sub += "，清除入口上覆盖的地址和端口"
		}
		steps = append(steps, fmt.Sprintf("%s：%s；%s", label, strings.Join(parts, "；"), sub))
		out = append(out, re)
	}
	return out, steps, subChanged, nil
}

func portText(listen, public int) string {
	if listen == public {
		return strconv.Itoa(listen)
	}
	return fmt.Sprintf("%d（外网 %d）", listen, public)
}

// applyRehop 按预演结果改入口的转发：删掉去掉的跳，保留的跳换序号，新增的跳在事务里重新校验端口后创建。
// 下一跳最后按当前机器属性统一重算，预演之后机器地址变了也不会下发过时的目标。
func applyRehop(ctx context.Context, q *db.Queries, entries []rehopEntry) error {
	occ := newOccupancy()
	nodes := map[int64]bool{}
	for _, re := range entries {
		fs, err := q.ListEntryForwards(ctx, re.EntryID)
		if err != nil {
			return err
		}
		own := map[int64]bool{}
		for _, f := range fs {
			own[f.ID] = true
		}
		for _, id := range re.Delete {
			if !own[id] {
				return apierr.Conflict("stale_plan", "入口 %d 的转发在预演后变了", re.EntryID).WithHint("重新预演一次")
			}
			if err := q.DeleteForward(ctx, id); err != nil {
				return err
			}
		}
		// 先把保留的转发挪到不冲突的临时序号，再放到最终位置，避开 (entry_id, hop) 唯一约束
		for i, f := range re.Forwards {
			if f.ForwardID == 0 {
				continue
			}
			if !own[f.ForwardID] {
				return apierr.Conflict("stale_plan", "入口 %d 的转发在预演后变了", re.EntryID).WithHint("重新预演一次")
			}
			if err := q.SetForwardHop(ctx, db.SetForwardHopParams{ID: f.ForwardID, Hop: int32(100 + i)}); err != nil {
				return err
			}
		}
		for i, f := range re.Forwards {
			if f.ForwardID == 0 {
				continue
			}
			if err := q.SetForwardHop(ctx, db.SetForwardHopParams{ID: f.ForwardID, Hop: int32(i)}); err != nil {
				return err
			}
		}
		for i, f := range re.Forwards {
			if f.ForwardID != 0 {
				continue
			}
			m, err := q.GetMachine(ctx, f.MachineID)
			if err != nil {
				return err
			}
			oc, err := occ.of(ctx, q, m)
			if err != nil {
				return err
			}
			if err := relay.Check(relay.Hop{Machine: m, Occupied: oc}, f.ListenPort); err != nil {
				return err
			}
			oc.Add(f.ListenPort, "本次计划已分配")
			_, err = q.CreateForward(ctx, db.CreateForwardParams{
				EntryID: re.EntryID, Hop: int32(i), MachineID: f.MachineID, ListenPort: int32(f.ListenPort), PublicPort: int32(f.PublicPort),
				TargetHost: f.TargetHost, TargetPort: int32(f.TargetPort), Mode: f.Mode, TargetIp: relay.LiteralIP(f.TargetHost),
			})
			if isUniqueViolation(err) {
				return apierr.Conflict("port_taken", "机器 %s 的端口 %d 在预演后被占用", f.Machine, f.ListenPort).WithHint("重新预演一次")
			}
			if err != nil {
				return err
			}
		}
		if re.ClearOverride {
			if err := q.SetEntryOverride(ctx, db.SetEntryOverrideParams{ID: re.EntryID}); err != nil {
				return err
			}
		}
		nodes[re.NodeID] = true
	}
	for _, id := range keys(nodes) {
		if _, err := relay.RefreshNodeForwards(ctx, q, id); err != nil {
			return err
		}
		if err := q.BumpNodeVersion(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// rehopTargets 是改跳要锁定的入口：预演之后入口被改过（停用、改名、删除）时拒绝执行。
func rehopTargets(entries []db.NodeEntry) []change.Target {
	out := make([]change.Target, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryTarget(e))
	}
	return out
}
