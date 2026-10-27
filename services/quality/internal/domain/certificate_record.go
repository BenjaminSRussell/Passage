package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// ProblemCertificateNOK is the problem type of the VENDOR NCR raised when a
// received certificate is out of specification (BR-33.13).
const ProblemCertificateNOK = "CERTIFICATE_NOK"

// MaxDocumentRef bounds the reference to the certificate document (an upload id,
// an EDI attachment or a supplier-portal document number).
const MaxDocumentRef = 200

// CertRecord is one certificate receipt, or a waiver, recorded against a quality
// order (FR-33.15). Records are append-only; the gate reads them all (BR-33.13).
// A waiver carries the order's required type, no values and a Reason.
type CertRecord struct {
	ID               string               `json:"id"`
	OrderID          string               `json:"qualityOrderId"`
	Type             string               `json:"type"`
	DocumentRef      string               `json:"documentRef,omitempty"`
	Values           map[string]CertValue `json:"values"`
	InSpec           bool                 `json:"inSpec"`
	Deviations       []string             `json:"deviations"`
	Waiver           bool                 `json:"waiver"`
	Reason           string               `json:"reason,omitempty"`
	NonconformanceID string               `json:"nonconformanceId,omitempty"`
	RecordedBy       string               `json:"recordedBy"`
	RecordedAt       time.Time            `json:"recordedAt"`
}

// ValidCertType reports whether t is a certificate type that can be received
// (every requirement except NONE).
func ValidCertType(t string) bool { return t != CertNone && ValidCertRequirement(t) }

// ValidateCertificate returns field errors for a certificate receipt against the
// order's test group: a known type, a document reference of at most 200
// characters, and values only for tests of the group, a number for a variable
// test and an outcome id for an attribute test.
func ValidateCertificate(certType, documentRef string, values map[string]CertValue, group TestGroup) map[string]string {
	fields := map[string]string{}
	if !ValidCertType(certType) {
		fields["type"] = "must be COA, COC or EN10204_31"
	}
	if utf8.RuneCountInString(documentRef) > MaxDocumentRef {
		fields["documentRef"] = "must be at most 200 characters"
	}
	lines := map[string]TestLine{}
	for _, l := range group.Tests {
		lines[l.TestID] = l
	}
	for testID, v := range values {
		key := "values." + testID
		l, ok := lines[testID]
		switch {
		case !ok:
			fields[key] = "is not a test of this order's test group"
		case l.Kind == KindVariable && (v.Value == nil || v.Outcome != ""):
			fields[key] = "needs a value (a number) for a variable test"
		case l.Kind == KindVariable && (v.Value.Exponent() < -6 || v.Value.Abs().GreaterThanOrEqual(maxQty)):
			fields[key] = "must have at most 12 integer digits and 6 decimals"
		case l.Kind == KindAttribute && (v.Value != nil || !ids.Valid(v.Outcome)):
			fields[key] = "needs an outcome id for an attribute test"
		}
	}
	return fields
}

// EvaluateCertificate checks a certificate's values against the specification
// (BR-33.13). A certificate of conformance (COC) is a statement without values:
// with no values it conforms; values it does carry are checked like any other.
func EvaluateCertificate(certType string, values map[string]CertValue, group TestGroup) (bool, []string) {
	if certType == CertCoC && len(values) == 0 {
		return true, []string{}
	}
	dev := CertDeviations(group.Tests, values)
	if dev == nil {
		dev = []string{}
	}
	return len(dev) == 0, dev
}

// CertificateStatusOf derives the gate status of an order from its certificate
// records (BR-33.13): a waiver of the required type wins, otherwise the latest
// receipt of the required type decides (CertificateStatus). recs are oldest first.
func CertificateStatusOf(required string, recs []CertRecord) string {
	waived := false
	receipts := make([]CertReceipt, 0, len(recs))
	for _, r := range recs {
		if r.Waiver {
			waived = waived || r.Type == required
			continue
		}
		receipts = append(receipts, CertReceipt{Type: r.Type, InSpec: r.InSpec})
	}
	return CertificateStatus(required, receipts, waived)
}

// CertificateGate applies BR-33.13 to a usage decision: when the order demands a
// certificate, a decision that releases quantity (accepted, or accepted with a
// deviation) needs it received in specification or waived (QM-0125). A decision
// that releases nothing is not gated.
func CertificateGate(o Order, split Split, recs []CertRecord) error {
	if o.CertificateRequired == "" || o.CertificateRequired == CertNone {
		return nil
	}
	if !split.Accept.IsPositive() && !split.Deviation.IsPositive() {
		return nil
	}
	return CheckCertificateAccept(o.CertificateRequired, CertificateStatusOf(o.CertificateRequired, recs))
}

// CheckCertificateOrder says whether an order takes a certificate record: not when
// it is closed (QM-0109) or cancelled (QM-0115), and a waiver only when the order
// requires a certificate (QM-0180).
func CheckCertificateOrder(o Order, waiver bool) error {
	switch o.Status {
	case StatusClosed:
		return fail(CodeClosed, "Order is closed; certificates can no longer be recorded.")
	case StatusCancelled:
		return fail(CodeStatus, "certificates cannot be recorded on a CANCELLED order")
	}
	if waiver && (o.CertificateRequired == "" || o.CertificateRequired == CertNone) {
		return fail(CodeCertNotRequired, "the order does not require a certificate, so there is nothing to waive")
	}
	return nil
}

// CertificateNCR is the VENDOR nonconformance raised for an out-of-specification
// certificate (BR-33.13). It covers the whole lot, which stays blocked.
func CertificateNCR(o Order, rec CertRecord, now time.Time) (Nonconformance, error) {
	dev := append([]string{}, rec.Deviations...)
	sort.Strings(dev)
	desc := fmt.Sprintf("Raised by an out-of-specification %s certificate on quality order %s", rec.Type, o.ID)
	if len(dev) > 0 {
		desc += ": " + strings.Join(dev, ", ")
	}
	desc += "."
	if utf8.RuneCountInString(desc) > MaxNCRDescription {
		desc = string([]rune(desc)[:MaxNCRDescription-1]) + "…"
	}
	return NewNCR(Nonconformance{
		Source:         SourceVendor,
		ItemID:         o.ItemID,
		Dims:           o.Dims,
		Qty:            o.LotQty,
		UOM:            o.UOM,
		ProblemType:    ProblemCertificateNOK,
		Severity:       NCRMajor,
		Description:    desc,
		QualityOrderID: o.ID,
	}, rec.RecordedBy, now)
}
