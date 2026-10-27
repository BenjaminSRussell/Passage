package domain

import (
	"slices"

	"github.com/shopspring/decimal"
)

// Trade agreement vocabulary (FR-17.6). Account and item codes say which
// level a row applies to; the AccountID/ItemID names the vendor, vendor group,
// product or product group.
const (
	RelationPrice     = "PRICE"
	AccountVendor     = "VENDOR"
	AccountGroup      = "GROUP"
	AccountAll        = "ALL"
	ItemProduct       = "PRODUCT"
	ItemGroup         = "GROUP"
	ItemAll           = "ALL"
	StatusActive      = "ACTIVE"
	TieLowestPrice    = "LOWEST_PRICE"
	TieLatestFrom     = "LATEST_FROM_DATE"
	scoreVendor       = 4
	scoreGroup        = 2
	scoreAll          = 0
	priceDivPrecision = 12
)

// TradeAgreement is one vendor price row. Price is per PriceUnit units.
type TradeAgreement struct {
	ID          string
	Relation    string
	AccountCode string
	AccountID   string
	ItemCode    string
	ItemID      string
	Currency    string
	Unit        string
	FromQty     decimal.Decimal
	ToQty       *decimal.Decimal
	Price       decimal.Decimal
	PriceUnit   decimal.Decimal
	ValidFrom   Day
	ValidTo     *Day
	Consignment bool
	Status      string
}

// Query is a price lookup. VendorGroupIDs and ProductGroupIDs list the groups
// the vendor and product belong to (memberships owned by waves 7 and 8).
type Query struct {
	Relation        string
	VendorID        string
	VendorGroupIDs  []string
	ProductID       string
	ProductGroupIDs []string
	Qty             decimal.Decimal
	Currency        string
	Unit            string
	Date            Day
	Tie             string
}

// Candidate explains one row: its specificity, whether it passed every filter
// (Matched), the reason it was excluded, and its rank (1 is the winner).
type Candidate struct {
	AgreementID string `json:"agreementId"`
	Specificity int    `json:"specificity"`
	Matched     bool   `json:"matched"`
	Rank        int    `json:"rank"`
	Reason      string `json:"reason"`
}

// Resolution is the winning row (nil when none matches) and the explain list.
type Resolution struct {
	Winner     *TradeAgreement
	Candidates []Candidate
}

type scoredRow struct {
	idx       int
	row       TradeAgreement
	spec      int
	unitPrice decimal.Decimal
}

// Resolve applies FR-17.6 to rows: rows failing a filter are explained and
// dropped; the rest are ordered by specificity (vendor 4/group 2/all 0 plus
// product 4/group 2/all 0), then by the tie rule. Pure and deterministic.
// Query.Tie is LATEST_FROM_DATE (default) or LOWEST_PRICE; see compareRows.
func Resolve(rows []TradeAgreement, q Query) Resolution {
	rel := q.Relation
	if rel == "" {
		rel = RelationPrice
	}
	res := Resolution{Candidates: make([]Candidate, len(rows))}
	var matched []scoredRow
	for i, row := range rows {
		res.Candidates[i] = Candidate{AgreementID: row.ID}
		spec, reason := match(row, q, rel)
		res.Candidates[i].Specificity = spec
		res.Candidates[i].Reason = reason
		if reason != "" {
			continue
		}
		res.Candidates[i].Matched = true
		matched = append(matched, scoredRow{idx: i, row: row, spec: spec, unitPrice: row.Price.DivRound(row.PriceUnit, priceDivPrecision)})
	}
	slices.SortStableFunc(matched, func(a, b scoredRow) int {
		return compareRows(a, b, q.Tie)
	})
	for rank, s := range matched {
		res.Candidates[s.idx].Rank = rank + 1
		if rank == 0 {
			winner := s.row
			res.Winner = &winner
		} else {
			res.Candidates[s.idx].Reason = "lower rank than a more specific row"
		}
	}
	return res
}

// match returns the specificity of row for q, or the reason it cannot apply.
func match(row TradeAgreement, q Query, rel string) (int, string) {
	acct, item := accountScore(row, q), itemScore(row, q)
	switch {
	case row.Relation != rel:
		return 0, "relation mismatch"
	case row.Status != StatusActive:
		return 0, "not active"
	case row.Consignment:
		return 0, "consignment agreement"
	case row.Currency != q.Currency:
		return 0, "currency mismatch"
	case row.Unit != q.Unit:
		return 0, "unit mismatch"
	case q.Date.Before(row.ValidFrom.Time) || (row.ValidTo != nil && q.Date.After(row.ValidTo.Time)):
		return 0, "outside validity"
	case q.Qty.LessThan(row.FromQty) || (row.ToQty != nil && q.Qty.GreaterThanOrEqual(*row.ToQty)):
		return 0, "outside quantity break"
	case acct < 0:
		return 0, "vendor mismatch"
	case item < 0:
		return 0, "product mismatch"
	}
	return acct + item, ""
}

func accountScore(row TradeAgreement, q Query) int {
	switch row.AccountCode {
	case AccountVendor:
		if row.AccountID == q.VendorID {
			return scoreVendor
		}
		return -1
	case AccountGroup:
		if slices.Contains(q.VendorGroupIDs, row.AccountID) {
			return scoreGroup
		}
		return -1
	}
	return scoreAll
}

func itemScore(row TradeAgreement, q Query) int {
	switch row.ItemCode {
	case ItemProduct:
		if row.ItemID == q.ProductID {
			return scoreVendor
		}
		return -1
	case ItemGroup:
		if slices.Contains(q.ProductGroupIDs, row.ItemID) {
			return scoreGroup
		}
		return -1
	}
	return scoreAll
}

// ValidTie reports whether tie is a known tie rule; empty means the default.
func ValidTie(tie string) bool {
	return tie == "" || tie == TieLatestFrom || tie == TieLowestPrice
}

// compareRows orders matched rows: higher specificity first; then, by the tie
// rule, latest valid_from first and then lowest unit price (LATEST_FROM_DATE,
// the FR-17.6 default and what an empty rule means), or lowest unit price
// first and then latest valid_from (LOWEST_PRICE); then the id for a stable result.
func compareRows(a, b scoredRow, tie string) int {
	if a.spec != b.spec {
		return b.spec - a.spec
	}
	byPrice := func() int { return a.unitPrice.Cmp(b.unitPrice) }
	byFrom := func() int { return b.row.ValidFrom.Compare(a.row.ValidFrom.Time) }
	first, second := byFrom, byPrice
	if tie == TieLowestPrice {
		first, second = byPrice, byFrom
	}
	if c := first(); c != 0 {
		return c
	}
	if c := second(); c != 0 {
		return c
	}
	switch {
	case a.row.ID < b.row.ID:
		return -1
	case a.row.ID > b.row.ID:
		return 1
	}
	return 0
}
