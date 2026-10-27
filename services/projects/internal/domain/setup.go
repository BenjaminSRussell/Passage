package domain

import (
	"regexp"
	"time"
	"unicode/utf8"
)

// Service-specific codes (not in §8.41.18) raised by the setup and plan rules of slice 2.
const (
	// CodePlanKind: a second WORKING or BASELINE plan version, or a copy from a
	// version that cannot be its source.
	CodePlanKind = "PROJ-PLAN-001"
	// CodePlanLocked: the plan version is locked (a BASELINE is immutable, FR-41.2).
	CodePlanLocked = "PROJ-PLAN-002"
)

var groupIDPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,9}$`)

// ProjectGroup groups projects for posting profiles and defaults (FR-41.1, §8.41.4).
type ProjectGroup struct {
	ID                 string      `json:"id"`
	Name               string      `json:"name"`
	DefaultProjectType ProjectType `json:"defaultProjectType,omitempty"`
	Version            int         `json:"version"`
	CreatedAt          time.Time   `json:"createdAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}

// ValidGroupID reports whether id is a project group code: 1 to 10 capitals,
// digits or hyphens. The posting-profile wildcard "*" is never a group id.
func ValidGroupID(id string) bool { return groupIDPattern.MatchString(id) }

// ValidateGroup returns field-level errors for a project group (empty when valid).
func ValidateGroup(g ProjectGroup) map[string]string {
	fields := map[string]string{}
	if !ValidGroupID(g.ID) {
		fields["id"] = "must be 1 to 10 characters of A-Z, 0-9 or hyphen"
	}
	if n := utf8.RuneCountInString(g.Name); n == 0 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if g.DefaultProjectType != "" && !ValidProjectType(g.DefaultProjectType) {
		fields["defaultProjectType"] = "must be a known project type"
	}
	return fields
}

// Line properties of a project transaction (FR-41.4).
const (
	LineChargeable    = "CHARGEABLE"
	LineNonChargeable = "NON_CHARGEABLE"
)

// ValidateProfile returns field-level errors for a posting profile (empty when
// valid). Only the three keys Resolve reads may be stored (FR-41.5): a concrete
// (group, category, property), the group fallback (group, *, *) and the
// catch-all (*, *, *). Any other mix of wildcards could never be resolved.
// Every account is required.
func ValidateProfile(p PostingProfile) map[string]string {
	fields := map[string]string{}
	k := p.Key
	if k.ProjectGroup != Wildcard && !ValidGroupID(k.ProjectGroup) {
		fields["projectGroup"] = "must be a project group id or *"
	}
	if n := utf8.RuneCountInString(k.Category); n == 0 || n > 20 {
		fields["category"] = "must be 1 to 20 characters or *"
	}
	switch k.LineProperty {
	case LineChargeable, LineNonChargeable, Wildcard:
	default:
		fields["lineProperty"] = "must be CHARGEABLE, NON_CHARGEABLE or *"
	}
	if len(fields) == 0 {
		switch {
		case (k.Category == Wildcard) != (k.LineProperty == Wildcard):
			fields["lineProperty"] = "category and line property are either both * (a group fallback) or both concrete"
		case k.ProjectGroup == Wildcard && k.Category != Wildcard:
			fields["category"] = "must be * when the project group is * (the catch-all row)"
		}
	}
	accounts := []struct{ name, value string }{
		{"costAccount", p.CostAccount},
		{"wipAccount", p.WIPAccount},
		{"accruedRevenueAccount", p.AccruedRevenueAccount},
		{"invoicedRevenueAccount", p.InvoicedRevenueAccount},
		{"onAccountAccount", p.OnAccountAccount},
	}
	for _, a := range accounts {
		if n := utf8.RuneCountInString(a.value); n == 0 || n > 40 {
			fields[a.name] = "must be 1 to 40 characters"
		}
	}
	return fields
}

// PlanKind is the kind of a plan version (FR-41.2, FR-41.17).
type PlanKind string

const (
	KindBaseline   PlanKind = "BASELINE"
	KindWorking    PlanKind = "WORKING"
	KindSimulation PlanKind = "SIMULATION"
)

// ValidPlanKind reports whether k is one of the three plan kinds.
func ValidPlanKind(k PlanKind) bool {
	switch k {
	case KindBaseline, KindWorking, KindSimulation:
		return true
	}
	return false
}

// PlanVersion is one WBS plan of a project. Sequence orders the versions of a
// project. Version is the optimistic-concurrency counter of its WBS.
type PlanVersion struct {
	ID                  string    `json:"id"`
	ProjectID           string    `json:"projectId"`
	Kind                PlanKind  `json:"kind"`
	Sequence            int       `json:"sequence"`
	CopiedFromVersionID *string   `json:"copiedFromVersionId,omitempty"`
	Locked              bool      `json:"locked"`
	Version             int       `json:"version"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// CheckNewPlan applies the FR-41.2 / FR-41.17 plan-version rules to a new
// version of kind, copied from source (nil for an empty plan), given the kinds
// the project already has:
//   - one WORKING and one BASELINE per project;
//   - a BASELINE is a copy of the WORKING plan and is locked on creation (the
//     copy is the approval; a baseline is immutable after it);
//   - a SIMULATION is a copy of the WORKING or the BASELINE plan;
//   - a WORKING plan starts empty or copies any version.
//
// It returns whether the new version is locked, or PROJ-PLAN-001.
func CheckNewPlan(kind PlanKind, source *PlanVersion, existing []PlanKind) (bool, error) {
	if kind == KindWorking || kind == KindBaseline {
		for _, k := range existing {
			if k == kind {
				return false, rule(CodePlanKind, "the project already has a %s plan version", kind)
			}
		}
	}
	switch kind {
	case KindBaseline:
		if source == nil || source.Kind != KindWorking {
			return false, rule(CodePlanKind, "a BASELINE is created by copying the WORKING plan version")
		}
		return true, nil
	case KindSimulation:
		if source == nil || source.Kind == KindSimulation {
			return false, rule(CodePlanKind, "a SIMULATION is created by copying the WORKING or BASELINE plan version")
		}
	}
	return false, nil
}
