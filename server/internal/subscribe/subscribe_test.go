package subscribe

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/momaek/patato/server/internal/proto"
)

func servers() []Server {
	reality := proto.Parse(json.RawMessage(`{"tls":2,"flow":"xtls-rprx-vision","network":"tcp",
		"reality_settings":{"public_key":"mAJqFPTqIyPFOtkot9Yfn6yYA-uT2WQjKeVboSS24yQ","short_id":"abcd","server_name":"www.apple.com"}}`))
	ws := proto.Parse(json.RawMessage(`{"tls":1,"network":"ws","tls_settings":{"server_name":"cdn.example.com"},
		"network_settings":{"path":"/ray","headers":{"Host":"cdn.example.com"}}}`))
	ss := proto.Parse(json.RawMessage(`{"cipher":"2022-blake3-aes-128-gcm","server_key":"MDEyMzQ1Njc4OWFiY2RlZg=="}`))
	hy := proto.Parse(json.RawMessage(`{"version":2,"tls":{"server_name":"hy.example.com"},"obfs":{"open":true,"type":"salamander","password":"ob"}}`))
	uuid := "0f7c3a8e-1111-4222-8333-944455556666"
	return []Server{
		{Name: "东京-01", Type: "vless", Host: "1.2.3.4", Port: 443, Password: uuid, S: reality},
		{Name: "香港-01", Type: "vmess", Host: "5.6.7.8", Port: 8443, Password: uuid, S: ws},
		{Name: "新加坡-01", Type: "shadowsocks", Host: "9.9.9.9", Port: 8388, Password: proto.UserPassword("shadowsocks", ss, uuid), S: ss},
		{Name: "洛杉矶-01", Type: "hysteria", Host: "2001:db8::1", Port: 9443, Password: uuid, S: hy},
	}
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]Format{
		"ClashMetaForAndroid/2.10":     FormatClash,
		"clash-verge/v1.7.7":           FormatClash,
		"mihomo/1.18":                  FormatClash,
		"SFI/1.11.0 (sing-box 1.11.0)": FormatSingBox,
		"Hiddify/2.5":                  FormatSingBox,
		"Shadowrocket/2070":            FormatGeneral,
		"v2rayN/6.42":                  FormatGeneral,
		"curl/8.0":                     FormatGeneral,
	}
	for ua, want := range cases {
		if got := DetectFormat("", ua); got != want {
			t.Errorf("%s: got %s want %s", ua, got, want)
		}
	}
	if DetectFormat("clash", "v2rayN/6.42") != FormatClash {
		t.Error("flag overrides user agent")
	}
}

func TestGeneral(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(General(servers()))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\r\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 links, got %d: %q", len(lines), raw)
	}
	vless, err := url.Parse(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	q := vless.Query()
	if vless.Scheme != "vless" || q.Get("security") != "reality" || q.Get("pbk") != "mAJqFPTqIyPFOtkot9Yfn6yYA-uT2WQjKeVboSS24yQ" || q.Get("flow") != "xtls-rprx-vision" {
		t.Fatalf("bad vless link: %s", lines[0])
	}
	if vless.Fragment != "东京-01" {
		t.Fatalf("name fragment: %q", vless.Fragment)
	}
	if !strings.HasPrefix(lines[1], "vmess://") {
		t.Fatal("vmess")
	}
	var vm map[string]string
	b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(lines[1], "vmess://"))
	if json.Unmarshal(b, &vm) != nil || vm["path"] != "/ray" || vm["tls"] != "tls" || vm["sni"] != "cdn.example.com" {
		t.Fatalf("bad vmess: %s", b)
	}
	if !strings.HasPrefix(lines[2], "ss://") || !strings.HasPrefix(lines[3], "hysteria2://") || !strings.Contains(lines[3], "[2001:db8::1]:9443") {
		t.Fatalf("ss/hy2 links: %v", lines[2:])
	}
}

func TestClashIsValidYAML(t *testing.T) {
	b, err := Clash(servers(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]any `yaml:"proxies"`
		Groups  []map[string]any `yaml:"proxy-groups"`
		Rules   []string         `yaml:"rules"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Proxies) != 4 || len(cfg.Groups) != 2 || cfg.Rules[len(cfg.Rules)-1] != "MATCH,节点选择" {
		t.Fatalf("unexpected clash config:\n%s", b)
	}
	if cfg.Proxies[0]["type"] != "vless" || cfg.Proxies[0]["reality-opts"] == nil || cfg.Proxies[3]["type"] != "hysteria2" {
		t.Fatalf("proxy types: %v", cfg.Proxies)
	}
}

func TestSingBoxIsValidJSON(t *testing.T) {
	b, err := SingBox(servers(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, o := range cfg.Outbounds {
		types[o["type"].(string)] = true
	}
	for _, want := range []string{"selector", "urltest", "vless", "vmess", "shadowsocks", "hysteria2", "direct"} {
		if !types[want] {
			t.Errorf("missing outbound type %s", want)
		}
	}
}

func TestFilter(t *testing.T) {
	got := filter(servers(), "vless|hysteria2", "")
	if len(got) != 2 {
		t.Fatalf("types filter: %d", len(got))
	}
	got = filter(servers(), "", "香港")
	if len(got) != 1 || got[0].Name != "香港-01" {
		t.Fatalf("keyword filter: %v", got)
	}
}
