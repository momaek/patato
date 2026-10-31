package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/approval"
	"github.com/momaek/patato/server/internal/config"
)

var buildCLI = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "patato-cli-bin")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "patato")
	cmd := exec.Command("go", "build", "-o", bin, "./cli/cmd/patato")
	cmd.Dir = "../../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build patato: %v\n%s", err, out)
	}
	return bin, nil
})

type cliEnv struct {
	t   *testing.T
	bin string
	env []string
}

// run 执行一条 patato 命令，返回标准输出和退出码。
func (c *cliEnv) run(args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = c.env
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatalf("patato %v: %v", args, err)
	}
	if code != 0 && code != 3 {
		c.t.Logf("patato %v exited %d\nstdout: %s\nstderr: %s", args, code, out, stderr.String())
	}
	return string(out), code
}

func (c *cliEnv) json(v any, args ...string) {
	c.t.Helper()
	out, code := c.run(append(args, "--json")...)
	if code != 0 {
		c.t.Fatalf("patato %v exited %d: %s", args, code, out)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		c.t.Fatalf("patato %v: bad JSON: %v\n%s", args, err, out)
	}
}

// TestCLI 用真实的 patato 二进制走一遍 Agent 的工作方式：设备授权登录、写操作、预演后执行、
// 敏感变更等审批（批准和拒绝）、价格按元输入、审计里带调用方自报身份、退出时吊销 token。
func TestCLI(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the patato binary")
	}
	bin, err := buildCLI()
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(c *config.Config) { c.Risk.UserThreshold = 2 })
	srv := httptest.NewServer(h.a.HTTP)
	defer srv.Close()
	h.a.Cfg.AdminURL = "http://admin.patato.test"
	ctx := context.Background()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "PATATO_CONFIG=" + cfgPath}
	cli := &cliEnv{t: t, bin: bin, env: append(base, "PATATO_URL="+srv.URL, "PATATO_TOKEN="+h.token, "PATATO_ACTOR=Claude Code 会话")}

	approve := func(id string) {
		t.Helper()
		if _, err := h.a.Approvals.Approve(ctx, id, h.op, approval.ViaCLI); err != nil {
			t.Fatal(err)
		}
	}

	// 常规操作直接执行
	if out, code := cli.run("template", "create", "--code", "jp", "--name", "日本", "--region", "东京", "--protocol", "vless",
		"--port-min", "24000", "--port-max", "24010", "--settings", `{"tls":0,"network":"tcp"}`); code != 0 || !strings.Contains(out, "已执行") {
		t.Fatalf("template create: %d %s", code, out)
	}

	// 带价格的套餐固定为敏感：返回审批；价格按元输入，服务端收到分
	var pending struct {
		Status     string `json:"status"`
		ApprovalID string `json:"approval_id"`
	}
	cli.json(&pending, "plan", "create", "--code", "premium", "--name", "高级", "--transfer-gb", "100", "--price", "monthly=19.9",
		"--instruction", "建一个高级套餐，月付 19.9")
	if pending.Status != "pending_approval" || pending.ApprovalID == "" {
		t.Fatalf("plan create should need approval: %+v", pending)
	}
	approve(pending.ApprovalID)
	if out, code := cli.run("approval", "show", pending.ApprovalID, "--wait"); code != 0 || !strings.Contains(out, "已批准") {
		t.Fatalf("approval show --wait: %d %s", code, out)
	}
	if out, _ := cli.run("plan", "show", "premium"); !strings.Contains(out, "19.90 / 月") {
		t.Fatalf("plan price: %s", out)
	}

	for i := 1; i <= 3; i++ {
		if _, code := cli.run("user", "create", "--email", fmt.Sprintf("c%d@example.com", i), "--plan", "premium", "--days", "30"); code != 0 {
			t.Fatal("user create failed")
		}
	}

	// 预演 → 执行；影响 3 名用户超过阈值 2，升为敏感，--wait 等到批准
	var plan struct {
		ChangeID string `json:"change_id"`
		Risk     string `json:"risk"`
		Status   string `json:"status"`
	}
	cli.json(&plan, "node", "provision", "--template", "jp", "--plan", "premium", "--address", "203.0.113.5", "--code", "tokyo-01", "--dry-run")
	if plan.Status != "planned" || plan.Risk != "sensitive" || plan.ChangeID == "" {
		t.Fatalf("dry run: %+v", plan)
	}
	if nodes, _ := h.a.Store.ListNodes(ctx); len(nodes) != 0 {
		t.Fatal("dry run must not create the node")
	}
	done := make(chan struct {
		out  string
		code int
	}, 1)
	go func() {
		out, code := cli.run("change", "apply", plan.ChangeID, "--wait")
		done <- struct {
			out  string
			code int
		}{out, code}
	}()
	var aprID string
	eventually(t, "approval created", 10*time.Second, func() bool {
		var l struct {
			Items []struct {
				ID string `json:"approval_id"`
			} `json:"items"`
		}
		cli.json(&l, "approval", "list")
		if len(l.Items) == 1 {
			aprID = l.Items[0].ID
		}
		return aprID != ""
	})
	approve(aprID)
	select {
	case r := <-done:
		if r.code != 0 || !strings.Contains(r.out, "已批准") || !strings.Contains(r.out, "tokyo-01") {
			t.Fatalf("change apply --wait: %d %s", r.code, r.out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("change apply --wait did not return")
	}
	var n struct {
		State   string `json:"state"`
		Backend string `json:"backend"`
	}
	cli.json(&n, "node", "show", "tokyo-01")
	if n.State != "pending" || n.Backend != "uniproxy" {
		t.Fatalf("node: %+v", n)
	}
	if out, code := cli.run("node", "install-cmd", "tokyo-01", "--command-only"); code != 0 || !strings.HasPrefix(out, "set -e") {
		t.Fatalf("install-cmd: %d %s", code, out)
	}

	// 被拒绝的变更：--wait 返回退出码 3
	cli.json(&pending, "node", "delete", "tokyo-01")
	go func() {
		time.Sleep(time.Second)
		_, _ = h.a.Approvals.Reject(ctx, pending.ApprovalID, h.op, approval.ViaWeb, "先不删")
	}()
	if out, code := cli.run("approval", "show", pending.ApprovalID, "--wait"); code != 3 || !strings.Contains(out, "已拒绝") {
		t.Fatalf("rejected approval: %d %s", code, out)
	}

	// 审计里有调用方自报的身份和原始指令对应的变更
	var audit struct {
		Items []struct {
			ActorClaim string `json:"actor_claim"`
			Entry      string `json:"entry"`
		} `json:"items"`
	}
	cli.json(&audit, "audit", "log", "--limit", "5")
	found := false
	for _, it := range audit.Items {
		found = found || (it.Entry == "cli" && it.ActorClaim == "Claude Code 会话")
	}
	if !found {
		t.Fatalf("audit should record the CLI caller's claim: %+v", audit.Items)
	}

	// 设备授权登录：CLI 打印短码，运营者批准后写入配置；退出时吊销 token
	login := exec.Command(bin, "auth", "login", "--url", srv.URL, "--no-browser", "--scope", "read")
	login.Env = base
	stderr, _ := login.StderrPipe()
	if err := login.Start(); err != nil {
		t.Fatal(err)
	}
	codeRe := regexp.MustCompile(`code=([A-Z0-9-]+)`)
	sc := bufio.NewScanner(stderr)
	userCode := ""
	for sc.Scan() {
		if m := codeRe.FindStringSubmatch(sc.Text()); m != nil {
			userCode = m[1]
			break
		}
	}
	go func() {
		for sc.Scan() {
		}
	}()
	if userCode == "" {
		t.Fatal("login did not print a code")
	}
	if err := h.a.Auth.DecideCLIGrant(ctx, userCode, h.op, true); err != nil {
		t.Fatal(err)
	}
	if err := login.Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	loggedIn := &cliEnv{t: t, bin: bin, env: base}
	if out, code := loggedIn.run("auth", "status"); code != 0 || !strings.Contains(out, "只读") {
		t.Fatalf("auth status: %d %s", code, out)
	}
	// 只读 token 只能查询，写接口（包括预演）都被拒绝
	if _, code := loggedIn.run("plan", "list"); code != 0 {
		t.Fatal("read-only token can query")
	}
	if _, code := loggedIn.run("user", "reset-traffic", "c1@example.com", "--dry-run"); code == 0 {
		t.Fatal("read-only token must not call write endpoints")
	}
	if _, code := loggedIn.run("auth", "logout"); code != 0 {
		t.Fatal("logout failed")
	}
	tokens, _ := h.a.Store.ListAPITokens(ctx, h.op.ID)
	for _, tk := range tokens {
		if tk.Scope == "read" {
			t.Fatal("logout must revoke the token on the server")
		}
	}
}
