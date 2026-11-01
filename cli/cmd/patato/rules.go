package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/momaek/patato/cli/internal/client"
)

// ---- rules（分流规则）------------------------------------------------------

type ruleProfile struct {
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	SourceURL      string     `json:"source_url"`
	UserSelectable bool       `json:"user_selectable"`
	FetchedAt      *time.Time `json:"fetched_at"`
	FetchError     string     `json:"fetch_error"`
	Groups         []string   `json:"groups"`
	RuleCount      int        `json:"rule_count"`
	ListCount      int        `json:"list_count"`
	Warnings       []string   `json:"warnings"`
	ParseError     string     `json:"parse_error"`
	PlanCount      int64      `json:"plan_count"`
	UserCount      int64      `json:"user_count"`
	Lists          []struct {
		Name       string     `json:"name"`
		URL        string     `json:"url"`
		FetchedAt  *time.Time `json:"fetched_at"`
		FetchError string     `json:"fetch_error"`
	} `json:"lists"`
	Version int64 `json:"version"`
}

type rulePreset struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
}

func (a *app) rules(args []string) error {
	sub, rest, err := subcommand(args)
	if err != nil {
		return err
	}
	switch sub {
	case "list", "ls":
		var l list[ruleProfile]
		if printed, err := a.get(epRuleProfiles, nil, &l); printed || err != nil {
			return err
		}
		rows := [][]string{}
		for _, r := range l.Items {
			fetched := "粘贴的内容"
			if r.SourceURL != "" {
				fetched = timeText(r.FetchedAt)
				if r.FetchError != "" {
					fetched = "拉取失败"
				}
			}
			rows = append(rows, []string{r.Code, r.Name, strconv.Itoa(len(r.Groups)), map[bool]string{true: "是", false: "否"}[r.UserSelectable],
				fetched, strconv.FormatInt(r.PlanCount, 10), strconv.FormatInt(r.UserCount, 10)})
		}
		if len(rows) == 0 {
			a.printf("还没有分流规则：用 patato rules presets 查看可导入的 ACL4SSR 模板，patato rules create --preset <代号> 导入\n")
			return nil
		}
		table(a.out, []string{"代号", "名称", "策略组", "用户可选", "最近拉取", "套餐", "自选用户"}, rows, 2, 5, 6)
		return nil
	case "presets":
		var l list[rulePreset]
		if printed, err := a.get(epRulePresets, nil, &l); printed || err != nil {
			return err
		}
		rows := [][]string{}
		for _, p := range l.Items {
			rows = append(rows, []string{p.Code, p.Name, p.Description})
		}
		table(a.out, []string{"代号", "名称", "说明"}, rows)
		return nil
	case "show", "get":
		c := newCommand("rules show", "patato rules show <规则>")
		if err := c.parse(rest); err != nil {
			return err
		}
		ref, err := c.arg(0, "分流规则")
		if err != nil {
			return err
		}
		var r ruleProfile
		if printed, err := a.get(client.Path(epRuleProfile, ref), nil, &r); printed || err != nil {
			return err
		}
		source := "粘贴的内容"
		if r.SourceURL != "" {
			source = r.SourceURL
		}
		pairs := [][2]string{{"规则", fmt.Sprintf("%s（%s）", r.Name, r.Code)}, {"说明", orDash(r.Description)}, {"来源", source}}
		if r.SourceURL != "" {
			pairs = append(pairs, [2]string{"最近拉取", timeText(r.FetchedAt) + "，每 6 小时自动更新"})
		}
		pairs = append(pairs, [][2]string{{"用户自选", map[bool]string{true: "允许", false: "不允许"}[r.UserSelectable]},
			{"使用的用户", strconv.FormatInt(r.UserCount, 10)}, {"规则", fmt.Sprintf("%d 条，引用 %d 个规则文件", r.RuleCount, r.ListCount)},
			{"策略组", strings.Join(r.Groups, "、")}, {"版本", fmt.Sprintf("v%d", r.Version)}}...)
		kv(a.out, pairs)
		if r.FetchError != "" {
			a.printf("\n上次拉取失败：%s（继续使用之前的内容）\n", r.FetchError)
		}
		if r.ParseError != "" {
			a.printf("\n解析失败：%s（使用它的用户暂时拿到内置规则）\n", r.ParseError)
		}
		for _, w := range r.Warnings {
			a.printf("注意：%s\n", w)
		}
		if len(r.Lists) > 0 {
			a.printf("\n规则文件\n")
			for _, l := range r.Lists {
				state := "还没下载"
				if l.FetchError != "" {
					state = "拉取失败：" + l.FetchError
				} else if l.FetchedAt != nil {
					state = timeText(l.FetchedAt)
				}
				a.printf("  %-24s %s\n", l.Name, state)
			}
		}
		return nil
	case "create", "update":
		return a.rulesWrite(sub, rest)
	case "refresh":
		return a.nodeRef(rest, "rules refresh", "patato rules refresh <规则>\n\n立即重新拉取 ini 模板（平时每 6 小时自动拉取一次）。", "POST", epRuleRefresh)
	case "delete", "rm":
		return a.nodeRef(rest, "rules delete", "patato rules delete <规则>\n\n删除分流规则。用它做默认的套餐改用内置规则，自选了它的用户改回跟随套餐。", "DELETE", epRuleProfile)
	}
	return fmt.Errorf("未知命令 rules %s\n\n%s", sub, usage)
}

func (a *app) rulesWrite(verb string, args []string) error {
	help := "patato rules create (--preset <代号> | --url <ini 地址> | --file <ini 文件>) [--code ...] [--name ...]\n\n" +
		"模板是 subconverter 格式的 ini（例如 ACL4SSR），只使用其中的 ruleset 和 custom_proxy_group。\n用 patato rules presets 查看内置的 ACL4SSR 模板。"
	if verb == "update" {
		help = "patato rules update <规则> [--name ...] [--description ...] [--url ... | --file ...] [--user-selectable=false]\n\n只修改给出的字段。"
	}
	c := newCommand("rules "+verb, help)
	code := c.fs.String("code", "", "create：代号；用 --preset 时默认取模板的代号")
	preset := c.fs.String("preset", "", "create：内置模板代号")
	nm := c.fs.String("name", "", "名称，用户侧会看到")
	desc := c.fs.String("description", "", "说明，用户侧选择时显示")
	src := c.fs.String("url", "", "ini 模板地址，服务端每 6 小时重新拉取")
	file := c.fs.String("file", "", "本地 ini 文件，内容直接上传（不再自动更新）")
	selectable := c.fs.Bool("user-selectable", true, "允许用户在用户侧自选（--user-selectable=false 关闭）")
	w := c.writeFlags()
	if err := c.parse(args); err != nil {
		return err
	}
	body := map[string]any{}
	c.copy(body, map[string]any{"name": *nm, "description": *desc, "user_selectable": *selectable})
	if c.set["url"] {
		body["source_url"] = *src
	}
	if c.set["file"] {
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		body["content"] = string(b)
	}
	if verb == "create" {
		c.copy(body, map[string]any{"code": *code, "preset": *preset})
		return a.mutate("POST", epRuleProfiles, body, w)
	}
	ref, err := c.arg(0, "分流规则")
	if err != nil {
		return err
	}
	return a.mutate("PATCH", client.Path(epRuleProfile, ref), body, w)
}
