package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func validOrderRequest() createOrderRequest {
	req := createOrderRequest{LegalEntityID: ids.New(), CustomerID: ids.New(), InvoiceAccountID: ids.New(), OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04"}
	line := orderLineRequest{ProductID: ids.New(), Quantity: "2", Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: "10.50", RequestedShipDate: "2027-03-04"}
	req.Lines = []orderLineRequest{line}
	return req
}

func TestBuildOrder_Table(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*createOrderRequest)
		wantField  string
		wantNetSum string
	}{
		{name: "valid order computes line net", mutate: func(*createOrderRequest) {}, wantNetSum: "21"},
		{name: "no lines", mutate: func(r *createOrderRequest) { r.Lines = nil }, wantField: "lines"},
		{name: "unknown order type", mutate: func(r *createOrderRequest) { r.OrderType = "QUOTE" }, wantField: "orderType"},
		{name: "lowercase currency", mutate: func(r *createOrderRequest) { r.Currency = "usd" }, wantField: "currency"},
		{name: "bad channel", mutate: func(r *createOrderRequest) { r.Channel = "web shop!" }, wantField: "channel"},
		{name: "non-decimal unit price", mutate: func(r *createOrderRequest) { r.Lines[0].UnitPrice = "ten" }, wantField: "lines[0].unitPrice"},
		{name: "one-time order without a buyer name", mutate: func(r *createOrderRequest) {
			r.OneTime = &domain.OneTimeParty{Line1: "Hauptstr. 1", City: "Berlin", PostalCode: "10115", Country: "DE"}
		}, wantField: "oneTime.name"},
		{name: "one-time order with a bad country", mutate: func(r *createOrderRequest) {
			r.OneTime = &domain.OneTimeParty{Name: "Walk-in Buyer GmbH", Line1: "Hauptstr. 1", City: "Berlin", PostalCode: "10115", Country: "Germany"}
		}, wantField: "oneTime.country"},
		{name: "valid one-time order", mutate: func(r *createOrderRequest) {
			r.OneTime = &domain.OneTimeParty{Name: "Walk-in Buyer GmbH", Line1: "Hauptstr. 1", City: "Berlin", PostalCode: "10115", Country: "DE", TaxID: "DE123456789"}
		}, wantNetSum: "21"},
		{name: "non-decimal quantity", mutate: func(r *createOrderRequest) { r.Lines[0].Quantity = "two" }, wantField: "lines[0].quantity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validOrderRequest()
			tc.mutate(&req)
			in, fields := buildOrder(req)
			if tc.wantField == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected field errors %v", fields)
				}
				sum := decimal.Zero
				for _, l := range in.Lines {
					sum = sum.Add(l.LineNet)
				}
				if !sum.Equal(decimal.RequireFromString(tc.wantNetSum)) {
					t.Fatalf("net sum %s want %s", sum, tc.wantNetSum)
				}
				return
			}
			if _, ok := fields[tc.wantField]; !ok {
				t.Fatalf("want field error on %q, got %v", tc.wantField, fields)
			}
		})
	}
}

func TestBuildCreditProfile_Table(t *testing.T) {
	good := creditProfileRequest{Limit: decimal.NewFromInt(10000), Currency: "USD", LimitType: domain.LimitBalance, TolerancePct: decimal.NewFromInt(5), ToleranceAmount: decimal.NewFromInt(1000), OldestItemDaysMax: 30, MaxDunningLevel: 3}
	tests := []struct {
		name      string
		mutate    func(*creditProfileRequest)
		wantField string
	}{
		{name: "valid profile", mutate: func(*creditProfileRequest) {}},
		{name: "negative limit", mutate: func(r *creditProfileRequest) { r.Limit = decimal.NewFromInt(-1) }, wantField: "limit"},
		{name: "unknown limit type", mutate: func(r *creditProfileRequest) { r.LimitType = "FULL" }, wantField: "limitType"},
		{name: "tolerance above 100 percent", mutate: func(r *creditProfileRequest) { r.TolerancePct = decimal.NewFromInt(101) }, wantField: "tolerancePct"},
		{name: "dunning level above nine", mutate: func(r *creditProfileRequest) { r.MaxDunningLevel = 10 }, wantField: "maxDunningLevel"},
		{name: "negative open AR", mutate: func(r *creditProfileRequest) { r.Exposure.OpenAR = decimal.NewFromInt(-5) }, wantField: "exposure"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := good
			tc.mutate(&req)
			_, fields := buildCreditProfile(ids.New(), ids.New(), req)
			if tc.wantField == "" {
				if len(fields) != 0 {
					t.Fatalf("unexpected field errors %v", fields)
				}
				return
			}
			if _, ok := fields[tc.wantField]; !ok {
				t.Fatalf("want field error on %q, got %v", tc.wantField, fields)
			}
		})
	}
}

// TestCreateOrder_RequiresIdempotencyKey checks the intake validation path: the
// request is refused with 400 before the store (nil here) is reached.
func TestCreateOrder_RequiresIdempotencyKey(t *testing.T) {
	a := New(nil)
	r := chi.NewRouter()
	a.Register(r)

	body, err := json.Marshal(validOrderRequest())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, orders, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	p := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "sales-rep", Duties: map[string]struct{}{dutyWrite: {}}}
	req = req.WithContext(tenant.With(context.Background(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), idempotencyKey) {
		t.Fatalf("problem must name the Idempotency-Key field: %s", rec.Body.String())
	}
}

func TestBuildCreditProfile_RejectsBadReviewDate(t *testing.T) {
	bad := "04/03/2027"
	req := creditProfileRequest{Currency: "USD", LimitType: domain.LimitNone, ReviewDate: &bad}
	_, fields := buildCreditProfile(ids.New(), ids.New(), req)
	if _, ok := fields["reviewDate"]; !ok {
		t.Fatalf("want a reviewDate field error, got %v", fields)
	}
}

// TestATPResponse_WireFormat checks the BR-27.1 fixture on the wire: dates as
// YYYY-MM-DD, decimals as strings, and cumulative ATP 40, 50, 60.
func TestATPResponse_WireFormat(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n) }
	periods := []domain.Period{
		{Date: day(0), Supply: decimal.NewFromInt(100), Demand: decimal.NewFromInt(60)},
		{Date: day(3), Supply: decimal.NewFromInt(50), Demand: decimal.NewFromInt(40)},
		{Date: day(7), Supply: decimal.NewFromInt(80), Demand: decimal.NewFromInt(70)},
	}
	promise, err := domain.PromiseDate(periods, day(0), decimal.NewFromInt(45), true)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(atpResponse(promise, periods))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"promisedDate":"2027-03-04"`, `"complete":true`, `"date":"2027-03-01"`, `"supply":"100"`, `"cumulative":["40","50","60"]`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("wire form missing %s in %s", want, body)
		}
	}
}

func TestAnyDuty_AllowsOneOfMany(t *testing.T) {
	p := tenant.Principal{Duties: map[string]struct{}{dutyIntake: {}}}
	ok, err := anyDuty{dutyWrite, dutyIntake}.Allow(context.Background(), p, "", "")
	if err != nil || !ok {
		t.Fatalf("intake duty must satisfy the order create route: ok=%v err=%v", ok, err)
	}
	ok, _ = anyDuty{dutyWrite}.Allow(context.Background(), p, "", "")
	if ok {
		t.Fatal("unrelated duty must not satisfy the route")
	}
}
