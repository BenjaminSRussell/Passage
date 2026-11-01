package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// TestPostgres_AgreementRevisionAndClose covers FR-27.10: a revision at the
// ETag changes commitment, price and validity with a diff and agreement.changed,
// a commitment below the released quantity is refused, a stale ETag is 412, a
// release after the revision uses the new price, and a closed agreement takes
// no releases or revisions.
func TestPostgres_AgreementRevisionAndClose(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 1000, nil, nil)
	r1, _, err := s.ReleaseAgreement(ctx, a.ID, release(600, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = s.GetAgreement(ctx, a.ID)
	if a.Version != 2 {
		t.Fatalf("version %d", a.Version)
	}

	below := d(599)
	if _, err := s.ReviseAgreement(ctx, a.ID, a.Version, AgreementRevision{Commitment: &below}, "ka", now); problemCode(t, err) != CodeCommitmentBelow {
		t.Fatalf("below released: %v", err)
	}
	more, price, until := d(1500), d(15), "2027-12-31"
	if _, err := s.ReviseAgreement(ctx, a.ID, 1, AgreementRevision{Commitment: &more}, "ka", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale: %v", err)
	}
	// another legal entity cannot see the agreement, so cannot revise, close or read its history
	stranger := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")})
	if _, err := s.ReviseAgreement(stranger, a.ID, a.Version, AgreementRevision{Commitment: &more}, "x", now); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("revise from another legal entity: %v", err)
	}
	if _, err := s.CloseAgreement(stranger, a.ID, a.Version, "x", "x", now); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("close from another legal entity: %v", err)
	}
	if _, err := s.ListAgreementVersions(stranger, a.ID); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("history from another legal entity: %v", err)
	}
	rev, err := s.ReviseAgreement(ctx, a.ID, a.Version, AgreementRevision{Commitment: &more, UnitPrice: &price, ValidTo: &until, Reason: "volume deal"}, "ka", now)
	if err != nil || rev.Version != 3 || !rev.Commitment.Equal(d(1500)) || !rev.Remaining.Equal(d(900)) || !rev.UnitPrice.Equal(d(15)) || rev.ValidTo == nil || *rev.ValidTo != until {
		t.Fatalf("revised %+v %v", rev, err)
	}
	same, err := s.ReviseAgreement(ctx, a.ID, rev.Version, AgreementRevision{Commitment: &more}, "ka", now)
	if err != nil || same.Version != 3 {
		t.Fatalf("no-op must not bump the version: %+v %v", same, err)
	}
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(100, "r2"), now)
	if err != nil || !res.Order.Lines[0].UnitPrice.Equal(d(15)) {
		t.Fatalf("release at the new price: %+v %v", res.Order.Lines, err)
	}
	// the first order keeps the price it was released at
	first, err := s.GetOrder(ctx, r1.Order.ID)
	if err != nil || !first.Lines[0].UnitPrice.Equal(d(12)) {
		t.Fatalf("first order repriced: %+v %v", first.Lines, err)
	}

	vs, err := s.ListAgreementVersions(ctx, a.ID)
	if err != nil || len(vs) != 1 || vs[0].Kind != "REVISED" || vs[0].Reason != "volume deal" || len(vs[0].Diff) != 3 || vs[0].Version != 3 {
		t.Fatalf("history %+v %v", vs, err)
	}
	if _, err := s.ListAgreementVersions(ctx, ids.New()); problemCode(t, err) != CodeAgreementNotFound {
		t.Fatalf("unknown agreement history: %v", err)
	}

	cur, _ := s.GetAgreement(ctx, a.ID)
	if _, err := s.CloseAgreement(ctx, a.ID, 1, "customer left", "ka", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("close stale: %v", err)
	}
	closed, err := s.CloseAgreement(ctx, a.ID, cur.Version, "customer left", "ka", now)
	if err != nil || closed.Status != domain.AgreementClosed || closed.CloseReason == nil || *closed.CloseReason != "customer left" || closed.ClosedAt == nil || !closed.Released.Equal(d(700)) {
		t.Fatalf("closed %+v %v", closed, err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(1, "r3"), now); problemCode(t, err) != CodeAgreementClosed {
		t.Fatalf("release on closed: %v", err)
	}
	if _, _, err := s.CreateScheduleRelease(ctx, relIn(a.ID, "SR-1", 0, bucket("2027-04-01", 10)), now); problemCode(t, err) != CodeAgreementClosed {
		t.Fatalf("schedule release on closed: %v", err)
	}
	if _, err := s.ReviseAgreement(ctx, a.ID, closed.Version, AgreementRevision{Commitment: &more}, "ka", now); problemCode(t, err) != CodeAgreementClosed {
		t.Fatalf("revise closed: %v", err)
	}
	if _, err := s.CloseAgreement(ctx, a.ID, closed.Version, "again", "ka", now); problemCode(t, err) != CodeAgreementClosed {
		t.Fatalf("close twice: %v", err)
	}
	want := []string{salesevents.AgreementReleased, salesevents.AgreementChanged, salesevents.AgreementReleased, salesevents.AgreementClosed}
	got := outboxTypes(t, pool, a.ID)
	if len(got) != len(want) {
		t.Fatalf("outbox %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outbox %v, want %v", got, want)
		}
	}
}

