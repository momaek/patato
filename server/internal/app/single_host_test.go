package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/momaek/patato/server/internal/config"
)

// TestSingleHost 覆盖单域名部署：Admin 挂在隐藏路径下，与 API、用户侧共用一个域名。
// Origin、CORS 和 passkey 用 Admin 的 origin 校验；未登录可访问的接口不能泄露隐藏路径。
func TestSingleHost(t *testing.T) {
	const site, admin = "https://panel.test", "https://panel.test/a8f3k2c9e1b0"
	// 按真实部署从 YAML 读配置，走默认值推导（passkey origins 等）
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yml := "database_url: postgres://x\nsecret_key: " + strings.Repeat("k", 40) + "\npublic_url: " + site +
		"\nadmin_url: " + admin + "/\nportal_url: " + site + "\nwebauthn:\n  rp_id: panel.test\n"
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *config.Config) {
		c.PublicURL, c.AdminURL, c.PortalURL = loaded.PublicURL, loaded.AdminURL, loaded.PortalURL
		c.WebAuthn = loaded.WebAuthn
	})
	if err := h.a.Cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := h.a.Cfg.WebAuthn.Origins; len(got) != 1 || got[0] != site {
		t.Fatalf("passkey origins must be the admin origin without path: %v", got)
	}

	// Admin 会话的写请求：浏览器发的 Origin 不带路径
	cookie := h.operatorCookie()
	withOrigin := func(origin string) map[string]string {
		m := map[string]string{"Origin": origin}
		for k, v := range cookie {
			m[k] = v
		}
		return m
	}
	r := h.do("POST", "/api/v1/auth/passkeys:begin", nil, withOrigin(site))
	expect(t, r, 200)
	if !strings.Contains(string(r.raw), `"panel.test"`) {
		t.Fatalf("passkey options: %s", r.raw)
	}
	expect(t, h.do("POST", "/api/v1/auth/passkeys:begin", nil, withOrigin("https://evil.test")), 403)
	expect(t, h.do("POST", "/api/v1/auth/passkeys:begin", nil, withOrigin(admin)), 403)

	// CORS 只认 origin
	r = h.do("OPTIONS", "/api/v1/auth/me", nil, map[string]string{"Origin": site})
	if r.header.Get("Access-Control-Allow-Origin") != site {
		t.Fatalf("cors: %v", r.header)
	}

	// 登录后的 whoami 可以给出完整地址，CLI 用它打开审批页
	r = h.api("GET", "/api/v1/auth/me", nil)
	expect(t, r, 200)
	if r.str("admin_url") != admin {
		t.Fatalf("whoami admin_url: %s", r.raw)
	}

	// 未登录的 CLI 授权只拿到相对路径
	r = h.do("POST", "/api/v1/auth/cli/grants", map[string]any{"client_name": "t", "scope": "read"}, nil)
	expect(t, r, 200)
	if r.str("verification_url") != "" || !strings.HasPrefix(r.str("verification_path"), "/cli-auth?code=") {
		t.Fatalf("cli grant: %s", r.raw)
	}
	for _, p := range []string{"/api/v1/tools", "/skill.md", "/api/v1/me/config", "/healthz"} {
		if r := h.do("GET", p, nil, nil); strings.Contains(string(r.raw), "a8f3k2c9e1b0") {
			t.Fatalf("%s leaks the admin path: %s", p, r.raw)
		}
	}
}
