package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

const testTenant = "00000000-0000-7000-8000-000000000001"

func TestReconciliation_ReturnsDayWithFailedCount(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testTenant, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	for _, r := range []struct{ le, semantic, source string }{
		{"11111111-0000-4000-8000-000000000001", "100.00", "100.00"},
		{"22222222-0000-4000-8000-000000000002", "100.00", "99.00"},
	} {
		if _, _, err := st.RecordReconciliation(ctx, domain.Reconciliation{MetricID: "revenue", LegalEntityID: r.le, Date: "2026-10-26",
			Currency: "EUR", SemanticValue: decimal.RequireFromString(r.semantic), SourceValue: decimal.RequireFromString(r.source),
			Tolerance: domain.DefaultTolerance()}); err != nil {
			t.Fatal(err)
		}
	}
	router := httpx.NewRouter()
	New(st).Register(router)
	h := tenant.Middleware(router)
	finance := map[string]string{tenant.HeaderDuties: DutyFinance}

	rec := do(t, h, http.MethodGet, "/v1/analytics/reconciliation?date=2026-10-26", "", finance)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got ReconciliationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.FailedCount != 1 || len(got.Items) != 2 || got.Items[1].Status != domain.ReconFailed {
		t.Fatalf("response = %+v", got)
	}
	// Money travels as decimal strings, never JSON numbers.
	if !strings.Contains(rec.Body.String(), `"delta":"1"`) {
		t.Fatalf("delta must be a decimal string: %s", rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/v1/analytics/reconciliation?date=2026-10-26&metric=cogs", "", finance)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 0 || got.FailedCount != 0 {
		t.Fatalf("cogs only = %+v, %v", got, err)
	}
	rec = do(t, h, http.MethodGet, "/v1/analytics/reconciliation?date=2026-10-26&metric=Revenue%27--", "", finance)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad metric: status %d", rec.Code)
	}
}
