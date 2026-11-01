// Package rules 实现分流规则：解析 subconverter 格式的 ini 模板（ACL4SSR 等），
// 按用户的节点生成策略组，并缓存、转换模板引用的规则文件（.list）。
//
// 只实现 ini 里决定分流的两类配置：ruleset（规则来源与目标策略组）和 custom_proxy_group（策略组）。
// 其余键（enable_rule_generator、clash_rule_base 等）忽略，订阅的其他部分仍由 subscribe 包生成。
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// 策略组里的内置目标
const (
	Direct = "DIRECT"
	Reject = "REJECT"
)

// Profile 是解析后的规则模板。
type Profile struct {
	Groups   []GroupSpec
	Rulesets []Ruleset
	// Warnings 是解析时跳过的行，展示给管理员，不影响使用。
	Warnings []string
}

// GroupSpec 是模板里的一个策略组，成员在生成订阅时按用户的节点展开。
type GroupSpec struct {
	Name string
	// Type 为 select、url-test、fallback 或 load-balance。
	Type  string
	Items []Item
	URL   string
	// Interval 是测速间隔（秒），Tolerance 是 url-test 的容差（毫秒）。
	Interval  int
	Tolerance int
}

// Item 是策略组成员：Ref 非空时引用另一个策略组或 DIRECT / REJECT，否则用 Pattern 按名称匹配节点。
type Item struct {
	Ref     string
	Pattern *regexp.Regexp
}

// Ruleset 是一条分流规则：命中 List（远程规则文件）或 Inline（单条规则，如 GEOIP,CN）时走 Target 策略组。
type Ruleset struct {
	Target string
	List   *List
	// Inline 是不带目标的单条规则，例如 GEOIP,CN 或 GEOIP,CN,no-resolve。
	Inline string
	// Final 表示兜底规则（[]FINAL / []MATCH）。
	Final bool
}

// List 是一个远程规则文件。Key 由 URL 计算，用作订阅里的下载地址。
type List struct {
	Key  string
	URL  string
	Name string
}

var groupTypes = map[string]bool{"select": true, "url-test": true, "fallback": true, "load-balance": true}

// acl4ssrLocal 是 ACL4SSR 非在线版模板里的相对路径前缀，对应仓库里的同名文件。
const acl4ssrLocal = "rules/ACL4SSR/"

const acl4ssrRaw = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/"

// Parse 解析 ini 模板。至少要有一个策略组和一条规则，规则和策略组引用的策略组都必须存在。
func Parse(content string) (*Profile, error) {
	p := &Profile{}
	lists := map[string]*List{}
	names := map[string]bool{}
	for n, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == ';' || line[0] == '#' || line[0] == '[' {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		lineNo := n + 1
		switch key {
		case "ruleset", "surge_ruleset":
			rs, warn := parseRuleset(value, lists)
			if warn != "" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("第 %d 行：%s", lineNo, warn))
				continue
			}
			p.Rulesets = append(p.Rulesets, rs)
		case "custom_proxy_group":
			g, warn, err := parseGroup(value)
			if err != nil {
				return nil, fmt.Errorf("第 %d 行：%w", lineNo, err)
			}
			if warn != "" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("第 %d 行：%s", lineNo, warn))
			}
			if names[g.Name] {
				return nil, fmt.Errorf("第 %d 行：策略组 %q 重复定义", lineNo, g.Name)
			}
			names[g.Name] = true
			p.Groups = append(p.Groups, g)
		}
	}
	if len(p.Groups) == 0 {
		return nil, fmt.Errorf("没有找到策略组（custom_proxy_group）")
	}
	if len(p.Rulesets) == 0 {
		return nil, fmt.Errorf("没有找到分流规则（ruleset）")
	}
	known := func(name string) bool { return names[name] || name == Direct || name == Reject }
	for _, g := range p.Groups {
		for _, it := range g.Items {
			if it.Ref != "" && !known(it.Ref) {
				return nil, fmt.Errorf("策略组 %q 引用了不存在的策略组 %q", g.Name, it.Ref)
			}
		}
	}
	for _, rs := range p.Rulesets {
		if !known(rs.Target) {
			return nil, fmt.Errorf("规则引用了不存在的策略组 %q", rs.Target)
		}
	}
	return p, nil
}

func parseRuleset(value string, lists map[string]*List) (Ruleset, string) {
	target, source, ok := strings.Cut(value, ",")
	target, source = strings.TrimSpace(target), strings.TrimSpace(source)
	if !ok || target == "" || source == "" {
		return Ruleset{}, "规则格式应为「策略组,来源」，已跳过"
	}
	if inline, ok := strings.CutPrefix(source, "[]"); ok {
		kind, _, _ := strings.Cut(inline, ",")
		switch strings.ToUpper(kind) {
		case "FINAL", "MATCH":
			return Ruleset{Target: target, Final: true}, ""
		}
		return Ruleset{Target: target, Inline: inline}, ""
	}
	for _, prefix := range []string{"clash-classic:", "surge:"} {
		source = strings.TrimPrefix(source, prefix)
	}
	if i := strings.Index(source, ":"); i > 0 && !strings.HasPrefix(source[i:], "://") {
		return Ruleset{}, fmt.Sprintf("不支持的规则类型 %q，已跳过", source[:i+1])
	}
	// 末尾可以带更新间隔（秒），例如 ...,86400
	if i := strings.LastIndex(source, ","); i > 0 {
		if _, err := strconv.Atoi(source[i+1:]); err == nil {
			source = source[:i]
		}
	}
	if rest, ok := strings.CutPrefix(source, acl4ssrLocal); ok {
		source = acl4ssrRaw + rest
	}
	u, err := url.Parse(source)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Ruleset{}, fmt.Sprintf("规则来源 %q 不是 http(s) 地址，已跳过", source)
	}
	l := lists[source]
	if l == nil {
		l = &List{Key: Key(source), URL: source, Name: listName(source)}
		lists[source] = l
	}
	return Ruleset{Target: target, List: l}, ""
}

