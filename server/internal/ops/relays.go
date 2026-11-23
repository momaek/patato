package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/relay"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// 中转线路的写操作。风险等级见 docs/relay.md「风险等级」：
//
//	设置机器网络属性            常规；改已在使用的对外地址会改变订阅，升为敏感
//	建线路、改线路名称          常规
//	给节点加入口、按套餐应用线路  常规，按受影响用户数升级
//	修改线路的跳                敏感（重建所有入口的转发）
//	删除入口或线路、停用入口      敏感
//	启用入口                    常规

// ---- 查找 ----------------------------------------------------------------

func findRoute(ctx context.Context, q *db.Queries, r Ref) (db.Route, error) {
	if r == "" {
		return db.Route{}, apierr.BadRequest("missing_route", "缺少线路").WithHint("传线路 ID 或代号；用 GET /api/v1/routes 查看全部")
	}
	var rt db.Route
	var err error
	if id, ok := r.id(); ok {
		rt, err = q.GetRoute(ctx, id)
	} else {
		rt, err = q.GetRouteByCode(ctx, string(r))
	}
	if store.IsNotFound(err) {
		return rt, apierr.NotFound("route_not_found", "线路 %s 不存在", r).WithHint("用 GET /api/v1/routes 查看全部线路")
	}
	return rt, err
}

func findEntry(ctx context.Context, q *db.Queries, n db.Node, r Ref) (db.NodeEntry, error) {
	id, ok := r.id()
	if !ok {
		return db.NodeEntry{}, apierr.BadRequest("invalid_entry", "入口要用 ID 指定").WithHint("用 GET /api/v1/nodes/%s/entries 查看入口 ID", n.Code)
	}
	e, err := q.GetEntry(ctx, id)
	if store.IsNotFound(err) || (err == nil && e.NodeID != n.ID) {
		return e, apierr.NotFound("entry_not_found", "节点 %s 没有入口 %s", n.Code, r).WithHint("用 GET /api/v1/nodes/%s/entries 查看入口", n.Code)
	}
	return e, err
}

// findMachineRef 按机器名或 ID 找机器；机器名在 Patato 里还没有记录时，按 tolato 中的机器建一条（中转机器可以先登记再接入 patato-node）。
func findMachineRef(ctx context.Context, q *db.Queries, r string, create bool) (db.Machine, error) {
	r = strings.TrimSpace(r)
	if r == "" {
		return db.Machine{}, apierr.BadRequest("missing_machine", "缺少机器").WithHint("传机器名（tolato 中的名称）；用 GET /api/v1/machines 查看全部")
	}
	if id, err := strconv.ParseInt(r, 10, 64); err == nil {
		m, err := q.GetMachine(ctx, id)
		if store.IsNotFound(err) {
			return m, apierr.NotFound("machine_not_found", "机器 %d 不存在", id)
		}
		return m, err
	}
	m, err := q.GetMachineByHost(ctx, r)
	if store.IsNotFound(err) && create {
		return q.EnsureMachine(ctx, r)
	}
	if store.IsNotFound(err) {
		return m, apierr.NotFound("machine_not_found", "机器 %s 还没有登记", r).
			WithHint("用 patato route create 或 patato machine set 登记，或用 patato machine install-cmd %s 接入 patato-node", r)
	}
	return m, err
}

func routeTarget(r db.Route) change.Target {
	return change.Target{Type: "route", ID: r.ID, Version: r.Version}
}

func entryTarget(e db.NodeEntry) change.Target {
	return change.Target{Type: "entry", ID: e.ID, Version: e.Version}
}

func machineTarget(m db.Machine) change.Target {
	return change.Target{Type: "machine", ID: m.ID, Version: m.Version}
}

// ---- machine.set_network ------------------------------------------------

type MachineSetNetwork struct{ d *Deps }

func (*MachineSetNetwork) Kind() string      { return "machine.set_network" }
func (*MachineSetNetwork) Risk() change.Risk { return change.Normal }

type machineNetworkParams struct {
	Machine       string            `json:"machine"`
	PublicAddress *string           `json:"public_address"`
	PortRanges    []relay.PortRange `json:"port_ranges"`
	NAT           *bool             `json:"nat"`
	ForwardMode   *string           `json:"forward_mode"`
	ReservedPorts []relay.PortRange `json:"reserved_ports"`
}

type machineNetwork struct {
	PublicAddress string            `json:"public_address"`
	PortRanges    []relay.PortRange `json:"port_ranges"`
	NAT           bool              `json:"nat"`
	ForwardMode   string            `json:"forward_mode"`
	ReservedPorts []relay.PortRange `json:"reserved_ports"`
}

func currentNetwork(m db.Machine) machineNetwork {
	pr, _ := relay.ParseRanges(m.PortRanges)
	rp, _ := relay.ParseRanges(m.ReservedPorts)
	return machineNetwork{PublicAddress: m.PublicAddress, PortRanges: nonNilRanges(pr), NAT: m.Nat, ForwardMode: m.ForwardMode,
		ReservedPorts: nonNilRanges(rp)}
}

func nonNilRanges(r []relay.PortRange) []relay.PortRange {
	if r == nil {
		return []relay.PortRange{}
	}
	return r
}

