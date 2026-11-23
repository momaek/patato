package ops

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/relay"
	"github.com/momaek/patato/server/internal/store/db"
)

// ---- 入口组 -----------------------------------------------------------------
//
// 线路的第一跳可以由多台机器共同承担：入口组成员（entry_peers）和第一跳共用一个对外域名（entry_address），
// 域名解析到哪台都能连上。每个经过这条线路的入口，在每个成员上有一条镜像转发：与第一跳的转发对外端口相同、
// 转到同一个下一跳。订阅里只有一条入口，地址是 entry_address。设计见 docs/relay.md「入口组」。

// resolvePeers 把入口组成员的机器名换成机器。成员不能与线路的跳重复；还没登记的机器在执行时登记（create 为 true）。
func resolvePeers(ctx context.Context, q *db.Queries, names []string, hops []db.Machine, create bool) ([]db.Machine, error) {
	inHops := map[string]bool{}
	for _, m := range hops {
		inHops[m.Host] = true
	}
	out := []db.Machine{}
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if seen[n] {
			return nil, apierr.BadRequest("duplicate_entry_peer", "入口组里机器 %s 出现了两次", n)
		}
		seen[n] = true
		m, err := findMachineRef(ctx, q, n, create)
		if !create && isCode(err, "machine_not_found") {
			m, err = db.Machine{Host: n, PortRanges: []byte("[]"), ReservedPorts: []byte("[]"), State: "unknown"}, nil
		}
		if err != nil {
			return nil, err
		}
		if inHops[m.Host] {
			return nil, apierr.BadRequest("entry_peer_in_hops", "机器 %s 已经是线路的一跳，不能同时是入口组成员", m.Host).
				WithHint("入口组成员是和第一跳并列的其他入口机器")
		}
		out = append(out, m)
	}
	if len(out) > 8 {
		return nil, apierr.BadRequest("too_many_entry_peers", "入口组最多 8 台机器")
	}
	return out, nil
}

// checkEntryAddress 校验入口地址：有入口组成员时必须填写，必须是域名或 IP。
func checkEntryAddress(addr string, peers int) error {
	if peers > 0 && addr == "" {
		return apierr.BadRequest("entry_address_required", "有入口组成员时必须填写入口地址").
			WithHint("填一个解析到入口组机器的域名，例如 --entry-address entry.example.com；订阅里用这个地址")
	}
	if strings.ContainsAny(addr, " /:") && net.ParseIP(addr) == nil {
		return apierr.BadRequest("invalid_entry_address", "入口地址 %q 应为域名或 IP", addr)
	}
	return nil
}

func routePeers(ctx context.Context, q *db.Queries, r db.Route) ([]db.Machine, error) {
	if len(r.EntryPeers) == 0 {
		return nil, nil
	}
	ms, err := q.ListMachinesByIDs(ctx, r.EntryPeers)
	if err != nil {
		return nil, err
	}
	return orderMachines(ms, r.EntryPeers), nil
}

// planMirrors 为一个新入口在入口组成员上规划镜像转发。ports 是 AllocateGroup 给成员分配的端口，target 是第一跳的下一跳。
func planMirrors(peers []db.Machine, ports []relay.HopPorts, first plannedForward) []plannedForward {
	out := make([]plannedForward, len(peers))
	for i, m := range peers {
		out[i] = plannedForward{Hop: 0, MachineID: m.ID, Machine: m.Host, ListenPort: ports[i].Listen, PublicPort: ports[i].Public,
			TargetHost: first.TargetHost, TargetPort: first.TargetPort, Mode: forwardModeOf(m), Mirror: true}
	}
	return out
}

