package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/momaek/patato/cli/internal/client"
)

// 中转线路：patato route、patato node entry、patato machine set。

const (
	epMachineNetwork = "/api/v1/machines/{id}/network"
	epRoutes         = "/api/v1/routes"
	epRoute          = "/api/v1/routes/{id}"
	epRoutesTraffic  = "/api/v1/routes/traffic"
	epRouteTraffic   = "/api/v1/routes/{id}/traffic"
	epRouteApply     = "/api/v1/routes/{id}:apply"
	epRouteDiscover  = "/api/v1/routes:discover"
	epNodeEntries    = "/api/v1/nodes/{id}/entries"
	epNodeEntry      = "/api/v1/nodes/{id}/entries/{entry}"
	epEntryEnable    = "/api/v1/nodes/{id}/entries/{entry}:enable"
	epEntryDisable   = "/api/v1/nodes/{id}/entries/{entry}:disable"
	epEntryAdopt     = "/api/v1/nodes/{id}/entries/{entry}:adopt"
)

func init() {
	usedEndpoints = append(usedEndpoints,
		"PATCH "+epMachineNetwork,
		"GET "+epRoutes, "POST "+epRoutes, "GET "+epRoute, "PATCH "+epRoute, "DELETE "+epRoute,
		"GET "+epRoutesTraffic, "GET "+epRouteTraffic, "POST "+epRouteApply, "POST "+epRouteDiscover,
		"GET "+epNodeEntries, "POST "+epNodeEntries, "DELETE "+epNodeEntry,
		"POST "+epEntryEnable, "POST "+epEntryDisable, "POST "+epEntryAdopt,
	)
}

type hopView struct {
	Host    string `json:"host"`
	Address string `json:"address"`
	State   string `json:"state"`
	Active  bool   `json:"active"`
}

type forwardView struct {
	Hop           int      `json:"hop"`
	Machine       string   `json:"machine"`
	ListenPort    int      `json:"listen_port"`
	PublicPort    int      `json:"public_port"`
	TargetHost    string   `json:"target_host"`
	TargetPort    int      `json:"target_port"`
	Mode          string   `json:"mode"`
	Status        string   `json:"status"`
	Error         string   `json:"error"`
	ProbeFailures int      `json:"probe_failures"`
	ProbeRate     *float64 `json:"probe_success_rate_24h"`
	ProbeAvgMS    *int64   `json:"probe_avg_ms_24h"`
	Mirror        bool     `json:"mirror"`
	Active        bool     `json:"active"`
}

type entryView struct {
	ID        int64         `json:"id"`
	Node      string        `json:"node"`
	Kind      string        `json:"kind"`
	Route     *ref          `json:"route"`
	Name      string        `json:"name"`
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	Enabled   bool          `json:"enabled"`
	Available bool          `json:"available"`
	Reason    string        `json:"reason"`
	Implicit  bool          `json:"implicit"`
	Forwards  []forwardView `json:"forwards"`
	TodayUp   int64         `json:"today_up"`
	TodayDown int64         `json:"today_down"`
}

type routeView struct {
	ID        int64       `json:"id"`
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Hops      []hopView   `json:"hops"`
	Peers     []hopView   `json:"entry_peers"`
	EntryAddr string      `json:"entry_address"`
	EntryIPs  []string    `json:"entry_ips"`
	Nodes     int         `json:"nodes"`
	Entries   []entryView `json:"entries"`
	TodayUp   int64       `json:"today_up"`
	TodayDown int64       `json:"today_down"`
	MonthUp   int64       `json:"month_up"`
	MonthDown int64       `json:"month_down"`
}

var hopStates = map[string]string{"online": "在线", "offline": "离线", "unknown": "未知"}

func hopText(hs []hopView) string {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = h.Host
		if h.State == "offline" {
			parts[i] += "（离线）"
		}
	}
	return strings.Join(parts, " → ")
}

// groupText 是入口组：第一跳和成员，标出入口地址当前指向的机器。
func groupText(r routeView) string {
	if len(r.Peers) == 0 {
		return ""
	}
	ms := append([]hopView{r.Hops[0]}, r.Peers...)
	parts := make([]string, len(ms))
	for i, h := range ms {
		parts[i] = h.Host
		var marks []string
		if h.Active {
			marks = append(marks, "当前解析")
		}
		if h.State == "offline" {
			marks = append(marks, "离线")
		}
		if len(marks) > 0 {
			parts[i] += "（" + strings.Join(marks, "，") + "）"
		}
	}
	return strings.Join(parts, "、")
}

