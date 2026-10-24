package domain_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

var epoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func day(n int) time.Time { return epoch.AddDate(0, 0, n) }

func TestParentAsOf_MatchesOracle(t *testing.T) {
	const (
		nodeCount    = 1000
		versionCount = 3
		span         = 100
		probes       = 10000
	)
	purposes := []string{"reporting", "procurement", "expenditure", "security", "custom"}
	rapid.Check(t, func(t *rapid.T) {
		rng := rand.New(rand.NewSource(rapid.Int64().Draw(t, "seed")))
		var versions []domain.Version
		oracle := make([][][]int, len(purposes))
		for p := range purposes {
			oracle[p] = make([][]int, versionCount)
			for v := 0; v < versionCount; v++ {
				parents := make([]int, nodeCount)
				nodes := make([]domain.Node, nodeCount)
				for i := range nodes {
					parents[i] = -1
					nodes[i] = domain.Node{ID: fmt.Sprintf("n%d", i)}
					if i > 0 {
						parents[i] = rng.Intn(i)
						nodes[i].ParentID = fmt.Sprintf("n%d", parents[i])
					}
				}
				oracle[p][v] = parents
				versions = append(versions, domain.Version{Purpose: purposes[p], EffectiveFrom: day(v * span), EffectiveTo: day((v + 1) * span), Nodes: nodes})
			}
		}
		for i := 0; i < probes; i++ {
			p, node, d := rng.Intn(len(purposes)), rng.Intn(nodeCount), rng.Intn(versionCount*span+40)-20
			wantOK := d >= 0 && d < versionCount*span
			wantParent := ""
			if wantOK && oracle[p][d/span][node] >= 0 {
				wantParent = fmt.Sprintf("n%d", oracle[p][d/span][node])
			}
			got, ok := domain.ParentAsOf(versions, purposes[p], fmt.Sprintf("n%d", node), day(d))
			if ok != wantOK || got != wantParent {
				t.Fatalf("%s n%d day %d: got (%q,%v), want (%q,%v)", purposes[p], node, d, got, ok, wantParent, wantOK)
			}
		}
	})
}

func TestParentAsOf_Edges(t *testing.T) {
	versions := []domain.Version{
		{Purpose: "reporting", EffectiveFrom: day(0), EffectiveTo: day(10), Nodes: []domain.Node{{ID: "r"}, {ID: "a", ParentID: "r"}}},
		{Purpose: "reporting", EffectiveFrom: day(20), Nodes: []domain.Node{{ID: "r"}, {ID: "a", ParentID: "r"}, {ID: "b", ParentID: "a"}}},
	}
	tests := []struct {
		name, purpose, node string
		date                time.Time
		wantParent          string
		wantOK              bool
	}{
		{"inside first version", "reporting", "a", day(5), "r", true},
		{"root has no parent", "reporting", "r", day(5), "", true},
		{"end is exclusive", "reporting", "a", day(10), "", false},
		{"gap between versions", "reporting", "a", day(15), "", false},
		{"before first version", "reporting", "a", day(-1), "", false},
		{"zero date", "reporting", "a", time.Time{}, "", false},
		{"open ended version", "reporting", "b", day(9999), "a", true},
		{"node absent from version", "reporting", "b", day(5), "", false},
		{"other purpose", "security", "a", day(5), "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := domain.ParentAsOf(versions, tc.purpose, tc.node, tc.date)
			if got != tc.wantParent || ok != tc.wantOK {
				t.Fatalf("got (%q,%v), want (%q,%v)", got, ok, tc.wantParent, tc.wantOK)
			}
		})
	}
}
