package notify

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"net/smtp"
	"strings"
	"time"
)

// Mailer 发送纯文本邮件，用于找回密码。
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

type SMTPMailer struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func (m *SMTPMailer) Send(_ context.Context, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", m.Host, m.Port)
	var auth smtp.Auth
	if m.Username != "" {
		auth = smtp.PlainAuth("", m.Username, m.Password, m.Host)
	}
	msg := strings.Join([]string{
		"From: " + m.From,
		"To: " + to,
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")
	return smtp.SendMail(addr, auth, m.From, []string{to}, []byte(msg))
}

// LogMailer 在没有配置 SMTP 时使用：只把邮件写进日志，便于本地开发。
type LogMailer struct{ Log *slog.Logger }

func (m *LogMailer) Send(_ context.Context, to, subject, body string) error {
	m.Log.Warn("smtp not configured, email not sent", "to", to, "subject", subject, "body", body)
	return nil
}