func (p *machineNetworkParams) merge(m db.Machine) (machineNetwork, error) {
	n := currentNetwork(m)
	if p.PublicAddress != nil {
		n.PublicAddress = strings.TrimSpace(*p.PublicAddress)
	}
	if p.PortRanges != nil {
		n.PortRanges = p.PortRanges
	}
	if p.NAT != nil {
		n.NAT = *p.NAT
	}
	if p.ForwardMode != nil {
		n.ForwardMode = strings.TrimSpace(*p.ForwardMode)
	}
	if p.ReservedPorts != nil {
		n.ReservedPorts = p.ReservedPorts
	}
	if _, err := relay.ParseRanges(mustJSON(n.PortRanges)); err != nil {
		return n, apierr.BadRequest("invalid_port_ranges", "%v", err)
	}
	if _, err := relay.ParseRanges(mustJSON(n.ReservedPorts)); err != nil {
		return n, apierr.BadRequest("invalid_reserved_ports", "%v", err)
	}
	if n.NAT && len(n.PortRanges) == 0 {
		return n, apierr.BadRequest("nat_needs_port_ranges", "NAT 机器必须填写端口段").
			WithHint("例如 --port-range 20000-20099:30000 表示外网 30000 对应本机 20000")
	}
	switch n.ForwardMode {
	case "auto", "nft", "userspace":
	default:
		return n, apierr.BadRequest("invalid_forward_mode", "转发模式 %q 不支持", n.ForwardMode).WithHint("可选 auto、nft、userspace")
	}
	if strings.ContainsAny(n.PublicAddress, " /:") && net.ParseIP(n.PublicAddress) == nil {
		return n, apierr.BadRequest("invalid_public_address", "对外地址 %q 应为 IP 或域名", n.PublicAddress)
	}
	return n, nil
}

func (o *MachineSetNetwork) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[machineNetworkParams](raw)
	if err != nil {
		return nil, err
	}
	m, err := findMachineRef(ctx, q, p.Machine, false)
	if isCode(err, "machine_not_found") {
		// 还没登记的机器：执行时登记，预演按默认值比较
		m = db.Machine{Host: strings.TrimSpace(p.Machine), PortRanges: json.RawMessage("[]"), ReservedPorts: json.RawMessage("[]"), ForwardMode: "auto"}
	} else if err != nil {
		return nil, err
	}
	before := currentNetwork(m)
	after, err := p.merge(m)
	if err != nil {
		return nil, err
	}
	diff := fieldDiff(before, after)
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	// 已分配的转发端口必须仍在新的端口段内，否则上一跳和订阅里的外网端口会对不上
	var forwards []db.Forward
	if m.ID != 0 {
		if forwards, err = q.ListMachineForwards(ctx, m.ID); err != nil {
			return nil, err
		}
	}
	probe := m
	probe.PortRanges, probe.Nat = mustJSON(after.PortRanges), after.NAT
	for _, f := range forwards {
		if f.Mode == "manual" {
			continue
		}
		inRange := false
		for _, r := range relay.Ranges(probe) {
			if r.Contains(int(f.ListenPort)) {
				inRange = true
			}
		}
		if !inRange {
			return nil, apierr.Conflict("port_in_use", "机器 %s 上的转发 %d 使用端口 %d，不在新的端口段内", m.Host, f.ID, f.ListenPort).
				WithHint("先删除或迁移这台机器上的入口，再缩小端口段")
		}
	}
	entries := map[int64]bool{}
	firstHop := map[int64]bool{}
	changedPublic := false
	for _, f := range forwards {
		entries[f.EntryID] = true
		if f.Hop == 0 {
			firstHop[f.EntryID] = true
		}
		if relay.PublicPort(probe, int(f.ListenPort)) != int(f.PublicPort) {
			changedPublic = true
		}
	}
	addrChanged := before.PublicAddress != after.PublicAddress && m.ID != 0
	ids := keys(firstHop)
	users := int64(0)
	if len(ids) > 0 {
		if users, err = q.CountUsersOfEntries(ctx, ids); err != nil {
			return nil, err
		}
	}
	steps := []string{fmt.Sprintf("更新机器 %s 的网络属性", m.Host)}
	draft := &change.Draft{Summary: fmt.Sprintf("设置机器 %s 的网络属性", m.Host), Diff: diff}
	if m.ID != 0 {
		draft.Targets = []change.Target{machineTarget(m)}
	}
	if len(entries) > 0 && (addrChanged || changedPublic) {
		steps = append(steps, fmt.Sprintf("重新生成经过这台机器的 %d 个入口的转发目标并推送", len(entries)))
		if len(firstHop) > 0 {
			steps = append(steps, fmt.Sprintf("%d 个入口的订阅地址或端口改变，%d 名用户需要刷新订阅", len(firstHop), users))
			draft.Impact = change.Impact{Users: users}
			draft.Escalate = "修改已在使用的入口机器的对外地址或端口映射会改变订阅，升为敏感"
		}
	}
	if before.ForwardMode != after.ForwardMode && len(forwards) > 0 {
		steps = append(steps, fmt.Sprintf("patato-node 按新的模式 %s 重建这台机器上的转发", after.ForwardMode))
	}
	draft.Steps = steps
	return draft, nil
}

func (o *MachineSetNetwork) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[machineNetworkParams](raw)
	if err != nil {
		return nil, err
	}
	m, err := findMachineRef(ctx, q, p.Machine, true)
	if err != nil {
		return nil, err
	}
	n, err := p.merge(m)
	if err != nil {
		return nil, err
	}
	m, err = q.SetMachineNetwork(ctx, db.SetMachineNetworkParams{
		ID: m.ID, PublicAddress: n.PublicAddress, PortRanges: mustJSON(n.PortRanges), Nat: n.NAT, ForwardMode: n.ForwardMode,
		ReservedPorts: mustJSON(n.ReservedPorts),
	})
	if err != nil {
		return nil, err
	}
	updated, err := relay.RefreshForwardTargets(ctx, q, m.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"machine": m.Host, "version": m.Version, "forwards_updated": updated}, nil
}