// TestPostgres_CancelledReleaseReturnsQuantity covers BR-27.9: cancelling an
// order created by a release gives its quantity back (an EXHAUSTED agreement is
// ACTIVE again), once, with agreement.returned; on a closed agreement the
// quantity returns and the status stays CLOSED.
func TestPostgres_CancelledReleaseReturnsQuantity(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 100, nil, nil)
	r1, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := s.ReleaseAgreement(ctx, a.ID, release(40, "r2"), now)
	if err != nil || r2.Agreement.Status != domain.AgreementExhausted {
		t.Fatalf("exhausted: %+v %v", r2.Agreement, err)
	}

	if _, err := s.CancelOrder(ctx, r1.Order.ID, r1.Order.Version, "customer changed mind", "rep", now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAgreement(ctx, a.ID)
	if got.Status != domain.AgreementActive || !got.Released.Equal(d(40)) || !got.Remaining.Equal(d(60)) || got.Version != r2.Agreement.Version+1 {
		t.Fatalf("after cancel %+v", got)
	}
	returned := 0
	for _, r := range got.Releases {
		if r.ReturnedAt != nil {
			returned++
			if r.SalesOrderID != r1.Order.ID {
				t.Fatalf("returned the wrong release %+v", r)
			}
		}
	}
	if returned != 1 {
		t.Fatalf("returned releases %d", returned)
	}
	if n := countWhere(t, pool, `select count(*) from agreement_return where agreement_id = $1`, a.ID); n != 1 {
		t.Fatalf("agreement_return rows %d", n)
	}
	// the freed quantity can be released again
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r3"), now); err != nil {
		t.Fatalf("release the returned quantity: %v", err)
	}
	vs, _ := s.ListAgreementVersions(ctx, a.ID)
	if len(vs) != 1 || vs[0].Kind != "RETURNED" {
		t.Fatalf("history %+v", vs)
	}
	types := outboxTypes(t, pool, a.ID)
	found := false
	for _, ty := range types {
		found = found || ty == salesevents.AgreementReturned
	}
	if !found {
		t.Fatalf("outbox %v", types)
	}

	// closed agreement: quantity returns, status stays CLOSED
	cur, _ := s.GetAgreement(ctx, a.ID)
	if _, err := s.CloseAgreement(ctx, a.ID, cur.Version, "ended", "ka", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelOrder(ctx, r2.Order.ID, r2.Order.Version, "", "rep", now); err != nil {
		t.Fatal(err)
	}
	end, _ := s.GetAgreement(ctx, a.ID)
	if end.Status != domain.AgreementClosed || !end.Released.Equal(d(60)) {
		t.Fatalf("closed agreement after a return %+v", end)
	}
}
