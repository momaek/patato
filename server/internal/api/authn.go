package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/store/db"
)

func (s *Server) setCookie(c *gin.Context, name, value string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true,
		Secure: s.d.Cfg.Session.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) startOperatorSession(c *gin.Context, op db.Operator) {
	token, exp, err := s.d.Auth.CreateSession(c, auth.KindOperator, op.ID, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		fail(c, err)
		return
	}
	s.setCookie(c, cookieAdmin, token, exp)
	ok(c, gin.H{"operator": gin.H{"id": op.ID, "name": op.Name}, "expires_at": exp})
}

func (s *Server) passkeyRegisterBegin(c *gin.Context) {
	var req struct {
		Invite string `json:"invite"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	opts, id, err := s.d.Auth.BeginRegister(c, req.Invite, nil)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"challenge_id": id, "options": opts})
}

func (s *Server) passkeyRegisterFinish(c *gin.Context) {
	var req struct {
		Invite      string          `json:"invite"`
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	op, err := s.d.Auth.FinishRegister(c, req.ChallengeID, req.Invite, nil, req.Credential)
	if err != nil {
		fail(c, err)
		return
	}
	s.startOperatorSession(c, op)
}

func (s *Server) passkeyAddBegin(c *gin.Context) {
	op := getPrincipal(c).operator
	opts, id, err := s.d.Auth.BeginRegister(c, "", &op)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"challenge_id": id, "options": opts})
}

func (s *Server) passkeyAddFinish(c *gin.Context) {
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	op := getPrincipal(c).operator
	if _, err := s.d.Auth.FinishRegister(c, req.ChallengeID, "", &op, req.Credential); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"added": true})
}

func (s *Server) passkeyLoginBegin(c *gin.Context) {
	opts, id, err := s.d.Auth.BeginLogin(c)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"challenge_id": id, "options": opts})
}

func (s *Server) passkeyLoginFinish(c *gin.Context) {
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	op, err := s.d.Auth.FinishLogin(c, req.ChallengeID, req.Credential)
	if err != nil {
		fail(c, err)
		return
	}
	s.startOperatorSession(c, op)
}

func (s *Server) logout(c *gin.Context) {
	if v, err := c.Cookie(cookieAdmin); err == nil {
		_ = s.d.Auth.EndSession(c, v)
	}
	s.setCookie(c, cookieAdmin, "", time.Time{})
	ok(c, gin.H{"logged_out": true})
}

func (s *Server) whoami(c *gin.Context) {
	p := getPrincipal(c)
	ok(c, gin.H{"kind": p.kind, "operator": gin.H{"id": p.operator.ID, "name": p.operator.Name}, "scope": p.scope,
		"token_id": p.tokenID, "can_approve": p.kind == "operator"})
}

func (s *Server) cliGrantCreate(c *gin.Context) {
	var req struct {
		ClientName string `json:"client_name"`
		Scope      string `json:"scope"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	if req.Scope == "" {
		req.Scope = "write"
	}
	g, err := s.d.Auth.CreateCLIGrant(c, req.ClientName, req.Scope)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, g)
}

func (s *Server) cliGrantToken(c *gin.Context) {
	var req struct {
		Secret string `json:"secret"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	token, status, err := s.d.Auth.ExchangeCLIGrant(c, c.Param("id"), req.Secret)
	if err != nil {
		fail(c, err)
		return
	}
	switch status {
	case "approved":
		ok(c, gin.H{"status": status, "token": token})
	case "pending":
		c.JSON(http.StatusAccepted, gin.H{"status": status})
	default:
		c.JSON(http.StatusGone, gin.H{"status": status})
	}
}

func (s *Server) cliGrantShow(c *gin.Context) {
	g, err := s.d.Auth.CLIGrantByCode(c, c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"user_code": g.UserCode, "client_name": g.ClientName, "scope": g.Scope, "status": g.Status, "expires_at": g.ExpiresAt})
}

func (s *Server) cliGrantApprove(c *gin.Context) {
	if err := s.d.Auth.DecideCLIGrant(c, c.Param("id"), getPrincipal(c).operator, true); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"status": "approved"})
}

func (s *Server) cliGrantDeny(c *gin.Context) {
	if err := s.d.Auth.DecideCLIGrant(c, c.Param("id"), getPrincipal(c).operator, false); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"status": "denied"})
}

func (s *Server) tokenList(c *gin.Context) {
	rows, err := s.d.Store.ListAPITokens(c, getPrincipal(c).operator.ID)
	if err != nil {
		fail(c, err)
		return
	}
	type item struct {
		ID         int64      `json:"id"`
		Name       string     `json:"name"`
		Scope      string     `json:"scope"`
		CreatedAt  time.Time  `json:"created_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
	}
	items := make([]item, 0, len(rows))
	for _, r := range rows {
		items = append(items, item{ID: r.ID, Name: r.Name, Scope: r.Scope, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt})
	}
	ok(c, gin.H{"items": items})
}

func (s *Server) tokenRevoke(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, apierr.BadRequest("invalid_id", "token ID 不对"))
		return
	}
	n, err := s.d.Store.RevokeAPIToken(c, db.RevokeAPITokenParams{ID: id, OperatorID: getPrincipal(c).operator.ID})
	if err != nil {
		fail(c, err)
		return
	}
	if n == 0 {
		fail(c, apierr.NotFound("token_not_found", "token %d 不存在", id))
		return
	}
	ok(c, gin.H{"revoked": id})
}

func (s *Server) telegramBind(c *gin.Context) {
	if s.d.Telegram == nil {
		fail(c, apierr.New(http.StatusServiceUnavailable, "telegram_not_configured", "服务端未配置 Telegram").
			WithHint("在配置里填写 telegram.bot_token 和 telegram.chat_id"))
		return
	}
	code := s.d.Telegram.NewBindCode(getPrincipal(c).operator.ID)
	ok(c, gin.H{"code": code, "expires_in": 600, "how": "在 Telegram 里给机器人发送 /bind " + code})
}
