//go:build embedweb

package api

import (
	"io"
	"log/slog"
	"net/http/httptest"
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
		Subscribe: subscribe.New(nil, subscribe.Config{}, log),
	})
	assetPattern := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
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
