package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// CodeSimConflict is PROJ-SIM-001 (§8.41.18): nodes changed in both WORKING and
// the simulation since the copy have no resolution, or the merged plan is not a
// valid WBS.
const CodeSimConflict = "PROJ-SIM-001"

// MaxSimulations is the PRD default of the tenant parameter "Max simulation
// versions per project" (BR-41.14, §8.41.13). The parameter store is not built,
// so the default applies to every tenant.
const MaxSimulations = 20

// Resolutions a caller gives for a conflicting node (BR-41.14).
const (
	ResolveKeepWorking    = "KEEP_WORKING"
	ResolveTakeSimulation = "TAKE_SIMULATION"
)

// Change kinds of a node in a transfer, as seen from the WORKING plan.
const (
	ChangeAdded   = "ADDED"
	ChangeChanged = "CHANGED"
	ChangeRemoved = "REMOVED"
)

// NodeContent is what the three-way diff compares for one WBS code: the node's
// own fields and its incoming dependencies (sorted by predecessor code). A node
// "moved" by a new date constraint, a new duration or a new predecessor is
// therefore a changed node. Scheduler output (dates, slack, critical) is not
// content: it is derived and cleared by a WBS replacement.
type NodeContent struct {
	Node         WBSNodeInput
	Predecessors []WBSDependencyInput
}

// ContentByCode turns a stored plan into content keyed by WBS code.
func ContentByCode(nodes []WBSNode, links []WBSLink) map[string]NodeContent {
	codeOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		codeOf[n.ID] = n.WBSCode
	}
	out := make(map[string]NodeContent, len(nodes))
	for _, n := range nodes {
		in := WBSNodeInput{WBSCode: n.WBSCode, Name: n.Name, EffortHours: n.EffortHours, DurationDays: n.DurationDays,
			ConstraintType: n.ConstraintType, ConstraintDate: n.ConstraintDate, RoleID: n.RoleID, IsBillableMilestone: n.IsBillableMilestone}
		if n.ParentID != nil {
			if p, ok := codeOf[*n.ParentID]; ok {
				in.ParentWBSCode = &p
			}
		}
		out[n.WBSCode] = NodeContent{Node: in}
	}
	for _, l := range links {
		succ, pred := codeOf[l.SuccessorID], codeOf[l.PredecessorID]
		c, ok := out[succ]
		if !ok || pred == "" {
			continue
		}
		c.Predecessors = append(c.Predecessors, WBSDependencyInput{PredecessorWBSCode: pred, SuccessorWBSCode: succ, Type: l.Type, LagDays: l.LagDays})
		out[succ] = c
	}
	for code, c := range out {
		sort.Slice(c.Predecessors, func(i, j int) bool {
			return CompareWBSCodes(c.Predecessors[i].PredecessorWBSCode, c.Predecessors[j].PredecessorWBSCode) < 0
		})
		out[code] = c
	}
	return out
}

