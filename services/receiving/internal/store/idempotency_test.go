package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func claim(t *testing.T, st *store.Postgres, p tenant.Principal, key, hash string, lockFor time.Duration) store.ClaimResult {
	t.Helper()
	res, err := st.ClaimKey(ctxOf(p), key, hash, lockFor)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestClaimKeyOutcomes(t *testing.T) {
	e := setup(t)
	p := principal(1)
	ctx := ctxOf(p)
	if got := claim(t, e.st, p, "k1", "h1", time.Minute); got.State != store.ClaimNew {
		t.Fatalf("first claim=%v, want new", got.State)
	}
	if got := claim(t, e.st, p, "k1", "h1", time.Minute); got.State != store.ClaimInFlight {
		t.Fatalf("repeat while locked=%v, want in-flight", got.State)
	}
	if got := claim(t, e.st, p, "k1", "other", time.Minute); got.State != store.ClaimMismatch {
		t.Fatalf("different hash=%v, want mismatch", got.State)
	}
	if err := e.st.CompleteKey(ctx, "k1", 201, []byte(`{"id":"r1"}`)); err != nil {
		t.Fatal(err)
	}
	done := claim(t, e.st, p, "k1", "h1", time.Minute)
	var body map[string]string
	if done.State != store.ClaimCompleted || done.Status != 201 || json.Unmarshal(done.Body, &body) != nil || body["id"] != "r1" {
		t.Fatalf("completed claim=%+v, want 201 with stored body", done)
	}
	if got := claim(t, e.st, p, "k1", "other", time.Minute); got.State != store.ClaimMismatch {
		t.Fatalf("completed key with different hash=%v, want mismatch", got.State)
	}
}

func TestClaimKeyExpiredAndLapsedAreNew(t *testing.T) {
	e := setup(t)
	p := principal(1)
	claim(t, e.st, p, "expired", "h", time.Minute)
	if err := e.st.CompleteKey(ctxOf(p), "expired", 200, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	err := dbx.WithTenantTx(ctxOf(p), e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update idempotency_keys set expires_at = now() - interval '1 minute' where key = 'expired'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := claim(t, e.st, p, "expired", "h2", time.Minute); got.State != store.ClaimNew {
		t.Fatalf("expired key=%v, want new", got.State)
	}
	if got := claim(t, e.st, p, "expired", "h2", time.Minute); got.State != store.ClaimInFlight {
		t.Fatalf("reclaimed key=%v, want in-flight under the new hash", got.State)
	}
	claim(t, e.st, p, "lapsed", "h", 0)
	if got := claim(t, e.st, p, "lapsed", "h", time.Minute); got.State != store.ClaimNew {
		t.Fatalf("lapsed lock=%v, want new", got.State)
	}
}

func TestReleaseKeyAllowsRetryAndIgnoresAbsent(t *testing.T) {
	e := setup(t)
	p := principal(1)
	claim(t, e.st, p, "k", "h", time.Minute)
	if err := e.st.ReleaseKey(ctxOf(p), "k"); err != nil {
		t.Fatal(err)
	}
	if got := claim(t, e.st, p, "k", "h", time.Minute); got.State != store.ClaimNew {
		t.Fatalf("after release=%v, want new", got.State)
	}
	if err := e.st.ReleaseKey(ctxOf(p), "absent"); err != nil {
		t.Fatalf("release absent: %v", err)
	}
	if err := e.st.CompleteKey(ctxOf(p), "absent", 200, nil); err != nil {
		t.Fatalf("complete absent: %v", err)
	}
	if err := e.st.CompleteKey(ctxOf(p), "k", 204, nil); err != nil {
		t.Fatal(err)
	}
	if got := claim(t, e.st, p, "k", "h", time.Minute); got.State != store.ClaimCompleted || got.Status != 204 || len(got.Body) != 0 {
		t.Fatalf("nil body claim=%+v, want completed 204 with empty body", got)
	}
}

func TestClaimKeyTenantIsolationAndPrincipal(t *testing.T) {
	e := setup(t)
	one, two := principal(1), principal(2)
	claim(t, e.st, one, "shared", "h1", time.Minute)
	if err := e.st.CompleteKey(ctxOf(one), "shared", 201, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := claim(t, e.st, two, "shared", "h2", time.Minute); got.State != store.ClaimNew {
		t.Fatalf("other tenant sees first tenant's key: %v", got.State)
	}
	if got := claim(t, e.st, one, "shared", "h1", time.Minute); got.State != store.ClaimCompleted {
		t.Fatalf("first tenant's key changed by the other tenant: %v", got.State)
	}
	if _, err := e.st.ClaimKey(context.Background(), "k", "h", time.Minute); err == nil {
		t.Fatal("want error without a principal")
	}
}

func TestClaimKeyConcurrentOneWins(t *testing.T) {
	e := setup(t)
	p := principal(1)
	const workers = 8
	states := make([]store.ClaimState, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := e.st.ClaimKey(ctxOf(p), "race", "h", time.Minute)
			if err != nil {
				t.Error(err)
			}
			states[i] = res.State
		}()
	}
	wg.Wait()
	won := 0
	for _, s := range states {
		if s == store.ClaimNew {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("new claims=%d, want exactly 1 (states=%v)", won, states)
	}
}
