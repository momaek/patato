package proto

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestXboardServerKeyMatchesPHP(t *testing.T) {
	// PHP: base64_encode(substr(md5($timestamp), 0, $length))
	ts := int64(1735689600)
	h := md5.Sum([]byte("1735689600"))
	want := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(h[:])[:16]))
	if got := XboardServerKey(ts, 16); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestUserPasswordSS2022(t *testing.T) {
	s := Parse(json.RawMessage(`{"cipher":"2022-blake3-aes-128-gcm","server_key":"c2VydmVy"}`))
	uuid := "0f7c3a8e-1111-4222-8333-944455556666"
	want := "c2VydmVy:" + base64.StdEncoding.EncodeToString([]byte(uuid[:16]))
	if got := UserPassword("shadowsocks", s, uuid); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	plain := Parse(json.RawMessage(`{"cipher":"aes-128-gcm"}`))
	if UserPassword("shadowsocks", plain, uuid) != uuid {
		t.Fatal("non-2022 ciphers use the uuid")
	}
	if UserPassword("vless", s, uuid) != uuid {
		t.Fatal("vless uses the uuid")
	}
}

func TestSettingsAccessors(t *testing.T) {
	s := Parse(json.RawMessage(`{"tls":"2","network_settings":{"headers":{"Host":"a.com"}},"utls":{"enabled":true}}`))
	if s.Int("tls") != 2 || s.Str("network_settings.headers.Host") != "a.com" || s.Fingerprint() != "chrome" {
		t.Fatalf("unexpected accessors: %v %v %v", s.Int("tls"), s.Str("network_settings.headers.Host"), s.Fingerprint())
	}
	if Normalize("V2ray") != "vmess" || Normalize("hysteria2") != "hysteria" {
		t.Fatal("aliases")
	}
}
