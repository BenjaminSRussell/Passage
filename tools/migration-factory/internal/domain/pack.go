// Package domain holds the pure migration-factory rules: pack run lifecycle,
// control totals, validation and quarantine, idempotent load planning, the
// parallel-run variance classifier and the cutover plan. No I/O lives here.
package domain

import (
	"errors"
	"fmt"
)

// Pack is one of the nine transactional migration packs (FR-49.1).
type Pack string

const (
	PackOpenItems   Pack = "open_items"
	PackGLBalances  Pack = "gl_balances"
	PackOnhand      Pack = "onhand"
	PackOpenOrders  Pack = "open_orders"
	PackWIPProd     Pack = "wip_prod"
	PackFixedAssets Pack = "fixed_assets"
	PackProjectWIP  Pack = "project_wip"
	PackPayrollYTD  Pack = "payroll_ytd"
	PackBankBal     Pack = "bank_balances"
)

// Packs lists every pack in FR-49.1 order.
var Packs = []Pack{PackOpenItems, PackGLBalances, PackOnhand, PackOpenOrders, PackWIPProd, PackFixedAssets, PackProjectWIP, PackPayrollYTD, PackBankBal}

// ParsePack returns the pack named s, or ErrUnknownPack.
func ParsePack(s string) (Pack, error) {
	for _, p := range Packs {
		if string(p) == s {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownPack, s)
}

// SourceSystem is the legacy system a pack is extracted from.
type SourceSystem string

const (
	SourceAX2012 SourceSystem = "AX2012"
	SourceD365FO SourceSystem = "D365FO"
	SourceSAPECC SourceSystem = "SAPECC"
	SourceS4HANA SourceSystem = "S4HANA"
)

// ParseSourceSystem validates a source system enum value.
func ParseSourceSystem(s string) (SourceSystem, error) {
	switch SourceSystem(s) {
	case SourceAX2012, SourceD365FO, SourceSAPECC, SourceS4HANA:
		return SourceSystem(s), nil
	}
	return "", fmt.Errorf("%w: source system %q", ErrInvalid, s)
}

// RunStatus is the pack run state machine (BR-49.1).
type RunStatus string

const (
	StatusExtracted  RunStatus = "EXTRACTED"
	StatusValidated  RunStatus = "VALIDATED"
	StatusLoaded     RunStatus = "LOADED"
	StatusReconciled RunStatus = "RECONCILED"
	StatusFailed     RunStatus = "FAILED"
	StatusRolledBack RunStatus = "ROLLED_BACK"
)

// ErrUnknownPack is returned for a pack name outside the nine packs.
var ErrUnknownPack = errors.New("unknown pack")

// ErrInvalid marks an input that fails a domain rule.
var ErrInvalid = errors.New("invalid input")

// ErrTransition is returned for a status change the state machine forbids.
var ErrTransition = errors.New("illegal pack run transition")

var allowed = map[RunStatus][]RunStatus{
	StatusExtracted:  {StatusValidated, StatusFailed},
	StatusValidated:  {StatusLoaded, StatusFailed},
	StatusLoaded:     {StatusReconciled, StatusFailed, StatusRolledBack},
	StatusReconciled: {StatusRolledBack},
	StatusFailed:     {},
	StatusRolledBack: {},
}

// CanTransition reports whether the state machine allows from -> to.
func CanTransition(from, to RunStatus) bool {
	for _, t := range allowed[from] {
		if t == to {
			return true
		}
	}
	return false
}
