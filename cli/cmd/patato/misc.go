package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/cli/internal/client"
)

// ---- status -------------------------------------------------------------

func (a *app) status(args []string) error {
	c := newCommand("status", "patato status\n\n需要处理的事（待审批、告警）、节点和用户概况、今日用量。")
	if err := c.parse(args); err != nil {
		return err
	}
	var s struct {
		Todo []struct {
			Kind      string     `json:"kind"`
			Severity  string     `json:"severity"`
			Title     string     `json:"title"`
			Detail    string     `json:"detail"`
			Ref       string     `json:"ref"`
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"todo"`
		Nodes struct {
			Total   int    `json:"total"`
			Online  int    `json:"online"`
			Offline int    `json:"offline"`
			Pending int    `json:"pending"`
			Items   []node `json:"items"`
		} `json:"nodes"`
		Users    map[string]int64 `json:"users"`
		Expiring struct {
			Today int64 `json:"today"`
			Days3 int64 `json:"days_3"`
			Days7 int64 `json:"days_7"`
		} `json:"expiring"`
		Usage struct {
			TodayBytes  int64 `json:"today_bytes"`
			OnlineUsers int64 `json:"online_users"`
		} `json:"usage"`
		PanelHost struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"panel_host"`
	}
	if printed, err := a.get(epStatus, nil, &s); printed || err != nil {
		return err
	}
	if len(s.Todo) == 0 {
		a.printf("没有需要处理的事。\n")
	} else {
		a.printf("需要处理（%d）\n", len(s.Todo))
		for _, t := range s.Todo {
			tag := map[string]string{"approval": "待审批", "node_offline": "节点离线", "panel_host_exec": "面板主机执行"}[t.Kind]
			if tag == "" {
				tag = "告警"
			}
			line := fmt.Sprintf("  [%s] %s", tag, t.Title)
			if t.ExpiresAt != nil {
				line += fmt.Sprintf("（%s过期）", untilText(*t.ExpiresAt))
			}
			a.printf("%s\n", line)
			if t.Detail != "" {
				a.printf("        %s\n", t.Detail)
			}
			if t.Kind == "approval" {
				a.printf("        patato approval show %s\n", t.Ref)
			}
		}
	}
	a.printf("\n")
	kv(a.out, [][2]string{
		{"节点", fmt.Sprintf("%d 个：%d 在线，%d 离线，%d 待接入", s.Nodes.Total, s.Nodes.Online, s.Nodes.Offline, s.Nodes.Pending)},
		{"用户", fmt.Sprintf("%d 名：%d 使用中，%d 已到期，%d 超额停用，%d 已封禁", s.Users["total"], s.Users["active"], s.Users["expired"], s.Users["over_quota"], s.Users["banned"])},
		{"即将到期", fmt.Sprintf("今天 %d，3 天内 %d，7 天内 %d", s.Expiring.Today, s.Expiring.Days3, s.Expiring.Days7)},
		{"今日用量", fmt.Sprintf("%s，%d 名用户在线", bytesText(s.Usage.TodayBytes), s.Usage.OnlineUsers)},
		{"面板主机", s.PanelHost.Message},
	})
	var bad []string
	for _, n := range s.Nodes.Items {
		if n.State == "offline" {
			bad = append(bad, fmt.Sprintf("%s（最后心跳 %s）", n.Code, agoText(n.LastHeartbeatAt)))
		}
	}
	if len(bad) > 0 {
		a.printf("\n离线节点：%s\n", strings.Join(bad, "、"))
	}
	return nil
}

// ---- change -------------------------------------------------------------

func (a *app) change(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "show", "get":
		c := newCommand("change show", "patato change show <change_id>")
		if err := c.parse(rest); err != nil {
			return err
		}
		id, err := c.arg(0, "change_id")
		if err != nil {
			return err
		}
		var p changePlan
		if printed, err := a.get(client.Path(epChange, id), nil, &p); printed || err != nil {
			return err
		}
		a.renderChange(&p, true)
		return nil
	case "apply":
		c := newCommand("change apply", "patato change apply <change_id> [--wait]\n\n执行预演好的变更计划。执行时会核对相关对象的版本，预演后被改过则作废；敏感计划转为审批。")
		w := &writeFlags{}
		c.fs.BoolVar(&w.wait, "wait", false, "需要审批时等待结果")
		c.fs.DurationVar(&w.timeout, "timeout", 30*time.Minute, "--wait 最多等多久")
		if err := c.parse(rest); err != nil {
			return err
		}
		id, err := c.arg(0, "change_id")
		if err != nil {
			return err
		}
		var p changePlan
		resp, err := a.c.Do(a.ctx, "POST", client.Path(epChangeApply, id), nil, nil, &p)
		if err != nil {
			return err
		}
		return a.afterChange(resp, &p, w)
	}
	return fmt.Errorf("未知命令 change %s\n\n%s", sub, usage)
}

