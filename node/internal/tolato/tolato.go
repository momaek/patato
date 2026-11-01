// Package tolato 向本机的 tolato-agent 要一份机器身份证明，patato-node 凭它接入面板，
// 安装命令里就不用带注册码。证明由 tolato 服务端签发，只对指定的面板有效、5 分钟内只能用一次。
package tolato

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	// DefaultAgent 和 DefaultUnit 是 tolato 安装脚本的默认位置。
	DefaultAgent = "/usr/local/bin/tolato-agent"
	DefaultUnit  = "/etc/systemd/system/tolato-agent.service"
)

// AgentArgs 是 tolato-agent 服务启动时用的 --server 和 --data-dir。
type AgentArgs struct {
	Server  string
	DataDir string
}

// ParseUnit 从 tolato-agent 的 systemd unit 里取出 ExecStart 的 --server 和 --data-dir。
func ParseUnit(unit []byte) (AgentArgs, error) {
	var a AgentArgs
	sc := bufio.NewScanner(bytes.NewReader(unit))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		cmd, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		fields := strings.Fields(cmd)
		for i := 0; i < len(fields); i++ {
			for _, name := range []string{"server", "data-dir"} {
				var v string
				switch {
				case fields[i] == "--"+name && i+1 < len(fields):
					v = fields[i+1]
				case strings.HasPrefix(fields[i], "--"+name+"="):
					v = strings.TrimPrefix(fields[i], "--"+name+"=")
				default:
					continue
				}
				if name == "server" {
					a.Server = strings.Trim(v, `"'`)
				} else {
					a.DataDir = strings.Trim(v, `"'`)
				}
			}
		}
	}
	if a.Server == "" {
		return a, errors.New("tolato-agent 的 systemd unit 里没有 --server")
	}
	return a, nil
}

// Attest 运行 tolato-agent --attest <audience>，返回一次性的身份证明。audience 是面板的 API 地址。
func Attest(ctx context.Context, agent, unitPath, audience string) (string, error) {
	unit, err := os.ReadFile(unitPath)
	if err != nil {
		return "", fmt.Errorf("读取 tolato-agent 服务配置 %s：%w（这台机器装了 tolato-agent 吗？）", unitPath, err)
	}
	args, err := ParseUnit(unit)
	if err != nil {
		return "", err
	}
	argv := []string{"--server", args.Server, "--attest", audience}
	if args.DataDir != "" {
		argv = append(argv, "--data-dir", args.DataDir)
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, agent, argv...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tolato-agent --attest 失败：%v %s（tolato-agent 需要支持 --attest）", err, strings.TrimSpace(stderr.String()))
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &res); err != nil || res.Token == "" {
		return "", fmt.Errorf("tolato-agent 的输出不是身份证明：%s", strings.TrimSpace(string(out)))
	}
	return res.Token, nil
}
