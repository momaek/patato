package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/cli/internal/client"
)

// ---- node ---------------------------------------------------------------

func (a *app) node(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		return a.nodeList(rest)
	case "show", "get":
		return a.nodeShow(rest)
	case "provision":
		return a.nodeProvision(rest)
	case "update":
		return a.nodeUpdate(rest)
	case "attach", "detach":
		return a.nodeAttach(sub, rest)
	case "rotate-key":
		return a.nodeRef(rest, "node rotate-key", "patato node rotate-key <节点>\n\n轮换节点密钥，旧密钥立即失效；之后要用新的安装命令更新机器上的配置。", "POST", epNodeRotate)
	case "delete", "rm":
		return a.nodeRef(rest, "node delete", "patato node delete <节点>\n\n删除节点（敏感操作，需要审批）。", "DELETE", epNode)
	case "entry", "entries":
		return a.nodeEntry(rest)
	case "install-cmd":
		return a.installCmd(rest, "node install-cmd", "patato node install-cmd <节点> [--command-only]\n\n输出在机器上安装节点后端的命令（含节点密钥或注册码，需要可写 token）。\n原样交给 tolato exec 执行：tolato exec <机器> -- \"$(patato node install-cmd <节点> --command-only)\"", epNodeInstall)
	}
	return fmt.Errorf("未知命令 node %s\n\n%s", sub, usage)
}

func (a *app) nodeList(args []string) error {
	c := newCommand("node list", "patato node list [--state online|offline|pending]")
	state := c.fs.String("state", "", "只看某个状态")
	if err := c.parse(args); err != nil {
		return err
	}
	var l list[node]
	if printed, err := a.get(epNodes, query("state", *state), &l); printed || err != nil {
		return err
	}
	if len(l.Items) == 0 {
		a.printf("没有节点\n")
		return nil
	}
	rows := [][]string{}
	for _, n := range l.Items {
		plans := make([]string, 0, len(n.Plans))
		for _, p := range n.Plans {
			plans = append(plans, p.Code)
		}
		online, today := strconv.Itoa(n.OnlineUsers), bytesText(n.TodayBytes)
		if n.State == "pending" {
			online, today = "—", "—"
		}
		rows = append(rows, []string{n.Code, n.Name, label(stateNames, n.State), n.Protocol, label(backendNames, n.Backend),
			orDash(strings.Join(plans, ",")), online, today, agoText(n.LastHeartbeatAt)})
	}
	table(a.out, []string{"代号", "名称", "状态", "协议", "后端", "套餐", "在线", "今日流量", "心跳"}, rows, 6, 7)
	return nil
}

func (a *app) nodeShow(args []string) error {
	c := newCommand("node show", "patato node show <节点>\n\n节点接受 ID 或代号。")
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "节点")
	if err != nil {
		return err
	}
	var n node
	if printed, err := a.get(client.Path(epNode, ref), nil, &n); printed || err != nil {
		return err
	}
	plans := []string{}
	for _, p := range n.Plans {
		plans = append(plans, fmt.Sprintf("%s（%s）", p.Name, p.Code))
	}
	sub := "否（待接入，收到心跳后下发）"
	if n.InSubscription {
		sub = "是"
	}
	a.printf("%s（%s）\n\n", n.Name, n.Code)
	kv(a.out, [][2]string{
		{"状态", label(stateNames, n.State)},
		{"下发到订阅", sub},
		{"协议", n.Protocol},
		{"后端", label(backendNames, n.Backend)},
		{"地址", fmt.Sprintf("%s:%d", n.Address, n.Port)},
		{"机器", n.Host},
		{"地区", n.Region},
		{"倍率", fmt.Sprint(n.Rate)},
		{"套餐", orDash(strings.Join(plans, "、"))},
		{"在线用户", strconv.Itoa(n.OnlineUsers)},
		{"今日流量", bytesText(n.TodayBytes)},
		{"最近心跳", agoText(n.LastHeartbeatAt)},
		{"最近上报", agoText(n.LastPushAt)},
		{"版本", fmt.Sprintf("v%d", n.Version)},
	})
	if r := n.Runtime; r != nil {
		st := "运行中"
		if !r.Running {
			st = "未运行：" + orDash(r.Error)
		}
		pairs := [][2]string{{"patato-node", st}, {"内核用户", strconv.Itoa(r.Users)}, {"连接数", strconv.Itoa(r.Connections)}}
		if r.CertExpiresAt != nil {
			pairs = append(pairs, [2]string{"证书到期", dateText(r.CertExpiresAt)})
		}
		a.printf("\n")
		kv(a.out, pairs)
	}
	if n.LegacyToken {
		a.printf("\n这个节点还接受 Xboard 的全局 token；换成节点密钥后应关掉。\n")
	}
	return nil
}

