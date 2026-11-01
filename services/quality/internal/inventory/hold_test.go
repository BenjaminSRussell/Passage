package inventory_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/inventory"
)

const legalEntity = "00000000-0000-0000-0000-0000000000e1"

type seen struct {
	path   string
	key    string
	claims tenant.ServiceClaims
	body   map[string]any
}

func rig(t *testing.T, status int) (*inventory.Client, *seen) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("quality", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("inventory", map[string]ed25519.PublicKey{"quality": pub})
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			t.Errorf("token refused: %v", err)
		}
		got.path, got.key, got.claims = r.URL.Path, r.Header.Get("Idempotency-Key"), claims
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return inventory.New(srv.URL+"/", signer, nil), got
}

func call() inventory.Call {
	return inventory.Call{TenantID: ids.New(), LegalEntityID: legalEntity, UserID: "qe-1", Key: "quality-order:o1:place", SourceType: "QUALITY_ORDER", SourceRef: "o1",
		ProductID: ids.New(), Dims: map[string]string{"site": "S1", "batch": "B1"}, Qty: decimal.NewFromInt(100), KeepQty: decimal.NewFromInt(10)}
}

func TestPlaceSignsOnlyTheHoldApplyDutyAndSendsTheLot(t *testing.T) {
	c, got := rig(t, http.StatusCreated)
	in := call()
	if err := c.Place(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/inventory/holds" || got.key != in.Key || !slices.Equal(got.claims.Duties, []string{"inventory.hold.apply"}) ||
		got.claims.Aud != "inventory" || got.claims.Le != legalEntity || got.claims.Act != "qe-1" {
		t.Fatalf("request %+v", got)
	}
	if got.body["qty"] != "100" || got.body["allowPartial"] != true || got.body["reason"] != "QUALITY" || got.body["sourceType"] != "QUALITY_ORDER" ||
		got.body["sourceRef"] != "o1" || got.body["productId"] != in.ProductID || got.body["legalEntityId"] != legalEntity {
		t.Fatalf("body %v", got.body)
	}
}

func TestReleaseStatesTheLevelToKeep(t *testing.T) {
	c, got := rig(t, http.StatusOK)
	if err := c.Release(context.Background(), call()); err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/inventory/holds:release-source" || got.body["keepQty"] != "10" || got.body["sourceRef"] != "o1" || got.key != "" {
		t.Fatalf("request %+v", got)
	}
}

func TestOutcomesSeparateRefusalFromRetry(t *testing.T) {
	for status, rejected := range map[int]bool{http.StatusUnprocessableEntity: true, http.StatusBadRequest: true, http.StatusForbidden: true,
		http.StatusServiceUnavailable: false, http.StatusTooManyRequests: false, http.StatusUnauthorized: false} {
		c, _ := rig(t, status)
		err := c.Place(context.Background(), call())
		if err == nil || errors.Is(err, inventory.ErrRejected) != rejected {
			t.Errorf("status %d: %v, rejected want %v", status, err, rejected)
		}
	}
}

func TestNoSignerNoCall(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()
	if err := inventory.New(srv.URL, nil, nil).Place(context.Background(), call()); err == nil || reached {
		t.Fatalf("an unsigned hold call was sent: %v", err)
	}
}
