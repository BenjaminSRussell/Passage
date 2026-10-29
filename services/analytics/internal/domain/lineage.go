package domain

import "strconv"

// Lineage limits (FR-46.12). A walk never goes deeper than MaxLineageDepth
// hops in either direction; one write carries at most MaxLineageEdges edges.
const (
	MaxLineageDepth = 20
	MaxLineageEdges = 500
)

// Edge says Target is derived from Source (Source feeds Target). Both are
// catalog asset names such as raw.ledger.journal_line or mart.fin_journal_line.
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// ValidateEdges checks a batch of lineage edges: well-formed asset names, no
// self loops, no duplicates within the batch, and the batch size cap. Every
// invalid edge is reported at once.
func ValidateEdges(edges []Edge) *Error {
	if len(edges) == 0 || len(edges) > MaxLineageEdges {
		return validation(map[string]string{"edges": "between 1 and 500 edges"})
	}
	fields := map[string]string{}
	seen := map[Edge]bool{}
	for i, e := range edges {
		key := "edges[" + strconv.Itoa(i) + "]"
		switch {
		case !IsAssetFQN(e.Source) || !IsAssetFQN(e.Target):
			fields[key] = "source and target must be dotted lowercase asset names"
		case e.Source == e.Target:
			fields[key] = "an asset cannot feed itself"
		case seen[e]:
			fields[key] = "duplicate edge"
		}
		seen[e] = true
	}
	if len(fields) > 0 {
		return validation(fields)
	}
	return nil
}

// LineageDepth normalises a requested walk depth: 0 means the maximum.
func LineageDepth(depth int) (int, bool) {
	if depth < 0 || depth > MaxLineageDepth {
		return 0, false
	}
	if depth == 0 {
		return MaxLineageDepth, true
	}
	return depth, true
}
