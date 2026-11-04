// Package app 把各个服务装配成一个完整的 Patato 服务端。
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/momaek/patato/server/internal/api"
	"github.com/momaek/patato/server/internal/approval"
	"github.com/momaek/patato/server/internal/auth"
	"github.com/momaek/patato/server/internal/automation"
	"github.com/momaek/patato/server/internal/billing"
	"github.com/momaek/patato/server/internal/change"
	"github.com/momaek/patato/server/internal/commerce"
	"github.com/momaek/patato/server/internal/config"
	"github.com/momaek/patato/server/internal/events"
	"github.com/momaek/patato/server/internal/jobs"
	"github.com/momaek/patato/server/internal/native"
	"github.com/momaek/patato/server/internal/notify"
	"github.com/momaek/patato/server/internal/ops"
	"github.com/momaek/patato/server/internal/rules"
	"github.com/momaek/patato/server/internal/secret"
	"github.com/momaek/patato/server/internal/store"
	"github.com/momaek/patato/server/internal/store/db"
	"github.com/momaek/patato/server/internal/subscribe"
	"github.com/momaek/patato/server/internal/tolato"
	"github.com/momaek/patato/server/internal/uniproxy"
)

type App struct {
	Cfg       *config.Config
	Store     *store.Store
	Box       *secret.Box
	Engine    *change.Engine
	Approvals *approval.Service
	Auth      *auth.Service
	Events    *events.Recorder
	Billing   *billing.Processor
	Telegram  *notify.Telegram
	Tolato    *tolato.Client
	Jobs      *jobs.Runner
	Native    *native.Handler
	Commerce  *commerce.Service
	Automate  *automation.Dispatcher
	Rules     *rules.Service
	HTTP      *api.Server
	Log       *slog.Logger
}

// New 连接数据库、执行迁移并装配全部服务，但不启动后台任务和 HTTP 监听。
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	if err := jobs.Migrate(ctx, st.Pool); err != nil {
		st.Close()
		return nil, fmt.Errorf("migrate jobs: %w", err)
	}
	box, err := secret.NewBox(cfg.SecretKey)
	if err != nil {
		st.Close()
		return nil, err
	}
	a := &App{Cfg: cfg, Store: st, Box: box, Log: log}
	loc := cfg.Location()

	a.Events = events.NewRecorder(st, log)
	a.Engine = change.NewEngine(st, change.Thresholds{Users: cfg.Risk.UserThreshold, Nodes: cfg.Risk.NodeThreshold},
		cfg.Change.PlanTTL.D(), cfg.Change.ApprovalTTL.D(), log)
	a.Engine.SetPolicy(cfg.Approval.Policy)
	a.Rules = rules.NewService(st, log)
	deps := &ops.Deps{Box: box, Loc: loc, Events: a.Events, Operations: a.Engine.Kinds, Rules: a.Rules}
	if cfg.Tolato.URL != "" && cfg.Tolato.APIKey != "" {
		a.Tolato = tolato.New(cfg.Tolato.URL, cfg.Tolato.APIKey)
		deps.Machines = a.Tolato
	}
	a.Engine.Register(ops.All(deps)...)
	var gateways []commerce.Gateway
	for _, m := range cfg.Payment.Methods {
		name := m.Name
		if name == "" {
			name = m.Code
		}
		switch m.Driver {
		case "stripe":
			gateways = append(gateways, commerce.NewStripe(m.Code, name, m.SecretKey, m.WebhookSecret))
		case "epay":
			gateways = append(gateways, commerce.NewEPay(m.Code, name, m.URL, m.PID, m.Key, m.Type))
		}
	}
	a.Commerce = commerce.NewService(st, a.Events, commerce.Config{
		OrderTimeout: cfg.Payment.OrderTimeout.D(), NotifyBase: cfg.PublicURL, ReturnBase: cfg.PortalURL, Loc: loc,
	}, gateways, log)
	a.Approvals = approval.New(st, a.Engine, log)
	if a.Auth, err = auth.New(st, cfg, log); err != nil {
		st.Close()
		return nil, err
	}
	a.Billing = billing.NewProcessor(st, a.Events, loc, log)

	if cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != 0 {
		a.Telegram, err = notify.NewTelegram(cfg.Telegram.BotToken, cfg.Telegram.ChatID, st, a.Approvals, box, loc, cfg.AdminURL, log)
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("telegram: %w", err)
		}
	}

	var mailer notify.Mailer = &notify.LogMailer{Log: log}
	if cfg.SMTP.Host != "" {
		mailer = &notify.SMTPMailer{Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username, Password: cfg.SMTP.Password, From: cfg.SMTP.From}
	}
	a.Automate = automation.New(st, box, a.Engine, mailer, automation.Config{ServiceName: cfg.ServiceName, PortalURL: cfg.PortalURL}, log)

	var sender jobs.Sender
	if a.Telegram != nil {
		sender = &telegramSender{tg: a.Telegram, st: st}
		a.Approvals.SetCodeSender(a.Telegram)
	}
	a.Jobs, err = jobs.New(st.Pool, a.tasks(), sender, a.Automate, log)
	if err != nil {
		st.Close()
		return nil, err
	}
	a.Events.SetNotifier(a.Jobs)
	a.Engine.SetNotifier(a.Jobs)
	a.Approvals.SetNotifier(a.Jobs)
	a.Native = native.New(st, native.Config{
		ReportInterval: cfg.Node.PushInterval, SyncInterval: cfg.Node.PullInterval, DeviceTTL: cfg.Node.DeviceTTL.D(),
		PublicURL: func() string { return cfg.PublicURL },
	}, a.Events, log)
	if cfg.Tolato.AttestEnroll && a.Tolato != nil {
		a.Native.SetAttestor(tolatoAttestor{a.Tolato})
	}
	var binder api.TelegramBinder
	if a.Telegram != nil {
		binder = a.Telegram
	}
	a.HTTP = api.New(api.Deps{
		Cfg: cfg, Store: st, Engine: a.Engine, Approvals: a.Approvals, Auth: a.Auth, Box: box,
		UniProxy: uniproxy.New(st, uniproxy.Config{
			PushInterval: cfg.Node.PushInterval, PullInterval: cfg.Node.PullInterval,
			LegacyToken: cfg.Node.LegacyToken, DeviceTTL: cfg.Node.DeviceTTL.D(),
		}, a.Events, log),
		Subscribe: subscribe.New(st, subscribe.Config{ServiceName: cfg.ServiceName, Path: cfg.SubscribePath, BaseURL: cfg.SubscribeURL, Loc: loc}, a.Rules, log),
		Native:    a.Native,
		Mailer:    mailer, Telegram: binder, Commerce: a.Commerce, Events: a.Events, Rules: a.Rules, Log: log,
	})
	return a, nil
}