// ---- route.create / route.update / route.delete -------------------------

type RouteCreate struct{ d *Deps }

func (*RouteCreate) Kind() string      { return "route.create" }
func (*RouteCreate) Risk() change.Risk { return change.Normal }

type routeParams struct {
	Code string   `json:"code"`
	Name string   `json:"name"`
	Hops []string `json:"hops"`
}

// resolveHops 把机器名换成机器；还没登记的机器在执行时登记（create 为 true）。预演时用 ID 0 占位。
func resolveHops(ctx context.Context, q *db.Queries, names []string, create bool) ([]db.Machine, error) {
	if len(names) == 0 {
		return nil, apierr.BadRequest("missing_hops", "线路至少要经过一台中转机器").WithHint("传 hops: [\"gz-01\", \"hk-01\"]，按客户端到落地的顺序")
	}
	if len(names) > 8 {
		return nil, apierr.BadRequest("too_many_hops", "一条线路最多 8 跳")
	}
	out := make([]db.Machine, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if seen[n] {
			return nil, apierr.BadRequest("duplicate_hop", "线路里机器 %s 出现了两次", n)
		}
		seen[n] = true
		m, err := findMachineRef(ctx, q, n, create)
		if !create && isCode(err, "machine_not_found") {
			m, err = db.Machine{Host: n, PortRanges: json.RawMessage("[]"), State: "unknown"}, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func hostNames(ms []db.Machine) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Host
	}
	return out
}

func (o *RouteCreate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[routeParams](raw)
	if err != nil {
		return nil, err
	}
	p.Code = strings.ToLower(strings.TrimSpace(p.Code))
	if !codePattern.MatchString(p.Code) {
		return nil, apierr.BadRequest("invalid_code", "线路代号 %q 只能包含小写字母、数字和连字符", p.Code)
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, apierr.BadRequest("missing_name", "缺少线路名称").WithHint("例如 --name 广州中转；订阅里入口名为「节点名 · 线路名」")
	}
	if _, err := q.GetRouteByCode(ctx, p.Code); err == nil {
		return nil, apierr.Conflict("route_code_taken", "线路代号 %s 已被占用", p.Code)
	}
	hops, err := resolveHops(ctx, q, p.Hops, false)
	if err != nil {
		return nil, err
	}
	steps := []string{fmt.Sprintf("新建线路 %s（%s）：%s", p.Name, p.Code, strings.Join(hostNames(hops), " → "))}
	for _, m := range hops {
		if m.ID == 0 {
			steps = append(steps, fmt.Sprintf("登记机器 %s；用 patato machine install-cmd %s 接入 patato-node 后才能托管转发", m.Host, m.Host))
		}
	}
	steps = append(steps, "线路本身不影响订阅；用 patato node entry add 或 patato route apply 给节点开入口")
	return &change.Draft{
		Summary: fmt.Sprintf("新建线路 %s", p.Name),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "线路", Before: nil, After: map[string]any{"code": p.Code, "name": p.Name, "hops": hostNames(hops)}}},
	}, nil
}

func (o *RouteCreate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[routeParams](raw)
	if err != nil {
		return nil, err
	}
	hops, err := resolveHops(ctx, q, p.Hops, true)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(hops))
	for i, m := range hops {
		ids[i] = m.ID
	}
	r, err := q.CreateRoute(ctx, db.CreateRouteParams{Code: strings.ToLower(strings.TrimSpace(p.Code)), Name: strings.TrimSpace(p.Name), Hops: ids})
	if isUniqueViolation(err) {
		return nil, apierr.Conflict("route_code_taken", "线路代号 %s 已被占用", p.Code)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"route_id": r.ID, "code": r.Code}, nil
}

type RouteUpdate struct{ d *Deps }

func (*RouteUpdate) Kind() string      { return "route.update" }
func (*RouteUpdate) Risk() change.Risk { return change.Normal }

type routeUpdateParams struct {
	Route Ref      `json:"route"`
	Name  *string  `json:"name"`
	Hops  []string `json:"hops"`
}

