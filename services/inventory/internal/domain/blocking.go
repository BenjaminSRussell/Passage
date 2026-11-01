package domain

import (
	"maps"
	"regexp"
	"slices"

	"github.com/shopspring/decimal"
)

// Blocking problem codes. INV-BLK-001 is from the message catalog (3.16.18);
// the others are service codes for rules the catalog does not name.
const (
	CodeQuarantineDisposition = "INV-BLK-001" // quarantine exit needs a disposition
	CodeBlockedReserve        = "INV-BLK-002" // blocked or quarantined stock cannot be reserved
	CodeBlockExceeds          = "INV-BLK-003" // blocked quantity exceeds the available quantity
	CodeBlockingNotFound      = "INV-BLK-004"
	CodeDispositionLater      = "INV-BLK-005" // return needs the vendor return (wave 18)
	CodeBlockingState         = "INV-BLK-006" // the blocking is no longer active
	CodeBlockingTransition    = "INV-BLK-007" // only quarantined stock moves to blocked
	CodeScrapDuty             = "INV-BLK-008" // scrapping writes stock off and needs inventory.blocking.scrap
)

// Inventory status values stock moves to when blocked (FR-16.8). Stock with no
// inventory_status dimension is unrestricted (AVAILABLE).
const (
	StockAvailable  = "AVAILABLE"
	StockBlocked    = "BLOCKED"
	StockQuarantine = "QUARANTINE"
)

// IsRestricted reports whether an inventory status keeps stock out of
// availability and reservation (FR-16.8).
func IsRestricted(status string) bool { return status == StockBlocked || status == StockQuarantine }

// BlockReasons are the reason codes a blocking must carry (3.16.13 seeded list).
var BlockReasons = []string{"QUALITY", "DAMAGE", "RECALL", "LEGAL", "OTHER"}

// Dispositions that end a blocking.
const (
	DispositionRelease = "RELEASE"
	DispositionScrap   = "SCRAP"
	DispositionReturn  = "RETURN"
)

// Blocking states.
const (
	BlockingActive   = "ACTIVE"
	BlockingReleased = "RELEASED"
)

// Blocking moves quantity of one product from unrestricted stock at Dims into
// a restricted inventory status (BLOCKED or QUARANTINE) at the same dimensions.
type Blocking struct {
	ID            string
	LegalEntityID string
	ProductID     string
	Dims          Dimensions // unrestricted source dimensions
	Qty           decimal.Decimal
	Unit          string // inventory unit the quantity is in, recorded on the legs
	Target        string // StockBlocked or StockQuarantine
	Reason        string
	Note          string
	State         string
	Disposition   string
	PhysicalDate  string
	// ExpectedReleaseDate is when the inspection or recall hold is expected to
	// end (YYYY-MM-DD, empty when unknown); it is information, not a deadline.
	ExpectedReleaseDate string
	// ScrapJournalID is the posted ADJUSTMENT journal that wrote a scrapped
	// blocking off (empty for any other disposition).
	ScrapJournalID string
	Version        int
}

// UnitCode is the shape of an inventory unit code.
var UnitCode = regexp.MustCompile(`^[A-Z0-9_-]{1,12}$`)

