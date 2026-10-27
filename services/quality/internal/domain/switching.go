package domain

// SwitchState tracks Z1.4 severity for one (item, vendor, test group) (BR-33.2).
// Recent holds the rejection flags of the last five lots while Normal; Streak
// counts consecutive accepted lots in the current severity.
type SwitchState struct {
	Severity Severity
	Recent   []bool
	Streak   int
}

// NewSwitchState starts at normal inspection with no history.
func NewSwitchState() SwitchState { return SwitchState{Severity: SeverityNormal} }

// Apply folds one lot outcome (rejected = true) into the state. Switching to
// reduced needs enabled = true.
func (s SwitchState) Apply(rejected, enabled bool) SwitchState {
	switch s.Severity {
	case SeverityTightened:
		if rejected {
			s.Streak = 0
			return s
		}
		s.Streak++
		if s.Streak >= 5 {
			return NewSwitchState()
		}
		return s
	case SeverityReduced:
		if rejected {
			return NewSwitchState()
		}
		s.Streak++
		return s
	}
	recent := append(append([]bool{}, s.Recent...), rejected)
	if len(recent) > 5 {
		recent = recent[len(recent)-5:]
	}
	next := SwitchState{Severity: SeverityNormal, Recent: recent}
	if rejected {
		next.Streak = 0
	} else {
		next.Streak = s.Streak + 1
	}
	rejections := 0
	for _, r := range recent {
		if r {
			rejections++
		}
	}
	if rejections >= 2 {
		return SwitchState{Severity: SeverityTightened}
	}
	if enabled && next.Streak >= 10 {
		return SwitchState{Severity: SeverityReduced}
	}
	return next
}

// ReplaySwitching folds a history of outcomes (oldest first) from normal inspection.
func ReplaySwitching(enabled bool, rejected []bool) SwitchState {
	s := NewSwitchState()
	for _, r := range rejected {
		s = s.Apply(r, enabled)
	}
	return s
}
