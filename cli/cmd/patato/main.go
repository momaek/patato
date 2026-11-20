// patato 是 Patato 的命令行客户端，也是 Agent 操作 Patato 的入口。
//
// 它只是 /api/v1 的薄封装：能做什么由服务端和 token 决定。写命令都支持 --dry-run 预演；
// 敏感操作返回审批，由人在 Telegram 或网页上用 passkey 批准，CLI 自己完成不了批准。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/momaek/patato/cli/internal/client"
	"github.com/momaek/patato/cli/internal/config"
)

// version 在发布构建时由 -ldflags "-X main.version=..." 写入。
var version = "dev"

const usage = `patato：Patato 的命令行客户端（短别名 pat）

登录与配置
  patato auth login --url <API 地址> [--scope read|write]   在浏览器里授权这台电脑
  patato auth status | logout
  patato profile list | use <名字> | remove <名字>

查看
  patato status                                  需要处理的事、节点和用户概况
  patato node list [--state online|offline|pending] | show <节点>
  patato machine list | show <机器>
  patato route list | show <线路> | traffic [<线路>] [--since 7d]   中转线路、入口状态与流量
  patato node entry list <节点>
  patato template list | show <模板>
  patato plan list | show <套餐>
  patato rules list | presets | show <规则>          分流规则（ACL4SSR 等 ini 模板）
  patato user list [--q <邮箱>] [--state ...] | show <用户>
  patato usage [--day YYYY-MM-DD]
  patato audit log [--q ...] [--entry cli|web|telegram] [--result ...] | export [--since ...] [--format csv|json]
  patato events list | watch
  patato order list [--status ...] [--user ...] | show <订单号>
  patato coupon list | show <优惠码>
  patato referral list [--status frozen] | settings
  patato ticket list [--needs-human] | show <工单>
  patato report traffic | revenue | nodes [--days N]
  patato webhook list | automation list

变更（都支持 --dry-run 预演、--wait 等待审批、--instruction 记录用户原话）
  patato node provision --template <模板> --plan <套餐> (--host <机器> | --address <地址>)
  patato node update <节点> [--name ...] [--port ...] [--server-port ...] [--rate ...] [--backend native|uniproxy]
  patato node attach | detach <节点> --plan <套餐>
  patato node rotate-key | delete <节点>
  patato node install-cmd <节点> [--command-only]
  patato machine install-cmd <机器> [--command-only] | revoke <机器>
  patato machine set <机器> [--public-address ...] [--port-range 本机段[:外网起点]]... [--nat] [--reserve 段]... [--forward-mode ...]
  patato route create <代号> --name <名称> --via <机器>,<机器> | update | delete <线路>
  patato route apply <线路> (--plan <套餐> | --node <节点>...)   套餐里每个节点经这条线路开一个入口
  patato route discover --node <节点> --entry <入口> [--snapshot 机器=@文件]...   链路发现（只读）
  patato node entry add <节点> [--route <线路>] [--suffix ...] | enable | disable | delete <节点> <入口>
  patato node entry adopt <节点> <入口> --route <线路> --forward 机器:端口=下一跳:端口...
  patato template create | update ...
  patato plan create | update | delete ...       价格用 --price monthly=19.9（单位元）；灰度调价用 --rollout-price
  patato plan history <套餐>
  patato rules create | update | refresh | delete ...  套餐默认规则用 patato plan update <套餐> --rule-profile <规则>
  patato user create | update | reset-traffic | reset-token | delete ...
  patato change show <change_id> | apply <change_id> [--wait]
  patato order refund | mark-paid | cancel <订单号>     退款和手动确认收款是敏感操作
  patato coupon create | update ...                   优惠涉及钱，需要审批（只停用除外）
  patato referral settings --rate ... | release <邀请人>
  patato ticket draft <工单> --body ... [--basis ...] | close <工单>   草稿不会发给用户，由运营者确认发送
  patato webhook create | delete ...
  patato automation create | update | delete ...

审批
  patato approval list [--status pending|approved|rejected] | show <id> [--wait]
  patato approval approve <id>                   在本机浏览器打开确认页，由人用 passkey 批准

全局选项
  --profile <名字>   用哪个 profile（也可以用 $PATATO_PROFILE）
  --json             输出服务端返回的 JSON，供 Agent 解析
  --actor <说明>     调用方自报的身份，写进审计（也可以用 $PATATO_ACTOR），例如「Claude Code 会话」

每条命令加 -h 查看详细选项。
退出码：0 成功；1 出错；2 用法不对；3 变更被拒绝、过期或执行失败。
`

