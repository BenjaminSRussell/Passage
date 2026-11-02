package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

type setEnv struct {
	env
	local, tax Ledger
	set        LedgerSet
}

func setupLedgerSet(t *testing.T) setEnv {
	t.Helper()
	e, _ := setup(t)
	for num, a := range map[string]Account{
		"7010": {Name: "Local freight expense", Type: "expense", Postable: true},
		"2100": {Name: "Vendor payable", Type: "liability", Postable: true},
	} {
		if _, _, err := e.store.PutAccount(e.ctx, "LOC", num, a); err != nil {
			t.Fatalf("put local account %s: %v", num, err)
		}
	}
	local, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "HGB", Name: "Local GAAP", ChartCode: "LOC", Currency: "USD", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create local ledger: %v", err)
	}
	tax, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "TAX", Name: "Tax", ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create tax ledger: %v", err)
	}
	if _, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: local.ID, SourceAccount: "6010", TargetAccount: "7010", EffectiveFrom: "2026-01-01"}); err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	set, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "ALL", Name: "All ledgers", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{local.ID, tax.ID}})
	if err != nil {
		t.Fatalf("create set: %v", err)
	}
	return setEnv{env: e, local: local, tax: tax, set: set}
}

func (e setEnv) entryCount(t *testing.T) (n int) {
	t.Helper()
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from journal_entry`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_SetPostingAppliesRulesPerLedgerAndBalancesEach(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-1", "1250.00")
	out, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil {
		t.Fatalf("post set: %v", err)
	}
	if len(out.Entries) != 3 {
		t.Fatalf("want one entry per ledger, got %d", len(out.Entries))
	}
	accountByLedger := map[string]string{}
	for _, entry := range out.Entries {
		if entry.DocumentID != in.DocumentID {
			t.Fatalf("entries must share the document id: %s != %s", entry.DocumentID, in.DocumentID)
		}
		accountByLedger[entry.LedgerID] = entry.Lines[0].Account
	}
	if accountByLedger[e.ledger.ID] != "6010" || accountByLedger[e.tax.ID] != "6010" || accountByLedger[e.local.ID] != "7010" {
		t.Fatalf("expense account per ledger: %v", accountByLedger)
	}
	for _, id := range []string{e.ledger.ID, e.local.ID, e.tax.ID} {
		tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: id})
		if err != nil {
			t.Fatalf("trial balance %s: %v", id, err)
		}
		if !tb.Balanced || tb.Debit != "1250.00" || tb.Credit != "1250.00" || tb.Net != "0.00" {
			t.Fatalf("ledger %s trial balance: %+v", id, tb)
		}
	}
	var posted int
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1`, ledgerevents.JournalPosted).Scan(&posted)
	})
	if err != nil || posted != 3 {
		t.Fatalf("journal.posted events = %d (err %v); want 3", posted, err)
	}
}

func TestPostgres_SetPostingReplaysAndRejectsChangedPayload(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-2", "80.00")
	first, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil {
		t.Fatalf("post set: %v", err)
	}
	replay, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Idempotency.Replayed || len(replay.Entries) != 3 || replay.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("replay must return the original entries: %+v", replay)
	}
	if n := e.entryCount(t); n != 3 {
		t.Fatalf("a replay must not post again, %d entries", n)
	}
	changed := in
	changed.Hash, _ = domain.HashPayload("different")
	if _, err := e.store.PostSetEntry(e.ctx, e.set.ID, changed); problemCode(err) != "LEDGER_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("want LEDGER_IDEMPOTENCY_CONFLICT, got %v", err)
	}
}

func TestPostgres_SetPostingIsAllOrNothing(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-3", "40.00")
	in.Lines[1].Account = "2110"
	_, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != "LEDGER_ACCOUNT_NOT_FOUND" {
		t.Fatalf("want LEDGER_ACCOUNT_NOT_FOUND from the local ledger, got %v", err)
	}
	if n := e.entryCount(t); n != 0 {
		t.Fatalf("a rejection on one ledger must leave %d entries on the others", n)
	}
	ok, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "AFTER", "5.00"))
	if err != nil || ok.Entries[0].PostingSequence != 1 {
		t.Fatalf("a rejected set must not consume sequence numbers: %v %+v", err, ok)
	}
}

