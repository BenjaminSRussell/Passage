package domain_test

import (
	"errors"
	"fmt"
	"reflect"
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

func TestValidateTreeRandomTrees(t *testing.T) {
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
		descendant := rapid.IntRange(0, size-1).Draw(t, "descendant")
		ancestor := descendant
		for hops := rapid.IntRange(0, size).Draw(t, "hops"); hops > 0 && ancestor != 0; hops-- {
			ancestor = parent[ancestor]
		}
		nodes[ancestor].ParentID = nodes[descendant].ID
		var de *domain.Error
		if err := domain.ValidateTree(nodes, limits); !errors.As(err, &de) || de.Code != domain.CodeHierarchyCycle {
			t.Fatalf("re-parenting %s under its descendant %s: got %v; want %s", nodes[ancestor].ID, nodes[descendant].ID, err, domain.CodeHierarchyCycle)
		}
	})
}

func TestClosure(t *testing.T) {
	var chainRows []domain.ClosureRow
	for i := 1; i < 15; i++ {
		for d := 1; d <= i; d++ {
			chainRows = append(chainRows, domain.ClosureRow{AncestorID: fmt.Sprintf("n%d", i-d), DescendantID: fmt.Sprintf("n%d", i), Depth: d})
		}
	}
	star := []domain.Node{{ID: "r"}}
	var starRows []domain.ClosureRow
	for i := range 5 {
		id := fmt.Sprintf("c%d", i)
		star = append(star, domain.Node{ID: id, ParentID: "r"})
		starRows = append(starRows, domain.ClosureRow{AncestorID: "r", DescendantID: id, Depth: 1})
	}
	mixed := []domain.Node{{ID: "a", ParentID: "r"}, {ID: "r"}, {ID: "b", ParentID: "a"}, {ID: "c", ParentID: "b"}, {ID: "d", ParentID: "r"}}
	tests := []struct {
		name  string
		nodes []domain.Node
		want  []domain.ClosureRow
		sum   int
	}{
		{"empty", nil, nil, 0},
		{"single root", []domain.Node{{ID: "r"}}, nil, 0},
		{"chain of 15", chain(15), chainRows, 105},
		{"wide star", star, starRows, 5},
		{"mixed tree in shuffled input order", mixed, []domain.ClosureRow{
			{"r", "a", 1},
			{"a", "b", 1}, {"r", "b", 2},
			{"b", "c", 1}, {"a", "c", 2}, {"r", "c", 3},
			{"r", "d", 1},
		}, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Closure(tc.nodes)
			if len(got) != tc.sum || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Closure = %v (%d rows); want %v (%d rows)", got, len(got), tc.want, tc.sum)
			}
		})
	}
}

func TestClosureRandomTrees(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		size := rapid.IntRange(1, 200).Draw(t, "size")
		nodes := make([]domain.Node, size)
		parent := make([]int, size)
		depth := make([]int, size)
		sum := 0
		for i := range nodes {
			nodes[i].ID = fmt.Sprintf("n%d", i)
			if i > 0 {
				parent[i] = rapid.IntRange(0, i-1).Draw(t, "parent")
				nodes[i].ParentID = nodes[parent[i]].ID
				depth[i] = depth[parent[i]] + 1
			}
			sum += depth[i]
		}
		rows := domain.Closure(nodes)
		if len(rows) != sum {
			t.Fatalf("%d rows; want the sum of depths %d", len(rows), sum)
		}
		byID := make(map[string]int, size)
		for i, n := range nodes {
			byID[n.ID] = i
		}
		for _, row := range rows {
			at := byID[row.DescendantID]
			for range row.Depth {
				at = parent[at]
			}
			if row.Depth < 1 || nodes[at].ID != row.AncestorID {
				t.Fatalf("row %+v: walking %d parents from %s reaches %s", row, row.Depth, row.DescendantID, nodes[at].ID)
			}
		}
	})
}
