package target_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
)

// The production signer and verifier accept what the client sends. The grant table has no entry for the
// service "migration-factory" yet (handoff row H-S8-1), so this proves the signing path with the grant of the
// service "migration", which holds the same receivables.import.write duty.
func TestLoadBatch_TokenVerifiesWithTheProductionVerifier(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("migration", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("receivables", map[string]ed25519.PublicKey{"migration": pub})
	var claims tenant.ServiceClaims
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		claims = c
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 0, "legacyKey": "A", "outcome": "CREATED", "openItemId": "00000000-0000-4000-8000-000000000001"}}})
	}))
	t.Cleanup(srv.Close)
	c := target.NewReceivables(target.Config{BaseURL: srv.URL, Signer: signer, Sleep: func(context.Context, time.Duration) error { return nil }})
	res, err := c.LoadBatch(context.Background(), tid, le, "src", "k", []domain.ReceivablesRow{row("A", "1")})
	if err != nil || res[0].Outcome != target.OutcomeCreated {
		t.Fatalf("load: %+v %v", res, err)
	}
	if claims.Aud != "receivables" || claims.Act != "service:migration-loader" || claims.Tid != tid || claims.Le != le || !slices.Equal(claims.Duties, []string{"receivables.import.write"}) {
		t.Fatalf("claims %+v", claims)
	}
}

// Until the main session seeds the grant, the factory cannot sign for receivables; once it does, the entry must hold exactly these two duties.
func TestServiceGrantForTheFactoryIsSeeded(t *testing.T) {
	grant := tenant.ServiceGrantTable()[target.ServiceName]["receivables"]
	if grant == nil {
		t.Skip("handoff H-S8-1: platform/go/tenant serviceGrants has no \"migration-factory\" entry yet")
	}
	want := []string{"receivables.import.write", "receivables.view"}
	if len(grant) != len(want) {
		t.Errorf("the grant for migration-factory -> receivables must hold exactly %v (least privilege), got %v", want, grant)
	}
	for _, duty := range want {
		if !slices.Contains(grant, duty) {
			t.Errorf("the grant for migration-factory -> receivables lacks %s: %v", duty, grant)
		}
	}
	ledger := tenant.ServiceGrantTable()[target.ServiceName]["ledger"]
	wantLedger := []string{"ledger.entry.post", "ledger.balance.read", "ledger.ledger.lookup"}
	if len(ledger) != len(wantLedger) {
		t.Errorf("the grant for migration-factory -> ledger must hold exactly %v (least privilege), got %v", wantLedger, ledger)
	}
	for _, duty := range wantLedger {
		if !slices.Contains(ledger, duty) {
			t.Errorf("the grant for migration-factory -> ledger lacks %s: %v", duty, ledger)
		}
	}
	payables := tenant.ServiceGrantTable()[target.ServiceName]["payables"]
	if want := []string{"opening-item.import", "opening-item.import.read"}; !slices.Equal(slices.Sorted(slices.Values(payables)), want) {
		t.Errorf("the grant for migration-factory -> payables must hold exactly %v (opening-item import, #565), got %v", want, payables)
	}
	if other := tenant.ServiceGrantTable()[target.ServiceName]; len(other) != 3 {
		t.Errorf("migration-factory is granted only receivables, payables (opening items) and ledger (replay, compensations), got %v", other)
	}
}
