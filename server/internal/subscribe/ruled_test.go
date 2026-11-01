package subscribe

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
	"gopkg.in/yaml.v3"

	"github.com/momaek/patato/server/internal/rules"
)

const testINI = "ruleset=🎯 全球直连,https://example.com/LocalAreaNetwork.list\n" +
	"ruleset=🛑 全球拦截,https://example.com/BanAD.list\n" +
	"ruleset=🎥 奈飞,https://example.com/Netflix.list\n" +
	"ruleset=🎯 全球直连,[]GEOIP,LAN\n" +
	"ruleset=🎯 全球直连,[]GEOIP,CN\n" +
	"ruleset=🐟 漏网之鱼,[]FINAL\n" +
	"custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择`[]DIRECT`.*\n" +
	"custom_proxy_group=♻️ 自动选择`url-test`.*`http://www.gstatic.com/generate_204`300,,50\n" +
	"custom_proxy_group=🎥 奈飞`select`(香港|NF)`[]🚀 节点选择\n" +
	"custom_proxy_group=🇰🇷 韩国`fallback`(KR)`http://www.gstatic.com/generate_204`300\n" +
	"custom_proxy_group=🎯 全球直连`select`[]DIRECT`[]🚀 节点选择\n" +
	"custom_proxy_group=🛑 全球拦截`select`[]REJECT`[]DIRECT\n" +
	"custom_proxy_group=🐟 漏网之鱼`select`[]🚀 节点选择`[]🎯 全球直连`[]🛑 全球拦截`.*\n"

func testRules(t *testing.T) *Rules {
	t.Helper()
	p, err := rules.Parse(testINI)
	if err != nil {
		t.Fatal(err)
	}
	return &Rules{Profile: p, ListBase: "https://sub.example.com/api/v1/client/rules/"}
}

func TestClashWithRules(t *testing.T) {
	b, err := Clash(servers(), testRules(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies   []map[string]any          `yaml:"proxies"`
		Groups    []map[string]any          `yaml:"proxy-groups"`
		Providers map[string]map[string]any `yaml:"rule-providers"`
		Rules     []string                  `yaml:"rules"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Proxies) != 4 || len(cfg.Groups) != 7 || len(cfg.Providers) != 3 {
		t.Fatalf("unexpected clash config:\n%s", b)
	}
	if p := cfg.Providers["Netflix"]; p["behavior"] != "classical" || p["url"] != "https://sub.example.com/api/v1/client/rules/"+rules.Key("https://example.com/Netflix.list")+".yaml" {
		t.Fatalf("provider: %v", p)
	}
	want := []string{"RULE-SET,LocalAreaNetwork,🎯 全球直连", "RULE-SET,BanAD,🛑 全球拦截", "RULE-SET,Netflix,🎥 奈飞",
		"GEOIP,LAN,🎯 全球直连", "GEOIP,CN,🎯 全球直连", "MATCH,🐟 漏网之鱼"}
	if strings.Join(cfg.Rules, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rules:\n%s", strings.Join(cfg.Rules, "\n"))
	}
	for _, g := range cfg.Groups {
		switch g["name"] {
		case "🎥 奈飞":
			if members := g["proxies"].([]any); len(members) != 2 || members[0] != "香港-01" {
				t.Fatalf("netflix group: %v", g)
			}
		case "🇰🇷 韩国":
			if g["type"] != "fallback" || g["proxies"].([]any)[0] != "DIRECT" {
				t.Fatalf("empty group: %v", g)
			}
		case "♻️ 自动选择":
			if g["tolerance"] != 50 || g["interval"] != 300 {
				t.Fatalf("url-test group: %v", g)
			}
		}
	}
	// 其余设置保持内置配置不变
	if !strings.Contains(string(b), "mixed-port: 7890") {
		t.Fatal("base settings lost")
	}
}

func TestSingBoxWithRules(t *testing.T) {
	// 只用不需要额外构建标签（QUIC、uTLS）的协议，让 sing-box 能完整加载配置
	var plain []Server
	for _, s := range servers() {
		if s.Type == "vmess" || s.Type == "shadowsocks" {
			plain = append(plain, s)
		}
	}
	b, err := SingBox(plain, testRules(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	route := cfg["route"].(map[string]any)
	if route["final"] != "🐟 漏网之鱼" {
		t.Fatalf("final: %v", route["final"])
	}
	raw, _ := json.Marshal(route["rules"])
	for _, want := range []string{`{"action":"reject","rule_set":"BanAD"}`, `{"ip_is_private":true,"outbound":"🎯 全球直连"}`,
		`{"outbound":"🎯 全球直连","rule_set":"geoip-cn"}`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing route rule %s in %s", want, raw)
		}
	}
	for _, o := range cfg["outbounds"].([]any) {
		if o.(map[string]any)["tag"] == "🛑 全球拦截" {
			t.Fatal("reject-first group must not become an outbound")
		}
	}

	// 交给 sing-box 校验：能解析，且所有出站、规则集引用都存在
	delete(cfg, "inbounds")
	raw, _ = json.Marshal(cfg)
	ctx := include.Context(context.Background())
	opts, err := singjson.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		t.Fatalf("sing-box rejected config: %v\n%s", err, b)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		t.Fatalf("sing-box rejected config: %v\n%s", err, b)
	}
	_ = instance.Close()
}

func TestSurgeWithRules(t *testing.T) {
	out := Surge(servers(), "https://sub.example.com/s/abc", testRules(t))
	_, rest, _ := strings.Cut(out, "[Proxy Group]\n")
	groups, ruleText, ok := strings.Cut(rest, "[Rule]\n")
	if !ok {
		t.Fatalf("no rule section:\n%s", out)
	}
	if !strings.Contains(groups, "♻️ 自动选择 = url-test, 香港-01, url=http://www.gstatic.com/generate_204, interval=300, tolerance=50\n") {
		t.Fatalf("groups:\n%s", groups)
	}
	want := "RULE-SET,https://sub.example.com/api/v1/client/rules/" + rules.Key("https://example.com/LocalAreaNetwork.list") + ".list,🎯 全球直连\n"
	if !strings.HasPrefix(ruleText, want) || strings.Contains(ruleText, "GEOIP,LAN") || !strings.HasSuffix(ruleText, "FINAL,🐟 漏网之鱼,dns-failed\n") {
		t.Fatalf("rules:\n%s", ruleText)
	}
}