func parseGroup(value string) (GroupSpec, string, error) {
	parts := strings.Split(value, "`")
	if len(parts) < 3 {
		return GroupSpec{}, "", fmt.Errorf("策略组格式应为「名称`类型`成员…」")
	}
	g := GroupSpec{Name: strings.TrimSpace(parts[0]), Type: strings.TrimSpace(parts[1])}
	if g.Name == "" {
		return g, "", fmt.Errorf("策略组缺少名称")
	}
	if !groupTypes[g.Type] {
		return g, "", fmt.Errorf("策略组 %q 的类型 %q 不支持，可选 select、url-test、fallback、load-balance", g.Name, g.Type)
	}
	var skipped []string
	items := parts[2:]
	for i := 0; i < len(items); i++ {
		it := strings.TrimSpace(items[i])
		switch {
		case it == "":
		case strings.HasPrefix(it, "[]"):
			g.Items = append(g.Items, Item{Ref: strings.TrimSpace(it[2:])})
		case strings.HasPrefix(it, "!!"):
			skipped = append(skipped, it)
		case g.Type != "select" && (strings.HasPrefix(it, "http://") || strings.HasPrefix(it, "https://")):
			g.URL = it
			if i+1 < len(items) {
				g.Interval, g.Tolerance = parseTiming(items[i+1])
				i++
			}
		default:
			re, err := regexp.Compile(it)
			if err != nil {
				skipped = append(skipped, it)
				continue
			}
			g.Items = append(g.Items, Item{Pattern: re})
		}
	}
	if g.Type != "select" {
		if g.URL == "" {
			g.URL = "http://www.gstatic.com/generate_204"
		}
		if g.Interval <= 0 {
			g.Interval = 300
		}
	}
	warn := ""
	if len(skipped) > 0 {
		warn = fmt.Sprintf("策略组 %q 有不支持的成员 %s，已跳过", g.Name, strings.Join(skipped, "、"))
	}
	return g, warn, nil
}

// parseTiming 解析「间隔,超时,容差」，例如 300,,50。
func parseTiming(s string) (interval, tolerance int) {
	f := strings.Split(s, ",")
	interval, _ = strconv.Atoi(strings.TrimSpace(f[0]))
	if len(f) >= 3 {
		tolerance, _ = strconv.Atoi(strings.TrimSpace(f[2]))
	}
	return interval, tolerance
}

// Key 是规则文件的缓存键，也出现在下发给客户端的下载地址里。
func Key(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:8])
}

var nameUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// listName 用文件名作为规则集名称（例如 LocalAreaNetwork），方便用户在客户端里辨认。
func listName(u string) string {
	p, err := url.Parse(u)
	name := ""
	if err == nil {
		name = strings.TrimSuffix(path.Base(p.Path), path.Ext(p.Path))
	}
	name = nameUnsafe.ReplaceAllString(name, "_")
	if name == "" || name == "_" || name == "." {
		name = "rules"
	}
	return name
}

// Lists 返回模板引用的全部规则文件，按首次出现的顺序。
func (p *Profile) Lists() []*List { return lists(p.Rulesets) }

func lists(rules []Ruleset) []*List {
	seen := map[string]bool{}
	var out []*List
	for _, rs := range rules {
		if rs.List != nil && !seen[rs.List.Key] {
			seen[rs.List.Key] = true
			out = append(out, rs.List)
		}
	}
	return out
}

// Preset 是内置的规则模板，管理员可以一键导入。
type Preset struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceURL   string `json:"source_url"`
}

func acl4ssr(file string) string { return acl4ssrRaw + "Clash/config/" + file }

// Presets 是 ACL4SSR 的在线版模板（规则文件按地址引用，适合由 Patato 缓存后下发）。
// ACL4SSR 以 CC-BY-SA-4.0 发布，这里只引用地址，不随程序分发其内容。
var Presets = []Preset{
	{"acl4ssr-online", "ACL4SSR 标准版", "去广告、自动测速，微软、苹果、谷歌 FCM、电报、国外媒体单独分组", acl4ssr("ACL4SSR_Online.ini")},
	{"acl4ssr-online-mini", "ACL4SSR 精简版", "分组最少：节点选择、自动选择、直连、拦截和漏网之鱼", acl4ssr("ACL4SSR_Online_Mini.ini")},
	{"acl4ssr-online-full", "ACL4SSR 全分组版", "按国家地区自动测速分组，奈飞、油管、AI、游戏平台等单独分组", acl4ssr("ACL4SSR_Online_Full.ini")},
	{"acl4ssr-online-multicountry", "ACL4SSR 多地区版", "标准版加上香港、日本、新加坡、美国地区分组和 AI 分组", acl4ssr("ACL4SSR_Online_MultiCountry.ini")},
	{"acl4ssr-online-noauto", "ACL4SSR 无测速版", "标准版去掉自动测速，所有分组手动选节点", acl4ssr("ACL4SSR_Online_NoAuto.ini")},
	{"acl4ssr-online-adblockplus", "ACL4SSR 加强去广告版", "标准版加上 AdBlock 分组，广告和隐私拦截更完整", acl4ssr("ACL4SSR_Online_AdblockPlus.ini")},
	{"acl4ssr-online-noreject", "ACL4SSR 不拦截版", "标准版去掉广告拦截，只做分流", acl4ssr("ACL4SSR_Online_NoReject.ini")},
}
