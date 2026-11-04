package commerce

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stripe/stripe-go/v87"

	"github.com/momaek/patato/server/internal/store/db"
)

func TestStripeCheckoutFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		cancel     bool
	}{
		{"API error", `{"error":{"type":"invalid_request_error","message":"fixture rejection"}}`, 400, false},
		{"missing URL", `{"id":"cs_fixture","object":"checkout.session"}`, 200, false},
		{"request cancelled", `{"id":"cs_fixture","url":"https://checkout.test/session"}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer remote.Close()
			g := NewStripe("stripe", "银行卡", "stripe-api-fixture", "stripe-webhook-fixture")
			g.client = stripe.NewClient("stripe-api-fixture", stripe.WithBackends(stripe.NewBackendsWithConfig(&stripe.BackendConfig{
				URL: stripe.String(remote.URL), HTTPClient: remote.Client(), MaxNetworkRetries: stripe.Int64(0),
			})))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			payURL, err := g.PayURL(ctx, db.Order{TradeNo: "ORDER1", PlanName: "高级", Period: "monthly", Total: 1990}, "", "https://portal.test/orders/ORDER1")
			if err == nil || payURL != "" {
				t.Fatalf("failure must not return a checkout link: %q %v", payURL, err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("request context cancellation lost: %v", err)
			}
		})
	}
}
