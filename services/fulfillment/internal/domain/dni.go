package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// DNIRow is one packing slip that is delivered but not invoiced (FR-28.12):
// Amount is its open balance on account 1405 as of the report date, that is
// the slip's Dr 1405 less any reversal posted by then. A slip posted with
// cogs_at_shipment never touches 1405 and so never has a row.
type DNIRow struct {
	PackingSlipID     string          `json:"packingSlipId"`
	PackingSlipNumber string          `json:"packingSlipNumber"`
	ShipmentID        string          `json:"shipmentId"`
	OrderID           string          `json:"orderId"`
	CustomerID        string          `json:"customerId"`
	Currency          string          `json:"currency"`
	PostingDate       time.Time       `json:"postingDate"`
	Amount            decimal.Decimal `json:"amount"`
}

// DNITotal is the delivered-not-invoiced balance in one currency.
type DNITotal struct {
	Currency string          `json:"currency"`
	Count    int             `json:"count"`
	Amount   decimal.Decimal `json:"amount"`
}

// DeliveredNotInvoiced sums the rows per currency (AC-28.8). Currencies are
// sorted so the result is deterministic; an empty input yields no totals.
func DeliveredNotInvoiced(rows []DNIRow) []DNITotal {
	byCurrency := map[string]*DNITotal{}
	for _, r := range rows {
		t, ok := byCurrency[r.Currency]
		if !ok {
			t = &DNITotal{Currency: r.Currency, Amount: decimal.Zero}
			byCurrency[r.Currency] = t
		}
		t.Count++
		t.Amount = t.Amount.Add(r.Amount)
	}
	out := make([]DNITotal, 0, len(byCurrency))
	for _, t := range byCurrency {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// DaysOutstanding counts whole calendar days (UTC) from the posting date to
// asOf. A posting dated after asOf is reported as zero days rather than negative.
func DaysOutstanding(postingDate, asOf time.Time) int {
	days := int(dateOnly(asOf).Sub(dateOnly(postingDate)).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}