// groupMirror 是修改入口组时给已有入口新增的一条镜像转发。
type groupMirror struct {
	EntryID    int64  `json:"entry_id"`
	NodeID     int64  `json:"node_id"`
	MachineID  int64  `json:"machine_id"`
	Machine    string `json:"machine"`
	ListenPort int    `json:"listen_port"`
	PublicPort int    `json:"public_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Mode       string `json:"mode"`
}

// groupResolved 是 route.update 修改入口组时预演固定下来的执行参数。
type groupResolved struct {
	Peers   []int64       `json:"peers"`
	Address string        `json:"address"`
	Add     []groupMirror `json:"add,omitempty"`
	Delete  []int64       `json:"delete,omitempty"`
	Nodes   []int64       `json:"nodes,omitempty"`
}

// planGroupUpdate 规划入口组成员或入口地址的修改，并把每个托管入口的镜像对齐到修改后的成员：
// 缺镜像的成员（新成员，或之前漏建的）分配同一对外端口的镜像，不在组里的机器上的镜像删掉。
// 执行阶段在事务里按当前入口重新规划一次，预演之后新加的入口也会对齐。
// 返回执行参数、给人看的步骤，以及订阅是否改变（入口地址变化且有入口没有自己覆盖地址）。
func planGroupUpdate(ctx context.Context, q *db.Queries, r db.Route, peers []db.Machine, addr string, entries []db.NodeEntry) (*groupResolved, []string, []int64, error) {
	res := &groupResolved{Address: addr, Peers: make([]int64, 0, len(peers))}
	before := map[int64]bool{}
	for _, id := range r.EntryPeers {
		before[id] = true
	}
	after := map[int64]bool{}
	for _, m := range peers {
		res.Peers = append(res.Peers, m.ID)
		after[m.ID] = true
	}
	var steps []string
	for _, m := range peers {
		if len(entries) > 0 && (m.ID == 0 || m.TokenHash == nil) {
			return nil, nil, nil, apierr.Conflict("machine_not_enrolled", "机器 %s 还没有接入 patato-node，线路上已有入口时不能加进入口组", m.Host).
				WithHint("先用 patato machine install-cmd %s 接入", m.Host)
		}
	}
	occ := newOccupancy()
	nodes := map[int64]bool{}
	var subChanged []int64
	for _, e := range entries {
		fs, err := q.ListEntryForwards(ctx, e.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(fs) == 0 || fs[0].Mode == "manual" {
			continue // 手写转发不托管，不建镜像
		}
		n, err := q.GetNode(ctx, e.NodeID)
		if err != nil {
			return nil, nil, nil, err
		}
		mirrors, err := q.ListEntryMirrors(ctx, e.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		has := map[int64]bool{}
		for _, f := range mirrors {
			has[f.MachineID] = true
			if !after[f.MachineID] {
				res.Delete = append(res.Delete, f.ID)
				nodes[n.ID] = true
			}
		}
		for _, m := range peers {
			if has[m.ID] {
				continue
			}
			oc, err := occ.of(ctx, q, m)
			if err != nil {
				return nil, nil, nil, err
			}
			hp, err := relay.PeerPort(relay.Hop{Machine: m, Occupied: oc}, int(fs[0].PublicPort))
			if err != nil {
				return nil, nil, nil, apierr.From(err).WithHint("%s 的入口 %d 对外用端口 %d，入口组成员必须用同一个端口", n.Name, e.ID, fs[0].PublicPort)
			}
			res.Add = append(res.Add, groupMirror{EntryID: e.ID, NodeID: n.ID, MachineID: m.ID, Machine: m.Host, ListenPort: hp.Listen,
				PublicPort: hp.Public, TargetHost: fs[0].TargetHost, TargetPort: int(fs[0].TargetPort), Mode: forwardModeOf(m)})
			nodes[n.ID] = true
		}
		if addr != r.EntryAddress && e.Address == "" && e.Enabled {
			subChanged = append(subChanged, e.ID)
			nodes[n.ID] = true
		}
	}
	res.Nodes = keys(nodes)
	byMachine := map[string][]string{}
	for _, a := range res.Add {
		byMachine[a.Machine] = append(byMachine[a.Machine], fmt.Sprintf("%d → %s:%d", a.ListenPort, a.TargetHost, a.TargetPort))
	}
	for _, m := range peers {
		isNew := !before[m.ID]
		switch {
		case isNew && len(byMachine[m.Host]) > 0:
			steps = append(steps, fmt.Sprintf("入口组加入 %s，给 %d 个入口建镜像转发：%s", m.Host, len(byMachine[m.Host]), strings.Join(byMachine[m.Host], "；")))
		case isNew:
			steps = append(steps, fmt.Sprintf("入口组加入 %s", m.Host))
		case len(byMachine[m.Host]) > 0:
			steps = append(steps, fmt.Sprintf("补齐 %s 上缺少的 %d 条镜像转发：%s", m.Host, len(byMachine[m.Host]), strings.Join(byMachine[m.Host], "；")))
		}
	}
	for _, id := range r.EntryPeers {
		if !after[id] {
			m, err := q.GetMachine(ctx, id)
			if err != nil {
				return nil, nil, nil, err
			}
			steps = append(steps, fmt.Sprintf("入口组去掉 %s，删除它上面的镜像转发；入口地址仍解析到它时客户端会连不上，先改解析", m.Host))
		}
	}
	if addr != r.EntryAddress {
		switch {
		case addr == "":
			steps = append(steps, "清除入口地址，订阅改用第一跳机器的地址")
		default:
			steps = append(steps, fmt.Sprintf("入口地址 %s → %s", firstNonEmpty(r.EntryAddress, "（第一跳机器的地址）"), addr))
		}
	}
	return res, steps, subChanged, nil
}

// applyGroupUpdate 修改入口组：按预演定下的成员和地址，在事务里对当前全部入口重新规划镜像（预演之后新加的入口也对齐），
// 删除不在组里的机器上的镜像，校验端口后创建缺少的镜像。新镜像的端口与预演不同时（被占用后换不了同号）直接报错。
func applyGroupUpdate(ctx context.Context, q *db.Queries, r db.Route, g *groupResolved) (db.Route, error) {
	ms, err := q.ListMachinesByIDs(ctx, g.Peers)
	if err != nil {
		return r, err
	}
	peers := orderMachines(ms, g.Peers)
	entries, err := q.ListRouteEntries(ctx, &r.ID)
	if err != nil {
		return r, err
	}
	fresh, _, _, err := planGroupUpdate(ctx, q, r, peers, g.Address, entries)
	if err != nil {
		return r, err
	}
	g = fresh
	r, err = q.SetRouteEntryGroup(ctx, db.SetRouteEntryGroupParams{ID: r.ID, EntryPeers: g.Peers, EntryAddress: g.Address})
	if err != nil {
		return r, err
	}
	for _, id := range g.Delete {
		if err := q.DeleteForward(ctx, id); err != nil {
			return r, err
		}
	}
	occ := newOccupancy()
	for _, a := range g.Add {
		e, err := q.GetEntry(ctx, a.EntryID)
		if err != nil || e.RouteID == nil || *e.RouteID != r.ID {
			return r, apierr.Conflict("stale_plan", "入口 %d 在预演后变了", a.EntryID).WithHint("重新预演一次")
		}
		m, err := q.GetMachine(ctx, a.MachineID)
		if err != nil {
			return r, err
		}
		oc, err := occ.of(ctx, q, m)
		if err != nil {
			return r, err
		}
		if err := relay.Check(relay.Hop{Machine: m, Occupied: oc}, a.ListenPort); err != nil {
			return r, err
		}
		oc.Add(a.ListenPort, "本次计划已分配")
		_, err = q.CreateForward(ctx, db.CreateForwardParams{
			EntryID: a.EntryID, Hop: 0, MachineID: a.MachineID, ListenPort: int32(a.ListenPort), PublicPort: int32(a.PublicPort),
			TargetHost: a.TargetHost, TargetPort: int32(a.TargetPort), Mode: a.Mode, TargetIp: relay.LiteralIP(a.TargetHost), Mirror: true,
		})
		if isUniqueViolation(err) {
			return r, apierr.Conflict("port_taken", "机器 %s 的端口 %d 在预演后被占用", a.Machine, a.ListenPort).WithHint("重新预演一次")
		}
		if err != nil {
			return r, err
		}
	}
	for _, id := range g.Nodes {
		if _, err := relay.RefreshNodeForwards(ctx, q, id); err != nil {
			return r, err
		}
		if err := q.BumpNodeVersion(ctx, id); err != nil {
			return r, err
		}
	}
	return r, nil
}

// checkGroupPortsAfter 在修改机器端口段前检查入口组：成员上的镜像和第一跳的转发必须保持相同的对外端口，
// 否则域名解析到另一台机器时订阅里的端口对不上。probe 是修改后的机器。
func checkGroupPortsAfter(ctx context.Context, q *db.Queries, probe db.Machine, forwards []db.Forward) error {
	for _, f := range forwards {
		if f.Hop != 0 || f.Mode == "manual" {
			continue
		}
		e, err := q.GetEntry(ctx, f.EntryID)
		if err != nil {
			return err
		}
		if e.RouteID == nil {
			continue
		}
		r, err := q.GetRoute(ctx, *e.RouteID)
		if err != nil {
			return err
		}
		if len(r.EntryPeers) == 0 {
			continue
		}
		// 同一入口在其他成员上的对外端口
		peersFs, err := q.ListEntryMirrors(ctx, e.ID)
		if err != nil {
			return err
		}
		firsts, err := q.ListEntryForwards(ctx, e.ID)
		if err != nil {
			return err
		}
		others := slices.Concat(peersFs, firsts[:min(1, len(firsts))])
		newPublic := relay.PublicPort(probe, int(f.ListenPort))
		for _, o := range others {
			if o.ID == f.ID || o.MachineID == probe.ID {
				continue
			}
			if int(o.PublicPort) != newPublic {
				return apierr.Conflict("entry_group_port_mismatch", "修改后 %s 上入口 %d 的对外端口变成 %d，与入口组里其他机器的 %d 不一致",
					probe.Host, e.ID, newPublic, o.PublicPort).
					WithHint("入口组 %s 的成员必须对外用同一个端口；保持端口映射不变，或先把这台机器移出入口组", r.Code)
			}
		}
	}
	return nil
}

// groupTargets 是修改入口组要锁定的对象。
func groupTargets(r db.Route, entries []db.NodeEntry) []change.Target {
	return append([]change.Target{routeTarget(r)}, rehopTargets(entries)...)
}
