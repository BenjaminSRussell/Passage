// Package domain holds the project-operations rules of wave 41: project
// validation, hierarchy inheritance and stage transitions, project groups,
// posting-profile keys and resolution, plan-version rules, WBS validation and
// the CPM work-breakdown scheduler, cost-to-cost percentage of completion,
// earned value, invoice proposal selection, and retention and interest
// arithmetic. Everything here is pure: no I/O, no clock reads, no globals
// besides constants.
package domain

import (
	"errors"
	"fmt"
)

// Catalog codes (PRD §8.41.18) raised by this package.
const (
	CodePostingProfile = "PROJ-POST-001"
	CodeStage          = "PROJ-STAGE-001"
	CodeFinish         = "PROJ-STAGE-002"
	CodeWBSCycle       = "PROJ-WBS-001"
	CodeRetentionEarly = "PROJ-RET-001"
	// CodeParentConflict is service-specific (not in §8.41.18): a sub-project
	// breaks an FR-41.1 hierarchy rule (different customer or legal entity).
	CodeParentConflict = "PROJ-PARENT-002"
)

// Error is a business-rule violation carrying a stable catalog code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func rule(code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ErrInvalidInput marks a request that is malformed rather than rule-violating.
var ErrInvalidInput = errors.New("projects: invalid input")
