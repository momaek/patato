package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// 可重试的错误自动重试，并且复用同一个幂等键，服务端据此只执行一次。
func TestRetryReusesIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		n := len(keys)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer pat_x" || r.Header.Get("X-Actor") != "Agent" {
			w.WriteHeader(401)
			return
		}
		if n == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":{"code":"internal","message":"服务端错误","retryable":true}}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "pat_x", "Agent", "test")
	c.Retries = 2
	var out struct{ OK bool }
	if _, err := c.Do(context.Background(), "POST", "/api/v1/x", nil, map[string]any{"a": 1}, &out); err != nil || !out.OK {
		t.Fatalf("%v %+v", err, out)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys: %v", keys)
	}
}

func TestErrorFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":{"code":"plan_not_found","message":"套餐 x 不存在","hint":"可用套餐：premium","retryable":false}}`))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "t", "", "").Do(context.Background(), "GET", "/api/v1/plans/x", nil, nil, nil)
	if !IsCode(err, "plan_not_found") || !IsStatus(err, 404) || err.Error() != "套餐 x 不存在\n提示：可用套餐：premium" {
		t.Fatalf("%v", err)
	}
	if Path("/api/v1/users/{id}:reset-token", "a@b.com") != "/api/v1/users/a@b.com:reset-token" {
		t.Fatal(Path("/api/v1/users/{id}:reset-token", "a@b.com"))
	}
}
