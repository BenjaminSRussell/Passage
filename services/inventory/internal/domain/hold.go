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
