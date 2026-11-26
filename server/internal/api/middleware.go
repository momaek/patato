package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

const (
	cookieAdmin  = "patato_admin"
	cookiePortal = "patato_portal"

	ctxPrincipal = "api.principal"
	ctxUser      = "api.user"
	ctxUserToken = "api.user_token"
)

// principal 是管理接口的调用方：运营者的网页会话，或 CLI / Agent token。
type principal struct {
	kind     string // "operator"（网页会话）或 "token"
	operator db.Operator
	tokenID  int64
	scope    string
	claim    string
}

func (p principal) canWrite() bool { return p.kind == "operator" || p.scope == "write" }

func (p principal) actor() change.Actor {
	a := change.Actor{Kind: p.kind, ID: p.operator.ID, Label: p.operator.Name, Claim: p.claim, CanWrite: p.canWrite()}
	if p.kind == "token" {
		a.ID = p.tokenID
		a.Label = "CLI token，属于 " + p.operator.Name
		a.Entry = "cli"
	} else {
		a.Entry = "web"
	}
	return a
}

func (p principal) subject() string {
	if p.kind == "token" {
		return fmt.Sprintf("token:%d", p.tokenID)
	}
	return fmt.Sprintf("operator:%d", p.operator.ID)
}

func getPrincipal(c *gin.Context) principal { return c.MustGet(ctxPrincipal).(principal) }

// adminAuth 识别管理接口的调用方。Bearer token 优先；否则看运营者会话 Cookie。
func (s *Server) adminAuth(c *gin.Context) {
	claim := strings.TrimSpace(c.GetHeader("X-Actor"))
	if len(claim) > 200 {
		claim = claim[:200]
	}
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		row, err := s.d.Auth.Token(c, strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			fail(c, apierr.ErrUnauthorized.WithHint("token 无效或已吊销；用 patato auth login 重新登录"))
			return
		}
		c.Set(ctxPrincipal, principal{kind: "token", tokenID: row.ID, scope: row.Scope, claim: claim,
			operator: db.Operator{ID: row.OperatorID, Name: row.OperatorName}})
		c.Next()
		return
	}
	cookie, err := c.Cookie(cookieAdmin)
	if err != nil {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	sess, err := s.d.Auth.Session(c, cookie)
	if err != nil || sess.Kind != auth.KindOperator {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	if !s.sameOrigin(c, s.d.Cfg.AdminOrigin()) {
		fail(c, apierr.Forbidden("bad_origin", "请求来源不对"))
		return
	}
	op, err := s.d.Store.GetOperator(c, sess.SubjectID)
	if err != nil {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	c.Set(ctxPrincipal, principal{kind: "operator", operator: op, scope: "write", claim: claim})
	c.Next()
}

// operatorOnly 只接受运营者网页会话。审批、签发 token 这类操作的凭证 Agent 拿不到。
func operatorOnly(c *gin.Context) {
	if getPrincipal(c).kind != "operator" {
		fail(c, apierr.Forbidden("operator_session_required", "此接口只接受运营者的网页会话，CLI / Agent token 无权调用").
			WithHint("在 Admin 网页上操作，或在 Telegram 里处理"))
		return
	}
	c.Next()
}

// writeScope 要求可写权限。
func writeScope(c *gin.Context) {
	if !getPrincipal(c).canWrite() {
		fail(c, apierr.Forbidden("read_only_token", "当前 token 只有只读权限").WithHint("用 patato auth login --scope write 重新授权"))
		return
	}
	c.Next()
}

// portalAuth 识别终端用户会话。
func (s *Server) portalAuth(c *gin.Context) {
	cookie, err := c.Cookie(cookiePortal)
	if err != nil {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	sess, err := s.d.Auth.Session(c, cookie)
	if err != nil || sess.Kind != auth.KindUser {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	if !s.sameOrigin(c, s.d.Cfg.PortalURL) {
		fail(c, apierr.Forbidden("bad_origin", "请求来源不对"))
		return
	}
	u, err := s.d.Store.GetUser(c, sess.SubjectID)
	if err != nil {
		fail(c, apierr.ErrUnauthorized)
		return
	}
	c.Set(ctxUser, u)
	c.Set(ctxUserToken, cookie)
	c.Next()
}

// sameOrigin 防跨站请求：用 Cookie 鉴权的写请求必须来自对应的前端。
// 未配置前端地址时（例如本地开发）不检查。
func (s *Server) sameOrigin(c *gin.Context, want string) bool {
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if want == "" {
		return true
	}
	origin := c.GetHeader("Origin")
	return origin == "" || origin == want
}

// idempotent 实现 Idempotency-Key：同一个 key 重复提交返回首次结果，不重复执行；
// 同一个 key 换了请求内容返回 422。
func (s *Server) idempotent(c *gin.Context) {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		fail(c, apierr.BadRequest("idempotency_key_required", "写接口必须带 Idempotency-Key 请求头").
			WithHint("生成一个随机值（例如 UUID），重试同一个请求时复用它"))
		return
	}
	if len(key) > 200 {
		fail(c, apierr.BadRequest("idempotency_key_invalid", "Idempotency-Key 太长"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		fail(c, apierr.BadRequest("invalid_body", "读取请求体失败"))
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	h := sha256.Sum256([]byte(c.Request.Method + " " + c.Request.URL.Path + "\n" + string(body)))
	subject := getPrincipal(c).subject()

	if prev, err := s.d.Store.GetIdempotencyKey(c, db.GetIdempotencyKeyParams{Subject: subject, Key: key}); err == nil {
		if !bytes.Equal(prev.RequestHash, h[:]) {
			fail(c, apierr.New(http.StatusUnprocessableEntity, "idempotency_key_reused", "这个 Idempotency-Key 已用于另一个请求").
				WithHint("每个不同的请求用新的 key"))
			return
		}
		c.Header("Idempotent-Replayed", "true")
		c.Data(int(prev.StatusCode), "application/json; charset=utf-8", prev.Response)
		c.Abort()
		return
	} else if !store.IsNotFound(err) {
		fail(c, err)
		return
	}

	rec := &recorder{ResponseWriter: c.Writer}
	c.Writer = rec
	c.Next()
	if st := rec.Status(); st < 500 && rec.buf.Len() > 0 && json.Valid(rec.buf.Bytes()) {
		_ = s.d.Store.SaveIdempotencyKey(c, db.SaveIdempotencyKeyParams{
			Subject: subject, Key: key, RequestHash: h[:], StatusCode: int32(st), Response: rec.buf.Bytes(),
		})
	}
}

type recorder struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (r *recorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

func (r *recorder) WriteString(s string) (int, error) {
	r.buf.WriteString(s)
	return r.ResponseWriter.WriteString(s)
}

// ---- 响应 ---------------------------------------------------------------

func ok(c *gin.Context, v any) { c.JSON(http.StatusOK, v) }

func fail(c *gin.Context, err error) {
	e := apierr.From(err)
	if e == apierr.ErrInternal {
		if l, ok := c.Get("api.log"); ok {
			_ = l
		}
	}
	c.AbortWithStatusJSON(e.Status, gin.H{"error": e})
}

// bindJSON 读取 JSON 请求体，空请求体视为 {}。
func bindJSON(c *gin.Context, v any) error {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		return apierr.BadRequest("invalid_body", "读取请求体失败")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return apierr.BadRequest("invalid_json", "请求体不是合法 JSON：%v", err)
	}
	return nil
}
