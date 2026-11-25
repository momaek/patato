package main

import "testing"

func TestVerificationLink(t *testing.T) {
	for _, tc := range []struct {
		g     grant
		admin string
		want  string
	}{
		// 独立 Admin 域名：服务端给完整链接
		{grant{VerificationURL: "https://admin.test/cli-auth?code=AB", VerificationPath: "/cli-auth?code=AB"}, "", "https://admin.test/cli-auth?code=AB"},
		// Admin 挂在隐藏路径下：服务端只给路径，用 --admin-url 补全
		{grant{VerificationPath: "/cli-auth?code=AB"}, "https://site.test/a8f3k2c9/", "https://site.test/a8f3k2c9/cli-auth?code=AB"},
		{grant{VerificationPath: "/cli-auth?code=AB"}, "", ""},
		// 旧服务端没有 verification_path
		{grant{VerificationURL: "https://admin.test/cli-auth?code=AB"}, "https://other.test", "https://admin.test/cli-auth?code=AB"},
	} {
		if got := verificationLink(tc.g, tc.admin); got != tc.want {
			t.Errorf("%+v %q: got %q, want %q", tc.g, tc.admin, got, tc.want)
		}
	}
}
