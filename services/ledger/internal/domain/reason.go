package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// MaxReasonCommentLength bounds the free-text reason comment (FR-11.15).
const MaxReasonCommentLength = 255

// ModuleLedger is the applies_to value that reversal rules check (FR-11.15).
const ModuleLedger = "ledger"

var (
	reasonCodeRe = regexp.MustCompile(`^[A-Z0-9_]{1,10}$`)
	// reasonModules is the closed applies_to set (FR-11.15, reason_code CHECK).
	reasonModules = []string{"ledger", "bank", "customer", "vendor", "fixed_asset", "inventory_counting", "tax", "project"}
)

// ValidReasonCode reports whether code matches ^[A-Z0-9_]{1,10}$.
func ValidReasonCode(code string) bool { return reasonCodeRe.MatchString(code) }

// ValidReasonModule reports whether m is one of the shared financial modules.
func ValidReasonModule(m string) bool { return slices.Contains(reasonModules, m) }

// ReasonInfo is the subset of a shared reason code that reversal rules read.
type ReasonInfo struct {
	Code                          string
	AppliesTo                     []string
	RequiresComment               bool
	NegativePostingAllowed        bool
	AlternativePostingDateAllowed bool
	Active                        bool
}

// ReversalPolicy is the ledger configuration that reversal rules read.
type ReversalPolicy struct {
	ReasonRequired  bool // ledger.reversal_reason_required
	NegativeEnabled bool // ledger.negative_posting_enabled
}

// ReversalRequest is the caller's reversal input relevant to the rules.
type ReversalRequest struct {
	ReasonCode    string
	ReasonComment string
	OriginalDate  time.Time
	ReversalDate  time.Time
}

// CheckReversal applies FR-11.15 and BR-11.16 to a reversal. It returns whether
// the reversing lines are negative postings (storno) and every violation found.
// reason is nil when the code does not exist. Negative is false whenever a
// violation is returned, so callers may use it only when the slice is empty.
func CheckReversal(p ReversalPolicy, reason *ReasonInfo, req ReversalRequest) (bool, []Violation) {
	var v []Violation
	if req.ReasonCode == "" {
		if p.ReasonRequired {
			v = append(v, Violation{Code: "LEDGER_REASON_REQUIRED", Field: "reasonCode",
				Message: "a reason code is required for this reversal"})
		}
		return false, v
	}
	if reason == nil || !reason.Active {
		return false, append(v, Violation{Code: "LEDGER_REASON_REQUIRED", Field: "reasonCode",
			Message: fmt.Sprintf("reason code %s is unknown or inactive", req.ReasonCode)})
	}
	if !slices.Contains(reason.AppliesTo, ModuleLedger) {
		v = append(v, Violation{Code: "LEDGER_REASON_INVALID_MODULE", Field: "reasonCode",
			Message: fmt.Sprintf("reason code %s does not apply to ledger", reason.Code)})
	}
	if reason.RequiresComment && strings.TrimSpace(req.ReasonComment) == "" {
		v = append(v, Violation{Code: "LEDGER_REASON_REQUIRED", Field: "reasonComment",
			Message: fmt.Sprintf("reason code %s requires a comment", reason.Code)})
	}
	if !reason.AlternativePostingDateAllowed && !req.ReversalDate.Equal(req.OriginalDate) {
		v = append(v, Violation{Code: "LEDGER_DATE_INVALID", Field: "postingDate",
			Message: fmt.Sprintf("reason code %s requires the reversal date to equal the original posting date %s",
				reason.Code, req.OriginalDate.Format(time.DateOnly))})
	}
	negative := false
	if reason.NegativePostingAllowed {
		if !p.NegativeEnabled {
			v = append(v, Violation{Code: "LEDGER_NEGATIVE_POSTING_DISABLED", Field: "reasonCode",
				Message: fmt.Sprintf("reason code %s allows negative posting but the ledger does not", reason.Code)})
		} else {
			negative = true
		}
	}
	return negative && len(v) == 0, v
}
