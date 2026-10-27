package domain

import (
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// CodeWBSDuplicate is PROJ-WBS-002 (§8.41.18): a WBS code repeats in one plan version.
const CodeWBSDuplicate = "PROJ-WBS-002"

// Bounds of one WBS replacement. The PRD sizes a plan at 2,000 nodes and 3,500
// dependencies (fixture proj-wbs-2000); the caps leave headroom.
const (
	MaxWBSNodes        = 5000
	MaxWBSDependencies = 10000
	maxDurationDays    = 100000
	maxLagDays         = 3650
)

// Constraint types on a WBS node (FR-41.2). The scheduler applies ASAP and
// SNET; FNLT and MSO are refused until the scheduler supports them.
const (
	ConstraintASAP = "ASAP"
	ConstraintSNET = "SNET"
	ConstraintFNLT = "FNLT"
	ConstraintMSO  = "MSO"
)

var (
	wbsCodePattern = regexp.MustCompile(`^\d{1,4}(\.\d{1,4}){0,7}$`)
	effortPattern  = regexp.MustCompile(`^\d{1,10}(\.\d{1,2})?$`)
)

// WBSNodeInput is one node of a WBS replacement. Codes are the client keys; the
// store assigns ids.
type WBSNodeInput struct {
	WBSCode             string  `json:"wbsCode"`
	ParentWBSCode       *string `json:"parentWbsCode,omitempty"`
	Name                string  `json:"name"`
	EffortHours         *string `json:"effortHours,omitempty"`
	DurationDays        int     `json:"durationDays"`
	ConstraintType      string  `json:"constraintType,omitempty"`
	ConstraintDate      *string `json:"constraintDate,omitempty"`
	RoleID              *string `json:"roleId,omitempty"`
	IsBillableMilestone bool    `json:"isBillableMilestone"`
}

// WBSDependencyInput links two nodes by their codes.
type WBSDependencyInput struct {
	PredecessorWBSCode string `json:"predecessorWbsCode"`
	SuccessorWBSCode   string `json:"successorWbsCode"`
	Type               string `json:"type"`
	LagDays            int    `json:"lagDays"`
}

// NormalizeWBS fills defaults in place: an empty constraint type is ASAP.
func NormalizeWBS(nodes []WBSNodeInput) {
	for i := range nodes {
		if nodes[i].ConstraintType == "" {
			nodes[i].ConstraintType = ConstraintASAP
		}
	}
}

// ValidateWBS checks a whole WBS replacement (after NormalizeWBS). It returns
// field-level errors for malformed input, PROJ-WBS-002 for a duplicate code and
// PROJ-WBS-001 for a dependency cycle. Parent chains must end at a root.
func ValidateWBS(nodes []WBSNodeInput, deps []WBSDependencyInput) (map[string]string, error) {
	fields := map[string]string{}
	if len(nodes) > MaxWBSNodes {
		fields["nodes"] = fmt.Sprintf("must have at most %d nodes", MaxWBSNodes)
	}
	if len(deps) > MaxWBSDependencies {
		fields["dependencies"] = fmt.Sprintf("must have at most %d dependencies", MaxWBSDependencies)
	}
	if len(fields) > 0 {
		return fields, nil
	}
	codes := make(map[string]int, len(nodes))
	for i, n := range nodes {
		key := fmt.Sprintf("nodes[%d]", i)
		if !wbsCodePattern.MatchString(n.WBSCode) {
			fields[key+".wbsCode"] = "must be dotted numbers such as 1.2.3"
		} else if _, dup := codes[n.WBSCode]; dup {
			return nil, rule(CodeWBSDuplicate, "WBS code %s already exists in this plan version", n.WBSCode)
		} else {
			codes[n.WBSCode] = i
		}
		if c := utf8.RuneCountInString(n.Name); c == 0 || c > 120 {
			fields[key+".name"] = "must be 1 to 120 characters"
		}
		if n.DurationDays < 0 || n.DurationDays > maxDurationDays {
			fields[key+".durationDays"] = fmt.Sprintf("must be 0 to %d working days", maxDurationDays)
		}
		if n.EffortHours != nil && !effortPattern.MatchString(*n.EffortHours) {
			fields[key+".effortHours"] = "must be a non-negative decimal with at most 2 fraction digits"
		}
		if n.RoleID != nil && !ids.Valid(*n.RoleID) {
			fields[key+".roleId"] = "must be a UUID"
		}
		switch n.ConstraintType {
		case ConstraintASAP:
			if n.ConstraintDate != nil {
				fields[key+".constraintDate"] = "must be empty for an ASAP node"
			}
		case ConstraintSNET:
			if n.ConstraintDate == nil {
				fields[key+".constraintDate"] = "is required for SNET"
			} else if _, err := time.Parse(time.DateOnly, *n.ConstraintDate); err != nil {
				fields[key+".constraintDate"] = "must be a date (YYYY-MM-DD)"
			}
		case ConstraintFNLT, ConstraintMSO:
			fields[key+".constraintType"] = "FNLT and MSO are not scheduled yet; use ASAP or SNET"
		default:
			fields[key+".constraintType"] = "must be ASAP or SNET"
		}
	}
	parent := map[string]string{}
	for i, n := range nodes {
		if n.ParentWBSCode == nil {
			continue
		}
		if _, ok := codes[*n.ParentWBSCode]; !ok {
			fields[fmt.Sprintf("nodes[%d].parentWbsCode", i)] = "must name a node in this plan version"
			continue
		}
		parent[n.WBSCode] = *n.ParentWBSCode
	}
	for code, i := range codes {
		cur := code
		for steps := 0; ; steps++ {
			p, ok := parent[cur]
			if !ok {
				break
			}
			if steps >= len(nodes) {
				fields[fmt.Sprintf("nodes[%d].parentWbsCode", i)] = "parent chain forms a cycle"
				break
			}
			cur = p
		}
	}
	summary := make(map[string]bool, len(parent))
	for _, p := range parent {
		summary[p] = true
	}
	seen := make(map[[2]string]bool, len(deps))
	for i, d := range deps {
		key := fmt.Sprintf("dependencies[%d]", i)
		_, okP := codes[d.PredecessorWBSCode]
		_, okS := codes[d.SuccessorWBSCode]
		pair := [2]string{d.PredecessorWBSCode, d.SuccessorWBSCode}
		switch {
		case !okP || !okS:
			fields[key] = "predecessor and successor must name nodes in this plan version"
		case summary[d.PredecessorWBSCode] || summary[d.SuccessorWBSCode]:
			fields[key] = "must link tasks, not summary nodes (a summary node's dates roll up from its children)"
		case d.PredecessorWBSCode == d.SuccessorWBSCode:
			fields[key] = "must not link a node to itself"
		case seen[pair]:
			fields[key] = "links the same two nodes as an earlier dependency"
		case d.Type != DepFS && d.Type != DepSS && d.Type != DepFF && d.Type != DepSF:
			fields[key+".type"] = "must be FS, SS, FF or SF"
		case d.LagDays < -maxLagDays || d.LagDays > maxLagDays:
			fields[key+".lagDays"] = fmt.Sprintf("must be between -%d and %d calendar days", maxLagDays, maxLagDays)
		}
		seen[pair] = true
	}
	if len(fields) > 0 {
		return fields, nil
	}
	return nil, checkAcyclic(nodes, deps)
}

// checkAcyclic runs Kahn's algorithm over the codes and returns PROJ-WBS-001,
// naming one dependency on a cycle, when the dependencies do not order every node.
func checkAcyclic(nodes []WBSNodeInput, deps []WBSDependencyInput) error {
	indeg := make(map[string]int, len(nodes))
	succ := make(map[string][]string, len(nodes))
	for _, d := range deps {
		indeg[d.SuccessorWBSCode]++
		succ[d.PredecessorWBSCode] = append(succ[d.PredecessorWBSCode], d.SuccessorWBSCode)
	}
	queue := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if indeg[n.WBSCode] == 0 {
			queue = append(queue, n.WBSCode)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, s := range succ[queue[i]] {
			indeg[s]--
			if indeg[s] == 0 {
				queue = append(queue, s)
			}
		}
	}
	if len(queue) == len(nodes) {
		return nil
	}
	// Every node left over has a predecessor that is also left over, so walking
	// back through left-over predecessors must revisit a node: that loop is a cycle.
	pred := make(map[string]string, len(nodes)-len(queue))
	for _, d := range deps {
		if indeg[d.PredecessorWBSCode] > 0 && indeg[d.SuccessorWBSCode] > 0 {
			pred[d.SuccessorWBSCode] = d.PredecessorWBSCode
		}
	}
	var cur string
	for _, n := range nodes {
		if _, ok := pred[n.WBSCode]; ok {
			cur = n.WBSCode
			break
		}
	}
	visited := map[string]bool{}
	for !visited[cur] {
		visited[cur] = true
		cur = pred[cur]
	}
	return rule(CodeWBSCycle, "Dependency %s → %s creates a cycle", pred[cur], cur)
}

// WBSNode is a stored WBS node. Dates are YYYY-MM-DD. The scheduler reads
// DurationDays and the constraint; it writes the early and late dates, slack
// (working days of total float) and Critical.
type WBSNode struct {
	ID                  string  `json:"id"`
	PlanVersionID       string  `json:"planVersionId"`
	WBSCode             string  `json:"wbsCode"`
	ParentID            *string `json:"parentId,omitempty"`
	Name                string  `json:"name"`
	EffortHours         *string `json:"effortHours,omitempty"`
	DurationDays        int     `json:"durationDays"`
	ConstraintType      string  `json:"constraintType"`
	ConstraintDate      *string `json:"constraintDate,omitempty"`
	RoleID              *string `json:"roleId,omitempty"`
	IsBillableMilestone bool    `json:"isBillableMilestone"`
	EarlyStart          *string `json:"earlyStart,omitempty"`
	EarlyFinish         *string `json:"earlyFinish,omitempty"`
	LateStart           *string `json:"lateStart,omitempty"`
	LateFinish          *string `json:"lateFinish,omitempty"`
	SlackDays           *int    `json:"slackDays,omitempty"`
	Critical            *bool   `json:"critical,omitempty"`
}

// WBSLink is a stored dependency between two node ids.
type WBSLink struct {
	PredecessorID string `json:"predecessorId"`
	SuccessorID   string `json:"successorId"`
	Type          string `json:"type"`
	LagDays       int    `json:"lagDays"`
}

// ScheduleWBS runs the CPM scheduler (forward and backward pass) over stored
// nodes from the project start on cal, and writes each node's early and late
// dates, slack and critical flag into nodes (same order). Tasks (nodes without
// children) are scheduled; a summary node takes the earliest start, latest
// finish, smallest slack and any-critical of its children, so its own
// duration and constraint are ignored. Role calendars come from wave 8; until
// then the caller passes the standard calendar.
func ScheduleWBS(cal Calendar, projectStart string, nodes []WBSNode, links []WBSLink) (Schedule, error) {
	start, err := time.Parse(time.DateOnly, projectStart)
	if err != nil {
		return Schedule{}, fmt.Errorf("%w: project start %q", ErrInvalidInput, projectStart)
	}
	children := make(map[string][]int, len(nodes))
	for i, n := range nodes {
		if n.ParentID != nil {
			children[*n.ParentID] = append(children[*n.ParentID], i)
		}
	}
	preds := make(map[string][]Dependency, len(links))
	for _, l := range links {
		preds[l.SuccessorID] = append(preds[l.SuccessorID], Dependency{PredecessorID: l.PredecessorID, Type: l.Type, LagDays: l.LagDays})
	}
	acts := make([]Activity, 0, len(nodes))
	taskAt := make(map[int]int, len(nodes)) // node index → activity index
	for i, n := range nodes {
		if len(children[n.ID]) > 0 {
			continue
		}
		a := Activity{ID: n.ID, DurationDays: n.DurationDays, Deps: preds[n.ID]}
		if n.ConstraintType == ConstraintSNET && n.ConstraintDate != nil {
			d, err := time.Parse(time.DateOnly, *n.ConstraintDate)
			if err != nil {
				return Schedule{}, fmt.Errorf("%w: constraint date %q", ErrInvalidInput, *n.ConstraintDate)
			}
			a.StartNoEarlierThan = &d
		}
		taskAt[i] = len(acts)
		acts = append(acts, a)
	}
	sched, err := CPM(cal, start, acts)
	if err != nil {
		return Schedule{}, err
	}
	out := make([]ScheduledActivity, len(nodes))
	done := make([]bool, len(nodes))
	var roll func(i int) ScheduledActivity
	roll = func(i int) ScheduledActivity {
		if done[i] {
			return out[i]
		}
		if k, ok := taskAt[i]; ok {
			out[i] = sched.Activities[k]
		} else {
			var agg ScheduledActivity
			for j, c := range children[nodes[i].ID] {
				a := roll(c)
				if j == 0 {
					agg = a
					continue
				}
				agg.EarlyStart, agg.LateStart = minTime(agg.EarlyStart, a.EarlyStart), minTime(agg.LateStart, a.LateStart)
				agg.EarlyFinish, agg.LateFinish = maxTime(agg.EarlyFinish, a.EarlyFinish), maxTime(agg.LateFinish, a.LateFinish)
				agg.SlackDays = min(agg.SlackDays, a.SlackDays)
				agg.Critical = agg.Critical || a.Critical
			}
			agg.ID = nodes[i].ID
			out[i] = agg
		}
		done[i] = true
		return out[i]
	}
	for i := range nodes {
		a := roll(i)
		es, ef := a.EarlyStart.Format(time.DateOnly), a.EarlyFinish.Format(time.DateOnly)
		ls, lf := a.LateStart.Format(time.DateOnly), a.LateFinish.Format(time.DateOnly)
		slack, critical := a.SlackDays, a.Critical
		nodes[i].EarlyStart, nodes[i].EarlyFinish = &es, &ef
		nodes[i].LateStart, nodes[i].LateFinish = &ls, &lf
		nodes[i].SlackDays, nodes[i].Critical = &slack, &critical
	}
	return sched, nil
}
