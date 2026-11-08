package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func toleranceAgreement(t *testing.T, s *Postgres, ctx context.Context, le string, commitment int64, pct string) Agreement {
	t.Helper()
	customer := ids.New()
	a, _, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, ProductID: ids.New(),
		Unit: "EA", UnitPrice: d(12), Currency: "USD", Commitment: d(commitment), OverdeliveryPct: decimal.RequireFromString(pct),
		IdempotencyKey: ids.New(), RequestHash: "tolerance", Actor: "key-account"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestPostgres_AgreementOverdeliveryTolerance covers BR-27.9: with a 5 percent
// tolerance a 1,000-unit agreement releases up to 1,050 and is EXHAUSTED only
// there; the next release is AGREEMENT_EXHAUSTED and changes nothing. A
// revision that cuts the tolerance below the released quantity is refused.
func TestPostgres_AgreementOverdeliveryTolerance(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a, _, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, CustomerID: ids.New(), InvoiceAccountID: ids.New(), ProductID: ids.New(),
		Unit: "EA", UnitPrice: d(12), Currency: "USD", Commitment: d(1000), OverdeliveryPct: decimal.RequireFromString("5"),
		IdempotencyKey: ids.New(), RequestHash: "tolerance", Actor: "key-account"})
	if err != nil || !a.CeilingQty.Equal(d(1050)) || !a.Remaining.Equal(d(1050)) || !a.OverdeliveryPct.Equal(d(5)) {
		t.Fatalf("new agreement %+v %v", a, err)
	}
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(1000, "r1"), now)
	if err != nil || res.Agreement.Status != domain.AgreementActive || !res.Agreement.Remaining.Equal(d(50)) {
		t.Fatalf("the commitment itself leaves the tolerance open: %+v %v", res.Agreement, err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(51, "r2"), now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("beyond the ceiling: %v", err)
	}
	last, _, err := s.ReleaseAgreement(ctx, a.ID, release(50, "r3"), now)
	if err != nil || last.Agreement.Status != domain.AgreementExhausted || !last.Agreement.Released.Equal(d(1050)) || !last.Agreement.Remaining.IsZero() {
		t.Fatalf("draw to the ceiling: %+v %v", last.Agreement, err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(1, "r4"), now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("exhausted: %v", err)
	}
	low := decimal.RequireFromString("4.9")
	if _, err := s.ReviseAgreement(ctx, a.ID, last.Agreement.Version, AgreementRevision{OverdeliveryPct: &low}, "ka", now); problemCode(t, err) != CodeCommitmentBelow {
		t.Fatalf("tolerance below released: %v", err)
	}
	up := decimal.RequireFromString("10")
	rev, err := s.ReviseAgreement(ctx, a.ID, last.Agreement.Version, AgreementRevision{OverdeliveryPct: &up, Reason: "wider tolerance"}, "ka", now)
	if err != nil || rev.Status != domain.AgreementActive || !rev.Remaining.Equal(d(50)) || !rev.CeilingQty.Equal(d(1100)) {
		t.Fatalf("a wider tolerance reopens the agreement: %+v %v", rev, err)
	}
}

// TestPostgres_ReleaseLineQuantityFollowsAgreement covers BR-27.9: lowering the
// quantity of line 1 of a released order gives the difference back, raising it
// draws the difference (refused beyond the ceiling, with nothing stored), and
// cancelling the order afterwards gives back only what is still outstanding.
func TestPostgres_ReleaseLineQuantityFollowsAgreement(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := toleranceAgreement(t, s, ctx, le, 100, "10")
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	order := res.Order
	qty := func(n int64) []LineChange {
		v := d(n)
		return []LineChange{{LineNo: 1, Quantity: &v}}
	}
	lowered, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: qty(40), Reason: "customer needs less"}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(40)) || !got.Remaining.Equal(d(70)) {
		t.Fatalf("lowered 60 to 40: %+v", got)
	}
	raised, err := s.ChangeOrder(ctx, order.ID, lowered.Version, ChangeRequest{Lines: qty(105)}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAgreement(ctx, a.ID)
	if !got.Released.Equal(d(105)) || !got.Remaining.Equal(d(5)) || got.Status != domain.AgreementActive {
		t.Fatalf("raised to 105 within 110: %+v", got)
	}
	versionBefore := got.Version
	if _, err := s.ChangeOrder(ctx, order.ID, raised.Version, ChangeRequest{Lines: qty(111)}, "rep", now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("raise beyond the ceiling: %v", err)
	}
	if after, _ := s.GetAgreement(ctx, a.ID); !after.Released.Equal(d(105)) || after.Version != versionBefore {
		t.Fatalf("a refused raise changes nothing: %+v", after)
	}
	if o, _ := s.GetOrder(ctx, order.ID); o.Version != raised.Version || !o.Lines[0].Quantity.Equal(d(105)) {
		t.Fatalf("the refused PATCH left the order alone: v%d %s", o.Version, o.Lines[0].Quantity)
	}
	full, err := s.ChangeOrder(ctx, order.ID, raised.Version, ChangeRequest{Lines: qty(110)}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = s.GetAgreement(ctx, a.ID); got.Status != domain.AgreementExhausted || !got.Remaining.IsZero() {
		t.Fatalf("drawn to the ceiling: %+v", got)
	}
	if _, err := s.CancelOrder(ctx, order.ID, full.Version, "dropped", "rep", now); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAgreement(ctx, a.ID)
	if got.Status != domain.AgreementActive || !got.Released.IsZero() || !got.Remaining.Equal(d(110)) {
		t.Fatalf("a cancel gives back what is outstanding, 110 not 60: %+v", got)
	}
	if len(got.Releases) != 1 {
		t.Fatalf("releases %+v", got.Releases)
	}
	r := got.Releases[0]
	if !r.Quantity.Equal(d(60)) || !r.DrawnQty.Equal(d(70)) || !r.ReturnedQty.Equal(d(130)) || r.ReturnedAt == nil {
		t.Fatalf("release record %+v", r)
	}
	if n := countWhere(t, pool, `select count(*) from agreement_return where release_id = $1`, r.ID); n != 2 {
		t.Fatalf("a release returned in parts: %d return rows", n)
	}
	if n := countWhere(t, pool, `select count(*) from agreement_draw where release_id = $1`, r.ID); n != 2 {
		t.Fatalf("draw rows %d", n)
	}
	kinds := map[string]int{}
	vs, _ := s.ListAgreementVersions(ctx, a.ID)
	for _, v := range vs {
		kinds[v.Kind]++
	}
	if kinds["DRAWN"] != 2 || kinds["RETURNED"] != 2 {
		t.Fatalf("history %v", kinds)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2`, a.ID, salesevents.AgreementDrawn); n != 2 {
		t.Fatalf("agreement.drawn events = %d", n)
	}
}

// TestPostgres_ReleaseLineRaiseChecksTheAgreement: a raise outside the validity
// period or on a closed agreement is refused, a lowered quantity on a closed
// agreement still returns, and an order that was not released from an agreement
// draws and returns nothing.
func TestPostgres_ReleaseLineRaiseChecksTheAgreement(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	to := "2027-03-31"
	a := agreementFixture(t, s, ctx, le, 100, nil, &to)
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(10, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	order := res.Order
	more, less := d(20), d(5)
	late := time.Date(2027, 4, 2, 9, 0, 0, 0, time.UTC)
	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: &more}}}, "rep", late); problemCode(t, err) != CodeAgreementNotValid {
		t.Fatalf("raise after the validity: %v", err)
	}
	cur, _ := s.GetAgreement(ctx, a.ID)
	closed, err := s.CloseAgreement(ctx, a.ID, cur.Version, "ended", "ka", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: &more}}}, "rep", now); problemCode(t, err) != CodeAgreementClosed {
		t.Fatalf("raise on a closed agreement: %v", err)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: &less}}}, "rep", now); err != nil {
		t.Fatalf("lowering on a closed agreement: %v", err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); got.Status != domain.AgreementClosed || !got.Released.Equal(d(5)) || got.Version != closed.Version+1 {
		t.Fatalf("closed agreement after the return: %+v", got)
	}
}

// TestPostgres_RemainderOfAnAddedLineReturnsNothing: a release can now be given
// back in parts, so only the release line's cut may reach the agreement. Closing
// the remainder of a line added by PATCH (which drew nothing) leaves the
// agreement alone; closing the release line's remainder then returns its
// undelivered 15, once.
func TestPostgres_RemainderOfAnAddedLineReturnsNothing(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := agreementFixture(t, s, ctx, le, 100, nil, nil)
	r, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.ChangeOrder(ctx, r.Order.ID, r.Order.Version, ChangeRequest{AddLines: []NewLine{newLine(10, 3)}}, "rep", now)
	if err != nil || len(added.Lines) != 2 {
		t.Fatalf("add: %+v %v", added.Lines, err)
	}
	if res, err := s.ConfirmOrder(ctx, r.Order.ID, added.Version, "rep", now); err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v %+v", err, res.Evaluation)
	}
	deliver(t, s, ctx, r.Order.ID, "PS-1", 1, 45)
	deliver(t, s, ctx, r.Order.ID, "PS-2", 2, 4)
	cur, _ := s.GetOrder(ctx, r.Order.ID)
	cut2, err := s.CancelLineRemainder(ctx, r.Order.ID, 2, cur.Version, "line 2 short", "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(60)) {
		t.Fatalf("the added line's cut gave %s back", d(60).Sub(got.Released))
	}
	if _, err := s.CancelLineRemainder(ctx, r.Order.ID, 1, cut2.Version, "line 1 short", "rep", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(45)) {
		t.Fatalf("the release line's cut returns 15: released %s", got.Released)
	}
	if n := countWhere(t, pool, `select count(*) from agreement_return where agreement_id = $1`, a.ID); n != 1 {
		t.Fatalf("return rows %d", n)
	}
}