func (a *App) tasks() []jobs.Task {
	return []jobs.Task{
		{Name: "traffic", Every: 15 * time.Second, Run: func(ctx context.Context) error {
			_, err := a.Billing.ProcessReports(ctx)
			return err
		}},
		{Name: "traffic_reset", Every: time.Minute, Run: func(ctx context.Context) error {
			_, err := a.Billing.ResetDue(ctx)
			return err
		}},
		{Name: "expiry", Every: time.Minute, Run: a.Billing.CheckExpired},
		// 分流规则模板和规则文件：每小时检查一次，超过 6 小时的重新拉取
		{Name: "rules_refresh", Every: time.Hour, Run: a.Rules.Refresh},
		{Name: "nodes_offline", Every: 30 * time.Second, Run: func(ctx context.Context) error {
			return a.Billing.CheckNodesOffline(ctx, a.Cfg.Node.OfflineAfter.D())
		}},
		{Name: "approvals_expire", Every: 30 * time.Second, Run: a.Approvals.ExpireDue},
		{Name: "cleanup", Every: 10 * time.Minute, Run: func(ctx context.Context) error {
			return a.Billing.Cleanup(ctx, a.Cfg.Node.DeviceTTL.D())
		}},
		{Name: "panel_host", Every: a.Cfg.Tolato.PollEvery.D(), Run: a.checkPanelHost},
		{Name: "orders_expire", Every: time.Minute, Run: func(ctx context.Context) error {
			_, err := a.Commerce.ExpireDue(ctx)
			return err
		}},
		{Name: "commissions_settle", Every: 10 * time.Minute, Run: func(ctx context.Context) error {
			_, err := a.Commerce.SettleDue(ctx)
			return err
		}},
		{Name: "expiring", Every: time.Hour, Run: func(ctx context.Context) error {
			return a.Billing.CheckExpiring(ctx, a.Cfg.Anomaly.ExpiringDays)
		}},
		{Name: "anomalies", Every: time.Hour, Run: func(ctx context.Context) error {
			return a.Billing.CheckAnomalies(ctx, a.Cfg.Anomaly.Factor, billing.GB(a.Cfg.Anomaly.MinGB))
		}},
	}
}

const panelCursorKey = "tolato.panel_host_cursor"