// routeHops 是线路经过的机器；有入口组时第一跳写成「第一跳|成员|…」。
func routeHops(r routeView) string {
	if len(r.Peers) == 0 || len(r.Hops) == 0 {
		return hopText(r.Hops)
	}
	first := append([]hopView{r.Hops[0]}, r.Peers...)
	names := make([]string, len(first))
	for i, h := range first {
		names[i] = h.Host
		if h.State == "offline" {
			names[i] += "（离线）"
		}
	}
	s := strings.Join(names, "|")
	if rest := hopText(r.Hops[1:]); rest != "" {
		s += " → " + rest
	}
	return s
}

func entryState(e entryView) string {
	switch {
	case !e.Enabled:
		return "停用"
	case e.Available:
		return "可用"
	}
	return "不可用：" + e.Reason
}

func (a *app) route(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		return a.routeList(rest)
	case "show", "get":
		return a.routeShow(rest)
	case "create":
		return a.routeCreate(rest)
	case "update":
		return a.routeUpdate(rest)
	case "delete", "rm":
		return a.nodeRef(rest, "route delete", "patato route delete <线路>\n\n删除线路（敏感操作；线路上不能还有入口）。", "DELETE", epRoute)
	case "apply":
		return a.routeApply(rest)
	case "traffic":
		return a.routeTraffic(rest)
	case "discover":
		return a.routeDiscover(rest)
	}
	return fmt.Errorf("未知命令 route %s\n\n%s", sub, usage)
}

func (a *app) routeList(args []string) error {
	c := newCommand("route list", "patato route list\n\n线路、经过的中转、挂了几个节点、今天和本月的流量。")
	if err := c.parse(args); err != nil {
		return err
	}
	var l list[routeView]
	if printed, err := a.get(epRoutes, nil, &l); printed || err != nil {
		return err
	}
	if len(l.Items) == 0 {
		a.printf("还没有线路；用 patato route create 新建\n")
		return nil
	}
	rows := [][]string{}
	for _, r := range l.Items {
		avail := 0
		for _, e := range r.Entries {
			if e.Enabled && e.Available {
				avail++
			}
		}
		rows = append(rows, []string{r.Code, r.Name, routeHops(r), strconv.Itoa(r.Nodes),
			fmt.Sprintf("%d/%d", avail, len(r.Entries)), bytesText(r.TodayUp + r.TodayDown), bytesText(r.MonthUp + r.MonthDown)})
	}
	table(a.out, []string{"代号", "名称", "经过", "节点", "可用入口", "今日", "本月"}, rows, 3)
	return nil
}

func (a *app) routeShow(args []string) error {
	c := newCommand("route show", "patato route show <线路>")
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "线路")
	if err != nil {
		return err
	}
	var r routeView
	if printed, err := a.get(client.Path(epRoute, ref), nil, &r); printed || err != nil {
		return err
	}
	pairs := [][2]string{{"线路", r.Name + "（" + r.Code + "）"}, {"经过", hopText(r.Hops)}}
	if len(r.Peers) > 0 {
		ips := strings.Join(r.EntryIPs, ", ")
		if ips == "" {
			ips = "还没解析"
		}
		pairs = append(pairs, [2]string{"入口组", groupText(r)}, [2]string{"入口地址", r.EntryAddr + " → " + ips})
	} else if r.EntryAddr != "" {
		pairs = append(pairs, [2]string{"入口地址", r.EntryAddr})
	}
	pairs = append(pairs, [2]string{"今日流量", bytesText(r.TodayUp + r.TodayDown)}, [2]string{"本月流量", bytesText(r.MonthUp + r.MonthDown)})
	kv(a.out, pairs)
	a.printEntries(r.Entries, true)
	return nil
}

