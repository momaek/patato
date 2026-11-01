package subscribe

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// 二期新增的订阅格式。字段写法对照 Xboard 的 Surge、QuantumultX、Loon 生成器（这些写法在真实客户端里长期使用过）；
// 每种客户端只支持部分协议，不支持的节点直接跳过，以免整份配置导入失败。

// classicSS 是 Surge 和 Loon 都支持的 Shadowsocks 加密方式；Shadowsocks 2022 不下发给它们，与 Xboard 一致。
var classicSS = []string{"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"}

// surgeName 去掉 Surge / Loon 配置里有特殊含义的字符（逗号、等号）。
func surgeName(s string) string {
	return strings.NewReplacer(",", " ", "=", " ", "\n", " ").Replace(s)
}

// Surge 生成 Surge 托管配置（Surfboard 兼容同一语法）。managedURL 是订阅链接，Surge 据此自动更新。
// r 为 nil 时用内置的两个策略组和简单规则。
func Surge(servers []Server, managedURL string, r *Rules) string {
	var proxies, names []string
	for _, s := range servers {
		line := surgeProxy(s)
		if line == "" {
			continue
		}
		name := surgeName(s.Name)
		proxies = append(proxies, name+" = "+line)
		names = append(names, name)
	}
	group := strings.Join(names, ", ")
	if group == "" {
		group = "DIRECT"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#!MANAGED-CONFIG %s interval=43200 strict=false\n\n", managedURL)
	b.WriteString("[General]\nloglevel = notify\ndns-server = 223.5.5.5, 114.114.114.114\n")
	b.WriteString("skip-proxy = localhost, *.local, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16\n")
	b.WriteString("proxy-test-url = http://www.gstatic.com/generate_204\n\n")
	b.WriteString("[Proxy]\n")
	for _, p := range proxies {
		b.WriteString(p + "\n")
	}
	b.WriteString("\n[Proxy Group]\n")
	if r != nil {
		groups, rules := surgeRuled(names, r)
		b.WriteString(groups + "\n[Rule]\n" + rules)
		return b.String()
	}
	fmt.Fprintf(&b, "%s = select, %s, %s, DIRECT\n", groupSelect, groupAuto, group)
	fmt.Fprintf(&b, "%s = url-test, %s, url=http://www.gstatic.com/generate_204, interval=300\n", groupAuto, group)
	b.WriteString("\n[Rule]\nGEOIP,CN,DIRECT\nFINAL," + groupSelect + ",dns-failed\n")
	return b.String()
}

func surgeProxy(s Server) string {
	p := s.S
	head := []string{"", s.Host, strconv.Itoa(s.Port)}
	join := func(kind string, rest ...string) string {
		head[0] = kind
		return strings.Join(append(head, rest...), ", ")
	}
	switch s.Type {
	case "shadowsocks":
		if !slices.Contains(classicSS, p.Str("cipher")) || p.Str("plugin") != "" {
			return ""
		}
		return join("ss", "encrypt-method="+p.Str("cipher"), "password="+s.Password, "udp-relay=true")
	case "vmess":
		parts := []string{"username=" + s.Password, "vmess-aead=true", "udp-relay=true"}
		if p.Int("tls") > 0 {
			parts = append(parts, "tls=true", fmt.Sprintf("skip-cert-verify=%t", p.Bool("tls_settings.allow_insecure")))
			if sni := p.Str("tls_settings.server_name"); sni != "" {
				parts = append(parts, "sni="+sni)
			}
		}
		switch p.Str("network") {
		case "ws":
			parts = append(parts, "ws=true")
			if path := p.Str("network_settings.path"); path != "" {
				parts = append(parts, "ws-path="+path)
			}
			if h := p.Str("network_settings.headers.Host"); h != "" {
				parts = append(parts, "ws-headers=Host:"+h)
			}
		case "", "tcp":
		default:
			return "" // Surge 只支持 VMess 的 TCP 和 WebSocket
		}
		return join("vmess", parts...)
	case "trojan":
		if p.Int("tls") == 2 || (p.Str("network") != "" && p.Str("network") != "tcp") {
			return ""
		}
		parts := []string{"password=" + s.Password, "udp-relay=true", fmt.Sprintf("skip-cert-verify=%t", p.TrojanInsecure())}
		if sni := p.TrojanSNI(); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		return join("trojan", parts...)
	case "hysteria":
		if p.Int("version") == 1 || p.Bool("obfs.open") {
			return "" // Surge 的 Hysteria2 不支持混淆
		}
		parts := []string{"password=" + s.Password, "udp-relay=true", fmt.Sprintf("skip-cert-verify=%t", p.Bool("tls.allow_insecure"))}
		if sni := p.Str("tls.server_name"); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		return join("hysteria2", parts...)
	case "tuic":
		// Xboard 不给 Surge 下发 TUIC；按 Surge 手册的 tuic-v5 写法
		parts := []string{"password=" + s.Password, "uuid=" + s.Password, "alpn=h3", fmt.Sprintf("skip-cert-verify=%t", p.Bool("tls.allow_insecure"))}
		if sni := p.Str("tls.server_name"); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		return join("tuic-v5", parts...)
	}
	return ""
}

// QuantumultX 生成 Quantumult X 的节点订阅（server_remote），与 Xboard 一致按 base64 返回。
func QuantumultX(servers []Server) string {
	var lines []string
	for _, s := range servers {
		if l := quanxLine(s); l != "" {
			lines = append(lines, l)
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(lines, "\r\n")))
}

func quanxLine(s Server) string {
	p := s.S
	addr := hostPort(s)
	tail := []string{"fast-open=false", "udp-relay=true", "tag=" + strings.ReplaceAll(s.Name, ",", " ")}
	join := func(parts ...string) string { return strings.Join(append(parts, tail...), ", ") }
	switch s.Type {
	case "shadowsocks":
		if p.Str("plugin") != "" {
			return ""
		}
		return join("shadowsocks="+addr, "method="+p.Str("cipher"), "password="+s.Password)
	case "vmess":
		parts := []string{"vmess=" + addr, "method=chacha20-poly1305", "password=" + s.Password}
		tls := p.Int("tls") > 0
		// obfs-host 优先用 TLS 的 SNI，其次 WebSocket 的 Host，与 Xboard 一致
		host := p.Str("tls_settings.server_name")
		switch p.Str("network") {
		case "ws":
			if tls {
				parts = append(parts, "obfs=wss")
			} else {
				parts = append(parts, "obfs=ws")
			}
			if path := p.Str("network_settings.path"); path != "" {
				parts = append(parts, "obfs-uri="+path)
			}
			if host == "" {
				host = p.Str("network_settings.headers.Host")
			}
		case "", "tcp":
			if tls {
				parts = append(parts, "obfs=over-tls")
			}
		default:
			return ""
		}
		if tls {
			parts = append(parts, fmt.Sprintf("tls-verification=%t", !p.Bool("tls_settings.allow_insecure")))
		}
		if host != "" {
			parts = append(parts, "obfs-host="+host)
		}
		return join(parts...)
	case "trojan":
		if p.Int("tls") == 2 || (p.Str("network") != "" && p.Str("network") != "tcp") {
			return ""
		}
		parts := []string{"trojan=" + addr, "password=" + s.Password, "over-tls=true"}
		if sni := p.TrojanSNI(); sni != "" {
			parts = append(parts, "tls-host="+sni)
		}
		return join(append(parts, fmt.Sprintf("tls-verification=%t", !p.TrojanInsecure()))...)
	case "vless":
		// Xboard 不给 Quantumult X 下发 VLESS；按 Quantumult X 示例配置的 vless 写法，只下发 TCP 和 WebSocket
		if n := p.Str("network"); n != "" && n != "tcp" && n != "ws" {
			return ""
		}
		parts := []string{"vless=" + addr, "method=none", "password=" + s.Password}
		switch p.Int("tls") {
		case 1:
			if p.Str("network") == "ws" {
				parts = append(parts, "obfs=wss", "obfs-uri="+firstNonEmpty(p.Str("network_settings.path"), "/"))
			} else {
				parts = append(parts, "obfs=over-tls")
			}
			if sni := p.Str("tls_settings.server_name"); sni != "" {
				parts = append(parts, "obfs-host="+sni)
			}
			parts = append(parts, fmt.Sprintf("tls-verification=%t", !p.Bool("tls_settings.allow_insecure")))
		case 2:
			parts = append(parts, "obfs=over-tls", "obfs-host="+p.Str("reality_settings.server_name"),
				"reality-base64-pubkey="+p.Str("reality_settings.public_key"), "reality-hex-shortid="+p.Str("reality_settings.short_id"))
		default:
			if p.Str("network") == "ws" {
				parts = append(parts, "obfs=ws", "obfs-uri="+firstNonEmpty(p.Str("network_settings.path"), "/"))
			}
		}
		if flow := p.Str("flow"); flow != "" {
			parts = append(parts, "vless-flow="+flow)
		}
		return join(parts...)
	}
	return ""
}

// Loon 生成 Loon 的节点订阅（每行一个节点），写法与 Xboard 一致。
func Loon(servers []Server) string {
	var lines []string
	for _, s := range servers {
		if l := loonLine(s); l != "" {
			lines = append(lines, surgeName(s.Name)+" = "+l)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func loonLine(s Server) string {
	p := s.S
	host, port := s.Host, strconv.Itoa(s.Port)
	join := func(parts ...string) string { return strings.Join(parts, ",") }
	switch s.Type {
	case "shadowsocks":
		if !slices.Contains(classicSS, p.Str("cipher")) || p.Str("plugin") != "" {
			return ""
		}
		return join("Shadowsocks", host, port, p.Str("cipher"), s.Password, "fast-open=false", "udp=true")
	case "vmess":
		parts := []string{"vmess", host, port, "auto", s.Password, "fast-open=false", "udp=true", "alterId=0"}
		if p.Int("tls") > 0 {
			if p.Str("network") == "" || p.Str("network") == "tcp" {
				parts = append(parts, "over-tls=true")
			}
			parts = append(parts, fmt.Sprintf("skip-cert-verify=%t", p.Bool("tls_settings.allow_insecure")))
			if sni := p.Str("tls_settings.server_name"); sni != "" {
				parts = append(parts, "tls-name="+sni)
			}
		}
		switch p.Str("network") {
		case "ws":
			parts = append(parts, "transport=ws")
			if path := p.Str("network_settings.path"); path != "" {
				parts = append(parts, "path="+path)
			}
			if h := p.Str("network_settings.headers.Host"); h != "" {
				parts = append(parts, "host="+h)
			}
		case "", "tcp":
			parts = append(parts, "transport=tcp")
		default:
			return ""
		}
		return join(parts...)
	case "trojan":
		if p.Int("tls") == 2 || (p.Str("network") != "" && p.Str("network") != "tcp") {
			return ""
		}
		parts := []string{"trojan", host, port, s.Password}
		if sni := p.TrojanSNI(); sni != "" {
			parts = append(parts, "tls-name="+sni)
		}
		return join(append(parts, "fast-open=false", "udp=true", fmt.Sprintf("skip-cert-verify=%t", p.TrojanInsecure()))...)
	case "hysteria":
		if p.Int("version") == 1 || p.Bool("obfs.open") {
			return "" // 与 Xboard 一致，不下发带混淆的 Hysteria2
		}
		parts := []string{"Hysteria2", host, port, s.Password}
		if sni := p.Str("tls.server_name"); sni != "" {
			parts = append(parts, "sni="+sni)
		}
		if p.Bool("tls.allow_insecure") {
			parts = append(parts, "skip-cert-verify=true")
		}
		return join(append(parts, "udp=true")...)
	}
	return ""
}