// ---- approval -----------------------------------------------------------

func (a *app) approval(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("approval list", "patato approval list [--status pending|approved|rejected|expired|failed]\n\n默认只看待审批的；--status all 看全部。")
		status := c.fs.String("status", "pending", "按状态筛选")
		if err := c.parse(rest); err != nil {
			return err
		}
		st := *status
		if st == "all" {
			st = ""
		}
		var l list[struct {
			ID         string    `json:"approval_id"`
			Status     string    `json:"status"`
			Summary    string    `json:"summary"`
			Risk       string    `json:"risk"`
			RiskReason string    `json:"risk_reason"`
			Requester  string    `json:"requester"`
			ActorClaim string    `json:"actor_claim"`
			ExpiresAt  time.Time `json:"expires_at"`
			CreatedAt  time.Time `json:"created_at"`
			Impact     struct {
				Users int64 `json:"users"`
			} `json:"impact"`
		}]
		if printed, err := a.get(epApprovals, query("status", st), &l); printed || err != nil {
			return err
		}
		if len(l.Items) == 0 {
			a.printf("没有%s审批\n", map[bool]string{true: "待处理的", false: ""}[st == "pending"])
			return nil
		}
		rows := [][]string{}
		for _, it := range l.Items {
			when := timeText(&it.CreatedAt)
			if it.Status == "pending" {
				when = untilText(it.ExpiresAt) + "过期"
			}
			who := it.ActorClaim
			if who == "" {
				who = it.Requester
			}
			rows = append(rows, []string{it.ID, label(approvalNames, it.Status), truncate(it.Summary, 40), strconv.FormatInt(it.Impact.Users, 10), truncate(who, 20), when})
		}
		table(a.out, []string{"审批", "状态", "变更", "影响用户", "发起方", "时间"}, rows, 3)
		return nil
	case "show", "get":
		c := newCommand("approval show", "patato approval show <id> [--wait]\n\n--wait 时等到有结果；被拒绝、过期或执行失败时退出码为 3。")
		wait := c.fs.Bool("wait", false, "等到审批有结果")
		timeout := c.fs.Duration("timeout", 30*time.Minute, "--wait 最多等多久")
		if err := c.parse(rest); err != nil {
			return err
		}
		id, err := c.arg(0, "审批 ID")
		if err != nil {
			return err
		}
		return a.approvalShow(id, *wait, *timeout)
	case "approve":
		c := newCommand("approval approve", `patato approval approve <id> [--no-browser] [--no-wait]

在本机浏览器打开 Admin 的确认页，由人看过变更内容后用 passkey 批准。
CLI 本身不能批准（token 无权调用审批接口），它只负责打开页面并等待结果。`)
		noBrowser := c.fs.Bool("no-browser", false, "只打印链接，不打开浏览器")
		noWait := c.fs.Bool("no-wait", false, "打开页面后立即返回，不等结果")
		timeout := c.fs.Duration("timeout", 30*time.Minute, "最多等多久")
		if err := c.parse(rest); err != nil {
			return err
		}
		id, err := c.arg(0, "审批 ID")
		if err != nil {
			return err
		}
		return a.approvalApprove(id, *noBrowser, !*noWait, *timeout)
	}
	return fmt.Errorf("未知命令 approval %s\n\n%s", sub, usage)
}

func (a *app) approvalShow(id string, wait bool, timeout time.Duration) error {
	var d approvalDetail
	resp, err := a.c.Do(a.ctx, "GET", client.Path(epApproval, id), nil, nil, &d)
	if err != nil {
		return err
	}
	if d.Status == "pending" && wait {
		if !a.json {
			a.renderApproval(&d, false)
			a.printf("\n")
		}
		final, raw, err := a.waitApprovalRaw(id, timeout)
		if err != nil {
			return err
		}
		if a.json {
			printJSON(a.out, raw)
		} else {
			a.renderDecision(final)
		}
		return decisionOutcome(final)
	}
	if a.json {
		printJSON(a.out, resp.Body)
	} else {
		a.renderApproval(&d, true)
	}
	if d.Status == "pending" {
		return nil
	}
	return decisionOutcome(&d)
}