func (a *app) printEntries(es []entryView, withNode bool) {
	if len(es) == 0 {
		a.printf("\n没有入口\n")
		return
	}
	a.printf("\n")
	rows := [][]string{}
	for _, e := range es {
		id := strconv.FormatInt(e.ID, 10)
		if e.Implicit {
			id = "—"
		}
		row := []string{id}
		if withNode {
			row = append(row, e.Node)
		}
		row = append(row, e.Name, fmt.Sprintf("%s:%d", e.Host, e.Port), entryState(e), bytesText(e.TodayUp+e.TodayDown))
		rows = append(rows, row)
		for _, f := range e.Forwards {
			port := strconv.Itoa(f.ListenPort)
			if f.PublicPort != f.ListenPort {
				port += fmt.Sprintf("（外网 %d）", f.PublicPort)
			}
			st := f.Status
			if f.Error != "" {
				st += "：" + f.Error
			}
			if f.ProbeRate != nil {
				st += fmt.Sprintf("，24h 探测 %.0f%%", *f.ProbeRate*100)
				if f.ProbeAvgMS != nil {
					st += fmt.Sprintf(" %dms", *f.ProbeAvgMS)
				}
			}
			sub := []string{""}
			if withNode {
				sub = append(sub, "")
			}
			label := fmt.Sprintf("%d.", f.Hop+1)
			if f.Mirror {
				label = "1'"
			}
			if f.Active {
				st += "，当前解析"
			}
			sub = append(sub, fmt.Sprintf("  └ %s %s:%s → %s:%d", label, f.Machine, port, f.TargetHost, f.TargetPort), f.Mode, st, "")
			rows = append(rows, sub)
		}
	}
	head := []string{"入口"}
	if withNode {
		head = append(head, "节点")
	}
	table(a.out, append(head, "名称", "地址", "状态", "今日"), rows, 0)
}

func (a *app) routeCreate(args []string) error {
	c := newCommand("route create", `patato route create <代号> --name <名称> --via <机器>,<机器>... [--entry-peers <机器>,... --entry-address <域名>]

新建线路：按客户端到落地的顺序列出中转机器。线路本身不影响订阅。
入口组：--entry-peers 列出和第一跳一起承担入口的其他机器，它们共用 --entry-address 这个域名，
域名解析到哪台都能连上（每个入口在这些机器上用同一个对外端口）。切换由运营者改 DNS 完成。`)
	nm := c.fs.String("name", "", "线路名称，订阅里入口名为「节点名 · 线路名」")
	via := c.fs.String("via", "", "经过的中转机器，逗号分隔，按顺序")
	peers := c.fs.String("entry-peers", "", "入口组：和第一跳一起承担入口的其他机器，逗号分隔")
	addr := c.fs.String("entry-address", "", "订阅里的入口地址（解析到入口组机器的域名）")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	code, err := c.arg(0, "代号")
	if err != nil {
		return err
	}
	body := map[string]any{"code": code, "name": *nm, "hops": splitList(*via)}
	if c.set["entry-peers"] {
		body["entry_peers"] = splitList(*peers)
	}
	if c.set["entry-address"] {
		body["entry_address"] = *addr
	}
	return a.mutate("POST", epRoutes, body, w)
}

func (a *app) routeUpdate(args []string) error {
	c := newCommand("route update", "patato route update <线路> [--name ...] [--via ...] [--entry-peers ...] [--entry-address ...]\n\n改名称为常规操作。--via 给出完整的新跳列表，可以增加或删除跳：线路上已有入口时就地修改每个入口的转发，\n保留的跳端口和规则不变，新增的跳优先沿用订阅或相邻跳的端口，删掉的跳由上一跳直接转到下一跳；保留的机器不能调换顺序。\n有入口时为敏感操作，需要审批。\n--entry-peers 给出完整的入口组成员列表（空串取消入口组）：新成员给每个入口建同端口的镜像转发；\n去掉成员和改已在使用的 --entry-address 为敏感操作。改跳和改入口组要分两次。")
	nm := c.fs.String("name", "", "线路名称")
	via := c.fs.String("via", "", "经过的中转机器，逗号分隔")
	peers := c.fs.String("entry-peers", "", "入口组成员，逗号分隔，完整列表")
	addr := c.fs.String("entry-address", "", "入口地址")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "线路")
	if err != nil {
		return err
	}
	body := map[string]any{}
	if c.set["name"] {
		body["name"] = *nm
	}
	if c.set["via"] {
		body["hops"] = splitList(*via)
	}
	if c.set["entry-peers"] {
		body["entry_peers"] = splitList(*peers)
	}
	if c.set["entry-address"] {
		body["entry_address"] = *addr
	}
	return a.mutate("PATCH", client.Path(epRoute, ref), body, w)
}

