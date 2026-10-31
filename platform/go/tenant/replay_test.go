package tenant_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func replayPool(t *testing.T) (*tenant.PostgresReplayStore, func() *tenant.PostgresReplayStore) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if _, err := admin.Exec(context.Background(), dbx.ServiceTokenReplayDDL); err != nil {
		t.Fatal(err)
	}
	return tenant.NewPostgresReplayStore(app), func() *tenant.PostgresReplayStore { return tenant.NewPostgresReplayStore(app) }
}

func TestPostgresReplayStoreRefusesASecondSightPerTenantAndPurgesExpiredRows(t *testing.T) {
	first, another := replayPool(t)
	ctx := context.Background()
	soon := time.Now().Add(time.Minute)
	tenantA, tenantB := testkit.TenantID(1), testkit.TenantID(2)

	for _, tc := range []struct {
		name   string
		store  *tenant.PostgresReplayStore
		tenant string
		want   bool
	}{
		{"first sight", first, tenantA, true},
		{"same id on another replica", another(), tenantA, false},
		{"same id again", first, tenantA, false},
		{"same id under another tenant", another(), tenantB, true},
	} {
		fresh, err := tc.store.Remember(ctx, tc.tenant, "receiving:jti-1", soon)
		if err != nil || fresh != tc.want {
			t.Fatalf("%s: fresh %v err %v, want %v", tc.name, fresh, err, tc.want)
		}
	}

	for _, id := range []string{tenantA, tenantB} {
		if fresh, err := first.Remember(ctx, id, "receiving:expired", time.Now().Add(-time.Minute)); err != nil || !fresh {
			t.Fatalf("expired row: %v %v", fresh, err)
		}
	}
	if fresh, err := another().Remember(ctx, tenantA, "receiving:jti-2", soon); err != nil || !fresh {
		t.Fatalf("purging call: %v %v", fresh, err)
	}
	if fresh, err := another().Remember(ctx, tenantA, "receiving:expired", soon); err != nil || !fresh {
		t.Fatalf("an expired id must have been purged by a later call: fresh %v err %v", fresh, err)
	}
	if fresh, err := another().Remember(ctx, tenantB, "receiving:expired", soon); err != nil || !fresh {
		t.Fatalf("the purge must delete expired rows of every tenant: fresh %v err %v", fresh, err)
	}
	if fresh, err := another().Remember(ctx, tenantA, "receiving:jti-1", soon); err != nil || fresh {
		t.Fatalf("a live id must survive the purge: fresh %v err %v", fresh, err)
	}
}

type failingReplayStore struct{}

func (failingReplayStore) Remember(context.Context, string, string, time.Time) (bool, error) {
	return false, errors.New("relation service_token_replay does not exist")
}

func TestVerifierWithSharedStoreRefusesAReplayOnAnotherPod(t *testing.T) {
	store, _ := replayPool(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	keys := map[string]ed25519.PublicKey{"receiving": pub}
	podA := tenant.NewServiceVerifier("ledger", keys).WithReplayStore(store)
	podB := tenant.NewServiceVerifier("ledger", keys).WithReplayStore(store)
	p := tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "warehouse-user"}
	token, err := signer.Mint("ledger", p, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := podA.Verify(token); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := podB.Verify(token); !errors.Is(err, tenant.ErrServiceToken) {
		t.Fatalf("replay on a second pod: %v", err)
	}
	if _, err := podA.Verify(token); !errors.Is(err, tenant.ErrServiceToken) {
		t.Fatalf("replay on the same pod: %v", err)
	}
	other, _ := signer.Mint("ledger", p, "")
	if _, err := podB.Verify(other); err != nil {
		t.Fatalf("a different token on the second pod: %v", err)
	}
}

func TestVerifierFailsClosedWhenTheSharedStoreFailsAndCountsIt(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	verifier := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"receiving": pub}).WithReplayStore(failingReplayStore{})
	token, _ := signer.Mint("ledger", tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "u"}, "")
	var before, after bytes.Buffer
	tenant.WriteReplayMetrics(&before)
	if _, err := verifier.Verify(token); !errors.Is(err, tenant.ErrServiceToken) {
		t.Fatalf("a store failure must refuse the token: %v", err)
	}
	tenant.WriteReplayMetrics(&after)
	if before.String() == after.String() || !strings.Contains(after.String(), "passage_service_token_replay_store_failures_total") {
		t.Fatalf("the failure must be counted:\n%s", after.String())
	}
}

func TestShareReplayCacheFailsAtStartupWithoutPoolOrTableAndIgnoresAnUnconfiguredVerifier(t *testing.T) {
	tenant.SetServiceVerifier(nil)
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	if err := tenant.ShareReplayCache(context.Background(), nil); err != nil {
		t.Fatalf("no verifier means nothing to share: %v", err)
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"receiving": pub}))
	if err := tenant.ShareReplayCache(context.Background(), nil); err == nil {
		t.Fatal("service tokens enabled without a pool must fail startup")
	}
	_, app := testkit.DBWithApp(t)
	if err := tenant.ShareReplayCache(context.Background(), app); err == nil || !strings.Contains(err.Error(), "service_token_replay") {
		t.Fatalf("a missing table must fail startup, got %v", err)
	}
}

func TestShareReplayCacheMakesTheMiddlewareRefuseAReplay(t *testing.T) {
	tenant.SetServiceVerifier(nil)
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	admin, app := testkit.DBWithApp(t)
	if _, err := admin.Exec(context.Background(), dbx.ServiceTokenReplayDDL); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"receiving": pub}))
	if err := tenant.ShareReplayCache(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	token, _ := signer.Mint("ledger", tenant.Principal{TenantID: validTenant, LegalEntityID: validLE, UserID: "u"}, "")
	handler := tenant.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	send := func() int {
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.Header.Set(tenant.HeaderServiceToken, token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if first, second := send(), send(); first != http.StatusNoContent || second != http.StatusUnauthorized {
		t.Fatalf("first %d second %d", first, second)
	}
	var rows int
	if err := admin.QueryRow(context.Background(), `select count(*) from service_token_replay where tenant_id = $1`, validTenant).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows %d err %v", rows, err)
	}
}
