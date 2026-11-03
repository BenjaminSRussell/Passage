package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
	"github.com/BenjaminSRussell/passage/services/ledger/migrations"
)

var (
	testPostingDate = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	testToday       = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	testTrace       = strings.Repeat("a", 32)
)

type env struct {
	ctx    context.Context
	store  *Postgres
	pool   *pgxpool.Pool // application role (passage_app): RLS and grants apply
	admin  *pgxpool.Pool // migration owner
	ledger Ledger
	tenant string
	le     string
}

func principalCtx(tenantID, legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, UserID: "test-user", Duties: testkit.ScopeDuties(legalEntityID)})
}

// setup migrates a fresh schema as the owner, then works through the
// application role so row-level security and the FR-11.6 grants apply. It
// creates a chart with three accounts (one a vendor reconciliation account)
// and a ledger.
func setup(t *testing.T) (env, context.Context) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	base := context.Background()
	if err := dbx.Migrate(base, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	tenantID := testkit.TenantID(1)
	le := ids.New()
	ctx := principalCtx(tenantID, le)
	s := New(pool)
	for num, a := range map[string]Account{
		"6010": {Name: "Freight expense", Type: "expense", Postable: true},
		"2100": {Name: "Vendor payable", Type: "liability", Postable: true},
		"2110": {Name: "Vendor control", Type: "liability", Postable: true, ReconType: "vendor"},
	} {
		if _, _, err := s.PutAccount(ctx, "STD", num, a); err != nil {
			t.Fatalf("put account %s: %v", num, err)
		}
	}
	l, err := s.CreateLedger(ctx, Ledger{LegalEntityID: le, Code: "GL", Name: "General ledger", ChartCode: "STD",
		Currency: "USD", DimensionAttributes: []string{"DEPT", "CC"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	return env{ctx: ctx, store: s, pool: pool, admin: admin, ledger: l, tenant: tenantID, le: le}, ctx
}

// vendorInvoice builds a balanced two-line entry of amount USD.
func vendorInvoice(ledgerID, key, amount string) PostInput {
	hash, _ := domain.HashPayload(map[string]string{"key": key, "amount": amount})
	return PostInput{
		LedgerID: ledgerID, DocumentID: ids.New(), PostingDate: testPostingDate, Description: "Vendor invoice " + key,
		Source: ledgerevents.Source{System: "ap", Type: "vendor_invoice", ID: key, URI: "passage://ap/invoices/" + key},
		Lines: []domain.LineInput{
			{Account: "6010", Side: domain.SideDebit, Amount: decimal.RequireFromString(amount), Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"}},
			{Account: "2100", Side: domain.SideCredit, Amount: decimal.RequireFromString(amount), Currency: "USD"},
		},
		Key: key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday,
	}
}

func TestPostgres_PostIsIdempotentAndBalances(t *testing.T) {
	e, _ := setup(t)
	in := vendorInvoice(e.ledger.ID, "INV-1", "1250.00")
	first, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if first.Idempotency.Replayed || len(first.Entries) != 1 || first.Entries[0].PostingSequence != 1 {
		t.Fatalf("first post: %+v", first)
	}
	replay, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Idempotency.Replayed || replay.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("replay must return the original entry: %+v", replay)
	}
	changed := vendorInvoice(e.ledger.ID, "INV-1", "1250.01")
	changed.Hash, _ = domain.HashPayload("different")
	_, err = e.store.PostEntry(e.ctx, changed)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != "LEDGER_IDEMPOTENCY_CONFLICT" || p.Status != http.StatusConflict {
		t.Fatalf("want LEDGER_IDEMPOTENCY_CONFLICT, got %v", err)
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if err != nil {
		t.Fatalf("trial balance: %v", err)
	}
	if !tb.Balanced || tb.Debit != "1250.00" || tb.Credit != "1250.00" || len(tb.Rows) != 2 {
		t.Fatalf("trial balance: %+v", tb)
	}
}

func TestPostgres_RejectedEntryWritesNothing(t *testing.T) {
	e, _ := setup(t)
	in := vendorInvoice(e.ledger.ID, "BAD-1", "10.00")
	in.Lines[1].Amount = decimal.RequireFromString("9.99")
	_, err := e.store.PostEntry(e.ctx, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_UNBALANCED" {
		t.Fatalf("want LEDGER_UNBALANCED, got %v", err)
	}
	ok, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "OK-1", "10.00"))
	if err != nil {
		t.Fatalf("post after rejection: %v", err)
	}
	if ok.Entries[0].PostingSequence != 1 {
		t.Fatalf("a rejected post must not consume a sequence number, got %d", ok.Entries[0].PostingSequence)
	}
}

func TestPostgres_ReversalNegatesOnce(t *testing.T) {
	e, _ := setup(t)
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "INV-R", "300.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	origID := orig.Entries[0].ID
	hash, _ := domain.HashPayload("reverse " + origID)
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: origID, Key: "REV-1", Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	r := rev.Entries[0]
	if r.ReversalOfEntryID == nil || *r.ReversalOfEntryID != origID || len(r.Lines) != 2 {
		t.Fatalf("reversal entry: %+v", r)
	}
	for i, l := range r.Lines {
		o := orig.Entries[0].Lines[i]
		if l.DebitCredit == o.DebitCredit || l.Amount != o.Amount || l.ReversalOfLineID == nil || *l.ReversalOfLineID != o.ID {
			t.Fatalf("line %d is not the exact negation: %+v vs %+v", i, l, o)
		}
	}
	hash2, _ := domain.HashPayload("reverse again")
	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: origID, Key: "REV-2", Hash: hash2, Today: testToday})
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_REVERSAL_ALREADY_REVERSED" {
		t.Fatalf("want LEDGER_REVERSAL_ALREADY_REVERSED, got %v", err)
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if err != nil || !tb.Balanced || tb.Net != "0.00" {
		t.Fatalf("after reversal the trial balance must net to zero: %+v %v", tb, err)
	}
}

