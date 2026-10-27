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
	Policy        ReservationPolicy
	Status        ReservationStatus
	AllocationNo  int
	CreatedAt     time.Time
	ReleasedAt    *time.Time
}

// ValidateReservable rejects a manual reservation at a blocked or quarantined
// dimension set (FR-16.8: restricted stock is excluded from reservation).
func ValidateReservable(dims Dimensions) error {
	if s := dims[DimStatus]; IsRestricted(s) {
		return fail(CodeBlockedReserve, "stock with inventory status %s cannot be reserved", s)
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

// PlanPolicy allocates a policy reservation over the candidate batches. With
// allowPartial the shortfall is returned to the caller; without it any
// shortfall is INV-RES-001 and nothing should be written. A FEFO request with
// no eligible batch at all is INV-RES-002.
func PlanPolicy(policy ReservationPolicy, batches []BatchStock, qty decimal.Decimal, requiredDate time.Time, marginDays int, allowPartial bool) (Selection, error) {
	if qty.Sign() <= 0 {
		return Selection{}, fail(CodeInvalid, "reservation quantity must be positive")
	}
	if policy == PolicyManual {
		return Selection{}, fail(CodeInvalid, "a MANUAL reservation names one dimension set")
	}
	if marginDays < 0 {
		return Selection{}, fail(CodeInvalid, "shelf life margin must not be negative")
	}
	sel := SelectBatches(policy, batches, qty, requiredDate, marginDays)
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
