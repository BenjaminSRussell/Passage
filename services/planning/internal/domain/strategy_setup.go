package domain

import "slices"

// StrategySpec is the setup of one planning strategy (FR-22.19, data model
// PlanningStrategy): how its items treat forecast against sales demand.
// Consumption mode and windows apply to assemble-to-order-final; a stored
// strategy of any other code keeps them for reference.
type StrategySpec struct {
	Code            string
	ConsumptionMode string
	BackwardDays    int
	ForwardDays     int
	UsesMarking     bool
	SegregatedStock bool
}

const strategyMaxDays = 365

var consumptionModes = []string{ConsumptionBackward, ConsumptionForward, ConsumptionBackwardForward}

// StrategyCodes lists the six FR-22.19 strategies in the order the setup shows them.
func StrategyCodes() []string { return slices.Clone(strategyCodes) }

// DefaultStrategy returns the PRD defaults of a strategy: backward consumption
// over 30 days and no forward window, marking as FR-22.19 notes give it for mto
// and plan-without-final-assembly, and no segregated stock.
func DefaultStrategy(code string) StrategySpec {
	return StrategySpec{Code: code, ConsumptionMode: ConsumptionBackward, BackwardDays: 30, UsesMarking: UsesMarking(code)}
}

// Validate returns one message per invalid field, keyed by the wire name of the
// field. An empty result means the spec is valid.
func (s StrategySpec) Validate() map[string]string {
	errs := map[string]string{}
	if !ValidStrategy(s.Code) {
		errs["code"] = "must be mts-net, mts-gross, mto, assemble-to-order-final, plan-without-final-assembly or subassembly"
	}
	if !slices.Contains(consumptionModes, s.ConsumptionMode) {
		errs["consumptionMode"] = "must be backward, forward or backward-forward"
	}
	if s.BackwardDays < 0 || s.BackwardDays > strategyMaxDays {
		errs["backwardDays"] = "must be 0 to 365"
	}
	if s.ForwardDays < 0 || s.ForwardDays > strategyMaxDays {
		errs["forwardDays"] = "must be 0 to 365"
	}
	if s.ConsumptionMode == ConsumptionBackwardForward && s.BackwardDays == 0 && s.ForwardDays == 0 {
		errs["consumptionMode"] = "backward-forward needs a backward or forward window of at least 1 day"
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}
