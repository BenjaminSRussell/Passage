package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

func problemCodeOf(err error) (int, string) {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status, p.Code
	}
	return 0, ""
}

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	if s, c := problemCodeOf(err); s != status || c != code {
		t.Fatalf("err = %v (status %d code %q), want %d %s", err, s, c, status, code)
	}
}

func holdSpec(entity, key string) HoldSpec {
	return HoldSpec{LegalEntityID: entity, Reason: "Audit 2026-17", Actor: "legal1", IdempotencyKey: key, RequestHash: "h-" + key}
}

func TestLegalHold_PlaceReplayConflictAndRelease(t *testing.T) {
	f := firingSetup(t)
	h, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k1"))
	if err != nil || !h.Active || h.Revision != 1 || h.LegalEntityID != f.entity || h.PlacedBy != "legal1" || h.Replayed {
		t.Fatalf("hold %+v err %v", h, err)
	}
	// The same key and request returns the stored hold, the same key with another request is refused.
	again, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k1"))
	if err != nil || again.ID != h.ID || !again.Replayed {
		t.Fatalf("replay %+v err %v", again, err)
	}
	other := holdSpec(f.entity, "k1")
	other.RequestHash = "different"
	_, err = f.st.PlaceHold(f.ctx, other)
	wantProblem(t, err, 422, CodeIdempotencyReuse)
	// One active hold per entity.
	_, err = f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k2"))
	wantProblem(t, err, 409, CodeHoldActive)
	// Shape errors.
	bad := holdSpec(f.entity, "k3")
	bad.Reason = "  "
	_, err = f.st.PlaceHold(f.ctx, bad)
	wantProblem(t, err, 422, CodeHoldReasonShape)
	bad = holdSpec(f.entity, "k3")
	bad.Reason = strings.Repeat("r", MaxHoldReason+1)
	_, err = f.st.PlaceHold(f.ctx, bad)
	wantProblem(t, err, 422, CodeHoldReasonShape)
	_, err = f.st.PlaceHold(f.ctx, holdSpec("not-a-uuid", "k3"))
	wantProblem(t, err, 422, "COM-0002")

	active, err := f.st.ListHolds(f.ctx, true)
	if err != nil || len(active) != 1 || active[0].ID != h.ID {
		t.Fatalf("active %+v err %v", active, err)
	}

	// Release: If-Match is required and checked.
	_, err = f.st.ReleaseHold(f.ctx, h.ID, nil, "", "legal2")
	wantProblem(t, err, 428, "precondition_required")
	stale := 7
	_, err = f.st.ReleaseHold(f.ctx, h.ID, &stale, "", "legal2")
	wantProblem(t, err, 412, CodeRevisionConflict)
	rev := h.Revision
	rel, err := f.st.ReleaseHold(f.ctx, h.ID, &rev, "matter closed", "legal2")
	if err != nil || rel.Active || rel.ReleasedBy != "legal2" || rel.ReleasedAt == nil || rel.ReleaseNote != "matter closed" || rel.Revision != 2 {
		t.Fatalf("released %+v err %v", rel, err)
	}
	rev = rel.Revision
	_, err = f.st.ReleaseHold(f.ctx, h.ID, &rev, "", "legal2")
	wantProblem(t, err, 409, CodeHoldReleased)
	_, err = f.st.ReleaseHold(f.ctx, ids.New(), &rev, "", "legal2")
	wantProblem(t, err, 404, CodeHoldNotFound)
	if got, err := f.st.GetHold(f.ctx, h.ID); err != nil || got.Active {
		t.Fatalf("get %+v err %v", got, err)
	}
	if active, _ := f.st.ListHolds(f.ctx, true); len(active) != 0 {
		t.Fatalf("active after release: %+v", active)
	}
	// A new hold can follow a release; the history keeps both.
	if _, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k4")); err != nil {
		t.Fatal(err)
	}
	if all, err := f.st.ListHolds(f.ctx, false); err != nil || len(all) != 2 {
		t.Fatalf("all holds %+v err %v", all, err)
	}

	// Events and audit entries are written with the changes.
	types := outboxTypes(t, f.ctx, f.admin)
	if countType(types, events.LegalHoldPlaced) != 2 || countType(types, events.LegalHoldReleased) != 1 {
		t.Fatalf("outbox %v: want 2 placed and 1 released", types)
	}
	var placed, released int
	_ = f.admin.QueryRow(f.ctx, `select count(*) filter (where kind='legal_hold_placed'), count(*) filter (where kind='legal_hold_released') from audit_trail_entry`).Scan(&placed, &released)
	if placed != 2 || released != 1 {
		t.Fatalf("audit entries placed %d released %d", placed, released)
	}
}

