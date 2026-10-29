package domain

import (
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Codes for transfer, split and group assets. AST-0507 and AST-0535 come from
// the PRD message catalog (7.37.18); AST-0030, AST-0031 and AST-0035 are
// slice-local.
const (
	// CodeSplitPercentage: the shares do not total 100.00% (FR-37.7).
	CodeSplitPercentage = "AST-0507"
	// CodeGroupDuplicate: the book already belongs to a group asset (BR-37.11).
	CodeGroupDuplicate = "AST-0535"
	// CodeInvalidTransfer: a transfer or split request with the wrong shape.
	CodeInvalidTransfer = "AST-0030"
	// CodeInvalidGroup: a member that does not fit the group asset.
	CodeInvalidGroup = "AST-0031"
	// CodeDepreciatedAtGroup: a member book takes no own charge (FR-37.16).
	CodeDepreciatedAtGroup = "AST-0035"
)

// MaxTransferParts bounds the targets of one split.
const MaxTransferParts = 50

// TransferKind is a move of the whole asset to one new asset (transfer, for
// example to another legal entity) or its partition into two or more new
// assets (split), FR-37.7.
type TransferKind string

const (
	TransferKindTransfer TransferKind = "transfer"
	TransferKindSplit    TransferKind = "split"
)

// TransactionType is the asset_transaction type the kind writes on the source
// and on each target book.
func (k TransferKind) TransactionType() string {
	if k == TransferKindSplit {
		return "Split"
	}
	return "Transfer"
}

// ValidateTransfer checks the shape of a transfer or split before any store
// access: a transfer has exactly one target taking 100%, a split two to
// MaxTransferParts targets, every share is above zero with at most two
// decimals, and the shares total 100.00% (AST-0507).
func ValidateTransfer(kind TransferKind, pcts []decimal.Decimal) error {
	switch kind {
	case TransferKindTransfer:
		if len(pcts) != 1 {
			return fail(CodeInvalidTransfer, "a transfer has exactly one target")
		}
	case TransferKindSplit:
		if len(pcts) < 2 || len(pcts) > MaxTransferParts {
			return fail(CodeInvalidTransfer, "a split has 2 to "+strconv.Itoa(MaxTransferParts)+" targets")
		}
	default:
		return fail(CodeInvalidTransfer, "kind must be transfer or split")
	}
	return checkPercentages(pcts)
}

func checkPercentages(pcts []decimal.Decimal) error {
	total := decimal.Zero
	for _, p := range pcts {
		if !p.IsPositive() || !p.Equal(p.Round(2)) {
			return fail(CodeSplitPercentage, "every share must be above 0% with at most two decimals")
		}
		total = total.Add(p)
	}
	if !total.Equal(hundred) {
		return fail(CodeSplitPercentage, "split percentages total "+total.StringFixed(2)+"%; they must total 100.00%")
	}
	return nil
}

// CheckTransferDate refuses a transfer dated before a book's in-service date
// or before the end of its last depreciated period: the depreciation already
// posted after the transfer date would move with the asset (BR-37.6 applies
// to a transfer as to a disposal).
func CheckTransferDate(date, inService time.Time, lastDepreciated string) error {
	if date.Before(inService) {
		return fail(CodeInvalidTransfer, "the transfer date is before the in-service date")
	}
	if lastDepreciated == "" {
		return nil
	}
	end, err := PeriodEnd(lastDepreciated)
	if err != nil {
		return err
	}
	if date.Before(end) {
		return fail(CodeBackdated, "transfer date precedes the end of the last depreciated period "+lastDepreciated+"; reverse the later depreciation first")
	}
	return nil
}

// BookAmounts are the balances of one book that a transfer or split moves.
// Impairment is the impairment share of Accumulated (kept so a target can
// still cap a reversal, FR-37.9); AcquisitionCost is the schedule basis a run
// spreads charges by.
type BookAmounts struct {
	Cost            decimal.Decimal
	Accumulated     decimal.Decimal
	Impairment      decimal.Decimal
	Salvage         decimal.Decimal
	AcquisitionCost decimal.Decimal
}

// AllocateShares divides a book's balances by percentage (FR-37.7, BR-37.7).
// Every share but the last is rounded half-up to the currency minor unit and
// the last takes the residual, so the shares sum back exactly. Accumulated
// depreciation is kept within each share's cost and impairment within each
// share's accumulated depreciation, so every target book satisfies
// 0 <= impairment <= accumulated <= cost.
func AllocateShares(b BookAmounts, pcts []decimal.Decimal, currency string) ([]BookAmounts, error) {
	if len(currency) != 3 {
		return nil, fail(CodeInvalidTransfer, "currency must be an ISO 4217 code")
	}
	if err := checkPercentages(pcts); err != nil {
		return nil, err
	}
	if b.Accumulated.IsNegative() || b.Accumulated.GreaterThan(b.Cost) || b.Impairment.IsNegative() || b.Impairment.GreaterThan(b.Accumulated) {
		return nil, fail(CodeInvalidTransfer, "book balances must keep 0 <= impairment <= accumulated <= cost")
	}
	minor := money.MinorUnits(currency)
	costs := allocate(b.Cost, pcts, minor)
	accs := allocateWithin(b.Accumulated, costs, pcts, minor)
	imps := allocateWithin(b.Impairment, accs, pcts, minor)
	salv := allocate(b.Salvage, pcts, minor)
	acq := allocate(b.AcquisitionCost, pcts, minor)
	out := make([]BookAmounts, len(pcts))
	for i := range pcts {
		out[i] = BookAmounts{Cost: costs[i], Accumulated: accs[i], Impairment: imps[i], Salvage: salv[i], AcquisitionCost: acq[i]}
	}
	return out, nil
}

// allocate rounds each non-final share and gives the last one the residual.
func allocate(total decimal.Decimal, pcts []decimal.Decimal, minor int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(pcts))
	rest := total
	for i := 0; i < len(pcts)-1; i++ {
		out[i] = total.Mul(pcts[i]).Div(hundred).Round(minor)
		rest = rest.Sub(out[i])
	}
	out[len(pcts)-1] = rest
	return out
}

