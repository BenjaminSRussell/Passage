package domain_test

import (
	"errors"
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func chain(length int) []domain.Node {
	nodes := make([]domain.Node, length)
	for i := range nodes {
		nodes[i].ID = fmt.Sprintf("n%d", i)
		if i > 0 {
			nodes[i].ParentID = nodes[i-1].ID
		}
	}
	return nodes
}

func TestValidateTree(t *testing.T) {
	limits := domain.TreeLimits{MaxDepth: 15, MaxNodes: 3}
	tests := []struct {
		name       string
		nodes      []domain.Node
		limits     domain.TreeLimits
		wantCode   string
		wantDetail string
	}{
		{"valid", []domain.Node{{ID: "r"}, {ID: "a", ParentID: "r"}}, domain.DefaultTreeLimits, "", ""},
		{"size", []domain.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}, limits, domain.CodeHierarchySize, "version has 4 nodes; the limit is 3"},
		{"duplicate", []domain.Node{{ID: "a"}, {ID: "a", ParentID: "a"}}, domain.DefaultTreeLimits, domain.CodeHierarchyDuplicateNode, "node a appears more than once"},
		{"unknown parent", []domain.Node{{ID: "a"}, {ID: "b", ParentID: "zzz"}}, domain.DefaultTreeLimits, domain.CodeHierarchyParentUnknown, "node b has parent zzz, which is not in the version"},
		{"self parent", []domain.Node{{ID: "r"}, {ID: "a", ParentID: "a"}}, domain.DefaultTreeLimits, domain.CodeHierarchyCycle, "Moving a under a creates a cycle."},
		{"three node cycle", []domain.Node{{ID: "r"}, {ID: "a", ParentID: "c"}, {ID: "b", ParentID: "a"}, {ID: "c", ParentID: "b"}}, domain.DefaultTreeLimits, domain.CodeHierarchyCycle, "Moving a under c creates a cycle."},
		{"feeder of a cycle is not reported", []domain.Node{{ID: "x", ParentID: "a"}, {ID: "a", ParentID: "b"}, {ID: "b", ParentID: "a"}, {ID: "r"}}, domain.DefaultTreeLimits, domain.CodeHierarchyCycle, "Moving a under b creates a cycle."},
		{"cycle without any root", []domain.Node{{ID: "a", ParentID: "b"}, {ID: "b", ParentID: "a"}}, domain.DefaultTreeLimits, domain.CodeHierarchyCycle, "Moving a under b creates a cycle."},
		{"empty set", nil, domain.DefaultTreeLimits, domain.CodeHierarchyMultiRoot, "A version must have exactly one root; found 0."},
		{"two roots", []domain.Node{{ID: "a"}, {ID: "b"}}, domain.DefaultTreeLimits, domain.CodeHierarchyMultiRoot, "A version must have exactly one root; found 2."},
		{"two roots before depth", append(chain(16), domain.Node{ID: "other"}), domain.DefaultTreeLimits, domain.CodeHierarchyMultiRoot, "A version must have exactly one root; found 2."},
		{"fifteen levels", chain(15), domain.DefaultTreeLimits, "", ""},
		{"sixteen levels", chain(16), domain.DefaultTreeLimits, domain.CodeHierarchyDepth, "node n15 is at depth 16; the limit is 15"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidateTree(tc.nodes, tc.limits)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("got %v; want nil", err)
				}
				return
			}
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != tc.wantCode || de.Detail != tc.wantDetail {
				t.Fatalf("got %v; want %s %q", err, tc.wantCode, tc.wantDetail)
			}
		})
	}
}

func TestValidateTreeRandomTreesAreValidAndReparentingUnderADescendantIsACycle(t *testing.T) {
	limits := domain.TreeLimits{MaxDepth: 300, MaxNodes: 300}
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(3, 300).Draw(t, "size")
		nodes := make([]domain.Node, size)
		parent := make([]int, size)
		for i := range nodes {
			nodes[i].ID = fmt.Sprintf("n%d", i)
			if i > 0 {
				parent[i] = rapid.IntRange(0, i-1).Draw(t, "parent")
				nodes[i].ParentID = nodes[parent[i]].ID
			}
		}
		if err := domain.ValidateTree(nodes, limits); err != nil {
			t.Fatalf("random tree rejected: %v", err)
		}
		descendant := rapid.IntRange(1, size-1).Draw(t, "descendant")
		var ancestors []int
		for a := parent[descendant]; a != 0; a = parent[a] {
			ancestors = append(ancestors, a)
		}
		if len(ancestors) == 0 {
			t.Skip("no non-root ancestor to move")
		}
		moved := ancestors[rapid.IntRange(0, len(ancestors)-1).Draw(t, "moved")]
		nodes[moved].ParentID = nodes[descendant].ID
		var de *domain.Error
		if err := domain.ValidateTree(nodes, limits); !errors.As(err, &de) || de.Code != domain.CodeHierarchyCycle {
			t.Fatalf("moving %s under its descendant %s gave %v; want a cycle", nodes[moved].ID, nodes[descendant].ID, err)
		}
	})
}
