package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Reservation problem codes. INV-RES-001 and INV-RES-002 are from the message
// catalog (3.16.18); INV-RES-003 is the service's not-found code.
const (
	CodeReserveShortfall    = "INV-RES-001"
	CodeReserveNoEligible   = "INV-RES-002"
	CodeReservationNotFound = "INV-RES-003"
	// CodeReservedOrderedCover is INV-RES-004: a change would leave more reserved against an
	// expected receipt than the dimension set still expects.
	CodeReservedOrderedCover = "INV-RES-004"
)

// ReservationStatus is the lifecycle of one reservation allocation. CONSUMED
// is written by the issuing documents of later waves (sales, production).
type ReservationStatus string

const (
	ReservationActive   ReservationStatus = "ACTIVE"
	ReservationReleased ReservationStatus = "RELEASED"
	ReservationConsumed ReservationStatus = "CONSUMED"
)

// ReservationStatuses lists every reservation status.
var ReservationStatuses = []ReservationStatus{ReservationActive, ReservationReleased, ReservationConsumed}

// ReservationKind says which bucket a reservation holds: physical stock
// (reserved_physical) or the expected receipt of a dimension set
// (reserved_ordered, FR-16.7).
type ReservationKind string

const (
	ReserveKindPhysical ReservationKind = "PHYSICAL"
	ReserveKindOrdered  ReservationKind = "ORDERED"
)

// DemandType names the demand a reservation serves.
type DemandType string

const (
	DemandManual          DemandType = "MANUAL"
	DemandSalesOrder      DemandType = "SALES_ORDER"
	DemandProductionOrder DemandType = "PRODUCTION_ORDER"
	DemandTransferOrder   DemandType = "TRANSFER_ORDER"
)

// DemandTypes lists every demand type.
var DemandTypes = []DemandType{DemandManual, DemandSalesOrder, DemandProductionOrder, DemandTransferOrder}

// ReservationPolicies lists every reservation policy.
var ReservationPolicies = []ReservationPolicy{PolicyManual, PolicyFIFO, PolicyFEFO, PolicyLIFO}

// Reservation is one allocation of quantity against one dimension set.
type Reservation struct {
	ID            string
	LegalEntityID string
	ProductID     string
	Dims          Dimensions
	Qty           decimal.Decimal
	DemandType    DemandType
	DemandID      string
	// DemandSegment is the segment of the demand ("" when unsegmented, FR-16.20).
	DemandSegment string
	Policy        ReservationPolicy
	Status        ReservationStatus
	// Kind is PHYSICAL (empty reads as PHYSICAL) or ORDERED.
	Kind         ReservationKind
	AllocationNo int
	CreatedAt    time.Time
	ReleasedAt   *time.Time
	// ExpiresAt is when an ACTIVE reservation lapses (nil: it holds until released).
	ExpiresAt *time.Time
	// ConvertedAt is when the reservation moved from the expected receipt to physical stock
	// because the receipt arrived (nil: never converted).
	ConvertedAt *time.Time
}

// ConversionStep is one reservation (or the part of it) a receipt turns physical. Whole is true
// when the step takes the reservation's entire quantity, which then changes kind in place; a
// partial step splits Qty off into a new PHYSICAL allocation and leaves the rest ORDERED.
type ConversionStep struct {
	ReservationID string
	Qty           decimal.Decimal
	Whole         bool
}

// PlanConversion spreads a received quantity over the ACTIVE ORDERED reservations of one dimension
// set, oldest first (the list is in creation order). Only reserved quantity is converted: a receipt
// larger than the reserved total converts all of it, a smaller one stops mid-list.
func PlanConversion(active []Reservation, received decimal.Decimal) []ConversionStep {
	var steps []ConversionStep
	left := received
	for _, r := range active {
		if !left.IsPositive() {
			break
		}
		take := decimal.Min(r.Qty, left)
		if !take.IsPositive() {
			continue
		}
		steps = append(steps, ConversionStep{ReservationID: r.ID, Qty: take, Whole: take.Equal(r.Qty)})
		left = left.Sub(take)
	}
	return steps
}

// OrderedUncovered is how much more is reserved against the expected receipt of a row than it
// still expects (ordered plus arrived); zero when the reservations are covered.
func OrderedUncovered(oh OnHand) decimal.Decimal {
	return decimal.Max(oh.ReservedOrdered.Sub(oh.Ordered.Add(oh.Arrived)), decimal.Zero)
}

