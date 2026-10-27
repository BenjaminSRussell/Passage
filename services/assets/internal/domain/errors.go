// Package domain holds the pure fixed-asset rules: depreciation schedules,
// disposal arithmetic, lease schedules, insured values, and asset and capex
// state machines. Nothing here performs I/O.
package domain

// Stable error codes raised by the domain. AST-05xx codes come from the
// PRD message catalog (7.37.18); AST-00xx codes are slice-local.
const (
	CodeInvalidDepreciation = "AST-0010"
	CodeInvalidDisposal     = "AST-0011"
	CodeInvalidLease        = "AST-0012"
	CodeIBRRange            = "AST-0515"
	CodeTooManyBooks        = "AST-0013"
	CodeAssetState          = "AST-0509"
)

// MaxBooksPerAsset is the default of assets.maxBooks (FR-37.2, ASSET_MAX_BOOKS);
// MaxBooksLimit is the largest value the parameter allows (PRD 7.37.13).
const (
	MaxBooksPerAsset = 10
	MaxBooksLimit    = 20
)

// Error is a domain rule violation with a stable code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, detail string) error { return &Error{Code: code, Detail: detail} }
