package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/relay"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

// 中转线路的只读视图：线路、入口、转发、线路流量和链路发现。写操作走 mutate（变更计划）。

type forwardView struct {
	ID            int64      `json:"id"`
	Hop           int32      `json:"hop"`
	Machine       string     `json:"machine"`
	MachineState  string     `json:"machine_state"`
	ListenPort    int32      `json:"listen_port"`
	PublicPort    int32      `json:"public_port"`
	TargetHost    string     `json:"target_host"`
	TargetIP      string     `json:"target_ip"`
	TargetPort    int32      `json:"target_port"`
	Mode          string     `json:"mode"`
	Status        string     `json:"status"`
	AppliedMode   string     `json:"applied_mode"`
	AppliedAt     *time.Time `json:"applied_at"`
	Error         string     `json:"error"`
	ProbeOK       *bool      `json:"probe_ok"`
	ProbeMS       *int32     `json:"probe_ms"`
	ProbeAt       *time.Time `json:"probe_at"`
	ProbeFailures int32      `json:"probe_failures"`
	// 最近 24 小时的探测成功率（0–1）与平均延迟；没有探测时为 null
	ProbeRate  *float64 `json:"probe_success_rate_24h"`
	ProbeAvgMS *int64   `json:"probe_avg_ms_24h"`
	TodayUp    int64    `json:"today_up"`
	TodayDown  int64    `json:"today_down"`
}

type entryView struct {
	ID        int64         `json:"id"`
	NodeID    int64         `json:"node_id"`
	Node      string        `json:"node"`
	Kind      relay.Kind    `json:"kind"`
	Route     *ref          `json:"route"`
	Name      string        `json:"name"`
	Suffix    string        `json:"suffix"`
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	Enabled   bool          `json:"enabled"`
	Available bool          `json:"available"`
	Reason    string        `json:"reason,omitempty"`
	Sort      int32         `json:"sort"`
	Implicit  bool          `json:"implicit,omitempty"`
	Forwards  []forwardView `json:"forwards"`
	TodayUp   int64         `json:"today_up"`
	TodayDown int64         `json:"today_down"`
	Version   int64         `json:"version"`
}

type routeView struct {
	ID        int64       `json:"id"`
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Hops      []hopView   `json:"hops"`
	Nodes     int         `json:"nodes"`
	Entries   []entryView `json:"entries"`
	TodayUp   int64       `json:"today_up"`
	TodayDown int64       `json:"today_down"`
	MonthUp   int64       `json:"month_up"`
	MonthDown int64       `json:"month_down"`
	Version   int64       `json:"version"`
}

type hopView struct {
	ID      int64  `json:"id"`
	Host    string `json:"host"`
	Address string `json:"address"`
	State   string `json:"state"`
}

// relayData 是构造视图需要的全部数据。
type relayData struct {
	world   *relay.World
	nodes   map[int64]db.Node
	entries []db.NodeEntry
	traffic map[int64][2]int64 // 转发今日流量
	probes  map[int64]db.ForwardProbeStatsRow
}

func (s *Server) loadRelay(c *gin.Context) (*relayData, error) {
	w, err := relay.Load(c, s.d.Store.Queries)
	if err != nil {
		return nil, err
	}
	nodes, err := s.d.Store.ListNodes(c)
	if err != nil {
		return nil, err
	}
	entries, err := s.d.Store.ListEntries(c)
	if err != nil {
		return nil, err
	}
	d := &relayData{world: w, nodes: map[int64]db.Node{}, entries: entries, traffic: map[int64][2]int64{},
		probes: map[int64]db.ForwardProbeStatsRow{}}
	for _, n := range nodes {
		d.nodes[n.ID] = n
	}
	rows, err := s.d.Store.ForwardTrafficSince(c, s.startOfToday())
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		d.traffic[r.ForwardID] = [2]int64{r.Up, r.Down}
	}
	ids := []int64{}
	for _, fs := range w.Forwards {
		for _, f := range fs {
			ids = append(ids, f.ID)
		}
	}
	if len(ids) > 0 {
		ps, err := s.d.Store.ForwardProbeStats(c, db.ForwardProbeStatsParams{Since: time.Now().Add(-24 * time.Hour), ForwardIds: ids})
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			d.probes[p.ForwardID] = p
		}
	}
	return d, nil
}

