package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// RMA status and disposition vocabulary (BR-30.3, BR-30.6, FR-30.11).
const (
	RMARequested     = "REQUESTED"
	RMAAuthorized    = "AUTHORIZED"
	RMAReceived      = "RECEIVED"
	RMAInspected     = "INSPECTED"
	RMADispositioned = "DISPOSITIONED"
	RMACredited      = "CREDITED"
	RMAClosed        = "CLOSED"
	RMARejected      = "REJECTED"
	RMAExpired       = "EXPIRED"

	RMAAuthorize   = "authorize"
	RMAReceive     = "receive"
	RMAInspect     = "inspect"
	RMADisposition = "disposition"
	RMACreditStart = "credit-start"
	RMACredit      = "credit"

	DispositionCreditOnly    = "CREDIT_ONLY"
	DispositionReturnToStock = "RETURN_TO_STOCK"

	CostOriginal = "ORIGINAL_COST"
	CostCurrent  = "CURRENT_COST"
	CostZero     = "ZERO"

	AccountSalesReturns = "4000"
	AccountRestocking   = "4200"
	AccountReceivable   = "1200"

	RMAValidityDays = 30
)

var (
	restockingDefault = decimal.NewFromInt(15)
	restockingMinUSD  = decimal.NewFromInt(25)
	restockingMinEUR  = decimal.NewFromInt(20)
	hundred           = decimal.NewFromInt(100)
)

// ReturnCredit is one RMA line's quantity, original net unit price and
// disposition, used to compute the credit note (BR-30.3).
type ReturnCredit struct {
	Disposition   string
	Quantity      decimal.Decimal
	UnitPrice     decimal.Decimal
	Currency      string
	RestockingPct *decimal.Decimal
}

// CreditNoteAmounts is the credit-note split: gross (qty × price), restocking
// charge and net credit to AR.
type CreditNoteAmounts struct {
	Gross      decimal.Decimal
	Restocking decimal.Decimal
	Net        decimal.Decimal
}

// GLLine is one side of the AC-30.5 credit-note journal.
type GLLine struct {
	Account string
	Debit   decimal.Decimal
	Credit  decimal.Decimal
}

// RMARefusal says why an RMA command cannot run.
type RMARefusal struct{ Detail string }

func (r *RMARefusal) Error() string { return r.Detail }

// FormatRMANumber renders the RMA- document number for sequence value seq.
func FormatRMANumber(seq int64) string { return fmt.Sprintf("RMA-%07d", seq) }

// CreditAmounts computes gross, restocking and net for one line (BR-30.3).
// RETURN_TO_STOCK defaults to 15% with a 25.00 USD / 20.00 EUR floor;
// CREDIT_ONLY defaults to 0%.
func CreditAmounts(in ReturnCredit) (CreditNoteAmounts, error) {
	if !in.Quantity.IsPositive() || in.UnitPrice.IsNegative() {
		return CreditNoteAmounts{}, &RMARefusal{"quantity must be greater than 0 and unit price must not be negative"}
	}
	gross := in.Quantity.Mul(in.UnitPrice).Round(2)
	pct, err := restockingPct(in)
	if err != nil {
		return CreditNoteAmounts{}, err
	}
	charge := gross.Mul(pct).Div(hundred).Round(2)
	if in.Disposition == DispositionReturnToStock {
		if min := restockingFloor(in.Currency); charge.LessThan(min) {
			charge = min
		}
	}
	if charge.GreaterThan(gross) {
		charge = gross
	}
	return CreditNoteAmounts{Gross: gross, Restocking: charge, Net: gross.Sub(charge)}, nil
}

func restockingPct(in ReturnCredit) (decimal.Decimal, error) {
	if in.RestockingPct != nil {
		if in.RestockingPct.IsNegative() || in.RestockingPct.GreaterThan(hundred) {
			return decimal.Zero, &RMARefusal{"restocking percent must be 0 to 100"}
		}
		return *in.RestockingPct, nil
	}
	switch in.Disposition {
	case DispositionReturnToStock:
		return restockingDefault, nil
	case DispositionCreditOnly:
		return decimal.Zero, nil
	default:
		return decimal.Zero, &RMARefusal{"disposition must be RETURN_TO_STOCK or CREDIT_ONLY"}
	}
}

func restockingFloor(currency string) decimal.Decimal {
	if currency == "EUR" {
		return restockingMinEUR
	}
	return restockingMinUSD
}

// Postings is the AC-30.5 journal: Dr 4000 gross / Cr 4200 restocking / Cr 1200 net.
func (a CreditNoteAmounts) Postings() []GLLine {
	out := []GLLine{{Account: AccountSalesReturns, Debit: a.Gross}}
	if a.Restocking.IsPositive() {
		out = append(out, GLLine{Account: AccountRestocking, Credit: a.Restocking})
	}
	if a.Net.IsPositive() {
		out = append(out, GLLine{Account: AccountReceivable, Credit: a.Net})
	}
	return out
}

// AdvanceRMA moves an RMA one step along BR-30.6. Authorize sets expiry to
// 30 days from now. Credit closes the RMA (CREDITED is transient).
func AdvanceRMA(status, action string, now time.Time) (string, *time.Time, error) {
	want := map[string]string{
		RMAAuthorize:   RMARequested,
		RMAReceive:     RMAAuthorized,
		RMAInspect:     RMAReceived,
		RMADisposition: RMAInspected,
		RMACreditStart: RMADispositioned,
		RMACredit:      RMACredited,
	}
	from, ok := want[action]
	if !ok {
		return "", nil, &RMARefusal{"unknown RMA action"}
	}
	if status != from {
		return "", nil, &RMARefusal{fmt.Sprintf("a %s RMA cannot %s", status, action)}
	}
	next := map[string]string{
		RMAAuthorize:   RMAAuthorized,
		RMAReceive:     RMAReceived,
		RMAInspect:     RMAInspected,
		RMADisposition: RMADispositioned,
		RMACreditStart: RMACredited,
		RMACredit:      RMAClosed,
	}[action]
	var expires *time.Time
	if action == RMAAuthorize {
		day := now.UTC().AddDate(0, 0, RMAValidityDays)
		expires = &day
	}
	return next, expires, nil
}

// ValidCostMethod reports whether m is a return cost method; the empty method means ORIGINAL_COST.
func ValidCostMethod(m string) bool {
	return m == "" || m == CostOriginal || m == CostCurrent || m == CostZero
}

// CreditQuantity is the quantity credited for a line: the accepted quantity, or the whole expected
// quantity of a line created CREDIT_ONLY that no goods came back for.
func CreditQuantity(creditOnly bool, expected, received, accepted decimal.Decimal) decimal.Decimal {
	if creditOnly && received.IsZero() {
		return expected
	}
	return accepted
}

// PhysicalReturn reports whether a disposition receives goods into stock.
func PhysicalReturn(disposition string) bool { return disposition == DispositionReturnToStock }
