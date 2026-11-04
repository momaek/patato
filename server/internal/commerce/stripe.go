package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/momaek/patato/server/internal/store/db"
)

// Stripe 使用人民币的一次性托管 Checkout，只有签名有效的已支付事件才能履约。
type Stripe struct {
	code, name    string
	webhookSecret string
	client        *stripe.Client
}

func NewStripe(code, name, secretKey, webhookSecret string) *Stripe {
	return &Stripe{code: code, name: name, webhookSecret: webhookSecret, client: stripe.NewClient(secretKey)}
}

func (s *Stripe) Code() string { return s.code }
func (s *Stripe) Name() string { return s.name }
func (s *Stripe) Ack() string  { return "success" }

func (s *Stripe) PayURL(ctx context.Context, o db.Order, _, returnURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p := &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String("payment"),
		ClientReferenceID: stripe.String(o.TradeNo),
		SuccessURL:        stripe.String(returnURL),
		CancelURL:         stripe.String(returnURL),
		AdaptivePricing:   &stripe.CheckoutSessionCreateAdaptivePricingParams{Enabled: stripe.Bool(false)},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency: stripe.String("cny"), UnitAmount: stripe.Int64(o.Total),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
					Name: stripe.String(o.PlanName + " " + orKey(PeriodNames, o.Period)),
				},
			},
		}},
	}
	p.SetIdempotencyKey("patato:" + s.code + ":" + o.TradeNo)
	session, err := s.client.V1CheckoutSessions.Create(ctx, p)
	if err != nil {
		return "", err
	}
	if session.URL == "" {
		return "", errors.New("Stripe 未返回 Checkout 付款链接")
	}
	return session.URL, nil
}

func (s *Stripe) Verify(r *http.Request) (Notification, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return Notification{}, err
	}
	// 校验原始字节与时间戳，不依赖商户 Webhook 的 API 版本。
	if err := webhook.ValidatePayload(body, r.Header.Get("Stripe-Signature"), s.webhookSecret); err != nil {
		return Notification{}, err
	}
	var event stripe.Event
	if err := json.Unmarshal(body, &event); err != nil {
		return Notification{}, err
	}
	n := Notification{Status: string(event.Type)}
	if event.Type != stripe.EventTypeCheckoutSessionCompleted && event.Type != stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded {
		return n, nil
	}
	if event.Data == nil {
		return n, errors.New("Stripe 事件缺少 Checkout Session")
	}
	var session stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return n, err
	}
	n.TradeNo, n.GatewayTradeNo = session.ClientReferenceID, session.ID
	n.Status = string(session.PaymentStatus)
	if session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		return n, nil
	}
	if session.Mode != stripe.CheckoutSessionModePayment || session.Currency != stripe.CurrencyCNY || n.TradeNo == "" || n.GatewayTradeNo == "" {
		return n, errors.New("Stripe Checkout 模式、币种或订单标识不符")
	}
	n.Amount, n.Paid = session.AmountTotal, true
	return n, nil
}
