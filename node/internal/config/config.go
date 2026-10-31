// Package config 读写 patato-node 的配置文件。机器 token 写在这里，文件权限为 0600。
package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/patato-node/config.yaml"

type Config struct {
	// Panel 是 Patato 的 API 地址，Token 是凭注册码换来的机器 token。
	Panel   string `yaml:"panel"`
	Token   string `yaml:"token"`
	Machine string `yaml:"machine,omitempty"`
	// StateDir 存放上报队列、证书等本地状态。
	StateDir string `yaml:"state_dir"`
	// LogLevel 是 patato-node 自身的日志级别（debug、info、warn、error），KernelLogLevel 是 sing-box 的。
	LogLevel       string `yaml:"log_level"`
	KernelLogLevel string `yaml:"kernel_log_level"`
	// BlockPrivate 拒绝用户经节点访问内网和本机地址，默认开启。
	BlockPrivate *bool `yaml:"block_private,omitempty"`
	// Standalone 不为空时不连面板，从这个本地文件读取节点和用户（格式见 control.LocalState），
	// 上报写到同目录的 reports.jsonl。
	Standalone string `yaml:"standalone,omitempty"`
}

func Default() *Config {
	return &Config{StateDir: "/var/lib/patato-node", LogLevel: "info", KernelLogLevel: "warn"}
}

// Load 读取配置；文件不存在时返回默认配置和 os.ErrNotExist。
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return c, err
	}
	if c.StateDir == "" {
		c.StateDir = Default().StateDir
	}
	return c, nil
}

func (c *Config) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) Validate() error {
	if c.Standalone != "" {
		return nil
	}
	if c.Panel == "" || c.Token == "" {
		return errors.New("panel 和 token 未配置：先执行 patato-node enroll --panel <地址> --code <注册码>")
	}
	return nil
}

func (c *Config) BlocksPrivate() bool { return c.BlockPrivate == nil || *c.BlockPrivate }