func (a *app) nodeProvision(args []string) error {
	c := newCommand("node provision", `patato node provision --template <模板> --plan <套餐> (--host <机器> | --address <地址>) [选项]

意图级上节点：按模板建节点、分配端口、挂到套餐。节点先是待接入，收到首次心跳后才下发到订阅。
影响用户超过阈值时自动升为敏感，需要审批。建议先加 --dry-run 预演。`)
	tpl := c.fs.String("template", "", "节点模板（ID 或代号）")
	var plans multi
	c.fs.Var(&plans, "plan", "挂到哪个套餐，可重复或用逗号分隔")
	host := c.fs.String("host", "", "tolato 中的机器名；不传 --address 时按它查询地址")
	address := c.fs.String("address", "", "客户端连接地址")
	nm := c.fs.String("name", "", "节点名称，默认「地区-序号」")
	code := c.fs.String("code", "", "节点代号，默认按机器名生成")
	port := c.fs.Int("port", 0, "端口，默认在模板范围内自动分配")
	region := c.fs.String("region", "", "地区，默认取模板或 tolato 里的城市")
	backend := c.fs.String("backend", "", "native（patato-node）或 uniproxy（V2bX / XrayR）；默认机器已接入 patato-node 时用 native")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	if err := c.noExtra(0); err != nil {
		return err
	}
	body := map[string]any{"template": *tpl, "plans": []string(plans)}
	c.copy(body, map[string]any{"host": *host, "address": *address, "name": *nm, "code": *code, "port": *port, "region": *region, "backend": *backend})
	return a.mutate("POST", epNodeProvision, body, w)
}

func (a *app) nodeUpdate(args []string) error {
	c := newCommand("node update", "patato node update <节点> [选项]\n\n只修改给出的字段。")
	nm := c.fs.String("name", "", "名称")
	address := c.fs.String("address", "", "客户端连接地址")
	port := c.fs.Int("port", 0, "客户端连接的对外端口")
	serverPort := c.fs.Int("server-port", 0, "节点实际监听的端口（与对外端口分别修改）")
	rate := c.fs.Float64("rate", 0, "流量倍率")
	var tags multi
	c.fs.Var(&tags, "tag", "标签，可重复；传一次空值 --tag '' 清空")
	sort := c.fs.Int("sort", 0, "排序")
	backend := c.fs.String("backend", "", "native 或 uniproxy")
	settings := c.fs.String("settings", "", "协议设置，完整 JSON 或 @文件")
	cost := c.fs.String("monthly-cost", "", "每月成本（元），只用于成本报表")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "节点")
	if err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"name": *nm, "address": *address, "port": *port, "server_port": *serverPort, "rate": *rate, "sort": *sort, "backend": *backend})
	if c.set["tag"] {
		body["tags"] = append([]string{}, tags...)
	}
	if c.set["monthly-cost"] {
		cents, err := parseYuan(*cost)
		if err != nil {
			return fmt.Errorf("--monthly-cost %w", err)
		}
		body["monthly_cost"] = cents
	}
	if c.set["settings"] {
		v, err := jsonArg(*settings)
		if err != nil {
			return fmt.Errorf("--settings %w", err)
		}
		body["settings"] = v
	}
	return a.mutate("PATCH", client.Path(epNode, ref), body, w)
}

func (a *app) nodeAttach(verb string, args []string) error {
	c := newCommand("node "+verb, fmt.Sprintf("patato node %s <节点> --plan <套餐>", verb))
	pl := c.fs.String("plan", "", "套餐（ID 或代号）")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "节点")
	if err != nil {
		return err
	}
	if *pl == "" {
		return fmt.Errorf("缺少 --plan\n\n%s", c.usageText())
	}
	ep := epNodeAttach
	if verb == "detach" {
		ep = epNodeDetach
	}
	return a.mutate("POST", client.Path(ep, ref), map[string]any{"plan": *pl}, w)
}

