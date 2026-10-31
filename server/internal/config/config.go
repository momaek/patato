// Package config 负责读取服务端配置：YAML 文件为主，少量敏感项可用环境变量覆盖。
package config

import (
	"errors"
	"fmt"
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
	// AdminURL 与 PortalURL 是两个前端的地址，用于 CORS、passkey 来源校验和邮件里的链接。
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

	Node     NodeConfig     `yaml:"node"`
	Risk     RiskConfig     `yaml:"risk"`
	Change   ChangeConfig   `yaml:"change"`
	Session  SessionConfig  `yaml:"session"`
	WebAuthn WebAuthnConfig `yaml:"webauthn"`
	Telegram TelegramConfig `yaml:"telegram"`
	SMTP     SMTPConfig     `yaml:"smtp"`
	Tolato   TolatoConfig   `yaml:"tolato"`
}

type NodeConfig struct {
	PushInterval int      `yaml:"push_interval"`
	PullInterval int      `yaml:"pull_interval"`
	OfflineAfter Duration `yaml:"offline_after"`
	DeviceTTL    Duration `yaml:"device_ttl"`
	// LegacyToken 是迁移前 Xboard 的全局 server_token，只在过渡期对标记了 allow_legacy_token 的节点生效。
	LegacyToken string `yaml:"legacy_token"`
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
		Listen:      ":8080",
		ServiceName: "Patato",
		Timezone:    "Asia/Shanghai",
		Node: NodeConfig{
			PushInterval: 60,
			PullInterval: 60,
			OfflineAfter: Duration(5 * time.Minute),
			DeviceTTL:    Duration(5 * time.Minute),
		},
		Risk:    RiskConfig{UserThreshold: 100, NodeThreshold: 5},
		Change:  ChangeConfig{PlanTTL: Duration(30 * time.Minute), ApprovalTTL: Duration(30 * time.Minute)},
		Session: SessionConfig{OperatorTTL: Duration(12 * time.Hour), UserTTL: Duration(30 * 24 * time.Hour), Secure: true},
		WebAuthn: WebAuthnConfig{
			RPName: "Patato",
		},
		SMTP:   SMTPConfig{Port: 587},
		Tolato: TolatoConfig{PollEvery: Duration(time.Minute)},
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
	if c.Risk.UserThreshold <= 0 || c.Risk.NodeThreshold <= 0 {
		errs = append(errs, errors.New("risk thresholds must be positive"))
	}
	return errors.Join(errs...)
}

// WebAuthnEnabled 表示是否配置了 passkey；未配置时运营者无法登录 Admin、无法在网页上审批。
func (c *Config) WebAuthnEnabled() bool { return c.WebAuthn.RPID != "" && len(c.WebAuthn.Origins) > 0 }
