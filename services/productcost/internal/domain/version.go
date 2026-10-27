package domain

import "fmt"

// Costing version types (FR-39.3, costing.versionType).
const (
	VersionStandard   = "Standard"
	VersionPlanned    = "Planned"
	VersionSimulation = "Simulation"
	VersionConversion = "Conversion"
)

// Costing version statuses (BR-39.2).
const (
	VersionOpen    = "Open"
	VersionBlocked = "Blocked"
	VersionActive  = "Active"
	VersionClosed  = "Closed"
)

// Fallback principles (costing.fallback, BR-39.4).
const (
	FallbackNone      = "None"
	FallbackActive    = "Active"
	FallbackSpecified = "SpecifiedVersion"
)

// Version lifecycle actions.
const (
	ActionBlock    = "block"
	ActionUnblock  = "unblock"
	ActionActivate = "activate"
	ActionClose    = "close"
)

// Codes for version lifecycle errors. CST-0711 is from the wave-39 catalog;
// CST-0715 and CST-0716 extend it for this slice (see README).
const (
	CodeSimulationActivation = "CST-0711"
	CodeInvalidTransition    = "CST-0715"
	CodeFallbackVersion      = "CST-0716"
)

// Version is the state of one costing version that the lifecycle rules read.
type Version struct {
	ID                string   `json:"id"`
	Type              string   `json:"type"`
	Status            string   `json:"status"`
	Fallback          string   `json:"fallback"`
	FallbackVersionID string   `json:"fallbackVersionId,omitempty"`
	SiteIDs           []string `json:"siteIds,omitempty"`
}

// coded builds a CodedError (see variance.go) with a formatted detail.
func coded(code, format string, args ...any) error {
	return &CodedError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// Validate checks that the version's enumerations are known, that a Simulation
// version never carries Active status, and that SpecifiedVersion names a
// fallback version.
func (v Version) Validate() error {
	switch v.Type {
	case VersionStandard, VersionPlanned, VersionSimulation, VersionConversion:
	default:
		return fmt.Errorf("version type %q is unknown", v.Type)
	}
	switch v.Status {
	case VersionOpen, VersionBlocked, VersionActive, VersionClosed:
	default:
		return fmt.Errorf("version status %q is unknown", v.Status)
	}
	switch v.Fallback {
	case FallbackNone, FallbackActive:
		if v.FallbackVersionID != "" {
			return fmt.Errorf("fallbackVersionId is only valid with fallback %s", FallbackSpecified)
		}
	case FallbackSpecified:
		if v.FallbackVersionID == "" {
			return coded(CodeFallbackVersion, "fallback %s needs a fallbackVersionId", FallbackSpecified)
		}
		if v.FallbackVersionID == v.ID {
			return coded(CodeFallbackVersion, "a version cannot fall back to itself")
		}
	default:
		return fmt.Errorf("fallback %q is unknown", v.Fallback)
	}
	if v.Type == VersionSimulation && v.Status == VersionActive {
		return coded(CodeSimulationActivation, "simulation version %s cannot be Active", v.ID)
	}
	return nil
}

// Transition applies a lifecycle action (BR-39.2): Open ⇄ Blocked, then
// Open or Blocked → Active, then Active → Closed. Simulation versions cannot
// activate (CST-0711). Any other move is CST-0715.
func Transition(v Version, action string) (Version, error) {
	next := v
	switch action {
	case ActionBlock:
		if v.Status != VersionOpen {
			return v, transitionErr(v, action)
		}
		next.Status = VersionBlocked
	case ActionUnblock:
		if v.Status != VersionBlocked {
			return v, transitionErr(v, action)
		}
		next.Status = VersionOpen
	case ActionActivate:
		if v.Type == VersionSimulation {
			return v, coded(CodeSimulationActivation, "simulation version %s cannot be activated; copy it to a Standard version", v.ID)
		}
		if v.Status != VersionOpen && v.Status != VersionBlocked {
			return v, transitionErr(v, action)
		}
		next.Status = VersionActive
	case ActionClose:
		if v.Status != VersionActive {
			return v, transitionErr(v, action)
		}
		next.Status = VersionClosed
	default:
		return v, fmt.Errorf("action %q is unknown", action)
	}
	return next, nil
}

func transitionErr(v Version, action string) error {
	return coded(CodeInvalidTransition, "action %s is not allowed from status %s", action, v.Status)
}