func (a *app) renderApproval(d *approvalDetail, hints bool) {
	status := label(approvalNames, d.Status)
	if d.Status == "pending" {
		status += fmt.Sprintf("，%s过期（%s）", untilText(d.ExpiresAt), local(d.ExpiresAt).Format("15:04"))
	}
	who := d.Requester.Label
	if d.Requester.Claim != "" {
		who += "，自报为「" + d.Requester.Claim + "」"
	}
	a.printf("审批 %s\n\n", d.ID)
	kv(a.out, [][2]string{{"状态", status}, {"发起方", fmt.Sprintf("%s（%s）", who, label(entryNames, d.Requester.Entry))}, {"原始指令", d.Instruction}})
	if d.DecidedBy != "" {
		kv(a.out, [][2]string{{"决定", fmt.Sprintf("%s，由 %s 经%s", label(approvalNames, d.Status), d.DecidedBy, label(entryNames, d.DecidedVia))}, {"原因", d.Reason}})
	}
	if d.Change != nil {
		a.printf("\n")
		a.renderChange(d.Change, false)
	}
	if d.Status == "pending" && hints {
		a.printf("运营者可以在这台电脑上批准：patato approval approve %s\n", d.ID)
	}
}

func (a *app) approvalApprove(id string, noBrowser, wait bool, timeout time.Duration) error {
	var d approvalDetail
	if _, err := a.c.Do(a.ctx, "GET", client.Path(epApproval, id), nil, nil, &d); err != nil {
		return err
	}
	if d.Status != "pending" || time.Now().After(d.ExpiresAt) {
		if d.Status == "pending" {
			d.Status = "expired"
		}
		a.renderDecision(&d)
		return decisionOutcome(&d)
	}
	var me whoami
	if _, err := a.c.Do(a.ctx, "GET", epMe, nil, nil, &me); err != nil {
		return err
	}
	if me.AdminURL == "" {
		return fmt.Errorf("服务端没有配置 admin_url，无法打开确认页；可以在 Telegram 的审批卡片上批准")
	}
	u := strings.TrimRight(me.AdminURL, "/") + "/approvals/" + url.PathEscape(id) + "?via=cli"
	summary := ""
	if d.Change != nil {
		summary = d.Change.Summary
	}
	fmt.Fprintf(os.Stderr, "审批：%s\n在浏览器里打开确认页，核对变更后用 passkey 批准：\n\n  %s\n\n", summary, u)
	if !noBrowser {
		if err := openBrowser(u); err != nil {
			fmt.Fprintf(os.Stderr, "没能自动打开浏览器（%v），请手动打开上面的链接。\n", err)
		}
	}
	if !wait {
		return nil
	}
	final, raw, err := a.waitApprovalRaw(id, timeout)
	if err != nil {
		return err
	}
	if a.json {
		printJSON(a.out, raw)
	} else {
		a.renderDecision(final)
	}
	return decisionOutcome(final)
}

// waitApprovalRaw 等到审批有结果，同时返回原始 JSON 供 --json 输出。
func (a *app) waitApprovalRaw(id string, timeout time.Duration) (*approvalDetail, json.RawMessage, error) {
	d, err := a.waitApproval(id, timeout)
	if err != nil {
		return nil, nil, err
	}
	resp, err := a.c.Do(a.ctx, "GET", client.Path(epApproval, id), nil, nil, nil)
	if err != nil {
		return d, nil, err
	}
	return d, resp.Body, nil
}

// ---- audit / events / usage ----------------------------------------------

