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