// startOfToday 是配置时区里今天 0 点。
func (s *Server) startOfToday() time.Time {
	t := time.Now().In(s.loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, s.loc)
}

func (d *relayData) entryView(n db.Node, e db.NodeEntry, implicit bool) entryView {
	ep := d.world.Resolve(n, e)
	v := entryView{ID: e.ID, NodeID: n.ID, Node: n.Name, Kind: relay.EntryKind(e), Name: ep.Name, Suffix: e.Suffix, Host: ep.Host,
		Port: ep.Port, Enabled: e.Enabled, Available: ep.Available, Reason: ep.Reason, Sort: e.Sort, Implicit: implicit,
		Forwards: []forwardView{}, Version: e.Version}
	if e.RouteID != nil {
		if r, ok := d.world.Routes[*e.RouteID]; ok {
			v.Route = &ref{ID: r.ID, Code: r.Code, Name: r.Name}
		}
	}
	for _, f := range d.world.Forwards[e.ID] {
		m := d.world.Machines[f.MachineID]
		fv := forwardView{ID: f.ID, Hop: f.Hop, Machine: m.Host, MachineState: m.State, ListenPort: f.ListenPort, PublicPort: f.PublicPort,
			TargetHost: f.TargetHost, TargetIP: f.TargetIp, TargetPort: f.TargetPort, Mode: f.Mode, Status: f.Status,
			AppliedMode: f.AppliedMode, AppliedAt: f.AppliedAt, Error: f.Error, ProbeOK: f.ProbeOk, ProbeMS: f.ProbeMs, ProbeAt: f.ProbeAt,
			ProbeFailures: f.ProbeFailures}
		if f.Mode == "manual" {
			fv.Status = "manual"
		}
		if p, ok := d.probes[f.ID]; ok && p.Ok+p.Fail > 0 {
			rate := float64(p.Ok) / float64(p.Ok+p.Fail)
			avg := int64(p.AvgMs)
			fv.ProbeRate, fv.ProbeAvgMS = &rate, &avg
		}
		t := d.traffic[f.ID]
		fv.TodayUp, fv.TodayDown = t[0], t[1]
		if f.Hop == 0 {
			v.TodayUp, v.TodayDown = t[0], t[1]
		}
		v.Forwards = append(v.Forwards, fv)
	}
	return v
}

// nodeEntries 返回节点的入口；没有入口时返回那个隐含的直连入口（implicit 为 true，ID 为 0）。
func (d *relayData) nodeEntries(n db.Node) []entryView {
	var out []entryView
	for _, e := range d.entries {
		if e.NodeID == n.ID {
			out = append(out, d.entryView(n, e, false))
		}
	}
	if len(out) == 0 {
		out = append(out, d.entryView(n, relay.Implicit(n), true))
	}
	return out
}

