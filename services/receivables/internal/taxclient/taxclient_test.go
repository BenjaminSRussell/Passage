package taxclient

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func signedEnv(url string) func(string) string {
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	m := map[string]string{"TAX_URL": url, tenant.EnvServiceName: "receivables", tenant.EnvServiceSigningKey: seed}
	return func(k string) string { return m[k] }
}

func TestRecord_PostsNegativeOutputTaxSignedWithTheRecordDuty(t *testing.T) {
	var path, token string
	var body struct {
		Transactions []map[string]string `json:"transactions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, token = r.URL.Path, r.Header.Get(tenant.HeaderServiceToken)
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	c, err := FromEnv(signedEnv(srv.URL))
	if err != nil || c == nil {
		t.Fatalf("client: %v %v", c, err)
	}
	le, id := ids.New(), ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	if err := c.Record(ctx, le, []Txn{{ID: id, Code: "DE19", Date: "2027-03-01", Amount: decimal.RequireFromString("-57.00")}}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if path != "/v1/tax/transactions" || token == "" || len(body.Transactions) != 1 {
		t.Fatalf("request %s token %q body %+v", path, token, body)
	}
	got := body.Transactions[0]
	if got["id"] != id || got["code"] != "DE19" || got["direction"] != "output" || got["tax_date"] != "2027-03-01" || got["amount"] != "-57" {
		t.Fatalf("transaction %+v", got)
	}
}

func TestRecord_ErrorsCarryTheStatusAndOnlyClientErrorsAreRejections(t *testing.T) {
	status := http.StatusBadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	c, _ := FromEnv(signedEnv(srv.URL))
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	for _, tc := range []struct {
		status   int
		rejected bool
	}{{http.StatusBadRequest, true}, {http.StatusUnprocessableEntity, true}, {http.StatusTooManyRequests, false}, {http.StatusRequestTimeout, false}, {http.StatusBadGateway, false}} {
		status = tc.status
		err := c.Record(ctx, le, []Txn{{ID: ids.New(), Code: "X", Date: "2027-03-01", Amount: decimal.NewFromInt(-1)}})
		if err == nil || Rejected(err) != tc.rejected {
			t.Fatalf("status %d: err %v rejected %v", tc.status, err, Rejected(err))
		}
	}
}

func TestFromEnv_UnsetIsNilAndURLWithoutKeyIsRefused(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if c, err := FromEnv(func(k string) string {
		if k == "TAX_URL" {
			return "http://tax"
		}
		return ""
	}); c != nil || err == nil {
		t.Fatalf("no signing key: %v %v", c, err)
	}
}
