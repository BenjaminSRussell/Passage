package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/events"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
	"github.com/BenjaminSRussell/passage/services/tax/migrations"
)

type glMovements map[string]string

func (g glMovements) Movements(context.Context, time.Time, time.Time) (map[string]decimal.Decimal, error) {
	out := map[string]decimal.Decimal{}
	for k, v := range g {
		out[k] = decimal.RequireFromString(v)
	}
	return out, nil
}

var (
	outputKey = domain.MovementKey("VAT19", domain.DirOutput)
	inputKey  = domain.MovementKey("VAT19", domain.DirInput)
	january   = domain.Period{From: domain.Day("2026-01-01"), To: domain.Day("2026-01-31")}
	config    = store.LedgerConfig{LedgerID: "7c1f6a52-0000-4000-8000-000000000001", Accounts: map[string]string{outputKey: "2200", inputKey: "1400"}}
)

func gl(m glMovements) func(store.LedgerConfig) domain.GLMovement {
	return func(store.LedgerConfig) domain.GLMovement { return m }
}

func setup(t *testing.T) (*store.Store, *pgxpool.Pool, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ctx := testkit.Ctx(ids.New(), "tax.settlement.run")
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = ids.New()
	ctx = tenant.With(ctx, p)
	s := store.New(app)
	if err := s.SetLedgerConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	return s, admin, ctx
}

func txn(date, code, direction, amount string) domain.Txn {
	return domain.Txn{ID: ids.New(), Code: code, Direction: direction, TaxDate: domain.Day(date), Amount: domain.D(amount)}
}

