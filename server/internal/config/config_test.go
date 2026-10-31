package config

import (
	"strings"
	"testing"
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