// nodeRef 是只需要一个对象引用的写命令。
func (a *app) nodeRef(args []string, name, help, method, ep string) error {
	c := newCommand(name, help)
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "对象")
	if err != nil {
		return err
	}
	return a.mutate(method, client.Path(ep, ref), nil, w)
}

func (a *app) installCmd(args []string, name, help, ep string) error {
	c := newCommand(name, help)
	only := c.fs.Bool("command-only", false, "只输出命令本身，便于交给 tolato exec")
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "对象")
	if err != nil {
		return err
	}
	var cmd installCmd
	if *only {
		if _, err := a.c.Do(a.ctx, "GET", client.Path(ep, ref), nil, nil, &cmd); err != nil {
			return err
		}
		a.printf("%s\n", cmd.Command)
		return nil
	}
	if printed, err := a.get(client.Path(ep, ref), nil, &cmd); printed || err != nil {
		return err
	}
	a.printf("节点后端：%s\n\n%s\n\n%s\n", cmd.Backend, cmd.Command, cmd.Note)
	return nil
}

// ---- machine ------------------------------------------------------------

func (a *app) machine(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("machine list", "patato machine list\n\n接入了 patato-node 的机器，以及已有原生节点但还没接入的机器。")
		if err := c.parse(rest); err != nil {
			return err
		}
		var l list[machine]
		if printed, err := a.get(epMachines, nil, &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("还没有机器接入 patato-node\n")
			return nil
		}
		rows := [][]string{}
		for _, m := range l.Items {
			push := "—"
			if m.Status == "enrolled" {
				push = map[bool]string{true: "已连接", false: "轮询"}[m.Connected]
			}
			rows = append(rows, []string{m.Host, label(machineNames, m.Status), push, strconv.Itoa(len(m.Nodes)), orDash(m.AgentVersion), agoText(m.LastSeenAt)})
		}
		table(a.out, []string{"机器", "接入", "推送", "节点", "patato-node", "最近联系"}, rows, 3)
		return nil
	case "show", "get":
		c := newCommand("machine show", "patato machine show <机器>")
		if err := c.parse(rest); err != nil {
			return err
		}
		host, err := c.arg(0, "机器")
		if err != nil {
			return err
		}
		var m machine
		if printed, err := a.get(client.Path(epMachine, host), nil, &m); printed || err != nil {
			return err
		}
		nodes := []string{}
		for _, n := range m.Nodes {
			nodes = append(nodes, n.Code)
		}
		kv(a.out, [][2]string{
			{"机器", m.Host}, {"接入", label(machineNames, m.Status)}, {"推送通道", map[bool]string{true: "已连接", false: "未连接"}[m.Connected]},
			{"主机名", m.Hostname}, {"patato-node", m.AgentVersion}, {"内核", m.Kernel}, {"接入时间", timeText(m.EnrolledAt)},
			{"最近联系", agoText(m.LastSeenAt)}, {"节点", orDash(strings.Join(nodes, "、"))},
		})
		if n := m.Network; n != nil {
			ranges := []string{}
			for _, r := range n.EffectiveRange {
				if r.PublicFrom > 0 {
					ranges = append(ranges, fmt.Sprintf("%s（外网从 %d）", r.Local, r.PublicFrom))
				} else {
					ranges = append(ranges, r.Local)
				}
			}
			kv(a.out, [][2]string{
				{"对外地址", n.Address}, {"NAT", map[bool]string{true: "是", false: "否"}[n.NAT]}, {"转发模式", n.ForwardMode},
				{"端口段", orDash(strings.Join(ranges, "，"))}, {"端口用量", fmt.Sprintf("%d / %d", n.PortsUsed, n.PortsTotal)},
				{"转发", fmt.Sprintf("%d 条，失败 %d 条", n.Forwards, n.Failed)},
				{"转发流量", fmt.Sprintf("今日 %s，本月 %s", bytesText(n.TodayUp+n.TodayDown), bytesText(n.MonthUp+n.MonthDown))},
			})
		}
		return nil
	case "install-cmd":
		return a.installCmd(rest, "machine install-cmd", "patato machine install-cmd <机器> [--command-only]\n\n输出 patato-node 的安装命令（含一小时内有效的一次性注册码，需要可写 token）。", epMachineCmd)
	case "set":
		return a.machineSet(rest)
	case "revoke":
		return a.nodeRef(rest, "machine revoke", "patato machine revoke <机器>\n\n吊销机器的 patato-node 凭证：机器上的节点停止同步和上报，恢复时重新执行安装命令。", "POST", epMachineRevoke)
	}
	return fmt.Errorf("未知命令 machine %s\n\n%s", sub, usage)
}