// allocateWithin allocates inner (inner <= Σ outer) like allocate, but clamps
// each share so it never exceeds its outer share and what is left of inner
// always fits in what is left of outer.
func allocateWithin(inner decimal.Decimal, outer, pcts []decimal.Decimal, minor int32) []decimal.Decimal {
	out := make([]decimal.Decimal, len(pcts))
	restInner := inner
	restOuter := decimal.Zero
	for _, o := range outer {
		restOuter = restOuter.Add(o)
	}
	for i := 0; i < len(pcts)-1; i++ {
		share := inner.Mul(pcts[i]).Div(hundred).Round(minor)
		restOuter = restOuter.Sub(outer[i])
		low := decimal.Max(decimal.Zero, restInner.Sub(restOuter))
		high := decimal.Min(outer[i], restInner)
		share = decimal.Min(decimal.Max(share, low), high)
		out[i] = share
		restInner = restInner.Sub(share)
	}
	out[len(pcts)-1] = restInner
	return out
}

// GroupMember is one book offered to a group asset.
type GroupMember struct {
	AssetNumber   string
	LegalEntityID string
	ClassCode     string
	BookCode      string
	Currency      string
	Status        string
	InService     time.Time
	Cost          decimal.Decimal
}

// GroupSpec is what every member of a group asset must share: the legal
// entity, the asset class, the tax book and the tax year (FR-37.16).
type GroupSpec struct {
	LegalEntityID string
	ClassCode     string
	BookCode      string
	TaxYear       int
}

// GroupBasis validates the members against the group and returns the
// aggregate cost basis and the common currency (FR-37.16). Members must be
// Open books of the group's legal entity, class and book code, placed in
// service in the tax year, in one currency, and listed once.
func GroupBasis(spec GroupSpec, members []GroupMember) (decimal.Decimal, string, error) {
	if len(members) == 0 {
		return decimal.Zero, "", fail(CodeInvalidGroup, "a group asset needs at least one member")
	}
	seen := map[string]bool{}
	total := decimal.Zero
	currency := members[0].Currency
	for _, m := range members {
		switch {
		case seen[m.AssetNumber]:
			return decimal.Zero, "", fail(CodeInvalidGroup, "asset "+m.AssetNumber+" is listed twice")
		case m.LegalEntityID != spec.LegalEntityID:
			return decimal.Zero, "", fail(CodeInvalidGroup, "asset "+m.AssetNumber+" belongs to another legal entity")
		case m.ClassCode != spec.ClassCode:
			return decimal.Zero, "", fail(CodeInvalidGroup, "asset "+m.AssetNumber+" is in class "+m.ClassCode+", not "+spec.ClassCode)
		case m.BookCode != spec.BookCode:
			return decimal.Zero, "", fail(CodeInvalidGroup, "asset "+m.AssetNumber+" has no "+spec.BookCode+" book")
		case m.Currency != currency:
			return decimal.Zero, "", fail(CodeInvalidGroup, "asset "+m.AssetNumber+" is in "+m.Currency+", not "+currency)
		case m.Status != string(StatusOpen):
			return decimal.Zero, "", fail(CodeInvalidGroup, "the "+spec.BookCode+" book of asset "+m.AssetNumber+" is "+m.Status+"; only Open books can be grouped")
		case m.InService.Year() != spec.TaxYear:
			return decimal.Zero, "", fail(CodeInvalidGroup, "asset "+m.AssetNumber+" was placed in service in "+strconv.Itoa(m.InService.Year())+", not in tax year "+strconv.Itoa(spec.TaxYear))
		}
		seen[m.AssetNumber] = true
		total = total.Add(m.Cost)
	}
	return total, currency, nil
}

// GroupStart is the in-service date of a group's schedule: 1 January of its
// tax year, so a pool such as the DE Sammelposten depreciates one fifth in
// the year of acquisition whatever the members' own dates.
func GroupStart(taxYear int) time.Time {
	return time.Date(taxYear, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// YearAmount is one fiscal (calendar) year of a group schedule.
type YearAmount struct {
	Year        string
	Amount      decimal.Decimal
	Accumulated decimal.Decimal
	NBV         decimal.Decimal
}

// YearlySchedule computes the schedule of in and sums monthly lines by year
// (yearly lines stay as they are). A DE Sammelposten is StraightLine over 60
// months with no convention from 1 January: 24,000.00 gives 4,800.00 a year
// (AC-37.10).
func YearlySchedule(in Input) ([]YearAmount, error) {
	lines, err := Schedule(in)
	if err != nil {
		return nil, err
	}
	out := []YearAmount{}
	for _, l := range lines {
		y := l.Period[:4]
		if n := len(out); n > 0 && out[n-1].Year == y {
			out[n-1].Amount = out[n-1].Amount.Add(l.Amount)
			out[n-1].Accumulated = l.Accumulated
			out[n-1].NBV = l.NBV
			continue
		}
		out = append(out, YearAmount{Year: y, Amount: l.Amount, Accumulated: l.Accumulated, NBV: l.NBV})
	}
	return out, nil
}
