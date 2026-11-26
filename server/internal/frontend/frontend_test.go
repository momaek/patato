package frontend

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHostRoutingAndSPA(t *testing.T) {
	app := func(title string) fs.FS {
		return fstest.MapFS{
			"index.html":         {Data: []byte("<!doctype html><title>" + title + "</title>")},
			"assets/app-123.js":  {Data: []byte("console.log('" + title + "')")},
			"assets/app-123.css": {Data: []byte("body{}")},
			"assets/font.woff2":  {Data: []byte("font")},
			"assets/nested/a.js": {Data: []byte("nested")},
			"favicon.svg":        {Data: []byte("<svg/>")},
		}
	}
	h := New("https://admin.test", "https://my.test:8443", app("Admin"), app("Portal"))
	for _, tc := range []struct {
		name, method, host, path string
		handled                  bool
		status                   int
		body, contentType, cache string
	}{
		{"admin", "GET", "admin.test", "/", true, 200, "Admin", "text/html", "no-cache"},
		{"admin deep link", "GET", "ADMIN.TEST", "/approvals/ap_123?via=cli", true, 200, "Admin", "text/html", "no-cache"},
		{"trailing slash", "GET", "admin.test", "/approvals/ap_123/", true, 200, "Admin", "text/html", "no-cache"},
		{"portal deep link", "GET", "my.test:8443", "/reset-password?token=x", true, 200, "Portal", "text/html", "no-cache"},
		{"js", "GET", "admin.test", "/assets/app-123.js", true, 200, "Admin", "javascript", "public, max-age=31536000, immutable"},
		{"favicon", "GET", "admin.test", "/favicon.svg", true, 200, "<svg/>", "image/svg+xml", "no-cache"},
		{"head", "HEAD", "my.test:8443", "/login", true, 200, "", "text/html", "no-cache"},
		{"missing js", "GET", "admin.test", "/assets/missing.js", true, 404, "404", "text/plain", "no-cache"},
		{"missing extensionless asset", "GET", "admin.test", "/assets/missing", true, 404, "404", "text/plain", "no-cache"},
		{"asset directory", "GET", "admin.test", "/assets", true, 404, "404", "text/plain", "no-cache"},
		{"nested directory", "GET", "admin.test", "/assets/nested", true, 404, "404", "text/plain", "no-cache"},
		{"traversal", "GET", "admin.test", "/assets/../index.html", true, 404, "404", "text/plain", "no-cache"},
		{"api", "GET", "admin.test", "/api/v1/missing", false, 0, "", "", ""},
		{"api root", "GET", "admin.test", "/api", false, 0, "", "", ""},
		{"health", "GET", "admin.test", "/healthz", false, 0, "", "", ""},
		{"metrics", "GET", "admin.test", "/metrics", false, 0, "", "", ""},
		{"skill", "GET", "admin.test", "/skill.md", false, 0, "", "", ""},
		{"post", "POST", "admin.test", "/login", false, 0, "", "", ""},
		{"api host", "GET", "api.test", "/login", false, 0, "", "", ""},
		{"unconfigured port", "GET", "my.test", "/", false, 0, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://"+tc.host+tc.path, nil)
			w := httptest.NewRecorder()
			if got := h.Serve(w, r); got != tc.handled {
				t.Fatalf("handled = %v", got)
			}
			if !tc.handled {
				if w.Body.Len() != 0 || len(w.Header()) != 0 {
					t.Fatal("unhandled request must be left unchanged")
				}
				return
			}
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) ||
				!strings.Contains(w.Header().Get("Content-Type"), tc.contentType) || w.Header().Get("Cache-Control") != tc.cache {
				t.Fatalf("response: %d %v %q", w.Code, w.Header(), w.Body.String())
			}
			if tc.method == http.MethodHead && (w.Body.Len() != 0 || w.Header().Get("Content-Length") == "") {
				t.Fatal("HEAD must return content length without a body")
			}
			if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" ||
				w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
				t.Fatal("missing frontend security headers")
			}
		})
	}

	r := httptest.NewRequest("GET", "https://unknown.test/", nil)
	r.Header.Set("X-Forwarded-Host", "admin.test")
	if h.Serve(httptest.NewRecorder(), r) {
		t.Fatal("must not select an app using X-Forwarded-Host")
	}
	if New("https://admin.test", "https://my.test", nil, nil).Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "https://admin.test/", nil)) {
		t.Fatal("development builds must leave frontend hosting to Vite")
	}
}

// 单域名：Admin 挂在用户侧域名的隐藏路径下，其余页面都归用户侧。
func TestAdminUnderPath(t *testing.T) {
	app := func(title string) fs.FS {
		return fstest.MapFS{
			"index.html":        {Data: []byte("<!doctype html><html><head><title>" + title + "</title></head></html>")},
			"assets/app-123.js": {Data: []byte("console.log('" + title + "')")},
			"favicon.svg":       {Data: []byte("<svg>" + title + "</svg>")},
		}
	}
	h := New("https://site.test/a8f3k2c9", "https://site.test", app("Admin"), app("Portal"))
	for _, tc := range []struct {
		path     string
		status   int
		body     string
		location string
	}{
		{"/a8f3k2c9/", 200, `<head><base href="/a8f3k2c9/" /><title>Admin`, ""},
		{"/a8f3k2c9/approvals/ap_1?via=cli", 200, "<title>Admin", ""},
		{"/a8f3k2c9/assets/app-123.js", 200, "Admin", ""},
		{"/a8f3k2c9/favicon.svg", 200, "<svg>Admin", ""},
		{"/a8f3k2c9/assets/missing.js", 404, "404", ""},
		{"/a8f3k2c9", 301, "", "/a8f3k2c9/"},
		{"/a8f3k2c9?next=/x", 301, "", "/a8f3k2c9/?next=/x"},
		// 前缀必须按整段匹配，前缀相同的其他路径归用户侧
		{"/a8f3k2c9x", 200, "<title>Portal", ""},
		{"/", 200, `<base href="/" /><title>Portal`, ""},
		{"/login", 200, "<title>Portal", ""},
		{"/assets/app-123.js", 200, "Portal", ""},
		{"/favicon.svg", 200, "<svg>Portal", ""},
		{"/admin", 200, "<title>Portal", ""},
	} {
		w := httptest.NewRecorder()
		if !h.Serve(w, httptest.NewRequest("GET", "https://site.test"+tc.path, nil)) {
			t.Fatalf("%s: not handled", tc.path)
		}
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) || w.Header().Get("Location") != tc.location {
			t.Errorf("%s: %d %q location=%q", tc.path, w.Code, w.Body.String(), w.Header().Get("Location"))
		}
	}
	if h.Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "https://site.test/api/v1/x", nil)) {
		t.Fatal("API must stay with the server")
	}
}