// ---- template -----------------------------------------------------------

func (a *app) template(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		var l list[template]
		if printed, err := a.get(epTemplates, nil, &l); printed || err != nil {
			return err
		}
		rows := [][]string{}
		for _, t := range l.Items {
			rows = append(rows, []string{t.Code, t.Name, t.Protocol, orDash(t.Region), fmt.Sprintf("%d-%d", t.PortMin, t.PortMax), fmt.Sprint(t.Rate)})
		}
		if len(rows) == 0 {
			a.printf("还没有节点模板：用 patato template create 创建\n")
			return nil
		}
		table(a.out, []string{"代号", "名称", "协议", "地区", "端口", "倍率"}, rows)
		return nil
	case "show", "get":
		c := newCommand("template show", "patato template show <模板>")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "模板")
		if err != nil {
			return err
		}
		var t template
		if printed, err := a.get(client.Path(epTemplate, ref), nil, &t); printed || err != nil {
			return err
		}
		settings, _ := json.MarshalIndent(t.Settings, "  ", "  ")
		kv(a.out, [][2]string{{"模板", fmt.Sprintf("%s（%s）", t.Name, t.Code)}, {"协议", t.Protocol}, {"地区", t.Region},
			{"端口范围", fmt.Sprintf("%d-%d", t.PortMin, t.PortMax)}, {"倍率", fmt.Sprint(t.Rate)}, {"版本", fmt.Sprintf("v%d", t.Version)}})
		a.printf("\n协议设置（私钥和密码已隐去）\n  %s\n", settings)
		return nil
	case "create", "update":
		return a.templateWrite(sub, rest)
	}
	return fmt.Errorf("未知命令 template %s\n\n%s", sub, usage)
}

func (a *app) templateWrite(verb string, args []string) error {
	help := "patato template create --code <代号> --name <名称> --protocol <协议> --port-min <端口> --port-max <端口> [--settings JSON|@文件]"
	if verb == "update" {
		help = "patato template update <模板> [--name ...] [--region ...] [--port-min ...] [--port-max ...] [--rate ...] [--settings JSON|@文件]"
	}
	c := newCommand("template "+verb, help+"\n\n协议设置沿用 Xboard 的 protocol_settings 结构；Reality 密钥、SS2022 密钥等每个节点独有的值留空，上节点时自动生成。")
	code := c.fs.String("code", "", "代号")
	nm := c.fs.String("name", "", "名称")
	region := c.fs.String("region", "", "地区")
	protocol := c.fs.String("protocol", "", "协议：vless vmess trojan shadowsocks hysteria tuic anytls")
	portMin := c.fs.Int("port-min", 0, "端口范围起点")
	portMax := c.fs.Int("port-max", 0, "端口范围终点")
	rate := c.fs.Float64("rate", 0, "流量倍率，默认 1")
	settings := c.fs.String("settings", "", "协议设置 JSON，或 @文件")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"name": *nm, "region": *region, "port_min": *portMin, "port_max": *portMax, "rate": *rate})
	if c.set["settings"] {
		v, err := jsonArg(*settings)
		if err != nil {
			return fmt.Errorf("--settings %w", err)
		}
		body["settings"] = v
	}
	if verb == "create" {
		c.copy(body, map[string]any{"code": *code, "protocol": *protocol})
		return a.mutate("POST", epTemplates, body, w)
	}
	ref, err := c.arg(0, "模板")
	if err != nil {
		return err
	}
	return a.mutate("PATCH", client.Path(epTemplate, ref), body, w)
}

// ---- plan ---------------------------------------------------------------

