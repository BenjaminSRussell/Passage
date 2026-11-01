package closeperiods

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

var postingDate = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)

func fixture(t *testing.T, handler http.HandlerFunc) (*Client, context.Context, string, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	signer, err := tenant.NewServiceSigner("ledger", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "u1", Duties: testkit.ScopeDuties(le)})
	return New(srv.URL, nil, signer), ctx, le, ids.New()
}

func TestStatus_SendsSignedResolveAndReturnsStatus(t *testing.T) {
	var got *http.Request
	c, ctx, le, ledgerID := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"fiscalPeriodId":"p","status":"permanently_closed"}`))
	})
	status, err := c.Status(ctx, le, ledgerID, postingDate, "ap")
	if err != nil || status != "permanently_closed" {
		t.Fatalf("status %q err %v", status, err)
	}
	q := got.URL.Query()
	if got.URL.Path != "/v1/close/periods:resolve" || q.Get("ledgerId") != ledgerID || q.Get("postingDate") != "2026-03-10" || q.Get("sourceSystem") != "ap" {
		t.Fatalf("unexpected request %s", got.URL)
	}
	if got.Header.Get(tenant.HeaderServiceToken) == "" {
		t.Fatal("request carries no service token")
	}
}

func TestStatus_NoRegisteredPeriodIsATypedError(t *testing.T) {
	c, ctx, le, ledgerID := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"CLOSE-0005","serviceCode":"CLOSE-0005"}`))
	})
	if _, err := c.Status(ctx, le, ledgerID, postingDate, "ap"); !errors.Is(err, store.ErrNoFiscalPeriod) {
		t.Fatalf("want ErrNoFiscalPeriod, got %v", err)
	}
}

func TestStatus_FailsOnEveryOtherOutcome(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"forbidden":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"server error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"other 404":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"ambiguous":    func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) },
		"empty status": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) },
		"not json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`nope`)) },
		"redirect":     func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			c, ctx, le, ledgerID := fixture(t, h)
			status, err := c.Status(ctx, le, ledgerID, postingDate, "ap")
			if err == nil || errors.Is(err, store.ErrNoFiscalPeriod) {
				t.Fatalf("want a hard failure, got status %q err %v", status, err)
			}
		})
	}
}

func TestStatus_UnreachableAndUnsignedFail(t *testing.T) {
	c, ctx, le, ledgerID := fixture(t, func(http.ResponseWriter, *http.Request) {})
	c.baseURL = "http://127.0.0.1:1"
	if _, err := c.Status(ctx, le, ledgerID, postingDate, "ap"); err == nil {
		t.Fatal("an unreachable close service must fail")
	}
	c.signer = nil
	if _, err := c.Status(ctx, le, ledgerID, postingDate, "ap"); err == nil {
		t.Fatal("a missing signing key must fail")
	}
}

func TestStatus_CachesPerLedgerModuleAndDateForThirtySeconds(t *testing.T) {
	calls := 0
	c, ctx, le, ledgerID := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"open"}`))
	})
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	ask := func(source string, date time.Time) {
		t.Helper()
		if _, err := c.Status(ctx, le, ledgerID, date, source); err != nil {
			t.Fatal(err)
		}
	}
	ask("ap", postingDate)
	ask("ap", postingDate)
	if calls != 1 {
		t.Fatalf("calls %d, want the second answer from cache", calls)
	}
	ask("ar", postingDate)
	ask("ap", postingDate.AddDate(0, 0, 1))
	if calls != 3 {
		t.Fatalf("calls %d, a different module or date must not share an entry", calls)
	}
	now = now.Add(CacheTTL + time.Second)
	ask("ap", postingDate)
	if calls != 4 {
		t.Fatalf("calls %d, an expired entry must be fetched again", calls)
	}
}

func TestStatus_DoesNotCacheFailures(t *testing.T) {
	calls := 0
	c, ctx, le, ledgerID := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	})
	for i := 0; i < 2; i++ {
		if _, err := c.Status(ctx, le, ledgerID, postingDate, "ap"); err == nil {
			t.Fatal("want an error")
		}
	}
	if calls != 2 {
		t.Fatalf("calls %d, failures must not be cached", calls)
	}
}
