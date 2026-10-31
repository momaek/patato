// Package control 是 patato-node 的控制面：从哪里拿节点配置和用户、把上报交给谁。
// 正常运行时连 Patato 面板；也可以读本地文件独立运行，便于测试和排障。
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/momaek/patato/nodeapi"
)

// Source 是控制面。Watch 阻塞到 ctx 结束，期间每当面板提示有变化就调用 changed。
type Source interface {
	Sync(ctx context.Context, req nodeapi.SyncRequest) (*nodeapi.SyncResponse, error)
	Report(ctx context.Context, req nodeapi.ReportRequest) (*nodeapi.ReportResponse, error)
	Watch(ctx context.Context, changed func())
}

// Error 是面板返回的错误。
type Error struct {
	Status    int
	Code      string
	Message   string
	Hint      string
	Retryable bool
}

func (e *Error) Error() string {
	s := fmt.Sprintf("panel %d %s: %s", e.Status, e.Code, e.Message)
	if e.Hint != "" {
		s += "（" + e.Hint + "）"
	}
	return s
}

// IsUnauthorized 说明机器 token 已失效，需要重新接入。
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

type Panel struct {
	base  string
	token string
	http  *http.Client
	log   *slog.Logger
}

func NewPanel(base, token string, log *slog.Logger) *Panel {
	return &Panel{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}, log: log}
}

func (p *Panel) Sync(ctx context.Context, req nodeapi.SyncRequest) (*nodeapi.SyncResponse, error) {
	var resp nodeapi.SyncResponse
	return &resp, p.post(ctx, nodeapi.PathSync, req, &resp)
}

func (p *Panel) Report(ctx context.Context, req nodeapi.ReportRequest) (*nodeapi.ReportResponse, error) {
	var resp nodeapi.ReportResponse
	return &resp, p.post(ctx, nodeapi.PathReport, req, &resp)
}

func (p *Panel) post(ctx context.Context, path string, in, out any) error {
	return do(ctx, p.http, p.base+path, p.token, in, out)
}

func do(ctx context.Context, c *http.Client, u, token string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e nodeapi.Error
		_ = json.Unmarshal(b, &e)
		return &Error{Status: resp.StatusCode, Code: e.Error.Code, Message: firstNonEmpty(e.Error.Message, strings.TrimSpace(string(b))),
			Hint: e.Error.Hint, Retryable: e.Error.Retryable || resp.StatusCode >= 500}
	}
	return json.Unmarshal(b, out)
}

// Watch 保持 WebSocket 连接，断开后退避重连。连不上时节点仍按 sync_interval 轮询，不影响正确性。
func (p *Panel) Watch(ctx context.Context, changed func()) {
	u, err := url.Parse(p.base + nodeapi.PathWS)
	if err != nil {
		p.log.Error("watch url", "err", err)
		return
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := p.watchOnce(ctx, u.String(), changed)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		p.log.Warn("push channel disconnected, falling back to polling", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (p *Panel) watchOnce(ctx context.Context, u string, changed func()) error {
	h := http.Header{"Authorization": {"Bearer " + p.token}}
	d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := d.DialContext(ctx, u, h)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return &Error{Status: resp.StatusCode, Code: "invalid_machine_token", Message: "机器 token 无效或已吊销"}
		}
		return err
	}
	defer conn.Close()
	p.log.Info("push channel connected")
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// 面板每 30 秒发一次 ping；超过 90 秒没有任何消息就认为连接已死
	conn.SetPingHandler(func(data string) error {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(10*time.Second))
	})
	for {
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		var msg nodeapi.Push
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.Type == "changed" {
			changed()
		}
	}
}

// Enroll 凭注册码向面板换取机器 token。
func Enroll(ctx context.Context, base string, req nodeapi.EnrollRequest) (*nodeapi.EnrollResponse, error) {
	var resp nodeapi.EnrollResponse
	err := do(ctx, &http.Client{Timeout: 30 * time.Second}, strings.TrimRight(base, "/")+nodeapi.PathEnroll, "", req, &resp)
	return &resp, err
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
