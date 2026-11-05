package domain

import (
	"strings"

	"github.com/shopspring/decimal"
)

// Certificate requirement types (FR-33.15, parameter certificate_required_default).
const (
	CertNone       = "NONE"
	CertCoA        = "COA"
	CertCoC        = "COC"
	CertEN10204_31 = "EN10204_31"
)

// Certificate gate statuses (BR-33.13).
const (
	CertNotRequired = "NOT_REQUIRED"
	CertMissing     = "MISSING"
	CertReceivedOK  = "RECEIVED_OK"
	CertReceivedNOK = "RECEIVED_NOK"
	CertWaived      = "WAIVED"
)

// maxWaiverReason bounds the free-text reason recorded with a waiver.
const maxWaiverReason = 500

// ValidCertRequirement reports whether t is a known requirement type.
func ValidCertRequirement(t string) bool {
	switch t {
	case CertNone, CertCoA, CertCoC, CertEN10204_31:
		return true
	}
	return false
}

// CertReceipt is a certificate recorded against a quality order. InSpec is the
// result of checking its values against the specification (CertInSpec).
type CertReceipt struct {
	Type   string `json:"type"`
	InSpec bool   `json:"inSpec"`
}

// CertValue is a certificate's value for one test: a number for variable tests,
// an outcome id for attribute tests.
type CertValue struct {
	Value   *decimal.Decimal `json:"value,omitempty"`
	Outcome string           `json:"outcome,omitempty"`
}

// CertInSpec checks certificate values against a test group (CertDeviations).
func CertInSpec(lines []TestLine, values map[string]CertValue) bool {
	return len(CertDeviations(lines, values)) == 0
}

// CertDeviations returns the names (or ids) of the tests a certificate does not
// meet: a required test without a value, and any test whose value is outside its
// tolerance or whose outcome is not a passing one. An optional test without a value
// is not a deviation. The order follows the test group.
func CertDeviations(lines []TestLine, values map[string]CertValue) []string {
	var out []string
	for _, l := range lines {
		v, ok := values[l.TestID]
		name := l.Name
		if name == "" {
			name = l.TestID
		}
		switch {
		case !ok || (v.Value == nil && v.Outcome == ""):
			if l.Required {
				out = append(out, name)
			}
		case l.Kind == KindVariable:
			if v.Value == nil {
				out = append(out, name)
			} else if _, pass := EvaluateVariable(l, *v.Value); !pass {
				out = append(out, name)
			}
		case l.Kind == KindAttribute:
			if !EvaluateAttribute(l, v.Outcome) {
				out = append(out, name)
			}
		default:
			out = append(out, name)
		}
	}
	return out
}

// CertificateStatus returns the gate status for a required certificate type (BR-33.13).
// The latest receipt of the required type decides. A waiver overrides a missing or
// NOK certificate; the caller must check quality.usage.decide and ValidateCertWaiver.
func CertificateStatus(required string, receipts []CertReceipt, waived bool) string {
	if required == "" || required == CertNone {
		return CertNotRequired
	}
	if waived {
		return CertWaived
	}
	var latest *CertReceipt
	for i := range receipts {
		if receipts[i].Type == required {
			latest = &receipts[i]
		}
	}
	switch {
	case latest == nil:
		return CertMissing
	case !latest.InSpec:
		return CertReceivedNOK
	}
	return CertReceivedOK
}

// CheckCertificateAccept rejects an ACCEPT decision unless the certificate gate is
// open (QM-0125). A NOK certificate also creates an NCR, which the caller handles.
func CheckCertificateAccept(required, status string) error {
	switch status {
	case CertNotRequired, CertReceivedOK, CertWaived:
		return nil
	}
	return fail(CodeCertRequired, "Certificate "+required+" is required before acceptance.")
}

// ValidateCertWaiver requires a reason for waiving a certificate.
func ValidateCertWaiver(reason string) error {
	r := strings.TrimSpace(reason)
	if r == "" {
		return invalid("a certificate waiver needs a reason")
	}
	if len(r) > maxWaiverReason {
		return invalid("a certificate waiver reason must be 500 characters or fewer")
	}
	return nil
}

// SourceAcceptance is an accepted source inspection of a PO line and batch.
type SourceAcceptance struct {
	POLine      string          `json:"poLine"`
	Batch       string          `json:"batch"`
	AcceptedQty decimal.Decimal `json:"acceptedQty"`
}

// ReceiptCover is how a receipt splits between source inspection and receiving
// inspection (BR-33.16). Skip is true only when the whole received quantity is covered.
type ReceiptCover struct {
	Skip       bool            `json:"skip"`
	CoveredQty decimal.Decimal `json:"coveredQty"`
	NormalQty  decimal.Decimal `json:"normalQty"`
}

// SourceInspectionCover applies BR-33.16: the accepted source-inspection quantity for
// (PO line, batch) covers the receipt up to min(accepted, received). Receiving
// inspection skips the covered part; any excess gets a normal quality order.
func SourceInspectionCover(accepted []SourceAcceptance, poLine, batch string, received decimal.Decimal) (ReceiptCover, error) {
	if !received.IsPositive() {
		return ReceiptCover{}, invalid("received quantity must be positive")
	}
	sum := decimal.Zero
	for _, a := range accepted {
		if a.POLine == poLine && a.Batch == batch && a.AcceptedQty.IsPositive() {
			sum = sum.Add(a.AcceptedQty)
		}
	}
	covered := decimal.Min(sum, received)
	return ReceiptCover{
		Skip:       covered.Equal(received),
		CoveredQty: covered,
		NormalQty:  received.Sub(covered),
	}, nil
}
