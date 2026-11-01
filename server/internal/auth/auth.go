// Package auth 管理四类凭证：运营者会话（passkey 登录）、CLI / Agent token、终端用户会话、节点密钥（见 uniproxy）。
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"

	"github.com/momaek/patato/server/internal/apierr"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/password"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
)

const (
	KindOperator = "operator"
	KindUser     = "user"
)

type Service struct {
	st  *store.Store
	cfg *config.Config
	wa  *webauthn.WebAuthn
	log *slog.Logger
	now func() time.Time
}

func New(st *store.Store, cfg *config.Config, log *slog.Logger) (*Service, error) {
	s := &Service{st: st, cfg: cfg, log: log, now: time.Now}
	if cfg.WebAuthnEnabled() {
		wa, err := webauthn.New(&webauthn.Config{
			RPID:          cfg.WebAuthn.RPID,
			RPDisplayName: cfg.WebAuthn.RPName,
			RPOrigins:     cfg.WebAuthn.Origins,
		})
		if err != nil {
			return nil, fmt.Errorf("webauthn: %w", err)
		}
		s.wa = wa
	}
	return s, nil
}

var errPasskeyDisabled = apierr.New(503, "passkey_not_configured", "服务端未配置 passkey").
	WithHint("在配置里填写 webauthn.rp_id 和 admin_url")

// ---- 会话 ---------------------------------------------------------------

// CreateSession 创建会话，返回只给浏览器一次的明文令牌。
func (s *Service) CreateSession(ctx context.Context, kind string, subjectID int64, ua, ip string) (string, time.Time, error) {
	ttl := s.cfg.Session.UserTTL.D()
	if kind == KindOperator {
		ttl = s.cfg.Session.OperatorTTL.D()
	}
	token := secret.Token("ses")
	exp := s.now().Add(ttl)
	err := s.st.CreateSession(ctx, db.CreateSessionParams{
		TokenHash: secret.Hash(token), Kind: kind, SubjectID: subjectID, ExpiresAt: exp, UserAgent: truncate(ua, 200), Ip: ip,
	})
	return token, exp, err
}

func (s *Service) Session(ctx context.Context, token string) (db.Session, error) {
	return s.st.GetSession(ctx, secret.Hash(token))
}

func (s *Service) EndSession(ctx context.Context, token string) error {
	return s.st.DeleteSession(ctx, secret.Hash(token))
}

// ---- API token ----------------------------------------------------------

func (s *Service) Token(ctx context.Context, token string) (db.GetAPITokenByHashRow, error) {
	row, err := s.st.GetAPITokenByHash(ctx, secret.Hash(token))
	if err == nil {
		_ = s.st.TouchAPIToken(ctx, row.ID)
	}
	return row, err
}

func (s *Service) IssueToken(ctx context.Context, operatorID int64, name, scope string) (string, db.ApiToken, error) {
	token := secret.Token("pat")
	row, err := s.st.CreateAPIToken(ctx, db.CreateAPITokenParams{OperatorID: operatorID, Name: name, TokenHash: secret.Hash(token), Scope: scope})
	return token, row, err
}

// ---- 运营者邀请与 passkey ----------------------------------------------

// InviteOperator 生成一次性邀请链接，受邀人在 Admin 上用它注册 passkey。
func (s *Service) InviteOperator(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if _, err := s.st.GetOperatorByName(ctx, name); err == nil {
		return "", fmt.Errorf("operator %q already exists", name)
	}
	token := secret.Token("inv")
	if err := s.st.CreateOperatorInvite(ctx, db.CreateOperatorInviteParams{
		TokenHash: secret.Hash(token), Name: name, ExpiresAt: s.now().Add(24 * time.Hour),
	}); err != nil {
		return "", err
	}
	return token, nil
}

type operatorUser struct {
	op    db.Operator
	creds []webauthn.Credential
}

