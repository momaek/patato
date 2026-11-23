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
		{"https://same.test", "https://SAME.test", "different hosts or ports"},
		{"http://same.test", "https://same.test", "different hosts or ports"},
		{"https://site.test/admin", "", "without a path"},
		{"admin.test", "", "http(s) origin"},
		{"https://admin.test?x=1", "", "without a path"},
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