func (s *Server) routeViews(c *gin.Context, only *db.Route) ([]routeView, error) {
	d, err := s.loadRelay(c)
	if err != nil {
		return nil, err
	}
	today, err := s.d.Store.RouteTrafficSince(c, s.startOfToday())
	if err != nil {
		return nil, err
	}
	monthStart := s.startOfToday().AddDate(0, 0, 1-s.startOfToday().Day())
	month, err := s.d.Store.RouteTrafficSince(c, monthStart)
	if err != nil {
		return nil, err
	}
	sum := func(rows []db.RouteTrafficSinceRow, id int64) (int64, int64) {
		for _, r := range rows {
			if r.RouteID == id {
				return r.Up, r.Down
			}
		}
		return 0, 0
	}
	routes, err := s.d.Store.ListRoutes(c)
	if err != nil {
		return nil, err
	}
	out := []routeView{}
	for _, r := range routes {
		if only != nil && r.ID != only.ID {
			continue
		}
		v := routeView{ID: r.ID, Code: r.Code, Name: r.Name, Hops: []hopView{}, Entries: []entryView{}, Version: r.Version}
		for _, id := range r.Hops {
			m := d.world.Machines[id]
			v.Hops = append(v.Hops, hopView{ID: m.ID, Host: m.Host, Address: relay.Address(m), State: m.State})
		}
		nodes := map[int64]bool{}
		for _, e := range d.entries {
			if e.RouteID != nil && *e.RouteID == r.ID {
				v.Entries = append(v.Entries, d.entryView(d.nodes[e.NodeID], e, false))
				nodes[e.NodeID] = true
			}
		}
		v.Nodes = len(nodes)
		v.TodayUp, v.TodayDown = sum(today, r.ID)
		v.MonthUp, v.MonthDown = sum(month, r.ID)
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) routeList(c *gin.Context) {
	views, err := s.routeViews(c, nil)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"items": views})
}

func (s *Server) findRoute(c *gin.Context) (db.Route, bool) {
	key := c.Param("id")
	var r db.Route
	var err error
	if id, perr := strconv.ParseInt(key, 10, 64); perr == nil {
		r, err = s.d.Store.GetRoute(c, id)
	} else {
		r, err = s.d.Store.GetRouteByCode(c, key)
	}
	if store.IsNotFound(err) {
		fail(c, apierr.NotFound("route_not_found", "线路 %s 不存在", key).WithHint("用 GET /api/v1/routes 查看全部线路"))
		return r, false
	}
	if err != nil {
		fail(c, err)
		return r, false
	}
	return r, true
}

func (s *Server) routeGet(c *gin.Context) {
	r, found := s.findRoute(c)
	if !found {
		return
	}
	views, err := s.routeViews(c, &r)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, views[0])
}

// routeTraffic 返回线路每天的流量（天表）。不带 id 时返回全部线路。
func (s *Server) routeTraffic(c *gin.Context) {
	days := queryInt(c, "days", 7, 1, 366)
	since := s.today().AddDate(0, 0, -(days - 1))
	rows, err := s.d.Store.RouteTrafficDaily(c, since)
	if err != nil {
		fail(c, err)
		return
	}
	routes, err := s.d.Store.ListRoutes(c)
	if err != nil {
		fail(c, err)
		return
	}
	var only int64
	if c.Param("id") != "" {
		r, found := s.findRoute(c)
		if !found {
			return
		}
		only = r.ID
	}
	type point struct {
		Day  string `json:"day"`
		Up   int64  `json:"up"`
		Down int64  `json:"down"`
	}
	type series struct {
		Route  ref     `json:"route"`
		Up     int64   `json:"up"`
		Down   int64   `json:"down"`
		Series []point `json:"series"`
	}
	out := []series{}
	for _, r := range routes {
		if only != 0 && r.ID != only {
			continue
		}
		byDay := map[string][2]int64{}
		for _, row := range rows {
			if row.RouteID == r.ID {
				byDay[row.Day.Format("2006-01-02")] = [2]int64{row.Up, row.Down}
			}
		}
		sr := series{Route: ref{ID: r.ID, Code: r.Code, Name: r.Name}, Series: make([]point, 0, days)}
		for i := 0; i < days; i++ {
			d := since.AddDate(0, 0, i).Format("2006-01-02")
			v := byDay[d]
			sr.Series = append(sr.Series, point{Day: d, Up: v[0], Down: v[1]})
			sr.Up += v[0]
			sr.Down += v[1]
		}
		out = append(out, sr)
	}
	ok(c, gin.H{"days": days, "since": since.Format("2006-01-02"), "items": out})
}

