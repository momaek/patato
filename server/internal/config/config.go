// Package config 负责读取服务端配置：YAML 文件为主，少量敏感项可用环境变量覆盖。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 让 YAML 里可以写 "30m"、"90s" 这样的时长。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type Config struct {
	Listen      string `yaml:"listen"`
	DatabaseURL string `yaml:"database_url"`
	// SecretKey 用于加密节点密钥、给 Telegram 回调签名，至少 32 个字符。
	SecretKey string `yaml:"secret_key"`

	// PublicURL 是 API 对外地址，节点、CLI、订阅链接都用它。
	PublicURL string `yaml:"public_url"`
	// AdminURL 与 PortalURL 用于前端 Host 分发、CORS、passkey 来源校验和邮件里的链接。
	AdminURL  string `yaml:"admin_url"`
	PortalURL string `yaml:"portal_url"`
	// SubscribeURL 是订阅链接的域名，默认等于 PublicURL。
	SubscribeURL string `yaml:"subscribe_url"`
	// SubscribePath 兼容 Xboard 自定义的订阅路径，例如 "/s"，访问 /s/<token>。
	SubscribePath string `yaml:"subscribe_path"`
	ServiceName   string `yaml:"service_name"`
	// Timezone 决定按自然月重置、按天统计流量用的时区。
	Timezone string `yaml:"timezone"`
	// MetricsToken 非空时开放 /metrics（Prometheus），抓取时带 Authorization: Bearer <token>。
	MetricsToken string `yaml:"metrics_token"`
	// TrustedProxies 是可信的反向代理地址：只有来自它们的 X-Forwarded-For 才被采信，
	// 注册限流和返佣风控按客户端 IP 判断。默认本机和内网（Caddy 所在的 Docker 网络）。
	TrustedProxies []string `yaml:"trusted_proxies"`

	Node     NodeConfig     `yaml:"node"`
	Risk     RiskConfig     `yaml:"risk"`
	Change   ChangeConfig   `yaml:"change"`
	Session  SessionConfig  `yaml:"session"`
	WebAuthn WebAuthnConfig `yaml:"webauthn"`
	Telegram TelegramConfig `yaml:"telegram"`
	SMTP     SMTPConfig     `yaml:"smtp"`
	Tolato   TolatoConfig   `yaml:"tolato"`

	Payment      PaymentConfig      `yaml:"payment"`
	Registration RegistrationConfig `yaml:"registration"`
	Approval     ApprovalConfig     `yaml:"approval"`
	Anomaly      AnomalyConfig      `yaml:"anomaly"`

	Monitoring MonitoringConfig `yaml:"monitoring"`
}

// ---- 监控 ----

type MonitoringConfig struct {
	Destinations DestinationsConfig `yaml:"destinations"`
}

// DestinationsConfig 是落地访问统计：patato-node 按「节点 × 用户 × 目标（主域名或 IP）」聚合流量。
// 记录的是用户的访问去向，默认关闭；开启后在用户侧「我的订阅」页告知统计范围与保留期限。
type DestinationsConfig struct {
	// Enabled 为 true 时所有 patato-node 落地开始采集，关闭后在一个同步周期内停止。
	Enabled bool `yaml:"enabled"`
	// RetentionDays 是保留天数，过期的小时数据由周期任务删除。
	RetentionDays int `yaml:"retention_days"`
}

type PaymentConfig struct {
	// OrderTimeout 是待支付订单的有效期，超时自动取消。
	OrderTimeout Duration        `yaml:"order_timeout"`
	Methods      []PaymentMethod `yaml:"methods"`
}

// PaymentMethod 是一种支付方式：epay（易支付）或 stripe（Checkout 单次付款）。
type PaymentMethod struct {
	Code   string `yaml:"code"`
	Name   string `yaml:"name"`
	Driver string `yaml:"driver"`
	URL    string `yaml:"url"`
	PID    string `yaml:"pid"`
	Key    string `yaml:"key"`
	// Type 是易支付的渠道（alipay、wxpay），为空时由收银台选择。
	Type string `yaml:"type"`
	// Stripe 的 API secret key 与 Webhook 签名密钥；不是 publishable key。
	SecretKey     string `yaml:"secret_key"`
	WebhookSecret string `yaml:"webhook_secret"`
}

type RegistrationConfig struct {
	// Enabled 为 true 时用户侧开放注册。
	Enabled bool `yaml:"enabled"`
	// RequireInvite 为 true 时只能凭邀请码注册。
	RequireInvite bool `yaml:"require_invite"`
}

type ApprovalConfig struct {
	// Required 是敏感操作默认需要几名运营者批准。
	Required int `yaml:"required"`
	// RequiredFor 按操作覆盖人数，例如 order.refund: 2。
	RequiredFor map[string]int `yaml:"required_for"`
	// CodeFor 列出批准时除 passkey 外还要输入一次性码（经 Telegram 私聊下发）的操作。
	CodeFor []string `yaml:"code_for"`
}

