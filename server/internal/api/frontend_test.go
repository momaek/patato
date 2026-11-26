//go:build embedweb

package api

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/subscribe"
	"github.com/momaek/patato/server/internal/uniproxy"
)

// 用实际构建产物经过完整 Gin 路由，保证 NoRoute 不会把 SPA 响应留在 404。
func TestEmbeddedFrontends(t *testing.T) {
	cfg := config.Default()
	cfg.AdminURL, cfg.PortalURL = "https://admin.test", "https://my.test"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Deps{
		Cfg: cfg, Log: log,
		UniProxy:  uniproxy.New(nil, uniproxy.Config{}, nil, log),
		Subscribe: subscribe.New(nil, subscribe.Config{}, nil, log),
	})
	// Admin 以相对路径构建（./assets/...），由注入的 <base href="/"> 解析到根下
	assetPattern := regexp.MustCompile(`(?:src|href)="\.?(/assets/[^"]+)"`)
	for _, tc := range []struct{ host, path, title string }{
		{"admin.test", "/", "Patato Admin"},
		{"admin.test", "/approvals/ap_123?via=cli", "Patato Admin"},
		{"my.test", "/reset-password?token=test", "我的订阅"},
	} {
		r := httptest.NewRequest("GET", "https://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>"+tc.title+"</title>") {
			t.Fatalf("%s%s: %d %s", tc.host, tc.path, w.Code, w.Body.String())
		}
		assets := assetPattern.FindAllStringSubmatch(w.Body.String(), -1)
		if len(assets) == 0 {
			t.Fatal("no built assets in embedded index.html")
		}
		for _, asset := range assets {
			a := httptest.NewRecorder()
			s.ServeHTTP(a, httptest.NewRequest("GET", "https://"+tc.host+asset[1], nil))
			if a.Code != 200 || a.Body.Len() == 0 || a.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Fatalf("%s: %d %v", asset[1], a.Code, a.Header())
			}
		}
	}
	for _, tc := range []struct {
		method, host, path, contentType string
		status                          int
	}{
		{"GET", "admin.test", "/api/v1/missing", "application/json", 404},
		{"GET", "my.test", "/api/v1/tools", "application/json", 200},
		{"GET", "admin.test", "/healthz", "text/plain", 200},
		{"GET", "admin.test", "/skill.md", "text/markdown", 200},
		{"GET", "admin.test", "/metrics", "application/json", 404},
		{"GET", "api.test", "/login", "application/json", 404},
		{"GET", "my.test", "/assets/missing.js", "text/plain", 404},
		{"HEAD", "my.test", "/login", "text/html", 200},
		{"POST", "admin.test", "/login", "application/json", 404},
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(tc.method, "https://"+tc.host+tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Header().Get("Content-Type"), tc.contentType) {
			t.Errorf("%s %s%s: %d %v %s", tc.method, tc.host, tc.path, w.Code, w.Header(), w.Body.String())
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Error("HEAD returned a body")
		}
	}
}

// 单域名部署：Admin 挂在隐藏路径下。用实际构建产物确认 <base> 注入后相对资源都能取到。
func TestEmbeddedAdminUnderPath(t *testing.T) {
	cfg := config.Default()
	cfg.AdminURL, cfg.PortalURL = "https://site.test/a8f3k2c9", "https://site.test"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(Deps{
		Cfg: cfg, Log: log,
		UniProxy:  uniproxy.New(nil, uniproxy.Config{}, nil, log),
		Subscribe: subscribe.New(nil, subscribe.Config{}, nil, log),
	})
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "https://site.test"+p, nil))
		return w
	}
	for _, tc := range []struct{ path, base, title string }{
		{"/a8f3k2c9/", "/a8f3k2c9/", "Patato Admin"},
		{"/a8f3k2c9/approvals/ap_123", "/a8f3k2c9/", "Patato Admin"},
		{"/", "/", "我的订阅"},
		{"/orders", "/", "我的订阅"},
	} {
		w := get(tc.path)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, "<title>"+tc.title+"</title>") || !strings.Contains(body, `<base href="`+tc.base+`" />`) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, body)
		}
		refs := regexp.MustCompile(`(?:src|href)="([^"#]+)"`).FindAllStringSubmatch(body, -1)
		if len(refs) == 0 {
			t.Fatal("no references in index.html")
		}
		for _, ref := range refs {
			// 按浏览器规则相对 <base> 解析
			u, err := url.Parse("https://site.test" + tc.base)
			if err != nil {
				t.Fatal(err)
			}
			target, err := u.Parse(ref[1])
			if err != nil {
				t.Fatal(err)
			}
			if target.Path == tc.base {
				continue // <base href> 自身
			}
			if a := get(target.Path); a.Code != 200 || a.Body.Len() == 0 || strings.Contains(a.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("%s -> %s: %d %v", ref[1], target.Path, a.Code, a.Header())
			}
		}
	}
	if w := get("/a8f3k2c9"); w.Code != 301 || w.Header().Get("Location") != "/a8f3k2c9/" {
		t.Fatalf("redirect: %d %v", w.Code, w.Header())
	}
}
