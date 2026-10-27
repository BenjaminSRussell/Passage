package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
)

func earmark(t *testing.T, ctx context.Context, st *Postgres, ruleID string, typ domain.ReservationType, expiry, amount string) Reservation {
	t.Helper()
	_, raw, err := st.CreateReservation(ctx, ids.New(), "h", ReservationRequest{RuleVersionID: ruleID, Type: typ, ExpiryDate: expiry,
		Lines: []ReservationLineInput{{DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d(amount)}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	var r Reservation
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func reservationsColumn(t *testing.T, ctx context.Context, st *Postgres, ruleID string) string {
	t.Helper()
	rows, err := st.Available(ctx, ruleID, "dept-10", "FY2026")
	if err != nil || len(rows) != 1 {
		t.Fatalf("available: %v (%d rows)", err, len(rows))
	}
	return rows[0].Reservations.StringFixed(2)
}

// assertReservationInvariant: the balance's reservations column equals Σ open
// amounts of the fund-holding reservation lines on it.
func assertReservationInvariant(t *testing.T, ctx context.Context, st *Postgres) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select b.control_dimension_key, b.reservations::text,
			coalesce(sum(l.amount - l.consumed - l.released) filter (where r.reservation_type <> 'RevenueForecast'), 0)::text
			from budget_control_balance b
			left join budget_reservation_line l on l.tenant_id = b.tenant_id and l.balance_id = b.id
			left join budget_reservation r on r.tenant_id = l.tenant_id and r.id = l.reservation_id
			group by b.tenant_id, b.id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var dim, col, open string
			if err := rows.Scan(&dim, &col, &open); err != nil {
				return err
			}
			if !d(col).Equal(d(open)) {
				t.Errorf("balance %s: reservations %s, open reservation lines %s", dim, col, open)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AC-38.12: a funds reservation of 20,000 consumed by a PO of 8,000 shows 12,000
// remaining and an 8,000 encumbrance, and available funds do not move.
func TestReservation_ConsumeConvertsWithoutMovingAvailable(t *testing.T) {
	ctx, st, _, rule := setup(t)
	r := earmark(t, ctx, st, rule.ID, domain.FundsReservation, "2099-12-31", "20000")
	if r.Number != "FR-000001" || r.Status != domain.ReservationOpen || r.Check == nil || r.Check.DocumentType != "Reservation" {
		t.Fatalf("created %+v", r)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("80000")) {
		t.Fatalf("available after reservation = %s, want 80000", got)
	}
	poLine := ids.New()
	_, raw, err := st.ConsumeReservation(ctx, ids.New(), "h", ConsumeRequest{ReservationID: r.ID, LineID: r.Lines[0].LineID,
		DocumentType: "PurchaseOrder", DocumentID: ids.New(), DocumentLineID: poLine, Amount: d("8000")})
	if err != nil {
		t.Fatal(err)
	}
	var resp ConsumeResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Open.Equal(d("12000")) || resp.Status != domain.ReservationPartiallyConsumed {
		t.Fatalf("consume = %+v, want open 12000 PartiallyConsumed", resp)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("80000")) {
		t.Fatalf("available after conversion = %s, want 80000", got)
	}
	enc, err := st.GetEncumbrance(ctx, lookupEncumbrance(t, ctx, st, poLine))
	if err != nil || !enc.Reserved.Equal(d("8000")) || enc.ID != resp.EncumbranceID {
		t.Fatalf("encumbrance = %+v (%v), want 8000 reserved", enc, err)
	}
	got, err := st.GetReservation(ctx, r.ID)
	if err != nil || len(got.Consumptions) != 1 || !got.Lines[0].Open.Equal(d("12000")) {
		t.Fatalf("reservation = %+v (%v)", got, err)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
	assertReservationInvariant(t, ctx, st)

	// Over-consumption is refused and changes nothing.
	_, _, err = st.ConsumeReservation(ctx, ids.New(), "h", ConsumeRequest{ReservationID: r.ID, LineID: r.Lines[0].LineID,
		DocumentType: "PurchaseOrder", DocumentID: ids.New(), DocumentLineID: ids.New(), Amount: d("12000.01")})
	if !hasCode(err, domain.CodeConsumptionExceeds) {
		t.Fatalf("over-consumption: %v, want %s", err, domain.CodeConsumptionExceeds)
	}

	// Close releases the 12,000 residual back to available.
	_, raw, err = st.CloseReservation(ctx, ids.New(), "h", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rel ReleaseResult
	if err := json.Unmarshal(raw, &rel); err != nil {
		t.Fatal(err)
	}
	if !rel.Released.Equal(d("12000")) || rel.Status != domain.ReservationClosed {
		t.Fatalf("close = %+v, want released 12000 Closed", rel)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("92000")) {
		t.Fatalf("available after close = %s, want 92000", got)
	}
	if _, _, err := st.CloseReservation(ctx, ids.New(), "h", r.ID); !hasCode(err, domain.CodeReservationNotOpen) {
		t.Fatalf("second close: %v, want %s", err, domain.CodeReservationNotOpen)
	}
	assertReservationInvariant(t, ctx, st)
}

// FR-38.20: the creation check blocks over-budget documents; nothing is held.
func TestReservation_CreationCheckBlocks(t *testing.T) {
	ctx, st, _, rule := setup(t)
	_, _, err := st.CreateReservation(ctx, ids.New(), "h", ReservationRequest{RuleVersionID: rule.ID, Type: domain.FundsReservation,
		Lines: []ReservationLineInput{{DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d("60000")}, {DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d("60000")}}}, false)
	if !hasCode(err, CodeCheckFailed) {
		t.Fatalf("two lines of 60,000 on 100,000: %v, want %s", err, CodeCheckFailed)
	}
	if rows, err := st.ListReservations(ctx, ReservationFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("failed creation left %d documents (%v)", len(rows), err)
	}
	if c := reservationsColumn(t, ctx, st, rule.ID); c != "0.00" {
		t.Fatalf("reservations = %s after a failed creation, want 0", c)
	}
}

// BR-38.14: FundsBlocking cannot be consumed (CO-0631); a RevenueForecast holds
// nothing; per-type numbering.
func TestReservation_TypeRules(t *testing.T) {
	ctx, st, _, rule := setup(t)
	fb := earmark(t, ctx, st, rule.ID, domain.FundsBlocking, "", "5000")
	_, _, err := st.ConsumeReservation(ctx, ids.New(), "h", ConsumeRequest{ReservationID: fb.ID, LineID: fb.Lines[0].LineID,
		DocumentType: "PurchaseOrder", DocumentID: ids.New(), DocumentLineID: ids.New(), Amount: d("1")})
	if !hasCode(err, domain.CodeFundsBlockingConsumed) {
		t.Fatalf("consume funds blocking: %v, want CO-0631", err)
	}
	rf := earmark(t, ctx, st, rule.ID, domain.RevenueForecast, "", "7000")
	if fb.Number != "FB-000001" || rf.Number != "RF-000001" || rf.Check != nil {
		t.Fatalf("numbers %s %s, revenue forecast check %+v", fb.Number, rf.Number, rf.Check)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("95000")) {
		t.Fatalf("available = %s, want 95000 (blocking holds, forecast does not)", got)
	}
	assertReservationInvariant(t, ctx, st)
}

// BR-38.14: the expiry run releases residuals of due documents once; FundsBlocking
// and undue documents stay open.
func TestReservation_ExpiryReleasesResidual(t *testing.T) {
	ctx, st, _, rule := setup(t)
	due := earmark(t, ctx, st, rule.ID, domain.Precommitment, "2026-09-30", "3000")
	later := earmark(t, ctx, st, rule.ID, domain.FundsReservation, "2026-12-31", "2000")
	earmark(t, ctx, st, rule.ID, domain.FundsBlocking, "", "1000")
	_, raw, err := st.ExpireReservations(ctx, ids.New(), "h", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	var resp ExpireResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Expired) != 1 || resp.Expired[0].ReservationID != due.ID || !resp.Expired[0].Released.Equal(d("3000")) {
		t.Fatalf("expired = %+v, want only %s with 3000 released", resp.Expired, due.Number)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("97000")) {
		t.Fatalf("available = %s, want 97000", got)
	}
	_, raw, err = st.ExpireReservations(ctx, ids.New(), "h", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.Expired) != 0 {
		t.Fatalf("rerun expired %d again (%v)", len(resp.Expired), err)
	}
	if r, err := st.GetReservation(ctx, later.ID); err != nil || r.Status != domain.ReservationOpen {
		t.Fatalf("undue reservation = %s (%v)", r.Status, err)
	}
	open, err := st.ListReservations(ctx, ReservationFilter{Status: "Expired"})
	if err != nil || len(open) != 1 {
		t.Fatalf("expired filter = %d (%v)", len(open), err)
	}
	assertReservationInvariant(t, ctx, st)
}
