// Package domain holds the Wave 04 workflow, batch and notify kernels.
package domain

const (
	CodeRecallNotAllowed  = "WFL-0401"
	CodeCommentRequired   = "WFL-0402"
	CodeDelegateRole      = "WFL-0403"
	CodeDelegationLimit   = "WFL-0404"
	CodeNoApprover        = "WFL-0405"
	CodeAlreadyInApproval = "WFL-0406"
	CodeBulkApproveLimit  = "WFL-0407"
	CodeJobHold           = "WFL-0408"
	CodeRunOverlap        = "WFL-0409"
	CodeSelfApprove       = "WFL-0410"
	CodeRunTimeout        = "WFL-0411"
	// CodeInboxSourcesChanged answers 409 when the sources that answered a My work page differ from those of the first page.
	CodeInboxSourcesChanged = "WFL-0412"
	CodeSchemaIncompatible  = "EVT-0401"
	CodeUnknownPlaceholder  = "NTF-0406"
	CodeRenderFailed        = "NTF-0407"
	CodeSenderUnverified    = "NTF-0401"
	CodeAllowlist           = "NTF-0405"
	CodeUnknown             = "WFL-0400"
	CodeInvalid             = "COM-0001"
)

// Error is a stable domain failure.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func errf(code, detail string) *Error { return &Error{Code: code, Detail: detail} }
