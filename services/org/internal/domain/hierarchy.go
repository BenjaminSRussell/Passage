package domain

import (
	"sort"
	"time"
)

const (
	MaxDepth = 15
	MaxNodes = 50000

	CodeCycle     = "ORG-HIER-CYCLE"
	CodeRoot      = "ORG-HIER-ROOT"
	CodeDuplicate = "ORG-HIER-DUPLICATE"
	CodeDepth     = "ORG-HIER-DEPTH"
	CodeSize      = "ORG-HIER-SIZE"
	CodeOverlap   = "ORG-HIER-OVERLAP"
	CodeNoPath    = "ORG-UOM-NO-PATH"
)

type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

type Node struct{ ID, ParentID string }

type Version struct {
	Purpose       string
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	Nodes         []Node
}

func (v Version) contains(date time.Time) bool {
	return !date.Before(v.EffectiveFrom) && (v.EffectiveTo.IsZero() || date.Before(v.EffectiveTo))
}

func ParentAsOf(versions []Version, purpose, orgUnitID string, date time.Time) (string, bool) {
	for _, v := range versions {
		if v.Purpose != purpose || !v.contains(date) {
			continue
		}
		for _, n := range v.Nodes {
			if n.ID == orgUnitID {
				return n.ParentID, true
			}
		}
		return "", false
	}
	return "", false
}

func ValidateVersions(versions []Version) error {
	byPurpose := map[string][]Version{}
	for _, v := range versions {
		byPurpose[v.Purpose] = append(byPurpose[v.Purpose], v)
	}
	for purpose, vs := range byPurpose {
		sort.Slice(vs, func(i, j int) bool { return vs[i].EffectiveFrom.Before(vs[j].EffectiveFrom) })
		for i := 1; i < len(vs); i++ {
			prev := vs[i-1]
			if prev.EffectiveTo.IsZero() || vs[i].EffectiveFrom.Before(prev.EffectiveTo) {
				return &Error{CodeOverlap, "published versions of purpose " + purpose + " overlap"}
			}
		}
	}
	return nil
}

func ValidateNodes(nodes []Node) error {
	if len(nodes) > MaxNodes {
		return &Error{CodeSize, "hierarchy exceeds the node limit"}
	}
	parent := make(map[string]string, len(nodes))
	roots := 0
	for _, n := range nodes {
		if _, dup := parent[n.ID]; dup {
			return &Error{CodeDuplicate, "node " + n.ID + " appears twice"}
		}
		parent[n.ID] = n.ParentID
		if n.ParentID == "" {
			roots++
		}
	}
	if roots != 1 {
		return &Error{CodeRoot, "a hierarchy needs exactly one root"}
	}
	for _, n := range nodes {
		if _, ok := parent[n.ParentID]; n.ParentID != "" && !ok {
			return &Error{CodeRoot, "node " + n.ID + " has an unknown parent"}
		}
	}
	depth := make(map[string]int, len(nodes))
	for _, n := range nodes {
		d, err := depthOf(n.ID, parent, depth)
		if err != nil {
			return err
		}
		if d > MaxDepth {
			return &Error{CodeDepth, "hierarchy is deeper than the depth limit"}
		}
	}
	return nil
}

func depthOf(id string, parent map[string]string, memo map[string]int) (int, error) {
	var path []string
	seen := map[string]bool{}
	d := 0
	for cur := id; ; cur = parent[cur] {
		if v, ok := memo[cur]; ok {
			d = v
			break
		}
		if seen[cur] {
			return 0, &Error{CodeCycle, "node " + cur + " is its own ancestor"}
		}
		seen[cur] = true
		path = append(path, cur)
		if parent[cur] == "" {
			break
		}
	}
	for i := len(path) - 1; i >= 0; i-- {
		d++
		memo[path[i]] = d
	}
	return memo[id], nil
}
