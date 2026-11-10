package domain

import (
	"regexp"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

const (
	CodeHoldExceeds    = "INV-HLD-001"
	CodeHoldNotFound   = "INV-HLD-002"
	CodeHoldState      = "INV-HLD-003"
	CodeHoldReleaseQty = "INV-HLD-004"
)

const (
	HoldActive   = "ACTIVE"
	HoldReleased = "RELEASED"
)

var HoldSourceType = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

const maxHoldSourceRef = 60

type Hold struct {
	ID            string
	LegalEntityID string
	ProductID     string
	Dims          Dimensions
	Qty           decimal.Decimal
	ReleasedQty   decimal.Decimal
	WholeLot      bool
	Reason        string
	SourceType    string
	SourceRef     string
	Note          string
	State         string
	AllocationNo  int
	Version       int
	CreatedAt     time.Time
	ReleasedAt    *time.Time
}

func (h Hold) Held() decimal.Decimal { return h.Qty.Sub(h.ReleasedQty) }

type HoldRequest struct {
	ProductID  string
	Dims       Dimensions
	Qty        decimal.Decimal
	WholeLot   bool
	Reason     string
	SourceType string
	SourceRef  string
}

func ValidateHoldRequest(r HoldRequest, maxScale int32) error {
	dims, err := r.Dims.Normalize()
	if err != nil {
		return fail(CodeInvalid, "%v", err)
	}
	if !slices.Contains(BlockReasons, r.Reason) {
		return fail(CodeInvalid, "reason %q is not a hold reason code", r.Reason)
	}
	if !HoldSourceType.MatchString(r.SourceType) {
		return fail(CodeInvalid, "source type %q must be 2 to 32 characters of A-Z, 0-9 or _", r.SourceType)
	}
	if n := len(r.SourceRef); n < 1 || n > maxHoldSourceRef {
		return fail(CodeInvalid, "source reference must be 1 to %d characters", maxHoldSourceRef)
	}
	if err := ValidateReservable(dims); err != nil {
		return err
	}
	for _, name := range reservedDimensions {
		if dims[name] != "" {
			return fail(CodeInvalid, "dimension %s is reserved and cannot be set yet", name)
		}
	}
	if r.WholeLot {
		if dims[DimBatch] == "" {
			return fail(CodeInvalid, "a whole-lot hold names the lot in the batch dimension")
		}
		return nil
	}
	if r.Qty.Sign() <= 0 || !r.Qty.Equal(r.Qty.Truncate(maxScale)) {
		return fail(CodeInvalid, "held quantity must be positive with at most %d decimals", maxScale)
	}
	if dims[DimSite] == "" {
		return fail(CodeDimensionMandatory, "dimension %s is mandatory for product %s", DimSite, r.ProductID)
	}
	return nil
}

func PlanHold(qty, avail decimal.Decimal, allowPartial bool) (held, shortfall decimal.Decimal, err error) {
	if avail.Sign() <= 0 {
		return decimal.Zero, qty, fail(CodeHoldExceeds, "nothing available to hold, %s requested", qty)
	}
	if qty.GreaterThan(avail) {
		if !allowPartial {
			return decimal.Zero, qty, fail(CodeHoldExceeds, "only %s available to hold, %s requested", avail, qty)
		}
		return avail, qty.Sub(avail), nil
	}
	return qty, decimal.Zero, nil
}

func CheckHoldRelease(h Hold, qty decimal.Decimal) error {
	if h.State != HoldActive {
		return fail(CodeHoldState, "hold %s is not active", h.ID)
	}
	if qty.Sign() <= 0 || qty.GreaterThan(h.Held()) {
		return fail(CodeHoldReleaseQty, "release quantity must be between 0 and the held %s", h.Held())
	}
	return nil
}

// IssueKinds are the movements a service claims. IssueBooked is not one of
// them: it marks the part of an inventory issue that settles the claims of
// its ref, and only inventory writes it.
var IssueKinds = []string{"PICK", "SHIP", "CONSUME"}

const IssueBooked = "ISSUED"

// MaxIssueRef bounds the document line a claim names.
const MaxIssueRef = 255

// CheckIssueClaim refuses a claim that would reach held stock. physical and
// held are the scope's unrestricted stock, outstanding the claimed quantity
// not yet booked as an issue with the claim counted (claims of one ref counted
// once). With any quantity held, free stock (physical - held - outstanding)
// may not be below zero after the claim, even when the claim takes nothing new:
// the ship of units a pick of the same ref staged is refused while a hold
// placed after the pick covers them, until the usage decision. Without a hold
// the stock is not this rule's to police.
func CheckIssueClaim(physical, held, outstanding decimal.Decimal) error {
	if !held.IsPositive() || !physical.Sub(held).Sub(outstanding).IsNegative() {
		return nil
	}
	return issueHeld(physical, held, outstanding)
}

// CheckIssueRoom is the rule for inventory's own issues, which book movements
// rather than ask for them: with freeBefore what was free before the issue and
// the other figures after it (the claims it settled no longer outstanding),
// free stock may not go below zero, and an issue that finds it below zero
// already (a hold placed over claimed stock) may not make it worse. The
// booking of a delivery its claims already let out passes.
func CheckIssueRoom(freeBefore, physical, held, outstanding decimal.Decimal) error {
	free := physical.Sub(held).Sub(outstanding)
	if !held.IsPositive() || !free.IsNegative() || !free.LessThan(freeBefore) {
		return nil
	}
	return issueHeld(physical, held, outstanding)
}

func issueHeld(physical, held, outstanding decimal.Decimal) error {
	return &Error{Code: CodeQualityHold, Detail: "the quantity is still on hold: " + held.String() + " held of " + physical.String() +
		" on hand, " + outstanding.String() + " claimed and not yet issued"}
}
