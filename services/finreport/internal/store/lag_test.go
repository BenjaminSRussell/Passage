package store

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

// waitingEntry parks an entry that arrived ahead of the mark, received age
// seconds ago, the way the consumer does.
func waitingEntry(t *testing.T, st *Postgres, ctx context.Context, ledger, entity string, seq int64, ageSeconds int) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into reporting_entry_pending(tenant_id, ledger_id, posting_sequence, entry_id, entry, received_at, legal_entity_id)
			values($1,$2,$3,$4,'{}', now() - make_interval(secs => $5), $6)`,
			testkit.TenantID(1), ledger, seq, ids.New(), ageSeconds, entity)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// lagFixture applies two entries as passage_app; admin reads the outbox, which
// the application role can only insert into.
func lagFixture(t *testing.T) (st *Postgres, ctx context.Context, ledger, entity string) {
	st, _, ctx, ledger, entity = lagFixtureAdmin(t)
	return
}

func lagFixtureAdmin(t *testing.T) (st *Postgres, admin *pgxpool.Pool, ctx context.Context, ledger, entity string) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx = scopedCtx(testkit.TenantID(1), "")
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st = New(app)
	ledger, entity = ids.New(), ids.New()
	mustApply(t, st, ctx, postedEntry(ledger, entity, 1, "2026-01", "", pair("10")...), 1)
	mustApply(t, st, ctx, postedEntry(ledger, entity, 2, "2026-01", "", pair("5")...), 1)
	return
}

func TestLag_AgeOfTheOldestWaitingEntry(t *testing.T) {
	st, ctx, ledger, entity := lagFixture(t)
	lag, err := st.Lag(ctx)
	if err != nil || len(lag) != 1 || lag[0].LagSeconds != 0 || lag[0].HWMLedger != 2 {
		t.Fatalf("caught up lag = %+v, %v", lag, err)
	}
	waitingEntry(t, st, ctx, ledger, entity, 5, 120)
	waitingEntry(t, st, ctx, ledger, entity, 6, 20)
	lag, err = st.Lag(ctx)
	if err != nil || len(lag) != 1 {
		t.Fatalf("lag = %+v, %v", lag, err)
	}
	l := lag[0]
	if l.LagSeconds < 120 || l.LagSeconds > 130 || l.EntriesPending != 2 || l.HWMLedger != 6 || l.PostingSequence != 2 {
		t.Fatalf("lag = %+v, want ~120 s, 2 waiting, ledger mark 6, applied 2", l)
	}
}

func TestLag_LedgerThatOnlyHasWaitingEntries(t *testing.T) {
	st, ctx, _, entity := lagFixture(t)
	other := ids.New()
	waitingEntry(t, st, ctx, other, entity, 3, 45)
	lag, err := st.Lag(ctx)
	if err != nil || len(lag) != 2 {
		t.Fatalf("lag = %+v, %v; want both ledgers", lag, err)
	}
	for _, l := range lag {
		if l.LedgerID == other && (l.PostingSequence != 0 || l.LagSeconds < 45 || l.HWMLedger != 3) {
			t.Fatalf("waiting-only ledger = %+v", l)
		}
	}
}

// TestCreateRun_LagGate refuses a run on a stale read model with REPORT-0004,
// lets allowStale through with the warning stored, and ignores lag under the gate.
func TestCreateRun_LagGate(t *testing.T) {
	st, ctx, ledger, entity := lagFixture(t)
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("LAGGATE"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "LAGGATE", "tester"); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{Code: "LAGGATE", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 1, Calendar: storeFixtureCalendar(),
		RequestedBy: "tester", RequestHash: "h", MaxLagSeconds: 30}
	run, err := st.CreateRun(ctx, spec)
	if err != nil || len(run.Checks) != 0 {
		t.Fatalf("caught up run = %+v, %v", run, err)
	}
	waitingEntry(t, st, ctx, ledger, entity, 5, 90)
	if _, err := st.CreateRun(ctx, spec); problemCode(err) != domain.CodeStale {
		t.Fatalf("stale run error = %v, want %s", err, domain.CodeStale)
	}
	// The API accepts any form uuid.Parse does; the gate must not miss the ledger.
	upper := spec
	upper.LedgerID = strings.ToUpper(ledger)
	if _, err := st.CreateRun(ctx, upper); problemCode(err) != domain.CodeStale {
		t.Fatalf("stale run with an upper-case ledger id = %v, want %s", err, domain.CodeStale)
	}
	if n := countAs(t, st.pool, ctx, `select count(*) from statement_run`); n != 1 {
		t.Fatalf("a refused run must store nothing, have %d runs", n)
	}
	spec.AllowStale = true
	run, err = st.CreateRun(ctx, spec)
	if err != nil || len(run.Checks) != 1 || run.Checks[0].Code != domain.CodeStale || run.Checks[0].Severity != domain.SeverityWarning {
		t.Fatalf("allowStale run = %+v, %v", run.Checks, err)
	}
	stored, err := st.GetRun(ctx, run.ID)
	if err != nil || len(stored.Checks) != 1 || stored.Checks[0].Code != domain.CodeStale {
		t.Fatalf("stored run checks = %+v, %v", stored.Checks, err)
	}
	spec.AllowStale, spec.MaxLagSeconds = false, 600
	if run, err = st.CreateRun(ctx, spec); err != nil || len(run.Checks) != 0 {
		t.Fatalf("lag under the gate must not warn: %+v, %v", run.Checks, err)
	}
}

// TestCheckLag_OneEventPerEpisode raises lag.exceeded only after the lag has
// stayed above the gate for the alert window, once, and again after a recovery.
func TestCheckLag_OneEventPerEpisode(t *testing.T) {
	st, admin, ctx, ledger, entity := lagFixtureAdmin(t)
	count := func() int { return countType(outboxTypes(t, ctx, admin), events.LagExceeded) }
	if res, err := st.CheckLag(ctx, 30, 60); err != nil || res.Checked != 1 || res.Exceeded != 0 || len(res.Raised) != 0 {
		t.Fatalf("caught up check = %+v, %v", res, err)
	}
	// 50 s behind: above the 30 s gate for 20 s, short of the 60 s window.
	waitingEntry(t, st, ctx, ledger, entity, 5, 50)
	res, err := st.CheckLag(ctx, 30, 60)
	if err != nil || res.Exceeded != 1 || len(res.Raised) != 0 || count() != 0 {
		t.Fatalf("first sighting = %+v, %v, events %d; want an episode but no event yet", res, err, count())
	}
	age := func(secs int) {
		err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update reporting_lag_alert set first_exceeded_at = now() - make_interval(secs => $1)`, secs)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	age(61)
	res, err = st.CheckLag(ctx, 30, 60)
	if err != nil || len(res.Raised) != 1 || count() != 1 {
		t.Fatalf("sustained check = %+v, %v, events %d; want one event", res, err, count())
	}
	if a := res.Raised[0]; a.LedgerID != ledger || a.LagSeconds < 50 || a.HWMLedger != 5 || a.HWMReporting != 2 {
		t.Fatalf("alert = %+v", a)
	}
	var le string
	if err := admin.QueryRow(ctx, `select coalesce(payload->>'legalentityid', '') from outbox where type = $1`, events.LagExceeded).Scan(&le); err != nil || le != entity {
		t.Fatalf("lag.exceeded legalentityid = %q, %v; want the ledger's entity %s", le, err, entity)
	}
	if res, err = st.CheckLag(ctx, 30, 60); err != nil || len(res.Raised) != 0 || count() != 1 {
		t.Fatalf("same episode = %+v, %v, events %d; want no second event", res, err, count())
	}
	// The missing entry arrives: the pending row is applied and deleted.
	err = dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from reporting_entry_pending`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if res, err = st.CheckLag(ctx, 30, 60); err != nil || res.Exceeded != 0 {
		t.Fatalf("recovered check = %+v, %v", res, err)
	}
	if n := countAs(t, st.pool, ctx, `select count(*) from reporting_lag_alert`); n != 0 {
		t.Fatalf("recovery must close the episode, have %d rows", n)
	}
	waitingEntry(t, st, ctx, ledger, entity, 7, 100)
	if _, err = st.CheckLag(ctx, 30, 0); err != nil || count() != 2 {
		t.Fatalf("a new episode with no alert window must alert at once: %v, events %d", err, count())
	}
}

// TestCheckLag_OtherTenantInvisible keeps one tenant's lag out of another's check.
func TestCheckLag_OtherTenantInvisible(t *testing.T) {
	st, ctx, ledger, entity := lagFixture(t)
	waitingEntry(t, st, ctx, ledger, entity, 5, 90)
	other := scopedCtx(testkit.TenantID(2), "")
	if res, err := st.CheckLag(other, 30, 0); err != nil || res.Checked != 0 {
		t.Fatalf("tenant 2 check = %+v, %v; want nothing visible", res, err)
	}
}

// TestCheckLag_AlertsAtOnceWhenAlreadyBehindForTheWindow: a ledger first seen
// 100 s behind has been above a 30 s gate for 70 s, past the 60 s window, so
// the first check alerts instead of waiting another interval.
func TestCheckLag_AlertsAtOnceWhenAlreadyBehindForTheWindow(t *testing.T) {
	st, admin, ctx, ledger, entity := lagFixtureAdmin(t)
	waitingEntry(t, st, ctx, ledger, entity, 5, 100)
	res, err := st.CheckLag(ctx, 30, 60)
	if err != nil || len(res.Raised) != 1 || countType(outboxTypes(t, ctx, admin), events.LagExceeded) != 1 {
		t.Fatalf("check = %+v, %v; want one event on the first sighting", res, err)
	}
}

// TestCheckLag_OverlappingChecksRaiseOneEventPerLedger starts several checks at
// once over several stale ledgers: none fails on the episode key or deadlocks
// on another's episode rows, and each ledger gets exactly one event.
func TestCheckLag_OverlappingChecksRaiseOneEventPerLedger(t *testing.T) {
	st, admin, ctx, ledger, entity := lagFixtureAdmin(t)
	const ledgers, checks = 8, 8
	waitingEntry(t, st, ctx, ledger, entity, 5, 100)
	for range ledgers - 1 {
		waitingEntry(t, st, ctx, ids.New(), entity, 3, 100)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, checks)
	raised := make([]int, checks)
	for i := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := st.CheckLag(ctx, 30, 0)
			errs[i], raised[i] = err, len(res.Raised)
		}()
	}
	close(start)
	wg.Wait()
	total := 0
	for i := range checks {
		if errs[i] != nil {
			t.Fatalf("check %d: %v", i, errs[i])
		}
		total += raised[i]
	}
	if got := countType(outboxTypes(t, ctx, admin), events.LagExceeded); total != ledgers || got != ledgers {
		t.Fatalf("raised %d, events %d; want exactly one per ledger (%d)", total, got, ledgers)
	}
}
