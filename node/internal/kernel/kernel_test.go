//go:build with_quic && with_utls

package kernel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"

	"github.com/momaek/patato/node/internal/meter"
	"github.com/momaek/patato/nodeapi"
)

const testUUID = "0f7c3a8e-1111-4222-8333-944455556666"

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// target 是被代理访问的本地 HTTP 服务，返回固定大小的响应。
func target(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("x", 64<<10)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return "http://" + l.Addr().String() + "/"
}

// client 起一个 sing-box 客户端：本地 mixed 代理端口，出站连到被测节点。
func client(t *testing.T, outbound map[string]any) *http.Client {
	t.Helper()
	port := freePort(t, "tcp")
	outbound["tag"] = "proxy"
	cfg := map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []map[string]any{{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []map[string]any{outbound},
		"route":     map[string]any{"final": "proxy"},
	}
	raw, _ := json.Marshal(cfg)
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
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
}

func newKernel(t *testing.T, m *meter.Meter) *Kernel {
	t.Helper()
	k, err := New(Options{DataDir: t.TempDir(), LogLevel: "error", Meter: m, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	return k
}

func fetch(c *http.Client, u string) (int, error) {
	resp, err := c.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return len(b), err
}

type protoCase struct {
	name     string
	node     func(port int) nodeapi.Node
	user     nodeapi.User
	outbound func(port int) map[string]any
	udp      bool
}

func cases() []protoCase {
	ssKey := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	ssUser := base64.StdEncoding.EncodeToString([]byte(testUUID[:16]))
	insecure := map[string]any{"enabled": true, "server_name": "node.test", "insecure": true}
	return []protoCase{
		{
			name: "vless",
			node: func(p int) nodeapi.Node { return nodeapi.Node{ID: 1, Code: "vless", Protocol: "vless", Port: p} },
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: testUUID},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": p, "uuid": testUUID}
			},
		},
		{
			name: "vmess-ws-tls",
			node: func(p int) nodeapi.Node {
				return nodeapi.Node{ID: 2, Code: "vmess", Protocol: "vmess", Port: p,
					TLS:       &nodeapi.TLS{Mode: "tls", ServerName: "node.test", Cert: &nodeapi.Cert{Mode: "self"}},
					Transport: &nodeapi.Transport{Type: "ws", Path: "/ray"}}
			},
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: testUUID},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "vmess", "server": "127.0.0.1", "server_port": p, "uuid": testUUID,
					"security": "auto", "tls": insecure, "transport": map[string]any{"type": "ws", "path": "/ray"}}
			},
		},
		{
			name: "trojan",
			node: func(p int) nodeapi.Node {
				return nodeapi.Node{ID: 3, Code: "trojan", Protocol: "trojan", Port: p, TLS: &nodeapi.TLS{Mode: "tls", ServerName: "node.test"}}
			},
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: testUUID},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "trojan", "server": "127.0.0.1", "server_port": p, "password": testUUID, "tls": insecure}
			},
		},
		{
			name: "shadowsocks-2022",
			node: func(p int) nodeapi.Node {
				return nodeapi.Node{ID: 4, Code: "ss", Protocol: "shadowsocks", Port: p, Cipher: "2022-blake3-aes-128-gcm", ServerKey: ssKey}
			},
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: ssUser},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "shadowsocks", "server": "127.0.0.1", "server_port": p,
					"method": "2022-blake3-aes-128-gcm", "password": ssKey + ":" + ssUser}
			},
		},
		{
			name: "hysteria2",
			udp:  true,
			node: func(p int) nodeapi.Node {
				return nodeapi.Node{ID: 5, Code: "hy2", Protocol: "hysteria2", Port: p, Obfs: "salamander", ObfsPass: "ob",
					TLS: &nodeapi.TLS{Mode: "tls", ServerName: "node.test", ALPN: []string{"h3"}}}
			},
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: testUUID},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "hysteria2", "server": "127.0.0.1", "server_port": p, "password": testUUID,
					"obfs": map[string]any{"type": "salamander", "password": "ob"},
					"tls":  map[string]any{"enabled": true, "server_name": "node.test", "insecure": true, "alpn": []string{"h3"}}}
			},
		},
		{
			name: "tuic",
			udp:  true,
			node: func(p int) nodeapi.Node {
				return nodeapi.Node{ID: 6, Code: "tuic", Protocol: "tuic", Port: p, Congestion: "bbr",
					TLS: &nodeapi.TLS{Mode: "tls", ServerName: "node.test", ALPN: []string{"h3"}}}
			},
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: testUUID},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "tuic", "server": "127.0.0.1", "server_port": p, "uuid": testUUID, "password": testUUID,
					"congestion_control": "bbr",
					"tls":                map[string]any{"enabled": true, "server_name": "node.test", "insecure": true, "alpn": []string{"h3"}}}
			},
		},
		{
			name: "anytls",
			node: func(p int) nodeapi.Node {
				return nodeapi.Node{ID: 8, Code: "anytls", Protocol: "anytls", Port: p, TLS: &nodeapi.TLS{Mode: "tls", ServerName: "node.test"}}
			},
			user: nodeapi.User{ID: 7, UUID: testUUID, Password: testUUID},
			outbound: func(p int) map[string]any {
				return map[string]any{"type": "anytls", "server": "127.0.0.1", "server_port": p, "password": testUUID, "tls": insecure}
			},
		},
	}
}

