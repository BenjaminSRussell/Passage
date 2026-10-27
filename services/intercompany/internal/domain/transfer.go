package domain

import (
	"encoding/csv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// EvidenceRow is one priced intercompany line for the transfer-pricing evidence
// file (FR-40.4). Price and Cost are line totals in one currency; Basis is the
// pricing basis recorded on the link (PriceList, CostPlus, TransferPriceTable,
// or a charge's Cost / CostPlus / Table).
type EvidenceRow struct {
	Item     string
	Quantity decimal.Decimal
	Price    money.Amount
	Cost     money.Amount
	Basis    string
}

// EvidenceFile is the evidence for one entity pair and period (FR-40.4,
// PRD 7.40.13 "Transfer-pricing evidence": entity pair, period).
type EvidenceFile struct {
	SellerEntity string
	BuyerEntity  string
	Period       string // YYYY-MM
	Rows         []EvidenceRow
}

// EvidenceHeader is the first line of the evidence file.
var EvidenceHeader = []string{"seller_entity", "buyer_entity", "period", "item", "quantity", "currency", "price", "cost", "margin", "margin_pct", "basis"}

// EvidenceCSV renders the evidence file as CSV (PRD open question 3: CSV) with
// one line per row in input order: price, cost and margin at the currency minor
// unit and margin as a percentage of price at two decimals (0.00 for a zero
// price). Text cells that a spreadsheet would read as a formula are prefixed
// with an apostrophe.
func EvidenceCSV(f EvidenceFile) (string, error) {
	switch {
	case strings.TrimSpace(f.SellerEntity) == "" || strings.TrimSpace(f.BuyerEntity) == "":
		return "", invalid("seller and buyer entities are required")
	case f.SellerEntity == f.BuyerEntity:
		return "", invalid("seller and buyer must be different entities")
	case !validPeriod(f.Period):
		return "", invalid("period must be YYYY-MM")
	case len(f.Rows) == 0:
		return "", invalid("evidence needs at least one row")
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.Write(EvidenceHeader)
	hundred := decimal.NewFromInt(100)
	for _, r := range f.Rows {
		switch {
		case strings.TrimSpace(r.Item) == "" || strings.TrimSpace(r.Basis) == "":
			return "", invalid("evidence rows need an item and a pricing basis")
		case !r.Quantity.IsPositive():
			return "", invalid("evidence quantity must be positive")
		case len(r.Price.Currency) != 3 || r.Price.Currency != r.Cost.Currency:
			return "", invalid("price and cost must share one ISO 4217 currency")
		case r.Price.Value.IsNegative() || r.Cost.Value.IsNegative():
			return "", invalid("price and cost must not be negative")
		}
		price, cost := r.Price.Round(), r.Cost.Round()
		minor := money.MinorUnits(price.Currency)
		margin := price.Value.Sub(cost.Value)
		pct := decimal.Zero
		if price.Value.IsPositive() {
			pct = margin.Mul(hundred).Div(price.Value)
		}
		_ = w.Write([]string{
			cell(f.SellerEntity), cell(f.BuyerEntity), f.Period, cell(r.Item), r.Quantity.String(), price.Currency,
			price.Value.StringFixed(minor), cost.Value.StringFixed(minor), margin.StringFixed(minor),
			pct.StringFixed(2), cell(r.Basis),
		})
	}
	w.Flush()
	return b.String(), w.Error()
}

// cell neutralises spreadsheet formula injection in a free-text cell.
func cell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func validPeriod(p string) bool {
	if len(p) != 7 || p[4] != '-' {
		return false
	}
	for i, c := range p {
		if i != 4 && (c < '0' || c > '9') {
			return false
		}
	}
	return p[5:] >= "01" && p[5:] <= "12"
}
