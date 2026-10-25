package domain

import (
	"cmp"
	"slices"
	"time"
)

const CodeNoPath = "ORG-UOM-NO-PATH"

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

type NodeChange struct{ ID, ParentID string }

type NodeMove struct{ ID, OldParentID, NewParentID string }

type Comparison struct {
	Added   []NodeChange
	Removed []NodeChange
	Moved   []NodeMove
}

func CompareNodes(from, to []Node) Comparison {
	fromParents, toParents := parentMap(from), parentMap(to)
	out := Comparison{Added: []NodeChange{}, Removed: []NodeChange{}, Moved: []NodeMove{}}
	for id, parent := range toParents {
		if old, ok := fromParents[id]; !ok {
			out.Added = append(out.Added, NodeChange{ID: id, ParentID: parent})
		} else if old != parent {
			out.Moved = append(out.Moved, NodeMove{ID: id, OldParentID: old, NewParentID: parent})
		}
	}
	for id, parent := range fromParents {
		if _, ok := toParents[id]; !ok {
			out.Removed = append(out.Removed, NodeChange{ID: id, ParentID: parent})
		}
	}
	slices.SortFunc(out.Added, func(a, b NodeChange) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(out.Removed, func(a, b NodeChange) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(out.Moved, func(a, b NodeMove) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func parentMap(nodes []Node) map[string]string {
	parents := make(map[string]string, len(nodes))
	for _, n := range nodes {
		parents[n.ID] = n.ParentID
	}
	return parents
}