// Fingerprint is a stable digest of a node's content. Effort is normalised to
// two decimals, so "8" and "8.00" are the same content.
func Fingerprint(c NodeContent) string {
	n := c.Node
	effort := ""
	if n.EffortHours != nil {
		if d, err := decimal.NewFromString(*n.EffortHours); err == nil {
			effort = d.StringFixed(2)
		} else {
			effort = *n.EffortHours
		}
	}
	ct := n.ConstraintType
	if ct == "" {
		ct = ConstraintASAP
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q|%q|%q|%q|%d|%q|%q|%q|%t", n.WBSCode, deref(n.ParentWBSCode), n.Name, effort, n.DurationDays, ct,
		deref(n.ConstraintDate), deref(n.RoleID), n.IsBillableMilestone)
	for _, d := range c.Predecessors {
		fmt.Fprintf(&b, "|<%q,%q,%d>", d.PredecessorWBSCode, d.Type, d.LagDays)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// Fingerprints returns the fingerprint of every node of a plan: the common
// ancestor a SIMULATION records when it is copied (BR-41.14).
func Fingerprints(content map[string]NodeContent) map[string]string {
	out := make(map[string]string, len(content))
	for code, c := range content {
		out[code] = Fingerprint(c)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// NodeChange is one node of a transfer. Working and Simulation are the node on
// each side (nil when the side has no node with that code). Resolution is set on
// a conflict the caller resolved.
type NodeChange struct {
	WBSCode    string        `json:"wbsCode"`
	Change     string        `json:"change"`
	Working    *WBSNodeInput `json:"working,omitempty"`
	Simulation *WBSNodeInput `json:"simulation,omitempty"`
	Resolution string        `json:"resolution,omitempty"`
}

// TransferPlan is the outcome of the three-way diff of BR-41.14.
type TransferPlan struct {
	// Applied lists the nodes the transfer changes in WORKING: those changed
	// only in the simulation, and conflicts resolved TAKE_SIMULATION.
	Applied []NodeChange `json:"applied"`
	// Conflicts lists the nodes changed in both since the copy (differently),
	// with the caller's resolution when given.
	Conflicts []NodeChange `json:"conflicts"`
	// Nodes and Dependencies are the merged WORKING plan.
	Nodes        []WBSNodeInput       `json:"-"`
	Dependencies []WBSDependencyInput `json:"-"`
}

// Unresolved returns the codes of conflicts without a resolution, in WBS order.
func (t TransferPlan) Unresolved() []string {
	var out []string
	for _, c := range t.Conflicts {
		if c.Resolution == "" {
			out = append(out, c.WBSCode)
		}
	}
	return out
}

// DiffSimulation is the three-way diff of BR-41.14. base holds the fingerprint
// of each node of the version the simulation was copied from, taken at copy
// time (a code missing from base did not exist); working and sim are the
// current content of the WORKING plan and the simulation.
//
//   - A node changed only in the simulation is applied (added, changed or removed in WORKING).
//   - A node changed only in WORKING, or changed alike on both sides, is kept.
//   - A node changed on both sides with different results is a conflict, resolved
//     by resolutions[code]: KEEP_WORKING or TAKE_SIMULATION.
//
// A merged node takes its incoming dependencies from the side it came from;
// those whose predecessor is not in the merged plan are dropped. Unknown codes
// in resolutions are ignored.
func DiffSimulation(base map[string]string, working, sim map[string]NodeContent, resolutions map[string]string) TransferPlan {
	codes := make(map[string]bool, len(working)+len(sim))
	for c := range base {
		codes[c] = true
	}
	for c := range working {
		codes[c] = true
	}
	for c := range sim {
		codes[c] = true
	}
	sorted := make([]string, 0, len(codes))
	for c := range codes {
		sorted = append(sorted, c)
	}
	sort.Slice(sorted, func(i, j int) bool { return CompareWBSCodes(sorted[i], sorted[j]) < 0 })

	out := TransferPlan{Applied: []NodeChange{}, Conflicts: []NodeChange{}}
	merged := make(map[string]NodeContent, len(sorted))
	for _, code := range sorted {
		wk, inWk := working[code]
		sm, inSim := sim[code]
		wkFP, simFP := "", ""
		if inWk {
			wkFP = Fingerprint(wk)
		}
		if inSim {
			simFP = Fingerprint(sm)
		}
		baseFP := base[code]
		change := NodeChange{WBSCode: code, Change: changeKind(inWk, inSim)}
		if inWk {
			n := wk.Node
			change.Working = &n
		}
		if inSim {
			n := sm.Node
			change.Simulation = &n
		}
		takeSim := false
		switch {
		case simFP == baseFP || simFP == wkFP:
			// Unchanged in the simulation, or the same result on both sides.
		case wkFP == baseFP:
			takeSim = true
			out.Applied = append(out.Applied, change)
		default:
			switch resolutions[code] {
			case ResolveTakeSimulation:
				change.Resolution = ResolveTakeSimulation
				takeSim = true
				out.Applied = append(out.Applied, change)
			case ResolveKeepWorking:
				change.Resolution = ResolveKeepWorking
			}
			out.Conflicts = append(out.Conflicts, change)
		}
		switch {
		case takeSim && inSim:
			merged[code] = sm
		case !takeSim && inWk:
			merged[code] = wk
		}
	}
	for _, code := range sorted {
		c, ok := merged[code]
		if !ok {
			continue
		}
		out.Nodes = append(out.Nodes, c.Node)
		for _, d := range c.Predecessors {
			if _, ok := merged[d.PredecessorWBSCode]; ok {
				out.Dependencies = append(out.Dependencies, d)
			}
		}
	}
	return out
}

func changeKind(inWorking, inSim bool) string {
	switch {
	case !inWorking:
		return ChangeAdded
	case !inSim:
		return ChangeRemoved
	}
	return ChangeChanged
}

// CheckTransfer refuses a transfer with PROJ-SIM-001 while conflicts are
// unresolved, or when the merged plan is not a valid WBS (for example the
// simulation removed a parent that WORKING gave a new child). A dependency
// cycle in the merged plan is PROJ-WBS-001.
func CheckTransfer(simID string, t TransferPlan) error {
	if open := t.Unresolved(); len(open) > 0 {
		return rule(CodeSimConflict, "%d nodes changed in both WORKING and simulation %s; resolve conflicts: %s",
			len(open), simID, strings.Join(open, ", "))
	}
	nodes := append([]WBSNodeInput(nil), t.Nodes...)
	NormalizeWBS(nodes)
	fields, err := ValidateWBS(nodes, t.Dependencies)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(fields))
	for k, v := range fields {
		msgs = append(msgs, nodeLabel(k, nodes)+" "+v)
	}
	sort.Strings(msgs)
	return rule(CodeSimConflict, "the merged plan of simulation %s is not a valid WBS: %s; resolve the nodes differently", simID, strings.Join(msgs, "; "))
}

// nodeLabel rewrites a ValidateWBS field key such as nodes[3].parentWbsCode to
// the node's WBS code, which is what the caller knows.
func nodeLabel(key string, nodes []WBSNodeInput) string {
	if strings.HasPrefix(key, "nodes[") {
		end := strings.Index(key, "]")
		if i, err := strconv.Atoi(key[len("nodes["):end]); err == nil && i < len(nodes) {
			return "node " + nodes[i].WBSCode + key[end+1:]
		}
	}
	return key
}

// CompareWBSCodes orders dotted WBS codes segment by segment numerically
// (1.2 < 1.10), the order the store reads nodes in.
func CompareWBSCodes(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, errX := strconv.Atoi(as[i])
		y, errY := strconv.Atoi(bs[i])
		if errX != nil || errY != nil {
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
			continue
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return len(as) - len(bs)
}
