package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/subscribe"
)

func meUser(c *gin.Context) db.User { return c.MustGet(ctxUser).(db.User) }

func (s *Server) meLogin(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	u, err := s.d.Auth.UserLogin(c, req.Email, req.Password)
	if err != nil {
		fail(c, err)
		return
	}
	token, exp, err := s.d.Auth.CreateSession(c, auth.KindUser, u.ID, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		fail(c, err)
		return
	}
	s.setCookie(c, cookiePortal, token, exp)
	ok(c, gin.H{"email": u.Email, "expires_at": exp})
}

func (s *Server) meLogout(c *gin.Context) {
	_ = s.d.Auth.EndSession(c, c.GetString(ctxUserToken))
	s.setCookie(c, cookiePortal, "", time.Time{})
	ok(c, gin.H{"logged_out": true})
}

// meForgot 无论邮箱是否存在都返回成功，避免被用来探测账号。
func (s *Server) meForgot(c *gin.Context) {
	var req struct {
		Email string `json:"email"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	token, u, err := s.d.Auth.StartPasswordReset(c, req.Email)
	if err != nil {
		fail(c, err)
		return
	}
	if token != "" {
		link := fmt.Sprintf("%s/reset-password?token=%s", s.d.Cfg.PortalURL, token)
		body := fmt.Sprintf("你好，\n\n点击下面的链接设置新密码，30 分钟内有效：\n\n%s\n\n设置新密码不影响订阅链接，客户端不用重新导入。\n如果不是你本人操作，忽略这封邮件即可。\n\n%s",
			link, s.d.Cfg.ServiceName)
		go func(to string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.d.Mailer.Send(ctx, to, s.d.Cfg.ServiceName+" 找回密码", body); err != nil {
				s.d.Log.Error("send reset email", "err", err)
			}
		}(u.Email)
	}
	ok(c, gin.H{"sent": true, "expires_in": 1800})
}

func (s *Server) meReset(c *gin.Context) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	if _, err := s.d.Auth.ResetPassword(c, req.Token, req.Password); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"reset": true})
}

func (s *Server) meChangePassword(c *gin.Context) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := bindJSON(c, &req); err != nil {
		fail(c, err)
		return
	}
	if err := s.d.Auth.ChangePassword(c, meUser(c), c.GetString(ctxUserToken), req.Current, req.New); err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"changed": true})
}

type meNode struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
}

func (s *Server) meSubscription(c *gin.Context) {
	u := meUser(c)
	now := time.Now()
	var plan gin.H
	if u.PlanID != nil {
		if p, err := s.d.Store.GetPlan(c, *u.PlanID); err == nil {
			plan = gin.H{"code": p.Code, "name": p.Name, "reset_method": p.ResetMethod}
		}
	}
	devices, _ := s.d.Store.UserDeviceCount(c, db.UserDeviceCountParams{UserID: u.ID, UpdatedAt: now.Add(-s.d.Cfg.Node.DeviceTTL.D())})
	deviceLimit := int32(0)
	if u.DeviceLimit != nil {
		deviceLimit = *u.DeviceLimit
	} else if u.PlanID != nil {
		if p, err := s.d.Store.GetPlan(c, *u.PlanID); err == nil {
			deviceLimit = p.DeviceLimit
		}
	}
	nodes := []meNode{}
	if u.PlanID != nil {
		if rows, err := s.d.Store.ListPlanNodes(c, *u.PlanID); err == nil {
			for _, n := range rows {
				if n.State == "pending" {
					continue
				}
				status := "正常"
				if n.State != "online" {
					status = "维护中"
				}
				nodes = append(nodes, meNode{Name: n.Name, Region: n.Region, Status: status})
			}
		}
	}
	state := userState(u, now)
	var daysLeft *int
	if u.ExpiresAt != nil {
		d := int(u.ExpiresAt.Sub(now).Hours() / 24)
		if d < 0 {
			d = 0
		}
		daysLeft = &d
	}
	ok(c, gin.H{
		"email":            u.Email,
		"plan":             plan,
		"state":            state,
		"transfer_bytes":   u.TransferBytes,
		"used_bytes":       u.UsedUp + u.UsedDown,
		"remaining_bytes":  max64(0, u.TransferBytes-u.UsedUp-u.UsedDown),
		"next_reset_at":    u.NextResetAt,
		"expires_at":       u.ExpiresAt,
		"days_left":        daysLeft,
		"devices_online":   devices,
		"device_limit":     deviceLimit,
		"subscription_url": subscribe.URL(s.d.Cfg.SubscribeURL, s.d.Cfg.SubscribePath, u.SubToken),
		"nodes":            nodes,
		"service_name":     s.d.Cfg.ServiceName,
	})
}

// meResetToken 由用户本人重置订阅：旧链接和旧连接凭证立即失效。
func (s *Server) meResetToken(c *gin.Context) {
	u := meUser(c)
	token := secret.HexToken(16)
	err := s.d.Store.Tx(c, func(q *db.Queries, _ pgx.Tx) error {
		if err := q.ResetUserSubscription(c, db.ResetUserSubscriptionParams{ID: u.ID, SubToken: token, Uuid: secret.UUID()}); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"summary": "用户本人重置订阅链接"})
		return q.InsertAudit(c, db.InsertAuditParams{
			SubjectKind: "user", SubjectID: u.ID, SubjectLabel: u.Email, Entry: "portal", Action: "user.reset_token",
			Target: fmt.Sprintf("user:%d", u.ID), Risk: "normal", Result: "success", Detail: detail,
		})
	})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, gin.H{"subscription_url": subscribe.URL(s.d.Cfg.SubscribeURL, s.d.Cfg.SubscribePath, token)})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
