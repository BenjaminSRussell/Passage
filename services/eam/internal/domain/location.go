package domain

import (
	"fmt"
	"strings"
)

// Functional-location rule codes (FR-44.1). EAM-FL-001 is the PRD's; EAM-FL-002
// is the service code for placement rules the PRD states without a code.
const (
	CodeLocationDepth = "EAM-FL-001"
	CodeLocationRule  = "EAM-FL-002"
)

// MaxLocationDepth is the deepest functional location the hierarchy allows.
const MaxLocationDepth = 12

// PathSeparator joins location codes into a path; codes cannot contain it.
const PathSeparator = "/"

// ChildLocation returns the depth and path of a functional location placed under
// a parent at parentDepth with parentPath. A parentDepth of 0 makes the new
// location a root (depth 1, path = code). Depth above MaxLocationDepth is refused
// with EAM-FL-001.
func ChildLocation(parentDepth int, parentPath, code string) (depth int, path string, err error) {
	if parentDepth <= 0 {
		return 1, code, nil
	}
	depth = parentDepth + 1
	if depth > MaxLocationDepth {
		return 0, "", GateError{Code: CodeLocationDepth,
			Detail: fmt.Sprintf("functional location depth exceeds %d", MaxLocationDepth)}
	}
	return depth, parentPath + PathSeparator + code, nil
}

// AssetMove is the kind of location change an asset undergoes (FR-44.2).
type AssetMove string

const (
	MoveInstall AssetMove = "INSTALL" // the asset had no location and now has one
	MoveMove    AssetMove = "MOVE"    // the asset changes from one location to another
	MoveRemove  AssetMove = "REMOVE"  // the asset leaves its location
)

// LocationChange classifies a change of an asset's functional location from
// current to next ("" = none). A no-op is refused with EAM-FL-002 so that the
// history records only real changes.
func LocationChange(current, next string) (AssetMove, error) {
	switch {
	case current == next && next == "":
		return "", GateError{Code: CodeLocationRule, Detail: "asset is not installed at any functional location"}
	case current == next:
		return "", GateError{Code: CodeLocationRule, Detail: "asset is already at this functional location"}
	case current == "":
		return MoveInstall, nil
	case next == "":
		return MoveRemove, nil
	default:
		return MoveMove, nil
	}
}

// PathDepth counts the segments of a functional-location path.
func PathDepth(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, PathSeparator) + 1
}
