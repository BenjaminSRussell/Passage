package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func relIn(agreementID, no string, received int64, buckets ...domain.ReleaseBucket) NewScheduleRelease {
	return NewScheduleRelease{AgreementID: agreementID, ReleaseNo: no, ReleaseType: domain.ReleaseJIT, CumReceived: d(received), Buckets: buckets,
		IdempotencyKey: "key-" + no, RequestHash: "hash-" + no, Actor: "edi"}
}

func bucket(date string, cum int64) domain.ReleaseBucket {
	return domain.ReleaseBucket{Date: date, CumRequired: d(cum)}
}

func openQty(t *testing.T, r ScheduleRelease) (n int, total int64) {
	t.Helper()
	for _, l := range r.Lines {
		if l.Status == domain.ReleaseLineOpen {
			n++
			total += l.Quantity.IntPart()
		}
	}
	return n, total
}

// TestPostgres_ScheduleReleases covers FR-27.20, BR-27.12 and AC-27.13: with
// 1,200 shipped a release stating 1,150 received is held with CUM_QTY_MISMATCH
// and changes no schedule line; a release stating 1,200 with 1,500 required by
// 2027-04-15 opens 300; a newer release replaces the open lines atomically;
// a held release can be accepted by override or rejected.
func TestPostgres_ScheduleReleases(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 100000, nil, nil)

	// Shipments of 1,200 so far.
	a, err := s.RecordShipped(ctx, a.ID, a.Version, d(1200), nil, "planner", now)
	if err != nil || !a.CumShipped.Equal(d(1200)) || a.Version != 2 {
		t.Fatalf("record shipped: %+v %v", a, err)
	}

	held, created, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "R1", 1150, bucket("2027-04-15", 1500)), now)
	if err != nil || !created || held.Status != domain.ReleaseHeld || held.Code != "CUM_QTY_MISMATCH" || held.ServiceCode != "SLS-0014" ||
		!held.Difference.Equal(d(-50)) || len(held.Lines) != 0 || !held.OpenQty.IsZero() {
		t.Fatalf("held release: created=%v err=%v %+v", created, err, held)
	}

	ok, created, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "R2", 1200, bucket("2027-04-15", 1500)), now.Add(time.Hour))
	if err != nil || !created || ok.Status != domain.ReleaseAccepted || !ok.OpenQty.Equal(d(300)) || ok.Code != "" {
		t.Fatalf("accepted release: created=%v err=%v %+v", created, err, ok)
	}
	if n, total := openQty(t, ok); n != 1 || total != 300 || ok.Lines[0].DueDate != "2027-04-15" {
		t.Fatalf("open lines %+v", ok.Lines)
	}

	// Replay of the same key and body returns the stored release.
	if again, created, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "R2", 1200, bucket("2027-04-15", 1500)), now); err != nil || created || again.ID != ok.ID || len(again.Lines) != 1 {
		t.Fatalf("replay: created=%v err=%v", created, err)
	}
	reuse := relIn(a.ID, "R2", 1200, bucket("2027-04-15", 1500))
	reuse.RequestHash = "other"
	if _, _, err := s.CreateScheduleRelease(ctx, reuse, now); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	// The same release number under a new key is a duplicate.
	dup := relIn(a.ID, "R2", 1200, bucket("2027-04-15", 1500))
	dup.IdempotencyKey = "another"
	if _, _, err := s.CreateScheduleRelease(ctx, dup, now); problemCode(t, err) != CodeReleaseDuplicate {
		t.Fatalf("duplicate release number: %v", err)
	}
	// Invalid buckets.
	if _, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "R3", 1200, bucket("2027-04-15", 1500), bucket("2027-04-01", 1600)), now); problemCode(t, err) != CodeInvalidRelease {
		t.Fatalf("unordered buckets: %v", err)
	}

	// A newer release replaces the open lines in one transaction.
	next, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "R4", 1200, bucket("2027-04-10", 1400), bucket("2027-05-01", 1700)), now.Add(2*time.Hour))
	if err != nil || next.Status != domain.ReleaseAccepted || !next.OpenQty.Equal(d(500)) {
		t.Fatalf("replacement: %v %+v", err, next)
	}
	if n, total := openQty(t, next); n != 2 || total != 500 {
		t.Fatalf("replacement lines %+v", next.Lines)
	}
	old, err := s.GetScheduleRelease(ctx, a.ID, ok.ID)
	if err != nil || old.Status != domain.ReleaseSuperseded {
		t.Fatalf("old release: %v %+v", err, old)
	}
	if n, _ := openQty(t, old); n != 0 {
		t.Fatalf("superseded lines still open: %+v", old.Lines)
	}
	if n := countWhere(t, pool, `select count(*) from schedule_release_line where agreement_id = $1 and status = 'OPEN'`, a.ID); n != 2 {
		t.Fatalf("%d open lines on the agreement, want 2", n)
	}
	list, err := s.ListScheduleReleases(ctx, a.ID, 50)
	if err != nil || len(list) != 3 || list[0].ReleaseNo != "R4" || len(list[0].Lines) != 0 {
		t.Fatalf("list: %v %+v", err, list)
	}

	// The held release R1 cannot be accepted: R2 and R4 are newer. Reject it.
	h, err := s.GetScheduleRelease(ctx, a.ID, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveScheduleRelease(ctx, a.ID, h.ID, h.Version+1, domain.DecisionReject, "x", "mgr", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.ResolveScheduleRelease(ctx, a.ID, h.ID, h.Version, domain.DecisionAccept, "override", "mgr", now); problemCode(t, err) != CodeReleaseNotHeld {
		t.Fatalf("older held release must not replace a newer one: %v", err)
	}
	rej, err := s.ResolveScheduleRelease(ctx, a.ID, h.ID, h.Version, domain.DecisionReject, "customer sent a corrected release", "mgr", now)
	if err != nil || rej.Status != domain.ReleaseRejected || rej.ResolvedBy == nil || *rej.ResolvedBy != "mgr" || rej.Version != h.Version+1 {
		t.Fatalf("reject: %v %+v", err, rej)
	}
	if _, err := s.ResolveScheduleRelease(ctx, a.ID, h.ID, rej.Version, domain.DecisionReject, "again", "mgr", now); problemCode(t, err) != CodeReleaseNotHeld {
		t.Fatalf("resolve twice: %v", err)
	}
	if n, _ := openQty(t, mustRelease(t, s, ctx, a.ID, next.ID)); n != 2 {
		t.Fatal("rejecting a held release must not touch the open lines")
	}
	if _, err := s.GetScheduleRelease(ctx, a.ID, ids.New()); problemCode(t, err) != CodeReleaseNotFound {
		t.Fatalf("unknown release: %v", err)
	}
	if _, err := s.GetScheduleRelease(ctx, ids.New(), next.ID); problemCode(t, err) != CodeReleaseNotFound {
		t.Fatalf("release under another agreement: %v", err)
	}

	types := outboxTypes(t, pool, held.ID)
	if len(types) != 2 || types[0] != salesevents.ScheduleReleaseReceived || types[1] != salesevents.ScheduleReleaseResolved {
		t.Fatalf("held release events %v", types)
	}
	var ev salesevents.ScheduleReleaseData
	if err := json.Unmarshal(outboxData(t, pool, next.ID, salesevents.ScheduleReleaseReceived), &ev); err != nil ||
		ev.Status != "ACCEPTED" || ev.SupersededReleaseID != ok.ID || len(ev.Lines) != 2 || ev.OpenQty != "500" || ev.AgreementNumber != a.AgreementNumber {
		t.Fatalf("received event %+v %v", ev, err)
	}
}

func mustRelease(t *testing.T, s *Postgres, ctx context.Context, agreementID, id string) ScheduleRelease {
	t.Helper()
	r, err := s.GetScheduleRelease(ctx, agreementID, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestPostgres_ScheduleReleaseOverrideAndTolerance: a held release accepted by
// override takes the open lines against the current shipped quantity, and a
// difference within the agreement's tolerance is not held.
func TestPostgres_ScheduleReleaseOverrideAndTolerance(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	customer := ids.New()
	a, _, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, ProductID: ids.New(), Unit: "EA",
		UnitPrice: d(1), Currency: "USD", Commitment: d(1000000), CumTolerance: d(10), IdempotencyKey: ids.New(), RequestHash: "a", Actor: "k"})
	if err != nil || !a.CumTolerance.Equal(d(10)) {
		t.Fatalf("agreement with tolerance: %v %+v", err, a)
	}
	a, err = s.RecordShipped(ctx, a.ID, a.Version, d(500), nil, "planner", now)
	if err != nil {
		t.Fatal(err)
	}
	within, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "W1", 508, bucket("2027-06-01", 800)), now)
	if err != nil || within.Status != domain.ReleaseAccepted || !within.OpenQty.Equal(d(300)) {
		t.Fatalf("within tolerance: %v %+v", err, within)
	}
	held, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "W2", 480, bucket("2027-06-01", 900), bucket("2027-07-01", 1000)), now.Add(time.Hour))
	if err != nil || held.Status != domain.ReleaseHeld {
		t.Fatalf("beyond tolerance: %v %+v", err, held)
	}
	if n := countWhere(t, pool, `select count(*) from schedule_release_line where agreement_id = $1 and status = 'OPEN'`, a.ID); n != 1 {
		t.Fatalf("a held release must leave the open lines alone, have %d", n)
	}
	// 100 more shipped before the reviewer decides: open quantity follows.
	a, err = s.RecordShipped(ctx, a.ID, a.Version, d(600), nil, "planner", now)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := s.ResolveScheduleRelease(ctx, a.ID, held.ID, held.Version, domain.DecisionAccept, "customer confirmed", "mgr", now.Add(2*time.Hour))
	if err != nil || acc.Status != domain.ReleaseAccepted || !acc.OpenQty.Equal(d(400)) || !acc.CumShipped.Equal(d(600)) {
		t.Fatalf("override: %v %+v", err, acc)
	}
	if n, total := openQty(t, acc); n != 2 || total != 400 {
		t.Fatalf("override lines %+v", acc.Lines)
	}
	if prev := mustRelease(t, s, ctx, a.ID, within.ID); prev.Status != domain.ReleaseSuperseded {
		t.Fatalf("previous release %s", prev.Status)
	}
	var ev salesevents.ScheduleReleaseData
	if err := json.Unmarshal(outboxData(t, pool, held.ID, salesevents.ScheduleReleaseResolved), &ev); err != nil ||
		ev.Decision != "ACCEPT" || ev.SupersededReleaseID != within.ID || ev.Reason != "customer confirmed" {
		t.Fatalf("resolved event %+v %v", ev, err)
	}
}

