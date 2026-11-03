package inventory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestBlocks(t *testing.T) {
	d := decimal.NewFromInt
	for name, c := range map[string]struct {
		physical, held, qty int64
		block               bool
	}{
		"nothing held":      {10, 0, 10, false},
		"within free":       {10, 8, 2, false},
		"exactly free":      {10, 8, 2, false},
		"into held":         {10, 8, 3, true},
		"all held":          {10, 10, 1, true},
		"zero quantity":     {10, 8, 0, false},
		"missing inventory": {0, 0, 5, false},
	} {
		if got := Blocks(d(c.physical), d(c.held), d(c.qty)); got != c.block {
			t.Errorf("%s: Blocks(%d,%d,%d)=%v, want %v", name, c.physical, c.held, c.qty, got, c.block)
		}
	}
}

func TestMapHoldReleaseClearsHeld(t *testing.T) {
	m := &MapHold{}
	m.Set("BOLT", decimal.NewFromInt(10), decimal.NewFromInt(8))
	lot, _ := m.Lot(context.Background(), "", "BOLT", "", "")
	if !lot.Held.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("held %s", lot.Held)
	}
	m.Set("BOLT", decimal.NewFromInt(10), decimal.Zero)
	lot, _ = m.Lot(context.Background(), "", "BOLT", "", "")
	if lot.Held.Sign() != 0 || !lot.Physical.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("after usage decision %+v", lot)
	}
}

func TestClientLot_SignedReadAndSumsHeld(t *testing.T) {
	const (
		tenantID = "11111111-1111-4111-8111-111111111111"
		entityID = "22222222-2222-4222-8222-222222222222"
		itemID   = "44444444-4444-4444-8444-444444444444"
	)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("warehouse", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("inventory", map[string]ed25519.PublicKey{"warehouse": pub})
	var path, groupBy, product, warehouse, duty string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			t.Errorf("token: %v", err)
		}
		if len(claims.Duties) == 1 {
			duty = claims.Duties[0]
		}
		path, groupBy, product, warehouse = r.URL.Path, r.URL.Query().Get("groupBy"), r.URL.Query().Get("product"), r.URL.Query().Get("warehouse")
		held := "8"
		_, _ = w.Write([]byte(`{"items":[{"physicalInvent":"10","heldPhysical":"` + held + `"}],"truncated":false}`))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "picker"})
	lot, err := c.Lot(ctx, entityID, itemID, "WH-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !lot.Physical.Equal(decimal.NewFromInt(10)) || !lot.Held.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("lot %+v", lot)
	}
	if path != onHandPath || groupBy != "product" || product != itemID || warehouse != "WH-1" || duty != dutyRead {
		t.Fatalf("request path=%s groupBy=%s product=%s warehouse=%s duty=%s", path, groupBy, product, warehouse, duty)
	}
	lot, err = c.Lot(ctx, entityID, "BOLT", "WH-1", "")
	if err != nil || lot.Held.Sign() != 0 {
		t.Fatalf("non-UUID item: %+v %v", lot, err)
	}
}

func TestFromEnv(t *testing.T) {
	c, err := FromEnv(func(string) string { return "" })
	if c != nil || err != nil {
		t.Fatalf("unset URL: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string {
		if k == "INVENTORY_URL" {
			return "http://inventory:8080"
		}
		return ""
	}); err == nil {
		t.Fatal("URL without a signing key must fail")
	}
}