func (s *Server) nodeEntryList(c *gin.Context) {
	n, found := s.findNode(c)
	if !found {
		return
	}
	d, err := s.loadRelay(c)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"items": d.nodeEntries(n)})
}

// entryMutate 把路径里的节点和入口填进参数。
func entryRef(c *gin.Context) map[string]any {
	return map[string]any{"node": c.Param("id"), "entry": c.Param("entry")}
}

// ---- 机器的网络属性与转发 -------------------------------------------------

type machineRelayView struct {
	PublicAddress  string            `json:"public_address"`
	Address        string            `json:"address"`
	PortRanges     []relay.PortRange `json:"port_ranges"`
	EffectiveRange []relay.PortRange `json:"effective_port_ranges"`
	ReservedPorts  []relay.PortRange `json:"reserved_ports"`
	NAT            bool              `json:"nat"`
	ForwardMode    string            `json:"forward_mode"`
	TolatoIP       string            `json:"tolato_ip"`
	State          string            `json:"state"`
	Runtime        json.RawMessage   `json:"runtime"`
	Forwards       int               `json:"forwards"`
	Failed         int               `json:"failed_forwards"`
	PortsUsed      int               `json:"ports_used"`
	PortsTotal     int               `json:"ports_total"`
	TodayUp        int64             `json:"today_up"`
	TodayDown      int64             `json:"today_down"`
	MonthUp        int64             `json:"month_up"`
	MonthDown      int64             `json:"month_down"`
}

// machineRelay 补上机器的网络属性和转发汇总，没有任何中转相关数据的机器也返回网络属性。
func (s *Server) machineRelay(ctx context.Context, m db.Machine, today, month map[int64][2]int64) (*machineRelayView, error) {
	pr, _ := relay.ParseRanges(m.PortRanges)
	rp, _ := relay.ParseRanges(m.ReservedPorts)
	v := &machineRelayView{PublicAddress: m.PublicAddress, Address: relay.Address(m), PortRanges: nonNilRanges(pr),
		EffectiveRange: nonNilRanges(relay.Ranges(m)), ReservedPorts: nonNilRanges(rp), NAT: m.Nat, ForwardMode: m.ForwardMode,
		TolatoIP: m.TolatoIp, State: m.State, Runtime: m.RelayRuntime}
	fs, err := s.d.Store.ListMachineForwards(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		v.Forwards++
		if f.Status == "failed" && f.Mode != "manual" {
			v.Failed++
		}
		for _, r := range relay.Ranges(m) {
			if r.Contains(int(f.ListenPort)) {
				v.PortsUsed++
			}
		}
	}
	for _, r := range relay.Ranges(m) {
		v.PortsTotal += r.To - r.From + 1
	}
	t, mo := today[m.ID], month[m.ID]
	v.TodayUp, v.TodayDown, v.MonthUp, v.MonthDown = t[0], t[1], mo[0], mo[1]
	return v, nil
}

func nonNilRanges(r []relay.PortRange) []relay.PortRange {
	if r == nil {
		return []relay.PortRange{}
	}
	return r
}

func (s *Server) machineTraffic(ctx context.Context) (today, month map[int64][2]int64, err error) {
	load := func(since time.Time) (map[int64][2]int64, error) {
		rows, err := s.d.Store.MachineForwardTrafficSince(ctx, since)
		if err != nil {
			return nil, err
		}
		out := map[int64][2]int64{}
		for _, r := range rows {
			out[r.MachineID] = [2]int64{r.Up, r.Down}
		}
		return out, nil
	}
	if today, err = load(s.startOfToday()); err != nil {
		return nil, nil, err
	}
	month, err = load(s.startOfToday().AddDate(0, 0, 1-s.startOfToday().Day()))
	return today, month, err
}

// ---- 链路发现 -------------------------------------------------------------