func (a *app) plan(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		var l list[plan]
		if printed, err := a.get(epPlans, nil, &l); printed || err != nil {
			return err
		}
		rows := [][]string{}
		for _, p := range l.Items {
			rows = append(rows, []string{p.Code, p.Name, gbText(p.TransferGB), label(resetNames, p.ResetMethod), pricesText(p.Prices),
				strconv.FormatInt(p.NodeCount, 10), strconv.FormatInt(p.UserCount, 10)})
		}
		if len(rows) == 0 {
			a.printf("还没有套餐：用 patato plan create 创建\n")
			return nil
		}
		table(a.out, []string{"代号", "名称", "流量", "重置", "价格", "节点", "用户"}, rows, 5, 6)
		return nil
	case "show", "get":
		c := newCommand("plan show", "patato plan show <套餐>")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "套餐")
		if err != nil {
			return err
		}
		var p plan
		if printed, err := a.get(client.Path(epPlan, ref), nil, &p); printed || err != nil {
			return err
		}
		nodes := []string{}
		for _, n := range p.Nodes {
			nodes = append(nodes, n.Code)
		}
		limit := func(n int, unit string) string {
			if n == 0 {
				return "不限"
			}
			return fmt.Sprintf("%d %s", n, unit)
		}
		kv(a.out, [][2]string{{"套餐", fmt.Sprintf("%s（%s）", p.Name, p.Code)}, {"流量", gbText(p.TransferGB)},
			{"重置", label(resetNames, p.ResetMethod)}, {"设备数", limit(p.DeviceLimit, "台")}, {"限速", limit(p.SpeedLimitMbps, "Mbps")},
			{"价格（元）", pricesText(p.Prices)}, {"对外售卖", map[bool]string{true: "是", false: "否"}[p.Sellable]},
			{"分流规则", ruleText(p.RuleProfile)},
			{"用户", strconv.FormatInt(p.UserCount, 10)}, {"节点", orDash(strings.Join(nodes, "、"))}, {"版本", fmt.Sprintf("v%d", p.Version)}})
		return nil
	case "create", "update":
		return a.planWrite(sub, rest)
	case "history":
		return a.planHistory(rest)
	case "delete", "rm":
		return a.nodeRef(rest, "plan delete", "patato plan delete <套餐>\n\n删除套餐（敏感操作，需要审批）。", "DELETE", epPlan)
	}
	return fmt.Errorf("未知命令 plan %s\n\n%s", sub, usage)
}

func (a *app) planWrite(verb string, args []string) error {
	help := "patato plan create --code <代号> --name <名称> --transfer-gb <GB> [选项]"
	if verb == "update" {
		help = "patato plan update <套餐> [选项]\n\n只修改给出的字段。改价格固定为敏感操作，需要审批。"
	}
	c := newCommand("plan "+verb, help+"\n\n价格用 --price 周期=元，例如 --price monthly=19.9 --price yearly=199；CLI 换算成分发给服务端。\n周期：monthly quarterly half_yearly yearly two_yearly three_yearly onetime reset_traffic")
	code := c.fs.String("code", "", "代号")
	nm := c.fs.String("name", "", "名称")
	transfer := c.fs.Float64("transfer-gb", 0, "每个周期的流量（GB）")
	reset := c.fs.String("reset", "", "流量重置：purchase_day（按购买日）、calendar_month（自然月）、never")
	devices := c.fs.Int("device-limit", 0, "设备数上限，0 为不限")
	speed := c.fs.Int("speed-limit", 0, "限速（Mbps），0 为不限")
	var prices multi
	c.fs.Var(&prices, "price", "价格，周期=元，可重复；update 时给出的是完整价格表")
	sellable := c.fs.Bool("sellable", false, "对外售卖")
	apply := c.fs.Bool("apply-to-users", false, "update：把新的流量额度同步给这个套餐下的所有用户")
	var rollout multi
	c.fs.Var(&rollout, "rollout-price", "灰度价格，周期=元，可重复；落在灰度范围内的用户按它下单")
	percent := c.fs.Int("rollout-percent", 0, "灰度比例（0–100）")
	promote := c.fs.Bool("promote-rollout", false, "update：把灰度价格转为正式价格并结束灰度")
	endRollout := c.fs.Bool("end-rollout", false, "update：结束灰度，恢复原价")
	rule := c.fs.String("rule-profile", "", "默认分流规则（代号）；update 时传空字符串改回内置规则")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"name": *nm, "transfer_gb": *transfer, "reset_method": *reset, "device_limit": *devices,
		"speed_limit_mbps": *speed, "sellable": *sellable, "rule_profile": *rule})
	if c.set["price"] {
		p, err := parsePrices(prices)
		if err != nil {
			return err
		}
		body["prices"] = p
	}
	if c.set["rollout-price"] {
		p, err := parsePrices(rollout)
		if err != nil {
			return err
		}
		body["rollout_prices"] = p
	}
	if c.set["rollout-percent"] {
		body["rollout_percent"] = *percent
	}
	if *promote {
		body["promote_rollout"] = true
	}
	if *endRollout {
		body["end_rollout"] = true
	}
	if verb == "create" {
		c.copy(body, map[string]any{"code": *code})
		return a.mutate("POST", epPlans, body, w)
	}
	if *apply {
		body["apply_to_users"] = true
	}
	ref, err := c.arg(0, "套餐")
	if err != nil {
		return err
	}
	return a.mutate("PATCH", client.Path(epPlan, ref), body, w)
}

