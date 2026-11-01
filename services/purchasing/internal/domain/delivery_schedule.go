package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Delivery schedule problem codes (FR-17.10, listed in the service README).
const (
	// CodeScheduleSum is PRC-DS-001: the rows do not add up to the line quantity.
	CodeScheduleSum = "PRC-DS-001"
	// CodeScheduleTarget is PRC-DS-002: the PO or line cannot take this schedule change.
	CodeScheduleTarget = "PRC-DS-002"
	// CodeScheduleBudget is PRC-DS-003: the PO's schedules together would hold more than
	// MaxScheduleRowsPerPO rows.
	CodeScheduleBudget = "PRC-DS-003"
)

// MaxScheduleRows bounds the rows of one line's delivery schedule.
const MaxScheduleRows = 100

// MaxScheduleRowsPerPO bounds the schedule rows of all lines of one PO. po.confirmed.v1 carries
// every row, and the broker takes messages up to 512 KiB: 500 lines plus 1,000 rows (about 100
// bytes each) stay well inside it, 500 lines × 100 rows would not.
const MaxScheduleRowsPerPO = 1000

// qtyScale is the decimal places a quantity carries (numeric(20,6)).
const qtyScale = 6

// ScheduleRow is one delivery of a PO line: Qty due on DeliveryDate, and the
// date the vendor confirmed, when it has.
type ScheduleRow struct {
	Seq           int             `json:"seq"`
	Qty           decimal.Decimal `json:"qty"`
	DeliveryDate  Day             `json:"deliveryDate"`
	ConfirmedDate *Day            `json:"confirmedDate,omitempty"`
	ConfirmedBy   string          `json:"confirmedBy,omitempty"`
	ConfirmedAt   *time.Time      `json:"confirmedAt,omitempty"`
}

// DeliverySchedule is the schedule of one PO line. Σ Rows.Qty = LineQty
// (FR-17.10); each row later produces its own ORDERED inventory transaction.
type DeliverySchedule struct {
	PurchaseOrderID string          `json:"purchaseOrderId"`
	LineNo          int             `json:"lineNo"`
	Unit            string          `json:"unit"`
	LineQty         decimal.Decimal `json:"lineQty"`
	Rows            []ScheduleRow   `json:"rows"`
}

// ScheduleInput is a requested row.
type ScheduleInput struct {
	Qty          decimal.Decimal
	DeliveryDate Day
}

// CheckScheduleTarget refuses a schedule rewrite unless the PO is still being
// prepared (DRAFT or REJECTED) and the line is live. A confirmed PO changes
// quantities and dates through change management (FR-17.8), not here.
func CheckScheduleTarget(poStatus, lineStatus string) error {
	if poStatus != POStatusDraft && poStatus != POStatusRejected {
		return fail(CodeScheduleTarget, "the delivery schedule of a purchase order in %s cannot be replaced; only a DRAFT or REJECTED order can", poStatus)
	}
	if lineStatus == POLineCancelled {
		return fail(CodeScheduleTarget, "a cancelled line cannot take a delivery schedule")
	}
	return nil
}

// BuildSchedule validates the requested rows against the line quantity and
// numbers them in delivery date order (rows on the same date keep their
// request order). The rows must be 1 to MaxScheduleRows, each with a positive
// quantity, and add up to lineQty exactly (PRC-DS-001).
func BuildSchedule(in []ScheduleInput, lineQty decimal.Decimal) ([]ScheduleRow, error) {
	if len(in) == 0 || len(in) > MaxScheduleRows {
		return nil, fail(CodeValidation, "a delivery schedule holds from 1 to %d rows", MaxScheduleRows)
	}
	sum := decimal.Zero
	for i, r := range in {
		if !r.Qty.IsPositive() {
			return nil, fail(CodeValidation, "schedule row %d: qty must be positive", i+1)
		}
		if !r.Qty.Equal(r.Qty.Truncate(qtyScale)) {
			return nil, fail(CodeValidation, "schedule row %d: qty has more than %d decimal places", i+1, qtyScale)
		}
		sum = sum.Add(r.Qty)
	}
	if !sum.Equal(lineQty) {
		return nil, fail(CodeScheduleSum, "delivery schedule quantities add up to %s, not the line quantity %s", sum.String(), lineQty.String())
	}
	rows := make([]ScheduleRow, len(in))
	for i, r := range in {
		rows[i] = ScheduleRow{Qty: r.Qty, DeliveryDate: r.DeliveryDate}
	}
	sort.SliceStable(rows, func(a, b int) bool { return rows[a].DeliveryDate.Before(rows[b].DeliveryDate.Time) })
	for i := range rows {
		rows[i].Seq = i + 1
	}
	return rows, nil
}

