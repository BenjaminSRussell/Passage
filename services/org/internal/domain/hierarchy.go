package domain

import "time"

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