func TestLegalHold_RefusesAnEntityOutsideTheCallersScope(t *testing.T) {
	f := firingSetup(t)
	mine, theirs := ids.New(), ids.New()
	ctx := scopedCtx(f.tenantID(), mine)
	_, err := f.st.PlaceHold(ctx, holdSpec(theirs, "k1"))
	wantProblem(t, err, 403, CodeHoldOutOfScope)
	h, err := f.st.PlaceHold(ctx, holdSpec(mine, "k2"))
	if err != nil {
		t.Fatal(err)
	}
	// The hold is invisible to a caller scoped to another entity.
	other := scopedCtx(f.tenantID(), theirs)
	if got, _ := f.st.ListHolds(other, false); len(got) != 0 {
		t.Fatalf("other entity sees %+v", got)
	}
	_, err = f.st.GetHold(other, h.ID)
	wantProblem(t, err, 404, CodeHoldNotFound)
}

func (f firingFixture) tenantID() string { return tenantID(f.ctx) }

// stored puts two files of the fixture's entity into the library: one retained
// until retained, one a year longer.
func (f firingFixture) stored(t *testing.T, retained time.Time) {
	t.Helper()
	res := f.result(t, apr1,
		FiringOutput{Format: "csv", FileName: "a.csv", ContentType: "text/csv", Content: []byte("aaaa")},
		FiringOutput{Format: "xlsx", FileName: "a.xlsx", ContentType: "application/octet-stream", Content: []byte("bbbbbb")})
	if _, err := f.st.RecordFiring(f.ctx, res); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `update report_library_entry set retain_until = case format when 'csv' then $1::timestamptz else $2::timestamptz end`,
		retained, retained.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
}

func TestSweep_DeletesOnlyPastRetentionAndNeverUnderLegalHold(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC().Truncate(time.Second)
	f.stored(t, now.Add(-time.Hour)) // csv is past retention, xlsx is not

	// Nothing is deleted before retention ends.
	if passes, err := f.st.SweepLibrary(f.ctx, now.AddDate(-1, 0, 0)); err != nil || len(passes) != 0 {
		t.Fatalf("early sweep %+v err %v", passes, err)
	}

	// Under legal hold the past-retention file stays and the pass says so.
	hold, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k1"))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 50)
	for _, e := range page.Items {
		if !e.OnHold {
			t.Fatalf("entry %s should be on hold", e.Format)
		}
	}
	passes, err := f.st.SweepLibrary(f.ctx, now)
	if err != nil || len(passes) != 1 || passes[0].DeletedCount != 0 || passes[0].HeldCount != 1 || passes[0].HoldID != hold.ID || passes[0].LegalEntityID != f.entity {
		t.Fatalf("held sweep %+v err %v", passes, err)
	}
	if page, _ := f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 50); len(page.Items) != 2 {
		t.Fatalf("%d files after the held sweep, want 2", len(page.Items))
	}

	// Released: the past-retention file goes, the other stays.
	rev := hold.Revision
	if _, err := f.st.ReleaseHold(f.ctx, hold.ID, &rev, "", "legal1"); err != nil {
		t.Fatal(err)
	}
	passes, err = f.st.SweepLibrary(f.ctx, now)
	if err != nil || len(passes) != 1 || passes[0].DeletedCount != 1 || passes[0].DeletedBytes != 4 || passes[0].HeldCount != 0 || passes[0].Actor != SweepActor {
		t.Fatalf("sweep %+v err %v", passes, err)
	}
	page, err = f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].Format != "xlsx" || page.Items[0].OnHold {
		t.Fatalf("library after sweep %+v err %v", page, err)
	}
	// A second pass finds nothing and records nothing.
	if passes, err := f.st.SweepLibrary(f.ctx, now); err != nil || len(passes) != 0 {
		t.Fatalf("second sweep %+v err %v", passes, err)
	}
	sweeps, err := f.st.ListSweeps(f.ctx, 10)
	if err != nil || len(sweeps) != 2 || sweeps[0].DeletedCount != 1 || sweeps[1].HeldCount != 1 || sweeps[0].SweptAt.IsZero() {
		t.Fatalf("sweeps %+v err %v", sweeps, err)
	}
	if n := countType(outboxTypes(t, f.ctx, f.admin), events.LibrarySwept); n != 2 {
		t.Fatalf("%d library.swept events, want 2", n)
	}
	var audited int
	_ = f.admin.QueryRow(f.ctx, `select count(*) from audit_trail_entry where kind=$1`, domain.AuditLibrarySwept).Scan(&audited)
	if audited != 2 {
		t.Fatalf("%d library_swept audit entries, want 2", audited)
	}
}