func TestPostgres_ConcurrentPostingIsGapFreeAndRebuildMatches(t *testing.T) {
	e, _ := setup(t)
	const workers, perWorker = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("W%d-%d", w, i)
				amount := fmt.Sprintf("%d.%02d", 1+i, w)
				if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, key, amount)); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent post: %v", err)
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if err != nil || !tb.Balanced {
		t.Fatalf("trial balance after load: %+v %v", tb, err)
	}
	rebuilt, err := e.store.RebuildBalances(e.ctx, e.ledger.ID)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if rebuilt.RowsDiffering != 0 || rebuilt.HighWaterSequence != workers*perWorker {
		t.Fatalf("rebuild must match the projection and cover every sequence: %+v", rebuilt)
	}
}

func TestPostgres_RebuildReportsDrift(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "DRIFT-1", "5.00")); err != nil {
		t.Fatalf("post: %v", err)
	}
	err := dbx.WithTenantTx(e.ctx, e.store.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx, `update balance set debit = debit + 1`)
		return err
	})
	if err != nil {
		t.Fatalf("corrupt projection: %v", err)
	}
	res, err := e.store.RebuildBalances(e.ctx, e.ledger.ID)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if res.RowsDiffering == 0 {
		t.Fatal("rebuild must report the drifted rows")
	}
	again, err := e.store.RebuildBalances(e.ctx, e.ledger.ID)
	if err != nil || again.RowsDiffering != 0 {
		t.Fatalf("second rebuild must be clean: %+v %v", again, err)
	}
}

