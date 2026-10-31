package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singjson "github.com/sagernet/sing/common/json"
	"gopkg.in/yaml.v3"

	"github.com/momaek/patato/server/internal/config"
)

// buildNode 编译 patato-node（带上线时用的构建标签），同一次测试只编译一次。
var buildNode = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "patato-node-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "patato-node")
	cmd := exec.Command("go", "build", "-tags", "with_quic,with_utls,with_acme", "-o", bin, "./node/cmd/patato-node")
	cmd.Dir = "../../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build patato-node: %v\n%s", err, out)
	}
	return bin, nil
})

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// proxyClient 起一个 sing-box 客户端，出站连到节点，返回经它访问网络的 HTTP 客户端。
func proxyClient(t *testing.T, outbound map[string]any) *http.Client {
	t.Helper()
	port := freeTCPPort(t)
	outbound["tag"] = "proxy"
	raw, _ := json.Marshal(map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []map[string]any{{"type": "mixed", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []map[string]any{outbound},
		"route":     map[string]any{"final": "proxy"},
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
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
}

func eventually(t *testing.T, what string, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func get(c *http.Client, u string) (int, error) {
	resp, err := c.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return len(b), err
}

// TestPatatoNode 跑通 patato-node 的完整链路：生成安装命令 → 凭注册码接入 → 上节点后通过推送启动 →
// 首次上报后转为在线 → 客户端连通、流量入账 → 同一台机器再加一个节点 → 封禁用户立即断开 → 吊销机器。
func TestPatatoNode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the patato-node binary")
	}
	bin, err := buildNode()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *config.Config) {
		c.Node.PushInterval, c.Node.PullInterval = 1, 30 // 上报每秒一次；同步主要靠推送
	})
	srv := httptest.NewServer(h.a.HTTP)
	defer srv.Close()
	h.a.Cfg.PublicURL = srv.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.a.Native.Hub().Run(ctx)

	// 被代理访问的目标
	body := strings.Repeat("p", 256<<10)
	tl, _ := net.Listen("tcp", "127.0.0.1:0")
	target := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })}
	go target.Serve(tl)
	defer target.Close()
	targetURL := "http://" + tl.Addr().String() + "/"

	vlessPort, ssPort := freeTCPPort(t), freeTCPPort(t)
	expect(t, h.api("POST", "/api/v1/templates", map[string]any{
		"code": "jp-vless", "name": "日本 VLESS", "region": "东京", "protocol": "vless", "port_min": vlessPort, "port_max": vlessPort,
		"settings": map[string]any{"tls": 0, "network": "tcp"},
	}), 200)
	expect(t, h.api("POST", "/api/v1/templates", map[string]any{
		"code": "jp-ss", "name": "日本 SS", "region": "东京", "protocol": "shadowsocks", "port_min": ssPort, "port_max": ssPort,
		"settings": map[string]any{"cipher": "2022-blake3-aes-128-gcm"},
	}), 200)
	expect(t, h.api("POST", "/api/v1/plans", map[string]any{"code": "premium", "name": "高级", "transfer_gb": 100}), 200)
	r := h.api("POST", "/api/v1/users", map[string]any{"email": "n@example.com", "plan": "premium", "password": "password-123", "days": 30})
	expect(t, r, 200)
	userID := r.str("result.user_id")
	user, err := h.a.Store.GetUserByEmail(ctx, "n@example.com")
	if err != nil {
		t.Fatal(err)
	}

	// 生成安装命令：一小时内重复调用返回同一个注册码
	r = h.api("GET", "/api/v1/machines/tokyo-01/install-cmd", nil)
	expect(t, r, 200)
	code := regexp.MustCompile(`--code '([^']+)'`).FindStringSubmatch(r.str("command"))
	if r.str("backend") != "patato-node" || code == nil {
		t.Fatalf("install cmd: %s", r.raw)
	}
	if again := h.api("GET", "/api/v1/machines/tokyo-01/install-cmd", nil); !strings.Contains(again.str("command"), code[1]) {
		t.Fatal("install-cmd should reuse the pending enroll code")
	}
	r = h.api("GET", "/api/v1/machines/tokyo-01", nil)
	if r.str("status") != "pending_enroll" {
		t.Fatalf("machine before enroll: %s", r.raw)
	}

	// 在机器上执行：凭注册码接入
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	out, err := exec.Command(bin, "enroll", "--panel", srv.URL, "--code", code[1], "-c", cfgPath, "--state-dir", filepath.Join(dir, "state")).CombinedOutput()
	if err != nil {
		t.Fatalf("enroll: %v\n%s", err, out)
	}
	// 注册码只能用一次
	if out, err := exec.Command(bin, "enroll", "--panel", srv.URL, "--code", code[1], "-c", filepath.Join(dir, "other.yaml")).CombinedOutput(); err == nil ||
		!strings.Contains(string(out), "invalid_enroll_code") {
		t.Fatalf("reused enroll code: %v %s", err, out)
	}
	// 测试里目标在本机，关掉内网拦截
	var nc map[string]any
	raw, _ := os.ReadFile(cfgPath)
	_ = yaml.Unmarshal(raw, &nc)
	nc["block_private"], nc["log_level"], nc["kernel_log_level"] = false, "debug", "error"
	raw, _ = yaml.Marshal(nc)
	_ = os.WriteFile(cfgPath, raw, 0o600)
	r = h.api("GET", "/api/v1/machines/tokyo-01", nil)
	if r.str("status") != "enrolled" {
		t.Fatalf("machine after enroll: %s", r.raw)
	}

	// 机器已接入，上节点时默认由 patato-node 承载
	r = h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-vless", "host": "tokyo-01", "address": "127.0.0.1", "plans": []string{"premium"}, "code": "tokyo-vless",
	})
	expect(t, r, 200)
	if r.str("result.backend") != "native" {
		t.Fatalf("provision: %s", r.raw)
	}

	logPath := filepath.Join(dir, "node.log")
	logFile, _ := os.Create(logPath)
	defer logFile.Close()
	cmd := exec.Command(bin, "run", "-c", cfgPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("patato-node log:\n%s", b)
		}
	}()

	nodeState := func(code string) string {
		return h.api("GET", "/api/v1/nodes/"+code, nil).str("state")
	}
	eventually(t, "tokyo-vless online", 20*time.Second, func() bool { return nodeState("tokyo-vless") == "online" })
	r = h.api("GET", "/api/v1/nodes/tokyo-vless", nil)
	if r.str("runtime.running") != "true" || r.str("backend") != "native" {
		t.Fatalf("node runtime: %s", r.raw)
	}
	r = h.api("GET", "/api/v1/machines/tokyo-01", nil)
	if !strings.HasPrefix(r.str("kernel"), "sing-box ") || r.str("connected") != "true" {
		t.Fatalf("machine view: %s", r.raw)
	}

	// 客户端经节点访问，流量按序号上报并入账
	vless := proxyClient(t, map[string]any{"type": "vless", "server": "127.0.0.1", "server_port": vlessPort, "uuid": user.Uuid})
	if n, err := get(vless, targetURL); err != nil || n != len(body) {
		t.Fatalf("fetch through vless: n=%d err=%v", n, err)
	}
	eventually(t, "traffic billed", 15*time.Second, func() bool {
		_, _ = h.a.Billing.ProcessReports(ctx)
		u, _ := h.a.Store.GetUser(ctx, user.ID)
		return u.UsedDown >= int64(len(body))
	})

	// 同一台机器再加一个节点：不登录机器，推送后几秒内启动
	expect(t, h.api("POST", "/api/v1/nodes:provision", map[string]any{
		"template": "jp-ss", "host": "tokyo-01", "address": "127.0.0.1", "plans": []string{"premium"}, "code": "tokyo-ss",
	}), 200)
	eventually(t, "tokyo-ss online", 15*time.Second, func() bool { return nodeState("tokyo-ss") == "online" })
	ssNode, _ := h.a.Store.GetNodeByCode(ctx, "tokyo-ss")
	var ssSettings struct {
		ServerKey string `json:"server_key"`
	}
	_ = json.Unmarshal(ssNode.Settings, &ssSettings)
	ss := proxyClient(t, map[string]any{"type": "shadowsocks", "server": "127.0.0.1", "server_port": ssPort,
		"method": "2022-blake3-aes-128-gcm", "password": ssSettings.ServerKey + ":" + base64.StdEncoding.EncodeToString([]byte(user.Uuid[:16]))})
	if n, err := get(ss, targetURL); err != nil || n != len(body) {
		t.Fatalf("fetch through shadowsocks: n=%d err=%v", n, err)
	}

	// 封禁用户：推送后节点立即拒绝
	expect(t, h.api("PATCH", "/api/v1/users/"+userID, map[string]any{"banned": true}), 200)
	eventually(t, "banned user rejected", 10*time.Second, func() bool {
		n, err := get(vless, targetURL)
		return err != nil || n != len(body)
	})
	// 解封后恢复（meter 放行，内核本就认识这个用户，不需要重建）
	expect(t, h.api("PATCH", "/api/v1/users/"+userID, map[string]any{"banned": false}), 200)
	eventually(t, "unbanned user accepted", 10*time.Second, func() bool {
		n, err := get(vless, targetURL)
		return err == nil && n == len(body)
	})

	// 吊销机器：之后的同步和上报都被拒绝
	r = h.api("POST", "/api/v1/machines/tokyo-01:revoke", map[string]any{"dry_run": true})
	expect(t, r, 200)
	if r.str("impact.nodes") != "2" {
		t.Fatalf("revoke plan: %s", r.raw)
	}
	expect(t, h.api("POST", "/api/v1/machines/tokyo-01:revoke", nil), 200)
	var token string
	_ = yaml.Unmarshal(raw, &nc)
	token, _ = nc["token"].(string)
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/node/sync", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token: status %d", resp.StatusCode)
	}
}
