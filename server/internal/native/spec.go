// Package native 实现 Patato 原生节点协议的服务端（patato-node 对接的接口），报文定义见 nodeapi。
package native

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/momaek/patato/nodeapi"
	"github.com/momaek/patato/server/internal/proto"
	"github.com/momaek/patato/server/internal/store/db"
)

// BuildNode 把节点的协议设置（沿用 Xboard 的 protocol_settings 结构）换成原生协议的节点配置。
// 返回错误说明这个节点不能由 patato-node 承载，例如用了 sing-box 不支持的选项。
func BuildNode(n db.Node) (nodeapi.Node, error) {
	s := proto.Parse(n.Settings)
	out := nodeapi.Node{
		ID: n.ID, Code: n.Code, Name: n.Name, Protocol: n.Protocol,
		Listen: "::", Port: int(n.ServerPort),
		Multiplex: s.Bool("multiplex.enabled"),
	}
	switch n.Protocol {
	case "vless":
		if s.Bool("encryption.enabled") {
			return out, fmt.Errorf("VLESS encryption 不受 sing-box 支持")
		}
		out.Flow = s.Str("flow")
		switch s.Int("tls") {
		case 1:
			out.TLS = tlsOf(s, "tls_settings", n.Address)
		case 2:
			out.TLS = realityOf(s)
		}
		out.Transport = transportOf(s)
	case "vmess":
		if s.Int("tls") > 0 {
			out.TLS = tlsOf(s, "tls_settings", n.Address)
		}
		out.Transport = transportOf(s)
	case "trojan":
		if s.Int("tls") == 2 {
			out.TLS = realityOf(s)
		} else {
			out.TLS = tlsOf(s, "tls_settings", n.Address)
		}
		out.Transport = transportOf(s)
	case "shadowsocks":
		out.Cipher = s.Str("cipher")
		if out.Cipher == "" {
			return out, fmt.Errorf("缺少 cipher")
		}
		if p := s.Str("plugin"); p != "" {
			return out, fmt.Errorf("Shadowsocks 插件 %s 不受支持", p)
		}
		if proto.SS2022KeySize(out.Cipher) > 0 {
			out.ServerKey = s.Str("server_key")
			if out.ServerKey == "" {
				return out, fmt.Errorf("Shadowsocks 2022 缺少 server_key")
			}
		}
		out.Multiplex = false
	case "hysteria":
		if v := s.Int("version"); v != 2 {
			return out, fmt.Errorf("只支持 Hysteria 2（当前为 v%d）", v)
		}
		out.Protocol = "hysteria2"
		out.TLS = tlsOf(s, "tls", n.Address)
		out.TLS.ALPN = []string{"h3"}
		out.UpMbps, out.DownMbps = s.Int("bandwidth.up"), s.Int("bandwidth.down")
		if s.Bool("obfs.open") {
			out.Obfs = firstNonEmpty(s.Str("obfs.type"), "salamander")
			out.ObfsPass = s.Str("obfs.password")
		}
		out.Multiplex = false
	case "tuic":
		out.TLS = tlsOf(s, "tls", n.Address)
		out.TLS.ALPN = []string{"h3"}
		out.Congestion = firstNonEmpty(s.Str("congestion_control"), "bbr")
		out.Multiplex = false
	case "anytls":
		out.TLS = tlsOf(s, "tls", n.Address)
		out.Padding = s.Strings("padding_scheme")
		out.Multiplex = false
	default:
		return out, fmt.Errorf("协议 %s 不受 patato-node 支持", n.Protocol)
	}
	return out, nil
}

func tlsOf(s proto.Settings, prefix, address string) *nodeapi.TLS {
	t := &nodeapi.TLS{Mode: "tls", ServerName: s.Str(prefix + ".server_name")}
	t.Cert = certOf(s, firstNonEmpty(t.ServerName, address))
	return t
}

