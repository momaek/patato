package native

import (
	"testing"

	"github.com/momaek/patato/server/internal/store/db"
)

func TestBuildNode(t *testing.T) {
	reality, err := BuildNode(db.Node{ID: 1, Protocol: "vless", ServerPort: 443, Settings: []byte(`{"tls":2,"flow":"xtls-rprx-vision",
		"reality_settings":{"server_name":"www.apple.com","private_key":"priv","short_id":"abcd"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if reality.TLS.Mode != "reality" || reality.TLS.HandshakeServer != "www.apple.com" || reality.TLS.HandshakePort != 443 ||
		reality.TLS.ShortIDs[0] != "abcd" || reality.Flow != "xtls-rprx-vision" || reality.Port != 443 {
		t.Fatalf("reality: %+v %+v", reality, reality.TLS)
	}

	ws, err := BuildNode(db.Node{ID: 2, Protocol: "vmess", Address: "cdn.example.com", ServerPort: 8443, Settings: []byte(`{"tls":1,"network":"ws",
		"network_settings":{"path":"/ray","headers":{"Host":"cdn.example.com"}},
		"cert_config":{"cert_mode":"dns","domain":"cdn.example.com","dns_provider":"cloudflare","dns_env":{"CF_DNS_API_TOKEN":"scoped"}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	c := ws.TLS.Cert
	if ws.Transport.Type != "ws" || ws.Transport.Path != "/ray" || c.Mode != "acme" || c.DNSProvider != "cloudflare" || c.DNSCredentials["api_token"] != "scoped" {
		t.Fatalf("vmess ws: %+v %+v %+v", ws, ws.Transport, c)
	}

	hy, err := BuildNode(db.Node{ID: 3, Protocol: "hysteria", Address: "1.2.3.4", ServerPort: 9443, Settings: []byte(`{"version":2,
		"bandwidth":{"up":100,"down":500},"obfs":{"open":true,"password":"ob"},"tls":{"server_name":"hy.example.com"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if hy.Protocol != "hysteria2" || hy.Obfs != "salamander" || hy.ObfsPass != "ob" || hy.DownMbps != 500 || hy.TLS.Cert.Mode != "self" || hy.TLS.ALPN[0] != "h3" {
		t.Fatalf("hysteria2: %+v %+v", hy, hy.TLS)
	}

	for _, bad := range []db.Node{
		{Protocol: "hysteria", Settings: []byte(`{"version":1}`)},
		{Protocol: "shadowsocks", Settings: []byte(`{"cipher":"aes-128-gcm","plugin":"obfs-local"}`)},
		{Protocol: "shadowsocks", Settings: []byte(`{"cipher":"2022-blake3-aes-128-gcm"}`)},
		{Protocol: "vless", Settings: []byte(`{"encryption":{"enabled":true}}`)},
	} {
		if _, err := BuildNode(bad); err == nil {
			t.Errorf("expected error for %s %s", bad.Protocol, bad.Settings)
		}
	}
}
