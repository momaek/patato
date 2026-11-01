package subscribe

import (
	"encoding/json"
	"strings"
)

// SingBox 生成 sing-box 配置（sing-box 1.11+ 的路由动作语法），适用于 SFI / SFA / SFM、Hiddify 等。
// r 为 nil 时用内置的两个策略组和简单规则。
func SingBox(servers []Server, r *Rules) ([]byte, error) {
	outbounds := []map[string]any{}
	names := []string{}
	for _, s := range servers {
		o := singboxOutbound(s)
		if o == nil {
			continue
		}
		outbounds = append(outbounds, o)
		names = append(names, s.Name)
	}
	auto := names
	if len(auto) == 0 {
		auto = []string{"direct"}
	}
	groups := []map[string]any{
		{"type": "selector", "tag": groupSelect, "outbounds": append(append([]string{groupAuto}, names...), "direct"), "default": groupAuto},
		{"type": "urltest", "tag": groupAuto, "outbounds": auto, "url": "https://www.gstatic.com/generate_204", "interval": "5m", "tolerance": 50},
	}
	all := append(groups, outbounds...)
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})
	cfg := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"inbounds": []map[string]any{
			{"type": "tun", "tag": "tun-in", "address": []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"auto_route": true, "strict_route": true},
			{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890},
		},
		"outbounds": all,
		"route": map[string]any{
			"auto_detect_interface": true,
			"final":                 groupSelect,
			"rules": []map[string]any{
				{"action": "sniff"},
				{"protocol": "dns", "action": "hijack-dns"},
				{"ip_is_private": true, "outbound": "direct"},
			},
		},
	}
	if r != nil {
		singboxRuled(cfg, outbounds, names, r)
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func singboxOutbound(s Server) map[string]any {
	p := s.S
	o := map[string]any{"tag": s.Name, "server": s.Host, "server_port": s.Port}
	tlsWith := func(sni string, insecure bool) map[string]any {
		t := map[string]any{"enabled": true}
		if sni != "" {
			t["server_name"] = sni
		}
		if insecure {
			t["insecure"] = true
		}
		if fp := p.Fingerprint(); fp != "" {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		return t
	}
	tls := func(sniPath, insecurePath string) map[string]any {
		return tlsWith(p.Str(sniPath), p.Bool(insecurePath))
	}
	reality := func() map[string]any {
		return map[string]any{
			"enabled":     true,
			"server_name": p.Str("reality_settings.server_name"),
			"utls":        map[string]any{"enabled": true, "fingerprint": firstNonEmpty(p.Fingerprint(), "chrome")},
			"reality":     map[string]any{"enabled": true, "public_key": p.Str("reality_settings.public_key"), "short_id": p.Str("reality_settings.short_id")},
		}
	}
	transport := func() {
		switch p.Str("network") {
		case "ws":
			t := map[string]any{"type": "ws", "path": firstNonEmpty(p.Str("network_settings.path"), "/")}
			if host := p.Str("network_settings.headers.Host"); host != "" {
				t["headers"] = map[string]any{"Host": host}
			}
			o["transport"] = t
		case "grpc":
			o["transport"] = map[string]any{"type": "grpc", "service_name": p.Str("network_settings.serviceName")}
		case "h2":
			o["transport"] = map[string]any{"type": "http", "path": p.Str("network_settings.path"), "host": p.Strings("network_settings.host")}
		case "httpupgrade":
			o["transport"] = map[string]any{"type": "httpupgrade", "path": p.Str("network_settings.path"),
				"host": firstNonEmpty(p.Str("network_settings.host"), s.Host)}
		}
	}
	switch s.Type {
	case "vmess":
		o["type"], o["uuid"], o["security"], o["alter_id"] = "vmess", s.Password, "auto", 0
		if p.Int("tls") > 0 {
			o["tls"] = tls("tls_settings.server_name", "tls_settings.allow_insecure")
		}
		transport()
	case "vless":
		o["type"], o["uuid"] = "vless", s.Password
		if flow := p.Str("flow"); flow != "" {
			o["flow"] = flow
		}
		switch p.Int("tls") {
		case 1:
			o["tls"] = tls("tls_settings.server_name", "tls_settings.allow_insecure")
		case 2:
			o["tls"] = reality()
		}
		transport()
	case "trojan":
		o["type"], o["password"] = "trojan", s.Password
		if p.Int("tls") == 2 {
			o["tls"] = reality()
		} else {
			o["tls"] = tlsWith(p.TrojanSNI(), p.TrojanInsecure())
		}
		transport()
	case "shadowsocks":
		o["type"], o["method"], o["password"] = "shadowsocks", p.Str("cipher"), s.Password
		if plugin := p.Str("plugin"); plugin != "" {
			o["plugin"], o["plugin_opts"] = plugin, p.Str("plugin_opts")
		}
	case "hysteria":
		if p.Int("version") == 1 {
			return nil
		}
		o["type"], o["password"] = "hysteria2", s.Password
		o["tls"] = tls("tls.server_name", "tls.allow_insecure")
		if p.Bool("obfs.open") {
			o["obfs"] = map[string]any{"type": firstNonEmpty(p.Str("obfs.type"), "salamander"), "password": p.Str("obfs.password")}
		}
	case "tuic":
		o["type"], o["uuid"], o["password"] = "tuic", s.Password, s.Password
		o["congestion_control"] = firstNonEmpty(p.Str("congestion_control"), "bbr")
		t := tls("tls.server_name", "tls.allow_insecure")
		t["alpn"] = []string{"h3"}
		o["tls"] = t
	case "anytls":
		o["type"], o["password"] = "anytls", s.Password
		o["tls"] = tls("tls.server_name", "tls.allow_insecure")
	default:
		return nil
	}
	return o
}

func splitNonEmpty(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cut(s, sep string) (string, string, bool) { return strings.Cut(s, sep) }