// ValidateBlockRequest checks a blocking request without the database: a
// positive quantity within the unit precision, a target status, a reason code,
// source dimensions that name a site and carry no inventory status, and no
// reserved dimension.
func ValidateBlockRequest(b Blocking, maxScale int32) error {
	if b.Qty.Sign() <= 0 || !b.Qty.Equal(b.Qty.Truncate(maxScale)) {
		return fail(CodeInvalid, "blocked quantity must be positive with at most %d decimals", maxScale)
	}
	if !UnitCode.MatchString(b.Unit) {
		return fail(CodeInvalid, "unit %q is not a unit code", b.Unit)
	}
	if !IsRestricted(b.Target) {
		return fail(CodeInvalid, "target status must be BLOCKED or QUARANTINE, got %q", b.Target)
	}
	if !slices.Contains(BlockReasons, b.Reason) {
		return fail(CodeInvalid, "reason %q is not a blocking reason code", b.Reason)
	}
	dims, err := b.Dims.Normalize()
	if err != nil {
		return fail(CodeInvalid, "%v", err)
	}
	if dims[DimStatus] != "" {
		return fail(CodeInvalid, "blocking source dimensions must not carry an inventory status")
	}
	for _, name := range reservedDimensions {
		if dims[name] != "" {
			return fail(CodeInvalid, "dimension %s is reserved and cannot be set yet", name)
		}
	}
	if dims[DimSite] == "" {
		return fail(CodeDimensionMandatory, "dimension %s is mandatory for product %s", DimSite, b.ProductID)
	}
	if dims[DimSerial] != "" && !b.Qty.Equal(decimal.NewFromInt(1)) {
		return fail(CodeSerialQty, "serial-tracked product %s requires quantity 1 per blocking", b.ProductID)
	}
	return nil
}

// CheckBlockable rejects a blocking larger than the unrestricted available
// physical quantity at the source (reserved stock cannot be blocked).
func CheckBlockable(qty, avail decimal.Decimal) error {
	if qty.GreaterThan(avail) {
		return fail(CodeBlockExceeds, "blocked quantity %s exceeds available %s", qty, decimal.Max(avail, decimal.Zero))
	}
	return nil
}

// BlockedDims returns the dimensions of blocked stock: the source dimensions
// with the inventory status set to the target status.
func BlockedDims(src Dimensions, target string) Dimensions {
	out := maps.Clone(src)
	if out == nil {
		out = Dimensions{}
	}
	out[DimStatus] = target
	return out
}

// ResolveRelease checks the disposition that ends an active blocking and
// returns the one to record. RELEASE returns the quantity to unrestricted
// stock; a BLOCKED blocking with no disposition is a RELEASE. A quarantine
// exit must name its disposition (INV-BLK-001). SCRAP writes the stock off
// through a posted ADJUSTMENT journal; RETURN needs the vendor return of
// wave 18 and is refused (INV-BLK-005).
func ResolveRelease(b Blocking, disposition string) (string, error) {
	if b.State != BlockingActive {
		return "", fail(CodeBlockingState, "blocking %s is not active", b.ID)
	}
	switch disposition {
	case "":
		if b.Target == StockQuarantine {
			return "", fail(CodeQuarantineDisposition, "quarantined quantity requires a disposition (release, scrap, return) before exit")
		}
		return DispositionRelease, nil
	case DispositionRelease:
		return DispositionRelease, nil
	case DispositionScrap:
		return DispositionScrap, nil
	case DispositionReturn:
		return "", fail(CodeDispositionLater, "disposition RETURN needs the vendor return and is not yet supported; release the stock and post an adjustment")
	}
	return "", fail(CodeInvalid, "disposition %q is not one of RELEASE, SCRAP, RETURN", disposition)
}

// CheckEscalate checks that an active quarantine can move to blocked: the
// QUARANTINE to BLOCKED transition of the status matrix (FR-16.8). Blocked
// stock has no further transition except its disposition.
func CheckEscalate(b Blocking) error {
	if b.State != BlockingActive {
		return fail(CodeBlockingState, "blocking %s is not active", b.ID)
	}
	if b.Target != StockQuarantine {
		return fail(CodeBlockingTransition, "only quarantined stock moves to blocked; blocking %s is already %s", b.ID, b.Target)
	}
	return nil
}

// CheckExpectedRelease checks an expected release date against the date the
// stock was blocked: it cannot be earlier (both are YYYY-MM-DD).
func CheckExpectedRelease(blockedOn, expected string) error {
	if expected != "" && expected < blockedOn {
		return fail(CodeInvalid, "expected release date %s is before the blocking date %s", expected, blockedOn)
	}
	return nil
}