func ruleText(code *string) string {
	if code == nil {
		return "内置规则"
	}
	return *code
}

// ---- user ---------------------------------------------------------------

func (a *app) user(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		return a.userList(rest)
	case "show", "get":
		return a.userShow(rest)
	case "create", "update":
		return a.userWrite(sub, rest)
	case "reset-traffic":
		return a.nodeRef(rest, "user reset-traffic", "patato user reset-traffic <用户>\n\n把用户本周期的已用流量清零。用户接受 ID 或邮箱。", "POST", epUserTraffic)
	case "reset-token":
		return a.nodeRef(rest, "user reset-token", "patato user reset-token <用户>\n\n重置订阅链接和连接凭证，旧链接立即失效，用户要重新导入。", "POST", epUserToken)
	case "delete", "rm":
		return a.nodeRef(rest, "user delete", "patato user delete <用户>\n\n删除用户（敏感操作，需要审批）。", "DELETE", epUser)
	}
	return fmt.Errorf("未知命令 user %s\n\n%s", sub, usage)
}

func (a *app) userList(args []string) error {
	c := newCommand("user list", "patato user list [选项]")
	q := c.fs.String("q", "", "按邮箱或用户 ID 搜索")
	state := c.fs.String("state", "", "active / expired / over_quota / banned")
	pl := c.fs.String("plan", "", "只看某个套餐")
	expiring := c.fs.Int("expiring", 0, "只看 N 天内到期的")
	limit := c.fs.Int("limit", 50, "最多返回多少个")
	cursor := c.fs.Int64("cursor", 0, "从上一页给出的 next_cursor 继续")
	if err := c.parse(args); err != nil {
		return err
	}
	v := query("q", strings.TrimPrefix(*q, "u_"), "state", *state, "plan", *pl)
	if *expiring > 0 {
		v.Set("expiring_within_days", strconv.Itoa(*expiring))
	}
	v.Set("limit", strconv.Itoa(*limit))
	if *cursor > 0 {
		v.Set("cursor", strconv.FormatInt(*cursor, 10))
	}
	var l list[user]
	if printed, err := a.get(epUsers, v, &l); printed || err != nil {
		return err
	}
	rows := [][]string{}
	for _, u := range l.Items {
		pl := "—"
		if u.Plan != nil {
			pl = u.Plan.Code
		}
		rows = append(rows, []string{strconv.FormatInt(u.ID, 10), u.Email, pl, label(userStateNames, u.State),
			bytesText(u.UsedBytes) + " / " + bytesText(u.TransferBytes), dateText(u.ExpiresAt)})
	}
	if len(rows) == 0 {
		a.printf("没有找到用户\n")
		return nil
	}
	table(a.out, []string{"ID", "邮箱", "套餐", "状态", "本周期流量", "到期"}, rows, 0)
	if l.NextCursor != nil {
		a.printf("\n还有更多：加 --cursor %d\n", *l.NextCursor)
	}
	return nil
}

