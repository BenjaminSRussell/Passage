package domain

// Scheduling parameters (PRD 4.23.13 site parameters): the defaults runs and
// moves take when the request leaves a choice out. A tenant that never set
// them gets DefaultParameters at version 1.

// Parameter limits (PRD 4.23.13 allowed values).
const (
	MaxDragSnapMinutes   = 60
	MaxPropagationOrders = 5000
	MaxPropagationLevels = 20
)

// Parameters are one tenant's scheduling defaults.
type Parameters struct {
	DefaultMode       string
	DefaultDirection  string
	DefaultFinite     bool
	DragSnapMinutes   int
	PropagationOrders int
	PropagationLevels int
}

// DefaultParameters are the values before anything is changed.
func DefaultParameters() Parameters {
	return Parameters{DefaultMode: "jobs", DefaultDirection: "forward", DefaultFinite: true, DragSnapMinutes: 5,
		PropagationOrders: DefaultPropagationOrders, PropagationLevels: DefaultPropagationLevels}
}

// Limit is the propagation limit the parameters set for a moved order's walk.
func (p Parameters) Limit() PropagationLimit {
	return PropagationLimit{Orders: p.PropagationOrders, Levels: p.PropagationLevels}
}

// Validate returns a message per invalid field (empty when valid).
func (p Parameters) Validate() map[string]string {
	out := map[string]string{}
	if p.DefaultMode != "jobs" && p.DefaultMode != "operations" {
		out["defaultMode"] = "must be jobs or operations"
	}
	if p.DefaultDirection != "forward" && p.DefaultDirection != "backward" {
		out["defaultDirection"] = "must be forward or backward"
	}
	if p.DragSnapMinutes < 1 || p.DragSnapMinutes > MaxDragSnapMinutes {
		out["dragSnapMinutes"] = "must be 1 to 60 minutes"
	}
	if p.PropagationOrders < 1 || p.PropagationOrders > MaxPropagationOrders {
		out["propagationOrders"] = "must be 1 to 5000"
	}
	if p.PropagationLevels < 1 || p.PropagationLevels > MaxPropagationLevels {
		out["propagationLevels"] = "must be 1 to 20"
	}
	return out
}