func (a *app) routeApply(args []string) error {
	c := newCommand("route apply", "patato route apply <线路> (--plan <套餐> | --node <节点>...)\n\n让套餐里（或指定）的每个节点经这条线路各开一个入口，端口由服务端分配。")
	plan := c.fs.String("plan", "", "套餐")
	var nodes multi
	c.fs.Var(&nodes, "node", "节点，可重复")
	suffix := c.fs.String("suffix", "", "入口名后缀，默认线路名")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "线路")
	if err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"plan": *plan, "suffix": *suffix})
	if len(nodes) > 0 {
		body["nodes"] = []string(nodes)
	}
	return a.mutate("POST", client.Path(epRouteApply, ref), body, w)
}

func (a *app) routeTraffic(args []string) error {
	c := newCommand("route traffic", "patato route traffic [<线路>] [--since 7d]\n\n线路每天的流量（按第一跳的线上字节统计）。")
	since := c.fs.String("since", "7d", "统计多少天，例如 7d、30d")
	if err := c.parse(args); err != nil {
		return err
	}
	days, err := parseDays(*since)
	if err != nil {
		return err
	}
	ep := epRoutesTraffic
	if len(c.fs.Args()) > 0 {
		ep = client.Path(epRouteTraffic, c.fs.Arg(0))
	}
	var out struct {
		Items []struct {
			Route  ref   `json:"route"`
			Up     int64 `json:"up"`
			Down   int64 `json:"down"`
			Series []struct {
				Day  string `json:"day"`
				Up   int64  `json:"up"`
				Down int64  `json:"down"`
			} `json:"series"`
		} `json:"items"`
	}
	if printed, err := a.get(ep, query("days", strconv.Itoa(days)), &out); printed || err != nil {
		return err
	}
	if len(out.Items) == 0 {
		a.printf("没有线路\n")
		return nil
	}
	rows := [][]string{}
	for _, r := range out.Items {
		rows = append(rows, []string{r.Route.Code, r.Route.Name, bytesText(r.Up), bytesText(r.Down), bytesText(r.Up + r.Down)})
	}
	table(a.out, []string{"代号", "名称", "上行", "下行", "合计"}, rows, 2)
	if len(out.Items) == 1 {
		a.printf("\n")
		rows = rows[:0]
		for _, p := range out.Items[0].Series {
			rows = append(rows, []string{p.Day, bytesText(p.Up), bytesText(p.Down)})
		}
		table(a.out, []string{"日期", "上行", "下行"}, rows, 1)
	}
	return nil
}

// parseDays 解析 7d、30d、168h 这样的时长，返回天数。
func parseDays(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch {
	case strings.HasSuffix(s, "d"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 {
			return n, nil
		}
	case strings.HasSuffix(s, "h"):
		n, err := strconv.Atoi(strings.TrimSuffix(s, "h"))
		if err == nil && n > 0 {
			return max((n+23)/24, 1), nil
		}
	default:
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n, nil
		}
	}
	return 0, fmt.Errorf("--since %q 格式应为 7d", s)
}

func (a *app) routeDiscover(args []string) error {
	c := newCommand("route discover", `patato route discover --node <节点> (--entry <入口> | --host <地址> --port <端口>) [--snapshot <机器>=@<文件>]...

链路发现（只读）：从入口第一跳出发，按各机器的转发现状逐跳追到落地节点。
不带 --snapshot 时输出要在每台机器上执行的只读采集命令；把每台机器的输出存成文件，再用 --snapshot 交回来。
Patato 不远程执行，采集由 Agent 经 tolato exec 完成：tolato exec <机器> -- "<采集命令>" > <机器>.txt`)
	node := c.fs.String("node", "", "节点")
	entry := c.fs.Int64("entry", 0, "入口 ID")
	host := c.fs.String("host", "", "入口地址")
	port := c.fs.Int("port", 0, "入口端口")
	var snaps multi
	c.fs.Var(&snaps, "snapshot", "机器名=@文件，可重复")
	if err := c.parse(args); err != nil {
		return err
	}
	body := map[string]any{"node": *node, "entry": *entry, "host": *host, "port": *port}
	if len(snaps) > 0 {
		m := map[string]string{}
		for _, s := range snaps {
			name, val, ok := strings.Cut(s, "=")
			if !ok {
				return fmt.Errorf("--snapshot %q 格式应为 机器名=@文件", s)
			}
			content, err := readValue(val)
			if err != nil {
				return err
			}
			m[name] = content
		}
		body["snapshots"] = m
	}
	var out map[string]any
	resp, err := a.c.Do(a.ctx, "POST", epRouteDiscover, nil, body, &out)
	if err != nil {
		return err
	}
	printJSON(a.out, resp.Body)
	return nil
}