func (u *operatorUser) WebAuthnID() []byte                         { return u.op.WebauthnUserID }
func (u *operatorUser) WebAuthnName() string                       { return u.op.Name }
func (u *operatorUser) WebAuthnDisplayName() string                { return u.op.Name }
func (u *operatorUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Service) loadOperator(ctx context.Context, op db.Operator) (*operatorUser, error) {
	rows, err := s.st.ListWebAuthnCredentials(ctx, op.ID)
	if err != nil {
		return nil, err
	}
	u := &operatorUser{op: op}
	for _, r := range rows {
		var c webauthn.Credential
		if json.Unmarshal(r.Credential, &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, nil
}

func (s *Service) saveChallenge(ctx context.Context, opID *int64, purpose string, sess *webauthn.SessionData) (string, error) {
	id := secret.ID("wac")
	b, _ := json.Marshal(sess)
	err := s.st.SaveWebAuthnChallenge(ctx, db.SaveWebAuthnChallengeParams{
		ID: id, OperatorID: opID, Purpose: purpose, Session: b, ExpiresAt: s.now().Add(5 * time.Minute),
	})
	return id, err
}

func (s *Service) takeChallenge(ctx context.Context, id, purpose string) (db.WebauthnChallenge, webauthn.SessionData, error) {
	var sess webauthn.SessionData
	ch, err := s.st.TakeWebAuthnChallenge(ctx, id)
	if err != nil {
		return ch, sess, apierr.BadRequest("challenge_expired", "passkey 验证已过期，请重试")
	}
	if ch.Purpose != purpose {
		return ch, sess, apierr.BadRequest("challenge_mismatch", "passkey 验证与当前操作不匹配")
	}
	if err := json.Unmarshal(ch.Session, &sess); err != nil {
		return ch, sess, err
	}
	return ch, sess, nil
}

// BeginRegister 开始注册 passkey：首次注册凭邀请令牌；已登录的运营者也可以再加一把。
func (s *Service) BeginRegister(ctx context.Context, inviteToken string, current *db.Operator) (any, string, error) {
	if s.wa == nil {
		return nil, "", errPasskeyDisabled
	}
	var user *operatorUser
	var purpose string
	var opID *int64
	if current != nil {
		u, err := s.loadOperator(ctx, *current)
		if err != nil {
			return nil, "", err
		}
		user, purpose, opID = u, "register", &current.ID
	} else {
		inv, err := s.st.GetOperatorInvite(ctx, secret.Hash(inviteToken))
		if err != nil {
			return nil, "", apierr.BadRequest("invalid_invite", "邀请链接无效或已过期")
		}
		uid := make([]byte, 32)
		_, _ = rand.Read(uid)
		user = &operatorUser{op: db.Operator{Name: inv.Name, WebauthnUserID: uid}}
		purpose = "register-invite:" + hex.EncodeToString(secret.Hash(inviteToken))
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range user.creds {
		exclude = append(exclude, c.Descriptor())
	}
	opts, sess, err := s.wa.BeginRegistration(user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, "", err
	}
	id, err := s.saveChallenge(ctx, opID, purpose, sess)
	return opts, id, err
}

// FinishRegister 完成注册。凭邀请注册时在这里创建运营者。
func (s *Service) FinishRegister(ctx context.Context, challengeID, inviteToken string, current *db.Operator, credential json.RawMessage) (db.Operator, error) {
	if s.wa == nil {
		return db.Operator{}, errPasskeyDisabled
	}
	purpose := "register"
	if current == nil {
		purpose = "register-invite:" + hex.EncodeToString(secret.Hash(inviteToken))
	}
	_, sess, err := s.takeChallenge(ctx, challengeID, purpose)
	if err != nil {
		return db.Operator{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(credential))
	if err != nil {
		return db.Operator{}, apierr.BadRequest("invalid_credential", "passkey 数据无效：%v", err)
	}
	var op db.Operator
	err = s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		var user *operatorUser
		if current != nil {
			op = *current
			user = &operatorUser{op: op}
		} else {
			inv, err := q.GetOperatorInvite(ctx, secret.Hash(inviteToken))
			if err != nil {
				return apierr.BadRequest("invalid_invite", "邀请链接无效或已过期")
			}
			user = &operatorUser{op: db.Operator{Name: inv.Name, WebauthnUserID: sess.UserID}}
		}
		cred, err := s.wa.CreateCredential(user, sess, parsed)
		if err != nil {
			return apierr.BadRequest("passkey_failed", "passkey 注册失败：%v", err)
		}
		if current == nil {
			if n, err := q.UseOperatorInvite(ctx, secret.Hash(inviteToken)); err != nil || n == 0 {
				return apierr.BadRequest("invalid_invite", "邀请链接已被使用")
			}
			op, err = q.CreateOperator(ctx, db.CreateOperatorParams{Name: user.op.Name, WebauthnUserID: sess.UserID})
			if err != nil {
				return err
			}
		}
		b, _ := json.Marshal(cred)
		return q.AddWebAuthnCredential(ctx, db.AddWebAuthnCredentialParams{ID: cred.ID, OperatorID: op.ID, Credential: b})
	})
	return op, err
}

// BeginLogin 开始 passkey 登录（可发现凭证，无需先输入用户名）。
func (s *Service) BeginLogin(ctx context.Context) (any, string, error) {
	if s.wa == nil {
		return nil, "", errPasskeyDisabled
	}
	opts, sess, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	id, err := s.saveChallenge(ctx, nil, "login", sess)
	return opts, id, err
}

func (s *Service) FinishLogin(ctx context.Context, challengeID string, credential json.RawMessage) (db.Operator, error) {
	if s.wa == nil {
		return db.Operator{}, errPasskeyDisabled
	}
	_, sess, err := s.takeChallenge(ctx, challengeID, "login")
	if err != nil {
		return db.Operator{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(credential))
	if err != nil {
		return db.Operator{}, apierr.BadRequest("invalid_credential", "passkey 数据无效：%v", err)
	}
	var found *operatorUser
	cred, err := s.wa.ValidateDiscoverableLogin(func(_, userHandle []byte) (webauthn.User, error) {
		op, err := s.st.GetOperatorByWebAuthnID(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		found, err = s.loadOperator(ctx, op)
		return found, err
	}, sess, parsed)
	if err != nil || found == nil {
		return db.Operator{}, apierr.New(401, "passkey_failed", "passkey 验证失败")
	}
	s.updateCredential(ctx, cred)
	return found.op, nil
}

// BeginStepUp 为某个具体操作（例如批准审批）要求运营者再次用 passkey 确认。
func (s *Service) BeginStepUp(ctx context.Context, op db.Operator, purpose string) (any, string, error) {
	if s.wa == nil {
		return nil, "", errPasskeyDisabled
	}
	user, err := s.loadOperator(ctx, op)
	if err != nil {
		return nil, "", err
	}
	if len(user.creds) == 0 {
		return nil, "", apierr.BadRequest("no_passkey", "当前运营者还没有注册 passkey")
	}
	opts, sess, err := s.wa.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	id, err := s.saveChallenge(ctx, &op.ID, purpose, sess)
	return opts, id, err
}

// FinishStepUp 校验 passkey 断言：必须是同一个运营者、同一个操作。
func (s *Service) FinishStepUp(ctx context.Context, op db.Operator, purpose, challengeID string, credential json.RawMessage) error {
	if s.wa == nil {
		return errPasskeyDisabled
	}
	ch, sess, err := s.takeChallenge(ctx, challengeID, purpose)
	if err != nil {
		return err
	}
	if ch.OperatorID == nil || *ch.OperatorID != op.ID {
		return apierr.Forbidden("challenge_mismatch", "passkey 验证不属于当前运营者")
	}
	user, err := s.loadOperator(ctx, op)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(credential))
	if err != nil {
		return apierr.BadRequest("invalid_credential", "passkey 数据无效：%v", err)
	}
	cred, err := s.wa.ValidateLogin(user, sess, parsed)
	if err != nil {
		return apierr.New(401, "passkey_failed", "passkey 验证失败")
	}
	s.updateCredential(ctx, cred)
	return nil
}

func (s *Service) updateCredential(ctx context.Context, cred *webauthn.Credential) {
	if cred == nil {
		return
	}
	b, _ := json.Marshal(cred)
	if err := s.st.UpdateWebAuthnCredential(ctx, db.UpdateWebAuthnCredentialParams{ID: cred.ID, Credential: b}); err != nil {
		s.log.Warn("update passkey counter", "err", err)
	}
}

// ---- CLI 设备授权 -------------------------------------------------------

type CLIGrant struct {
	ID              string    `json:"grant_id"`
	Secret          string    `json:"secret"`
	UserCode        string    `json:"user_code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	Interval        int       `json:"interval"`
}

// CreateCLIGrant 由 CLI 发起：返回一个短码，运营者在 Admin 上核对短码后批准。
func (s *Service) CreateCLIGrant(ctx context.Context, clientName, scope string) (*CLIGrant, error) {
	if scope != "read" && scope != "write" {
		return nil, apierr.BadRequest("invalid_scope", "scope 只能是 read 或 write")
	}
	g := &CLIGrant{ID: secret.ID("clg"), Secret: secret.Token("cgs"), UserCode: secret.UserCode(),
		ExpiresAt: s.now().Add(10 * time.Minute), Interval: 3}
	if s.cfg.AdminURL != "" {
		g.VerificationURL = s.cfg.AdminURL + "/cli-auth?code=" + g.UserCode
	}
	err := s.st.CreateCLIGrant(ctx, db.CreateCLIGrantParams{
		ID: g.ID, SecretHash: secret.Hash(g.Secret), UserCode: g.UserCode, Scope: scope,
		ClientName: truncate(clientName, 100), ExpiresAt: g.ExpiresAt,
	})
	return g, err
}

func (s *Service) CLIGrantByCode(ctx context.Context, code string) (db.CliGrant, error) {
	g, err := s.st.GetCLIGrantByUserCode(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if err != nil {
		return g, apierr.NotFound("grant_not_found", "授权码 %s 不存在", code)
	}
	return g, nil
}

func (s *Service) DecideCLIGrant(ctx context.Context, code string, op db.Operator, approve bool) error {
	g, err := s.CLIGrantByCode(ctx, code)
	if err != nil {
		return err
	}
	status := "denied"
	if approve {
		status = "approved"
	}
	n, err := s.st.DecideCLIGrant(ctx, db.DecideCLIGrantParams{ID: g.ID, Status: status, OperatorID: &op.ID})
	if err != nil {
		return err
	}
	if n == 0 {
		return apierr.Conflict("grant_closed", "授权码已处理或已过期")
	}
	return nil
}

// ExchangeCLIGrant 由 CLI 轮询：批准后换取 token，只能换一次。
func (s *Service) ExchangeCLIGrant(ctx context.Context, id, grantSecret string) (string, string, error) {
	g, err := s.st.GetCLIGrant(ctx, id)
	if err != nil || !bytes.Equal(g.SecretHash, secret.Hash(grantSecret)) {
		return "", "", apierr.NotFound("grant_not_found", "授权请求不存在")
	}
	if s.now().After(g.ExpiresAt) {
		return "", "expired", nil
	}
	switch g.Status {
	case "pending", "denied", "consumed":
		return "", g.Status, nil
	}
	consumed, err := s.st.ConsumeCLIGrant(ctx, id)
	if err != nil {
		return "", "consumed", nil
	}
	name := "CLI"
	if consumed.ClientName != "" {
		name = consumed.ClientName
	}
	token, _, err := s.IssueToken(ctx, *consumed.OperatorID, name, consumed.Scope)
	return token, "approved", err
}

// ---- 终端用户 ----------------------------------------------------------

const (
	maxLoginFailures = 5
	loginWindow      = 15 * time.Minute
)

// UserLogin 校验邮箱和密码。同一邮箱连续失败 5 次锁定 15 分钟；迁移来的旧哈希在登录成功后升级。
func (s *Service) UserLogin(ctx context.Context, email, plain string) (db.User, error) {
	key := "login:" + strings.ToLower(strings.TrimSpace(email))
	if f, err := s.st.GetAuthFailure(ctx, key); err == nil && f.LockedUntil != nil && f.LockedUntil.After(s.now()) {
		return db.User{}, apierr.New(429, "login_locked", "连续输错次数太多，请 %d 分钟后再试",
			int(time.Until(*f.LockedUntil).Minutes())+1)
	}
	u, err := s.st.GetUserByEmail(ctx, strings.TrimSpace(email))
	if err != nil || !password.Verify(u.PasswordAlgo, u.PasswordSalt, plain, u.PasswordHash) {
		f, ferr := s.st.RecordAuthFailure(ctx, db.RecordAuthFailureParams{Key: key, WindowSeconds: int32(loginWindow.Seconds())})
		if ferr == nil && f.Failures >= maxLoginFailures {
			until := s.now().Add(loginWindow)
			_ = s.st.LockAuthKey(ctx, db.LockAuthKeyParams{Key: key, LockedUntil: &until})
		}
		return db.User{}, apierr.New(401, "invalid_credentials", "邮箱或密码不对")
	}
	_ = s.st.ClearAuthFailures(ctx, key)
	if password.NeedsUpgrade(u.PasswordAlgo) {
		if err := s.st.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: u.ID, PasswordHash: password.Hash(plain), PasswordAlgo: password.Argon2id}); err != nil {
			s.log.Warn("upgrade password hash", "user", u.ID, "err", err)
		}
	}
	return u, nil
}

const (
	maxRegistersPerIP = 5
	registerWindow    = time.Hour
)

// CheckRegisterRate 限制同一 IP 每小时注册的次数。
func (s *Service) CheckRegisterRate(ctx context.Context, ip string) error {
	f, err := s.st.RecordAuthFailure(ctx, db.RecordAuthFailureParams{Key: "register:" + ip, WindowSeconds: int32(registerWindow.Seconds())})
	if err != nil {
		return err
	}
	if f.Failures > maxRegistersPerIP {
		return apierr.New(429, "register_rate_limited", "注册太频繁，请稍后再试")
	}
	return nil
}

// StartPasswordReset 生成重置令牌；邮箱不存在时返回空串，调用方对外不区分两种情况。
func (s *Service) StartPasswordReset(ctx context.Context, email string) (string, db.User, error) {
	u, err := s.st.GetUserByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		return "", u, nil
	}
	token := secret.Token("pwr")
	err = s.st.CreatePasswordReset(ctx, db.CreatePasswordResetParams{TokenHash: secret.Hash(token), UserID: u.ID, ExpiresAt: s.now().Add(30 * time.Minute)})
	return token, u, err
}

// ResetPassword 凭邮件里的令牌设置新密码，并让其他登录全部失效。订阅链接不受影响。
func (s *Service) ResetPassword(ctx context.Context, token, plain string) (int64, error) {
	if err := password.Validate(plain); err != nil {
		return 0, apierr.BadRequest("weak_password", "%v", err)
	}
	var uid int64
	err := s.st.Tx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		id, err := q.UsePasswordReset(ctx, secret.Hash(token))
		if err != nil {
			return apierr.BadRequest("invalid_reset_token", "重置链接无效或已过期").WithHint("重新申请找回密码")
		}
		uid = id
		if err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: id, PasswordHash: password.Hash(plain), PasswordAlgo: password.Argon2id}); err != nil {
			return err
		}
		return q.DeleteSubjectSessions(ctx, db.DeleteSubjectSessionsParams{Kind: KindUser, SubjectID: id})
	})
	return uid, err
}

// ChangePassword 已登录用户修改密码，当前会话保留，其他会话失效。
func (s *Service) ChangePassword(ctx context.Context, u db.User, sessionToken, current, next string) error {
	if !password.Verify(u.PasswordAlgo, u.PasswordSalt, current, u.PasswordHash) {
		return apierr.BadRequest("wrong_password", "当前密码不对")
	}
	if err := password.Validate(next); err != nil {
		return apierr.BadRequest("weak_password", "%v", err)
	}
	if err := s.st.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{ID: u.ID, PasswordHash: password.Hash(next), PasswordAlgo: password.Argon2id}); err != nil {
		return err
	}
	return s.st.DeleteSubjectSessionsExcept(ctx, db.DeleteSubjectSessionsExceptParams{Kind: KindUser, SubjectID: u.ID, TokenHash: secret.Hash(sessionToken)})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
