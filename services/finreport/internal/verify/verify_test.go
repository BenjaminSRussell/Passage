package verify_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/ledgerbalances"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/verify"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

type fakeLedger struct {
	nets   map[string]decimal.Decimal
	err    error
	entity string
	calls  int
	onNets func() // runs while the ledger "answers", to move the read model
}

func (f *fakeLedger) Nets(_ context.Context, _, entity string) (map[string]decimal.Decimal, error) {
	f.calls++
	f.entity = entity
	if f.onNets != nil {
		f.onNets()
	}
	return f.nets, f.err
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func setup(t *testing.T, entity string) (*verify.Service, *fakeLedger, *pgxpool.Pool, context.Context, string) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	base := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(base, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity, UserID: "u1",
		Duties: map[string]struct{}{"org.switcher.all-scope": {}}})
	ledger := ids.New()
	seed := func(period int, account, currency, net string) {
		err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into reporting_balance(tenant_id, ledger_id, period_year, period_no, quarter, account, combination, currency_type, net, legal_entity_id)
				values($1,$2,2026,$3,1,$4,'-',$5,$6::numeric,nullif($7,'')::uuid)`, testkit.TenantID(1), ledger, period, account, currency, net, entity)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed(1, "1000", "acct", "100.25")
	seed(2, "1000", "acct", "-40.25") // two periods add up
	seed(2, "4000", "acct", "-60")
	seed(2, "1000", "rpt", "999") // other currency types are not compared
	err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence, legal_entity_id) values($1,$2,9,nullif($3,'')::uuid)`,
			testkit.TenantID(1), ledger, entity)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	fl := &fakeLedger{nets: map[string]decimal.Decimal{"1000": d("60"), "4000": d("-60")}}
	return &verify.Service{Store: store.New(app), Ledger: fl}, fl, admin, ctx, ledger
}

func request(ledger, key string) verify.Request {
	return verify.Request{LedgerID: ledger, RequestedBy: "u1", IdempotencyKey: key, RequestHash: "h-" + ledger}
}

func TestRun_MatchedStoresACheckAndAnEvent(t *testing.T) {
	entity := ids.New()
	svc, fl, admin, ctx, ledger := setup(t, entity)
	v, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "matched" || v.AccountsCompared != 2 || v.DiffCount != 0 || v.HWMBefore != 9 || v.HWMAfter != 9 || v.LegalEntityID != entity || len(v.Diffs) != 0 {
		t.Fatalf("check = %+v", v)
	}
	if fl.entity != entity {
		t.Errorf("ledger asked for entity %q, want the owner %q", fl.entity, entity)
	}
	var events int
	var extension, status string
	err = dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*), max(payload->>'legalentityid'), max(payload->'data'->>'status') from outbox
			where type = 'dev.passage.reporting.balance.verified.v1'`).Scan(&events, &extension, &status)
	})
	if err != nil || events != 1 {
		t.Fatalf("outbox events %d, %v", events, err)
	}
	if extension != entity || status != "matched" {
		t.Errorf("event legalentityid %q status %q", extension, status)
	}
	got, err := svc.Get(ctx, v.ID)
	if err != nil || got.ID != v.ID || got.Status != "matched" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := svc.List(ctx, ledger, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if other, _ := svc.List(ctx, ids.New(), 10); len(other) != 0 {
		t.Errorf("another ledger lists %d checks", len(other))
	}
}

func TestRun_DifferenceIsStoredAsAResult(t *testing.T) {
	svc, fl, _, ctx, ledger := setup(t, ids.New())
	fl.nets = map[string]decimal.Decimal{"1000": d("61.5"), "4000": d("-60"), "5000": d("1.5")}
	v, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != "differs" || v.DiffCount != 2 || v.AccountsCompared != 3 || len(v.Diffs) != 2 {
		t.Fatalf("check = %+v", v)
	}
	if v.Diffs[0] != (store.VerificationDiff{Account: "1000", ReadModel: "60.000000", Ledger: "61.500000", Difference: "-1.500000"}) ||
		v.Diffs[1].Account != "5000" || v.Diffs[1].ReadModel != "0.000000" || v.Diffs[1].Difference != "-1.500000" {
		t.Errorf("diffs = %+v", v.Diffs)
	}
}

func TestRun_MarkMovedDuringTheLedgerCallIsInconclusive(t *testing.T) {
	svc, fl, admin, ctx, ledger := setup(t, ids.New())
	fl.nets = map[string]decimal.Decimal{"1000": d("1")}
	fl.onNets = func() {
		err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update reporting_hwm set posting_sequence = 10 where ledger_id = $1`, ledger)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	v, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil || v.Status != "inconclusive" || v.HWMBefore != 9 || v.HWMAfter != 10 {
		t.Fatalf("check = %+v, %v", v, err)
	}
}

// An entry that reached the consumer ahead of the mark proves the ledger is
// ahead of the read model: the check is inconclusive, not a mismatch.
func TestRun_EntriesWaitingAheadOfTheMarkAreInconclusive(t *testing.T) {
	entity := ids.New()
	svc, fl, admin, ctx, ledger := setup(t, entity)
	fl.nets = map[string]decimal.Decimal{"1000": d("70"), "4000": d("-70")} // the ledger already holds entry 11
	err := dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into reporting_entry_pending(tenant_id, ledger_id, posting_sequence, entry_id, entry, legal_entity_id)
			values($1,$2,11,$3,'{}',$4)`, testkit.TenantID(1), ledger, ids.New(), entity)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil || v.Status != "inconclusive" || v.EntriesPending != 1 || v.HWMBefore != 9 || v.HWMAfter != 9 || v.DiffCount != 2 {
		t.Fatalf("check = %+v, %v", v, err)
	}
}

// Two requests with one key that both miss the replay read store one check and
// one event; the loser answers the winner's check as a replay.
func TestRun_ConcurrentSameKeyStoresOneCheck(t *testing.T) {
	svc, _, admin, ctx, ledger := setup(t, ids.New())
	var wg sync.WaitGroup
	results := make([]store.Verification, 4)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = svc.Run(ctx, request(ledger, "same"))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil || results[i].ID != results[0].ID {
			t.Fatalf("run %d = %+v, %v (first %s)", i, results[i], err, results[0].ID)
		}
	}
	var checks, events int
	_ = dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select (select count(*) from balance_verification), (select count(*) from outbox where aggregate_type = 'balance_verification')`).Scan(&checks, &events)
	})
	if checks != 1 || events != 1 {
		t.Errorf("%d checks and %d events stored, want 1 and 1", checks, events)
	}
}

