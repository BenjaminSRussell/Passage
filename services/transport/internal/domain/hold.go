package domain

import (
	"fmt"
	"strings"
	"time"
)

// Screening hold states (BR-34.17). A hold opens when a load screen decides HOLD
// and closes only when a transport.screening-hold.release user releases it with a reason;
// the hook never clears it on its own.
const (
	HoldOpen     = "OPEN"
	HoldReleased = "RELEASED"
)

// CodeHoldState is slice-local (the catalog leaves TMS-0122 unassigned): the hold
// is already released by someone else or for another reason.
const CodeHoldState = "TMS-0122"

// MaxReleaseReasonLength bounds the trade-compliance release reason.
const MaxReleaseReasonLength = 500

// ScreeningHold is a load held by screening, with the party results that caused
// it and, once released, who released it, when and why.
type ScreeningHold struct {
	ID            string           `json:"id"`
	LoadID        string           `json:"loadId"`
	Status        string           `json:"status"`
	Parties       []PartyScreening `json:"parties"`
	CreatedBy     string           `json:"createdBy"`
	CreatedAt     time.Time        `json:"createdAt"`
	ReleasedBy    string           `json:"releasedBy,omitempty"`
	ReleaseReason string           `json:"releaseReason,omitempty"`
	ReleasedAt    *time.Time       `json:"releasedAt,omitempty"`
	Version       int              `json:"version"`
}

// ValidateHoldRelease reports field errors for a release reason.
func ValidateHoldRelease(reason string) map[string]string {
	f := map[string]string{}
	if n := len(strings.TrimSpace(reason)); n == 0 || n > MaxReleaseReasonLength {
		f["reason"] = fmt.Sprintf("is required, up to %d characters: say why the parties may be released", MaxReleaseReasonLength)
	}
	return f
}

// BlockedParty returns the first party the provider BLOCKED, or "" when none is.
func (h ScreeningHold) BlockedParty() string {
	for _, p := range h.Parties {
		if p.Status == ScreenBlocked {
			return p.Role + " " + p.Name
		}
	}
	return ""
}

// Release closes an open hold for userID with reason at at. POTENTIAL_MATCH and
// SCREENING_PENDING holds can be released; a BLOCKED party stays held until the
// screening list changes (BR-34.17), so a hold with one is refused with TMS-0133.
// Releasing a hold that the same user already released for the same reason
// returns it unchanged, so a retried release is safe; any other second release
// is TMS-0122.
func (h ScreeningHold) Release(userID, reason string, at time.Time) (ScreeningHold, bool, error) {
	reason = strings.TrimSpace(reason)
	if h.Status == HoldReleased {
		if h.ReleasedBy == userID && h.ReleaseReason == reason {
			return h, false, nil
		}
		return ScreeningHold{}, false, &Error{Code: CodeHoldState, Detail: "screening hold is already released by " + h.ReleasedBy}
	}
	if h.Status != HoldOpen {
		return ScreeningHold{}, false, &Error{Code: CodeHoldState, Detail: "screening hold is " + h.Status}
	}
	if party := h.BlockedParty(); party != "" {
		return ScreeningHold{}, false, &Error{Code: CodeScreeningHold,
			Detail: "party " + party + " is BLOCKED by the screening list; the hold stays until the list changes"}
	}
	h.Status = HoldReleased
	h.ReleasedBy = userID
	h.ReleaseReason = reason
	released := at.UTC()
	h.ReleasedAt = &released
	h.Version++
	return h, true, nil
}
