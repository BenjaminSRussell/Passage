// Package domain holds the ops control plane's rules: the region state machine (BR-50.1), the failover plan (FR-50.5)
// and the capacity model (FR-50.9). It does no I/O.
package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Role is a region's place in the active/passive pair.
type Role string

const (
	Primary Role = "PRIMARY"
	Passive Role = "PASSIVE"
	Fenced  Role = "FENCED"
)

// Problem codes of the PRD's OPS family (9.50 error catalogue); 1709 and up are this service's additions.
const (
	CodeLagExceeded       = "OPS-1701"
	CodeApprovalRequired  = "OPS-1702"
	CodeTransitionInvalid = "OPS-1709"
	CodeRegionUnknown     = "OPS-1710"
	CodeResyncUnverified  = "OPS-1711"
	CodeNoPrimary         = "OPS-1712"
)

// Region is one deployed region and its replication state.
type Region struct {
	Name       string `json:"name"`
	Role       Role   `json:"role"`
	LagSeconds int    `json:"lagSeconds"`
	Version    int64  `json:"version"`
}

// Error is a rule violation with its problem code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ValidRole reports whether r is one of the three roles.
func ValidRole(r Role) bool { return r == Primary || r == Passive || r == Fenced }

// ValidateTopology checks a set of regions: known roles, unique names and never two PRIMARY (BR-50.1).
func ValidateTopology(regions []Region) error {
	seen := map[string]bool{}
	primaries := 0
	for _, r := range regions {
		if r.Name == "" || seen[r.Name] {
			return fail(CodeRegionUnknown, "region names must be unique and not empty")
		}
		seen[r.Name] = true
		if !ValidRole(r.Role) {
			return fail(CodeTransitionInvalid, "region %s has unknown role %q", r.Name, r.Role)
		}
		if r.Role == Primary {
			primaries++
		}
	}
	if primaries > 1 {
		return fail(CodeTransitionInvalid, "two regions are PRIMARY")
	}
	return nil
}

// FailoverRequest is the input of PlanFailover. Approver is the second PlatformAdmin; Caller is the requester.
type FailoverRequest struct {
	To             string
	AcceptDataLoss bool
	Caller         string
	Approver       string
	MaxLagSeconds  int
}

// Step is one ordered action of the failover (FR-50.5). The control plane records the plan and the state change;
// executing the infrastructure steps belongs to the runners that read the plan.
type Step struct {
	Order  int    `json:"order"`
	Action string `json:"action"`
	Region string `json:"region"`
}

// Plan is the validated outcome: the region fenced, the one promoted, the ordered steps and whether data loss was accepted.
type Plan struct {
	From           string `json:"from"`
	To             string `json:"to"`
	AcceptDataLoss bool   `json:"acceptDataLoss"`
	LagSeconds     int    `json:"lagSeconds"`
	Steps          []Step `json:"steps"`
}

// PlanFailover validates a failover to req.To: the target must be PASSIVE, exactly one PRIMARY must exist to fence, and a
// target whose lag exceeds the limit needs acceptDataLoss and a second approver other than the caller (OPS-1701, OPS-1702).
func PlanFailover(regions []Region, req FailoverRequest) (Plan, error) {
	if err := ValidateTopology(regions); err != nil {
		return Plan{}, err
	}
	var from, to *Region
	for i := range regions {
		r := &regions[i]
		if r.Role == Primary {
			from = r
		}
		if r.Name == req.To {
			to = r
		}
	}
	if to == nil {
		return Plan{}, fail(CodeRegionUnknown, "region %s is not registered", req.To)
	}
	if from == nil {
		return Plan{}, fail(CodeNoPrimary, "no region is PRIMARY; nothing to fence")
	}
	if to.Role != Passive {
		return Plan{}, fail(CodeTransitionInvalid, "region %s is %s; only a PASSIVE region can be promoted (a FENCED region rejoins as PASSIVE after a verified re-sync)", to.Name, to.Role)
	}
	lossy := to.LagSeconds > req.MaxLagSeconds
	if lossy {
		if !req.AcceptDataLoss {
			return Plan{}, fail(CodeLagExceeded, "target region lag %d s exceeds %d s. Use acceptDataLoss with a second approver", to.LagSeconds, req.MaxLagSeconds)
		}
		if req.Approver == "" || req.Approver == req.Caller {
			return Plan{}, fail(CodeApprovalRequired, "failover needs a second PlatformAdmin, other than the requester, as approvedBy")
		}
	}
	plan := Plan{From: from.Name, To: to.Name, AcceptDataLoss: lossy, LagSeconds: to.LagSeconds}
	for i, a := range []struct{ action, region string }{
		{"fence-old-primary", from.Name}, {"promote-cnpg", to.Name}, {"switch-mirrormaker", to.Name},
		{"activate-temporal", to.Name}, {"flip-dns", to.Name}, {"verify-ledger-hash-chain", to.Name},
	} {
		plan.Steps = append(plan.Steps, Step{Order: i + 1, Action: a.action, Region: a.region})
	}
	return plan, nil
}

// Apply returns the regions after the plan: the old primary FENCED, the target PRIMARY.
func (p Plan) Apply(regions []Region) []Region {
	out := append([]Region(nil), regions...)
	for i := range out {
		switch out[i].Name {
		case p.From:
			out[i].Role = Fenced
		case p.To:
			out[i].Role = Primary
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Rejoin moves a FENCED region back to PASSIVE, only after its re-sync was verified (BR-50.1).
func Rejoin(r Region, resyncVerified bool) (Region, error) {
	if r.Role != Fenced {
		return r, fail(CodeTransitionInvalid, "region %s is %s; only a FENCED region rejoins", r.Name, r.Role)
	}
	if !resyncVerified {
		return r, fail(CodeResyncUnverified, "region %s may rejoin as PASSIVE only after its re-sync is verified", r.Name)
	}
	r.Role = Passive
	return r, nil
}

// ParseRegions reads "name=ROLE,name=ROLE" (the OPS_REGIONS setting) into a validated topology.
func ParseRegions(s string) ([]Region, error) {
	var out []Region
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		name, role, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(role) == "" {
			return nil, fmt.Errorf("region %q must be name=ROLE", part)
		}
		out = append(out, Region{Name: strings.TrimSpace(name), Role: Role(strings.TrimSpace(role))})
	}
	if err := ValidateTopology(out); err != nil {
		return nil, err
	}
	return out, nil
}
