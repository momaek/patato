// patato-node 是 Patato 的节点端：一台机器一个进程，承载机器上的全部节点，
// 内嵌 sing-box 作为内核，通过 Patato 原生节点协议同步配置和用户、上报流量。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/momaek/patato/node/internal/agent"
	"github.com/momaek/patato/node/internal/config"
	"github.com/momaek/patato/node/internal/control"
	"github.com/momaek/patato/node/internal/kernel"
	"github.com/momaek/patato/node/internal/meter"
	"github.com/momaek/patato/node/internal/relay"
	"github.com/momaek/patato/node/internal/spool"
	"github.com/momaek/patato/node/internal/tolato"
	"github.com/momaek/patato/nodeapi"
)

// version 在发布构建时由 -ldflags "-X main.version=..." 写入。
var version = "dev"

const usage = `patato-node：Patato 的节点端

用法：
  patato-node run [-c 配置文件]                       运行
  patato-node enroll --panel <地址> --code <注册码>    凭注册码接入面板，写入配置文件
  patato-node enroll --panel <地址> --tolato           凭本机 tolato-agent 的身份证明接入面板
  patato-node install-service [-c 配置文件]           安装并启动 systemd 服务
  patato-node check [-c 配置文件]                     连一次面板，列出这台机器上的节点，不启动
  patato-node version

配置文件默认在 ` + config.DefaultPath + `。安装命令由面板生成：patato machine install-cmd <机器>。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "enroll":
		err = enroll(os.Args[2:])
	case "install-service":
		err = installService(os.Args[2:])
	case "check":
		err = check(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("patato-node %s (%s)\n", version, kernel.Version())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "patato-node:", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func loadConfig(args []string, name string) (*config.Config, string, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	path := fs.String("c", config.DefaultPath, "配置文件")
	_ = fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		return nil, *path, fmt.Errorf("读取配置 %s：%w", *path, err)
	}
	return cfg, *path, cfg.Validate()
}

func source(cfg *config.Config, log *slog.Logger) control.Source {
	if cfg.Standalone != "" {
		return control.NewFile(cfg.Standalone, filepath.Join(filepath.Dir(cfg.Standalone), "reports.jsonl"))
	}
	return control.NewPanel(cfg.Panel, cfg.Token, log)
}

func run(args []string) error {
	cfg, path, err := loadConfig(args, "run")
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)
	m := meter.New(5 * time.Minute)
	k, err := kernel.New(kernel.Options{
		DataDir: filepath.Join(cfg.StateDir, "kernel"), LogLevel: cfg.KernelLogLevel, BlockPrivate: cfg.BlocksPrivate(),
		Meter: m, Log: log,
	})
	if err != nil {
		return err
	}
	defer k.Close()
	sp, err := spool.Open(filepath.Join(cfg.StateDir, "spool"), log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("patato-node starting", "version", version, "kernel", kernel.Version(), "config", path,
		"panel", cfg.Panel, "standalone", cfg.Standalone, "spool", sp.ID())
	a := agent.New(source(cfg, log), k, m, sp, version, log)
	rm := relay.NewManager(func() (relay.Backend, error) {
		n, err := relay.NewNFT()
		if err != nil {
			return nil, err
		}
		return n, nil
	}, relay.NewUserspace(cfg.KernelLogLevel), log)
	defer rm.Close()
	a.SetRelay(rm)
	return a.Run(ctx)
}

func enroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	path := fs.String("c", config.DefaultPath, "配置文件")
	panel := fs.String("panel", "", "Patato 的 API 地址")
	code := fs.String("code", "", "面板生成的一次性注册码")
	viaTolato := fs.Bool("tolato", false, "凭本机 tolato-agent 签发的身份证明接入，不需要注册码")
	agentBin := fs.String("tolato-agent", tolato.DefaultAgent, "tolato-agent 的路径")
	agentUnit := fs.String("tolato-unit", tolato.DefaultUnit, "tolato-agent 的 systemd unit，从中读取 --server 和 --data-dir")
	stateDir := fs.String("state-dir", "", "本地状态目录，默认 /var/lib/patato-node")
	_ = fs.Parse(args)
	if *panel == "" || (*code == "" && !*viaTolato) {
		return errors.New("需要 --panel，以及 --code 或 --tolato；它们在面板生成的安装命令里")
	}
	cfg, err := config.Load(*path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hostname, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req := nodeapi.EnrollRequest{Code: *code, Hostname: hostname, Version: version}
	if *viaTolato {
		if req.Attestation, err = tolato.Attest(ctx, *agentBin, *agentUnit, strings.TrimRight(*panel, "/")); err != nil {
			return err
		}
		req.Code = ""
	}
	resp, err := control.Enroll(ctx, *panel, req)
	if err != nil {
		return fmt.Errorf("接入失败：%w", err)
	}
	cfg.Panel, cfg.Token, cfg.Machine, cfg.Standalone = strings.TrimRight(*panel, "/"), resp.Token, resp.Machine, ""
	if *stateDir != "" {
		cfg.StateDir = *stateDir
	}
	if err := cfg.Save(*path); err != nil {
		return err
	}
	fmt.Printf("已接入面板，机器名 %s，配置写入 %s\n", resp.Machine, *path)
	return nil
}

const unit = `[Unit]
Description=Patato node
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s run -c %s
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

func installService(args []string) error {
	fs := flag.NewFlagSet("install-service", flag.ExitOnError)
	path := fs.String("c", config.DefaultPath, "配置文件")
	_ = fs.Parse(args)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	if err := os.WriteFile("/etc/systemd/system/patato-node.service", []byte(fmt.Sprintf(unit, exe, *path)), 0o644); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "patato-node"}, {"restart", "patato-node"}} {
		out, err := exec.Command("systemctl", c...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl %s：%v：%s", strings.Join(c, " "), err, out)
		}
	}
	fmt.Println("patato-node 服务已启动：journalctl -u patato-node -f 查看日志")
	return nil
}

func check(args []string) error {
	cfg, _, err := loadConfig(args, "check")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := source(cfg, newLogger("error")).Sync(ctx, nodeapi.SyncRequest{Version: version, Kernel: kernel.Version()})
	if err != nil {
		return err
	}
	fmt.Printf("机器 %s，%d 个节点\n", resp.Machine, len(resp.Nodes))
	for _, n := range resp.Nodes {
		if n.Config == nil {
			continue
		}
		fmt.Printf("  %-20s %-12s 端口 %-6d 用户 %d\n", n.Config.Code, n.Config.Protocol, n.Config.Port, len(n.Users))
	}
	return nil
}
