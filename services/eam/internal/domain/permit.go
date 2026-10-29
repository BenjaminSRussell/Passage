package domain

import (
	"errors"
	"fmt"
	"time"
)

// Permit error codes (8.44.18). The gate and transition rules return them inside
// a GateError so the store can map them to problem details without parsing text.
const (
	CodePermitMissing  = "EAM-PMT-001"
	CodeIsolationOpen  = "EAM-PMT-002"
	CodeLocksOpen      = "EAM-PMT-003"
	CodePermitExpired  = "EAM-PMT-004"
	CodeSelfApproval   = "EAM-PMT-005"
	CodePermitState    = "EAM-PMT-006"
	CodeLockHeld       = "EAM-PMT-007"
	CodeWorkInProgress = "EAM-PMT-008"
)

// GateError is a permit rule violation with its stable code.
type GateError struct {
	Code   string
	Detail string
}

func (e GateError) Error() string { return e.Detail }

// PermitStatus is the permit lifecycle state (FR-44.19).
type PermitStatus string

const (
	PermitRequested PermitStatus = "REQUESTED"
	PermitApproved  PermitStatus = "APPROVED"
	PermitIssued    PermitStatus = "ISSUED"
	PermitSuspended PermitStatus = "SUSPENDED"
	PermitClosed    PermitStatus = "CLOSED"
	PermitCanceled  PermitStatus = "CANCELED"
)

var permitTransitions = map[PermitStatus][]PermitStatus{
	PermitRequested: {PermitApproved, PermitCanceled},
	PermitApproved:  {PermitIssued, PermitCanceled},
	PermitIssued:    {PermitSuspended, PermitClosed},
	PermitSuspended: {PermitIssued, PermitClosed},
}

// CanPermitTransition reports whether a permit may move from → to.
func CanPermitTransition(from, to PermitStatus) bool {
	for _, next := range permitTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Lockable reports whether isolation locks may be applied to a permit in s.
func (s PermitStatus) Lockable() bool {
	return s == PermitApproved || s == PermitIssued || s == PermitSuspended
}

// Active reports whether a permit still governs work (not closed or canceled).
func (s PermitStatus) Active() bool { return s != PermitClosed && s != PermitCanceled }

// CheckApprover enforces the separation of duties: approver ≠ requester (BR-44.11).
func CheckApprover(requester, approver string) error {
	if requester == approver {
		return GateError{CodeSelfApproval, "approver must differ from requester"}
	}
	return nil
}

// CheckIssuer enforces the second separation of duties on an isolation permit
// (8.44.17): the issuer cannot be the only holder of the verified locks, so at
// least one other person has put a personal lock on the isolation.
func CheckIssuer(issuer string, lockHolders []string) error {
	for _, h := range lockHolders {
		if h != issuer {
			return nil
		}
	}
	if len(lockHolders) == 0 {
		return nil
	}
	return GateError{CodeSelfApproval, "the issuer cannot be the only holder of the isolation locks"}
}

// CheckIsolation is the ISSUE rule for an isolation permit (BR-44.11): the
// asset must have isolation points (permit checks fail closed, 8.44.19), and
// every one must carry a verified lock on the permit.
func CheckIsolation(points, unlocked int) error {
	if points == 0 {
		return GateError{CodeIsolationOpen, "the asset has no isolation points; define them before issuing an isolation permit"}
	}
	if unlocked > 0 {
		return GateError{CodeIsolationOpen, fmt.Sprintf("%d isolation points lack a verified lock", unlocked)}
	}
	return nil
}

// Expired reports whether an ISSUED permit is past its validity at now and is
// due for automatic suspension (BR-44.11, EAM-PMT-004).
func Expired(status PermitStatus, validTo, now time.Time) bool {
	return status == PermitIssued && !now.Before(validTo)
}

// ValidPermitWindow checks the validity period against the permit type maximum.
func ValidPermitWindow(from, to time.Time, maxHours int) error {
	if !to.After(from) {
		return errors.New("validTo must be after validFrom")
	}
	if to.Sub(from) > time.Duration(maxHours)*time.Hour {
		return fmt.Errorf("permit validity exceeds %d hours for this permit type", maxHours)
	}
	return nil
}

// PermitValidAt reports whether the window covers instant at.
func PermitValidAt(from, to, at time.Time) bool { return !at.Before(from) && at.Before(to) }

// PermitRequirement is what the work-order gate knows about one permit type that
// the job needs: the governing permit (latest active one of that type) and the
// isolation points of the asset still lacking a verified lock on it.
type PermitRequirement struct {
	TypeCode          string
	RequiresIsolation bool
	Found             bool
	Status            PermitStatus
	ValidFrom         time.Time
	ValidTo           time.Time
	Points            int
	UnlockedPoints    int
}

// CheckPermitGate enforces BR-44.11 before a job may start: a permit of the type
// must exist and be ISSUED and valid now; for isolation permits every isolation
// point must carry a verified lock first (checked before the status so the
// lock gap is reported as the blocker).
func CheckPermitGate(r PermitRequirement, now time.Time) error {
	if !r.Found {
		return GateError{CodePermitMissing, fmt.Sprintf("job requires an ISSUED %s permit", r.TypeCode)}
	}
	if r.RequiresIsolation {
		if err := CheckIsolation(r.Points, r.UnlockedPoints); err != nil {
			return err
		}
	}
	if r.Status != PermitIssued || !PermitValidAt(r.ValidFrom, r.ValidTo, now) {
		return GateError{CodePermitMissing, fmt.Sprintf("job requires an ISSUED %s permit valid now", r.TypeCode)}
	}
	return nil
}

// LockState is the state of one personal lock (BR-44.11).
type LockState string

const (
	LockApplied  LockState = "APPLIED"
	LockVerified LockState = "VERIFIED"
	LockRemoved  LockState = "REMOVED"
)

// LockAction is a request on a personal lock.
type LockAction string

const (
	LockApply  LockAction = "APPLY"
	LockVerify LockAction = "VERIFY"
	LockRemove LockAction = "REMOVE"
)

// NextLockState applies action to a lock in state current. current is the
// empty string when no lock exists yet (only APPLY is valid then). Verify and
// remove repeat as no-ops on a lock already in the target state.
func NextLockState(current LockState, action LockAction) (LockState, error) {
	switch {
	case current == "" && action == LockApply:
		return LockApplied, nil
	case current == LockApplied && action == LockVerify, current == LockVerified && action == LockVerify:
		return LockVerified, nil
	case (current == LockApplied || current == LockVerified) && action == LockRemove:
		return LockRemoved, nil
	case current == LockRemoved && action == LockRemove:
		return LockRemoved, nil
	}
	return current, GateError{CodePermitState, fmt.Sprintf("lock action %s not allowed on a %s lock", action, current)}
}

// LocksOpen reports whether a permit can still close: no lock may remain applied
// or verified (BR-44.11, EAM-PMT-003).
func LocksOpen(applied int) error {
	if applied > 0 {
		return GateError{CodeLocksOpen, fmt.Sprintf("permit cannot close: %d locks still applied", applied)}
	}
	return nil
}
