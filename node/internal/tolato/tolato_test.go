package tolato

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const unit = `[Unit]
Description=Tolato Agent

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/tolato-agent --server wss://tolato.example.com/ws/agent --token reg-123 --data-dir /var/lib/tolato --node-exporter.enabled
Restart=always
`

func TestParseUnit(t *testing.T) {
	a, err := ParseUnit([]byte(unit))
	if err != nil || a.Server != "wss://tolato.example.com/ws/agent" || a.DataDir != "/var/lib/tolato" {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = ParseUnit([]byte("ExecStart=/x --server=ws://h:8080/ws/agent"))
	if err != nil || a.Server != "ws://h:8080/ws/agent" || a.DataDir != "" {
		t.Fatalf("equals form: %+v %v", a, err)
	}
	if _, err := ParseUnit([]byte("ExecStart=/x --token y")); err == nil {
		t.Fatal("missing --server must fail")
	}
}

func TestAttest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	unitPath := filepath.Join(dir, "tolato-agent.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	// 假的 tolato-agent：把收到的参数写下来，输出一份证明
	agent := filepath.Join(dir, "tolato-agent")
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho '{\"token\":\"tat_abc\",\"node_id\":\"n1\"}'\n"
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	token, err := Attest(context.Background(), agent, unitPath, "https://api.example.com")
	if err != nil || token != "tat_abc" {
		t.Fatalf("attest: %q %v", token, err)
	}
	got, _ := os.ReadFile(argsFile)
	if strings.TrimSpace(string(got)) != "--server wss://tolato.example.com/ws/agent --attest https://api.example.com --data-dir /var/lib/tolato" {
		t.Fatalf("args: %s", got)
	}

	fail := filepath.Join(dir, "fail")
	_ = os.WriteFile(fail, []byte("#!/bin/sh\necho 'error: server refused attestation' >&2\nexit 1\n"), 0o755)
	if _, err := Attest(context.Background(), fail, unitPath, "https://api.example.com"); err == nil || !strings.Contains(err.Error(), "server refused") {
		t.Fatalf("failure must carry stderr: %v", err)
	}
	if _, err := Attest(context.Background(), agent, filepath.Join(dir, "missing"), "https://api.example.com"); err == nil {
		t.Fatal("missing unit must fail")
	}
}