// TestProtocols 对每个协议：节点在内核里跑起来，客户端能通过它访问目标，流量按用户计入；
// 用户被移除后连接立即失败，重新加入后恢复。
func TestProtocols(t *testing.T) {
	u := target(t)
	for _, tc := range cases() {
		t.Run(tc.name, func(t *testing.T) {
			m := meter.New(time.Minute)
			k := newKernel(t, m)
			network := "tcp"
			if tc.udp {
				network = "udp"
			}
			port := freePort(t, network)
			spec := tc.node(port)
			users := []nodeapi.User{tc.user}
			m.SetUsers(spec.ID, users)
			if err := k.Apply(spec, users); err != nil {
				t.Fatalf("apply: %v", err)
			}
			if st := k.Status(spec.ID); !st.Running {
				t.Fatalf("not running: %+v", st)
			}
			c := client(t, tc.outbound(port))
			for _, targetURL := range []string{u, strings.Replace(u, "127.0.0.1", "localhost", 1)} {
				n, err := fetch(c, targetURL)
				if err != nil || n != 64<<10 {
					t.Fatalf("fetch %s through %s: n=%d err=%v", targetURL, tc.name, n, err)
				}
			}
			r := m.Collect()[spec.ID]
			if r == nil || r.Traffic[7][1] < 64<<10 || r.Traffic[7][0] == 0 {
				t.Fatalf("traffic not counted: %+v", r)
			}
			if len(r.Alive[7]) != 1 {
				t.Fatalf("alive: %+v", r.Alive)
			}

			// 移除用户：meter 拒绝新连接，不需要重建 inbound
			m.SetUsers(spec.ID, nil)
			if rebuilt, err := k.SetUsers(spec.ID, nil); err != nil || rebuilt {
				t.Fatalf("remove user: rebuilt=%v err=%v", rebuilt, err)
			}
			if n, err := fetch(c, u); err == nil && n == 64<<10 {
				t.Fatal("removed user can still connect")
			}

			// 加回用户
			m.SetUsers(spec.ID, users)
			if _, err := k.SetUsers(spec.ID, users); err != nil {
				t.Fatal(err)
			}
			if n, err := fetch(c, u); err != nil || n != 64<<10 {
				t.Fatalf("re-added user: n=%d err=%v", n, err)
			}
		})
	}
}

func TestPortConflictReported(t *testing.T) {
	l, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	k := newKernel(t, meter.New(time.Minute))
	spec := nodeapi.Node{ID: 9, Code: "busy", Protocol: "vless", Port: port}
	if err := k.Apply(spec, nil); err == nil {
		t.Fatal("expected port conflict")
	}
	st := k.Status(9)
	if st.Running || st.Error == "" {
		t.Fatalf("status should carry the error: %+v", st)
	}
}
