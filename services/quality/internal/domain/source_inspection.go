package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// maxBatchLength bounds a batch number on a receipt check.
const maxBatchLength = 60

// ReceiptCheckInput is one receipt line to check against the accepted source
// inspections of its PO line and batch (FR-33.19, BR-33.16).
type ReceiptCheckInput struct {
	POLineID      string          `json:"poLineId"`
	Batch         string          `json:"batch"`
	ReceiptLineID string          `json:"receiptLineId"`
	ReceivedQty   decimal.Decimal `json:"receivedQty"`
}

// ValidateReceiptCheck returns field errors for a receipt check, empty when valid.
func ValidateReceiptCheck(in ReceiptCheckInput) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(in.POLineID) {
		fields["poLineId"] = "must be a UUID"
	}
	if !ids.Valid(in.ReceiptLineID) {
		fields["receiptLineId"] = "must be a UUID"
	}
	if b := strings.TrimSpace(in.Batch); b == "" || len(b) > maxBatchLength {
		fields["batch"] = "must be 1 to 60 characters"
	}
	if !in.ReceivedQty.IsPositive() || !in.ReceivedQty.Equal(in.ReceivedQty.Round(6)) {
		fields["receivedQty"] = "must be positive with at most 6 decimals"
	}
	return fields
}

// ReceiptCheck is the stored answer for one receipt line (BR-33.16). AcceptedQty is
// what source inspection accepted for the PO line and batch over all its orders,
// CoveredBefore what earlier receipts of that PO line and batch already used, so one
// accepted quantity never covers two receipts. Skip means no receiving quality order
// is created (QM-0128); otherwise the receiving service creates a normal order for
// NormalQty.
type ReceiptCheck struct {
	ID            string          `json:"id"`
	POLineID      string          `json:"poLineId"`
	Batch         string          `json:"batch"`
	ReceiptLineID string          `json:"receiptLineId"`
	ReceivedQty   decimal.Decimal `json:"receivedQty"`
	AcceptedQty   decimal.Decimal `json:"acceptedQty"`
	CoveredBefore decimal.Decimal `json:"coveredBefore"`
	ReceiptCover
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message"`
	CheckedBy string    `json:"checkedBy"`
	CheckedAt time.Time `json:"checkedAt"`
}

// CheckReceipt applies BR-33.16 to one receipt line: the part of the accepted
// source-inspection quantity that earlier receipts have not used covers the
// receipt up to its received quantity; any excess gets a normal quality order.
func CheckReceipt(in ReceiptCheckInput, accepted, coveredBefore decimal.Decimal, user string, now time.Time) (ReceiptCheck, error) {
	if accepted.IsNegative() || coveredBefore.IsNegative() {
		return ReceiptCheck{}, invalid("accepted and covered quantities cannot be negative")
	}
	batch := strings.TrimSpace(in.Batch)
	available := decimal.Max(accepted.Sub(coveredBefore), decimal.Zero)
	cover, err := SourceInspectionCover([]SourceAcceptance{{POLine: in.POLineID, Batch: batch, AcceptedQty: available}},
		in.POLineID, batch, in.ReceivedQty)
	if err != nil {
		return ReceiptCheck{}, err
	}
	out := ReceiptCheck{POLineID: in.POLineID, Batch: batch, ReceiptLineID: in.ReceiptLineID, ReceivedQty: in.ReceivedQty,
		AcceptedQty: accepted, CoveredBefore: coveredBefore, ReceiptCover: cover, CheckedBy: user, CheckedAt: now.UTC()}
	switch {
	case cover.Skip:
		out.Code = CodeSourceSkipped
		out.Message = "Batch " + batch + " was source-inspected; receiving inspection skipped."
	case cover.CoveredQty.IsPositive():
		out.Message = "Batch " + batch + " was source-inspected for " + cover.CoveredQty.String() + "; create a receiving quality order for the remaining " + cover.NormalQty.String() + "."
	case accepted.IsPositive():
		out.Message = "The source-inspected quantity of batch " + batch + " is used by earlier receipts; create a receiving quality order for " + cover.NormalQty.String() + "."
	default:
		out.Message = "No accepted source inspection covers batch " + batch + "; create a receiving quality order for " + cover.NormalQty.String() + "."
	}
	return out, nil
}

// SameReceipt reports whether a repeated check of a receipt line carries the same
// PO line, batch and quantity as the stored one, so it can be answered from it.
func (c ReceiptCheck) SameReceipt(in ReceiptCheckInput) bool {
	return c.POLineID == in.POLineID && c.Batch == strings.TrimSpace(in.Batch) && c.ReceivedQty.Equal(in.ReceivedQty)
}
