package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestFrontendOrigins(t *testing.T) {
	for _, tc := range []struct {
		admin, portal, want string
	}{
		{"https://admin.test", "https://my.test", ""},
		{"http://localhost:5173", "http://localhost:5174", ""},
		{"", "", ""},
		{"https://same.test", "https://SAME.test", "share a host"},
		{"http://same.test", "https://same.test", "share a host"},
		{"https://same.test/", "https://same.test", "share a host"},
		// 单域名：Admin 挂在随机路径下
		{"https://same.test/a8f3k2c9", "https://same.test", ""},
		{"https://same.test/a8f3k2c9/", "https://same.test", ""},
		{"https://admin.test/x_y-Z9", "https://my.test", ""},
		{"https://same.test/a/b", "https://same.test", "single segment"},
		{"https://same.test/a%20b", "https://same.test", "single segment"},
		{"https://same.test/a.b", "https://same.test", "single segment"},
		{"https://same.test/api", "https://same.test", "reserved"},
		{"https://same.test/Orders", "https://same.test", "reserved"},
		{"https://same.test/assets", "https://same.test", "reserved"},
		{"https://admin.test", "https://my.test/portal", "without a path"},
		{"admin.test", "", "http(s) URL"},
		{"https://admin.test?x=1", "", "without query"},
	} {
		c := Default()
		c.DatabaseURL, c.PublicURL, c.SecretKey = "postgres://test", "https://api.test", strings.Repeat("k", 32)
		c.AdminURL, c.PortalURL = tc.admin, tc.portal
		err := c.Validate()
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("admin=%q portal=%q: %v", tc.admin, tc.portal, err)
		}
	}
}

func TestPaymentDrivers(t *testing.T) {
	for _, tc := range []struct {
		name, method, want string
	}{
		{"stripe", "driver: stripe\nsecret_key: sk_test_fixture\nwebhook_secret: whsec_fixture", ""},
		{"stripe missing API key", "driver: stripe\nwebhook_secret: whsec_fixture", "secret_key"},
		{"stripe missing signing secret", "driver: stripe\nsecret_key: sk_test_fixture", "webhook_secret"},
		{"epay", "driver: epay\nurl: https://pay.test\npid: '1001'\nkey: epay-fixture", ""},
		{"epay missing key", "driver: epay\nurl: https://pay.test\npid: '1001'", "url, pid and key"},
		{"unknown driver", "driver: unknown", "not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.DatabaseURL, c.PublicURL, c.SecretKey = "postgres://test", "https://api.test", strings.Repeat("k", 32)
			var method PaymentMethod
			if err := yaml.Unmarshal([]byte("code: checkout\n"+tc.method), &method); err != nil {
				t.Fatal(err)
			}
			c.Payment.Methods = []PaymentMethod{method}
			err := c.Validate()
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validation: %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDefaultNodeBinaryURL(t *testing.T) {
	for version, want := range map[string]string{
		"v0.2.0": "https://github.com/momaek/patato/releases/download/v0.2.0/patato-node-linux-{arch}",
		"dev":    "https://github.com/momaek/patato/releases/latest/download/patato-node-linux-{arch}",
		"":       "https://github.com/momaek/patato/releases/latest/download/patato-node-linux-{arch}",
	} {
		if got := DefaultNodeBinaryURL(version); got != want {
			t.Errorf("DefaultNodeBinaryURL(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestAdminOriginAndPath(t *testing.T) {
	for _, tc := range []struct{ url, origin, path string }{
		{"https://admin.test", "https://admin.test", ""},
		{"https://same.test/a8f3k2c9", "https://same.test", "/a8f3k2c9"},
		{"http://localhost:5173/x", "http://localhost:5173", "/x"},
	} {
		c := Default()
		c.AdminURL = tc.url
		c.fillDerived()
		if c.AdminOrigin() != tc.origin || c.AdminPath() != tc.path || c.WebAuthn.Origins[0] != tc.origin {
			t.Errorf("%s: origin=%q path=%q webauthn=%v", tc.url, c.AdminOrigin(), c.AdminPath(), c.WebAuthn.Origins)
		}
	}
}
