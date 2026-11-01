package subscribe

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/momaek/patato/server/internal/proto"
)

func TestDetectNewFormats(t *testing.T) {
	cases := map[string]Format{
		"Surge iOS/2920":          FormatSurge,
		"Surfboard/2.24.0":        FormatSurge,
		"Quantumult%20X/1.4.1":    FormatQuanX,
		"Loon/3.2.1 CFNetwork/14": FormatLoon,
	}
	for ua, want := range cases {
		if got := DetectFormat("", ua); got != want {
			t.Errorf("%s: got %s want %s", ua, got, want)
		}
	}
	for flag, want := range map[string]Format{"surge": FormatSurge, "quanx": FormatQuanX, "loon": FormatLoon} {
		if got := DetectFormat(flag, "curl/8"); got != want {
			t.Errorf("flag %s: got %s", flag, got)
		}
	}
}

// 补充 Xboard 结构的 Trojan（server_name、allow_insecure 在顶层）和经典加密的 Shadowsocks。
func moreServers() []Server {
	trojan := proto.Parse(json.RawMessage(`{"network":"tcp","server_name":"tj.example.com","allow_insecure":false}`))
	ss := proto.Parse(json.RawMessage(`{"cipher":"aes-128-gcm"}`))
	return append(servers(),
		Server{Name: "大阪-01", Type: "trojan", Host: "7.7.7.7", Port: 443, Password: "pw-trojan", S: trojan},
		Server{Name: "首尔-01", Type: "shadowsocks", Host: "8.8.8.8", Port: 8389, Password: "pw-ss", S: ss},
	)
}

// Surge 与 Xboard 一致：不下发 VLESS、Shadowsocks 2022、带混淆的 Hysteria2。
func TestSurge(t *testing.T) {
	out := Surge(moreServers(), "https://sub.example.com/s/abc", nil)
	if !strings.HasPrefix(out, "#!MANAGED-CONFIG https://sub.example.com/s/abc ") {
		t.Fatalf("missing managed header:\n%s", out)
	}
	for _, want := range []string{
		"香港-01 = vmess, 5.6.7.8, 8443, username=0f7c3a8e-1111-4222-8333-944455556666, vmess-aead=true, udp-relay=true, tls=true, skip-cert-verify=false, sni=cdn.example.com, ws=true, ws-path=/ray, ws-headers=Host:cdn.example.com",
		"大阪-01 = trojan, 7.7.7.7, 443, password=pw-trojan, udp-relay=true, skip-cert-verify=false, sni=tj.example.com",
		"首尔-01 = ss, 8.8.8.8, 8389, encrypt-method=aes-128-gcm, password=pw-ss, udp-relay=true",
		"节点选择 = select, 自动选择, 香港-01, 大阪-01, 首尔-01, DIRECT",
		"FINAL,节点选择,dns-failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, skipped := range []string{"东京-01 =", "洛杉矶-01 =", "新加坡-01 =", "DIRECT = direct"} {
		if strings.Contains(out, skipped) {
			t.Errorf("must not contain %q:\n%s", skipped, out)
		}
	}
}

func TestQuantumultX(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(QuantumultX(moreServers()))
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"vless=1.2.3.4:443, method=none, password=0f7c3a8e-1111-4222-8333-944455556666, obfs=over-tls, obfs-host=www.apple.com, reality-base64-pubkey=",
		"vmess=5.6.7.8:8443, method=chacha20-poly1305, password=0f7c3a8e-1111-4222-8333-944455556666, obfs=wss, obfs-uri=/ray, tls-verification=true, obfs-host=cdn.example.com",
		"shadowsocks=9.9.9.9:8388, method=2022-blake3-aes-128-gcm",
		"trojan=7.7.7.7:443, password=pw-trojan, over-tls=true, tls-host=tj.example.com, tls-verification=true, fast-open=false, udp-relay=true, tag=大阪-01",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "洛杉矶-01") {
		t.Errorf("Quantumult X has no Hysteria2:\n%s", out)
	}
}

func TestLoon(t *testing.T) {
	out := Loon(moreServers())
	for _, want := range []string{
		"香港-01 = vmess,5.6.7.8,8443,auto,0f7c3a8e-1111-4222-8333-944455556666,fast-open=false,udp=true,alterId=0,skip-cert-verify=false,tls-name=cdn.example.com,transport=ws,path=/ray,host=cdn.example.com",
		"大阪-01 = trojan,7.7.7.7,443,pw-trojan,tls-name=tj.example.com,fast-open=false,udp=true,skip-cert-verify=false",
		"首尔-01 = Shadowsocks,8.8.8.8,8389,aes-128-gcm,pw-ss,fast-open=false,udp=true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, skipped := range []string{"东京-01", "洛杉矶-01", "新加坡-01"} {
		if strings.Contains(out, skipped) {
			t.Errorf("Loon must skip %s:\n%s", skipped, out)
		}
	}
}

// Xboard 结构的 Trojan 把 SNI 放在顶层：每种格式都要带上。
func TestTrojanTopLevelSNI(t *testing.T) {
	tj := []Server{moreServers()[4]}
	if g, _ := base64.StdEncoding.DecodeString(General(tj)); !strings.Contains(string(g), "sni=tj.example.com") {
		t.Errorf("general: %s", g)
	}
	if c, _ := Clash(tj, nil); !strings.Contains(string(c), "sni: tj.example.com") {
		t.Errorf("clash: %s", c)
	}
	if s, _ := SingBox(tj, nil); !strings.Contains(string(s), `"server_name": "tj.example.com"`) {
		t.Errorf("sing-box: %s", s)
	}
}

func TestByRegion(t *testing.T) {
	ss := []Server{{Name: "a", Region: "东京"}, {Name: "b", Region: "香港"}, {Name: "c", Region: "新加坡"}}
	got := byRegion(ss, "东京|香港")
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("region filter: %+v", got)
	}
	if len(byRegion(ss, "")) != 3 {
		t.Fatal("empty region keeps all")
	}
}
