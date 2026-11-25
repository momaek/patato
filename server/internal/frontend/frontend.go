// Package frontend 按配置的域名和路径前缀提供两个 SPA，API 等服务端路由始终优先。
//
// Admin 可以挂在独立域名上，也可以挂在用户侧域名的一段路径下（如 /a8f3k2c9），
// 后一种情况只需要一个域名；未命中 Admin 前缀的页面都交给用户侧，不暴露 Admin 的存在。
package frontend

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type mount struct {
	prefix string // "" 或 "/a8f3k2c9"
	fs     fs.FS
}

type Handler struct {
	hosts map[string][]mount // 每个 Host 下按前缀从长到短排列
}

// New 用两个前端的地址建立分发表；地址可以带路径前缀，未配置或没有构建产物的应用被忽略。
func New(adminURL, portalURL string, admin, portal fs.FS) *Handler {
	h := &Handler{hosts: make(map[string][]mount)}
	for _, app := range []struct {
		url string
		fs  fs.FS
	}{{adminURL, admin}, {portalURL, portal}} {
		u, err := url.Parse(app.url)
		if err != nil || u.Host == "" || app.fs == nil {
			continue
		}
		host := strings.ToLower(u.Host)
		m := mount{prefix: strings.TrimRight(u.Path, "/"), fs: app.fs}
		ms := h.hosts[host]
		// 有前缀的排在前面，空前缀兜底
		if m.prefix != "" {
			ms = append([]mount{m}, ms...)
		} else {
			ms = append(ms, m)
		}
		h.hosts[host] = ms
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
	var app *mount
	for i, m := range h.hosts[strings.ToLower(r.Host)] {
		if m.prefix == "" || p == m.prefix || strings.HasPrefix(p, m.prefix+"/") {
			app = &h.hosts[strings.ToLower(r.Host)][i]
			break
		}
	}
	if app == nil {
		return false
	}

	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Cache-Control", "no-cache")
	if app.prefix != "" && p == app.prefix {
		// 补上结尾的斜杠，页面里的相对资源路径才会落在前缀下
		target := app.prefix + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return true
	}
	name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(p, app.prefix), "/"), "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return true
	}
	data, err := fs.ReadFile(app.fs, name)
	if err != nil {
		// 缺失的 JS / CSS 等文件和资源目录不能返回 HTML，也不列出目录。
		if path.Ext(name) != "" || name == "assets" || strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return true
		}
		name = "index.html"
		data, err = fs.ReadFile(app.fs, name)
		if err != nil {
			http.NotFound(w, r)
			return true
		}
	}
	if name == "index.html" {
		data = withBase(data, app.prefix+"/")
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}

// withBase 在 <head> 后插入 <base href>：Admin 以相对路径构建，靠它把资源和
// vue-router 的 history 落在挂载前缀下；用户侧使用绝对路径，不受影响。
func withBase(page []byte, base string) []byte {
	tag := []byte(`<base href="` + html.EscapeString(base) + `" />`)
	i := bytes.Index(page, []byte("<head>"))
	if i < 0 || bytes.Contains(page, []byte("<base ")) {
		return page
	}
	i += len("<head>")
	out := make([]byte, 0, len(page)+len(tag))
	out = append(out, page[:i]...)
	out = append(out, tag...)
	return append(out, page[i:]...)
}