// TestPostgres_RecordShipped covers the cumulative shipped rules and the
// recalculation of the release in effect.
func TestPostgres_RecordShipped(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 100000, nil, nil)

	// No release in effect: only the agreement moves.
	a, err := s.RecordShipped(ctx, a.ID, a.Version, d(100), nil, "planner", now)
	if err != nil || a.Version != 2 || len(outboxTypes(t, pool, a.ID)) != 0 {
		t.Fatalf("no release: %v %+v", err, a)
	}
	if _, err := s.RecordShipped(ctx, a.ID, 1, d(200), nil, "planner", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.RecordShipped(ctx, a.ID, a.Version, d(50), nil, "planner", now); problemCode(t, err) != CodeShippedRefused {
		t.Fatalf("decrease: %v", err)
	}
	if _, err := s.RecordShipped(ctx, ids.New(), 1, d(50), nil, "planner", now); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("unknown agreement: %v", err)
	}

	rel, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "S1", 100, bucket("2027-04-01", 300), bucket("2027-05-01", 500)), now)
	if err != nil || !rel.OpenQty.Equal(d(400)) {
		t.Fatalf("release: %v %+v", err, rel)
	}
	a, err = s.RecordShipped(ctx, a.ID, a.Version, d(350), nil, "planner", now)
	if err != nil {
		t.Fatal(err)
	}
	cur := mustRelease(t, s, ctx, a.ID, rel.ID)
	if !cur.OpenQty.Equal(d(150)) || !cur.CumShipped.Equal(d(350)) || cur.Version != rel.Version+1 {
		t.Fatalf("recalculated release %+v", cur)
	}
	if n, total := openQty(t, cur); n != 1 || total != 150 || cur.Lines[len(cur.Lines)-1].DueDate != "2027-05-01" {
		t.Fatalf("recalculated lines %+v", cur.Lines)
	}
	if types := outboxTypes(t, pool, rel.ID); len(types) != 2 || types[1] != salesevents.ScheduleReleaseRecalculated {
		t.Fatalf("events %v", types)
	}

	// A held release received in the same period, to be decided after the reset.
	held, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "S2", 340, bucket("2027-05-01", 520), bucket("2027-06-01", 600)), now.Add(time.Hour))
	if err != nil || held.Status != domain.ReleaseHeld {
		t.Fatalf("held: %v %+v", err, held)
	}

	// A new cumulative period may start lower. The 350 shipped in the closed
	// period still count against the releases received in it, so nothing
	// already shipped is reopened (BR-27.12: open = required - shipped).
	reset := "2028-01-01"
	a, err = s.RecordShipped(ctx, a.ID, a.Version, d(0), &reset, "planner", now)
	if err != nil || a.CumResetDate == nil || *a.CumResetDate != reset || !a.CumShipped.IsZero() {
		t.Fatalf("reset: %v %+v", err, a)
	}
	if cur = mustRelease(t, s, ctx, a.ID, rel.ID); !cur.OpenQty.Equal(d(150)) || !cur.ShippedOffset.Equal(d(350)) || !cur.CumShipped.Equal(d(350)) {
		t.Fatalf("after reset %+v", cur)
	}
	// Shipments of the new period reduce the same open lines.
	a, err = s.RecordShipped(ctx, a.ID, a.Version, d(100), nil, "planner", now)
	if err != nil {
		t.Fatal(err)
	}
	if cur = mustRelease(t, s, ctx, a.ID, rel.ID); !cur.OpenQty.Equal(d(50)) {
		t.Fatalf("after shipping in the new period %+v", cur)
	}
	// The release held across the reset is accepted on its own basis:
	// 600 required less 350 + 100 shipped.
	acc, err := s.ResolveScheduleRelease(ctx, a.ID, held.ID, held.Version, domain.DecisionAccept, "customer confirmed", "mgr", now.Add(2*time.Hour))
	if err != nil || !acc.OpenQty.Equal(d(150)) || !acc.ShippedOffset.Equal(d(350)) {
		t.Fatalf("override after reset: %v %+v", err, acc)
	}
	// A release received in the new period reconciles on the new basis only.
	fresh, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "S3", 100, bucket("2028-02-01", 180)), now.Add(3*time.Hour))
	if err != nil || fresh.Status != domain.ReleaseAccepted || !fresh.OpenQty.Equal(d(80)) || !fresh.ShippedOffset.IsZero() {
		t.Fatalf("release in the new period: %v %+v", err, fresh)
	}
	back := "2027-01-01"
	if _, err := s.RecordShipped(ctx, a.ID, a.Version, d(0), &back, "planner", now); problemCode(t, err) != CodeShippedRefused {
		t.Fatalf("reset moves back: %v", err)
	}
}

