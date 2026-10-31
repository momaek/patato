package uniproxy

import (
	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/store/db"
)

// BuildConfig 生成节点后端拉取的配置，字段与 Xboard 的 ServerService::buildNodeConfig 一致。
func BuildConfig(n db.Node) map[string]any {
	s := proto.Parse(n.Settings)
	base := map[string]any{
		"protocol":        n.Protocol,
		"listen_ip":       "0.0.0.0",
		"server_port":     int(n.ServerPort),
		"network":         nullable(s.Get("network")),
		"networkSettings": nullableMap(s.Get("network_settings")),
	}
	merge := func(extra map[string]any) map[string]any {
		for k, v := range extra {
			base[k] = v
		}
		return base
	}
	tlsSettings := func() any {
		if s.Int("tls") == 2 {
			return s.Get("reality_settings")
		}
		return s.Get("tls_settings")
	}

	var out map[string]any
	switch n.Protocol {
	case "shadowsocks":
		var serverKey any
		if proto.SS2022KeySize(s.Str("cipher")) > 0 {
			serverKey = s.Str("server_key")
		}
		out = merge(map[string]any{
			"cipher":      s.Get("cipher"),
			"plugin":      s.Get("plugin"),
			"plugin_opts": s.Get("plugin_opts"),
			"server_key":  serverKey,
		})
	case "vmess":
		out = merge(map[string]any{
			"tls":          s.Int("tls"),
			"tls_settings": s.Get("tls_settings"),
			"multiplex":    s.Get("multiplex"),
		})
	case "trojan":
		out = merge(map[string]any{
			"host":         n.Address,
			"server_name":  s.Get("tls_settings.server_name"),
			"multiplex":    s.Get("multiplex"),
			"tls":          s.Int("tls"),
			"tls_settings": tlsSettings(),
		})
	case "vless":
		var decryption any
		if s.Bool("encryption.enabled") {
			decryption = s.Get("encryption.decryption")
		}
		out = merge(map[string]any{
			"tls":          s.Int("tls"),
			"flow":         s.Get("flow"),
			"decryption":   decryption,
			"tls_settings": tlsSettings(),
			"multiplex":    s.Get("multiplex"),
		})
	case "hysteria":
		extra := map[string]any{
			"version":      s.Int("version"),
			"host":         n.Address,
			"server_name":  s.Get("tls.server_name"),
			"tls_settings": s.Get("tls"),
			"up_mbps":      s.Int("bandwidth.up"),
			"down_mbps":    s.Int("bandwidth.down"),
		}
		switch s.Int("version") {
		case 1:
			extra["obfs"] = s.Get("obfs.password")
		case 2:
			if s.Bool("obfs.open") {
				extra["obfs"] = s.Get("obfs.type")
			} else {
				extra["obfs"] = nil
			}
			extra["obfs-password"] = s.Get("obfs.password")
		}
		out = merge(extra)
	case "tuic":
		out = merge(map[string]any{
			"version":            s.Int("version"),
			"server_name":        s.Get("tls.server_name"),
			"congestion_control": s.Get("congestion_control"),
			"tls_settings":       s.Get("tls"),
			"auth_timeout":       "3s",
			"zero_rtt_handshake": false,
			"heartbeat":          "3s",
		})
	case "anytls":
		out = merge(map[string]any{
			"server_name":    s.Get("tls.server_name"),
			"tls_settings":   s.Get("tls"),
			"padding_scheme": s.Get("padding_scheme"),
		})
	default:
		out = base
	}
	for _, k := range []string{"routes", "custom_outbounds", "custom_routes"} {
		if v := s.Get(k); v != nil {
			out[k] = v
		}
	}
	if cc := s.Map("cert_config"); cc != nil {
		if mode, ok := cc["mode"]; ok {
			if _, has := cc["cert_mode"]; !has {
				cc["cert_mode"] = mode
				delete(cc, "mode")
			}
		}
		if cc["cert_mode"] != "none" {
			out["cert_config"] = cc
		}
	}
	return out
}

func nullable(v any) any {
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	return v
}

func nullableMap(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return nil
	}
	return v
}