func (o *RouteUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[routeUpdateParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRoute(ctx, q, p.Route)
	if err != nil {
		return nil, err
	}
	var diff []change.DiffEntry
	if p.Name != nil && strings.TrimSpace(*p.Name) != r.Name {
		if strings.TrimSpace(*p.Name) == "" {
			return nil, apierr.BadRequest("missing_name", "线路名称不能为空")
		}
		diff = append(diff, change.DiffEntry{Field: "name", Before: r.Name, After: strings.TrimSpace(*p.Name)})
	}
	entries, err := q.ListRouteEntries(ctx, &r.ID)
	if err != nil {
		return nil, err
	}
	draft := &change.Draft{Summary: fmt.Sprintf("修改线路 %s", r.Name), Targets: []change.Target{routeTarget(r)}}
	if p.Hops != nil {
		cur, err := q.ListMachinesByIDs(ctx, r.Hops)
		if err != nil {
			return nil, err
		}
		before := orderMachines(cur, r.Hops)
		after, err := resolveHops(ctx, q, p.Hops, false)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(hostNames(before), hostNames(after)) {
			diff = append(diff, change.DiffEntry{Field: "hops", Before: hostNames(before), After: hostNames(after)})
			if len(entries) > 0 {
				// 跳变了要重建入口的全部转发，端口和入口地址都可能变，固定为敏感
				return nil, apierr.Conflict("route_in_use", "线路 %s 上还有 %d 个入口，不能直接修改跳", r.Name, len(entries)).
					WithHint("新建一条线路，给节点开新入口并确认可用后，停用并删除旧入口，再删除旧线路")
			}
		}
	}
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	draft.Diff = diff
	draft.Steps = []string{fmt.Sprintf("更新线路 %s 的 %d 项设置", r.Name, len(diff))}
	if p.Name != nil && len(entries) > 0 {
		ids := make([]int64, len(entries))
		for i, e := range entries {
			ids[i] = e.ID
		}
		users, err := q.CountUsersOfEntries(ctx, ids)
		if err != nil {
			return nil, err
		}
		draft.Steps = append(draft.Steps, fmt.Sprintf("没有自定义名称的入口在订阅里改名，%d 名用户下次更新订阅时生效", users))
	}
	return draft, nil
}

func (o *RouteUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[routeUpdateParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRoute(ctx, q, p.Route)
	if err != nil {
		return nil, err
	}
	name, hops := r.Name, r.Hops
	if p.Name != nil {
		name = strings.TrimSpace(*p.Name)
	}
	if p.Hops != nil {
		ms, err := resolveHops(ctx, q, p.Hops, true)
		if err != nil {
			return nil, err
		}
		hops = make([]int64, len(ms))
		for i, m := range ms {
			hops[i] = m.ID
		}
	}
	r, err = q.UpdateRoute(ctx, db.UpdateRouteParams{ID: r.ID, Name: name, Hops: hops})
	if err != nil {
		return nil, err
	}
	return map[string]any{"route_id": r.ID, "version": r.Version}, nil
}

func orderMachines(ms []db.Machine, ids []int64) []db.Machine {
	by := map[int64]db.Machine{}
	for _, m := range ms {
		by[m.ID] = m
	}
	out := make([]db.Machine, 0, len(ids))
	for _, id := range ids {
		out = append(out, by[id])
	}
	return out
}

type RouteDelete struct{ d *Deps }

func (*RouteDelete) Kind() string      { return "route.delete" }
func (*RouteDelete) Risk() change.Risk { return change.Sensitive }

type routeRefParams struct {
	Route Ref `json:"route"`
}

func (o *RouteDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[routeRefParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRoute(ctx, q, p.Route)
	if err != nil {
		return nil, err
	}
	entries, err := q.ListRouteEntries(ctx, &r.ID)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, apierr.Conflict("route_in_use", "线路 %s 上还有 %d 个入口", r.Name, len(entries)).
			WithHint("先用 patato node entry delete 删除这些入口")
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除线路 %s", r.Name),
		Steps:   []string{fmt.Sprintf("删除线路 %s（%s），历史流量保留", r.Name, r.Code)},
		Diff:    []change.DiffEntry{{Field: "线路", Before: r.Name, After: nil}},
		Targets: []change.Target{routeTarget(r)},
	}, nil
}

func (o *RouteDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[routeRefParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRoute(ctx, q, p.Route)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteRoute(ctx, r.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": r.Code}, nil
}

// ---- entry.add / route.apply --------------------------------------------

// plannedForward 是预演时定下的一跳转发，执行阶段只按它创建。
type plannedForward struct {
	Hop        int    `json:"hop"`
	MachineID  int64  `json:"machine_id"`
	Machine    string `json:"machine"`
	ListenPort int    `json:"listen_port"`
	PublicPort int    `json:"public_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
	Mode       string `json:"mode"`
}

// plannedEntry 是预演时定下的一个入口。
type plannedEntry struct {
	NodeID   int64            `json:"node_id"`
	RouteID  *int64           `json:"route_id,omitempty"`
	Suffix   string           `json:"suffix"`
	Name     string           `json:"name"`
	Address  string           `json:"address"`
	Port     int              `json:"port"`
	Enabled  bool             `json:"enabled"`
	Sort     int              `json:"sort"`
	Forwards []plannedForward `json:"forwards"`
}

// occupancy 记录分配端口时每台机器上已占用的端口：落地节点监听端口、已分配的转发端口（含手写）、
// patato-node 上报的正在监听的端口、运营者登记的保留段。
type occupancy struct {
	byMachine map[int64]*relay.Occupied
}

func newOccupancy() *occupancy { return &occupancy{byMachine: map[int64]*relay.Occupied{}} }

func (o *occupancy) of(ctx context.Context, q *db.Queries, m db.Machine) (*relay.Occupied, error) {
	if oc, ok := o.byMachine[m.ID]; ok && m.ID != 0 {
		return oc, nil
	}
	oc := relay.NewOccupied()
	if m.Host != "" {
		ports, err := q.UsedServerPorts(ctx, m.Host)
		if err != nil {
			return nil, err
		}
		for _, p := range ports {
			oc.Add(int(p), "节点监听端口")
		}
	}
	if m.ID != 0 {
		ports, err := q.UsedForwardPorts(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		for _, p := range ports {
			oc.Add(int(p), "已分配的转发端口")
		}
		for _, p := range m.ListeningPorts {
			oc.Add(int(p), "机器上正在监听")
		}
	}
	rs, _ := relay.ParseRanges(m.ReservedPorts)
	for _, r := range rs {
		oc.Reserve(r)
	}
	if m.ID != 0 {
		o.byMachine[m.ID] = oc
	}
	return oc, nil
}

// planRelayEntry 为节点规划一个经 route 的托管入口：分配每跳端口并计算下一跳。
func planRelayEntry(ctx context.Context, q *db.Queries, occ *occupancy, n db.Node, r db.Route, hops []db.Machine) (plannedEntry, error) {
	pe := plannedEntry{NodeID: n.ID, RouteID: &r.ID, Enabled: true}
	hs := make([]relay.Hop, len(hops))
	for i, m := range hops {
		oc, err := occ.of(ctx, q, m)
		if err != nil {
			return pe, err
		}
		hs[i] = relay.Hop{Machine: m, Occupied: oc}
	}
	ports, err := relay.Allocate(hs)
	if err != nil {
		return pe, err
	}
	var landing *db.Machine
	if m, err := q.GetMachineByHost(ctx, n.Host); err == nil {
		landing = &m
	}
	targets := relay.Targets(n, landing, hops, ports)
	for i, m := range hops {
		pe.Forwards = append(pe.Forwards, plannedForward{
			Hop: i, MachineID: m.ID, Machine: m.Host, ListenPort: ports[i].Listen, PublicPort: ports[i].Public,
			TargetHost: targets[i].Host, TargetPort: targets[i].Port, Mode: forwardModeOf(m),
		})
	}
	return pe, nil
}

// forwardModeOf 是新转发的模式：机器指定了 userspace 就用它，否则 nft（patato-node 在不支持 nft 时自动退回并上报）。
func forwardModeOf(m db.Machine) string {
	if m.ForwardMode == "userspace" {
		return "userspace"
	}
	return "nft"
}

// applyPlannedEntry 在执行阶段创建入口和转发，并在同一事务里重新校验端口没有被占用。
func applyPlannedEntry(ctx context.Context, q *db.Queries, pe plannedEntry) (db.NodeEntry, error) {
	occ := newOccupancy()
	for _, f := range pe.Forwards {
		m, err := q.GetMachine(ctx, f.MachineID)
		if err != nil {
			return db.NodeEntry{}, err
		}
		oc, err := occ.of(ctx, q, m)
		if err != nil {
			return db.NodeEntry{}, err
		}
		if f.Mode != "manual" {
			if err := relay.Check(relay.Hop{Machine: m, Occupied: oc}, f.ListenPort); err != nil {
				return db.NodeEntry{}, err
			}
		}
		oc.Add(f.ListenPort, "本次计划已分配")
	}
	e, err := q.CreateEntry(ctx, db.CreateEntryParams{
		NodeID: pe.NodeID, RouteID: pe.RouteID, Suffix: pe.Suffix, Name: pe.Name, Address: pe.Address, Port: int32(pe.Port),
		Enabled: pe.Enabled, Sort: int32(pe.Sort),
	})
	if err != nil {
		return e, err
	}
	for _, f := range pe.Forwards {
		_, err := q.CreateForward(ctx, db.CreateForwardParams{
			EntryID: e.ID, Hop: int32(f.Hop), MachineID: f.MachineID, ListenPort: int32(f.ListenPort), PublicPort: int32(f.PublicPort),
			TargetHost: f.TargetHost, TargetPort: int32(f.TargetPort), Mode: f.Mode, TargetIp: relay.LiteralIP(f.TargetHost),
		})
		if isUniqueViolation(err) {
			return e, apierr.Conflict("port_taken", "机器 %s 的端口 %d 在预演后被占用", f.Machine, f.ListenPort).WithHint("重新预演一次")
		}
		if err != nil {
			return e, err
		}
	}
	if err := q.BumpNodeVersion(ctx, pe.NodeID); err != nil {
		return e, err
	}
	return e, nil
}

func describeForwards(fs []plannedForward) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		port := strconv.Itoa(f.ListenPort)
		if f.PublicPort != f.ListenPort {
			port = fmt.Sprintf("%d（外网 %d）", f.ListenPort, f.PublicPort)
		}
		parts = append(parts, fmt.Sprintf("%s:%s → %s:%d", f.Machine, port, f.TargetHost, f.TargetPort))
	}
	return strings.Join(parts, "；")
}

type EntryAdd struct{ d *Deps }

func (*EntryAdd) Kind() string      { return "entry.add" }
func (*EntryAdd) Risk() change.Risk { return change.Normal }

type entryAddParams struct {
	Node    Ref    `json:"node"`
	Route   Ref    `json:"route"`
	Suffix  string `json:"suffix"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Sort    *int   `json:"sort"`
	Enabled *bool  `json:"enabled"`
	// Manual 为 true 时登记运营者手写的转发：Forwards 给出每跳的端口和下一跳，Patato 不下发、不计流量。
	Manual   bool            `json:"manual"`
	Forwards []manualForward `json:"forwards"`
}

// manualForward 是一跳手写转发：机器上监听的端口和它转去的下一跳。
type manualForward struct {
	Machine    string `json:"machine"`
	ListenPort int    `json:"listen_port"`
	PublicPort int    `json:"public_port"`
	TargetHost string `json:"target_host"`
	TargetPort int    `json:"target_port"`
}

type entryResolved struct {
	Entry plannedEntry `json:"entry"`
}

func (o *EntryAdd) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[entryAddParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	existing, err := q.ListNodeEntries(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	var pe plannedEntry
	var r db.Route
	switch {
	case p.Route != "":
		r, err = findRoute(ctx, q, p.Route)
		if err != nil {
			return nil, err
		}
		ms, err := q.ListMachinesByIDs(ctx, r.Hops)
		if err != nil {
			return nil, err
		}
		hops := orderMachines(ms, r.Hops)
		if p.Manual {
			pe, err = planManualEntry(ctx, q, n, r, hops, p.Forwards)
		} else {
			pe, err = planRelayEntry(ctx, q, newOccupancy(), n, r, hops)
		}
		if err != nil {
			return nil, err
		}
		// 中转入口也可以覆盖订阅里的第一跳地址和端口：第一跳前面还有一层运营者自管的转发时（例如入口机整段 DNAT 到中转），
		// 客户端连的是那一层，而不是第一跳机器。
		if p.Port < 0 || p.Port > 65535 {
			return nil, apierr.BadRequest("invalid_port", "端口 %d 不合法", p.Port)
		}
		pe.Address, pe.Port = strings.TrimSpace(p.Address), p.Port
	case p.Address != "" || p.Port > 0:
		if p.Port < 0 || p.Port > 65535 {
			return nil, apierr.BadRequest("invalid_port", "端口 %d 不合法", p.Port)
		}
		pe = plannedEntry{NodeID: n.ID, Address: strings.TrimSpace(p.Address), Port: p.Port, Enabled: true}
	default:
		for _, e := range existing {
			if relay.EntryKind(e) == relay.KindDirect {
				return nil, apierr.Conflict("entry_exists", "节点 %s 已经有直连入口（%d）", n.Name, e.ID)
			}
		}
		pe = plannedEntry{NodeID: n.ID, Enabled: true}
	}
	pe.Suffix, pe.Name = strings.TrimSpace(p.Suffix), strings.TrimSpace(p.Name)
	if p.Enabled != nil {
		pe.Enabled = *p.Enabled
	}
	pe.Sort = int(n.Sort)
	if p.Sort != nil {
		pe.Sort = *p.Sort
	}
	users, err := q.CountUsersOfNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	routes := map[int64]db.Route{}
	if r.ID != 0 {
		routes[r.ID] = r
	}
	name := relay.EntryName(n, db.NodeEntry{RouteID: pe.RouteID, Suffix: pe.Suffix, Name: pe.Name}, routes)
	steps := []string{}
	if len(existing) == 0 && relay.EntryKind(db.NodeEntry{RouteID: pe.RouteID, Address: pe.Address, Port: int32(pe.Port)}) != relay.KindDirect {
		steps = append(steps, fmt.Sprintf("把节点 %s 原来的地址登记为直连入口，订阅照旧", n.Name))
	}
	switch {
	case pe.RouteID != nil && p.Manual:
		steps = append(steps, fmt.Sprintf("登记手写转发（不下发、不计流量）：%s", describeForwards(pe.Forwards)))
	case pe.RouteID != nil:
		steps = append(steps, fmt.Sprintf("分配端口并生成转发：%s", describeForwards(pe.Forwards)),
			"各跳的 patato-node 收到推送后应用转发，全部生效后入口出现在订阅里")
	}
	first := ""
	if len(pe.Forwards) > 0 {
		f := pe.Forwards[0]
		port := f.PublicPort
		if pe.Port > 0 {
			port = pe.Port
		}
		first = fmt.Sprintf("%s:%d", firstNonEmpty(pe.Address, f.Machine), port)
	}
	steps = append(steps, fmt.Sprintf("新增入口「%s」%s，%d 名用户的订阅里多一条", name, first, users))
	return &change.Draft{
		Summary:  fmt.Sprintf("给 %s 加入口「%s」", n.Name, name),
		Steps:    steps,
		Diff:     []change.DiffEntry{{Field: "入口", Before: nil, After: map[string]any{"name": name, "forwards": pe.Forwards}}},
		Impact:   change.Impact{Users: users, Nodes: 1},
		Targets:  append([]change.Target{nodeTarget(n)}, routeTargets(r)...),
		Resolved: entryResolved{Entry: pe},
	}, nil
}

func routeTargets(r db.Route) []change.Target {
	if r.ID == 0 {
		return nil
	}
	return []change.Target{routeTarget(r)}
}

// planManualEntry 登记手写转发：每跳必须与线路的机器一一对应，端口不能与已分配的冲突。
func planManualEntry(ctx context.Context, q *db.Queries, n db.Node, r db.Route, hops []db.Machine, fs []manualForward) (plannedEntry, error) {
	pe := plannedEntry{NodeID: n.ID, RouteID: &r.ID, Enabled: true}
	if len(fs) != len(hops) {
		return pe, apierr.BadRequest("manual_forwards_mismatch", "线路 %s 有 %d 跳，手写转发给了 %d 条", r.Name, len(hops), len(fs)).
			WithHint("按线路顺序给出每跳的 machine、listen_port、target_host、target_port")
	}
	occ := newOccupancy()
	for i, f := range fs {
		m := hops[i]
		if f.Machine != "" && f.Machine != m.Host {
			return pe, apierr.BadRequest("manual_forwards_mismatch", "第 %d 跳应为机器 %s，给的是 %s", i+1, m.Host, f.Machine)
		}
		if f.ListenPort < 1 || f.ListenPort > 65535 || f.TargetPort < 1 || f.TargetPort > 65535 || strings.TrimSpace(f.TargetHost) == "" {
			return pe, apierr.BadRequest("invalid_manual_forward", "第 %d 跳的端口或下一跳不完整", i+1)
		}
		oc, err := occ.of(ctx, q, m)
		if err != nil {
			return pe, err
		}
		if why := oc.Why(f.ListenPort); why == "已分配的转发端口" {
			return pe, apierr.Conflict("port_taken", "机器 %s 的端口 %d 已登记过转发", m.Host, f.ListenPort)
		}
		public := f.PublicPort
		if public == 0 {
			public = f.ListenPort
		}
		pe.Forwards = append(pe.Forwards, plannedForward{Hop: i, MachineID: m.ID, Machine: m.Host, ListenPort: f.ListenPort,
			PublicPort: public, TargetHost: strings.TrimSpace(f.TargetHost), TargetPort: f.TargetPort, Mode: "manual"})
	}
	return pe, nil
}

func (o *EntryAdd) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[entryResolved](raw)
	if err != nil {
		return nil, err
	}
	if err := ensureDirectEntry(ctx, q, r.Entry); err != nil {
		return nil, err
	}
	e, err := applyPlannedEntry(ctx, q, r.Entry)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entry_id": e.ID, "node_id": e.NodeID, "forwards": r.Entry.Forwards}, nil
}

// ensureDirectEntry 在节点第一次加非直连入口时，把节点原来的地址登记为直连入口，避免订阅里少了原来那条。
func ensureDirectEntry(ctx context.Context, q *db.Queries, pe plannedEntry) error {
	if relay.EntryKind(db.NodeEntry{RouteID: pe.RouteID, Address: pe.Address, Port: int32(pe.Port)}) == relay.KindDirect {
		return nil
	}
	existing, err := q.ListNodeEntries(ctx, pe.NodeID)
	if err != nil || len(existing) > 0 {
		return err
	}
	n, err := q.GetNode(ctx, pe.NodeID)
	if err != nil {
		return err
	}
	_, err = q.CreateEntry(ctx, db.CreateEntryParams{NodeID: n.ID, Enabled: true, Sort: n.Sort})
	return err
}

// ---- route.apply：把套餐里的所有节点经一条线路各开一个入口 ---------------

type RouteApply struct{ d *Deps }

func (*RouteApply) Kind() string      { return "route.apply" }
func (*RouteApply) Risk() change.Risk { return change.Normal }

type routeApplyParams struct {
	Route  Ref    `json:"route"`
	Plan   Ref    `json:"plan"`
	Nodes  []Ref  `json:"nodes"`
	Suffix string `json:"suffix"`
}

type routeApplyResolved struct {
	RouteID int64          `json:"route_id"`
	Entries []plannedEntry `json:"entries"`
}

func (o *RouteApply) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[routeApplyParams](raw)
	if err != nil {
		return nil, err
	}
	r, err := findRoute(ctx, q, p.Route)
	if err != nil {
		return nil, err
	}
	var nodes []db.Node
	targets := []change.Target{routeTarget(r)}
	switch {
	case p.Plan != "":
		pl, err := findPlan(ctx, q, p.Plan)
		if err != nil {
			return nil, err
		}
		if nodes, err = q.ListPlanNodes(ctx, pl.ID); err != nil {
			return nil, err
		}
		targets = append(targets, planTarget(pl))
	case len(p.Nodes) > 0:
		for _, ref := range p.Nodes {
			n, err := findNode(ctx, q, ref)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, n)
		}
	default:
		return nil, apierr.BadRequest("missing_scope", "要指定套餐或节点").WithHint("传 plan 或 nodes")
	}
	ms, err := q.ListMachinesByIDs(ctx, r.Hops)
	if err != nil {
		return nil, err
	}
	hops := orderMachines(ms, r.Hops)
	occ := newOccupancy()
	res := routeApplyResolved{RouteID: r.ID}
	var steps, skipped []string
	ids := []int64{}
	for _, n := range nodes {
		entries, err := q.ListNodeEntries(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		dup := false
		for _, e := range entries {
			if e.RouteID != nil && *e.RouteID == r.ID {
				dup = true
			}
		}
		if dup {
			skipped = append(skipped, n.Name)
			continue
		}
		pe, err := planRelayEntry(ctx, q, occ, n, r, hops)
		if err != nil {
			return nil, err
		}
		pe.Suffix, pe.Sort = strings.TrimSpace(p.Suffix), int(n.Sort)
		res.Entries = append(res.Entries, pe)
		ids = append(ids, n.ID)
		targets = append(targets, nodeTarget(n))
		steps = append(steps, fmt.Sprintf("%s：%s", n.Name, describeForwards(pe.Forwards)))
	}
	if len(res.Entries) == 0 {
		return nil, apierr.Conflict("nothing_to_apply", "这些节点都已经有经过线路 %s 的入口", r.Name)
	}
	if len(skipped) > 0 {
		steps = append(steps, fmt.Sprintf("跳过已经有这条线路入口的节点：%s", strings.Join(skipped, "、")))
	}
	users, err := q.CountUsersOfNodes(ctx, ids)
	if err != nil {
		return nil, err
	}
	steps = append(steps, fmt.Sprintf("各跳的 patato-node 收到推送后应用转发，全部生效后 %d 名用户的订阅里多出 %d 个入口", users, len(res.Entries)))
	return &change.Draft{
		Summary:  fmt.Sprintf("让 %d 个节点经过线路 %s", len(res.Entries), r.Name),
		Steps:    steps,
		Diff:     []change.DiffEntry{{Field: "新入口", Before: nil, After: res.Entries}},
		Impact:   change.Impact{Users: users, Nodes: len(res.Entries)},
		Targets:  targets,
		Resolved: res,
	}, nil
}

func (o *RouteApply) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[routeApplyResolved](raw)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, pe := range r.Entries {
		if err := ensureDirectEntry(ctx, q, pe); err != nil {
			return nil, err
		}
		e, err := applyPlannedEntry(ctx, q, pe)
		if err != nil {
			return nil, err
		}
		ids = append(ids, e.ID)
	}
	return map[string]any{"route_id": r.RouteID, "entry_ids": ids}, nil
}

// ---- entry.enable / entry.disable / entry.delete ------------------------

type EntrySetEnabled struct {
	d      *Deps
	enable bool
}

func (o *EntrySetEnabled) Kind() string {
	if o.enable {
		return "entry.enable"
	}
	return "entry.disable"
}

func (o *EntrySetEnabled) Risk() change.Risk {
	if o.enable {
		return change.Normal
	}
	return change.Sensitive
}

type entryRefParams struct {
	Node  Ref `json:"node"`
	Entry Ref `json:"entry"`
}

func (o *EntrySetEnabled) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[entryRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	e, err := findEntry(ctx, q, n, p.Entry)
	if err != nil {
		return nil, err
	}
	if e.Enabled == o.enable {
		return nil, apierr.Conflict("nothing_to_change", "入口 %d 已经是%s状态", e.ID, map[bool]string{true: "启用", false: "停用"}[o.enable])
	}
	routes, err := routeMap(ctx, q, e.RouteID)
	if err != nil {
		return nil, err
	}
	name := relay.EntryName(n, e, routes)
	users, err := q.CountUsersOfNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	fs, err := q.ListEntryForwards(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	var steps []string
	verb := "启用"
	if o.enable {
		steps = append(steps, fmt.Sprintf("启用入口「%s」，可用时出现在 %d 名用户的订阅里", name, users))
	} else {
		verb = "停用"
		steps = append(steps, fmt.Sprintf("停用入口「%s」，%d 名用户的订阅里少一条；客户端刷新订阅前仍可连接", name, users))
	}
	if managed := countManaged(fs); managed > 0 {
		if o.enable {
			steps = append(steps, fmt.Sprintf("各跳的 patato-node 重新应用 %d 条转发", managed))
		} else {
			steps = append(steps, fmt.Sprintf("各跳的 patato-node 移除 %d 条转发，端口保留，重新启用时不变", managed))
		}
	}
	return &change.Draft{
		Summary: fmt.Sprintf("%s %s 的入口「%s」", verb, n.Name, name),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "enabled", Before: e.Enabled, After: o.enable}},
		Impact:  change.Impact{Users: users, Nodes: 1},
		Targets: []change.Target{entryTarget(e)},
	}, nil
}

func (o *EntrySetEnabled) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[entryRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	e, err := findEntry(ctx, q, n, p.Entry)
	if err != nil {
		return nil, err
	}
	if err := q.SetEntryEnabled(ctx, db.SetEntryEnabledParams{ID: e.ID, Enabled: o.enable}); err != nil {
		return nil, err
	}
	if err := q.BumpNodeVersion(ctx, n.ID); err != nil {
		return nil, err
	}
	return map[string]any{"entry_id": e.ID, "enabled": o.enable}, nil
}

type EntryDelete struct{ d *Deps }

func (*EntryDelete) Kind() string      { return "entry.delete" }
func (*EntryDelete) Risk() change.Risk { return change.Sensitive }

func (o *EntryDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[entryRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	e, err := findEntry(ctx, q, n, p.Entry)
	if err != nil {
		return nil, err
	}
	routes, err := routeMap(ctx, q, e.RouteID)
	if err != nil {
		return nil, err
	}
	fs, err := q.ListEntryForwards(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	users, err := q.CountUsersOfNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	name := relay.EntryName(n, e, routes)
	steps := []string{fmt.Sprintf("删除入口「%s」，释放 %d 条转发占用的端口", name, len(fs))}
	if managed := countManaged(fs); managed > 0 {
		steps = append(steps, fmt.Sprintf("各跳的 patato-node 在一个同步周期内移除 %d 条转发规则", managed))
	}
	if len(fs) > countManaged(fs) {
		steps = append(steps, "手写转发只删除登记，机器上的规则由运营者自己清理")
	}
	if e.Enabled {
		steps = append(steps, fmt.Sprintf("%d 名用户的订阅里少一条", users))
	}
	return &change.Draft{
		Summary: fmt.Sprintf("删除 %s 的入口「%s」", n.Name, name),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "入口", Before: name, After: nil}},
		Impact:  change.Impact{Users: users, Nodes: 1},
		Targets: []change.Target{entryTarget(e)},
	}, nil
}

func (o *EntryDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[entryRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	e, err := findEntry(ctx, q, n, p.Entry)
	if err != nil {
		return nil, err
	}
	if err := q.DeleteEntry(ctx, e.ID); err != nil {
		return nil, err
	}
	if err := q.BumpNodeVersion(ctx, n.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": e.ID}, nil
}

func countManaged(fs []db.Forward) int {
	n := 0
	for _, f := range fs {
		if f.Mode != "manual" {
			n++
		}
	}
	return n
}

func routeMap(ctx context.Context, q *db.Queries, id *int64) (map[int64]db.Route, error) {
	out := map[int64]db.Route{}
	if id == nil {
		return out, nil
	}
	r, err := q.GetRoute(ctx, *id)
	if err != nil {
		return nil, err
	}
	out[r.ID] = r
	return out, nil
}

func isCode(err error, code string) bool { return err != nil && apierr.From(err).Code == code }

func keys(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
