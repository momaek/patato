package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/secret"
)

// fakeTolato 实现 tolato 核实机器身份证明的接口：证明只对签发时的 audience 有效，只能用一次。
type fakeTolato struct {
	mu     sync.Mutex
	tokens map[string][2]string // token → {机器名, audience}
}

func (f *fakeTolato) issue(machine, audience string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := secret.Token("tat")
	f.tokens[t] = [2]string{machine, strings.ToLower(strings.TrimRight(audience, "/"))}
	return t
}

func (f *fakeTolato) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/attestations/verify" || r.Header.Get("Authorization") != "Bearer tolato-key" {
		http.NotFound(w, r)
		return
	}
	var req struct{ Token, Audience string }
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	v, ok := f.tokens[req.Token]
	delete(f.tokens, req.Token)
	f.mu.Unlock()
	if !ok || v[1] != strings.ToLower(strings.TrimRight(req.Audience, "/")) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found","message":"attestation not found, expired or already used"}`))
		return
	}
	// 与 tolato 的 VerifyAttestation 响应一致：{"node": NodeDetail}
	_ = json.NewEncoder(w).Encode(map[string]any{"node": map[string]any{"id": "node-uuid-1", "name": v[0], "alias": nil, "ip": "203.0.113.9"}})
}

// TestEnrollViaTolato 走一遍经 tolato-agent 引导身份的接入：安装命令不含密钥，
// patato-node 用本机 tolato-agent 的一次性证明换取机器 token；没有安装窗口或已吊销时拒绝。
func TestEnrollViaTolato(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the patato-node binary")
	}
	bin, err := buildNode()
	if err != nil {
		t.Fatal(err)
	}
	tol := &fakeTolato{tokens: map[string][2]string{}}
	tsrv := httptest.NewServer(tol)
	defer tsrv.Close()
	h := newHarness(t, func(c *config.Config) {
		c.Tolato.URL, c.Tolato.APIKey, c.Tolato.AttestEnroll = tsrv.URL, "tolato-key", true
	})
	srv := httptest.NewServer(h.a.HTTP)
	defer srv.Close()
	h.a.Cfg.PublicURL = srv.URL

	enroll := func(att string) resp {
		return h.do("POST", "/api/v1/node/enroll", map[string]any{"attestation": att, "hostname": "tokyo-01", "version": "test"}, nil)
	}

	// 还没有生成安装命令：没有安装窗口
	r := enroll(tol.issue("tokyo-01", srv.URL))
	expect(t, r, 403)
	if r.str("error.code") != "enroll_not_open" {
		t.Fatalf("no window: %s", r.raw)
	}

	// 安装命令里不再有注册码
	r = h.api("GET", "/api/v1/machines/tokyo-01/install-cmd", nil)
	expect(t, r, 200)
	if cmd := r.str("command"); !strings.Contains(cmd, "--tolato") || strings.Contains(cmd, "--code") {
		t.Fatalf("install cmd: %s", cmd)
	}

	// 伪造的、给别的面板的证明都不认
	expect(t, enroll("tat_forged"), 401)
	expect(t, enroll(tol.issue("tokyo-01", "https://other-panel.example.com")), 401)

	// 在机器上：patato-node 从 tolato-agent 的 systemd unit 读出参数，运行 tolato-agent --attest
	dir := t.TempDir()
	unit := filepath.Join(dir, "tolato-agent.service")
	_ = os.WriteFile(unit, []byte("[Service]\nExecStart=/usr/local/bin/tolato-agent --server wss://tolato.test/ws/agent --token x --data-dir /var/lib/tolato\n"), 0o644)
	token := tol.issue("tokyo-01", srv.URL)
	agent := filepath.Join(dir, "tolato-agent")
	script := "#!/bin/sh\n" +
		`[ "$*" = "--server wss://tolato.test/ws/agent --attest ` + srv.URL + ` --data-dir /var/lib/tolato" ] || { echo "unexpected args: $*" >&2; exit 1; }` + "\n" +
		`echo '{"token":"` + token + `","node_id":"node-uuid-1"}'` + "\n"
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	out, err := exec.Command(bin, "enroll", "--panel", srv.URL, "--tolato", "--tolato-agent", agent, "--tolato-unit", unit, "-c", cfgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("enroll --tolato: %v\n%s", err, out)
	}
	var cfg struct {
		Token   string `yaml:"token"`
		Machine string `yaml:"machine"`
	}
	raw, _ := os.ReadFile(cfgPath)
	if err := yaml.Unmarshal(raw, &cfg); err != nil || cfg.Machine != "tokyo-01" || !strings.HasPrefix(cfg.Token, "mt_") {
		t.Fatalf("node config: %s", raw)
	}
	r = h.api("GET", "/api/v1/machines/tokyo-01", nil)
	if r.str("status") != "enrolled" {
		t.Fatalf("machine after enroll: %s", r.raw)
	}
	// 证明只能用一次
	if out, err := exec.Command(bin, "enroll", "--panel", srv.URL, "--tolato", "--tolato-agent", agent, "--tolato-unit", unit, "-c", filepath.Join(dir, "b.yaml")).CombinedOutput(); err == nil ||
		!strings.Contains(string(out), "invalid_attestation") {
		t.Fatalf("replayed attestation: %v %s", err, out)
	}
	// 已接入的机器可以凭新证明换发 token
	expect(t, enroll(tol.issue("tokyo-01", srv.URL)), 200)

	// 吊销后不会被自动接回，要重新生成安装命令
	r = h.api("POST", "/api/v1/machines/tokyo-01:revoke", nil)
	expect(t, r, 200)
	expect(t, enroll(tol.issue("tokyo-01", srv.URL)), 403)
	expect(t, h.api("GET", "/api/v1/machines/tokyo-01/install-cmd", nil), 200)
	expect(t, enroll(tol.issue("tokyo-01", srv.URL)), 200)
}
