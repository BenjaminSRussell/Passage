package store

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

func amountAgreement(t *testing.T, s *Postgres, ctx context.Context, le string, commitment int64) Agreement {
	t.Helper()
	customer := ids.New()
	a, _, err := s.CreateAgreement(ctx, NewAgreement{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, ProductID: ids.New(),
		Unit: "EA", UnitPrice: d(12), Currency: "USD", CommitmentType: domain.CommitmentAmount, Commitment: d(commitment),
		IdempotencyKey: ids.New(), RequestHash: "amount", Actor: "key-account"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestPostgres_AmountAgreementDrawsLineValue covers FR-27.10 and BR-27.9 for a
// commitment by amount: a release draws the net value of its line (50 units at
// 12.00 = 600.00 of 1,000.00), a release worth more than what is left is
// AGREEMENT_EXHAUSTED and changes nothing, PATCH of the release line quantity
// draws and returns at the line price, a revision takes commitmentAmount only,
// and a cancel returns what is outstanding.
func TestPostgres_AmountAgreementDrawsLineValue(t *testing.T) {
	s, pool, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := amountAgreement(t, s, ctx, le, 1000)
	if a.CommitmentType != domain.CommitmentAmount || a.CommitmentUnit != "USD" || !a.Remaining.Equal(d(1000)) {
		t.Fatalf("new amount agreement %+v", a)
	}
	res, _, err := s.ReleaseAgreement(ctx, a.ID, release(50, "r1"), now)
	if err != nil || !res.Agreement.Released.Equal(d(600)) || !res.Agreement.Remaining.Equal(d(400)) || res.Agreement.Status != domain.AgreementActive {
		t.Fatalf("50 x 12.00 draws 600.00: %+v %v", res.Agreement, err)
	}
	if _, _, err := s.ReleaseAgreement(ctx, a.ID, release(34, "r2"), now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("408.00 against 400.00 left: %v", err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(600)) {
		t.Fatalf("a refused release changes nothing: %+v", got)
	}
	order := res.Order
	qty := func(n int64) []LineChange {
		v := d(n)
		return []LineChange{{LineNo: 1, Quantity: &v}}
	}
	lowered, err := s.ChangeOrder(ctx, order.ID, order.Version, ChangeRequest{Lines: qty(40)}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(480)) {
		t.Fatalf("50 to 40 units returns 120.00: %+v", got)
	}
	raised, err := s.ChangeOrder(ctx, order.ID, lowered.Version, ChangeRequest{Lines: qty(83)}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(996)) || !got.Remaining.Equal(d(4)) {
		t.Fatalf("40 to 83 units draws 516.00: %+v", got)
	}
	if _, err := s.ChangeOrder(ctx, order.ID, raised.Version, ChangeRequest{Lines: qty(84)}, "rep", now); problemCode(t, err) != CodeAgreementExhausted {
		t.Fatalf("84 units is 1,008.00: %v", err)
	}
	qtyField := d(900)
	cur, _ := s.GetAgreement(ctx, a.ID)
	if _, err := s.ReviseAgreement(ctx, a.ID, cur.Version, AgreementRevision{Commitment: &qtyField}, "ka", now); problemCode(t, err) != "COM-0001" {
		t.Fatalf("commitmentQty on an AMOUNT agreement: %v", err)
	}
	zero := d(0)
	if _, err := s.ReviseAgreement(ctx, a.ID, cur.Version, AgreementRevision{UnitPrice: &zero}, "ka", now); problemCode(t, err) != "COM-0001" {
		t.Fatalf("a zero price on an AMOUNT agreement: %v", err)
	}
	if _, err := s.ReviseAgreement(ctx, a.ID, cur.Version, AgreementRevision{Commitment: &qtyField, CommitmentIsAmount: true}, "ka", now); problemCode(t, err) != CodeCommitmentBelow {
		t.Fatalf("an amount below the released 996.00: %v", err)
	}
	more := d(1500)
	rev, err := s.ReviseAgreement(ctx, a.ID, cur.Version, AgreementRevision{Commitment: &more, CommitmentIsAmount: true, Reason: "extended"}, "ka", now)
	if err != nil || !rev.Remaining.Equal(d(504)) {
		t.Fatalf("raise the amount: %+v %v", rev, err)
	}
	vs, _ := s.ListAgreementVersions(ctx, a.ID)
	if len(vs) == 0 || vs[0].Diff[0].Field != "commitmentAmount" {
		t.Fatalf("history names the amount: %+v", vs)
	}
	if _, err := s.CancelOrder(ctx, order.ID, raised.Version, "dropped", "rep", now); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetAgreement(ctx, a.ID)
	if !got.Released.IsZero() || !got.Remaining.Equal(d(1500)) || len(got.Releases) != 1 {
		t.Fatalf("a cancel returns the outstanding 996.00: %+v", got)
	}
	r := got.Releases[0]
	if !r.Quantity.Equal(d(50)) || !r.Units.Equal(d(600)) || !r.DrawnQty.Equal(d(516)) || !r.ReturnedQty.Equal(d(1116)) {
		t.Fatalf("release record %+v", r)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where aggregate_id = $1 and type = $2 and payload->'data'->>'commitment_type' = 'AMOUNT' and payload->'data'->>'drawn_units' = '600'`, a.ID, "dev.passage.sales.agreement.released.v1"); n != 1 {
		t.Fatalf("agreement.released carries the commitment type: %d", n)
	}
}

// TestPostgres_AmountAgreementRemainderReturnsValue: closing the undelivered
// remainder of a released line gives back its value at the release price.
func TestPostgres_AmountAgreementRemainderReturnsValue(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := amountAgreement(t, s, ctx, le, 1000)
	r, _, err := s.ReleaseAgreement(ctx, a.ID, release(60, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.ConfirmOrder(ctx, r.Order.ID, r.Order.Version, "rep", now); err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v %+v", err, res.Evaluation)
	}
	deliver(t, s, ctx, r.Order.ID, "PS-1", 1, 45)
	cur, _ := s.GetOrder(ctx, r.Order.ID)
	if _, err := s.CancelLineRemainder(ctx, r.Order.ID, 1, cur.Version, "short", "rep", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(540)) {
		t.Fatalf("720.00 drawn, 15 x 12.00 = 180.00 returned: released %s", got.Released)
	}
}

// TestPostgres_AmountAgreementValuesAtReleasePrice: the commitment is valued at
// the agreement price the release was made at, whatever price or discount the
// order line is later edited to, so what is drawn and given back stays in
// step. Release 50 at 12.00 (600.00); a 50 percent discount on the line draws
// nothing; lowering to 40 returns 10 x 12.00 = 120.00; delivering 10 and
// closing the remainder of 30 returns 360.00, leaving exactly the 120.00 of
// the delivered 10.
func TestPostgres_AmountAgreementValuesAtReleasePrice(t *testing.T) {
	s, _, ctx := migrated(t)
	le := ids.New()
	now := time.Date(2027, 3, 10, 9, 0, 0, 0, time.UTC)
	a := amountAgreement(t, s, ctx, le, 1000)
	r, _, err := s.ReleaseAgreement(ctx, a.ID, release(50, "r1"), now)
	if err != nil {
		t.Fatal(err)
	}
	half := d(50)
	disc, err := s.ChangeOrder(ctx, r.Order.ID, r.Order.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, DiscPct: &half}}}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(600)) {
		t.Fatalf("a discount alone draws nothing: released %s", got.Released)
	}
	forty := d(40)
	lowered, err := s.ChangeOrder(ctx, r.Order.ID, disc.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, Quantity: &forty}}}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(480)) {
		t.Fatalf("50 to 40 units returns 120.00 at the release price: released %s", got.Released)
	}
	six, fortyFive := d(6), d(45)
	raised, err := s.ChangeOrder(ctx, r.Order.ID, lowered.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: &six, Quantity: &fortyFive}}}, "rep", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(540)) {
		t.Fatalf("40 to 45 units draws 60.00 at the release price: released %s", got.Released)
	}
	if res, err := s.ConfirmOrder(ctx, r.Order.ID, raised.Version, "rep", now); err != nil || !res.Confirmed {
		t.Fatalf("confirm: %v %+v", err, res.Evaluation)
	}
	deliver(t, s, ctx, r.Order.ID, "PS-1", 1, 10)
	cur, _ := s.GetOrder(ctx, r.Order.ID)
	if _, err := s.CancelLineRemainder(ctx, r.Order.ID, 1, cur.Version, "short", "rep", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetAgreement(ctx, a.ID); !got.Released.Equal(d(120)) {
		t.Fatalf("the delivered 10 units keep 120.00 drawn: released %s", got.Released)
	}
}
