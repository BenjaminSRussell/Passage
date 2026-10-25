package domain

import "fmt"

const (
	CodeHierarchyCycle         = "ORG-HIER-CYCLE"
	CodeHierarchyMultiRoot     = "ORG-HIER-MULTI-ROOT"
	CodeHierarchyDuplicateNode = "ORG-HIER-DUPLICATE-NODE"
	CodeHierarchyParentUnknown = "ORG-HIER-PARENT-UNKNOWN"
	CodeHierarchyDepth         = "ORG-HIER-DEPTH"
	CodeHierarchySize          = "ORG-HIER-SIZE"
)

type ClosureRow struct {
	AncestorID, DescendantID string
	Depth                    int
}

type TreeLimits struct{ MaxDepth, MaxNodes int }

var DefaultTreeLimits = TreeLimits{MaxDepth: 15, MaxNodes: 50000}

func ValidateTree(nodes []Node, limits TreeLimits) error {
	if len(nodes) > limits.MaxNodes {
		return &Error{CodeHierarchySize, fmt.Sprintf("version has %d nodes; the limit is %d", len(nodes), limits.MaxNodes)}
	}
	index := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if _, seen := index[n.ID]; seen {
			return &Error{CodeHierarchyDuplicateNode, fmt.Sprintf("node %s appears more than once", n.ID)}
		}
		index[n.ID] = i
	}
	parent := make([]int, len(nodes))
	roots := 0
	for i, n := range nodes {
		if n.ParentID == "" {
			parent[i] = -1
			roots++
			continue
		}
		p, ok := index[n.ParentID]
		if !ok {
			return &Error{CodeHierarchyParentUnknown, fmt.Sprintf("node %s has parent %s, which is not in the version", n.ID, n.ParentID)}
		}
		parent[i] = p
	}
	if i := firstOnCycle(parent); i >= 0 {
		return &Error{CodeHierarchyCycle, fmt.Sprintf("Moving %s under %s creates a cycle.", nodes[i].ID, nodes[i].ParentID)}
	}
	if roots != 1 {
		return &Error{CodeHierarchyMultiRoot, fmt.Sprintf("A version must have exactly one root; found %d.", roots)}
	}
	for i, d := range depths(parent) {
		if d > limits.MaxDepth {
			return &Error{CodeHierarchyDepth, fmt.Sprintf("node %s is at depth %d; the limit is %d", nodes[i].ID, d, limits.MaxDepth)}
		}
	}
	return nil
}

func firstOnCycle(parent []int) int {
	state := make([]uint8, len(parent))
	onCycle := make([]bool, len(parent))
	var path []int
	for i := range parent {
		path = path[:0]
		j := i
		for j >= 0 && state[j] == 0 {
			state[j] = 1
			path = append(path, j)
			j = parent[j]
		}
		if j >= 0 && state[j] == 1 {
			for k := len(path) - 1; ; k-- {
				onCycle[path[k]] = true
				if path[k] == j {
					break
				}
			}
		}
		for _, k := range path {
			state[k] = 2
		}
	}
	for i, c := range onCycle {
		if c {
			return i
		}
	}
	return -1
}

func depths(parent []int) []int {
	depth := make([]int, len(parent))
	var path []int
	for i := range parent {
		path = path[:0]
		j := i
		for j >= 0 && depth[j] == 0 {
			path = append(path, j)
			j = parent[j]
		}
		d := 0
		if j >= 0 {
			d = depth[j]
		}
		for k := len(path) - 1; k >= 0; k-- {
			d++
			depth[path[k]] = d
		}
	}
	return depth
}

func Closure(nodes []Node) []ClosureRow {
	index := make(map[string]int, len(nodes))
	for i, n := range nodes {
		index[n.ID] = i
	}
	var rows []ClosureRow
	for _, n := range nodes {
		for depth, parentID := 1, n.ParentID; parentID != ""; depth++ {
			rows = append(rows, ClosureRow{AncestorID: parentID, DescendantID: n.ID, Depth: depth})
			parentID = nodes[index[parentID]].ParentID
		}
	}
	return rows
}
