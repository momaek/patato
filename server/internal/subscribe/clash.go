package subscribe

import (
	"gopkg.in/yaml.v3"
)

const (
	groupSelect = "节点选择"
	groupAuto   = "自动选择"
)

// Clash 生成 Clash Meta（mihomo）配置，适用于 Clash Verge、FlClash、Clash Meta for Android 等。
// r 为 nil 时用内置的两个策略组和简单规则。
func Clash(servers []Server, r *Rules) ([]byte, error) {
	proxies := make([]map[string]any, 0, len(servers))
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		p := clashProxy(s)
		if p == nil {
			continue
		}
		proxies = append(proxies, p)
		names = append(names, s.Name)
	}
	selectList := append([]string{groupAuto}, names...)
	selectList = append(selectList, "DIRECT")
	autoList := names
	if len(autoList) == 0 {
		autoList = []string{"DIRECT"}
	}
	cfg := yamlMap{
		{"mixed-port", 7890},
		{"allow-lan", false},
		{"mode", "rule"},
		{"log-level", "info"},
		{"unified-delay", true},
		{"proxies", proxies},
		{"proxy-groups", []yamlMap{
			{{"name", groupSelect}, {"type", "select"}, {"proxies", selectList}},
			{{"name", groupAuto}, {"type", "url-test"}, {"proxies", autoList},
				{"url", "https://www.gstatic.com/generate_204"}, {"interval", 300}, {"tolerance", 50}},
		}},
		{"rules", []string{
			"GEOIP,LAN,DIRECT,no-resolve",
			"GEOSITE,CN,DIRECT",
			"GEOIP,CN,DIRECT",
			"MATCH," + groupSelect,
		}},
	}
	if r != nil {
		cfg = clashRuled(cfg, names, r)
	}
	return yaml.Marshal(cfg)
}

func clashProxy(s Server) map[string]any {
	p := s.S
	base := map[string]any{"name": s.Name, "server": s.Host, "port": s.Port, "udp": true}
	transport := func() {
		switch network := p.Str("network"); network {
		case "ws":
			base["network"] = "ws"
			opts := map[string]any{"path": firstNonEmpty(p.Str("network_settings.path"), "/")}
			if host := p.Str("network_settings.headers.Host"); host != "" {
				opts["headers"] = map[string]any{"Host": host}
			}
			base["ws-opts"] = opts
		case "grpc":
			base["network"] = "grpc"
			base["grpc-opts"] = map[string]any{"grpc-service-name": p.Str("network_settings.serviceName")}
		case "h2":
			base["network"] = "h2"
			base["h2-opts"] = map[string]any{"path": p.Str("network_settings.path"), "host": p.Strings("network_settings.host")}
		case "httpupgrade":
			base["network"] = "ws"
			base["ws-opts"] = map[string]any{"path": p.Str("network_settings.path"), "v2ray-http-upgrade": true,
				"headers": map[string]any{"Host": firstNonEmpty(p.Str("network_settings.host"), s.Host)}}
		}
	}
	reality := func() {
		base["reality-opts"] = map[string]any{
			"public-key": p.Str("reality_settings.public_key"),
			"short-id":   p.Str("reality_settings.short_id"),
		}
		base["servername"] = p.Str("reality_settings.server_name")
		base["client-fingerprint"] = firstNonEmpty(p.Fingerprint(), "chrome")
	}
	switch s.Type {
	case "vmess":
		base["type"] = "vmess"
		base["uuid"] = s.Password
		base["alterId"] = 0
		base["cipher"] = "auto"
		if p.Int("tls") > 0 {
			base["tls"] = true
			base["skip-cert-verify"] = p.Bool("tls_settings.allow_insecure")
			if sni := p.Str("tls_settings.server_name"); sni != "" {
				base["servername"] = sni
			}
		}
		transport()
	case "vless":
		base["type"] = "vless"
		base["uuid"] = s.Password
		if flow := p.Str("flow"); flow != "" {
			base["flow"] = flow
		}
		switch p.Int("tls") {
		case 1:
			base["tls"] = true
			base["skip-cert-verify"] = p.Bool("tls_settings.allow_insecure")
			if sni := p.Str("tls_settings.server_name"); sni != "" {
				base["servername"] = sni
			}
			if fp := p.Fingerprint(); fp != "" {
				base["client-fingerprint"] = fp
			}
		case 2:
			base["tls"] = true
			reality()
		}
		transport()
	case "trojan":
		base["type"] = "trojan"
		base["password"] = s.Password
		if p.Int("tls") == 2 {
			reality()
			base["sni"] = p.Str("reality_settings.server_name")
		} else {
			base["skip-cert-verify"] = p.TrojanInsecure()
			if sni := p.TrojanSNI(); sni != "" {
				base["sni"] = sni
			}
		}
		transport()
	case "shadowsocks":
		base["type"] = "ss"
		base["cipher"] = p.Str("cipher")
		base["password"] = s.Password
		if plugin := p.Str("plugin"); plugin == "obfs" || plugin == "simple-obfs" {
			base["plugin"] = "obfs"
			base["plugin-opts"] = parsePluginOpts(p.Str("plugin_opts"))
		}
	case "hysteria":
		if p.Int("version") == 1 {
			return nil
		}
		base["type"] = "hysteria2"
		base["password"] = s.Password
		base["sni"] = p.Str("tls.server_name")
		base["skip-cert-verify"] = p.Bool("tls.allow_insecure")
		if p.Bool("obfs.open") {
			base["obfs"] = firstNonEmpty(p.Str("obfs.type"), "salamander")
			base["obfs-password"] = p.Str("obfs.password")
		}
	case "tuic":
		base["type"] = "tuic"
		base["uuid"] = s.Password
		base["password"] = s.Password
		base["sni"] = p.Str("tls.server_name")
		base["skip-cert-verify"] = p.Bool("tls.allow_insecure")
		base["congestion-controller"] = firstNonEmpty(p.Str("congestion_control"), "bbr")
		base["alpn"] = []string{"h3"}
		base["udp-relay-mode"] = "native"
	case "anytls":
		base["type"] = "anytls"
		base["password"] = s.Password
		base["sni"] = p.Str("tls.server_name")
		base["skip-cert-verify"] = p.Bool("tls.allow_insecure")
	default:
		return nil
	}
	return base
}

func parsePluginOpts(s string) map[string]any {
	out := map[string]any{}
	for _, kv := range splitNonEmpty(s, ";") {
		k, v, _ := cut(kv, "=")
		switch k {
		case "obfs":
			out["mode"] = v
		case "obfs-host":
			out["host"] = v
		default:
			out[k] = v
		}
	}
	return out
}

// yamlMap 是有序的 YAML 映射，保证生成的配置字段顺序稳定、便于阅读。
type yamlMap []struct {
	K string
	V any
}

func (m yamlMap) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, kv := range m {
		var v yaml.Node
		if err := v.Encode(kv.V); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: kv.K}, &v)
	}
	return n, nil
}
