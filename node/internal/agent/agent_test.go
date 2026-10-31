//go:build with_quic && with_utls

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"

	"github.com/momaek/patato/node/internal/control"
	"github.com/momaek/patato/node/internal/kernel"
	"github.com/momaek/patato/node/internal/meter"
	"github.com/momaek/patato/node/internal/spool"
	"github.com/momaek/patato/nodeapi"
)

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func vlessClient(t *testing.T, port int, uuid string) *http.Client {
	local := freePort(t)
	raw, _ := json.Marshal(map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []map[string]any{{"type": "mixed", "listen": "127.0.0.1", "listen_port": local}},
		"outbounds": []map[string]any{{"type": "vless", "tag": "proxy", "server": "127.0.0.1", "server_port": port, "uuid": uuid}},
	})
	ctx := include.Context(context.Background())
	opts, err := singjson.UnmarshalExtendedContext[option.Options](ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", local))
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestStandalone 用本地文件当控制面：节点启动、流量写进上报文件；在文件里加用户后自动生效。
func TestStandalone(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tl, _ := net.Listen("tcp", "127.0.0.1:0")
	body := strings.Repeat("s", 32<<10)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })}
	go srv.Serve(tl)
	defer srv.Close()
	target := "http://" + tl.Addr().String() + "/"

	port := freePort(t)
	const u1, u2 = "0f7c3a8e-1111-4222-8333-944455550001", "0f7c3a8e-1111-4222-8333-944455550002"
	statePath := filepath.Join(dir, "state.json")
	write := func(users ...nodeapi.User) {
		st := control.LocalState{
			Nodes:    []control.LocalNode{{Config: nodeapi.Node{ID: 1, Code: "local", Protocol: "vless", Port: port}, Users: users}},
			Settings: nodeapi.Settings{ReportInterval: 1, SyncInterval: 60, DeviceTTL: 60},
		}
		b, _ := json.Marshal(st)
		if err := os.WriteFile(statePath, b, 0o600); err != nil {
			t.Fatal(err)
		}
		// 保证修改时间变化，Watch 才能察觉
		future := time.Now().Add(time.Duration(len(users)) * time.Second)
		_ = os.Chtimes(statePath, future, future)
	}
	write(nodeapi.User{ID: 1, UUID: u1})

	m := meter.New(time.Minute)
	k, err := kernel.New(kernel.Options{DataDir: filepath.Join(dir, "kernel"), LogLevel: "error", Meter: m, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	sp, err := spool.Open(filepath.Join(dir, "spool"), log)
	if err != nil {
		t.Fatal(err)
	}
	reports := filepath.Join(dir, "reports.jsonl")
	a := New(control.NewFile(statePath, reports), k, m, sp, "test", log)
	a.gap = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	fetch := func(uuid string) bool {
		resp, err := vlessClient(t, port, uuid).Get(target)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return len(b) == len(body)
	}
	eventually(t, "user 1 connects", func() bool { return fetch(u1) })
	if fetch(u2) {
		t.Fatal("user 2 is not configured yet")
	}
	eventually(t, "traffic in report file", func() bool {
		f, err := os.Open(reports)
		if err != nil {
			return false
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var r nodeapi.ReportRequest
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				for _, n := range r.Nodes {
					if n.Traffic[1][1] >= int64(len(body)) && n.Status != nil && n.Status.Running {
						return true
					}
				}
			}
		}
		return false
	})

	write(nodeapi.User{ID: 1, UUID: u1}, nodeapi.User{ID: 2, UUID: u2})
	eventually(t, "user 2 connects after file change", func() bool { return fetch(u2) })
}