func (a *app) userShow(args []string) error {
	c := newCommand("user show", "patato user show <用户>\n\n用户接受 ID 或邮箱。")
	if err := c.parse(args); err != nil {
		return err
	}
	ref, err := c.arg(0, "用户")
	if err != nil {
		return err
	}
	var d struct {
		User        user         `json:"user"`
		RecentAudit []auditEntry `json:"recent_audit"`
	}
	if printed, err := a.get(client.Path(epUser, ref), nil, &d); printed || err != nil {
		return err
	}
	u := d.User
	pl := "无"
	if u.Plan != nil {
		pl = fmt.Sprintf("%s（%s）", u.Plan.Name, u.Plan.Code)
	}
	devices := strconv.Itoa(u.DevicesOnline)
	if u.DeviceLimit != nil {
		devices += fmt.Sprintf(" / %d", *u.DeviceLimit)
	}
	speed := "按套餐"
	if u.SpeedLimitMbps != nil {
		speed = fmt.Sprintf("%d Mbps", *u.SpeedLimitMbps)
	}
	expires := "长期"
	if u.ExpiresAt != nil {
		expires = dateText(u.ExpiresAt)
	}
	kv(a.out, [][2]string{
		{"用户", fmt.Sprintf("%s（ID %d）", u.Email, u.ID)}, {"状态", label(userStateNames, u.State)}, {"套餐", pl},
		{"本周期流量", fmt.Sprintf("%s / %s（上行 %s，下行 %s）", bytesText(u.UsedBytes), bytesText(u.TransferBytes), bytesText(u.UsedUp), bytesText(u.UsedDown))},
		{"下次重置", dateText(u.NextResetAt)}, {"到期", expires}, {"在线设备", devices}, {"限速", speed},
		{"订阅 token", map[bool]string{true: "重置于 " + timeText(u.TokenResetAt), false: "未重置过"}[u.TokenResetAt != nil]},
	})
	if len(d.RecentAudit) > 0 {
		a.printf("\n最近记录\n")
		for _, e := range d.RecentAudit {
			a.printf("  %s  %s（%s，%s）\n", timeText(e.At), orDash(e.Detail.Summary), label(entryNames, e.Entry), label(resultNames, e.Result))
		}
	}
	return nil
}

func (a *app) userWrite(verb string, args []string) error {
	help := "patato user create --email <邮箱> --plan <套餐> [--password ...] [--days N | --expires YYYY-MM-DD]"
	if verb == "update" {
		help = "patato user update <用户> [选项]\n\n只修改给出的字段：换套餐、续期、限速、封禁。"
	}
	c := newCommand("user "+verb, help)
	email := c.fs.String("email", "", "邮箱")
	password := c.fs.String("password", "", "初始密码；不传时用户需要走找回密码")
	pl := c.fs.String("plan", "", "套餐（ID 或代号）")
	days := c.fs.Int("days", 0, "create：有效天数")
	extend := c.fs.Int("extend-days", 0, "update：在当前到期时间上续期 N 天")
	expires := c.fs.String("expires", "", "到期日 YYYY-MM-DD（本地时区当天结束）")
	never := c.fs.Bool("never-expire", false, "update：改为长期有效")
	transfer := c.fs.Float64("transfer-gb", 0, "单独设置流量额度（GB）")
	devices := c.fs.Int("device-limit", 0, "单独设置设备数")
	speed := c.fs.Int("speed-limit", 0, "单独设置限速（Mbps）")
	banned := c.fs.Bool("banned", false, "update：封禁（--banned=false 解封）")
	rule := c.fs.String("rule-profile", "", "update：替用户选分流规则（代号）；传空字符串改回跟随套餐")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"plan": *pl, "transfer_gb": *transfer, "device_limit": *devices, "speed_limit_mbps": *speed})
	if c.set["expires"] {
		t, err := time.ParseInLocation("2006-01-02", *expires, time.Local)
		if err != nil {
			return fmt.Errorf("--expires 应为 YYYY-MM-DD")
		}
		body["expires_at"] = t.Add(24*time.Hour - time.Second)
	}
	if verb == "create" {
		c.copy(body, map[string]any{"email": *email, "password": *password, "days": *days})
		return a.mutate("POST", epUsers, body, w)
	}
	c.copy(body, map[string]any{"extend_days": *extend, "never_expire": *never, "banned": *banned, "rule_profile": *rule})
	ref, err := c.arg(0, "用户")
	if err != nil {
		return err
	}
	return a.mutate("PATCH", client.Path(epUser, ref), body, w)
}

// copy 把显式给出的 flag 放进请求体。flag 名里的连字符对应字段名里的下划线。
func (c *command) copy(body map[string]any, fields map[string]any) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if c.set[flagName(k)] {
			body[k] = fields[k]
		}
	}
}

// flagName 由字段名得到 flag 名；少数字段的 flag 名更短。
func flagName(field string) string {
	switch field {
	case "speed_limit_mbps":
		return "speed-limit"
	case "reset_method":
		return "reset"
	case "expires_at":
		return "expires"
	}
	return strings.ReplaceAll(field, "_", "-")
}

func query(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			v.Set(kv[i], kv[i+1])
		}
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
