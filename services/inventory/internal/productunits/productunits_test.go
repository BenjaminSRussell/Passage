package productunits

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	entity   = "22222222-2222-4222-8222-222222222222"
	product  = "44444444-4444-4444-8444-444444444444"
)

// The conversion is asked of product without a target unit, signed as
// inventory for the document's legal entity with product.uom.convert only and
// the consumer's service principal as actor; a refused conversion is
// ErrNoConversion, anything else is not.
func TestToInventoryUnit_SignsTheNarrowDutyAndMapsTheAnswer(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("inventory", priv)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := tenant.ServicePrincipal("inventory-postings", tenantID, entity)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), principal)
	tests := []struct {
		name      string
		status    int
		body      string
		want      string
		refused   bool
		transient bool
	}{
		{"converted", http.StatusOK, `{"from":"BOX","to":"EA","quantity":"24"}`, "24", false, false},
		{"no path", http.StatusUnprocessableEntity, `{"code":"PRD-UOM-UNDERFLOW","detail":"no conversion path from BOX to EA"}`, "", true, false},
		{"unknown product", http.StatusNotFound, `{"code":"COM-0001","detail":"product not found"}`, "", true, false},
		{"unavailable", http.StatusServiceUnavailable, `{}`, "", false, true},
		{"token refused", http.StatusForbidden, `{}`, "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen tenant.ServiceClaims
			var query url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := tenant.NewServiceVerifier("product", map[string]ed25519.PublicKey{"inventory": pub}).Verify(r.Header.Get(tenant.HeaderServiceToken))
				if err != nil {
					t.Errorf("service token: %v", err)
				}
				seen, query = c, r.URL.Query()
				if r.URL.Path != "/v1/product/uom/convert" {
					t.Errorf("path %s", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			qty, unit, err := New(srv.URL, signer, nil).ToInventoryUnit(ctx, entity, product, "BOX", decimal.NewFromInt(2))
			switch {
			case tt.refused && !errors.Is(err, ErrNoConversion):
				t.Fatalf("%v, want ErrNoConversion", err)
			case tt.transient && (err == nil || errors.Is(err, ErrNoConversion)):
				t.Fatalf("%v, want a retryable error", err)
			case tt.want != "" && (err != nil || !qty.Equal(decimal.RequireFromString(tt.want)) || unit != "EA"):
				t.Fatalf("%s %s %v, want %s EA", qty, unit, err, tt.want)
			}
			if len(seen.Duties) != 1 || seen.Duties[0] != "product.uom.convert" || seen.Le != entity || seen.Act != "service:inventory-postings" {
				t.Fatalf("token %+v", seen)
			}
			if query.Get("to") != "" || query.Get("product") != product || query.Get("from") != "BOX" || query.Get("qty") != "2" {
				t.Fatalf("query %v", query)
			}
		})
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("no PRODUCT_URL: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string { return map[string]string{"PRODUCT_URL": "http://product"}[k] }); err == nil {
		t.Fatal("PRODUCT_URL without a signing key accepted")
	}
}