func TestPostgres_LedgerSetValidation(t *testing.T) {
	e := setupLedgerSet(t)
	wide := principalCtx(e.tenant, "")
	otherEntity, err := e.store.CreateLedger(wide, Ledger{LegalEntityID: ids.New(), Code: "OTH", Name: "Other", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, code string
		in         LedgerSetInput
	}{
		{"legal entity", CodeSetEntityMismatch, LedgerSetInput{Code: "S2", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{otherEntity.ID}}},
		{"no parallel", CodeSetInvalid, LedgerSetInput{Code: "S3", Name: "s", LeadingLedgerID: e.ledger.ID}},
		{"leading is parallel", CodeSetInvalid, LedgerSetInput{Code: "S4", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{e.ledger.ID}}},
		{"repeated ledger", CodeSetInvalid, LedgerSetInput{Code: "S5", Name: "s", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{e.tax.ID, e.tax.ID}}},
	} {
		_, err := e.store.CreateLedgerSet(wide, tc.in)
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != tc.code {
			t.Errorf("%s: want %s, got %v", tc.name, tc.code, err)
		}
	}
	if _, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "ALL", Name: "dup", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{e.tax.ID}}); problemCode(err) != CodeSetCodeDuplicate {
		t.Errorf("duplicate code: %v", err)
	}
	if _, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: e.local.ID, SourceAccount: "6010", TargetAccount: "7010", EffectiveFrom: "2026-01-01"}); problemCode(err) != CodeMappingDuplicate {
		t.Errorf("duplicate mapping: %v", err)
	}
	if _, err := e.store.GetLedgerSet(principalCtx(testkit.TenantID(2), e.le), e.set.ID); problemCode(err) != CodeSetNotFound {
		t.Errorf("another tenant must not see the set: %v", err)
	}
}

func TestPostgres_SetTablesAreAppendOnlyForTheAppRole(t *testing.T) {
	e := setupLedgerSet(t)
	for _, stmt := range []string{
		`update ledger_set set name = 'x'`,
		`delete from ledger_set_member`,
		`update ledger_account_mapping set target_account = 'x'`,
		`delete from ledger_account_mapping`,
	} {
		err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(e.ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("passage_app must get insufficient_privilege for %q, got %v", stmt, err)
		}
	}
}

func TestPostgres_AccountMappingOnLeadingLedgerIsRejected(t *testing.T) {
	e := setupLedgerSet(t)
	_, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: e.ledger.ID, SourceAccount: "6010", TargetAccount: "2110", EffectiveFrom: "2026-01-01"})
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != CodeMappingOnLeading {
		t.Fatalf("mapping on the leading ledger must be rejected, got %v", err)
	}
}

func TestPostgres_LedgerSetRejectsLeadingLedgerWithExistingMapping(t *testing.T) {
	e := setupLedgerSet(t)
	_, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "REV", Name: "reverse", LeadingLedgerID: e.local.ID, ParallelLedgerIDs: []string{e.tax.ID}})
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != CodeMappingOnLeading {
		t.Fatalf("a leading ledger that already has a mapping must be rejected, got %v", err)
	}
}

func TestPostgres_SetPostingMappingDateBoundary(t *testing.T) {
	e := setupLedgerSet(t)
	if _, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: e.tax.ID, SourceAccount: "6010", TargetAccount: "2100", EffectiveFrom: "2026-03-10"}); err != nil {
		t.Fatalf("mapping: %v", err)
	}
	taxAccount := func(key string, date time.Time) string {
		in := vendorInvoice("", key, "9.00")
		in.PostingDate = date
		out, err := e.store.PostSetEntry(e.ctx, e.set.ID, in)
		if err != nil {
			t.Fatalf("post %s: %v", key, err)
		}
		for _, entry := range out.Entries {
			if entry.LedgerID == e.tax.ID {
				return entry.Lines[0].Account
			}
		}
		t.Fatal("no tax entry")
		return ""
	}
	if got := taxAccount("B-1", time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC)); got != "6010" {
		t.Errorf("the day before effectiveFrom must not map, got %s", got)
	}
	if got := taxAccount("B-2", time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)); got != "2100" {
		t.Errorf("effectiveFrom itself must map, got %s", got)
	}
}