func TestPostgres_JournalIsAppendOnly(t *testing.T) {
	e, _ := setup(t)
	res, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "AO-1", "7.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	id := res.Entries[0].ID
	// The application role holds INSERT and SELECT only (FR-11.6).
	for _, stmt := range []string{
		`update journal_entry set description = 'changed' where id = $1`,
		`delete from journal_line where entry_id = $1`,
	} {
		err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(e.ctx, stmt, id)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("app role %q must be denied, got %v", stmt, err)
		}
	}
	// The owner (and any superuser) is stopped by the trigger.
	for _, stmt := range []string{
		`update journal_entry set description = 'changed' where id = $1`,
		`update journal_line set amount_acct = 0 where entry_id = $1`,
		`delete from journal_line where entry_id = $1`,
		`delete from journal_entry where id = $1`,
	} {
		_, err := e.admin.Exec(e.ctx, stmt, id)
		if err == nil || !strings.Contains(err.Error(), "LEDGER_APPEND_ONLY") {
			t.Fatalf("owner %q must raise LEDGER_APPEND_ONLY, got %v", stmt, err)
		}
	}
	if _, err := e.admin.Exec(e.ctx, `truncate journal_line, journal_entry cascade`); err == nil || !strings.Contains(err.Error(), "LEDGER_APPEND_ONLY") {
		t.Fatalf("TRUNCATE must raise LEDGER_APPEND_ONLY, got %v", err)
	}
}

func TestPostgres_DatabaseRejectsUnbalancedEntryAtCommit(t *testing.T) {
	e, _ := setup(t)
	posted, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "DB-1", "10.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	line := posted.Entries[0].Lines[0]
	// Bypass the domain check: insert an entry whose single line leaves it unbalanced.
	entryID := ids.New()
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(e.ctx, `insert into journal_entry (tenant_id, id, legal_entity_id, ledger_id, document_id, voucher, posting_sequence,
				posting_date, period_key, source_system, source_type, source_id, idempotency_key, payload_hash, trace_id)
			values ($1, $2, $3, $4, $5, 'GL-X', 999, '2026-03-10', '2026-03', 'test', 'raw', 'x', 'raw-key', $6, $7)`,
			e.tenant, entryID, e.le, e.ledger.ID, ids.New(), make([]byte, 32), testTrace); err != nil {
			return err
		}
		_, err := tx.Exec(e.ctx, `insert into journal_line (tenant_id, id, legal_entity_id, entry_id, line_number, main_account_id,
				dimension_combination_id, debit_credit, currency_txn, amount_txn, amount_acct)
			select tenant_id, $1, legal_entity_id, $2, 1, main_account_id, dimension_combination_id, 'D', currency_txn, 5, 5
			from journal_line where id = $3`, ids.New(), entryID, line.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "LEDGER_UNBALANCED") {
		t.Fatalf("commit of an unbalanced entry must fail with LEDGER_UNBALANCED, got %v", err)
	}
	if _, err := e.store.GetEntry(e.ctx, entryID); problemCode(err) != "LEDGER_ENTRY_NOT_FOUND" {
		t.Fatalf("the rejected entry must not exist, got %v", err)
	}
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestPostgres_TenantAndLegalEntityIsolation(t *testing.T) {
	e, _ := setup(t)
	posted, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "ISO-1", "42.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	id := posted.Entries[0].ID
	putReason(t, e, "ISO01", ReasonCode{Name: "Isolation", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true})
	otherTenant := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.GetEntry(otherTenant, id); problemCode(err) != "LEDGER_ENTRY_NOT_FOUND" {
		t.Fatalf("another tenant must not see the entry, got %v", err)
	}
	if _, err := e.store.TrialBalance(otherTenant, BalanceFilter{LedgerID: e.ledger.ID}); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another tenant must not see the ledger, got %v", err)
	}
	if _, err := e.store.PostEntry(otherTenant, vendorInvoice(e.ledger.ID, "ISO-2", "1.00")); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("another tenant must not post to the ledger, got %v", err)
	}
	otherEntity := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetEntry(otherEntity, id); problemCode(err) != "LEDGER_ENTRY_NOT_FOUND" {
		t.Fatalf("a caller scoped to another legal entity must not see the entry, got %v", err)
	}
	// Direct table reads through RLS: the other tenant sees no rows at all.
	for _, table := range []string{"journal_entry", "journal_line", "balance", "outbox", "idempotency_keys", "ledger", "main_account", "dimension_combination", "reason_code", "ledger_set", "ledger_set_member", "ledger_account_mapping"} {
		var n int
		err := dbx.WithTenantTx(otherTenant, e.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(otherTenant, `select count(*) from `+table).Scan(&n)
		})
		if err != nil || n != 0 {
			t.Fatalf("%s: tenant 2 sees %d rows (err %v); want 0", table, n, err)
		}
	}
}

