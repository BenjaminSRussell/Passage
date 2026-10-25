package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

type claimErrStore struct {
	*memstore.Store
}

func (claimErrStore) ClaimKey(context.Context, string, string, time.Duration) (store.ClaimResult, error) {
	return store.ClaimResult{}, errors.New("claim table unreachable")
}

func TestArrival_RejectsMissingOrOverlongKey(t *testing.T) {
	for name, key := range map[string]string{"missing": "", "overlong": strings.Repeat("k", 256)} {
		t.Run(name, func(t *testing.T) {
			r, _ := newRouter(memstore.New())
			rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS", ""), maintainer(key))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"Idempotency-Key"`) || !strings.Contains(rec.Body.String(), "COM-0001") {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestArrival_ReplayReturnsOriginalAndStoresOneReceipt(t *testing.T) {
	ms := memstore.New()
	r, _ := newRouter(ms)
	first := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	second := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("got %d then %d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" || first.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("replay header: first %q second %q", first.Header().Get("Idempotent-Replayed"), second.Header().Get("Idempotent-Replayed"))
	}
	if second.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", second.Header().Get("Content-Type"))
	}
	list, err := ms.ListReceipts(testkit.Ctx(testkit.TenantID(1)), store.ReceiptFilter{Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("receipts %d err %v", len(list), err)
	}
}

func TestArrival_SameKeyDifferentBodyIsRejected(t *testing.T) {
	r, _ := newRouter(memstore.New())
	doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-2", ""), maintainer("k1"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "COM-0409") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestArrival_KeyInFlightIsConflict(t *testing.T) {
	ms := memstore.New()
	r, _ := newRouter(ms)
	body := arrivalJSON("PS-1", "")
	claim, err := ms.ClaimKey(testkit.Ctx(testkit.TenantID(1)), "k1", api.RequestHash(http.MethodPost, arrivalsPath, []byte(body)), time.Minute)
	if err != nil || claim.State != store.ClaimNew {
		t.Fatalf("claim %v %v", claim, err)
	}
	rec := doWith(r, http.MethodPost, arrivalsPath, body, maintainer("k1"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "COM-0410") || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d %s retry-after %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
	}
}

func TestArrival_RejectedRequestReleasesKey(t *testing.T) {
	r, _ := newRouter(memstore.New())
	doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	for i := 0; i < 2; i++ {
		rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k2"))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "RCV-DUP-001") || rec.Header().Get("Idempotent-Replayed") != "" {
			t.Fatalf("attempt %d: got %d %s replayed %q", i, rec.Code, rec.Body.String(), rec.Header().Get("Idempotent-Replayed"))
		}
	}
}

func TestArrival_ClaimFailureIsInternalError(t *testing.T) {
	r, _ := newRouter(claimErrStore{Store: memstore.New()})
	rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") || strings.Contains(rec.Body.String(), "unreachable") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestArrival_KeysAreScopedPerTenant(t *testing.T) {
	r, _ := newRouter(memstore.New())
	doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	other := maintainer("k1")
	other["X-Passage-Tenant"] = testkit.TenantID(2)
	rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), other)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