// The database refuses a delete the sweep would not make, whoever issues it.
func TestSweep_TriggerRefusesEarlyAndHeldDeletes(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC().Truncate(time.Second)
	f.stored(t, now.Add(-time.Hour))
	// xlsx is retained for another year: a delete is refused.
	if _, err := f.admin.Exec(f.ctx, `delete from report_library_entry where format='xlsx'`); err == nil || !strings.Contains(err.Error(), "retained until") {
		t.Fatalf("early delete = %v, want the retention refusal", err)
	}
	// Past retention but held.
	if _, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k1")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(f.ctx, `delete from report_library_entry where format='csv'`); err == nil || !strings.Contains(err.Error(), "legal hold") {
		t.Fatalf("held delete = %v, want the legal hold refusal", err)
	}
}

func TestSweep_LeavesFilesWithoutAnEntityAndOtherEntitiesAlone(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC().Truncate(time.Second)
	f.stored(t, now.Add(-time.Hour))
	if _, err := f.admin.Exec(f.ctx, `update report_library_entry set legal_entity_id = null where format='csv'`); err != nil {
		t.Fatal(err)
	}
	passes, err := f.st.SweepLibrary(f.ctx, now)
	if err != nil || len(passes) != 0 {
		t.Fatalf("sweep %+v err %v: a file without an entity is never deleted", passes, err)
	}
	// A hold on another entity does not shield this entity's files, and a
	// caller scoped to the entity sweeps it alone.
	if _, err := f.admin.Exec(f.ctx, `update report_library_entry set legal_entity_id = $1::uuid where format='csv'`, f.entity); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.PlaceHold(f.ctx, holdSpec(ids.New(), "k1")); err != nil {
		t.Fatal(err)
	}
	scoped := scopedCtx(f.tenantID(), ids.New())
	if passes, err := f.st.SweepLibrary(scoped, now); err != nil || len(passes) != 0 {
		t.Fatalf("a caller of another entity swept %+v err %v", passes, err)
	}
	passes, err = f.st.SweepLibrary(f.ctx, now)
	if err != nil || len(passes) != 1 || passes[0].DeletedCount != 1 {
		t.Fatalf("sweep %+v err %v", passes, err)
	}
}

// A hold being placed while the sweep runs is never lost: the sweep of that
// entity waits for the hold's transaction and then keeps the files.
func TestSweep_WaitsForAHoldBeingPlacedAndKeepsTheFiles(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC().Truncate(time.Second)
	f.stored(t, now.Add(-time.Hour))
	tx, err := f.admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	holdID := ids.New()
	if _, err := tx.Exec(f.ctx, `insert into report_legal_hold(tenant_id, id, legal_entity_id, reason, placed_by, idempotency_key, request_hash)
		values($1::uuid, $2::uuid, $3::uuid, 'litigation', 'legal1', 'race', 'h')`, f.tenantID(), holdID, f.entity); err != nil {
		t.Fatal(err)
	}
	type result struct {
		passes []Sweep
		err    error
	}
	done := make(chan result, 1)
	go func() {
		passes, err := f.st.SweepLibrary(f.ctx, now)
		done <- result{passes, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the sweep finished while a hold was being placed: %+v err %v", r.passes, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || len(r.passes) != 1 || r.passes[0].DeletedCount != 0 || r.passes[0].HeldCount != 1 || r.passes[0].HoldID != holdID {
			t.Fatalf("sweep after the hold committed %+v err %v, want the file kept by the hold", r.passes, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the sweep did not resume after the hold committed")
	}
	if page, _ := f.st.ListLibrary(f.ctx, LibraryQuery{}, nil, 50); len(page.Items) != 2 {
		t.Fatalf("%d files after the sweep, want both kept", len(page.Items))
	}
}

// A job clock ahead of the database deletes only what the database clock says
// is past retention, and does not trip the delete guard.
func TestSweep_CutoffNeverPassesTheDatabaseClock(t *testing.T) {
	f := firingSetup(t)
	now := time.Now().UTC().Truncate(time.Second)
	f.stored(t, now.Add(time.Hour)) // csv retained for another hour, xlsx a year longer
	passes, err := f.st.SweepLibrary(f.ctx, now.Add(2*time.Hour))
	if err != nil || len(passes) != 0 {
		t.Fatalf("sweep with the job clock ahead %+v err %v, want nothing deleted and no error", passes, err)
	}
}

// The application role releases a hold through the release columns only: it
// cannot move a hold to another entity or rewrite why it was placed.
func TestLegalHold_AppRoleCannotRewriteAHold(t *testing.T) {
	f := firingSetup(t)
	if _, err := f.st.PlaceHold(f.ctx, holdSpec(f.entity, "k1")); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update report_legal_hold set legal_entity_id = '` + ids.New() + `'`,
		`update report_legal_hold set reason = 'rewritten'`,
		`update report_legal_hold set placed_at = now()`,
	} {
		err := dbx.WithTenantTx(f.ctx, f.st.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(f.ctx, stmt)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s as passage_app = %v, want permission denied", stmt, err)
		}
	}
}
