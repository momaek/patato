package ops

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/relay"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
)

var codePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ---- node.provision ----------------------------------------------------

type NodeProvision struct{ d *Deps }

func (*NodeProvision) Kind() string      { return "node.provision" }
func (*NodeProvision) Risk() change.Risk { return change.Normal }

type provisionParams struct {
	Template Ref    `json:"template"`
	Host     string `json:"host"`
	Plans    []Ref  `json:"plans"`
	Address  string `json:"address"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	Port     int    `json:"port"`
	Region   string `json:"region"`
	// Backend 为 native（patato-node）或 uniproxy（V2bX / XrayR）；不传时，机器已接入 patato-node 则为 native。
	Backend string `json:"backend"`
}

// provisionResolved 是预演时定下的完整节点参数，执行阶段只按它创建。
type provisionResolved struct {
	TemplateID int64           `json:"template_id"`
	PlanIDs    []int64         `json:"plan_ids"`
	Code       string          `json:"code"`
	Name       string          `json:"name"`
	Host       string          `json:"host"`
	Address    string          `json:"address"`
	Region     string          `json:"region"`
	Protocol   string          `json:"protocol"`
	Port       int             `json:"port"`
	Settings   json.RawMessage `json:"settings"`
	Rate       float64         `json:"rate"`
	Backend    string          `json:"backend"`
}

func (o *NodeProvision) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[provisionParams](raw)
	if err != nil {
		return nil, err
	}
	tpl, err := findTemplate(ctx, q, p.Template)
	if err != nil {
		return nil, err
	}
	if len(p.Plans) == 0 {
		return nil, apierr.BadRequest("missing_plan", "至少要挂到一个套餐").WithHint("传 plans: [\"premium\"]；用 GET /api/v1/plans 查看全部")
	}
	var plans []db.Plan
	for _, r := range p.Plans {
		pl, err := findPlan(ctx, q, r)
		if err != nil {
			return nil, err
		}
		plans = append(plans, pl)
	}

	res := provisionResolved{TemplateID: tpl.ID, Host: strings.TrimSpace(p.Host), Protocol: tpl.Protocol, Rate: tpl.Rate}
	res.Region = firstNonEmpty(p.Region, tpl.Region)
	res.Address = strings.TrimSpace(p.Address)
	if res.Address == "" {
		if res.Host == "" {
			return nil, apierr.BadRequest("missing_host", "缺少机器").WithHint("传 host（tolato 中的机器名），或直接传 address")
		}
		if o.d.Machines == nil {
			return nil, apierr.BadRequest("tolato_not_configured", "未配置 tolato，无法按机器名 %s 查询地址", res.Host).
				WithHint("在配置里填写 tolato.url 与 tolato.api_key，或直接传 address")
		}
		m, err := o.d.Machines.LookupMachine(ctx, res.Host)
		if err != nil {
			return nil, err
		}
		res.Address = m.IP
		res.Region = firstNonEmpty(res.Region, m.City, m.Country)
	}

	res.Backend, err = chooseBackend(ctx, q, res.Host, p.Backend)
	if err != nil {
		return nil, err
	}

	port, err := allocatePort(ctx, q, res.Host, tpl, p.Port)
	if err != nil {
		return nil, err
	}
	res.Port = port

	res.Name = strings.TrimSpace(p.Name)
	if res.Name == "" {
		prefix := firstNonEmpty(res.Region, "节点")
		n, err := q.CountNodesWithNamePrefix(ctx, prefix+"-")
		if err != nil {
			return nil, err
		}
		res.Name = fmt.Sprintf("%s-%02d", prefix, n+1)
	}
	res.Code = strings.ToLower(strings.TrimSpace(p.Code))
	if res.Code == "" {
		res.Code = defaultCode(res.Host, res.Address, port)
	}
	if !codePattern.MatchString(res.Code) {
		return nil, apierr.BadRequest("invalid_code", "节点代号 %q 只能包含小写字母、数字和连字符", res.Code)
	}
	if _, err := q.GetNodeByCode(ctx, res.Code); err == nil {
		res.Code = fmt.Sprintf("%s-%d", res.Code, port)
		if _, err := q.GetNodeByCode(ctx, res.Code); err == nil {
			return nil, apierr.Conflict("node_code_taken", "节点代号 %s 已被占用", res.Code).WithHint("传 code 指定一个新的代号")
		}
	}

	settings, err := completeSettings(tpl.Protocol, tpl.Settings)
	if err != nil {
		return nil, err
	}
	res.Settings = settings

	ids := make([]int64, 0, len(plans))
	targets := []change.Target{templateTarget(tpl)}
	planNames := make([]string, 0, len(plans))
	var diff []change.DiffEntry
	for _, pl := range plans {
		ids = append(ids, pl.ID)
		targets = append(targets, planTarget(pl))
		planNames = append(planNames, fmt.Sprintf("%s（%s）", pl.Name, pl.Code))
		before, err := planNodeNames(ctx, q, pl.ID)
		if err != nil {
			return nil, err
		}
		diff = append(diff, change.DiffEntry{
			Field:  fmt.Sprintf("%s 的节点", pl.Name),
			Before: before,
			After:  append(slices.Clone(before), res.Name),
		})
	}
	res.PlanIDs = ids
	users, err := q.CountUsersInPlans(ctx, ids)
	if err != nil {
		return nil, err
	}

	diff = append([]change.DiffEntry{{Field: "节点", Before: nil, After: map[string]any{
		"name": res.Name, "code": res.Code, "address": res.Address, "port": res.Port,
		"protocol": res.Protocol, "region": res.Region, "state": "pending", "backend": res.Backend,
	}}}, diff...)
	steps := []string{
		fmt.Sprintf("创建节点 %s（%s），协议 %s，地址 %s:%d，状态为待接入", res.Name, res.Code, res.Protocol, res.Address, res.Port),
		"生成节点通信密钥",
		fmt.Sprintf("挂到套餐：%s", strings.Join(planNames, "、")),
	}
	if res.Backend == "native" {
		steps = append(steps,
			fmt.Sprintf("机器 %s 上的 patato-node 通过推送收到新节点并启动", res.Host),
			fmt.Sprintf("节点在 patato-node 上启动成功后，出现在 %d 名用户的订阅里", users))
	} else {
		steps = append(steps, fmt.Sprintf("节点收到首次心跳后，出现在 %d 名用户的订阅里", users))
	}
	return &change.Draft{
		Summary:  fmt.Sprintf("把 %s 上线到%s", res.Name, strings.Join(planNames, "、")),
		Steps:    steps,
		Diff:     diff,
		Impact:   change.Impact{Users: users, Nodes: 1},
		Targets:  targets,
		Resolved: res,
	}, nil
}

func (o *NodeProvision) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	r, err := decode[provisionResolved](raw)
	if err != nil {
		return nil, err
	}
	key, hash, sealed := newNodeKey(o.d.Box)
	_ = key
	tplID := r.TemplateID
	n, err := q.CreateNode(ctx, db.CreateNodeParams{
		Code: r.Code, Name: r.Name, TemplateID: &tplID, Host: r.Host, Address: r.Address, Region: r.Region,
		Protocol: r.Protocol, Port: int32(r.Port), ServerPort: int32(r.Port), Settings: r.Settings, Rate: r.Rate,
		State: "pending", Tags: []string{}, KeyHash: hash, KeyCipher: sealed, Backend: firstNonEmpty(r.Backend, "uniproxy"),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("node_conflict", "节点代号或端口在预演后被占用").WithHint("重新预演一次")
		}
		return nil, err
	}
	for _, pid := range r.PlanIDs {
		if _, err := q.AttachNode(ctx, db.AttachNodeParams{PlanID: pid, NodeID: n.ID}); err != nil {
			return nil, err
		}
		if err := q.BumpPlanVersion(ctx, pid); err != nil {
			return nil, err
		}
	}
	next := fmt.Sprintf("用 patato node install-cmd %s 取得安装命令，在机器上执行后节点会自动转为在线", n.Code)
	if n.Backend == "native" {
		next = fmt.Sprintf("机器 %s 上的 patato-node 会在几秒内收到推送并启动节点；用 patato node show %s 确认已转为在线", n.Host, n.Code)
		if m, err := q.GetMachineByHost(ctx, n.Host); err != nil || m.TokenHash == nil {
			next = fmt.Sprintf("机器 %s 还没有接入 patato-node：用 patato machine install-cmd %s 取得安装命令，在机器上执行后节点会自动转为在线", n.Host, n.Host)
		}
	}
	return map[string]any{
		"node_id": n.ID, "code": n.Code, "name": n.Name, "state": n.State, "backend": n.Backend, "next": next,
	}, nil
}

// ---- node.update -------------------------------------------------------

type NodeUpdate struct{ d *Deps }

func (*NodeUpdate) Kind() string      { return "node.update" }
func (*NodeUpdate) Risk() change.Risk { return change.Normal }

type nodeUpdateParams struct {
	Node Ref     `json:"node"`
	Name *string `json:"name"`
	// Host 是节点所在的机器（tolato 中的机器名）。从 Xboard 导入的节点没有它，要先填上才能改由 patato-node 承载。
	Host    *string `json:"host"`
	Address *string `json:"address"`
	// Port 是客户端连接的对外端口，ServerPort 是节点实际监听的端口。两者分别修改：
	// 对外地址经 NAT 或中转映射时它们不同，改对外端口不能把监听端口一起改掉。
	Port       *int            `json:"port"`
	ServerPort *int            `json:"server_port"`
	Rate       *float64        `json:"rate"`
	Tags       []string        `json:"tags"`
	Settings   json.RawMessage `json:"settings"`
	Sort       *int            `json:"sort"`
	Backend    *string         `json:"backend"`
	// MonthlyCost 是这个节点每月的成本（分），只用于成本报表，不影响用户。
	MonthlyCost *int64 `json:"monthly_cost"`
}

func (o *NodeUpdate) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[nodeUpdateParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	var diff []change.DiffEntry
	add := func(field string, before, after any) {
		diff = append(diff, change.DiffEntry{Field: field, Before: before, After: after})
	}
	if p.Name != nil && *p.Name != n.Name {
		add("name", n.Name, *p.Name)
	}
	if p.Address != nil && *p.Address != n.Address {
		add("address", n.Address, *p.Address)
	}
	if p.Port != nil && int32(*p.Port) != n.Port {
		if *p.Port <= 0 || *p.Port > 65535 {
			return nil, apierr.BadRequest("invalid_port", "端口 %d 不合法", *p.Port)
		}
		add("port", n.Port, *p.Port)
	}
	if p.ServerPort != nil && int32(*p.ServerPort) != n.ServerPort {
		if *p.ServerPort <= 0 || *p.ServerPort > 65535 {
			return nil, apierr.BadRequest("invalid_port", "监听端口 %d 不合法", *p.ServerPort)
		}
		add("server_port", n.ServerPort, *p.ServerPort)
	}
	host := n.Host
	if p.Host != nil {
		host = strings.TrimSpace(*p.Host)
		if host == "" && n.Backend == "native" {
			return nil, apierr.BadRequest("missing_host", "patato-node 按机器承载节点，不能清空 host")
		}
		if host != n.Host {
			add("host", n.Host, host)
		}
	}
	serverPort := n.ServerPort
	if p.ServerPort != nil {
		serverPort = int32(*p.ServerPort)
	}
	if host != "" && (host != n.Host || serverPort != n.ServerPort) {
		used, err := q.UsedServerPorts(ctx, host)
		if err != nil {
			return nil, err
		}
		if slices.Contains(used, serverPort) {
			return nil, apierr.Conflict("port_taken", "机器 %s 上端口 %d 已被占用", host, serverPort)
		}
	}
	if p.Rate != nil && *p.Rate != n.Rate {
		if *p.Rate <= 0 {
			return nil, apierr.BadRequest("invalid_rate", "倍率必须大于 0")
		}
		add("rate", n.Rate, *p.Rate)
	}
	if p.Tags != nil && !slices.Equal(p.Tags, n.Tags) {
		add("tags", n.Tags, p.Tags)
	}
	if len(p.Settings) > 0 {
		var v any
		if json.Unmarshal(p.Settings, &v) != nil {
			return nil, apierr.BadRequest("invalid_settings", "settings 不是合法 JSON")
		}
		add("settings", json.RawMessage(n.Settings), json.RawMessage(p.Settings))
	}
	if p.Sort != nil && int32(*p.Sort) != n.Sort {
		add("sort", n.Sort, *p.Sort)
	}
	costOnly := len(diff) == 0 && p.Backend == nil
	if p.MonthlyCost != nil && *p.MonthlyCost != n.MonthlyCost {
		if *p.MonthlyCost < 0 {
			return nil, apierr.BadRequest("invalid_cost", "成本不能为负数")
		}
		add("monthly_cost", n.MonthlyCost, *p.MonthlyCost)
	} else {
		costOnly = false
	}
	steps := []string{}
	if p.Backend != nil && *p.Backend != n.Backend {
		b, err := chooseBackend(ctx, q, host, *p.Backend)
		if err != nil {
			return nil, err
		}
		add("backend", n.Backend, b)
		if b == "native" {
			steps = append(steps, fmt.Sprintf("改由机器 %s 上的 patato-node 承载；先停掉机器上原来的 V2bX / XrayR，避免端口冲突", host))
		} else {
			steps = append(steps, fmt.Sprintf("patato-node 停掉这个节点；之后用 patato node install-cmd %s 安装 V2bX", n.Code))
		}
	}
	if len(diff) == 0 {
		return nil, apierr.BadRequest("nothing_to_change", "没有任何字段需要修改")
	}
	if costOnly {
		// 只改成本不影响用户和节点后端
		return &change.Draft{
			Summary: fmt.Sprintf("修改节点 %s 的成本", n.Name),
			Steps:   []string{fmt.Sprintf("节点 %s 的月成本改为 %s 元，只用于成本报表", n.Name, commerce.Yuan(*p.MonthlyCost))},
			Diff:    diff,
			Targets: []change.Target{nodeTarget(n)},
		}, nil
	}
	users, err := q.CountUsersOfNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("修改节点 %s", n.Name),
		Steps:   append([]string{fmt.Sprintf("更新节点 %s 的 %d 项设置，%s", n.Name, len(diff), applyHint(n.Backend))}, steps...),
		Diff:    diff,
		Impact:  change.Impact{Users: users, Nodes: 1},
		Targets: []change.Target{nodeTarget(n)},
	}, nil
}

func (o *NodeUpdate) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[nodeUpdateParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	args := db.UpdateNodeParams{
		ID: n.ID, Name: n.Name, Address: n.Address, Port: n.Port, ServerPort: n.ServerPort,
		Rate: n.Rate, Tags: n.Tags, Settings: n.Settings, Sort: n.Sort, Backend: n.Backend, Host: n.Host,
	}
	if p.Host != nil {
		args.Host = strings.TrimSpace(*p.Host)
	}
	if p.Backend != nil {
		args.Backend = *p.Backend
	}
	if p.Name != nil {
		args.Name = *p.Name
	}
	if p.Address != nil {
		args.Address = *p.Address
	}
	if p.Port != nil {
		args.Port = int32(*p.Port)
	}
	if p.ServerPort != nil {
		args.ServerPort = int32(*p.ServerPort)
	}
	if p.Rate != nil {
		args.Rate = *p.Rate
	}
	if p.Tags != nil {
		args.Tags = p.Tags
	}
	if len(p.Settings) > 0 {
		args.Settings = p.Settings
	}
	if p.Sort != nil {
		args.Sort = int32(*p.Sort)
	}
	if p.MonthlyCost != nil {
		if err := q.SetNodeMonthlyCost(ctx, db.SetNodeMonthlyCostParams{ID: n.ID, MonthlyCost: *p.MonthlyCost}); err != nil {
			return nil, err
		}
	}
	updated, err := q.UpdateNode(ctx, args)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierr.Conflict("port_taken", "这台机器上端口 %d 已被占用", args.ServerPort)
		}
		return nil, err
	}
	// 中转入口最后一跳指向节点的对外端口（和节点地址），改了要重新下发
	if updated.Port != n.Port || updated.Address != n.Address {
		if _, err := relay.RefreshNodeForwards(ctx, q, n.ID); err != nil {
			return nil, err
		}
	}
	return map[string]any{"node_id": updated.ID, "version": updated.Version}, nil
}

// ---- node.attach / node.detach ----------------------------------------

type NodeAttach struct {
	d      *Deps
	attach bool
}

func (o *NodeAttach) Kind() string {
	if o.attach {
		return "node.attach"
	}
	return "node.detach"
}
func (*NodeAttach) Risk() change.Risk { return change.Normal }

type attachParams struct {
	Node Ref `json:"node"`
	Plan Ref `json:"plan"`
}

func (o *NodeAttach) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[attachParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	pl, err := findPlan(ctx, q, p.Plan)
	if err != nil {
		return nil, err
	}
	attached, err := q.IsNodeAttached(ctx, db.IsNodeAttachedParams{PlanID: pl.ID, NodeID: n.ID})
	if err != nil {
		return nil, err
	}
	if o.attach && attached {
		return nil, apierr.Conflict("already_attached", "%s 已经挂在%s上", n.Name, pl.Name)
	}
	if !o.attach && !attached {
		return nil, apierr.Conflict("not_attached", "%s 没有挂在%s上", n.Name, pl.Name)
	}
	before, err := planNodeNames(ctx, q, pl.ID)
	if err != nil {
		return nil, err
	}
	users, err := q.CountUsersInPlan(ctx, &pl.ID)
	if err != nil {
		return nil, err
	}
	var after []string
	var summary, step string
	if o.attach {
		after = append(slices.Clone(before), n.Name)
		summary = fmt.Sprintf("把 %s 挂到%s", n.Name, pl.Name)
		step = fmt.Sprintf("把 %s 加入%s的节点列表", n.Name, pl.Name)
	} else {
		after = slices.DeleteFunc(slices.Clone(before), func(s string) bool { return s == n.Name })
		summary = fmt.Sprintf("把 %s 从%s卸载", n.Name, pl.Name)
		step = fmt.Sprintf("把 %s 从%s的节点列表移除", n.Name, pl.Name)
	}
	steps := []string{step}
	if o.attach && n.State == "pending" {
		steps = append(steps, fmt.Sprintf("%s 收到首次心跳后，出现在 %d 名用户的订阅里", n.Name, users))
	} else {
		steps = append(steps, fmt.Sprintf("%d 名用户的订阅在下次更新时生效", users))
	}
	return &change.Draft{
		Summary: summary,
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: fmt.Sprintf("%s 的节点", pl.Name), Before: before, After: after}},
		Impact:  change.Impact{Users: users, Nodes: 1},
		Targets: []change.Target{nodeTarget(n), planTarget(pl)},
	}, nil
}

func (o *NodeAttach) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[attachParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	pl, err := findPlan(ctx, q, p.Plan)
	if err != nil {
		return nil, err
	}
	if o.attach {
		_, err = q.AttachNode(ctx, db.AttachNodeParams{PlanID: pl.ID, NodeID: n.ID})
	} else {
		_, err = q.DetachNode(ctx, db.DetachNodeParams{PlanID: pl.ID, NodeID: n.ID})
	}
	if err != nil {
		return nil, err
	}
	if err := q.BumpPlanVersion(ctx, pl.ID); err != nil {
		return nil, err
	}
	if err := q.BumpNodeVersion(ctx, n.ID); err != nil {
		return nil, err
	}
	return map[string]any{"node": n.Code, "plan": pl.Code, "attached": o.attach}, nil
}

// ---- node.delete -------------------------------------------------------

type NodeDelete struct{ d *Deps }

func (*NodeDelete) Kind() string      { return "node.delete" }
func (*NodeDelete) Risk() change.Risk { return change.Sensitive }

type nodeRefParams struct {
	Node Ref `json:"node"`
}

func (o *NodeDelete) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[nodeRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	users, err := q.CountUsersOfNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	plans, err := q.ListNodePlans(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(plans))
	for _, pl := range plans {
		names = append(names, pl.Name)
	}
	steps := []string{fmt.Sprintf("删除节点 %s（%s）", n.Name, n.Code)}
	if len(names) > 0 {
		steps = append(steps, fmt.Sprintf("从%s中移除，%d 名用户的订阅里不再有这个节点", strings.Join(names, "、"), users))
	}
	steps = append(steps, "节点密钥随之失效，节点后端将无法再拉取用户")
	return &change.Draft{
		Summary: fmt.Sprintf("删除节点 %s", n.Name),
		Steps:   steps,
		Diff:    []change.DiffEntry{{Field: "节点", Before: n.Name, After: nil}},
		Impact:  change.Impact{Users: users, Nodes: 1},
		Targets: []change.Target{nodeTarget(n)},
	}, nil
}

func (o *NodeDelete) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[nodeRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	plans, err := q.ListNodePlans(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	for _, pl := range plans {
		if err := q.BumpPlanVersion(ctx, pl.ID); err != nil {
			return nil, err
		}
	}
	if err := q.DeleteNode(ctx, n.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": n.Code}, nil
}

// ---- node.rotate_key ---------------------------------------------------

type NodeRotateKey struct{ d *Deps }

func (*NodeRotateKey) Kind() string      { return "node.rotate_key" }
func (*NodeRotateKey) Risk() change.Risk { return change.Normal }

func (o *NodeRotateKey) Plan(ctx context.Context, q *db.Queries, raw json.RawMessage) (*change.Draft, error) {
	p, err := decode[nodeRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	users, err := q.CountUsersOfNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	return &change.Draft{
		Summary: fmt.Sprintf("轮换节点 %s 的通信密钥", n.Name),
		Steps: []string{
			"生成新密钥，旧密钥立即失效",
			fmt.Sprintf("在节点机器上用新的安装命令更新配置前，%s 无法同步用户和上报流量", n.Name),
		},
		Impact:  change.Impact{Users: users, Nodes: 1},
		Targets: []change.Target{nodeTarget(n)},
	}, nil
}

func (o *NodeRotateKey) Apply(ctx context.Context, q *db.Queries, raw json.RawMessage) (any, error) {
	p, err := decode[nodeRefParams](raw)
	if err != nil {
		return nil, err
	}
	n, err := findNode(ctx, q, p.Node)
	if err != nil {
		return nil, err
	}
	_, hash, sealed := newNodeKey(o.d.Box)
	if err := q.SetNodeKey(ctx, db.SetNodeKeyParams{ID: n.ID, KeyHash: hash, KeyCipher: sealed}); err != nil {
		return nil, err
	}
	return map[string]any{"node": n.Code, "next": fmt.Sprintf("用 patato node install-cmd %s 取得新的安装命令", n.Code)}, nil
}

// ---- helpers -----------------------------------------------------------

// chooseBackend 决定节点由谁承载。不指定时：机器已接入 patato-node 则用它，否则用 V2bX / XrayR（UniProxy）。
func chooseBackend(ctx context.Context, q *db.Queries, host, want string) (string, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	switch want {
	case "uniproxy", "v2bx", "xrayr":
		return "uniproxy", nil
	case "native", "patato-node":
		if host == "" {
			return "", apierr.BadRequest("missing_host", "patato-node 按机器承载节点，需要传 host").
				WithHint("传 host（tolato 中的机器名），或改用 backend: uniproxy")
		}
		return "native", nil
	case "":
		if host != "" {
			if m, err := q.GetMachineByHost(ctx, host); err == nil && m.TokenHash != nil {
				return "native", nil
			}
		}
		return "uniproxy", nil
	}
	return "", apierr.BadRequest("invalid_backend", "backend %q 不支持", want).WithHint("可选 native（patato-node）或 uniproxy（V2bX / XrayR）")
}

func applyHint(backend string) string {
	if backend == "native" {
		return "patato-node 收到推送后立即生效"
	}
	return "节点后端在下次拉取配置时生效"
}

// newNodeKey 生成节点密钥：明文只在安装命令里出现，库里保存哈希（用于校验）和密文（用于再次生成安装命令）。
func newNodeKey(box *secret.Box) (plain string, hash, sealed []byte) {
	plain = secret.Token("nk")
	return plain, secret.Hash(plain), box.Seal([]byte(plain))
}

func allocatePort(ctx context.Context, q *db.Queries, host string, tpl db.NodeTemplate, want int) (int, error) {
	used := map[int32]bool{}
	if host != "" {
		ports, err := q.UsedServerPorts(ctx, host)
		if err != nil {
			return 0, err
		}
		for _, p := range ports {
			used[p] = true
		}
	}
	if want > 0 {
		if want < 1 || want > 65535 {
			return 0, apierr.BadRequest("invalid_port", "端口 %d 不合法", want)
		}
		if used[int32(want)] {
			return 0, apierr.Conflict("port_taken", "机器 %s 上端口 %d 已被占用", host, want).WithHint("不传 port，让服务端在模板范围内自动分配")
		}
		return want, nil
	}
	for p := tpl.PortMin; p <= tpl.PortMax; p++ {
		if !used[p] {
			return int(p), nil
		}
	}
	return 0, apierr.Conflict("no_free_port", "机器 %s 在模板端口范围 %d-%d 内没有空闲端口", host, tpl.PortMin, tpl.PortMax)
}

func defaultCode(host, address string, port int) string {
	base := strings.ToLower(host)
	if base == "" {
		base = strings.ToLower(address)
	}
	base = regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "node"
	}
	if len(base) > 50 {
		base = base[:50]
	}
	return base
}

func planNodeNames(ctx context.Context, q *db.Queries, planID int64) ([]string, error) {
	nodes, err := q.ListPlanNodes(ctx, planID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(nodes))
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return names, nil
}

// completeSettings 补齐模板里没有写死、每个节点必须独有的密钥：
// Reality 的密钥对与 short_id、Shadowsocks 2022 的服务端密钥、Hysteria2 的混淆密码。
func completeSettings(protocol string, raw json.RawMessage) (json.RawMessage, error) {
	s := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, apierr.BadRequest("invalid_template_settings", "模板 settings 不是合法 JSON")
		}
	}
	switch protocol {
	case "vless", "trojan":
		if toInt(s["tls"]) == 2 {
			rs := asMap(s, "reality_settings")
			if str(rs["private_key"]) == "" {
				priv, pub, err := x25519()
				if err != nil {
					return nil, err
				}
				rs["private_key"], rs["public_key"] = priv, pub
			}
			if str(rs["short_id"]) == "" {
				b := make([]byte, 4)
				_, _ = rand.Read(b)
				rs["short_id"] = hex.EncodeToString(b)
			}
		}
	case "shadowsocks":
		cipher := str(s["cipher"])
		if strings.HasPrefix(cipher, "2022-") && str(s["server_key"]) == "" {
			n := 32
			if cipher == "2022-blake3-aes-128-gcm" {
				n = 16
			}
			b := make([]byte, n)
			_, _ = rand.Read(b)
			s["server_key"] = base64.StdEncoding.EncodeToString(b)
		}
	case "hysteria":
		obfs := asMap(s, "obfs")
		if open, _ := obfs["open"].(bool); open && str(obfs["password"]) == "" {
			obfs["password"] = secret.HexToken(12)
		}
	}
	return json.Marshal(s)
}

func x25519() (priv, pub string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString(k.Bytes()), enc.EncodeToString(k.PublicKey().Bytes()), nil
}

func asMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	v := map[string]any{}
	m[key] = v
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		var i int
		fmt.Sscanf(n, "%d", &i)
		return i
	}
	return 0
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
