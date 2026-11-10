package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Self-billing codes (BR-35.15 and §6.35.18).
const (
	CodeSelfBillDifference = "EDI-0136"
	CodeSelfBillUnmatched  = "EDI-0137"
	CodeSelfBillUnbilled   = "EDI-0138"
)

// Self-billing outcomes for one customer line.
const (
	SelfBillMatched    = "MATCHED"
	SelfBillDifference = "DIFFERENCE"
	SelfBillUnmatched  = "UNMATCHED"
)

// Defaults and limits from §6.35.13 (self_billing_tolerance, self_billing_wait_days).
const (
	SelfBillDefaultWaitDays = 30
	selfBillMaxWaitDays     = 120
)

// SBShipment is a Passage shipment line the customer may self-bill against. The
// key is (delivery note or ASN number, PO line, item); the customer is implicit.
type SBShipment struct {
	ID           string
	DeliveryNote string
	POLine       string
	Item         string
	Quantity     decimal.Decimal
	UnitPrice    decimal.Decimal
	TaxRatePct   decimal.Decimal
	ShippedOn    time.Time
}

// SBLine is one line of the customer's self-billing document (INVOIC 389, VDA
// 4908 or 810 flagged as self-billed).
type SBLine struct {
	Line         string
	DeliveryNote string
	POLine       string
	Item         string
	Quantity     decimal.Decimal
	UnitPrice    decimal.Decimal
	Tax          decimal.Decimal // tax amount the customer computed for the line
	// NoTax says the document states no tax on the line (an INVOIC with its tax only in the summary): the tax is then not compared.
	NoTax bool
}

// SBTolerance is the customer's DIFFERENCE threshold per line.
type SBTolerance struct {
	Quantity decimal.Decimal // default 0
	Price    decimal.Decimal // default 0.01 per unit
}

// DefaultSBTolerance returns the defaults from §6.35.13.
func DefaultSBTolerance() SBTolerance {
	return SBTolerance{Quantity: decimal.Zero, Price: decimal.RequireFromString("0.01")}
}

// SBDifference names one field that differs between the document and Passage.
type SBDifference struct {
	Field  string `json:"field"`
	Theirs string `json:"theirs"`
	Ours   string `json:"ours"`
}

// SBLineResult is the match of one self-billed line.
type SBLineResult struct {
	Line        string         `json:"line"`
	Outcome     string         `json:"outcome"`
	ShipmentID  string         `json:"shipmentId,omitempty"`
	Differences []SBDifference `json:"differences,omitempty"`
}

// SBResult is the full self-billing reconciliation for one document.
type SBResult struct {
	Lines    []SBLineResult `json:"lines"`
	Unbilled []string       `json:"unbilled"` // shipment ids past the wait period
	Issues   []Issue        `json:"issues"`
}

// MatchSelfBilling applies BR-35.15. A line is matched to the first unclaimed
// shipment with the same key. Quantity or unit price beyond tolerance, or a tax
// amount differing from the recomputed VAT by more than 0.01, makes the line a
// DIFFERENCE. The VAT is recomputed at the shipment's rate on the line as the
// customer billed it (their quantity times their price), because the customer's
// document is the legal invoice: a price difference is then reported once, as a
// price difference, and a tax difference means the customer's VAT itself is
// wrong. A line without a shipment is UNMATCHED. A shipment still unbilled
// after waitDays since shipping is reported with EDI-0138.
func MatchSelfBilling(lines []SBLine, shipments []SBShipment, tol SBTolerance, asOf time.Time, waitDays int) (SBResult, error) {
	if waitDays < 1 || waitDays > selfBillMaxWaitDays {
		return SBResult{}, fmt.Errorf("self_billing_wait_days must be 1..%d, got %d", selfBillMaxWaitDays, waitDays)
	}
	if tol.Quantity.IsNegative() || tol.Price.IsNegative() {
		return SBResult{}, errors.New("self-billing tolerances must not be negative")
	}
	claimed := make([]bool, len(shipments))
	res := SBResult{}
	taxTolerance := decimal.RequireFromString("0.01")

	for _, line := range lines {
		idx := -1
		for i, s := range shipments {
			if !claimed[i] && s.DeliveryNote == line.DeliveryNote && s.POLine == line.POLine && s.Item == line.Item {
				idx = i
				break
			}
		}
		if idx < 0 {
			res.Lines = append(res.Lines, SBLineResult{Line: line.Line, Outcome: SelfBillUnmatched})
			res.Issues = append(res.Issues, Issue{Code: CodeSelfBillUnmatched, Reject: true,
				Detail: fmt.Sprintf("Self-billed line %s has no matching shipment.", line.Line)})
			continue
		}
		claimed[idx] = true
		s := shipments[idx]
		var diffs []SBDifference
		if d := line.Quantity.Sub(s.Quantity).Abs(); d.GreaterThan(tol.Quantity) {
			diffs = append(diffs, SBDifference{Field: "quantity", Theirs: line.Quantity.String(), Ours: s.Quantity.String()})
		}
		if d := line.UnitPrice.Sub(s.UnitPrice).Abs(); d.GreaterThan(tol.Price) {
			diffs = append(diffs, SBDifference{Field: "unitPrice", Theirs: line.UnitPrice.String(), Ours: s.UnitPrice.String()})
		}
		ourTax := line.Quantity.Mul(line.UnitPrice).Mul(s.TaxRatePct).Div(hundred).Round(2)
		if !line.NoTax && line.Tax.Sub(ourTax).Abs().GreaterThan(taxTolerance) {
			diffs = append(diffs, SBDifference{Field: "tax", Theirs: line.Tax.StringFixed(2), Ours: ourTax.StringFixed(2)})
		}
		lr := SBLineResult{Line: line.Line, Outcome: SelfBillMatched, ShipmentID: s.ID, Differences: diffs}
		if len(diffs) > 0 {
			lr.Outcome = SelfBillDifference
			for _, d := range diffs {
				res.Issues = append(res.Issues, Issue{Code: CodeSelfBillDifference,
					Detail: fmt.Sprintf("Self-billed line %s differs: %s %s vs %s.", line.Line, d.Field, d.Theirs, d.Ours)})
			}
		}
		res.Lines = append(res.Lines, lr)
	}

	for i, s := range shipments {
		if claimed[i] {
			continue
		}
		if !asOf.Before(s.ShippedOn.AddDate(0, 0, waitDays)) {
			res.Unbilled = append(res.Unbilled, s.ID)
			res.Issues = append(res.Issues, Issue{Code: CodeSelfBillUnbilled,
				Detail: fmt.Sprintf("Shipment %s not self-billed after %d days.", s.ID, waitDays)})
		}
	}
	return res, nil
}
