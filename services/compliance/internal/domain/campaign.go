package domain

import (
	"fmt"
	"sort"
	"time"
)

// Access certification codes (FR-48.6, BR-48.9).
const (
	CodeCampaignWindow   = "SOD-1120" // window outside the allowed days or start missing
	CodeCampaignSelf     = "SOD-1121" // a manager cannot certify their own roles
	CodeCampaignDecision = "SOD-1122" // entry incomplete, duplicated or decision not allowed
)

const (
	defaultCertWindowDays = 14
	minCertWindowDays     = 3
	maxCertWindowDays     = 60
)

// CertDecision is a manager's decision on one role of one direct report.
type CertDecision string

const (
	CertCertify CertDecision = "CERTIFY"
	CertRevoke  CertDecision = "REVOKE"
	CertPending CertDecision = "PENDING"
)

// CampaignEntry is one manager-user-role line of a certification campaign.
type CampaignEntry struct {
	ManagerID string       `json:"managerId"`
	UserID    string       `json:"userId"`
	RoleName  string       `json:"roleName"`
	Decision  CertDecision `json:"decision,omitempty"`
}

// PlanCampaign returns the close instant of a campaign started at startsAt.
// windowDays 0 means the default of 14; otherwise it must be within 3..60.
func PlanCampaign(startsAt time.Time, windowDays int) (time.Time, error) {
	if startsAt.IsZero() {
		return time.Time{}, &Error{Code: CodeCampaignWindow, Detail: "startsAt is required"}
	}
	if windowDays == 0 {
		windowDays = defaultCertWindowDays
	}
	if windowDays < minCertWindowDays || windowDays > maxCertWindowDays {
		return time.Time{}, &Error{Code: CodeCampaignWindow, Detail: fmt.Sprintf("certification window %d days is outside %d..%d", windowDays, minCertWindowDays, maxCertWindowDays)}
	}
	return startsAt.UTC().AddDate(0, 0, windowDays), nil
}

// CampaignCloseInput is the state of a campaign at close.
type CampaignCloseInput struct {
	Entries    []CampaignEntry `json:"entries"`
	AutoRevoke bool            `json:"autoRevoke"`
	ClosesAt   time.Time       `json:"closesAt"`
	Now        time.Time       `json:"-"`
}

// CampaignProgress is the KPI view: % certified, revoked, overdue by manager.
type CampaignProgress struct {
	Total            int            `json:"total"`
	Certified        int            `json:"certified"`
	Revoked          int            `json:"revoked"`
	Pending          int            `json:"pending"`
	PercentCertified int            `json:"percentCertified"`
	OverdueByManager map[string]int `json:"overdueByManager"`
}

// CampaignCloseResult lists the revocations and flagged roles the close produces.
// Before the window ends Closed is false and the result is the progress view:
// Revoke holds only the managers' explicit revocations and nothing is flagged.
type CampaignCloseResult struct {
	Closed   bool             `json:"closed"`
	Revoke   []CampaignEntry  `json:"revoke"`
	Flagged  []CampaignEntry  `json:"flagged"`
	Progress CampaignProgress `json:"progress"`
}

// CloseCampaign applies BR-48.9 once the window has ended (now after closesAt).
// Explicit REVOKE decisions are always revoked. An uncertified (PENDING) role is
// revoked only when autoRevoke is set; otherwise it is flagged. Either way its
// manager counts as overdue. Before the window ends a PENDING role is only
// counted: the manager still has time to decide.
func CloseCampaign(in CampaignCloseInput) (CampaignCloseResult, error) {
	if in.ClosesAt.IsZero() {
		return CampaignCloseResult{}, &Error{Code: CodeCampaignWindow, Detail: "closesAt is required"}
	}
	res := CampaignCloseResult{
		Revoke:   []CampaignEntry{},
		Flagged:  []CampaignEntry{},
		Progress: CampaignProgress{OverdueByManager: map[string]int{}},
	}
	ended := !in.Now.Before(in.ClosesAt)
	res.Closed = ended
	seen := map[string]bool{}
	for _, e := range in.Entries {
		if e.ManagerID == "" || e.UserID == "" || e.RoleName == "" {
			return CampaignCloseResult{}, &Error{Code: CodeCampaignDecision, Detail: "every entry needs a manager, a user and a role"}
		}
		if e.ManagerID == e.UserID {
			return CampaignCloseResult{}, &Error{Code: CodeCampaignSelf, Detail: "a manager cannot certify their own roles"}
		}
		key := e.UserID + "|" + e.RoleName
		if seen[key] {
			return CampaignCloseResult{}, &Error{Code: CodeCampaignDecision, Detail: fmt.Sprintf("role %s for user %s appears twice", e.RoleName, e.UserID)}
		}
		seen[key] = true
		switch e.Decision {
		case "":
			e.Decision = CertPending
		case CertCertify, CertRevoke, CertPending:
		default:
			return CampaignCloseResult{}, &Error{Code: CodeCampaignDecision, Detail: fmt.Sprintf("decision %q must be CERTIFY, REVOKE or PENDING", e.Decision)}
		}
		res.Progress.Total++
		switch e.Decision {
		case CertCertify:
			res.Progress.Certified++
		case CertRevoke:
			res.Progress.Revoked++
			res.Revoke = append(res.Revoke, e)
		case CertPending:
			if !ended {
				res.Progress.Pending++
				continue
			}
			res.Progress.OverdueByManager[e.ManagerID]++
			if in.AutoRevoke {
				e.Decision = CertRevoke
				res.Progress.Revoked++
				res.Revoke = append(res.Revoke, e)
				continue
			}
			res.Progress.Pending++
			res.Flagged = append(res.Flagged, e)
		}
	}
	if res.Progress.Total > 0 {
		res.Progress.PercentCertified = res.Progress.Certified * 100 / res.Progress.Total
	}
	sortCampaignEntries(res.Revoke)
	sortCampaignEntries(res.Flagged)
	return res, nil
}

func sortCampaignEntries(es []CampaignEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.ManagerID != b.ManagerID {
			return a.ManagerID < b.ManagerID
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		return a.RoleName < b.RoleName
	})
}
