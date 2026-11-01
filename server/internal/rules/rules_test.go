package rules

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 写法与 ACL4SSR 的在线版模板一致。
const sampleINI = `[custom]
;注释
ruleset=🎯 全球直连,https://example.com/Clash/LocalAreaNetwork.list
ruleset=🛑 全球拦截,https://example.com/Clash/BanAD.list
ruleset=🎥 奈飞,rules/ACL4SSR/Clash/Ruleset/Netflix.list
ruleset=🚀 节点选择,https://example.com/Clash/ProxyLite.list,86400
ruleset=🚀 节点选择,quanx:https://example.com/x.list
ruleset=🎯 全球直连,[]GEOIP,CN
ruleset=🐟 漏网之鱼,[]FINAL

custom_proxy_group=🚀 节点选择` + "`select`[]♻️ 自动选择`[]DIRECT`.*" + `
custom_proxy_group=♻️ 自动选择` + "`url-test`.*`http://www.gstatic.com/generate_204`300,,50" + `
custom_proxy_group=🎥 奈飞` + "`select`(NF|奈飞)`!!GROUPID=0`[]🚀 节点选择" + `
custom_proxy_group=🇰🇷 韩国` + "`url-test`(KR|韩)`http://www.gstatic.com/generate_204`600,,30" + `
custom_proxy_group=🎯 全球直连` + "`select`[]DIRECT`[]🚀 节点选择" + `
custom_proxy_group=🛑 全球拦截` + "`select`[]REJECT`[]DIRECT" + `
custom_proxy_group=🐟 漏网之鱼` + "`select`[]🚀 节点选择`[]🎯 全球直连`.*" + `
enable_rule_generator=true
`

func TestParse(t *testing.T) {
	p, err := Parse(sampleINI)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 7 || len(p.Rulesets) != 6 {
		t.Fatalf("groups %d rulesets %d", len(p.Groups), len(p.Rulesets))
	}
	if len(p.Warnings) != 2 {
		t.Fatalf("warnings: %v", p.Warnings)
	}
	nf := p.Rulesets[2].List
	if nf.URL != "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/Ruleset/Netflix.list" || nf.Name != "Netflix" {
		t.Fatalf("relative ACL4SSR path: %+v", nf)
	}
	if p.Rulesets[3].List.URL != "https://example.com/Clash/ProxyLite.list" {
		t.Fatalf("interval suffix not stripped: %s", p.Rulesets[3].List.URL)
	}
	if p.Rulesets[4].Inline != "GEOIP,CN" || !p.Rulesets[5].Final {
		t.Fatalf("inline rules: %+v", p.Rulesets[4:])
	}
	auto := p.Groups[1]
	if auto.Type != "url-test" || auto.Interval != 300 || auto.Tolerance != 50 || auto.URL != "http://www.gstatic.com/generate_204" {
		t.Fatalf("url-test group: %+v", auto)
	}
	if len(p.Lists()) != 4 {
		t.Fatalf("lists: %d", len(p.Lists()))
	}
}

func TestParseRejectsBrokenTemplates(t *testing.T) {
	cases := map[string]string{
		"no groups":     "ruleset=A,[]FINAL\n",
		"no rulesets":   "custom_proxy_group=A`select`.*\n",
		"unknown group": "ruleset=B,[]FINAL\ncustom_proxy_group=A`select`.*\n",
		"unknown ref":   "ruleset=A,[]FINAL\ncustom_proxy_group=A`select`[]B\n",
		"bad type":      "ruleset=A,[]FINAL\ncustom_proxy_group=A`relay`.*\n",
		"duplicate":     "ruleset=A,[]FINAL\ncustom_proxy_group=A`select`.*\ncustom_proxy_group=A`select`.*\n",
	}
	for name, ini := range cases {
		if _, err := Parse(ini); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestBuild(t *testing.T) {
	p, err := Parse(sampleINI)
	if err != nil {
		t.Fatal(err)
	}
	l := Build(p, []string{"香港-01", "奈飞-HK", "日本-01"})
	sel := l.Group("🚀 节点选择")
	if len(sel.Members) != 5 || sel.Members[0].Kind != MemberGroup || sel.Members[1].Kind != MemberDirect || sel.Members[4].Name != "日本-01" {
		t.Fatalf("select members: %+v", sel.Members)
	}
	nf := l.Group("🎥 奈飞")
	if len(nf.Members) != 2 || nf.Members[0].Name != "奈飞-HK" {
		t.Fatalf("regex members: %+v", nf.Members)
	}
	if kr := l.Group("🇰🇷 韩国"); len(kr.Members) != 1 || kr.Members[0].Kind != MemberDirect {
		t.Fatalf("empty group should fall back to DIRECT: %+v", kr.Members)
	}
	if fish := l.Group("🐟 漏网之鱼"); len(fish.Members) != 5 {
		t.Fatalf("members must not repeat: %+v", fish.Members)
	}

	noFinal, err := Parse("ruleset=A,[]GEOIP,CN\ncustom_proxy_group=A`select`.*\n")
	if err != nil {
		t.Fatal(err)
	}
	l = Build(noFinal, []string{"n1"})
	if last := l.Rules[len(l.Rules)-1]; !last.Final || last.Target != "A" {
		t.Fatalf("missing final rule: %+v", l.Rules)
	}
	if len(noFinal.Rulesets) != 1 {
		t.Fatal("Build must not modify the profile")
	}
}

const sampleList = `# 注释
DOMAIN-SUFFIX,google.com
DOMAIN,www.example.com
DOMAIN-KEYWORD,youtube
IP-CIDR,10.0.0.0/8,no-resolve
IP-CIDR6,fc00::/7,no-resolve
PROCESS-NAME,Thunder.exe
URL-REGEX,^https?://ad\.
USER-AGENT,Foo*
DST-PORT,8080
`

func TestConvertList(t *testing.T) {
	entries := ParseList(sampleList)
	if len(entries) != 9 {
		t.Fatalf("entries: %d", len(entries))
	}
	b, err := Clash(entries)
	if err != nil {
		t.Fatal(err)
	}
	var clash struct {
		Payload []string `yaml:"payload"`
	}
	if err := yaml.Unmarshal(b, &clash); err != nil {
		t.Fatal(err)
	}
	if len(clash.Payload) != 7 || clash.Payload[3] != "IP-CIDR,10.0.0.0/8,no-resolve" {
		t.Fatalf("clash payload: %v", clash.Payload)
	}
	// Clash 的 YAML 形式也能读回来
	if again := ParseList(string(b)); len(again) != 7 {
		t.Fatalf("yaml round trip: %d", len(again))
	}

	surge := string(Surge(entries))
	if !strings.Contains(surge, "URL-REGEX,^https?://ad\\.\n") || !strings.Contains(surge, "DEST-PORT,8080\n") {
		t.Fatalf("surge:\n%s", surge)
	}

	b, err = SingBox(entries)
	if err != nil {
		t.Fatal(err)
	}
	var sb struct {
		Version int              `json:"version"`
		Rules   []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(b, &sb); err != nil {
		t.Fatal(err)
	}
	if sb.Version != 2 || len(sb.Rules) != 3 {
		t.Fatalf("sing-box rule set: %s", b)
	}
	if cidrs := sb.Rules[0]["ip_cidr"].([]any); len(cidrs) != 2 {
		t.Fatalf("ip_cidr: %v", cidrs)
	}
}
