package subscribe

import (
	"fmt"
	"strings"

	"github.com/momaek/patato/server/internal/rules"
)

// Rules 是订阅使用的分流规则模板。为 nil 时各格式使用内置的简单规则。
type Rules struct {
	Profile *rules.Profile
	// ListBase 是规则文件的下载地址前缀，例如 https://sub.example.com/api/v1/client/rules/。
	ListBase string
}

func (r *Rules) listURL(l *rules.List, ext string) string { return r.ListBase + l.Key + ext }

// providerNames 给规则文件起不重复的名字（文件名，重名时加序号）。
func providerNames(lists []*rules.List) map[string]string {
	out := map[string]string{}
	used := map[string]int{}
	for _, l := range lists {
		name := l.Name
		if n := used[name]; n > 0 {
			name = fmt.Sprintf("%s_%d", l.Name, n+1)
		}
		used[l.Name]++
		out[l.Key] = name
	}
	return out
}

// splitInline 把 GEOIP,CN,no-resolve 拆成类型、值和参数。
func splitInline(inline string) (typ, value string, opts []string, ok bool) {
	f := strings.Split(inline, ",")
	if len(f) < 2 {
		return "", "", nil, false
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	return strings.ToUpper(f[0]), f[1], f[2:], true
}

// ---- Clash ---------------------------------------------------------------

func clashRuled(cfg yamlMap, names []string, r *Rules) yamlMap {
	l := rules.Build(r.Profile, names)
	groups := make([]yamlMap, 0, len(l.Groups))
	for _, g := range l.Groups {
		members := make([]string, 0, len(g.Members))
		for _, m := range g.Members {
			members = append(members, m.Name)
		}
		gm := yamlMap{{"name", g.Name}, {"type", g.Type}, {"proxies", members}}
		if g.Type != "select" {
			gm = append(gm, yamlMap{{"url", g.URL}, {"interval", g.Interval}}...)
			if g.Type == "url-test" && g.Tolerance > 0 {
				gm = append(gm, yamlMap{{"tolerance", g.Tolerance}}...)
			}
			if g.Type == "load-balance" {
				gm = append(gm, yamlMap{{"strategy", "consistent-hashing"}}...)
			}
		}
		groups = append(groups, gm)
	}
	lists := l.Lists()
	pnames := providerNames(lists)
	providers := yamlMap{}
	for _, li := range lists {
		name := pnames[li.Key]
		providers = append(providers, yamlMap{{name, yamlMap{
			{"type", "http"}, {"behavior", "classical"}, {"url", r.listURL(li, ".yaml")},
			{"path", "./ruleset/" + name + ".yaml"}, {"interval", 86400},
		}}}...)
	}
	var lines []string
	for _, rs := range l.Rules {
		switch {
		case rs.Final:
			lines = append(lines, "MATCH,"+rs.Target)
		case rs.List != nil:
			lines = append(lines, "RULE-SET,"+pnames[rs.List.Key]+","+rs.Target)
		default:
			typ, value, opts, ok := splitInline(rs.Inline)
			if !ok || !rules.ClashSupports(typ) {
				continue
			}
			lines = append(lines, strings.Join(append([]string{typ, value, rs.Target}, opts...), ","))
		}
		if rs.Final {
			break
		}
	}
	out := make(yamlMap, 0, len(cfg)+2)
	for _, kv := range cfg {
		switch kv.K {
		case "proxy-groups":
			kv.V = groups
		case "rules":
			out = append(out, yamlMap{{"rule-providers", providers}}...)
			kv.V = lines
		}
		out = append(out, kv)
	}
	return out
}

// ---- sing-box ------------------------------------------------------------

// sing-box 1.13 起没有 block 出站，拦截只能用路由动作。所以默认选 REJECT 的策略组（例如「全球拦截」）
// 不生成出站，命中它的规则直接 reject；其他组里的 REJECT 成员去掉。
const (
	geoipURL   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-%s.srs"
	geositeURL = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-%s.srs"
)

func singboxRuled(cfg map[string]any, nodeOutbounds []map[string]any, names []string, r *Rules) {
	l := rules.Build(r.Profile, names)
	rejecting := map[string]bool{}
	for _, g := range l.Groups {
		if g.Members[0].Kind == rules.MemberReject {
			rejecting[g.Name] = true
		}
	}
	// proxy 是第一个能出站的策略组，用来下载 GitHub 上的 geoip / geosite 规则集。
	proxy := "direct"
	var groups []map[string]any
	for _, g := range l.Groups {
		if rejecting[g.Name] {
			continue
		}
		var members []string
		for _, m := range g.Members {
			switch {
			case m.Kind == rules.MemberDirect:
				members = append(members, "direct")
			case m.Kind == rules.MemberReject, m.Kind == rules.MemberGroup && rejecting[m.Name]:
			default:
				members = append(members, m.Name)
			}
		}
		if len(members) == 0 {
			members = []string{"direct"}
		}
		o := map[string]any{"tag": g.Name, "outbounds": members}
		if g.Type == "select" {
			o["type"] = "selector"
		} else {
			o["type"], o["url"], o["interval"] = "urltest", g.URL, fmt.Sprintf("%ds", g.Interval)
			if g.Tolerance > 0 {
				o["tolerance"] = g.Tolerance
			}
		}
		if proxy == "direct" {
			proxy = g.Name
		}
		groups = append(groups, o)
	}
	target := func(name string) map[string]any {
		switch {
		case name == rules.Reject || rejecting[name]:
			return map[string]any{"action": "reject"}
		case name == rules.Direct:
			return map[string]any{"outbound": "direct"}
		}
		return map[string]any{"outbound": name}
	}
	lists := l.Lists()
	pnames := providerNames(lists)
	ruleSets := []map[string]any{}
	for _, li := range lists {
		ruleSets = append(ruleSets, map[string]any{"type": "remote", "tag": pnames[li.Key], "format": "source",
			"url": r.listURL(li, ".json"), "download_detour": "direct", "update_interval": "1d"})
	}
	remote := map[string]bool{}
	geo := func(kind, code string) string {
		tag := kind + "-" + strings.ToLower(code)
		if !remote[tag] {
			remote[tag] = true
			url := fmt.Sprintf(geoipURL, strings.ToLower(code))
			if kind == "geosite" {
				url = fmt.Sprintf(geositeURL, strings.ToLower(code))
			}
			ruleSets = append(ruleSets, map[string]any{"type": "remote", "tag": tag, "format": "binary",
				"url": url, "download_detour": proxy, "update_interval": "7d"})
		}
		return tag
	}
	routeRules := []map[string]any{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}
	final := proxy
	for _, rs := range l.Rules {
		var match map[string]any
		switch {
		case rs.Final:
			if t := target(rs.Target); t["outbound"] != nil {
				final = t["outbound"].(string)
			}
		case rs.List != nil:
			match = map[string]any{"rule_set": pnames[rs.List.Key]}
		default:
			typ, value, _, ok := splitInline(rs.Inline)
			if !ok {
				continue
			}
			switch typ {
			case "GEOIP":
				if v := strings.ToUpper(value); v == "LAN" || v == "PRIVATE" {
					match = map[string]any{"ip_is_private": true}
				} else {
					match = map[string]any{"rule_set": geo("geoip", value)}
				}
			case "GEOSITE":
				match = map[string]any{"rule_set": geo("geosite", value)}
			case "DOMAIN":
				match = map[string]any{"domain": []string{value}}
			case "DOMAIN-SUFFIX":
				match = map[string]any{"domain_suffix": []string{value}}
			case "DOMAIN-KEYWORD":
				match = map[string]any{"domain_keyword": []string{value}}
			case "IP-CIDR", "IP-CIDR6":
				match = map[string]any{"ip_cidr": []string{value}}
			}
		}
		if rs.Final {
			break
		}
		if match == nil {
			continue
		}
		for k, v := range target(rs.Target) {
			match[k] = v
		}
		routeRules = append(routeRules, match)
	}
	all := append(groups, nodeOutbounds...)
	cfg["outbounds"] = append(all, map[string]any{"type": "direct", "tag": "direct"})
	cfg["route"] = map[string]any{"auto_detect_interface": true, "final": final, "rules": routeRules, "rule_set": ruleSets}
	// 远程规则集缓存在本地，客户端重启时不必重新下载。
	cfg["experimental"] = map[string]any{"cache_file": map[string]any{"enabled": true}}
}

// ---- Surge ---------------------------------------------------------------

func surgeRuled(names []string, r *Rules) (groupsSection, rulesSection string) {
	l := rules.Build(r.Profile, names)
	var g strings.Builder
	for _, grp := range l.Groups {
		members := make([]string, 0, len(grp.Members))
		for _, m := range grp.Members {
			members = append(members, surgeName(m.Name))
		}
		fmt.Fprintf(&g, "%s = %s, %s", surgeName(grp.Name), grp.Type, strings.Join(members, ", "))
		if grp.Type != "select" {
			fmt.Fprintf(&g, ", url=%s, interval=%d", grp.URL, grp.Interval)
			if grp.Type == "url-test" && grp.Tolerance > 0 {
				fmt.Fprintf(&g, ", tolerance=%d", grp.Tolerance)
			}
		}
		g.WriteByte('\n')
	}
	var b strings.Builder
	for _, rs := range l.Rules {
		t := surgeName(rs.Target)
		switch {
		case rs.Final:
			fmt.Fprintf(&b, "FINAL,%s,dns-failed\n", t)
		case rs.List != nil:
			fmt.Fprintf(&b, "RULE-SET,%s,%s\n", r.listURL(rs.List, ".list"), t)
		default:
			typ, value, opts, ok := splitInline(rs.Inline)
			// Surge 的 GEOIP 没有 LAN；局域网地址由 skip-proxy 和规则文件处理。
			if !ok || !rules.SurgeSupports(typ) || (typ == "GEOIP" && strings.EqualFold(value, "LAN")) {
				continue
			}
			b.WriteString(strings.Join(append([]string{typ, value, t}, opts...), ",") + "\n")
		}
		if rs.Final {
			break
		}
	}
	return g.String(), b.String()
}