func TestPostgres_PostWritesOutboxInTheSameTransaction(t *testing.T) {
	e, _ := setup(t)
	bad := vendorInvoice(e.ledger.ID, "OB-0", "10.00")
	bad.Lines[1].Amount = decimal.RequireFromString("9.00")
	if _, err := e.store.PostEntry(e.ctx, bad); err == nil {
		t.Fatal("unbalanced entry must be rejected")
	}
	posted, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "OB-1", "10.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: posted.Entries[0].ID, Key: "OB-R", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	var types []string
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(e.ctx, `select type from outbox order by created_at, type`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			if err := rows.Scan(&typ); err != nil {
				return err
			}
			types = append(types, typ)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{ledgerevents.JournalPosted: 2, ledgerevents.JournalReversed: 1}
	got := map[string]int{}
	for _, typ := range types {
		got[typ]++
	}
	if len(types) != 3 || got[ledgerevents.JournalPosted] != want[ledgerevents.JournalPosted] || got[ledgerevents.JournalReversed] != want[ledgerevents.JournalReversed] {
		t.Fatalf("outbox types = %v; want two journal.posted and one journal.reversed, none for the rejected post", types)
	}
}

func TestPostgres_ReconciliationAccountsAcceptSubledgerPostingsOnly(t *testing.T) {
	e, _ := setup(t)
	sub := vendorInvoice(e.ledger.ID, "RC-1", "50.00")
	sub.Lines[1].Account = "2110"
	if _, err := e.store.PostEntry(e.ctx, sub); err != nil {
		t.Fatalf("an AP posting to the vendor reconciliation account must be accepted: %v", err)
	}
	manual := vendorInvoice(e.ledger.ID, "RC-2", "50.00")
	manual.Lines[1].Account = "2110"
	manual.Source = ledgerevents.Source{System: domain.ManualSourceSystem, Type: domain.ManualSourceType, ID: "MJ-1"}
	_, err := e.store.PostEntry(e.ctx, manual)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_RECON_ACCOUNT_MANUAL" || *rej.Violations[0].LineIndex != 1 {
		t.Fatalf("a manual journal on the reconciliation account must be refused, got %v", err)
	}
}

func TestPostgres_ReversalDateAndReinstatement(t *testing.T) {
	e, _ := setup(t)
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "RD-1", "80.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	origID := orig.Entries[0].ID
	early := testPostingDate.AddDate(0, 0, -1)
	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: origID, PostingDate: &early, Key: "RD-R0", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday})
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_DATE_INVALID" {
		t.Fatalf("reversal before the original date must be LEDGER_DATE_INVALID, got %v", err)
	}
	later := testPostingDate.AddDate(0, 1, 0)
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: origID, PostingDate: &later, Key: "RD-R1", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	r := rev.Entries[0]
	if r.PostingDate != later.Format(time.DateOnly) || r.PeriodKey != "2026-04" {
		t.Fatalf("reversal must post on the requested date: %+v", r)
	}
	for i, l := range r.Lines {
		if l.Account != orig.Entries[0].Lines[i].Account || l.Account == "" {
			t.Fatalf("reversing line %d must name the original account, got %q", i, l.Account)
		}
	}
	// Reversing the reversal re-instates the original (FR-11.8).
	again, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: r.ID, Key: "RD-R2", Hash: make([]byte, 32), TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	if *again.Entries[0].ReversalOfEntryID != r.ID || again.Entries[0].Lines[0].DebitCredit != orig.Entries[0].Lines[0].DebitCredit {
		t.Fatalf("reinstating entry must link to the reversal and restore the original sides: %+v", again.Entries[0])
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, AccountFrom: "6010", AccountTo: "6010"})
	if err != nil || len(tb.Rows) != 1 || tb.Rows[0].Net != "80.00" || tb.Rows[0].Debit != "160.00" || tb.Rows[0].Credit != "80.00" {
		t.Fatalf("after post, reverse and reinstate account 6010 must net 80.00: %+v %v", tb, err)
	}
}