// CheckOrderedCover rejects a change (before to after) that leaves more reserved against the
// expected receipt than the dimension set still expects (ordered plus arrived), as when a transfer
// order is cancelled, shortened, deleted or closed short while reservations rely on its receipt.
// A row already over-committed before the change (data from before the guard) is refused only
// when the change makes the gap larger, so a receipt that shrinks it still goes through.
func CheckOrderedCover(before, after OnHand) error {
	gap := OrderedUncovered(after)
	if gap.IsPositive() && gap.GreaterThan(OrderedUncovered(before)) {
		return fail(CodeReservedOrderedCover, "%s is reserved against the expected receipt but only %s would still be expected; release the reservations first",
			after.ReservedOrdered, decimal.Max(after.Ordered.Add(after.Arrived), decimal.Zero))
	}
	return nil
}

// ValidateReservable rejects a reservation at a blocked or quarantined
// dimension set (FR-16.8: restricted stock is excluded from reservation) and
// at vendor-owned stock, which the company may consume only after an ownership
// change (FR-16.17, BR-16.14).
func ValidateReservable(dims Dimensions) error {
	if s := dims[DimStatus]; IsRestricted(s) {
		return fail(CodeBlockedReserve, "stock with inventory status %s cannot be reserved", s)
	}
	if VendorOwned(dims) {
		return fail(CodeOwnershipValue, "vendor-owned stock cannot be reserved; post an ownership change first")
	}
	return nil
}

// CheckReservable rejects a manual reservation larger than the available
// physical quantity (over-reservation, FR-16.7) and non-positive quantities.
func CheckReservable(qty, avail decimal.Decimal) error {
	if qty.Sign() <= 0 {
		return fail(CodeInvalid, "reservation quantity must be positive")
	}
	if qty.GreaterThan(avail) {
		return fail(CodeReserveShortfall, "only %s available to reserve, %s requested", decimal.Max(avail, decimal.Zero), qty)
	}
	return nil
}

// ExpectedReceipt is the supply of a dimension set a reservation against
// expected receipts can still claim: ordered plus arrived, less what is already
// reserved against it.
func (o OnHand) ExpectedReceipt() decimal.Decimal {
	return o.Ordered.Add(o.Arrived).Sub(o.ReservedOrdered)
}

// CheckReservableOrdered rejects a reservation against expected receipts larger
// than the unreserved ordered supply of the dimension set, or one that
// availability (which counts the open demand ON_ORDER) cannot cover.
func CheckReservableOrdered(qty decimal.Decimal, oh OnHand) error {
	if qty.Sign() <= 0 {
		return fail(CodeInvalid, "reservation quantity must be positive")
	}
	room := decimal.Min(oh.ExpectedReceipt(), oh.AvailOrdered())
	if qty.GreaterThan(room) {
		return fail(CodeReserveShortfall, "only %s expected to arrive and unreserved, %s requested", decimal.Max(room, decimal.Zero), qty)
	}
	return nil
}

// PlanPolicy allocates a policy reservation over the candidate batches. With
// allowPartial the shortfall is returned to the caller; without it any
// shortfall is INV-RES-001 and nothing should be written. A FEFO request with
// no eligible batch at all is INV-RES-002.
func PlanPolicy(policy ReservationPolicy, batches []BatchStock, qty decimal.Decimal, requiredDate time.Time, marginDays int, allowPartial bool) (Selection, error) {
	return PlanDemandPolicy(policy, batches, qty, requiredDate, marginDays, allowPartial, "", false)
}