// CheckScheduleBudget is PRC-DS-003 when otherRows (the rows of the PO's other
// lines) plus rows exceed MaxScheduleRowsPerPO.
func CheckScheduleBudget(otherRows, rows int) error {
	if otherRows+rows > MaxScheduleRowsPerPO {
		return fail(CodeScheduleBudget, "the delivery schedules of one purchase order hold at most %d rows; the other lines already have %d", MaxScheduleRowsPerPO, otherRows)
	}
	return nil
}

// CarryConfirmations keeps the vendor's confirmed date on each new row that
// repeats an old row (same delivery date and quantity), so replacing the
// schedule of a rejected change does not drop what the vendor already
// confirmed. Each old row is carried to at most one new row.
func CarryConfirmations(old, rows []ScheduleRow) []ScheduleRow {
	used := make([]bool, len(old))
	for i := range rows {
		for j, o := range old {
			if used[j] || o.ConfirmedDate == nil || !o.DeliveryDate.Equal(rows[i].DeliveryDate.Time) || !o.Qty.Equal(rows[i].Qty) {
				continue
			}
			used[j] = true
			rows[i].ConfirmedDate, rows[i].ConfirmedBy, rows[i].ConfirmedAt = o.ConfirmedDate, o.ConfirmedBy, o.ConfirmedAt
			break
		}
	}
	return rows
}

// AdjustToQty brings a schedule in step with a changed line quantity: more
// quantity goes to the last row, less is taken from the last rows backwards and
// a row that reaches zero is dropped. Rows keep their dates; a row whose
// quantity changes loses the vendor's confirmed date (the vendor confirmed a
// different quantity and confirms again), the others keep theirs.
// Σ Qty = newQty afterwards; newQty must be positive.
func AdjustToQty(rows []ScheduleRow, newQty decimal.Decimal) ([]ScheduleRow, error) {
	if !newQty.IsPositive() {
		return nil, fail(CodeValidation, "line quantity must be positive")
	}
	if len(rows) == 0 {
		return rows, nil
	}
	out := append([]ScheduleRow(nil), rows...)
	sum := decimal.Zero
	for _, r := range out {
		sum = sum.Add(r.Qty)
	}
	diff := newQty.Sub(sum)
	switch {
	case diff.IsPositive():
		last := &out[len(out)-1]
		last.Qty = last.Qty.Add(diff)
		clearConfirmation(last)
	case diff.IsNegative():
		cut := diff.Neg()
		for len(out) > 0 && cut.IsPositive() {
			last := &out[len(out)-1]
			if last.Qty.GreaterThan(cut) {
				last.Qty = last.Qty.Sub(cut)
				clearConfirmation(last)
				cut = decimal.Zero
				break
			}
			cut = cut.Sub(last.Qty)
			out = out[:len(out)-1]
		}
	}
	return out, nil
}

func clearConfirmation(r *ScheduleRow) {
	r.ConfirmedDate, r.ConfirmedBy, r.ConfirmedAt = nil, "", nil
}

// CheckScheduleBalanced is PRC-DS-001 when a schedule does not add up to the line quantity.
func CheckScheduleBalanced(rows []ScheduleRow, lineQty decimal.Decimal) error {
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.Qty)
	}
	if !sum.Equal(lineQty) {
		return fail(CodeScheduleSum, "delivery schedule quantities add up to %s, not the line quantity %s", sum.String(), lineQty.String())
	}
	return nil
}

// CheckConfirmTarget refuses a confirmed date on a PO that is not CONFIRMED:
// the vendor confirms dates of an order it has been sent.
func CheckConfirmTarget(poStatus, lineStatus string) error {
	if poStatus != POStatusConfirmed {
		return fail(CodeScheduleTarget, "a delivery date is confirmed on a CONFIRMED purchase order, not one in %s", poStatus)
	}
	if lineStatus == POLineCancelled {
		return fail(CodeScheduleTarget, "a cancelled line cannot take a confirmed delivery date")
	}
	return nil
}