func TestIdempotencyRace_MapsKeyCollisionsOnly(t *testing.T) {
	keyErr := &pgconn.PgError{Code: uniqueViolation, ConstraintName: "idempotency_keys_pkey"}
	if p := problemCode(idempotencyRace(fmt.Errorf("wrapped: %w", keyErr))); p != CodeIdempotencyInProgress {
		t.Fatalf("idempotency key collision must map to %s, got %q", CodeIdempotencyInProgress, p)
	}
	other := &pgconn.PgError{Code: uniqueViolation, ConstraintName: "journal_entry_tenant_id_ledger_id_voucher_key"}
	if got := idempotencyRace(other); got != error(other) {
		t.Fatalf("other unique violations must pass through, got %v", got)
	}
	if idempotencyRace(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestPostgres_TrialBalanceByDimension(t *testing.T) {
	e, _ := setup(t)
	february := vendorInvoice(e.ledger.ID, "D-0", "1000.00")
	february.PostingDate = time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)
	other := vendorInvoice(e.ledger.ID, "D-3", "7.00")
	other.Lines[0].Dimensions = map[string]string{"DEPT": "D200", "CC": "C1"}
	for _, in := range []PostInput{february, vendorInvoice(e.ledger.ID, "D-1", "100.00"), vendorInvoice(e.ledger.ID, "D-2", "40.00"), other} {
		if _, err := e.store.PostEntry(e.ctx, in); err != nil {
			t.Fatalf("post %s: %v", in.Key, err)
		}
	}
	march := BalanceFilter{LedgerID: e.ledger.ID, PeriodFrom: "2026-03", PeriodTo: "2026-03"}

	f := march
	f.Dimensions = map[string]string{"DEPT": "D100"}
	tb, err := e.store.TrialBalance(e.ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if !tb.Filtered || len(tb.Rows) != 1 || tb.Rows[0].Account != "6010" || tb.Rows[0].Debit != "140.00" || tb.Rows[0].LineCount != 2 || tb.Balanced {
		t.Fatalf("filtered balance: %+v", tb)
	}

	// An empty value keeps the combinations without the attribute: the payable lines carry no DEPT.
	f.Dimensions = map[string]string{"DEPT": ""}
	if tb, err = e.store.TrialBalance(e.ctx, f); err != nil || len(tb.Rows) != 1 || tb.Rows[0].Account != "2100" || tb.Rows[0].Credit != "147.00" {
		t.Fatalf("filter on a missing attribute: %+v %v", tb, err)
	}

	// Grouping keeps the period range: February's 1,000 is outside March.
	f = march
	f.GroupBy = []string{"DEPT", "CC"}
	if tb, err = e.store.TrialBalance(e.ctx, f); err != nil {
		t.Fatal(err)
	}
	if tb.Filtered || !tb.Balanced || tb.Debit != "147.00" || tb.Credit != "147.00" || strings.Join(tb.GroupBy, ",") != "DEPT,CC" {
		t.Fatalf("grouped totals must equal the plain trial balance: %+v", tb)
	}
	got := map[string]string{}
	for _, r := range tb.Rows {
		got[r.Account+"/"+r.Dimensions["DEPT"]+"/"+r.Dimensions["CC"]] = r.Debit + "|" + r.Credit
	}
	want := map[string]string{"2100//": "0.00|147.00", "6010/D100/": "140.00|0.00", "6010/D200/C1": "7.00|0.00"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("grouped rows: got %v want %v", got, want)
	}
	plain, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if err != nil || plain.Debit != "1147.00" || !plain.Balanced || plain.GroupBy != nil || plain.Rows[0].Dimensions != nil {
		t.Fatalf("plain trial balance: %+v %v", plain, err)
	}

	// Attributes the ledger does not define are refused, not answered with an empty result.
	for _, bad := range []BalanceFilter{
		{LedgerID: e.ledger.ID, Dimensions: map[string]string{"PROJECT": "P1"}},
		{LedgerID: e.ledger.ID, GroupBy: []string{"DEPT", "PROJECT"}},
	} {
		if _, err := e.store.TrialBalance(e.ctx, bad); !isProblem(err, http.StatusBadRequest, "COM-0001") {
			t.Fatalf("unknown attribute %+v: %v", bad, err)
		}
	}

	// A grouping with more rows than the cap is refused rather than truncated.
	defer func(n int) { maxBalanceRows = n }(maxBalanceRows)
	maxBalanceRows = 2
	f = march
	f.GroupBy = []string{"DEPT"}
	if _, err := e.store.TrialBalance(e.ctx, f); !isProblem(err, http.StatusUnprocessableEntity, CodeBalanceTooManyRows) {
		t.Fatalf("grouping over the cap: %v", err)
	}
	f.Dimensions = map[string]string{"DEPT": "D100"}
	if tb, err := e.store.TrialBalance(e.ctx, f); err != nil || len(tb.Rows) != 1 {
		t.Fatalf("a narrowed grouping under the cap: %+v %v", tb, err)
	}

	// Another legal entity's caller cannot read the ledger's balances at all.
	if _, err := e.store.TrialBalance(principalCtx(e.tenant, ids.New()), f); !isProblem(err, http.StatusNotFound, "LEDGER_NOT_FOUND") {
		t.Fatalf("out-of-scope caller: %v", err)
	}
}

func TestPostgres_StatisticalQuantityRoundTrips(t *testing.T) {
	e, _ := setup(t)
	in := vendorInvoice(e.ledger.ID, "STAT-1", "100.00")
	in.Lines = append(in.Lines,
		domain.LineInput{Account: "6010", Side: domain.SideDebit, Amount: decimal.Zero, Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"},
			Quantity: decimal.RequireFromString("12.5"), Unit: "KG"},
		domain.LineInput{Account: "6010", Side: domain.SideCredit, Amount: decimal.Zero, Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"},
			Quantity: decimal.RequireFromString("12.5"), Unit: "KG"})
	out, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	lines := out.Entries[0].Lines
	if len(lines) != 4 || lines[2].Quantity != "12.500000" || lines[2].Unit != "KG" || lines[0].Quantity != "" {
		t.Fatalf("posted lines: %+v", lines)
	}
	got, err := e.store.GetEntry(e.ctx, out.Entries[0].ID)
	if err != nil || got.Lines[2].Quantity != "12.500000" || got.Lines[2].Unit != "KG" {
		t.Fatalf("read back: %+v %v", got.Lines, err)
	}
	hash, _ := domain.HashPayload("reverse stat")
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: out.Entries[0].ID, Key: "REV-STAT", Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if rl := rev.Entries[0].Lines[2]; rl.Quantity != "12.500000" || rl.Unit != "KG" || rl.DebitCredit != domain.SideCredit {
		t.Fatalf("reversing statistical line: %+v", rl)
	}
	page, err := e.store.ListLines(e.ctx, LineFilter{LedgerID: e.ledger.ID, Limit: 50})
	if err != nil {
		t.Fatalf("inquiry: %v", err)
	}
	n := 0
	for _, l := range page.Items {
		if l.Quantity == "12.500000" && l.Unit == "KG" {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("inquiry shows %d statistical lines, want 4", n)
	}
}