func main() {
	err := run(os.Args[1:], os.Stdout)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return
	case errors.Is(err, errUsage):
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var ex *exitError
	if errors.As(err, &ex) {
		if ex.msg != "" {
			fmt.Fprintln(os.Stderr, "patato:", ex.msg)
		}
		os.Exit(ex.code)
	}
	fmt.Fprintln(os.Stderr, "patato:", err)
	os.Exit(1)
}

// exitError 让命令用指定的退出码结束，例如审批被拒绝时返回 3。
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

type app struct {
	profile string
	json    bool
	actor   string
	out     io.Writer
	ctx     context.Context
	c       *client.Client
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(out, "patato", version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	}
	a := &app{out: out, actor: os.Getenv("PATATO_ACTOR")}
	args, err := a.globalFlags(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a.ctx = ctx

	group, rest := args[0], args[1:]
	switch group {
	case "auth":
		return a.auth(rest)
	case "profile":
		return a.profileCmd(rest)
	}
	if err := a.connect(); err != nil {
		return err
	}
	switch group {
	case "status":
		return a.status(rest)
	case "node", "nodes":
		return a.node(rest)
	case "machine", "machines":
		return a.machine(rest)
	case "route", "routes":
		return a.route(rest)
	case "template", "templates":
		return a.template(rest)
	case "plan", "plans":
		return a.plan(rest)
	case "rules", "rule":
		return a.rules(rest)
	case "user", "users":
		return a.user(rest)
	case "change", "changes":
		return a.change(rest)
	case "approval", "approvals":
		return a.approval(rest)
	case "audit":
		return a.audit(rest)
	case "events":
		return a.events(rest)
	case "usage":
		return a.usage(rest)
	case "order", "orders":
		return a.order(rest)
	case "coupon", "coupons":
		return a.coupon(rest)
	case "referral", "referrals":
		return a.referral(rest)
	case "ticket", "tickets":
		return a.ticket(rest)
	case "report", "reports":
		return a.report(rest)
	case "webhook", "webhooks":
		return a.webhook(rest)
	case "automation", "automations":
		return a.automation(rest)
	}
	return fmt.Errorf("未知命令 %q\n\n%s", group, usage)
}

// globalFlags 取出写在任意位置的 --profile、--json、--actor。遇到 -- 就停：后面的内容原样保留。
func (a *app) globalFlags(args []string) ([]string, error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		take := func(name string) (string, bool, error) {
			if arg == "--"+name || arg == "-"+name {
				if i+1 >= len(args) {
					return "", true, fmt.Errorf("--%s 需要一个值", name)
				}
				i++
				return args[i], true, nil
			}
			for _, p := range []string{"--" + name + "=", "-" + name + "="} {
				if v, ok := strings.CutPrefix(arg, p); ok {
					return v, true, nil
				}
			}
			return "", false, nil
		}
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if arg == "--json" || arg == "-json" {
			a.json = true
			continue
		}
		if v, ok, err := take("profile"); err != nil {
			return nil, err
		} else if ok {
			if err := config.ValidateName(v); err != nil {
				return nil, err
			}
			a.profile = v
			continue
		}
		if v, ok, err := take("actor"); err != nil {
			return nil, err
		} else if ok {
			a.actor = v
			continue
		}
		rest = append(rest, arg)
	}
	return rest, nil
}

func (a *app) connect() error {
	_, p, err := config.Load(a.profile)
	if err != nil {
		return err
	}
	a.c = a.newClient(p.URL, p.Token)
	return nil
}

func (a *app) newClient(base, token string) *client.Client {
	actor := a.actor
	if actor == "" {
		actor = "patato CLI"
	}
	return client.New(base, token, actor, "patato-cli/"+version)
}

// get 发一个 GET；--json 时原样输出并返回 printed=true，调用方不再输出人可读格式。
func (a *app) get(path string, query url.Values, out any) (printed bool, err error) {
	resp, err := a.c.Do(a.ctx, "GET", path, query, nil, out)
	if err != nil {
		return false, err
	}
	if a.json {
		printJSON(a.out, resp.Body)
		return true, nil
	}
	return false, nil
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }

func subcommand(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, errUsage
	}
	return args[0], args[1:], nil
}
