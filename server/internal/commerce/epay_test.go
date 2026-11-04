package commerce

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/momaek/patato/server/internal/store/db"
)

// xboardSign 照搬 Xboard 的 EPay 签名（app/Payments/EPay.php）：
// ksort 后 stripslashes(urldecode(http_build_query($params))) 再接密钥取 MD5。
// http_build_query 和 urldecode 一来一回，结果就是按键排序的原始 k=v&k=v；空值也参与（写成 "k="）。
func xboardSign(params map[string]string, key string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strings.ReplaceAll(params[k], `\`, ""))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

func TestEPayMatchesXboard(t *testing.T) {
	g := NewEPay("alipay", "支付宝", "https://pay.example.com", "1001", "merchant-key", "alipay")
	o := db.Order{TradeNo: "20261101120000ABCD1234", PlanName: "高级", Period: "monthly", Total: 1990, ExpiresAt: time.Now()}
	raw, err := g.PayURL(context.Background(), o, "https://api.example.com/api/v1/payment/alipay/notify", "https://my.example.com/orders/20261101120000ABCD1234")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Scheme+"://"+u.Host+u.Path != "https://pay.example.com/submit.php" {
		t.Fatalf("submit url: %s", raw)
	}
	q := u.Query()
	params := map[string]string{}
	for k := range q {
		if k != "sign" && k != "sign_type" {
			params[k] = q.Get(k)
		}
	}
	// 平台按 Xboard 同样的算法验签：我们发出去的签名要对得上
	if q.Get("sign") != xboardSign(params, "merchant-key") || q.Get("sign_type") != "MD5" || q.Get("money") != "19.90" {
		t.Fatalf("pay url signature differs from Xboard's algorithm: %s", raw)
	}

	// 平台的回调：按 Xboard 的算法签名，我们要能验过
	cb := map[string]string{"pid": "1001", "trade_no": "2026110112345678", "out_trade_no": o.TradeNo, "type": "alipay",
		"name": "高级 月付", "money": "19.90", "trade_status": "TRADE_SUCCESS"}
	v := url.Values{}
	for k, x := range cb {
		v.Set(k, x)
	}
	v.Set("sign", xboardSign(cb, "merchant-key"))
	v.Set("sign_type", "MD5")
	n, err := g.Verify(httptest.NewRequest("GET", "/?"+v.Encode(), nil))
	if err != nil || !n.Paid || n.Amount != 1990 || n.TradeNo != o.TradeNo || n.GatewayTradeNo != "2026110112345678" {
		t.Fatalf("verify xboard-signed callback: %+v %v", n, err)
	}
	post := httptest.NewRequest("POST", "/", strings.NewReader(v.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if got, err := g.Verify(post); err != nil || got != n {
		t.Fatalf("POST form must verify the same payment: %+v %v", got, err)
	}

	// 改过金额、换了商户号、签名不对都拒绝
	tampered := url.Values{}
	for k := range v {
		tampered.Set(k, v.Get(k))
	}
	tampered.Set("money", "0.01")
	if _, err := g.Verify(httptest.NewRequest("GET", "/?"+tampered.Encode(), nil)); err == nil {
		t.Fatal("tampered amount must fail")
	}
	other := NewEPay("alipay", "支付宝", "https://pay.example.com", "2002", "merchant-key", "")
	if _, err := other.Verify(httptest.NewRequest("GET", "/?"+v.Encode(), nil)); err == nil {
		t.Fatal("wrong pid must fail")
	}

	// 回调里带空参数时：彩虹易支付官方 SDK 签名跳过空值，Xboard 把空值计入签名，两种都要验过。
	cb["param"] = ""
	v.Set("param", "")
	sdkStyle := map[string]string{}
	for k, x := range cb {
		if x != "" {
			sdkStyle[k] = x
		}
	}
	v.Set("sign", xboardSign(sdkStyle, "merchant-key"))
	if _, err := g.Verify(httptest.NewRequest("GET", "/?"+v.Encode(), nil)); err != nil {
		t.Fatalf("official SDK style (empty values skipped) must verify: %v", err)
	}
	v.Set("sign", xboardSign(cb, "merchant-key"))
	if _, err := g.Verify(httptest.NewRequest("GET", "/?"+v.Encode(), nil)); err != nil {
		t.Fatalf("Xboard style (empty values signed) must verify: %v", err)
	}
	v.Set("sign", xboardSign(cb, "wrong-key"))
	if _, err := g.Verify(httptest.NewRequest("GET", "/?"+v.Encode(), nil)); err == nil {
		t.Fatal("wrong key must fail")
	}
}
