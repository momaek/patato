package commerce

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/momaek/patato/server/internal/store/db"
)

// Gateway 是一种支付方式：生成付款链接，校验异步回调。
type Gateway interface {
	Code() string
	Name() string
	PayURL(o db.Order, notifyURL, returnURL string) (string, error)
	Verify(values url.Values) (Notification, error)
	// Ack 是回调处理成功后应返回给支付平台的内容。
	Ack() string
}

// Notification 是验签通过的回调内容。
type Notification struct {
	TradeNo        string
	GatewayTradeNo string
	Amount         int64
	Paid           bool
	Status         string
}

// EPay 是易支付（彩虹易支付及兼容接口）：MD5 签名，页面跳转支付，异步回调返回 success。
type EPay struct {
	code, name string
	// URL 是接口地址，例如 https://pay.example.com/
	URL string
	PID string
	Key string
	// Type 是支付渠道（alipay、wxpay、qqpay），为空时由收银台选择。
	Type string
}

func NewEPay(code, name, apiURL, pid, key, typ string) *EPay {
	return &EPay{code: code, name: name, URL: strings.TrimRight(apiURL, "/") + "/", PID: pid, Key: key, Type: typ}
}

func (e *EPay) Code() string { return e.code }
func (e *EPay) Name() string { return e.name }
func (e *EPay) Ack() string  { return "success" }

// Sign 按易支付规则签名：去掉 sign、sign_type 和空值，按键名排序拼成 a=b&c=d，末尾接密钥取 MD5。
func (e *EPay) Sign(values url.Values) string { return e.sign(values, false) }

// sign 的 withEmpty 为 true 时空值也参与签名：彩虹易支付官方 SDK 跳过空值，Xboard 的实现不跳过，
// 验签时两种都认，兼容不同的易支付平台。
func (e *EPay) sign(values url.Values, withEmpty bool) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "sign" || k == "sign_type" || (!withEmpty && values.Get(k) == "") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+values.Get(k))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + e.Key))
	return hex.EncodeToString(sum[:])
}

func (e *EPay) PayURL(o db.Order, notifyURL, returnURL string) (string, error) {
	v := url.Values{}
	v.Set("pid", e.PID)
	if e.Type != "" {
		v.Set("type", e.Type)
	}
	v.Set("out_trade_no", o.TradeNo)
	v.Set("notify_url", notifyURL)
	v.Set("return_url", returnURL)
	v.Set("name", o.PlanName+" "+orKey(PeriodNames, o.Period))
	v.Set("money", Yuan(o.Total))
	v.Set("sign", e.Sign(v))
	v.Set("sign_type", "MD5")
	return e.URL + "submit.php?" + v.Encode(), nil
}

func (e *EPay) Verify(values url.Values) (Notification, error) {
	n := Notification{TradeNo: values.Get("out_trade_no"), GatewayTradeNo: values.Get("trade_no"), Status: values.Get("trade_status")}
	sig := []byte(strings.ToLower(values.Get("sign")))
	if len(sig) == 0 || (subtle.ConstantTimeCompare(sig, []byte(e.sign(values, false))) != 1 &&
		subtle.ConstantTimeCompare(sig, []byte(e.sign(values, true))) != 1) {
		return n, errors.New("签名不对")
	}
	if values.Get("pid") != e.PID {
		return n, fmt.Errorf("商户号 %s 不对", values.Get("pid"))
	}
	amount, err := ParseYuan(values.Get("money"))
	if err != nil {
		return n, err
	}
	n.Amount = amount
	n.Paid = n.Status == "TRADE_SUCCESS"
	return n, nil
}

// ParseYuan 把 "19.90" 这样的金额换成分。
func ParseYuan(s string) (int64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("金额 %q 格式不对", s)
	}
	return int64(math.Round(f * 100)), nil
}