// checkPanelHost 拉取 tolato 中针对 Patato 所在主机的执行记录，有记录即告警。
// 这是「Agent 可在面板主机上执行命令」这一已接受风险的事后发现手段。
func (a *App) checkPanelHost(ctx context.Context) error {
	save := func(st api.PanelHostStatus) error {
		st.CheckedAt = time.Now()
		b, _ := json.Marshal(st)
		return a.Store.SetSetting(ctx, db.SetSettingParams{Key: api.PanelHostKey, Value: b})
	}
	if a.Tolato == nil || a.Cfg.Tolato.PanelHost == "" {
		return save(api.PanelHostStatus{Status: "not_configured", Message: "未配置 tolato.url、tolato.api_key 或 tolato.panel_host"})
	}
	since := time.Now().Add(-24 * time.Hour)
	if raw, err := a.Store.GetSetting(ctx, panelCursorKey); err == nil {
		_ = json.Unmarshal(raw, &since)
	}
	cmds, err := a.Tolato.CommandsSince(ctx, a.Cfg.Tolato.PanelHost, since)
	if errors.Is(err, tolato.ErrUnsupported) {
		return save(api.PanelHostStatus{Status: "unsupported", Message: "tolato 尚未提供执行记录接口（/api/v1/nodes/{id}/commands）"})
	}
	if err != nil {
		return save(api.PanelHostStatus{Status: "error", Message: err.Error()})
	}
	latest := since
	for _, cmd := range cmds {
		a.Events.Emit(ctx, nil, events.Event{
			Kind: events.PanelHostExec, Severity: "critical", Subject: "panel_host",
			Message:  fmt.Sprintf("面板主机 %s 上有远程执行：%s（来源 %s）", a.Cfg.Tolato.PanelHost, truncate(cmd.Command, 120), cmd.Source),
			Payload:  cmd,
			DedupKey: fmt.Sprintf("panel_host_exec:%d", cmd.ID),
		})
		if cmd.CreatedAt.After(latest) {
			latest = cmd.CreatedAt
		}
	}
	b, _ := json.Marshal(latest)
	_ = a.Store.SetSetting(ctx, db.SetSettingParams{Key: panelCursorKey, Value: b})
	if len(cmds) > 0 {
		return save(api.PanelHostStatus{Status: "alert", Message: fmt.Sprintf("发现 %d 条针对面板主机的远程执行，已推送告警", len(cmds))})
	}
	return save(api.PanelHostStatus{Status: "ok", Message: "过去一个周期没有针对面板主机的远程执行"})
}

// Run 启动后台任务、Telegram 与 HTTP 服务，直到 ctx 结束后优雅退出。
func (a *App) Run(ctx context.Context) error {
	if err := a.Jobs.Start(ctx); err != nil {
		return fmt.Errorf("start jobs: %w", err)
	}
	if a.Telegram != nil {
		go a.Telegram.Run(ctx)
	}
	go a.Native.Hub().Run(ctx)
	srv := &http.Server{Addr: a.Cfg.Listen, Handler: a.HTTP, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		a.Log.Info("patato listening", "addr", a.Cfg.Listen)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	_ = a.Jobs.Stop(shutdown)
	a.Store.Close()
	return nil
}

// tolatoAttestor 把 tolato 客户端接到原生节点协议的接入流程上。
type tolatoAttestor struct{ c *tolato.Client }

func (t tolatoAttestor) VerifyAttestation(ctx context.Context, token, audience string) ([]string, error) {
	n, err := t.c.VerifyAttestation(ctx, token, audience)
	if err != nil {
		return nil, err
	}
	return n.Names(), nil
}

type telegramSender struct {
	tg *notify.Telegram
	st *store.Store
}

// SendEvent 只推送需要人关注的事件（告警、注意）、节点恢复和新工单。
func (s *telegramSender) SendEvent(ctx context.Context, id int64) error {
	e, err := s.st.GetEvent(ctx, id)
	if err != nil {
		return err
	}
	if e.NotifiedAt != nil {
		return nil
	}
	if e.Severity == "info" && e.Kind != events.NodeOnline && e.Kind != events.TicketCreated {
		return nil
	}
	if err := s.tg.Send(ctx, notify.EventText(e)); err != nil {
		return err
	}
	return s.st.MarkEventNotified(ctx, id)
}

func (s *telegramSender) SendApproval(ctx context.Context, id string) error {
	return s.tg.SendApproval(ctx, id)
}

func (s *telegramSender) UpdateApproval(ctx context.Context, id string) error {
	return s.tg.UpdateApproval(ctx, id)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
