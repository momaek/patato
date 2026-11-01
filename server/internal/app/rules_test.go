package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/momaek/patato/server/internal/rules"
)

// fakeRuleHost 模拟 GitHub：提供一个 ini 模板和它引用的规则文件。
func fakeRuleHost(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var listHits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Online.ini":
			_, _ = w.Write([]byte("[custom]\n" +
				"ruleset=🎯 全球直连," + srv.URL + "/LocalAreaNetwork.list\n" +
				"ruleset=🎯 全球直连,[]GEOIP,CN\n" +
				"ruleset=🐟 漏网之鱼,[]FINAL\n" +
				"custom_proxy_group=🚀 节点选择`select`[]DIRECT`.*\n" +
				"custom_proxy_group=🎯 全球直连`select`[]DIRECT`[]🚀 节点选择\n" +
				"custom_proxy_group=🐟 漏网之鱼`select`[]🚀 节点选择`[]🎯 全球直连\n"))
		case "/LocalAreaNetwork.list":
			listHits.Add(1)
			_, _ = w.Write([]byte("# 局域网\nDOMAIN-SUFFIX,local\nIP-CIDR,192.168.0.0/16,no-resolve\nURL-REGEX,^http://x\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &listHits
}

func TestRuleProfiles(t *testing.T) {
	h := newHarness(t, nil)
	_, _, _, subTokens := setup(h)
	host, listHits := fakeRuleHost(t)
	ctx := context.Background()

	// 预演：拉取并解析模板，给出策略组和规则数量
	r := h.api("POST", "/api/v1/rule-profiles", map[string]any{"code": "online", "name": "标准版", "source_url": host.URL + "/Online.ini", "dry_run": true})
	expect(t, r, 200)
	if !strings.Contains(string(r.raw), "3 个策略组、3 条规则，引用 1 个规则文件") {
		t.Fatalf("dry run: %s", r.raw)
	}
	expect(t, h.api("POST", "/api/v1/rule-profiles", map[string]any{"code": "online", "name": "标准版", "source_url": host.URL + "/Online.ini"}), 200)
	// 拉不到的地址在预演时就报错
	r = h.api("POST", "/api/v1/rule-profiles", map[string]any{"code": "broken", "name": "坏的", "source_url": host.URL + "/missing.ini"})
	expect(t, r, 400)
	// 粘贴内容，并且不允许用户自选
	expect(t, h.api("POST", "/api/v1/rule-profiles", map[string]any{"code": "mini", "name": "精简", "user_selectable": false,
		"content": "ruleset=A,[]FINAL\ncustom_proxy_group=A`select`.*\n"}), 200)
	expect(t, h.api("POST", "/api/v1/rule-profiles", map[string]any{"preset": "nope"}), 400)

	r = h.api("GET", "/api/v1/rule-profiles/online", nil)
	expect(t, r, 200)
	if r.str("rule_count") != "3" || r.str("source_url") != host.URL+"/Online.ini" {
		t.Fatalf("detail: %s", r.raw)
	}

	// 还没设置时用内置规则
	sub := func(token string) string {
		r := h.do("GET", "/api/v1/client/subscribe?token="+token+"&flag=clash", nil, nil)
		expect(t, r, 200)
		return string(r.raw)
	}
	// lastRule 返回 Clash 配置的兜底规则（YAML 里的 emoji 会被转义，不能直接按字符串比较）
	lastRule := func(token string) string {
		var c struct {
			Rules []string `yaml:"rules"`
		}
		out := sub(token)
		if err := yaml.Unmarshal([]byte(out), &c); err != nil || len(c.Rules) == 0 {
			t.Fatalf("bad clash config: %v\n%s", err, out)
		}
		return c.Rules[len(c.Rules)-1]
	}
	if out := sub(subTokens[0]); strings.Contains(out, "rule-providers") || lastRule(subTokens[0]) != "MATCH,节点选择" {
		t.Fatalf("builtin rules expected:\n%s", out)
	}

	// 套餐默认规则：影响套餐下的 3 名用户
	r = h.api("PATCH", "/api/v1/plans/premium", map[string]any{"rule_profile": "online", "dry_run": true})
	expect(t, r, 200)
	if r.str("impact.users") != "3" {
		t.Fatalf("plan impact: %s", r.raw)
	}
	expect(t, h.api("PATCH", "/api/v1/plans/premium", map[string]any{"rule_profile": "online"}), 200)
	expect(t, h.api("PATCH", "/api/v1/plans/premium", map[string]any{"rule_profile": "nope"}), 404)

	out := sub(subTokens[0])
	var cfg struct {
		Providers map[string]map[string]any `yaml:"rule-providers"`
		Rules     []string                  `yaml:"rules"`
	}
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers["LocalAreaNetwork"]
	if p == nil || len(cfg.Rules) != 3 || cfg.Rules[2] != "MATCH,🐟 漏网之鱼" {
		t.Fatalf("profile rules expected:\n%s", out)
	}
	listURL := p["url"].(string)
	if !strings.HasPrefix(listURL, "http://example.com/api/v1/client/rules/") {
		t.Fatalf("list url should use the subscription host: %s", listURL)
	}

	// 规则文件从订阅域名下发：第一次请求时拉取，之后用缓存；不支持的规则类型跳过
	path := strings.TrimPrefix(listURL, "http://example.com")
	r = h.do("GET", path, nil, nil)
	expect(t, r, 200)
	if !strings.Contains(string(r.raw), "IP-CIDR,192.168.0.0/16,no-resolve") || strings.Contains(string(r.raw), "URL-REGEX") {
		t.Fatalf("clash list: %s", r.raw)
	}
	expect(t, h.do("GET", strings.Replace(path, ".yaml", ".json", 1), nil, nil), 200)
	r = h.do("GET", strings.Replace(path, ".yaml", ".list", 1), nil, nil)
	if !strings.Contains(string(r.raw), "URL-REGEX,^http://x") {
		t.Fatalf("surge list: %s", r.raw)
	}
	if listHits.Load() != 1 {
		t.Fatalf("list fetched %d times, want 1 (cached)", listHits.Load())
	}
	expect(t, h.do("GET", "/api/v1/client/rules/deadbeef.yaml", nil, nil), 404)

	// 用户侧：只能选允许自选的规则
	auth := h.login("u1@example.com", "password-123")
	r = h.do("GET", "/api/v1/me/rule-profiles", nil, auth)
	expect(t, r, 200)
	if r.str("effective.code") != "online" || r.str("plan_default.code") != "online" || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("me rule profiles: %s", r.raw)
	}
	expect(t, h.do("PUT", "/api/v1/me/rule-profile", map[string]any{"rule_profile": "mini"}, auth), 400)

	// 管理员允许自选后，用户换成精简版，只影响他自己
	expect(t, h.api("PATCH", "/api/v1/rule-profiles/mini", map[string]any{"user_selectable": true}), 200)
	expect(t, h.do("PUT", "/api/v1/me/rule-profile", map[string]any{"rule_profile": "mini"}, auth), 200)
	if got := lastRule(subTokens[0]); got != "MATCH,A" {
		t.Fatalf("user choice ignored: %s", got)
	}
	if got := lastRule(subTokens[1]); got != "MATCH,🐟 漏网之鱼" {
		t.Fatalf("other users keep the plan default: %s", got)
	}
	r = h.do("GET", "/api/v1/me/subscription", nil, auth)
	if r.str("rule_profile.code") != "mini" {
		t.Fatalf("subscription rule profile: %s", r.raw)
	}

	// 取消自选资格：已选的用户回到套餐默认
	expect(t, h.api("PATCH", "/api/v1/rule-profiles/mini", map[string]any{"user_selectable": false}), 200)
	if got := lastRule(subTokens[0]); got != "MATCH,🐟 漏网之鱼" {
		t.Fatalf("should fall back to plan default: %s", got)
	}
	// 用户改回跟随套餐
	expect(t, h.do("PUT", "/api/v1/me/rule-profile", map[string]any{"rule_profile": ""}, auth), 200)

	// 删除套餐默认规则：套餐的默认清空，订阅回到内置规则
	r = h.api("DELETE", "/api/v1/rule-profiles/online", map[string]any{"dry_run": true})
	expect(t, r, 200)
	if r.str("impact.users") != "3" {
		t.Fatalf("delete impact: %s", r.raw)
	}
	expect(t, h.api("DELETE", "/api/v1/rule-profiles/online", map[string]any{}), 200)
	if got := lastRule(subTokens[0]); got != "MATCH,节点选择" {
		t.Fatalf("builtin rules after delete: %s", got)
	}
	pl, err := h.a.Store.GetPlanByCode(ctx, "premium")
	if err != nil || pl.RuleProfile != nil {
		t.Fatalf("plan rule profile should be cleared: %v %v", pl.RuleProfile, err)
	}

	// 后台刷新清理不再被引用的规则文件
	if err := h.a.Rules.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.a.Rules.List(ctx, rules.Key(host.URL+"/LocalAreaNetwork.list")); err != rules.ErrNotFound {
		t.Fatalf("orphan list should be removed: %v", err)
	}
}

// TestRulesCLI 用真实的 patato 二进制管理分流规则：上传 ini、设为套餐默认、查看。
func TestRulesCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the patato binary")
	}
	bin, err := buildCLI()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, nil)
	setup(h)
	srv := httptest.NewServer(h.a.HTTP)
	defer srv.Close()
	ini := filepath.Join(t.TempDir(), "mini.ini")
	if err := os.WriteFile(ini, []byte("ruleset=🚀 节点选择,[]GEOIP,CN\nruleset=🚀 节点选择,[]FINAL\ncustom_proxy_group=🚀 节点选择`select`[]DIRECT`.*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := &cliEnv{t: t, bin: bin, env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"PATATO_CONFIG=" + filepath.Join(t.TempDir(), "config.yaml"), "PATATO_URL=" + srv.URL, "PATATO_TOKEN=" + h.token}}

	if out, code := cli.run("rules", "presets"); code != 0 || !strings.Contains(out, "acl4ssr-online") {
		t.Fatalf("rules presets: %d %s", code, out)
	}
	if out, code := cli.run("rules", "create", "--code", "mini", "--name", "精简", "--file", ini, "--user-selectable=false"); code != 0 || !strings.Contains(out, "已执行") {
		t.Fatalf("rules create: %d %s", code, out)
	}
	if out, code := cli.run("plan", "update", "premium", "--rule-profile", "mini", "--dry-run"); code != 0 || !strings.Contains(out, "没有自选规则的用户改用分流规则「精简」") {
		t.Fatalf("plan update dry run: %d %s", code, out)
	}
	if out, code := cli.run("plan", "update", "premium", "--rule-profile", "mini"); code != 0 || !strings.Contains(out, "已执行") {
		t.Fatalf("plan update: %d %s", code, out)
	}
	if out, _ := cli.run("plan", "show", "premium"); !strings.Contains(out, "mini") {
		t.Fatalf("plan show: %s", out)
	}
	if out, _ := cli.run("rules", "show", "mini"); !strings.Contains(out, "粘贴的内容") || !strings.Contains(out, "不允许") || !strings.Contains(out, "3") {
		t.Fatalf("rules show: %s", out)
	}
	if out, code := cli.run("rules", "refresh", "mini"); code == 0 {
		t.Fatalf("refresh of pasted content should fail: %s", out)
	}
	if _, code := cli.run("plan", "update", "premium", "--rule-profile", ""); code != 0 {
		t.Fatal("clear plan rule failed")
	}
	if out, _ := cli.run("plan", "show", "premium"); !strings.Contains(out, "内置规则") {
		t.Fatalf("plan show after clear: %s", out)
	}
}