// PlanDemandPolicy is PlanPolicy for a demand in segment demandSegment ("" for
// unsegmented demand; FR-16.20, BR-16.17). Stock of the demand's own segment is
// taken first, in policy order; unsegmented stock fills the remainder only when
// fallback is set. Stock of any other segment is never selected, so an
// unsegmented demand never takes segregated stock.
func PlanDemandPolicy(policy ReservationPolicy, batches []BatchStock, qty decimal.Decimal, requiredDate time.Time, marginDays int,
	allowPartial bool, demandSegment string, fallback bool) (Selection, error) {
	if qty.Sign() <= 0 {
		return Selection{}, fail(CodeInvalid, "reservation quantity must be positive")
	}
	if policy == PolicyManual {
		return Selection{}, fail(CodeInvalid, "a MANUAL reservation names one dimension set")
	}
	if marginDays < 0 {
		return Selection{}, fail(CodeInvalid, "shelf life margin must not be negative")
	}
	sel := SelectBatches(policy, inSegment(batches, demandSegment), qty, requiredDate, marginDays)
	if sel.Shortfall.Sign() > 0 && demandSegment != "" && fallback {
		rest := SelectBatches(policy, inSegment(batches, ""), sel.Shortfall, requiredDate, marginDays)
		sel.Allocations = append(sel.Allocations, rest.Allocations...)
		sel.Shortfall = rest.Shortfall
	}
	if len(sel.Allocations) == 0 {
		if policy == PolicyFEFO {
			return sel, fail(CodeReserveNoEligible, "no eligible batch found for FEFO reservation (expiry margin %d days)", marginDays)
		}
		return sel, fail(CodeReserveShortfall, "only 0 available to reserve, %s requested", qty)
	}
	if sel.Shortfall.Sign() > 0 && !allowPartial {
		return sel, &Error{Code: CodeReserveShortfall, Detail: fmt.Sprintf("only %s available to reserve, %s requested", qty.Sub(sel.Shortfall), qty)}
	}
	return sel, nil
}

// inSegment keeps the candidates whose segment is exactly segment.
func inSegment(batches []BatchStock, segment string) []BatchStock {
	out := make([]BatchStock, 0, len(batches))
	for _, b := range batches {
		if b.Segment == segment {
			out = append(out, b)
		}
	}
	return out
}

// Reasons a candidate is listed but cannot be reserved by a policy request.
const (
	CandidateRestricted = "RESTRICTED"     // BLOCKED or QUARANTINE (INV-BLK-002)
	CandidateExpires    = "EXPIRES_BEFORE" // FEFO: expires before the required date plus the margin (BR-16.7)
	CandidateSegment    = "OTHER_SEGMENT"  // stock of a segment the demand may not take (BR-16.17)
)

// Candidate is one dimension set of a product with available quantity, as a
// reservation dialog lists it (FR-16.7). Eligible says whether a policy
// request would take it; Reason says why not.
type Candidate struct {
	BatchStock
	Eligible bool
	Reason   string
}

// RankCandidates lists the stock with a positive available quantity in the
// pick order of policy, eligible stock first in that order and ineligible stock
// after it, applying the same rules as a reservation would: restricted status,
// the demand segment (BR-16.17: stock of the demand's segment, and unsegmented
// stock only when fallback is set; a MANUAL reservation checks it too) and,
// for FEFO only, the expiry cut-off. Like PlanDemandPolicy, a segmented demand
// takes its own segment first and the unsegmented fallback after it, so those
// come in that order. MANUAL ranks like FIFO.
func RankCandidates(policy ReservationPolicy, stock []BatchStock, requiredDate time.Time, marginDays int, demandSegment string, fallback bool) []Candidate {
	cutoff := requiredDate.AddDate(0, 0, marginDays)
	var ok, rest, no []BatchStock
	reasons := map[string]string{}
	for _, b := range stock {
		if b.Available.Sign() <= 0 {
			continue
		}
		switch {
		case b.Status != StockAvailable:
			reasons[b.Key] = CandidateRestricted
		case !SegmentEligible(b.Segment, demandSegment, fallback):
			reasons[b.Key] = CandidateSegment
		case policy == PolicyFEFO && b.Expiry != nil && b.Expiry.Before(cutoff):
			reasons[b.Key] = CandidateExpires
		case b.Segment != demandSegment:
			rest = append(rest, b) // unsegmented fallback stock, taken after the demand's own segment
			continue
		default:
			ok = append(ok, b)
			continue
		}
		no = append(no, b)
	}
	sortByPolicy(policy, ok)
	sortByPolicy(policy, rest)
	sortByPolicy(policy, no)
	ok = append(ok, rest...)
	out := make([]Candidate, 0, len(ok)+len(no))
	for _, b := range ok {
		out = append(out, Candidate{BatchStock: b, Eligible: true})
	}
	for _, b := range no {
		out = append(out, Candidate{BatchStock: b, Reason: reasons[b.Key]})
	}
	return out
}

// IsExpectedSupply reports whether a status is expected receipt (ordered or arrived, not yet
// stock): the supply a reservation against expected receipts claims.
func IsExpectedSupply(s Status) bool { return s == StatusOrdered || s == StatusArrived }