func settlementCode(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestSettleRun_ReconcilesMarksAndDeltas(t *testing.T) {
	s, admin, ctx := setup(t)
	first := []domain.Txn{txn("2026-01-10", "VAT19", domain.DirOutput, "100.01"), txn("2026-01-20", "VAT19", domain.DirOutput, "50.49"), txn("2026-01-12", "VAT19", domain.DirInput, "30.25")}
	if n, err := s.RecordTransactions(ctx, first); err != nil || n != 3 {
		t.Fatalf("record: %d %v", n, err)
	}
	if n, err := s.RecordTransactions(ctx, first[:1]); err != nil || n != 0 {
		t.Fatalf("re-recording a transaction inserted %d rows (%v)", n, err)
	}

	run, err := s.SettleRun(ctx, january, gl(glMovements{outputKey: "150.50", inputKey: "30.25"}))
	if err != nil {
		t.Fatal(err)
	}
	var persistedLedger string
	var persistedAccounts map[string]string
	if err := admin.QueryRow(context.Background(), `select ledger_id::text, accounts from tax_settlement_run where id = $1`, run.ID).Scan(&persistedLedger, &persistedAccounts); err != nil ||
		persistedLedger != config.LedgerID || persistedAccounts[outputKey] != "2200" || run.LedgerID != config.LedgerID {
		t.Fatalf("run did not keep its config: %q %v (%v)", persistedLedger, persistedAccounts, err)
	}
	if run.Version != 1 || run.NetAmount.String() != "120.25" || len(run.TransactionIDs) != 3 {
		t.Fatalf("unexpected run %+v", run)
	}
	var marked int
	if err := admin.QueryRow(context.Background(), `select count(*) from tax_transaction where settlement_run_id = $1`, run.ID).Scan(&marked); err != nil || marked != 3 {
		t.Fatalf("marked %d transactions (%v), want 3", marked, err)
	}

	if _, err := s.SettleRun(ctx, january, gl(glMovements{outputKey: "150.50", inputKey: "30.25"})); settlementCode(err) != domain.CodeNothingToSettle {
		t.Fatalf("repeat run: %v, want %s", err, domain.CodeNothingToSettle)
	}

	if _, err := s.RecordTransactions(ctx, []domain.Txn{txn("2026-01-28", "VAT19", domain.DirOutput, "7.00")}); err != nil {
		t.Fatal(err)
	}
	delta, err := s.SettleRun(ctx, january, gl(glMovements{outputKey: "157.50", inputKey: "30.25"}))
	if err != nil {
		t.Fatal(err)
	}
	if delta.Version != 2 || delta.NetAmount.String() != "7" || len(delta.TransactionIDs) != 1 {
		t.Fatalf("unexpected delta %+v", delta)
	}
}

func TestSettleRun_MismatchRollsBackAndLeavesTransactionsOpen(t *testing.T) {
	s, admin, ctx := setup(t)
	if _, err := s.RecordTransactions(ctx, []domain.Txn{txn("2026-01-10", "VAT19", domain.DirOutput, "100.00")}); err != nil {
		t.Fatal(err)
	}
	_, err := s.SettleRun(ctx, january, gl(glMovements{outputKey: "100.01"}))
	if settlementCode(err) != domain.CodeSettlementMismatch {
		t.Fatalf("got %v, want %s", err, domain.CodeSettlementMismatch)
	}
	var runs, open int
	_ = admin.QueryRow(context.Background(), `select (select count(*) from tax_settlement_run), (select count(*) from tax_transaction where settlement_run_id is null)`).Scan(&runs, &open)
	if runs != 0 || open != 1 {
		t.Fatalf("runs %d open %d, want 0 and 1", runs, open)
	}
}

func TestSettleRun_RefusesUnmappedCodeMissingConfigAndMissingEntity(t *testing.T) {
	s, _, ctx := setup(t)
	if _, err := s.RecordTransactions(ctx, []domain.Txn{txn("2026-01-10", "VAT19", domain.DirOutput, "1.00"), txn("2026-01-10", "VAT7", domain.DirOutput, "1.00")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleRun(ctx, january, gl(glMovements{})); settlementCode(err) != domain.CodeValidation {
		t.Fatalf("unmapped code: %v, want %s", err, domain.CodeValidation)
	}
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = ids.New()
	if _, err := s.SettleRun(tenant.With(ctx, p), january, gl(glMovements{})); !errors.Is(err, store.ErrNoLedgerConfig) {
		t.Fatalf("entity without config: %v", err)
	}
	p.LegalEntityID = ""
	if _, err := s.SettleRun(tenant.With(ctx, p), january, gl(glMovements{})); !errors.Is(err, store.ErrNoLegalEntity) {
		t.Fatalf("no legal entity: %v", err)
	}
}

func TestRecordTransactions_IdsAreScopedToTheLegalEntity(t *testing.T) {
	s, admin, ctx := setup(t)
	shared := txn("2026-01-10", "VAT19", domain.DirOutput, "10.00")
	if n, err := s.RecordTransactions(ctx, []domain.Txn{shared}); err != nil || n != 1 {
		t.Fatalf("first entity: %d %v", n, err)
	}

	p, _ := tenant.FromContext(ctx)
	firstEntity := p.LegalEntityID
	p.LegalEntityID = ids.New()
	otherCtx := tenant.With(ctx, p)
	if n, err := s.RecordTransactions(otherCtx, []domain.Txn{shared}); err != nil || n != 1 {
		t.Fatalf("a second legal entity could not record the same id: %d %v", n, err)
	}
	var rows int
	_ = admin.QueryRow(context.Background(), `select count(*) from tax_transaction where id = $1`, shared.ID).Scan(&rows)
	if rows != 2 {
		t.Fatalf("%d rows for the shared id, want one per legal entity", rows)
	}

	if n, err := s.RecordTransactions(ctx, []domain.Txn{shared}); err != nil || n != 0 {
		t.Fatalf("identical replay: %d %v", n, err)
	}
	changed := shared
	changed.Amount = domain.D("99.00")
	if _, err := s.RecordTransactions(ctx, []domain.Txn{changed}); settlementCode(err) != domain.CodeValidation {
		t.Fatalf("replay with different content: %v, want %s", err, domain.CodeValidation)
	}
	var amount string
	_ = admin.QueryRow(context.Background(), `select amount::text from tax_transaction where id = $1 and legal_entity_id = $2`, shared.ID, firstEntity).Scan(&amount)
	if amount != "10.000000" {
		t.Fatalf("first entity's row is %s after the rejected replay", amount)
	}
}

func TestSetLedgerConfig_ReplacesPerLegalEntity(t *testing.T) {
	s, admin, ctx := setup(t)
	replacement := store.LedgerConfig{LedgerID: config.LedgerID, Accounts: map[string]string{outputKey: "2300"}}
	if err := s.SetLedgerConfig(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	var rows int
	var account string
	_ = admin.QueryRow(context.Background(), `select count(*), min(accounts->>$1) from tax_ledger_config`, outputKey).Scan(&rows, &account)
	if rows != 1 || account != "2300" {
		t.Fatalf("rows %d account %q", rows, account)
	}
}

func TestRecommit_QueuesWithEventAndDrains(t *testing.T) {
	s, admin, ctx := setup(t)
	first, err := s.EnqueueRecommit(ctx, "avalara", "provider status 503", json.RawMessage(`{"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnqueueRecommit(ctx, "avalara", "timeout", json.RawMessage(`{"n":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var events_ int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and aggregate_id in ($2, $3)`, events.ProviderFallback, first, second).Scan(&events_); err != nil || events_ != 2 {
		t.Fatalf("outbox events %d (%v), want 2", events_, err)
	}

	sum, err := s.Recommit(ctx, 10, func(_ context.Context, item store.Recommit) (json.RawMessage, error) {
		if item.ID == second {
			return nil, errors.New("provider still down")
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil || sum != (store.RecommitSummary{Processed: 2, Completed: 1, Failed: 1}) {
		t.Fatalf("summary %+v err %v", sum, err)
	}
	var done, pending, attempts int
	_ = admin.QueryRow(context.Background(), `select count(*) filter (where status = 'done'), count(*) filter (where status = 'pending'), max(attempts) from tax_recommit_queue`).Scan(&done, &pending, &attempts)
	if done != 1 || pending != 1 || attempts != 1 {
		t.Fatalf("done %d pending %d attempts %d", done, pending, attempts)
	}

	again, err := s.Recommit(ctx, 10, func(context.Context, store.Recommit) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	if err != nil || again.Completed != 1 {
		t.Fatalf("second drain %+v %v", again, err)
	}
}

func TestRecommit_AStuckEntryIsAbandonedAndNeverBlocksNewerOnes(t *testing.T) {
	s, admin, ctx := setup(t)
	stuck, err := s.EnqueueRecommit(ctx, "avalara", "down", json.RawMessage(`{"n":0}`))
	if err != nil {
		t.Fatal(err)
	}
	failStuck := func(_ context.Context, item store.Recommit) (json.RawMessage, error) {
		if item.ID == stuck {
			return nil, errors.New("always failing")
		}
		return json.RawMessage(`{"ok":true}`), nil
	}
	for range store.MaxRecommitAttempts {
		if _, err := s.Recommit(ctx, 1, failStuck); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	_ = admin.QueryRow(context.Background(), `select status from tax_recommit_queue where id = $1`, stuck).Scan(&status)
	if status != "failed" {
		t.Fatalf("stuck entry status %q after %d attempts, want failed", status, store.MaxRecommitAttempts)
	}
	sum, err := s.Recommit(ctx, 10, failStuck)
	if err != nil || sum.Processed != 0 {
		t.Fatalf("abandoned entry was taken again: %+v %v", sum, err)
	}

	retried, _ := s.EnqueueRecommit(ctx, "avalara", "down", json.RawMessage(`{"n":1}`))
	if _, err := s.Recommit(ctx, 1, func(context.Context, store.Recommit) (json.RawMessage, error) {
		return nil, errors.New("fails once")
	}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.EnqueueRecommit(ctx, "avalara", "down", json.RawMessage(`{"n":2}`))
	var taken []string
	if _, err := s.Recommit(ctx, 1, func(_ context.Context, item store.Recommit) (json.RawMessage, error) {
		taken = append(taken, item.ID)
		return json.RawMessage(`{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(taken) != 1 || taken[0] != fresh {
		t.Fatalf("batch of one took %v, want the fresh entry %s ahead of the already-failed %s", taken, fresh, retried)
	}
}

func TestTotals_SumPerCodeAndDirection(t *testing.T) {
	s, _, ctx := setup(t)
	_, err := s.RecordTransactions(ctx, []domain.Txn{txn("2026-01-10", "VAT19", domain.DirOutput, "10.10"), txn("2026-01-11", "VAT19", domain.DirOutput, "5.05"), txn("2026-02-01", "VAT19", domain.DirOutput, "99")})
	if err != nil {
		t.Fatal(err)
	}
	totals, err := s.Totals(ctx, january)
	if err != nil || len(totals) != 1 || totals[0].Amount.String() != "15.15" || totals[0].Count != 2 {
		t.Fatalf("totals %+v err %v", totals, err)
	}
}

func TestAppRole_CrossTenantRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	testkit.CrossTenant(t, app, []testkit.RLSCase{
		testkit.OutboxCase(),
		{
			Table:  "tax_settlement_run",
			Insert: `insert into tax_settlement_run (tenant_id, id, legal_entity_id, period_from, period_to, version, output_tax_total, input_tax_total, net_amount, lines, ledger_id, accounts) values ($1, $2, $3, '2026-01-01', '2026-01-31', 1, 0, 0, 0, '[]', $2, '{}')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID}
			},
			LegalEntity: true,
			NoUpdate:    true,
		},
		{
			Table:  "tax_transaction",
			Insert: `insert into tax_transaction (tenant_id, id, legal_entity_id, code, direction, tax_date, amount) values ($1, $2, $3, 'VAT19', 'output', '2026-01-10', 1)`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID}
			},
			LegalEntity: true,
		},
		{
			Table:  "tax_ledger_config",
			Insert: `insert into tax_ledger_config (tenant_id, legal_entity_id, ledger_id, accounts) values ($1, $3, $2, '{}')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID}
			},
			LegalEntity: true,
		},
		{
			Table:  "tax_recommit_queue",
			Insert: `insert into tax_recommit_queue (tenant_id, id, legal_entity_id, provider, reason, request) values ($1, $2, $3, 'avalara', 'timeout', '{}')`,
			Args: func(tenantID, entityID string) []any {
				return []any{tenantID, ids.New(), entityID}
			},
			LegalEntity: true,
		},
	})
}
