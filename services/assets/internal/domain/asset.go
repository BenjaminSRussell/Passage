package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// AssetStatus is the lifecycle of an asset and of one of its books:
// NotYetAcquired -> Open -> Sold | Scrapped | Transferred (BR-37.2).
type AssetStatus string

const (
	StatusNotYetAcquired AssetStatus = "NotYetAcquired"
	StatusOpen           AssetStatus = "Open"
	StatusSold           AssetStatus = "Sold"
	StatusScrapped       AssetStatus = "Scrapped"
	StatusTransferred    AssetStatus = "Transferred"
)

// DisposalKind is how an asset leaves the register.
type DisposalKind string

const (
	DisposalSale  DisposalKind = "sale"
	DisposalScrap DisposalKind = "scrap"
)

// AcquireStatus returns Open when an acquisition is posted to an asset that has
// not been acquired yet; any other current state is refused.
func AcquireStatus(current AssetStatus) (AssetStatus, error) {
	if current == StatusNotYetAcquired || current == StatusOpen {
		return StatusOpen, nil
	}
	return "", fail(CodeAssetState, "asset is "+string(current)+"; acquisitions need a NotYetAcquired or Open asset")
}

// AfterDisposal returns the status after a disposal of percent (0,100]. Only an
// Open asset can be disposed of. A partial disposal keeps the asset Open.
func AfterDisposal(current AssetStatus, percent decimal.Decimal, kind DisposalKind) (AssetStatus, error) {
	if current != StatusOpen {
		return "", fail(CodeAssetState, "asset is "+string(current)+"; only an Open asset can be disposed of")
	}
	if percent.LessThan(decimal.RequireFromString("100")) {
		return StatusOpen, nil
	}
	if kind == DisposalScrap {
		return StatusScrapped, nil
	}
	return StatusSold, nil
}

// Disposal is the arithmetic of disposing of percent of an asset book (FR-37.8).
type Disposal struct {
	DisposedCost         decimal.Decimal
	DisposedAccumulated  decimal.Decimal
	RemainingCost        decimal.Decimal
	RemainingAccumulated decimal.Decimal
	NBVDisposed          decimal.Decimal
	GainLoss             decimal.Decimal
}

var hundred = decimal.NewFromInt(100)

// DisposedShare is percent of amount rounded half-up to the currency minor
// unit: the part of a book balance (cost, accumulated depreciation or the
// impairment within it) that leaves with a disposal; the residual stays on
// the remaining asset (BR-37.7).
func DisposedShare(amount, percent decimal.Decimal, currency string) decimal.Decimal {
	return amount.Mul(percent).Div(hundred).Round(money.MinorUnits(currency))
}

// Dispose splits cost and accumulated depreciation by percent, rounding the
// disposed share once; residual cents stay on the remaining asset (BR-37.7).
// Gain or loss is proceeds minus the NBV of the disposed share.
func Dispose(cost, accumulated, proceeds, percent decimal.Decimal, currency string) (Disposal, error) {
	if percent.LessThanOrEqual(decimal.Zero) || percent.GreaterThan(hundred) {
		return Disposal{}, fail(CodeInvalidDisposal, "disposal percentage must be above 0 and at most 100")
	}
	if proceeds.IsNegative() || accumulated.IsNegative() || accumulated.GreaterThan(cost) {
		return Disposal{}, fail(CodeInvalidDisposal, "proceeds must be zero or more and accumulated depreciation within cost")
	}
	if len(currency) != 3 {
		return Disposal{}, fail(CodeInvalidDisposal, "currency must be an ISO 4217 code")
	}
	dc := DisposedShare(cost, percent, currency)
	da := DisposedShare(accumulated, percent, currency)
	nbv := dc.Sub(da)
	return Disposal{
		DisposedCost:         dc,
		DisposedAccumulated:  da,
		RemainingCost:        cost.Sub(dc),
		RemainingAccumulated: accumulated.Sub(da),
		NBVDisposed:          nbv,
		GainLoss:             proceeds.Sub(nbv),
	}, nil
}
