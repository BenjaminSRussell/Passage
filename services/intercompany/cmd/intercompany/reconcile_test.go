package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/ledgerrecon"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/migrations"
)

const (
	tenantA = "00000000-0000-0000-0000-0000000000f1"
	tenantB = "00000000-0000-0000-0000-0000000000f2"
	leA     = "00000000-0000-0000-0000-0000000000b1"
	leB     = "00000000-0000-0000-0000-0000000000b2"
)

type fakeRunner struct {
	calls []string
	fail  map[string]error
}

func (f *fakeRunner) Run(ctx context.Context, period, currency string, _ decimal.Decimal, key string) (domain.ReconRun, bool, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, p.TenantID+"|"+period+"|"+currency+"|"+key)
	if err := f.fail[p.TenantID]; err != nil {
		return domain.ReconRun{}, false, err
	}
	return domain.ReconRun{ID: "run", Period: period}, false, nil
}

type fakeSettings map[string]domain.Settings

func (f fakeSettings) GetSettings(ctx context.Context) (domain.Settings, error) {
	p, _ := tenant.FromContext(ctx)
	return f[p.TenantID], nil
}

func TestReconcilePeriods_AreTheLastAndTheCurrentMonth(t *testing.T) {
	got := reconcilePeriods(time.Date(2027, 1, 15, 3, 0, 0, 0, time.UTC))
	if got[0] != "2026-12" || got[1] != "2027-01" {
		t.Fatalf("periods = %v", got)
	}
}

func TestReconcileTenants_RunsEveryTenantThatSetACurrencyAndReportsFailures(t *testing.T) {
	now := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	settings := fakeSettings{tenantA: {ReconCurrency: "USD", FXTolerancePct: decimal.NewFromInt(1)}, tenantB: {}}
	r := &fakeRunner{}
	if err := reconcileTenants(context.Background(), settings, r, []string{tenantA, tenantB}, now); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[0] != tenantA+"|2026-10|USD|scheduled-recon:2026-10:2026-11-20" || !strings.HasSuffix(r.calls[1], "|2026-11|USD|scheduled-recon:2026-11:2026-11-20") {
		t.Fatalf("calls = %v", r.calls)
	}
	// A tenant whose ledgers cannot be read fails the job, but the other tenants still run.
	r = &fakeRunner{fail: map[string]error{tenantA: errors.New("ledger down")}}
	settings[tenantB] = domain.Settings{ReconCurrency: "EUR"}
	err := reconcileTenants(context.Background(), settings, r, []string{tenantA, tenantB}, now)
	if err == nil || !strings.Contains(err.Error(), "2 reconciliations") || len(r.calls) != 4 {
		t.Fatalf("err = %v, calls = %v", err, r.calls)
	}
}

func TestReconcileTenants_StoresARunFromTheLedgersUnderASystemPrincipalOncePerDay(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, UserID: "admin", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	for _, e := range []domain.EntityRecord{
		{Entity: "A", LegalEntityID: leA, LedgerID: "L-A", Currency: "USD"}, {Entity: "B", LegalEntityID: leB, LedgerID: "L-B", Currency: "USD"}} {
		if err := st.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceMatrix(ctx, domain.Matrix{{EntityA: "A", EntityB: "B", DueFrom: "1500", DueTo: "2500"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, domain.Settings{ChargeSettlementMode: domain.SettlementJournal, ReconCurrency: "USD", FXTolerancePct: domain.DefaultFXTolerancePct}); err != nil {
		t.Fatal(err)
	}
	fake := ledgerclient.NewFake()
	post := func(ledger, le, account string, credit bool) {
		if _, err := fake.Post(ctx, ledgerclient.Entry{LegalEntityID: le, LedgerID: ledger, DocumentID: ledger, PostingDate: "2026-11-05", Currency: "USD", IdempotencyKey: ledger, SourceType: "t",
			Lines: []ledgerclient.Line{{Account: account, Credit: credit, Amount: decimal.NewFromInt(100)}, {Account: "9999", Credit: !credit, Amount: decimal.NewFromInt(100)}}}); err != nil {
			t.Fatal(err)
		}
	}
	post("L-A", leA, "1500", false)
	post("L-B", leB, "2500", true)
	rec := ledgerrecon.New(st, st, st, fake, nil, nil, "")
	now := time.Date(2026, 11, 20, 2, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ { // the second night's job on the same day replays the stored runs
		if err := reconcileTenants(context.Background(), st, rec, []string{tenantA}, now); err != nil {
			t.Fatal(err)
		}
	}
	runs, _, err := st.ListReconRuns(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Source != "ledger" || runs[0].Matched != 1 || runs[0].CreatedBy != reconcileActor {
		t.Fatalf("runs = %+v", runs)
	}
}
