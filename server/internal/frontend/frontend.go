// Package frontend 按配置的域名提供两个 SPA，API 等服务端路由始终优先。
package frontend

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type Handler struct {
	apps map[string]fs.FS
}

func New(adminURL, portalURL string, admin, portal fs.FS) *Handler {
	h := &Handler{apps: make(map[string]fs.FS)}
	for _, app := range []struct {
		url string
		fs  fs.FS
	}{{adminURL, admin}, {portalURL, portal}} {
		if u, err := url.Parse(app.url); err == nil && u.Host != "" && app.fs != nil {
			h.apps[strings.ToLower(u.Host)] = app.fs
		}
	}
	return h
}

// Serve 只处理已配置前端域名上的 GET / HEAD，返回是否接管了请求。
// 未命中的 API 必须继续返回 JSON 404，不能被 SPA 的首页掩盖。
func (h *Handler) Serve(w http.ResponseWriter, r *http.Request) bool {
	p := r.URL.Path
	if r.Method != http.MethodGet && r.Method != http.MethodHead ||
		p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/healthz" || p == "/metrics" || p == "/skill.md" {
		return false
	}
	app := h.apps[strings.ToLower(r.Host)]
	if app == nil {
		return false
	}

	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Cache-Control", "no-cache")
	name := strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return true
	}
	data, err := fs.ReadFile(app, name)
	if err != nil {
		// 缺失的 JS / CSS 等文件和资源目录不能返回 HTML，也不列出目录。
		if path.Ext(name) != "" || name == "assets" || strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return true
		}
		name = "index.html"
		data, err = fs.ReadFile(app, name)
		if err != nil {
			http.NotFound(w, r)
			return true
		}
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}
