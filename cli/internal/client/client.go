// Package client 是 patato CLI 访问 Patato API 的薄封装：Bearer token、X-Actor、写请求的幂等键、
// 可重试错误的自动重试，以及服务端统一错误格式的解析。
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Error 是服务端返回的错误：{"error": {code, message, hint, retryable}}。
type Error struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string {
	s := e.Message
	if e.Hint != "" {
		s += "\n提示：" + e.Hint
	}
	return s
}

// IsCode 判断错误是否为某个服务端错误码。
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

type Client struct {
	Base  string
	Token string
	// Actor 写进 X-Actor：调用方自报的身份，只进审计，不参与权限判断。
	Actor   string
	UA      string
	HTTP    *http.Client
	Retries int
}

func New(base, token, actor, ua string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, Actor: actor, UA: ua,
		HTTP: &http.Client{Timeout: 60 * time.Second}, Retries: 2}
}

// Response 是一次请求的结果：状态码和原始响应体。
type Response struct {
	Status int
	Body   json.RawMessage
}

// Path 把契约里的路径模板 /api/v1/nodes/{id} 填上参数，参数会被转义。
func Path(tpl string, args ...string) string {
	for _, a := range args {
		i := strings.Index(tpl, "{")
		j := strings.Index(tpl, "}")
		if i < 0 || j < i {
			break
		}
		tpl = tpl[:i] + url.PathEscape(a) + tpl[j+1:]
	}
	return tpl
}

// Do 发出请求并把响应体解到 out（可以为 nil）。写请求自动带幂等键，重试时复用同一个键，
// 服务端据此保证同一个请求只执行一次。
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) (*Response, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	key := ""
	if method != http.MethodGet {
		key = newKey()
	}
	u := c.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		resp, err := c.once(ctx, method, u, payload, key)
		if err == nil {
			if out != nil && len(resp.Body) > 0 {
				if err := json.Unmarshal(resp.Body, out); err != nil {
					return resp, fmt.Errorf("解析响应失败：%w", err)
				}
			}
			return resp, nil
		}
		lastErr = err
		var apiErr *Error
		if errors.As(err, &apiErr) && !apiErr.Retryable {
			return resp, err
		}
		if ctx.Err() != nil {
			return resp, err
		}
	}
	return nil, lastErr
}

func (c *Client) once(ctx context.Context, method, u string, payload []byte, key string) (*Response, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	c.headers(req)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 %s：%w", c.Base, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	resp := &Response{Status: res.StatusCode, Body: b}
	if res.StatusCode >= 400 {
		return resp, parseError(res.StatusCode, b)
	}
	return resp, nil
}

func (c *Client) headers(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Actor != "" {
		req.Header.Set("X-Actor", c.Actor)
	}
	if c.UA != "" {
		req.Header.Set("User-Agent", c.UA)
	}
}

func parseError(status int, b []byte) error {
	var env struct {
		Error *Error `json:"error"`
	}
	if json.Unmarshal(b, &env) == nil && env.Error != nil && env.Error.Message != "" {
		env.Error.Status = status
		return env.Error
	}
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &Error{Status: status, Code: fmt.Sprintf("http_%d", status), Message: msg, Retryable: status >= 500}
}

// Event 是事件流里的一条。
type Event struct {
	ID   string
	Kind string
	Data json.RawMessage
}

// Stream 订阅 Server-Sent Events，每收到一条调用 fn，直到 ctx 结束或连接断开。
func (c *Client) Stream(ctx context.Context, path string, query url.Values, fn func(Event)) error {
	u := c.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	c.headers(req)
	req.Header.Set("Accept", "text/event-stream")
	res, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("连不上 %s：%w", c.Base, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return parseError(res.StatusCode, b)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var ev Event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(ev.Data) > 0 {
				fn(ev)
			}
			ev = Event{}
		case strings.HasPrefix(line, ":"):
			// 保活注释
		case strings.HasPrefix(line, "id:"):
			ev.ID = strings.TrimSpace(line[3:])
		case strings.HasPrefix(line, "event:"):
			ev.Kind = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			ev.Data = append(ev.Data, []byte(strings.TrimSpace(line[5:]))...)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("事件流被服务端关闭")
}

func newKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "idem_" + hex.EncodeToString(b)
}
