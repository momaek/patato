// Command patato-server 是 Patato 服务端。
//
//	patato-server [serve] [-config config.yaml]      启动服务（默认）
//	patato-server migrate [-config config.yaml]      只执行数据库迁移
//	patato-server operator invite -name <名字>       生成运营者邀请链接（首次部署用它创建第一个运营者）
//	patato-server import-xboard -dsn <DSN> [-dry-run] 从 Xboard 导入套餐、节点、用户
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/momaek/patato/server/internal/app"
	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/jobs"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/xboard"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, args, log)
	case "migrate":
		err = migrate(ctx, args)
	case "operator":
		err = operator(ctx, args, log)
	case "import-xboard":
		err = importXboard(ctx, args, log)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadConfig(fs *flag.FlagSet, args []string) (*config.Config, error) {
	path := fs.String("config", os.Getenv("PATATO_CONFIG"), "配置文件路径")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return config.Load(*path)
}

func serve(ctx context.Context, args []string, log *slog.Logger) error {
	cfg, err := loadConfig(flag.NewFlagSet("serve", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	if !cfg.WebAuthnEnabled() {
		log.Warn("passkey not configured: operators cannot sign in to Admin or approve on the web")
	}
	if a.Telegram == nil {
		log.Warn("telegram not configured: approval cards and alerts will not be pushed")
	}
	return a.Run(ctx)
}

func migrate(ctx context.Context, args []string) error {
	cfg, err := loadConfig(flag.NewFlagSet("migrate", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	return jobs.Migrate(ctx, st.Pool)
}

func operator(ctx context.Context, args []string, log *slog.Logger) error {
	if len(args) == 0 || args[0] != "invite" {
		return fmt.Errorf("usage: patato-server operator invite -name <名字>")
	}
	fs := flag.NewFlagSet("operator invite", flag.ExitOnError)
	name := fs.String("name", "", "运营者名字")
	cfg, err := loadConfig(fs, args[1:])
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	svc, err := auth.New(st, cfg, log)
	if err != nil {
		return err
	}
	token, err := svc.InviteOperator(ctx, *name)
	if err != nil {
		return err
	}
	if cfg.AdminURL != "" {
		fmt.Printf("把这个链接发给 %s，24 小时内有效：\n%s/setup?invite=%s\n", *name, cfg.AdminURL, token)
	} else {
		fmt.Printf("邀请令牌（24 小时内有效）：%s\n", token)
	}
	return nil
}

func importXboard(ctx context.Context, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("import-xboard", flag.ExitOnError)
	driver := fs.String("driver", "mysql", "Xboard 数据库类型：mysql 或 pgx")
	dsn := fs.String("dsn", "", "Xboard 数据库连接串，例如 user:pass@tcp(127.0.0.1:3306)/xboard")
	prefix := fs.String("prefix", "v2_", "Xboard 表前缀")
	dry := fs.Bool("dry-run", false, "只预演，不写入")
	reset := fs.String("default-reset", "calendar_month", "Xboard 套餐「跟随系统设置」时的重置方式")
	cfg, err := loadConfig(fs, args)
	if err != nil {
		return err
	}
	if *dsn == "" {
		return fmt.Errorf("-dsn is required")
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	box, err := secret.NewBox(cfg.SecretKey)
	if err != nil {
		return err
	}
	rep, err := xboard.Import(ctx, st, box, xboard.Options{
		Driver: *driver, DSN: *dsn, Prefix: *prefix, DryRun: *dry, DefaultReset: *reset, Loc: cfg.Location(),
	}, log)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	if !*dry {
		fmt.Println("导入完成。节点暂时允许使用 Xboard 的全局 token（node.legacy_token），逐台切换到节点密钥后再关闭。")
	}
	return nil
}