func realityOf(s proto.Settings) *nodeapi.TLS {
	t := &nodeapi.TLS{
		Mode:            "reality",
		ServerName:      s.Str("reality_settings.server_name"),
		PrivateKey:      s.Str("reality_settings.private_key"),
		HandshakeServer: firstNonEmpty(s.Str("reality_settings.dest"), s.Str("reality_settings.server_name")),
		HandshakePort:   s.Int("reality_settings.server_port"),
	}
	if t.HandshakePort == 0 {
		t.HandshakePort = 443
	}
	// short_id 允许为空串（客户端不带 short_id 时匹配）
	t.ShortIDs = []string{s.Str("reality_settings.short_id")}
	return t
}

// certOf 读取 cert_config，换成节点取证书的方式。没有配置时用自签名证书。
func certOf(s proto.Settings, domain string) *nodeapi.Cert {
	cc := proto.Settings(s.Map("cert_config"))
	mode := firstNonEmpty(cc.Str("cert_mode"), cc.Str("mode"))
	c := &nodeapi.Cert{
		Domain: firstNonEmpty(cc.Str("domain"), cc.Str("cert_domain"), domain),
		Email:  cc.Str("email"),
	}
	switch mode {
	case "file":
		c.Mode, c.CertFile, c.KeyFile = "file", cc.Str("cert_file"), cc.Str("key_file")
	case "pem":
		c.Mode, c.CertPEM, c.KeyPEM = "pem", cc.Str("cert_pem"), cc.Str("key_pem")
	case "http", "acme":
		c.Mode = "acme"
	case "dns":
		c.Mode = "acme"
		c.DNSProvider, c.DNSCredentials = dnsCredentials(cc)
	default:
		c.Mode = "self"
	}
	return c
}

// dnsCredentials 把 V2bX 风格的 DNS 服务商环境变量换成 sing-box 的字段。只应在这里放权限受限的子 token。
func dnsCredentials(cc proto.Settings) (string, map[string]string) {
	provider := strings.ToLower(firstNonEmpty(cc.Str("dns_provider"), cc.Str("provider")))
	env := map[string]string{}
	for _, key := range []string{"dns_env", "env"} {
		for k, v := range cc.Map(key) {
			if sv, ok := v.(string); ok {
				env[strings.ToUpper(k)] = sv
			}
		}
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v := env[k]; v != "" {
				return v
			}
		}
		return ""
	}
	switch provider {
	case "cloudflare", "cf":
		return "cloudflare", map[string]string{"api_token": pick("CF_DNS_API_TOKEN", "CLOUDFLARE_DNS_API_TOKEN", "CF_API_TOKEN")}
	case "alidns", "aliyun":
		return "alidns", map[string]string{
			"access_key_id":     pick("ALICLOUD_ACCESS_KEY", "ALIDNS_ACCESS_KEY_ID"),
			"access_key_secret": pick("ALICLOUD_SECRET_KEY", "ALIDNS_ACCESS_KEY_SECRET"),
		}
	}
	return provider, nil
}

func transportOf(s proto.Settings) *nodeapi.Transport {
	switch s.Str("network") {
	case "ws":
		t := &nodeapi.Transport{Type: "ws", Path: firstNonEmpty(s.Str("network_settings.path"), "/")}
		if host := s.Str("network_settings.headers.Host"); host != "" {
			t.Headers = map[string]string{"Host": host}
		}
		return t
	case "grpc":
		return &nodeapi.Transport{Type: "grpc", ServiceName: s.Str("network_settings.serviceName")}
	case "h2":
		return &nodeapi.Transport{Type: "http", Path: s.Str("network_settings.path"), Host: s.Strings("network_settings.host")}
	case "httpupgrade":
		return &nodeapi.Transport{Type: "httpupgrade", Path: s.Str("network_settings.path"), Host: s.Strings("network_settings.host")}
	}
	return nil
}

// UserCredential 返回用户在节点上的凭证：Shadowsocks 2022 是用户密钥（UUID 前 n 个字符做 base64，
// 与订阅里的「服务端密钥:用户密钥」对应），其余协议直接用 UUID。
func UserCredential(n nodeapi.Node, uuid string) string {
	if n.Protocol != "shadowsocks" {
		return uuid
	}
	size := proto.SS2022KeySize(n.Cipher)
	if size == 0 {
		return uuid
	}
	u := uuid
	if len(u) > size {
		u = u[:size]
	}
	return base64.StdEncoding.EncodeToString([]byte(u))
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
