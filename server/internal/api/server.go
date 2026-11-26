// Package api 是 Patato 的 HTTP 入口：管理接口（Admin 与 CLI 共用）、用户侧接口、
// 节点兼容接口与订阅。所有能力都先以 API 形式存在，网页和 CLI 只是它的客户端。
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/approval"
	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/frontend"
	"github.com/momaek/patato/server/internal/native"
	"github.com/momaek/patato/server/internal/notify"
	"github.com/momaek/patato/server/internal/rules"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/subscribe"
	"github.com/momaek/patato/server/internal/uniproxy"
	"github.com/momaek/patato/web"
)

// TelegramBinder 由 Telegram 通道实现，未配置时为 nil。
type TelegramBinder interface {
	NewBindCode(operatorID int64) string
}

type Deps struct {
	Cfg       *config.Config
	Store     *store.Store
	Engine    *change.Engine
	Approvals *approval.Service
	Auth      *auth.Service
	Box       *secret.Box
	UniProxy  *uniproxy.Handler
	Subscribe *subscribe.Handler
	Native    *native.Handler
	Mailer    notify.Mailer
	Telegram  TelegramBinder
	Commerce  *commerce.Service
	Events    EventEmitter
	Rules     *rules.Service
	Log       *slog.Logger
}

// EventEmitter 记录事件，由 events.Recorder 实现。
type EventEmitter interface {
	Emit(ctx context.Context, q *db.Queries, e events.Event)
}

type Server struct {
	d        Deps
	engine   *gin.Engine
	loc      *time.Location
	frontend *frontend.Handler
}

func New(d Deps) *Server {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(gin.Recovery(), requestLog(d.Log))
	e.HandleMethodNotAllowed = true
	if err := e.SetTrustedProxies(d.Cfg.TrustedProxies); err != nil {
		d.Log.Warn("invalid trusted_proxies, trusting none", "err", err)
		_ = e.SetTrustedProxies(nil)
	}
	s := &Server{d: d, engine: e, loc: d.Cfg.Location()}
	admin, portal := web.Assets()
	s.frontend = frontend.New(d.Cfg.AdminURL, d.Cfg.PortalURL, admin, portal)

	e.Use(s.cors)
	e.GET("/healthz", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	e.GET("/skill.md", s.skill)
	if d.Cfg.MetricsToken != "" {
		e.GET("/metrics", s.metrics)
	}
	d.UniProxy.Register(e)
	d.Subscribe.Register(e)
	for _, r := range s.routes() {
		e.Handle(r.Method, internalPath(r.Path), r.chain(s)...)
	}
	e.NoRoute(func(c *gin.Context) {
		if s.frontend.Serve(c.Writer, c.Request) {
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"code": "not_found", "message": "接口不存在",
			"hint": "用 GET /skill.md 或 GET /api/v1/tools 查看可用接口", "retryable": false}})
	})
	return s
}

// ServeHTTP 把对外的「自定义方法」路径（/nodes:provision、/changes/{id}:apply）
// 改写成内部路由（/nodes/~provision），Gin 的路由不支持路径中的冒号。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if p, ok := rewriteVerb(r.URL.Path); ok {
			r.URL.Path = p
			r.URL.RawPath = ""
		}
	}
	s.engine.ServeHTTP(w, r)
}

func rewriteVerb(path string) (string, bool) {
	slash := strings.LastIndex(path, "/")
	colon := strings.LastIndex(path, ":")
	if colon <= slash {
		return path, false
	}
	return path[:colon] + "/~" + path[colon+1:], true
}

// internalPath 把契约里的路径换成 Gin 路由：{id} → :id，末尾的 :verb → /~verb。
func internalPath(p string) string {
	if q, ok := rewriteVerb(p); ok {
		p = q
	}
	p = strings.ReplaceAll(p, "{", ":")
	return strings.ReplaceAll(p, "}", "")
}

func (s *Server) cors(c *gin.Context) {
	origin := c.GetHeader("Origin")
	if origin != "" && (origin == s.d.Cfg.AdminOrigin() || origin == s.d.Cfg.PortalURL) {
		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Vary", "Origin")
		if c.Request.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Actor")
			h.Set("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
	}
	c.Next()
}

func requestLog(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		p := c.Request.URL.Path
		if (strings.HasPrefix(p, "/api/v1/server/") || strings.HasPrefix(p, "/api/v1/node/")) && c.Writer.Status() < 400 {
			return // 节点高频请求，只记录失败
		}
		log.Info("http", "method", c.Request.Method, "path", c.Request.URL.Path, "status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds())
	}
}