// ---- patato node entry ---------------------------------------------------

func (a *app) nodeEntry(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("node entry list", "patato node entry list <节点>")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "节点")
		if err != nil {
			return err
		}
		var l list[entryView]
		if printed, err := a.get(client.Path(epNodeEntries, ref), nil, &l); printed || err != nil {
			return err
		}
		a.printEntries(l.Items, false)
		return nil
	case "add":
		return a.entryAdd(rest)
	case "enable", "disable", "delete", "rm":
		ep := map[string]string{"enable": epEntryEnable, "disable": epEntryDisable, "delete": epNodeEntry, "rm": epNodeEntry}[sub]
		method := "POST"
		if ep == epNodeEntry {
			method = "DELETE"
		}
		c := newCommand("node entry "+sub, fmt.Sprintf("patato node entry %s <节点> <入口>\n\n停用和删除是敏感操作：用户的订阅里会少一条。", sub))
		w := c.writeFlags()
		if err := c.parse(rest); err != nil {
			return err
		}
		node, err := c.arg(0, "节点")
		if err != nil {
			return err
		}
		entry, err := c.arg(1, "入口")
		if err != nil {
			return err
		}
		return a.mutate(method, strings.Replace(client.Path(ep, node), "{entry}", entry, 1), nil, w)
	case "adopt":
		return a.entryAdopt(rest)
	}
	return fmt.Errorf("未知命令 node entry %s\n\n%s", sub, usage)
}

func (a *app) entryAdd(args []string) error {
	c := newCommand("node entry add", `patato node entry add <节点> [--route <线路>] [--suffix ...] [--name ...] [--address <地址> --port <端口>]
  [--manual --forward <机器>:<端口>=<下一跳>:<端口>...]

经线路的入口由服务端在各跳分配端口并生成转发，同时给 --address/--port 时订阅里改用这个地址和端口
（第一跳前面还有一层自管转发时，例如入口机整段 DNAT 到中转）；只给 --address/--port 是自定义入口（例如 IPLC），状态只跟随节点；
--manual 登记运营者手写的转发（不下发、不计流量），--forward 按线路顺序每跳一个。`)
	route := c.fs.String("route", "", "线路")
	suffix := c.fs.String("suffix", "", "入口名后缀，订阅里为「节点名 · 后缀」，默认线路名")
	nm := c.fs.String("name", "", "订阅里的完整名称")
	address := c.fs.String("address", "", "入口地址；经线路时覆盖订阅里的第一跳地址")
	port := c.fs.Int("port", 0, "入口端口；经线路时覆盖订阅里的第一跳外网端口")
	sort := c.fs.Int("sort", 0, "排序")
	manual := c.fs.Bool("manual", false, "登记手写转发")
	var forwards multi
	c.fs.Var(&forwards, "forward", "手写转发：机器:端口=下一跳:端口，可重复")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	node, err := c.arg(0, "节点")
	if err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"route": *route, "suffix": *suffix, "name": *nm, "address": *address, "port": *port, "sort": *sort, "manual": *manual})
	if len(forwards) > 0 {
		fs, err := parseForwards(forwards)
		if err != nil {
			return err
		}
		body["forwards"] = fs
	}
	return a.mutate("POST", client.Path(epNodeEntries, node), body, w)
}

