package domain

// Error is a domain failure with a stable PRD message-catalog code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func errf(code, detail string) *Error { return &Error{Code: code, Detail: detail} }

const (
	CodeNumFormat           = "PRD-NUM-FORMAT"
	CodeNumImmutable        = "PRD-NUM-IMMUTABLE"
	CodeVarCap              = "PRD-VAR-CAP"
	CodeVarDimUnknown       = "PRD-VAR-DIMVAL-UNKNOWN"
	CodeReleaseDraft        = "PRD-RELEASE-DRAFT"
	CodeReleaseDefault      = "PRD-RELEASE-DEFAULT-MISSING"
	CodeDimGroupLocked      = "PRD-DIMGRP-LOCKED"
	CodeUOMUnderflow        = "PRD-UOM-UNDERFLOW"
	CodeGTINCheckDigit      = "PRD-GTIN-CHECKDIGIT"
	CodeGTINDuplicate       = "PRD-GTIN-DUPLICATE"
	CodeCfgTimeout          = "PRD-CFG-TIMEOUT"
	CodeCfgInfeasible       = "PRD-CFG-INFEASIBLE"
	CodeAttrRequired        = "PRD-ATTR-REQUIRED"
	CodeAttrRange           = "PRD-ATTR-RANGE"
	CodeLCBlocked           = "PRD-LC-BLOCKED"
	CodeLCWarn              = "PRD-LC-WARN"
	CodeRelSelf             = "PRD-REL-SELF"
	CodeGrpChgOnHand        = "PRD-GRPCHG-ONHAND"
	CodeSvcStocked          = "PRD-SVC-STOCKED"
	CodeDocOriginalLimit    = "PRD-DOC-ORIGINAL-LIMIT"
	CodeLifecycleIllegal    = "PRD-LC-ILLEGAL"
	CodeDimGroupFrozen      = "PRD-DIMGRP-FROZEN"
)