func TestPostgres_LedgerSetNeedsOneLeadingLedgerPerLegalEntity(t *testing.T) {
	e := setupLedgerSet(t)
	alt, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "ALT", Name: "Alt", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "ALT", Name: "alt", LeadingLedgerID: alt.ID, ParallelLedgerIDs: []string{e.tax.ID}})
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || rej.Violations[0].Code != CodeSetLeadingConflict {
		t.Fatalf("a second leading ledger must be refused, got %v", err)
	}
	if _, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "ALT2", Name: "alt", LeadingLedgerID: e.ledger.ID, ParallelLedgerIDs: []string{alt.ID}}); err != nil {
		t.Fatalf("a second set under the same leading ledger is allowed: %v", err)
	}
}

func TestPostgres_LedgerSetConcurrentLeadingLedgerIsSerialized(t *testing.T) {
	e, _ := setup(t)
	parallel, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "PAR", Name: "Parallel", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	leading, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "ALT", Name: "Alternative", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, lead := range []string{e.ledger.ID, leading.ID} {
		go func(i int, lead string) {
			<-start
			_, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: fmt.Sprintf("RACE%d", i), Name: "Concurrent", LeadingLedgerID: lead, ParallelLedgerIDs: []string{parallel.ID}})
			results <- err
		}(i, lead)
	}
	close(start)
	success, rejected := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
			continue
		}
		var rej *domain.RejectedError
		if errors.As(err, &rej) && rej.Violations[0].Code == CodeSetLeadingConflict {
			rejected++
			continue
		}
		t.Fatalf("unexpected concurrent create error: %v", err)
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("concurrent creates: %d succeeded, %d rejected", success, rejected)
	}
}

func TestPostgres_LedgerSetCodeIsUniquePerLegalEntity(t *testing.T) {
	e := setupLedgerSet(t)
	wide := principalCtx(e.tenant, "")
	otherLE := ids.New()
	lead, err := e.store.CreateLedger(wide, Ledger{LegalEntityID: otherLE, Code: "OLEAD", Name: "Other leading", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := e.store.CreateLedger(wide, Ledger{LegalEntityID: otherLE, Code: "OPAR", Name: "Other parallel", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateLedgerSet(wide, LedgerSetInput{Code: e.set.Code, Name: "Other entity set", LeadingLedgerID: lead.ID, ParallelLedgerIDs: []string{parallel.ID}}); err != nil {
		t.Fatalf("same set code in another legal entity: %v", err)
	}
}

func TestPostgres_AccountMappingTargetMustBePostableInTheLedgerChart(t *testing.T) {
	e := setupLedgerSet(t)
	if _, _, err := e.store.PutAccount(e.ctx, "LOC", "7999", Account{Name: "Total", Type: "reporting", Postable: false}); err != nil {
		t.Fatal(err)
	}
	for target, code := range map[string]string{"9999": "LEDGER_ACCOUNT_NOT_FOUND", "7999": "LEDGER_ACCOUNT_NOT_POSTABLE", "2110": "LEDGER_ACCOUNT_NOT_FOUND"} {
		_, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: e.local.ID, SourceAccount: "2100", TargetAccount: target, EffectiveFrom: "2026-01-01"})
		var rej *domain.RejectedError
		if !errors.As(err, &rej) || rej.Violations[0].Code != code {
			t.Errorf("target %s: want %s, got %v", target, code, err)
		}
	}
}

func TestPostgres_LedgerSetTablesRespectLegalEntityScope(t *testing.T) {
	e := setupLedgerSet(t)
	other := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetLedgerSet(other, e.set.ID); problemCode(err) != CodeSetNotFound {
		t.Errorf("a caller scoped to another legal entity must not see the set: %v", err)
	}
	if _, err := e.store.PostSetEntry(other, e.set.ID, vendorInvoice("", "SCOPE", "1.00")); problemCode(err) != CodeSetNotFound {
		t.Errorf("a caller scoped to another legal entity must not post the set: %v", err)
	}
	for _, table := range []string{"ledger_set", "ledger_set_member", "ledger_account_mapping"} {
		var n int
		err := dbx.WithTenantTx(other, e.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(other, `select count(*) from `+table).Scan(&n)
		})
		if err != nil || n != 0 {
			t.Errorf("%s: other legal entity sees %d rows (err %v)", table, n, err)
		}
	}
}

func TestPostgres_ConcurrentSetPostsWithOneKeyPostOnce(t *testing.T) {
	e := setupLedgerSet(t)
	in := vendorInvoice("", "SET-RACE", "33.00")
	const workers = 6
	results := make([]EntryResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = e.store.PostSetEntry(e.ctx, e.set.ID, in)
		}()
	}
	wg.Wait()
	fresh := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if !results[i].Idempotency.Replayed {
			fresh++
		}
		if len(results[i].Entries) != 3 || results[i].Entries[0].ID != results[0].Entries[0].ID {
			t.Fatalf("worker %d saw different entries", i)
		}
	}
	if fresh != 1 || e.entryCount(t) != 3 {
		t.Fatalf("want one fresh post and 3 entries, got %d fresh, %d entries", fresh, e.entryCount(t))
	}
}