func (a *app) entryAdopt(args []string) error {
	c := newCommand("node entry adopt", "patato node entry adopt <节点> <入口> --route <线路> --forward <机器>:<端口>=<下一跳>:<端口>...\n\n链路发现确认后，把自定义入口登记为经过线路的手写转发；订阅内容不变。")
	route := c.fs.String("route", "", "线路")
	var forwards multi
	c.fs.Var(&forwards, "forward", "手写转发：机器:端口=下一跳:端口，按线路顺序，可重复")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	node, err := c.arg(0, "节点")
	if err != nil {
		return err
	}
	entry, err := c.arg(1, "入口")
	if err != nil {
		return err
	}
	fs, err := parseForwards(forwards)
	if err != nil {
		return err
	}
	return a.mutate("POST", strings.Replace(client.Path(epEntryAdopt, node), "{entry}", entry, 1),
		map[string]any{"route": *route, "forwards": fs}, w)
}

// parseForwards 解析「机器:端口=下一跳:端口」。
func parseForwards(list []string) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, s := range list {
		l, r, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("--forward %q 格式应为 机器:端口=下一跳:端口", s)
		}
		lh, lp, err1 := splitHostPort(l)
		rh, rp, err2 := splitHostPort(r)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("--forward %q 格式应为 机器:端口=下一跳:端口", s)
		}
		out = append(out, map[string]any{"machine": lh, "listen_port": lp, "target_host": rh, "target_port": rp})
	}
	return out, nil
}

func splitHostPort(s string) (string, int, error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 {
		return "", 0, fmt.Errorf("missing port")
	}
	p, err := strconv.Atoi(s[i+1:])
	return strings.Trim(s[:i], "[]"), p, err
}

// ---- patato machine set ----------------------------------------------------

func (a *app) machineSet(args []string) error {
	c := newCommand("machine set", `patato machine set <机器> [--public-address <IP 或域名>] [--port-range <本机段>[:外网起点]]... [--nat]
  [--reserve <段>]... [--forward-mode auto|nft|userspace]

设置机器的网络属性。--port-range 可重复，例如 --port-range 40000-49999、--port-range 20000-20099:30000（外网 30000 对应本机 20000）；
传一次空值 --port-range '' 恢复默认段 40000-49999。--reserve 登记机器上现有规则占用的端口段，分配时避开。
修改已在使用的入口机器的对外地址或端口映射会改变订阅，是敏感操作。`)
	addr := c.fs.String("public-address", "", "对外地址；空串表示用 tolato 上报的 IP")
	var ranges, reserve multi
	c.fs.Var(&ranges, "port-range", "可分配的端口段，可重复")
	c.fs.Var(&reserve, "reserve", "现有规则占用的端口段，可重复")
	nat := c.fs.Bool("nat", false, "NAT 机器：只在 --port-range 内分配")
	mode := c.fs.String("forward-mode", "", "auto、nft 或 userspace")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	host, err := c.arg(0, "机器")
	if err != nil {
		return err
	}
	body := map[string]any{}
	if c.set["public-address"] {
		body["public_address"] = *addr
	}
	if c.set["nat"] {
		body["nat"] = *nat
	}
	if c.set["forward-mode"] {
		body["forward_mode"] = *mode
	}
	if c.set["port-range"] {
		rs, err := parseRanges(ranges)
		if err != nil {
			return err
		}
		body["port_ranges"] = rs
	}
	if c.set["reserve"] {
		rs, err := parseRanges(reserve)
		if err != nil {
			return err
		}
		body["reserved_ports"] = rs
	}
	return a.mutate("PATCH", client.Path(epMachineNetwork, host), body, w)
}

// parseRanges 解析「40000-49999」或「20000-20099:30000」。空值表示清空。
func parseRanges(list []string) ([]map[string]any, error) {
	out := []map[string]any{}
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		local, pub, hasPub := strings.Cut(s, ":")
		r := map[string]any{"local": local}
		if hasPub {
			n, err := strconv.Atoi(pub)
			if err != nil {
				return nil, fmt.Errorf("端口段 %q 的外网起点不是数字", s)
			}
			r["public_from"] = n
		}
		out = append(out, r)
	}
	return out, nil
}

// readValue 读取 @文件 或原样返回。
func readValue(v string) (string, error) {
	if strings.HasPrefix(v, "@") {
		b, err := os.ReadFile(v[1:])
		return string(b), err
	}
	return v, nil
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
