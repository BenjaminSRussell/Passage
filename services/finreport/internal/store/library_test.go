package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

var apr1 = time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)

type firingFixture struct {
	st     *Postgres
	admin  *pgxpool.Pool
	ctx    context.Context
	ledger string
	entity string
	sc     domain.Schedule
}

// firingSetup applies two entries to a ledger, publishes a definition and saves
// a monthly schedule over the ledger, saved on 10 February 2026.
func firingSetup(t *testing.T) firingFixture {
	t.Helper()
	st, admin, ctx, ledger, entity := lagFixtureAdmin(t)
	if _, _, err := st.SaveDraft(ctx, storeFixtureDefinition("FIRE-DEF"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "FIRE-DEF", "tester"); err != nil {
		t.Fatal(err)
	}
	sc := domain.Schedule{Code: "MONTHLY", Name: "Month-end pack", Definition: "FIRE-DEF", Cron: "0 2 1 * *", Timezone: "UTC",
		LedgerIDs: []string{ledger}, CurrencyType: "acct", Formats: []string{"csv", "xlsx"}, Recipients: []string{"b@example.com", "a@example.com"},
		AttachmentLimitMB: 1, Enabled: true, FiscalYearStartMonth: 1, UpdatedBy: "u1"}
	if _, _, err := st.SaveSchedule(ctx, sc, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `update report_schedule set updated_at = '2026-02-10T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	return firingFixture{st: st, admin: admin, ctx: ctx, ledger: ledger, entity: entity, sc: sc}
}

func (f firingFixture) result(t *testing.T, fire time.Time, files ...FiringOutput) FiringResult {
	t.Helper()
	run, err := f.st.CreateRun(f.ctx, RunSpec{Code: "FIRE-DEF", LedgerID: f.ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 1,
		Calendar: storeFixtureCalendar(), RequestedBy: "system", IdempotencyKey: "fire-" + fire.Format(time.RFC3339), RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	return FiringResult{Schedule: f.sc, LedgerID: f.ledger, FireTime: fire, Period: domain.FiringPeriod{Year: 2026, No: 1},
		Run: run, Outputs: files, RetainUntil: fire.AddDate(7, 0, 0), Attempts: 1, StartedAt: fire}
}

func TestFiring_DueRecordedOnceAndNotDueAgain(t *testing.T) {
	f := firingSetup(t)
	now := apr1.Add(30 * time.Second)
	due, err := f.st.DueFirings(f.ctx, now)
	if err != nil || len(due) != 1 || due[0].LedgerID != f.ledger || !due[0].FireTime.Equal(apr1) || due[0].Schedule.Code != "MONTHLY" {
		t.Fatalf("due %+v err %v: want the 1 April firing only (1 March is outside the 31-day look-back window)", due, err)
	}
	// Before the first cron time after the save nothing is due.
	if due, err := f.st.DueFirings(f.ctx, time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)); err != nil || len(due) != 0 {
		t.Fatalf("due before the cron time: %+v %v", due, err)
	}
	csv := FiringOutput{Format: "csv", FileName: "FIRE-DEF-1-2026-01.csv", ContentType: "text/csv; charset=utf-8", Content: []byte("row,C01\nR001,10\n")}
	big := FiringOutput{Format: "xlsx", FileName: "FIRE-DEF-1-2026-01.xlsx", ContentType: "application/octet-stream", Content: bytes.Repeat([]byte{7}, 1<<20+1)}
	res := f.result(t, apr1, csv, big)
	rec, err := f.st.RecordFiring(f.ctx, res)
	if err != nil || rec.Status != FiringCompleted || rec.RunID != res.Run.ID || rec.Attempts != 1 || rec.LegalEntityID != f.entity || rec.PeriodNo != 1 {
		t.Fatalf("recorded %+v err %v", rec, err)
	}
	if due, err := f.st.DueFirings(f.ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("fired cron time is due again: %+v %v", due, err)
	}
	due, err = f.st.DueFirings(f.ctx, time.Date(2026, 5, 1, 2, 0, 5, 0, time.UTC))
	if err != nil || len(due) != 1 || !due[0].FireTime.Equal(time.Date(2026, 5, 1, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("next month: %+v %v", due, err)
	}

	// The job running twice records one firing, one set of files and one set of events.
	again, err := f.st.RecordFiring(f.ctx, f.result(t, apr1, csv, big))
	if err != nil || again.ID != rec.ID {
		t.Fatalf("second record %+v err %v, want the first firing back", again, err)
	}
	page, err := f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 50)
	if err != nil || len(page.Items) != 2 || page.Next != nil {
		t.Fatalf("library %+v err %v", page, err)
	}
	if n := countType(outboxTypes(t, f.ctx, f.admin), events.DistributionSent); n != 2 {
		t.Fatalf("%d distribution.sent events, want 2", n)
	}
	// Files come back byte for byte, with their digest.
	var gotCSV LibraryEntry
	for _, e := range page.Items {
		if e.Format == "csv" {
			gotCSV = e
		}
	}
	entry, body, err := f.st.LibraryContent(f.ctx, gotCSV.ID)
	sum := sha256.Sum256(csv.Content)
	if err != nil || !bytes.Equal(body, csv.Content) || entry.SHA256 != hex.EncodeToString(sum[:]) || entry.SizeBytes != int64(len(csv.Content)) ||
		entry.ScheduleCode != "MONTHLY" || entry.PeriodNo != 1 || entry.LegalEntityID != f.entity || !entry.RetainUntil.Equal(apr1.AddDate(7, 0, 0)) {
		t.Fatalf("content %+v err %v", entry, err)
	}
	if meta, err := f.st.GetLibraryEntry(f.ctx, gotCSV.ID); err != nil || meta.ID != gotCSV.ID {
		t.Fatalf("meta %+v %v", meta, err)
	}
	// The file over the attachment limit is sent as a link, the other as an attachment.
	var delivery []string
	rows, err := f.admin.Query(f.ctx, `select payload->'data'->>'format' || ':' || (payload->'data'->>'delivery') || ':' || (payload->'data'->>'recipients_sha256') from outbox where type=$1 order by 1`, events.DistributionSent)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		delivery = append(delivery, s)
	}
	rows.Close()
	wantDigest := recipientsDigest([]string{"a@example.com", "b@example.com"})
	if len(delivery) != 2 || delivery[0] != "csv:attachment:"+wantDigest || delivery[1] != "xlsx:link:"+wantDigest {
		t.Fatalf("delivery %v", delivery)
	}
}

func TestFiring_FailureIsRecordedOnceWithItsEvent(t *testing.T) {
	f := firingSetup(t)
	fail := FiringFailure{Schedule: f.sc, LedgerID: f.ledger, FireTime: apr1, Period: domain.FiringPeriod{Year: 2026, No: 3},
		Stage: "run", Code: "REPORT-0004", Detail: strings.Repeat("x", 3000), Attempts: 2, StartedAt: apr1}
	got, err := f.st.RecordFailure(f.ctx, fail)
	if err != nil || got.Status != FiringFailed || got.ErrorCode != "REPORT-0004" || got.Attempts != 2 || len(got.ErrorDetail) != 2000 || got.LegalEntityID != f.entity || got.RunID != "" {
		t.Fatalf("failure %+v err %v", got, err)
	}
	if again, err := f.st.RecordFailure(f.ctx, fail); err != nil || again.ID != got.ID {
		t.Fatalf("second failure %+v %v", again, err)
	}
	if n := countType(outboxTypes(t, f.ctx, f.admin), events.StatementFailed); n != 1 {
		t.Fatalf("%d statement.failed events, want 1", n)
	}
	// A failed firing is not retried by the next pass; the next cron time is.
	if due, err := f.st.DueFirings(f.ctx, apr1.Add(time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("due after failure %+v %v", due, err)
	}
	list, err := f.st.ListFirings(f.ctx, "MONTHLY", false, 10)
	if err != nil || len(list) != 1 || list[0].Status != FiringFailed {
		t.Fatalf("list %+v %v", list, err)
	}
	if other, _ := f.st.ListFirings(f.ctx, "OTHER", false, 10); len(other) != 0 {
		t.Fatalf("filter by schedule: %+v", other)
	}
}

func TestFiring_LatestKeepsTheNewestFiringOfEachScheduleAndLedger(t *testing.T) {
	f := firingSetup(t)
	may1 := time.Date(2026, 5, 1, 2, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{apr1, may1} {
		if _, err := f.st.RecordFailure(f.ctx, FiringFailure{Schedule: f.sc, LedgerID: f.ledger, FireTime: at, Period: domain.FiringPeriod{Year: 2026, No: 3},
			Stage: "run", Code: "REPORT-0004", Detail: "stale", Attempts: 2, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := f.st.ListFirings(f.ctx, "", false, 10)
	if err != nil || len(all) != 2 {
		t.Fatalf("all firings %+v %v", all, err)
	}
	latest, err := f.st.ListFirings(f.ctx, "", true, 10)
	if err != nil || len(latest) != 1 || !latest[0].FireTime.Equal(may1) {
		t.Fatalf("latest %+v %v: want the 1 May firing only", latest, err)
	}
}

func TestFiring_FailureDetailIsCutOnACharacterBoundary(t *testing.T) {
	f := firingSetup(t)
	// 1999 ASCII bytes then a two-byte character: a byte cut at 2000 would split it.
	detail := strings.Repeat("x", 1999) + strings.Repeat("é", 10)
	got, err := f.st.RecordFailure(f.ctx, FiringFailure{Schedule: f.sc, LedgerID: f.ledger, FireTime: apr1, Period: domain.FiringPeriod{Year: 2026, No: 3},
		Stage: "render", Code: "REPORT-0016", Detail: detail, Attempts: 2, StartedAt: apr1})
	if err != nil || got.ErrorDetail != strings.Repeat("x", 1999) {
		t.Fatalf("failure %+v err %v: want the detail cut before the split character", got, err)
	}
}

func TestSchedule_StoredBeforeFiscalYearStartMonthReadsWithTheDefault(t *testing.T) {
	f := firingSetup(t)
	if _, err := f.admin.Exec(f.ctx, `update report_schedule set body = body - 'fiscalYearStartMonth'`); err != nil {
		t.Fatal(err)
	}
	got, _, err := f.st.GetSchedule(f.ctx, "MONTHLY")
	if err != nil || got.FiscalYearStartMonth != 1 {
		t.Fatalf("get %+v %v", got, err)
	}
	list, err := f.st.ListSchedules(f.ctx)
	if err != nil || len(list) != 1 || list[0].FiscalYearStartMonth != 1 {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestFiring_DisabledScheduleDoesNotFire(t *testing.T) {
	f := firingSetup(t)
	f.sc.Enabled = false
	one := 1
	if _, _, err := f.st.SaveSchedule(f.ctx, f.sc, &one); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `update report_schedule set updated_at = '2026-02-10T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if due, err := f.st.DueFirings(f.ctx, apr1.Add(time.Minute)); err != nil || len(due) != 0 {
		t.Fatalf("disabled schedule due: %+v %v", due, err)
	}
}

func TestLibrary_FiltersAndKeysetPaging(t *testing.T) {
	f := firingSetup(t)
	for i, fire := range []time.Time{apr1, apr1.AddDate(0, 1, 0), apr1.AddDate(0, 2, 0)} {
		res := f.result(t, fire, FiringOutput{Format: "csv", FileName: "a.csv", ContentType: "text/csv", Content: []byte{byte(i)}},
			FiringOutput{Format: "html", FileName: "a.html", ContentType: "text/html", Content: []byte{byte(i), 1}})
		if _, err := f.st.RecordFiring(f.ctx, res); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	var cursor *domain.AuditCursor
	for pages := 0; pages < 10; pages++ {
		page, err := f.st.ListLibrary(f.ctx, LibraryQuery{Definition: "FIRE-DEF", LedgerID: f.ledger}, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			seen = append(seen, e.ID)
		}
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	unique := map[string]bool{}
	for _, id := range seen {
		unique[id] = true
	}
	if len(seen) != 6 || len(unique) != 6 {
		t.Fatalf("paging saw %d entries, %d distinct, want 6 each", len(seen), len(unique))
	}
	for _, c := range []struct {
		q    LibraryQuery
		want int
	}{
		{LibraryQuery{Format: "html"}, 3},
		{LibraryQuery{ScheduleCode: "OTHER"}, 0},
		{LibraryQuery{Definition: "NOPE"}, 0},
		{LibraryQuery{LedgerID: ids.New()}, 0},
		{LibraryQuery{PeriodYear: 2026, PeriodNo: 1}, 6},
		{LibraryQuery{PeriodYear: 2026, PeriodNo: 2}, 0},
	} {
		page, err := f.st.ListLibrary(f.ctx, c.q, nil, 50)
		if err != nil || len(page.Items) != c.want {
			t.Errorf("%+v: %d entries (%v), want %d", c.q, len(page.Items), err, c.want)
		}
	}
	if _, err := f.st.GetLibraryEntry(f.ctx, ids.New()); problemCode(err) != CodeLibraryNotFound {
		t.Fatalf("missing entry = %v", err)
	}
}

// Library files, firings and their events follow the legal-entity scope and the
// tenant: another entity's caller and another tenant see nothing.
func TestLibrary_ScopedByLegalEntityAndTenant(t *testing.T) {
	f := firingSetup(t)
	res := f.result(t, apr1, FiringOutput{Format: "csv", FileName: "a.csv", ContentType: "text/csv", Content: []byte("x")})
	if _, err := f.st.RecordFiring(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	page, _ := f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 10)
	id := page.Items[0].ID
	for name, ctx := range map[string]context.Context{
		"another entity": scopedCtx(testkit.TenantID(1), ids.New()),
		"another tenant": scopedCtx(testkit.TenantID(2), ""),
	} {
		if p, err := f.st.ListLibrary(ctx, LibraryQuery{}, nil, 10); err != nil || len(p.Items) != 0 {
			t.Errorf("%s lists %+v %v", name, p, err)
		}
		if _, _, err := f.st.LibraryContent(ctx, id); problemCode(err) != CodeLibraryNotFound {
			t.Errorf("%s reads the file: %v", name, err)
		}
		if fl, err := f.st.ListFirings(ctx, "", false, 10); err != nil || len(fl) != 0 {
			t.Errorf("%s lists firings %+v %v", name, fl, err)
		}
	}
	// The owning entity's own scope sees it.
	own := scopedCtx(testkit.TenantID(1), f.entity)
	if p, err := f.st.ListLibrary(own, LibraryQuery{}, nil, 10); err != nil || len(p.Items) != 1 {
		t.Fatalf("own entity lists %+v %v", p, err)
	}
}

// The application role can read and add library files, never change or remove
// them (the migration grants SELECT and INSERT only).
func TestLibrary_AppRoleCannotRewriteStoredFiles(t *testing.T) {
	f := firingSetup(t)
	res := f.result(t, apr1, FiringOutput{Format: "csv", FileName: "a.csv", ContentType: "text/csv", Content: []byte("x")})
	if _, err := f.st.RecordFiring(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`update report_library_entry set content = 'y'`,
		`delete from report_library_entry`,
		`update report_schedule_firing set status = 'failed'`,
		`delete from report_schedule_firing`,
	} {
		err := dbx.WithTenantTx(f.ctx, f.st.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(f.ctx, q)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s as passage_app = %v, want permission denied", q, err)
		}
	}
}