// TestPostgres_ScheduleReleaseValidityAndScope: a release is refused outside
// the agreement's validity, and releases are invisible to a caller scoped to
// another legal entity or tenant, who can neither store, resolve nor record
// shipments against them.
func TestPostgres_ScheduleReleaseValidityAndScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	ended := "2027-02-28"
	expired := agreementFixture(t, s, ctx, le, 1000, nil, &ended)
	if _, _, err := s.CreateScheduleRelease(ctx, relIn(expired.ID, "E1", 0, bucket("2027-04-01", 10)), now); problemCode(t, err) != CodeAgreementNotValid {
		t.Fatalf("expired agreement: %v", err)
	}

	a := agreementFixture(t, s, ctx, le, 1000, nil, nil)
	held, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "X1", 5, bucket("2027-04-01", 10)), now)
	if err != nil || held.Status != domain.ReleaseHeld {
		t.Fatalf("held: %v %+v", err, held)
	}
	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if _, err := s.ListScheduleReleases(c, a.ID, 50); problemCode(t, err) != CodeAgreementNotFound {
			t.Fatalf("%s: list %v", name, err)
		}
		if _, err := s.GetScheduleRelease(c, a.ID, held.ID); problemCode(t, err) != CodeReleaseNotFound {
			t.Fatalf("%s: get %v", name, err)
		}
		in := relIn(a.ID, "X2", 0, bucket("2027-04-01", 10))
		in.IdempotencyKey = name
		if _, _, err := s.CreateScheduleRelease(c, in, now); problemCode(t, err) != CodeAgreementNotFound {
			t.Fatalf("%s: create %v", name, err)
		}
		if _, err := s.ResolveScheduleRelease(c, a.ID, held.ID, held.Version, domain.DecisionReject, "x", "x", now); problemCode(t, err) != CodeAgreementNotFound {
			t.Fatalf("%s: resolve %v", name, err)
		}
		if _, err := s.RecordShipped(c, a.ID, a.Version, d(1), nil, "x", now); problemCode(t, err) != CodeAgreementNotFound {
			t.Fatalf("%s: record shipped %v", name, err)
		}
	}
	own := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "x", Duties: testkit.ScopeDuties(le)})
	if list, err := s.ListScheduleReleases(own, a.ID, 50); err != nil || len(list) != 1 || list[0].ID != held.ID {
		t.Fatalf("own legal entity: %v %+v", err, list)
	}
}