func TestRun_IdempotencyKeyReplaysWithoutReadingTheLedger(t *testing.T) {
	svc, fl, admin, ctx, ledger := setup(t, ids.New())
	first, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil || again.ID != first.ID || !again.Replayed || fl.calls != 1 {
		t.Fatalf("replay = %+v, %v, ledger calls %d", again, err, fl.calls)
	}
	reuse := request(ledger, "k1")
	reuse.RequestHash = "different"
	var p *httpx.Problem
	if _, err := svc.Run(ctx, reuse); !errors.As(err, &p) || p.Status != 422 || p.Code != store.CodeIdempotencyReuse {
		t.Errorf("reuse = %v", err)
	}
	var n int
	_ = dbx.WithTenantTx(ctx, admin, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from balance_verification`).Scan(&n)
	})
	if n != 1 {
		t.Errorf("%d checks stored, want 1", n)
	}
}

func TestRun_Refusals(t *testing.T) {
	svc, fl, _, ctx, ledger := setup(t, ids.New())
	var p *httpx.Problem

	if _, err := svc.Run(ctx, request(ids.New(), "k-unknown")); !errors.As(err, &p) || p.Status != 404 || p.Code != store.CodeLedgerNotFound {
		t.Errorf("unknown ledger = %v", err)
	}
	wrong := request(ledger, "k-entity")
	wrong.LegalEntityID = ids.New()
	if _, err := svc.Run(ctx, wrong); !errors.As(err, &p) || p.Status != 422 || p.Code != store.CodeLedgerEntity {
		t.Errorf("entity assertion = %v", err)
	}
	fl.err = &ledgerbalances.Error{Status: http.StatusServiceUnavailable}
	if _, err := svc.Run(ctx, request(ledger, "k-down")); !errors.As(err, &p) || p.Status != 502 || p.Code != verify.CodeLedgerUnavailable {
		t.Errorf("ledger down = %v", err)
	}
	if !strings.Contains(p.Detail, "nothing was stored") || strings.Contains(p.Detail, "status 503") {
		t.Errorf("502 detail %q should explain without echoing the cause", p.Detail)
	}
	fl.err = fmt.Errorf("%w: no grant", ledgerbalances.ErrSign)
	if _, err := svc.Run(ctx, request(ledger, "k-sign")); !errors.As(err, &p) || p.Status != 503 || p.Code != verify.CodeLedgerUnavailable {
		t.Errorf("unsigned = %v", err)
	}
	fl.err = &ledgerbalances.Error{Status: http.StatusNotFound}
	if _, err := svc.Run(ctx, request(ledger, "k-404")); !errors.As(err, &p) || p.Status != 422 {
		t.Errorf("ledger 404 = %v", err)
	}
	if list, _ := svc.List(ctx, ledger, 10); len(list) != 0 {
		t.Errorf("a refused check stored %d rows", len(list))
	}
	if _, err := (&verify.Service{Store: svc.Store}).Run(ctx, request(ledger, "k")); !errors.As(err, &p) || p.Status != 503 {
		t.Errorf("no ledger client = %v", err)
	}
	if _, err := svc.Get(ctx, ids.New()); !errors.As(err, &p) || p.Status != 404 || p.Code != store.CodeVerificationNotFound {
		t.Errorf("get unknown = %v", err)
	}
}

func TestRun_LegalEntityScopeAndTenantIsolation(t *testing.T) {
	entity := ids.New()
	svc, _, admin, ctx, ledger := setup(t, entity)
	v, err := svc.Run(ctx, request(ledger, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	otherEntity := ids.New()
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: otherEntity, UserID: "u2"})
	var p *httpx.Problem
	if _, err := svc.Get(other, v.ID); !errors.As(err, &p) || p.Status != 404 {
		t.Errorf("another entity reads the check: %v", err)
	}
	if list, err := svc.List(other, "", 10); err != nil || len(list) != 0 {
		t.Errorf("another entity lists %d checks, %v", len(list), err)
	}
	otherTenant := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "u3", Duties: map[string]struct{}{"org.switcher.all-scope": {}}})
	if list, err := svc.List(otherTenant, "", 10); err != nil || len(list) != 0 {
		t.Errorf("another tenant lists %d checks, %v", len(list), err)
	}
	// A key already holding a check of an entity outside the caller's scope is
	// refused, not a server error, and does not reveal that check.
	_, err = svc.Store.RecordVerification(other, store.VerificationSpec{LedgerID: ids.New(), LegalEntityID: otherEntity,
		Result: domain.Verify(nil, nil, 0, 0, 0), RequestedBy: "u2", IdempotencyKey: "k1", RequestHash: "h-" + ledger})
	if !errors.As(err, &p) || p.Status != 422 || p.Code != store.CodeIdempotencyReuse {
		t.Errorf("key of another entity = %v", err)
	}
	testkit.AssertTenantRLS(t, admin)
}
