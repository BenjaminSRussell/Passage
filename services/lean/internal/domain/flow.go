package domain

// Activity is one FlowActivity of a production flow, reduced to the fields the
// connectivity rule needs.
type Activity struct {
	ID               string
	Name             string
	Kind             string
	OutputLocationID string
	PredecessorIDs   []string
}

// ValidActivityKind reports whether k is process, transfer or service (FR-24.8).
func ValidActivityKind(k string) bool {
	return k == "process" || k == "transfer" || k == "service"
}

// MaxActivities bounds one production flow.
const MaxActivities = 100

// ValidateFlow checks FR-24.8 connectivity: at least one activity, every
// process activity has an output location (LEAN-0004), every predecessor names
// another activity of the same flow, the predecessor graph has no cycle and
// every kind is known.
func ValidateFlow(acts []Activity) error {
	if len(acts) == 0 || len(acts) > MaxActivities {
		return errorf(CodeInvalid, "a flow needs 1 to %d activities", MaxActivities)
	}
	known := make(map[string]bool, len(acts))
	for _, a := range acts {
		if known[a.ID] {
			return errorf(CodeInvalid, "activity id %s is used twice", a.ID)
		}
		known[a.ID] = true
	}
	for _, a := range acts {
		if !ValidActivityKind(a.Kind) {
			return errorf(CodeInvalid, "activity %s has unknown kind %q", a.Name, a.Kind)
		}
		if a.Kind == "process" && a.OutputLocationID == "" {
			return errorf(CodeNoOutput, "flow activity %s has no output location", a.Name)
		}
		for _, p := range a.PredecessorIDs {
			if !known[p] || p == a.ID {
				return errorf(CodeInvalid, "activity %s names unknown predecessor %s", a.Name, p)
			}
		}
	}
	if name := cycle(acts); name != "" {
		return errorf(CodeInvalid, "activity %s is its own predecessor through a cycle", name)
	}
	return nil
}

// cycle returns the name of an activity on a predecessor cycle, or "".
func cycle(acts []Activity) string {
	byID := make(map[string]Activity, len(acts))
	for _, a := range acts {
		byID[a.ID] = a
	}
	const (
		unseen = iota
		open
		done
	)
	mark := make(map[string]int, len(acts))
	var visit func(id string) string
	visit = func(id string) string {
		switch mark[id] {
		case open:
			return byID[id].Name
		case done:
			return ""
		}
		mark[id] = open
		for _, p := range byID[id].PredecessorIDs {
			if name := visit(p); name != "" {
				return name
			}
		}
		mark[id] = done
		return ""
	}
	for _, a := range acts {
		if name := visit(a.ID); name != "" {
			return name
		}
	}
	return ""
}

// ActivityCycle is one activity's name and cycle time in seconds.
type ActivityCycle struct {
	Name       string
	CycleTimeS int
}

// TaktCheck is the FR-24.14 "takt vs cycle" figure of a flow: the slowest
// activity (the bottleneck) sets the flow's cycle time, and the flow keeps up
// with demand when that cycle time is at most the takt time.
type TaktCheck struct {
	TaktTimeS  int
	CycleTimeS int
	Bottleneck string
	WithinTakt bool
}

// TaktVsCycle compares a flow's takt time with its bottleneck cycle time. ok is
// false when the flow has no takt time or no activities; between activities
// with the same cycle time the first is the bottleneck.
func TaktVsCycle(taktS int, acts []ActivityCycle) (TaktCheck, bool) {
	if taktS <= 0 || len(acts) == 0 {
		return TaktCheck{}, false
	}
	slow := acts[0]
	for _, a := range acts[1:] {
		if a.CycleTimeS > slow.CycleTimeS {
			slow = a
		}
	}
	return TaktCheck{TaktTimeS: taktS, CycleTimeS: slow.CycleTimeS, Bottleneck: slow.Name, WithinTakt: slow.CycleTimeS <= taktS}, true
}
