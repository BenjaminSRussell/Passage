package domain

import (
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	AckAccepted        = "ACCEPTED"
	AckRejected        = "REJECTED"
	AckChangesProposed = "CHANGES_PROPOSED"

	AckApproved   = "APPROVED"
	AckDeclined   = "DECLINED"
	AckSuperseded = "SUPERSEDED"
)

const (
	MaxAckReasonLen = 2000
	MaxAckChanges   = 500
)

type LineChange struct {
	LineNo       int              `json:"lineNo"`
	Qty          *decimal.Decimal `json:"qty,omitempty"`
	UnitPrice    *decimal.Decimal `json:"unitPrice,omitempty"`
	DeliveryDate *Day             `json:"deliveryDate,omitempty"`
}

type AckInput struct {
	VersionNo int          `json:"versionNo"`
	Outcome   string       `json:"outcome"`
	Reason    string       `json:"reason,omitempty"`
	Changes   []LineChange `json:"changes,omitempty"`
}

func (in AckInput) Validate() error {
	if in.VersionNo < 1 {
		return fail(CodeValidation, "versionNo must be the version of the order you are answering")
	}
	if utf8.RuneCountInString(in.Reason) > MaxAckReasonLen {
		return fail(CodeValidation, "reason must be at most %d characters", MaxAckReasonLen)
	}
	switch in.Outcome {
	case AckAccepted:
		if len(in.Changes) > 0 {
			return fail(CodeValidation, "changes can only be sent with a proposal")
		}
	case AckRejected:
		if strings.TrimSpace(in.Reason) == "" {
			return fail(CodeValidation, "a reason is required when you reject an order")
		}
		if len(in.Changes) > 0 {
			return fail(CodeValidation, "changes can only be sent with a proposal")
		}
	case AckChangesProposed:
		return validateChanges(in.Changes)
	default:
		return fail(CodeValidation, "outcome must be ACCEPTED, REJECTED or CHANGES_PROPOSED")
	}
	return nil
}

func validateChanges(changes []LineChange) error {
	if len(changes) == 0 || len(changes) > MaxAckChanges {
		return fail(CodeValidation, "a proposal needs between 1 and %d line changes", MaxAckChanges)
	}
	seen := make(map[int]bool, len(changes))
	for _, c := range changes {
		switch {
		case c.LineNo < 1:
			return fail(CodeValidation, "each change must name a line number")
		case seen[c.LineNo]:
			return fail(CodeValidation, "line %d appears more than once", c.LineNo)
		case c.Qty == nil && c.UnitPrice == nil && c.DeliveryDate == nil:
			return fail(CodeValidation, "line %d proposes nothing; give a quantity, a price or a delivery date", c.LineNo)
		case c.Qty != nil && !c.Qty.IsPositive():
			return fail(CodeValidation, "line %d: the quantity must be more than zero", c.LineNo)
		case c.UnitPrice != nil && c.UnitPrice.IsNegative():
			return fail(CodeValidation, "line %d: the price cannot be negative", c.LineNo)
		case c.DeliveryDate != nil && c.DeliveryDate.IsZero():
			return fail(CodeValidation, "line %d: the delivery date is missing", c.LineNo)
		}
		seen[c.LineNo] = true
	}
	return nil
}