// discoverRequest 是链路发现的输入：入口地址（通常是 Xboard 子节点的地址）和沿途机器的采集结果。
// 采集结果由 Agent 在每台机器上经 tolato exec 执行 discover_command 得到，Patato 自己不远程执行。
type discoverRequest struct {
	Node      string            `json:"node"`
	Entry     int64             `json:"entry"`
	Host      string            `json:"host"`
	Port      int               `json:"port"`
	Snapshots map[string]string `json:"snapshots"`
}

// routeDiscover 是只读的链路发现：从入口第一跳出发，按各机器的转发现状逐跳追到落地节点，返回跳列表、
// 每跳端口和下一跳，以及可以直接执行的登记参数（交给运营者确认后用 entry.adopt 登记为手写转发）。
func (s *Server) routeDiscover(c *gin.Context) {
	var req discoverRequest
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	if len(req.Snapshots) == 0 {
		ok(c, gin.H{"discover_command": relay.DiscoverCommand,
			"next": "在入口机器以及沿途每台中转机器上用 tolato exec 执行 discover_command（只读），把输出按机器名放进 snapshots 再调用一次"})
		return
	}
	var n db.Node
	var err error
	if id, perr := strconv.ParseInt(req.Node, 10, 64); perr == nil {
		n, err = s.d.Store.GetNode(c, id)
	} else {
		n, err = s.d.Store.GetNodeByCode(c, req.Node)
	}
	if err != nil {
		fail(c, apierr.NotFound("node_not_found", "节点 %s 不存在", req.Node))
		return
	}
	host, port := strings.TrimSpace(req.Host), req.Port
	if req.Entry != 0 {
		e, err := s.d.Store.GetEntry(c, req.Entry)
		if err != nil || e.NodeID != n.ID {
			fail(c, apierr.NotFound("entry_not_found", "节点 %s 没有入口 %d", n.Code, req.Entry))
			return
		}
		host = firstNonEmptyStr(host, e.Address, n.Address)
		if port == 0 {
			port = int(e.Port)
		}
	}
	if host == "" || port == 0 {
		fail(c, apierr.BadRequest("missing_start", "缺少入口地址和端口").WithHint("传 entry（入口 ID）或 host + port"))
		return
	}
	machines, err := s.d.Store.ListMachines(c)
	if err != nil {
		fail(c, err)
		return
	}
	addrs := map[string][]string{}
	var landing relay.Landing
	landing.Addrs = []string{n.Address}
	landing.Ports = []int{int(n.Port), int(n.ServerPort)}
	for _, m := range machines {
		addrs[m.Host] = []string{m.Host}
		for _, a := range []string{m.PublicAddress, m.TolatoIp} {
			if a != "" {
				addrs[m.Host] = append(addrs[m.Host], a)
			}
		}
		if m.Host == n.Host {
			landing.Addrs = append(landing.Addrs, addrs[m.Host]...)
		}
	}
	var snaps []relay.Snapshot
	for m, out := range req.Snapshots {
		snaps = append(snaps, relay.ParseSnapshot(m, out))
	}
	tr := relay.TraceRoute(c, netResolver{}, host, port, snaps, addrs, landing)
	resp := gin.H{"trace": tr, "snapshots": snaps}
	if tr.Complete && len(tr.Hops) > 0 {
		hops := make([]string, len(tr.Hops))
		fwd := make([]gin.H, len(tr.Hops))
		for i, h := range tr.Hops {
			hops[i] = h.Machine
			fwd[i] = gin.H{"machine": h.Machine, "listen_port": h.ListenPort, "target_host": h.TargetHost, "target_port": h.TargetPort}
		}
		resp["hops"] = hops
		resp["adopt"] = gin.H{"node": n.Code, "entry": req.Entry, "forwards": fwd,
			"next": fmt.Sprintf("确认无误后：patato route create <代号> --name <名称> --via %s；再对入口执行 entry.adopt 登记这些手写转发；"+
				"reserved 里的端口段用 patato machine set <机器> --reserve 登记，分配新端口时避开", strings.Join(hops, ","))}
	}
	ok(c, resp)
}

type netResolver struct{}

func (netResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