// Policy 返回某种操作的审批要求。
func (a ApprovalConfig) Policy(kind string) (int, bool) {
	n := a.Required
	if v, ok := a.RequiredFor[kind]; ok {
		n = v
	}
	if n < 1 {
		n = 1
	}
	for _, k := range a.CodeFor {
		if k == kind {
			return n, true
		}
	}
	return n, false
}

type AnomalyConfig struct {
	// 用户某天的用量超过前 7 天日均的 Factor 倍、且不少于 MinGB 时发出流量异常事件。Factor 为 0 时不检测。
	Factor int     `yaml:"factor"`
	MinGB  float64 `yaml:"min_gb"`
	// ExpiringDays 天内到期的用户发出「即将到期」事件，可用于自动发提醒邮件。0 表示不发。
	ExpiringDays int `yaml:"expiring_days"`
}

type NodeConfig struct {
	PushInterval int      `yaml:"push_interval"`
	PullInterval int      `yaml:"pull_interval"`
	OfflineAfter Duration `yaml:"offline_after"`
	DeviceTTL    Duration `yaml:"device_ttl"`
	// LegacyToken 是迁移前 Xboard 的全局 server_token，只在过渡期对标记了 allow_legacy_token 的节点生效。
	LegacyToken string `yaml:"legacy_token"`
	// BinaryURL 是 patato-node 安装命令下载二进制的地址，{arch} 换成 amd64 或 arm64。
	BinaryURL string `yaml:"binary_url"`
}

type RiskConfig struct {
	// 超过任一阈值，常规操作升为敏感。
	UserThreshold int `yaml:"user_threshold"`
	NodeThreshold int `yaml:"node_threshold"`
}

type ChangeConfig struct {
	PlanTTL     Duration `yaml:"plan_ttl"`
	ApprovalTTL Duration `yaml:"approval_ttl"`
}

type SessionConfig struct {
	OperatorTTL Duration `yaml:"operator_ttl"`
	UserTTL     Duration `yaml:"user_ttl"`
	Secure      bool     `yaml:"secure"`
}

type WebAuthnConfig struct {
	RPID    string   `yaml:"rp_id"`
	RPName  string   `yaml:"rp_name"`
	Origins []string `yaml:"origins"`
}

type TelegramConfig struct {
	BotToken string `yaml:"bot_token"`
	// ChatID 是接收审批卡片和告警的会话。
	ChatID int64 `yaml:"chat_id"`
}

type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

type TolatoConfig struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
	// PanelHost 是 Patato 所在主机在 tolato 里的名称，用于面板主机执行告警。
	PanelHost string   `yaml:"panel_host"`
	PollEvery Duration `yaml:"poll_every"`
	// AttestEnroll 为 true 时，patato-node 经本机 tolato-agent 签发的身份证明接入，安装命令里不再带注册码。
	// 需要 tolato 提供 /api/v1/attestations/verify。
	AttestEnroll bool `yaml:"attest_enroll"`
}

// Load 读取配置文件；path 为空时只用默认值和环境变量。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	c.applyEnv()
	c.fillDerived()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func Default() *Config {
	return &Config{
		Listen:         ":8080",
		ServiceName:    "Patato",
		TrustedProxies: []string{"127.0.0.1/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"},
		Timezone:       "Asia/Shanghai",
		Node: NodeConfig{
			PushInterval: 60,
			PullInterval: 60,
			OfflineAfter: Duration(5 * time.Minute),
			DeviceTTL:    Duration(5 * time.Minute),
			BinaryURL:    "https://github.com/momaek/patato/releases/latest/download/patato-node-linux-{arch}",
		},
		Risk:    RiskConfig{UserThreshold: 100, NodeThreshold: 5},
		Change:  ChangeConfig{PlanTTL: Duration(30 * time.Minute), ApprovalTTL: Duration(30 * time.Minute)},
		Session: SessionConfig{OperatorTTL: Duration(12 * time.Hour), UserTTL: Duration(30 * 24 * time.Hour), Secure: true},
		WebAuthn: WebAuthnConfig{
			RPName: "Patato",
		},
		SMTP:     SMTPConfig{Port: 587},
		Tolato:   TolatoConfig{PollEvery: Duration(time.Minute)},
		Payment:  PaymentConfig{OrderTimeout: Duration(30 * time.Minute)},
		Approval: ApprovalConfig{Required: 1},
		Anomaly:  AnomalyConfig{Factor: 5, MinGB: 20, ExpiringDays: 3},

		Monitoring: MonitoringConfig{Destinations: DestinationsConfig{RetentionDays: 7}},
	}
}

