package kernel

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/momaek/patato/nodeapi"
)

// buildInbound 把节点配置换成 sing-box 的 inbound JSON。用户名用用户 ID，追踪器据此找到用户。
func buildInbound(n nodeapi.Node, users []nodeapi.User, certs *certStore, acmeDir string) ([]byte, *time.Time, error) {
	in := map[string]any{
		"type":        n.Protocol,
		"tag":         tag(n.ID),
		"listen":      firstNonEmpty(n.Listen, "::"),
		"listen_port": n.Port,
	}
	sorted := sortedUsers(users)
	name := func(u nodeapi.User) string { return strconv.FormatInt(u.ID, 10) }
	list := func(f func(u nodeapi.User) map[string]any) []map[string]any {
		out := make([]map[string]any, 0, len(sorted))
		for _, u := range sorted {
			out = append(out, f(u))
		}
		return out
	}
	needTLS := false
	switch n.Protocol {
	case "vless":
		in["users"] = list(func(u nodeapi.User) map[string]any {
			m := map[string]any{"name": name(u), "uuid": u.UUID}
			if n.Flow != "" {
				m["flow"] = n.Flow
			}
			return m
		})
	case "vmess":
		in["users"] = list(func(u nodeapi.User) map[string]any {
			return map[string]any{"name": name(u), "uuid": u.UUID, "alterId": 0}
		})
	case "trojan":
		needTLS = true
		in["users"] = list(func(u nodeapi.User) map[string]any {
			return map[string]any{"name": name(u), "password": u.Password}
		})
	case "shadowsocks":
		// managed 模式的 inbound 支持原地更新用户，用户在创建后通过 UpdateUsers 设置
		in["method"] = n.Cipher
		in["managed"] = true
		if n.ServerKey != "" {
			in["password"] = n.ServerKey
		}
	case "hysteria2":
		needTLS = true
		in["users"] = list(func(u nodeapi.User) map[string]any {
			return map[string]any{"name": name(u), "password": u.Password}
		})
		if n.UpMbps > 0 {
			in["up_mbps"] = n.UpMbps
		}
		if n.DownMbps > 0 {
			in["down_mbps"] = n.DownMbps
		}
		if n.Obfs != "" {
			in["obfs"] = map[string]any{"type": n.Obfs, "password": n.ObfsPass}
		}
	case "tuic":
		needTLS = true
		in["users"] = list(func(u nodeapi.User) map[string]any {
			return map[string]any{"name": name(u), "uuid": u.UUID, "password": u.Password}
		})
		in["congestion_control"] = firstNonEmpty(n.Congestion, "bbr")
		in["auth_timeout"] = "3s"
		in["heartbeat"] = "10s"
	case "anytls":
		needTLS = true
		in["users"] = list(func(u nodeapi.User) map[string]any {
			return map[string]any{"name": name(u), "password": u.Password}
		})
		if len(n.Padding) > 0 {
			in["padding_scheme"] = n.Padding
		}
	default:
		return nil, nil, fmt.Errorf("不支持的协议 %s", n.Protocol)
	}
	if needTLS && n.TLS == nil {
		n.TLS = &nodeapi.TLS{Mode: "tls"}
	}
	var expiry *time.Time
	if n.TLS != nil {
		t, exp, err := buildTLS(n, certs, acmeDir)
		if err != nil {
			return nil, nil, err
		}
		in["tls"], expiry = t, exp
	}
	if n.Transport != nil {
		t, err := buildTransport(*n.Transport)
		if err != nil {
			return nil, nil, err
		}
		in["transport"] = t
	}
	if n.Multiplex {
		in["multiplex"] = map[string]any{"enabled": true}
	}
	raw, err := json.Marshal(in)
	return raw, expiry, err
}

func buildTLS(n nodeapi.Node, certs *certStore, acmeDir string) (map[string]any, *time.Time, error) {
	t := n.TLS
	out := map[string]any{"enabled": true}
	if t.ServerName != "" {
		out["server_name"] = t.ServerName
	}
	if len(t.ALPN) > 0 {
		out["alpn"] = t.ALPN
	}
	if t.Mode == "reality" {
		if t.PrivateKey == "" || t.HandshakeServer == "" {
			return nil, nil, fmt.Errorf("Reality 缺少私钥或握手目标")
		}
		out["reality"] = map[string]any{
			"enabled":     true,
			"handshake":   map[string]any{"server": t.HandshakeServer, "server_port": max(t.HandshakePort, 1)},
			"private_key": t.PrivateKey,
			"short_id":    t.ShortIDs,
		}
		return out, nil, nil
	}
	c := t.Cert
	if c == nil {
		c = &nodeapi.Cert{Mode: "self"}
	}
	domain := firstNonEmpty(c.Domain, t.ServerName)
	switch c.Mode {
	case "pem":
		if c.CertPEM == "" || c.KeyPEM == "" {
			return nil, nil, fmt.Errorf("面板下发的证书为空")
		}
		out["certificate"] = []string{c.CertPEM}
		out["key"] = []string{c.KeyPEM}
		return out, certExpiry([]byte(c.CertPEM)), nil
	case "file":
		if c.CertFile == "" || c.KeyFile == "" {
			return nil, nil, fmt.Errorf("证书文件路径为空")
		}
		out["certificate_path"], out["key_path"] = c.CertFile, c.KeyFile
		return out, fileExpiry(c.CertFile), nil
	case "acme":
		if domain == "" {
			return nil, nil, fmt.Errorf("用 ACME 申请证书需要域名")
		}
		acme := map[string]any{"domain": []string{domain}, "data_directory": acmeDir}
		if c.Email != "" {
			acme["email"] = c.Email
		}
		if c.DNSProvider != "" {
			ch := map[string]any{"provider": c.DNSProvider}
			for k, v := range c.DNSCredentials {
				ch[k] = v
			}
			acme["dns01_challenge"] = ch
		}
		out["acme"] = acme
		return out, nil, nil
	default:
		certPEM, keyPEM, exp, err := certs.selfSigned(firstNonEmpty(domain, "localhost"))
		if err != nil {
			return nil, nil, err
		}
		out["certificate"] = []string{certPEM}
		out["key"] = []string{keyPEM}
		return out, exp, nil
	}
}

func buildTransport(t nodeapi.Transport) (map[string]any, error) {
	out := map[string]any{"type": t.Type}
	switch t.Type {
	case "ws":
		out["path"] = firstNonEmpty(t.Path, "/")
		if len(t.Headers) > 0 {
			out["headers"] = t.Headers
		}
	case "grpc":
		out["service_name"] = t.ServiceName
	case "http":
		if t.Path != "" {
			out["path"] = t.Path
		}
		if len(t.Host) > 0 {
			out["host"] = t.Host
		}
	case "httpupgrade":
		if t.Path != "" {
			out["path"] = t.Path
		}
		if len(t.Host) > 0 {
			out["host"] = t.Host[0]
		}
	default:
		return nil, fmt.Errorf("不支持的传输方式 %s", t.Type)
	}
	return out, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
