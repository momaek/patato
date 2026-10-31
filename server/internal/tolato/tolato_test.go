package tolato

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCommandsSince 按 tolato 的分页格式（items、数字 id）解析执行记录，并丢掉 since 之前的。
func TestCommandsSince(t *testing.T) {
	since := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/nodes":
			w.Write([]byte(`[{"id":"n1","name":"panel","ip":"1.2.3.4","status":"online"}]`))
		case "/api/v1/nodes/n1/commands":
			if got := r.URL.Query().Get("since"); got != "2026-10-31T08:00:00Z" {
				t.Errorf("since = %q", got)
			}
			w.Write([]byte(`{"items":[
				{"id":41,"command":"uptime","source":"cli","actor":"ops","created_at":"2026-10-31T08:00:00Z"},
				{"id":42,"command":"rm -rf /tmp/x","source":"terminal","actor":"ops","stdout":"","created_at":"2026-10-31T08:05:00Z"}
			],"total":2,"page":1,"page_size":100,"total_pages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmds, err := New(srv.URL, "k").CommandsSince(context.Background(), "panel", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].ID != 42 || cmds[0].Source != "terminal" || cmds[0].Actor != "ops" {
		t.Fatalf("cmds = %+v", cmds)
	}
}

// TestCommandsSinceUnsupported 在 tolato 还没有接口时返回 ErrUnsupported。
func TestCommandsSinceUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			w.Write([]byte(`[{"id":"n1","name":"panel","ip":"1.2.3.4","status":"online"}]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "k").CommandsSince(context.Background(), "panel", time.Now())
	if err != ErrUnsupported {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