func (c *Config) applyEnv() {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	str("PATATO_LISTEN", &c.Listen)
	str("PATATO_DATABASE_URL", &c.DatabaseURL)
	str("PATATO_SECRET_KEY", &c.SecretKey)
	str("PATATO_PUBLIC_URL", &c.PublicURL)
	str("PATATO_ADMIN_URL", &c.AdminURL)
	str("PATATO_PORTAL_URL", &c.PortalURL)
	str("PATATO_TELEGRAM_BOT_TOKEN", &c.Telegram.BotToken)
	str("PATATO_SMTP_PASSWORD", &c.SMTP.Password)
	str("PATATO_TOLATO_API_KEY", &c.Tolato.APIKey)
	str("PATATO_NODE_LEGACY_TOKEN", &c.Node.LegacyToken)
	str("PATATO_METRICS_TOKEN", &c.MetricsToken)
	if v, ok := os.LookupEnv("PATATO_TELEGRAM_CHAT_ID"); ok {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Telegram.ChatID = id
		}
	}
}

func (c *Config) fillDerived() {
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	c.AdminURL = strings.TrimRight(c.AdminURL, "/")
	c.PortalURL = strings.TrimRight(c.PortalURL, "/")
	if c.SubscribeURL == "" {
		c.SubscribeURL = c.PublicURL
	}
	c.SubscribeURL = strings.TrimRight(c.SubscribeURL, "/")
	if c.SubscribePath != "" && !strings.HasPrefix(c.SubscribePath, "/") {
		c.SubscribePath = "/" + c.SubscribePath
	}
	c.SubscribePath = strings.TrimRight(c.SubscribePath, "/")
	if len(c.WebAuthn.Origins) == 0 && c.AdminURL != "" {
		c.WebAuthn.Origins = []string{c.AdminURL}
	}
}

// Location 返回配置的时区，无效时退回 UTC。
func (c *Config) Location() *time.Location {
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (c *Config) Validate() error {
	var errs []error
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		errs = append(errs, fmt.Errorf("invalid timezone %q", c.Timezone))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url is required"))
	}
	if len(c.SecretKey) < 32 {
		errs = append(errs, errors.New("secret_key must be at least 32 characters"))
	}
	if c.PublicURL == "" {
		errs = append(errs, errors.New("public_url is required"))
	}
	hosts := make(map[string]string)
	for _, frontend := range []struct{ name, value string }{{"admin_url", c.AdminURL}, {"portal_url", c.PortalURL}} {
		if frontend.value == "" {
			continue
		}
		u, err := url.Parse(frontend.value)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%s must be an http(s) origin without a path", frontend.name))
			continue
		}
		host := strings.ToLower(u.Host)
		if previous := hosts[host]; previous != "" {
			errs = append(errs, fmt.Errorf("%s and %s must use different hosts or ports", previous, frontend.name))
		}
		hosts[host] = frontend.name
	}
	if c.Risk.UserThreshold <= 0 || c.Risk.NodeThreshold <= 0 {
		errs = append(errs, errors.New("risk thresholds must be positive"))
	}
	seen := map[string]bool{}
	for i, m := range c.Payment.Methods {
		switch {
		case m.Code == "" || strings.ContainsAny(m.Code, "/?# "):
			errs = append(errs, fmt.Errorf("payment.methods[%d].code is required and must be a plain word", i))
		case seen[m.Code]:
			errs = append(errs, fmt.Errorf("payment.methods[%d].code %q is duplicated", i, m.Code))
		case m.Driver != "epay" && m.Driver != "stripe":
			errs = append(errs, fmt.Errorf("payment.methods[%d].driver %q is not supported (epay, stripe)", i, m.Driver))
		case m.Driver == "epay" && (m.URL == "" || m.PID == "" || m.Key == ""):
			errs = append(errs, fmt.Errorf("payment.methods[%d] needs url, pid and key", i))
		case m.Driver == "stripe" && (m.SecretKey == "" || m.WebhookSecret == ""):
			errs = append(errs, fmt.Errorf("payment.methods[%d] needs secret_key and webhook_secret", i))
		}
		seen[m.Code] = true
	}
	if c.Tolato.AttestEnroll && (c.Tolato.URL == "" || c.Tolato.APIKey == "") {
		errs = append(errs, errors.New("tolato.attest_enroll needs tolato.url and tolato.api_key"))
	}
	if c.Approval.Required < 1 {
		errs = append(errs, errors.New("approval.required must be at least 1"))
	}
	if d := c.Monitoring.Destinations.RetentionDays; d < 1 || d > 90 {
		errs = append(errs, errors.New("monitoring.destinations.retention_days must be between 1 and 90"))
	}
	return errors.Join(errs...)
}

// WebAuthnEnabled 表示是否配置了 passkey；未配置时运营者无法登录 Admin、无法在网页上审批。
func (c *Config) WebAuthnEnabled() bool { return c.WebAuthn.RPID != "" && len(c.WebAuthn.Origins) > 0 }
