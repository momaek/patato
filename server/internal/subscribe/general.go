package subscribe

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// General 生成通用 base64 订阅（v2rayN、Shadowrocket、Passwall 等），链接格式与 Xboard 的 General 一致。
func General(servers []Server) string {
	var sb strings.Builder
	for _, s := range servers {
		var link string
		switch s.Type {
		case "vmess":
			link = vmessLink(s)
		case "vless":
			link = vlessLink(s)
		case "trojan":
			link = trojanLink(s)
		case "shadowsocks":
			link = ssLink(s)
		case "hysteria":
			link = hysteriaLink(s)
		case "tuic":
			link = tuicLink(s)
		case "anytls":
			link = anytlsLink(s)
		}
		if link != "" {
			sb.WriteString(link)
			sb.WriteString("\r\n")
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(sb.String()))
}

func hostPort(s Server) string {
	h := s.Host
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h + ":" + strconv.Itoa(s.Port)
}

// query 按键名排序编码，保证输出稳定；值为空的键省略。
func query(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(m[k]))
	}
	return strings.Join(parts, "&")
}

func vmessLink(s Server) string {
	p := s.S
	cfg := map[string]string{
		"v": "2", "ps": s.Name, "add": s.Host, "port": strconv.Itoa(s.Port), "id": s.Password, "aid": "0",
		"net": p.Str("network"), "type": "none", "host": "", "path": "", "tls": "",
	}
	if p.Int("tls") > 0 {
		cfg["tls"] = "tls"
	}
	if sni := p.Str("tls_settings.server_name"); sni != "" {
		cfg["sni"] = sni
	}
	if fp := p.Fingerprint(); fp != "" {
		cfg["fp"] = fp
	}
	switch p.Str("network") {
	case "tcp":
		if t := p.Str("network_settings.header.type"); t != "" && t != "none" {
			cfg["type"] = t
			if paths := p.Strings("network_settings.header.request.path"); len(paths) > 0 {
				cfg["path"] = paths[0]
			}
			if hosts := p.Strings("network_settings.header.request.headers.Host"); len(hosts) > 0 {
				cfg["host"] = hosts[0]
			}
		}
	case "ws":
		cfg["type"] = "ws"
		cfg["path"] = p.Str("network_settings.path")
		cfg["host"] = p.Str("network_settings.headers.Host")
	case "grpc":
		cfg["type"] = "grpc"
		cfg["path"] = p.Str("network_settings.serviceName")
	case "httpupgrade", "xhttp":
		cfg["type"] = p.Str("network")
		cfg["path"] = p.Str("network_settings.path")
		cfg["host"] = firstNonEmpty(p.Str("network_settings.host"), s.Host)
	}
	b, _ := json.Marshal(cfg)
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func transportQuery(s Server, q map[string]string) {
	p := s.S
	switch p.Str("network") {
	case "ws":
		q["path"] = p.Str("network_settings.path")
		q["host"] = p.Str("network_settings.headers.Host")
	case "grpc":
		q["serviceName"] = p.Str("network_settings.serviceName")
		q["mode"] = "multi"
	case "h2":
		q["type"] = "http"
		q["path"] = p.Str("network_settings.path")
		q["host"] = strings.Join(p.Strings("network_settings.host"), ",")
	case "httpupgrade", "xhttp":
		q["path"] = p.Str("network_settings.path")
		q["host"] = firstNonEmpty(p.Str("network_settings.host"), s.Host)
		if p.Str("network") == "xhttp" {
			q["mode"] = firstNonEmpty(p.Str("network_settings.mode"), "auto")
		}
	}
}

func vlessLink(s Server) string {
	p := s.S
	q := map[string]string{"type": p.Str("network"), "flow": p.Str("flow"), "encryption": "none"}
	if p.Bool("encryption.enabled") {
		q["encryption"] = firstNonEmpty(p.Str("encryption.encryption"), "none")
	}
	switch p.Int("tls") {
	case 1:
		q["security"] = "tls"
		q["sni"] = p.Str("tls_settings.server_name")
		q["fp"] = p.Fingerprint()
		if p.Bool("tls_settings.allow_insecure") {
			q["allowInsecure"] = "1"
		}
	case 2:
		q["security"] = "reality"
		q["pbk"] = p.Str("reality_settings.public_key")
		q["sid"] = p.Str("reality_settings.short_id")
		q["sni"] = p.Str("reality_settings.server_name")
		q["spx"] = "/"
		q["fp"] = firstNonEmpty(p.Fingerprint(), "chrome")
	}
	transportQuery(s, q)
	return fmt.Sprintf("vless://%s@%s?%s#%s", s.Password, hostPort(s), query(q), url.PathEscape(s.Name))
}

func trojanLink(s Server) string {
	p := s.S
	q := map[string]string{}
	if p.Int("tls") == 2 {
		q["security"] = "reality"
		q["pbk"] = p.Str("reality_settings.public_key")
		q["sid"] = p.Str("reality_settings.short_id")
		q["sni"] = p.Str("reality_settings.server_name")
		q["fp"] = firstNonEmpty(p.Fingerprint(), "chrome")
	} else {
		if p.TrojanInsecure() {
			q["allowInsecure"] = "1"
		}
		if sni := p.TrojanSNI(); sni != "" {
			q["sni"], q["peer"] = sni, sni
		}
		q["fp"] = p.Fingerprint()
	}
	if n := p.Str("network"); n != "" && n != "tcp" {
		q["type"] = n
	}
	transportQuery(s, q)
	return fmt.Sprintf("trojan://%s@%s?%s#%s", url.PathEscape(s.Password), hostPort(s), query(q), url.PathEscape(s.Name))
}

func ssLink(s Server) string {
	p := s.S
	userinfo := base64.RawURLEncoding.EncodeToString([]byte(p.Str("cipher") + ":" + s.Password))
	link := fmt.Sprintf("ss://%s@%s", userinfo, hostPort(s))
	if plugin, opts := p.Str("plugin"), p.Str("plugin_opts"); plugin != "" && opts != "" {
		link += "/?plugin=" + url.QueryEscape(plugin+";"+opts)
	}
	return link + "#" + url.PathEscape(s.Name)
}

func hysteriaLink(s Server) string {
	p := s.S
	if p.Int("version") == 1 {
		return ""
	}
	q := map[string]string{"sni": p.Str("tls.server_name")}
	if p.Bool("tls.allow_insecure") {
		q["insecure"] = "1"
	}
	if p.Bool("obfs.open") {
		q["obfs"] = firstNonEmpty(p.Str("obfs.type"), "salamander")
		q["obfs-password"] = p.Str("obfs.password")
	}
	return fmt.Sprintf("hysteria2://%s@%s?%s#%s", url.PathEscape(s.Password), hostPort(s), query(q), url.PathEscape(s.Name))
}

func tuicLink(s Server) string {
	p := s.S
	q := map[string]string{
		"sni":                p.Str("tls.server_name"),
		"congestion_control": firstNonEmpty(p.Str("congestion_control"), "bbr"),
		"alpn":               "h3",
		"udp_relay_mode":     "native",
	}
	if p.Bool("tls.allow_insecure") {
		q["allow_insecure"] = "1"
	}
	return fmt.Sprintf("tuic://%s:%s@%s?%s#%s", s.Password, s.Password, hostPort(s), query(q), url.PathEscape(s.Name))
}

func anytlsLink(s Server) string {
	p := s.S
	q := map[string]string{"sni": p.Str("tls.server_name")}
	if p.Bool("tls.allow_insecure") {
		q["insecure"] = "1"
	}
	return fmt.Sprintf("anytls://%s@%s?%s#%s", url.PathEscape(s.Password), hostPort(s), query(q), url.PathEscape(s.Name))
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
