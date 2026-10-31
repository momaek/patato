// Package proto 解析节点的协议设置。设置沿用 Xboard 的 protocol_settings 结构，
// 迁移时原样复制，节点配置和订阅生成都从这里取值。
package proto

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Settings 是一个节点的协议设置，提供带默认值的取值方法。
type Settings map[string]any

func Parse(raw json.RawMessage) Settings {
	s := Settings{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// Get 按点分路径取值，例如 "tls_settings.server_name"。
func (s Settings) Get(path string) any {
	var cur any = map[string]any(s)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func (s Settings) Str(path string) string {
	switch v := s.Get(path).(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return ""
}

func (s Settings) Int(path string) int {
	switch v := s.Get(path).(type) {
	case float64:
		return int(v)
	case string:
		var n int
		fmt.Sscanf(v, "%d", &n)
		return n
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

func (s Settings) Bool(path string) bool {
	switch v := s.Get(path).(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v == "1" || v == "true"
	}
	return false
}

func (s Settings) Map(path string) map[string]any {
	if m, ok := s.Get(path).(map[string]any); ok {
		return m
	}
	return nil
}

// Strings 取字符串或字符串数组，统一返回数组。
func (s Settings) Strings(path string) []string {
	switch v := s.Get(path).(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if str, ok := x.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// Normalize 把协议别名换成标准名，与 Xboard 一致：v2ray 即 vmess，hysteria2 即 hysteria。
func Normalize(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "v2ray":
		return "vmess"
	case "hysteria2":
		return "hysteria"
	}
	return t
}

// SS2022KeySize 返回 Shadowsocks 2022 加密方式的密钥长度；不是 2022 系列时返回 0。
func SS2022KeySize(cipher string) int {
	switch cipher {
	case "2022-blake3-aes-128-gcm":
		return 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	}
	return 0
}

// XboardServerKey 复现 Xboard 由节点创建时间派生服务端密钥的算法，迁移时用来保持密钥不变。
func XboardServerKey(createdAtUnix int64, size int) string {
	h := md5.Sum([]byte(fmt.Sprint(createdAtUnix)))
	hexs := hex.EncodeToString(h[:])
	return base64.StdEncoding.EncodeToString([]byte(hexs[:size]))
}

// UserPassword 返回用户在该节点上的连接密码：Shadowsocks 2022 为「服务端密钥:用户密钥」，
// 用户密钥取 UUID 前 n 个字符做 base64（与 Xboard、V2bX 的算法一致）；其余协议直接用 UUID。
func UserPassword(protocol string, s Settings, uuid string) string {
	if protocol != "shadowsocks" {
		return uuid
	}
	n := SS2022KeySize(s.Str("cipher"))
	if n == 0 {
		return uuid
	}
	u := uuid
	if len(u) > n {
		u = u[:n]
	}
	return s.Str("server_key") + ":" + base64.StdEncoding.EncodeToString([]byte(u))
}

// Fingerprint 把 utls 设置换成客户端指纹名，未启用时返回空串。
func (s Settings) Fingerprint() string {
	if !s.Bool("utls.enabled") {
		return ""
	}
	if fp := s.Str("utls.fingerprint"); fp != "" {
		return fp
	}
	return "chrome"
}