func waitForAdvisoryWaiters(t *testing.T, e env, tenantID, legalEntityID string, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := e.pool.QueryRow(e.ctx, `select count(*) from pg_locks
			where locktype = 'advisory' and not granted
			and ((classid::bigint << 32) | objid::bigint) = hashtextextended($1, 0)`, tenantID+"/"+legalEntityID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d advisory lock waiters", want)
}

func TestPostgres_AccountMappingAndLedgerSetOnTheSameLeadingLedgerAdmitExactlyOne(t *testing.T) {
	e, _ := setup(t)
	lead, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "LEAD", Name: "Lead", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "PAR", Name: "Parallel", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	p, err := principalOf(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	holding, release, barrierDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	releaseBarrier := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBarrier)
	go func() {
		barrierDone <- dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
			if err := lockLegalEntity(e.ctx, tx, p.TenantID, e.le); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	results := make(chan error, 2)
	go func() {
		_, err := e.store.CreateAccountMapping(e.ctx, AccountMapping{LedgerID: lead.ID, SourceAccount: "6010", TargetAccount: "2110", EffectiveFrom: "2026-01-01"})
		results <- err
	}()
	go func() {
		_, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "RACE", Name: "Race", LeadingLedgerID: lead.ID, ParallelLedgerIDs: []string{parallel.ID}})
		results <- err
	}()
	waitForAdvisoryWaiters(t, e, p.TenantID, e.le, 2)
	releaseBarrier()
	if err := <-barrierDone; err != nil {
		t.Fatal(err)
	}
	success, rejected := 0, 0
	for range 2 {
		err := <-results
		var rej *domain.RejectedError
		switch {
		case err == nil:
			success++
		case errors.As(err, &rej) && rej.Violations[0].Code == CodeMappingOnLeading:
			rejected++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("%d succeeded, %d rejected; want 1 and 1", success, rejected)
	}
}

func TestPostgres_LedgerSetCreateDoesNotDeadlockWithSetPosting(t *testing.T) {
	e, _ := setup(t)
	a, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "LA", Name: "A", ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "LB", Name: "B", ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID > b.ID {
		a, b = b, a
	}
	rawA, rawB := strings.ReplaceAll(a.ID, "-", ""), strings.ToUpper(b.ID)
	if rawA < rawB {
		t.Fatalf("raw ids %s, %s sort in canonical order; the case needs them inverted", rawA, rawB)
	}
	set, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: "BASE", Name: "Base", LeadingLedgerID: rawB, ParallelLedgerIDs: []string{rawA}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{a.ID, b.ID}; !slices.Equal(set.LedgerIDs, want) || set.LeadingLedgerID != b.ID {
		t.Fatalf("set = %+v; want canonical ids %v leading %s", set, want, b.ID)
	}
	for round := range 15 {
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := e.store.PostSetEntry(e.ctx, set.ID, vendorInvoice("", fmt.Sprintf("DL-%d", round), "1.00"))
			results <- err
		}()
		go func() {
			<-start
			_, err := e.store.CreateLedgerSet(e.ctx, LedgerSetInput{Code: fmt.Sprintf("DL%d", round), Name: "Concurrent", LeadingLedgerID: rawB, ParallelLedgerIDs: []string{rawA}})
			results <- err
		}()
		close(start)
		for range 2 {
			if err := <-results; err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}