func (a *app) audit(args []string) error {
	sub, rest := "log", args
	if len(args) > 0 && (args[0] == "log" || args[0] == "list") {
		sub, rest = args[0], args[1:]
	}
	_ = sub
	c := newCommand("audit log", "patato audit log [选项]\n\n每一个写操作：谁、从哪个入口、做了什么、结果如何。")
	q := c.fs.String("q", "", "按操作、对象或 change_id 搜索")
	entry := c.fs.String("entry", "", "入口：cli web telegram cli_passkey portal")
	result := c.fs.String("result", "", "结果：success pending_approval rejected voided failed")
	target := c.fs.String("target", "", "对象，例如 node:3、user:42")
	limit := c.fs.Int("limit", 30, "最多返回多少条")
	cursor := c.fs.Int64("cursor", 0, "从上一页给出的 next_cursor 继续")
	if err := c.parse(rest); err != nil {
		return err
	}
	v := query("q", *q, "entry", *entry, "result", *result, "target", *target, "limit", strconv.Itoa(*limit))
	if *cursor > 0 {
		v.Set("cursor", strconv.FormatInt(*cursor, 10))
	}
	var l list[auditEntry]
	if printed, err := a.get(epAudit, v, &l); printed || err != nil {
		return err
	}
	rows := [][]string{}
	for _, e := range l.Items {
		who := e.Subject
		if e.ActorClaim != "" {
			who += "（" + e.ActorClaim + "）"
		}
		what := orDash(e.Detail.Summary)
		if e.Detail.Error != "" {
			what += "：" + e.Detail.Error
		}
		rows = append(rows, []string{timeText(e.At), truncate(what, 44), truncate(who, 24), label(entryNames, e.Entry), label(riskNames, e.Risk), label(resultNames, e.Result), orDash(e.ChangeID)})
	}
	if len(rows) == 0 {
		a.printf("没有记录\n")
		return nil
	}
	table(a.out, []string{"时间", "操作", "操作者", "入口", "风险", "结果", "change_id"}, rows)
	if l.NextCursor != nil {
		a.printf("\n更早的记录：加 --cursor %d\n", *l.NextCursor)
	}
	return nil
}

type event struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	Severity   string     `json:"severity"`
	Subject    string     `json:"subject"`
	Message    string     `json:"message"`
	CreatedAt  *time.Time `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

func (a *app) events(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		c := newCommand("events list", "patato events list [--after <id>] [--limit N]")
		after := c.fs.Int64("after", 0, "只看这个 ID 之后的事件")
		limit := c.fs.Int("limit", 50, "最多返回多少条")
		if err := c.parse(rest); err != nil {
			return err
		}
		var l list[event]
		if printed, err := a.get(epEvents, query("after", strconv.FormatInt(*after, 10), "limit", strconv.Itoa(*limit)), &l); printed || err != nil {
			return err
		}
		for _, e := range l.Items {
			a.printEvent(e)
		}
		if len(l.Items) == 0 {
			a.printf("没有事件\n")
		}
		return nil
	case "watch":
		c := newCommand("events watch", "patato events watch\n\n持续输出新事件（节点上下线、到期、超额、审批），Ctrl-C 结束；--json 时每行一个 JSON。")
		if err := c.parse(rest); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "等待新事件……（Ctrl-C 结束）")
		return a.c.Stream(a.ctx, epEvents, nil, func(ev client.Event) {
			if a.json {
				a.printf("%s\n", ev.Data)
				return
			}
			var e event
			if json.Unmarshal(ev.Data, &e) == nil {
				a.printEvent(e)
			}
		})
	}
	return fmt.Errorf("未知命令 events %s\n\n%s", sub, usage)
}

func (a *app) printEvent(e event) {
	sev := map[string]string{"critical": "告警", "warning": "注意", "info": "信息"}[e.Severity]
	a.printf("%s  [%s] %s  %s\n", timeText(e.CreatedAt), orDash(sev), e.Message, e.Subject)
}

func (a *app) usage(args []string) error {
	c := newCommand("usage", "patato usage [--day YYYY-MM-DD]\n\n某一天的总流量和各节点流量，默认今天。")
	day := c.fs.String("day", "", "日期")
	if err := c.parse(args); err != nil {
		return err
	}
	var u struct {
		Day         string `json:"day"`
		TotalBytes  int64  `json:"total_bytes"`
		OnlineUsers int64  `json:"online_users"`
		Nodes       []struct {
			NodeID int64 `json:"NodeID"`
			Up     int64 `json:"Up"`
			Down   int64 `json:"Down"`
		} `json:"nodes"`
	}
	if printed, err := a.get(epUsage, query("day", *day), &u); printed || err != nil {
		return err
	}
	kv(a.out, [][2]string{{"日期", u.Day}, {"总流量", bytesText(u.TotalBytes)}, {"在线用户", strconv.FormatInt(u.OnlineUsers, 10)}})
	if len(u.Nodes) > 0 {
		rows := [][]string{}
		for _, n := range u.Nodes {
			rows = append(rows, []string{strconv.FormatInt(n.NodeID, 10), bytesText(n.Up), bytesText(n.Down), bytesText(n.Up + n.Down)})
		}
		a.printf("\n")
		table(a.out, []string{"节点 ID", "上行", "下行", "合计"}, rows, 1, 2, 3)
	}
	return nil
}
