package store_test

import (
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	oracleSeed     = 6
	oracleUnits    = 1000
	oracleSamples  = 10000
	oracleMaxDepth = 14
	oracleP95Limit = 20 * time.Millisecond
)

// envPerfTest turns on the as-of p95 assertion. Without it the oracle still checks every sample
// against domain.ParentAsOf and logs the timings, but no wall-clock budget is asserted, since
// `go test ./...` runs packages in parallel against one database.
const envPerfTest = "PASSAGE_PERF_TEST"

var oraclePurposes = []string{"reporting", "procurement", "expenditure", "security", "centralized_payments"}

func oracleTree(rng *rand.Rand, units []string) ([]store.HierarchyNode, []domain.Node) {
	nodes := make([]store.HierarchyNode, len(units))
	pairs := make([]domain.Node, len(units))
	depth := make(map[string]int, len(units))
	for i, id := range units {
		nodes[i], pairs[i], depth[id] = store.HierarchyNode{OrgUnitID: id, SortOrder: i}, domain.Node{ID: id}, 1
		if i == 0 {
			continue
		}
		parent := units[rng.Intn(i)]
		if depth[parent] >= oracleMaxDepth {
			parent = units[0]
		}
		nodes[i].ParentOrgUnitID, pairs[i].ParentID, depth[id] = &parent, parent, depth[parent]+1
	}
	return nodes, pairs
}

func oracleSlots(rng *rand.Rand, units []string, slotCount int) [][]string {
	slots := make([][]string, slotCount)
	members := make([]map[string]bool, slotCount)
	for k := range slots {
		members[k] = map[string]bool{}
	}
	for _, u := range units {
		k := rng.Intn(slotCount)
		slots[k], members[k][u] = append(slots[k], u), true
	}
	for k := range slots {
		for extra := 100 + rng.Intn(200); extra > 0; extra-- {
			if u := units[rng.Intn(len(units))]; !members[k][u] {
				slots[k], members[k][u] = append(slots[k], u), true
			}
		}
		rng.Shuffle(len(slots[k]), func(i, j int) { slots[k][i], slots[k][j] = slots[k][j], slots[k][i] })
	}
	return slots
}

func TestPostgres_HierarchyAsOfOracle1k(t *testing.T) {
	s, ctx, pool := lifecycleStore(t)
	rng := rand.New(rand.NewSource(oracleSeed))
	units, codes := make([]string, oracleUnits), make([]string, oracleUnits)
	for i := range units {
		units[i], codes[i] = ids.New(), fmt.Sprintf("D%04d", i)
	}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into operating_unit (tenant_id, id, type, code, name)
			select $1::uuid, u.id::uuid, 'department', u.code, u.code from unnest($2::text[], $3::text[]) as u(id, code)`, testkit.TenantID(1), units, codes)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	starts := []time.Time{day(2025, 1, 1), day(2025, 7, 1), day(2026, 1, 1)}
	slots := oracleSlots(rng, units, len(oraclePurposes)*len(starts))
	typeIDs := make([]string, len(oraclePurposes))
	versions := make([][]domain.Version, len(oraclePurposes))
	for i, purpose := range oraclePurposes {
		created, err := s.CreateHierarchyType(ctx, store.HierarchyType{Code: fmt.Sprintf("T%d", i), Name: purpose, Purposes: []string{purpose}}, "")
		if err != nil {
			t.Fatal(err)
		}
		typeIDs[i] = created.ID
		for v, from := range starts {
			nodes, pairs := oracleTree(rng, slots[i*len(starts)+v])
			mustPublish(t, s, ctx, draftWithNodes(t, s, ctx, created.ID, nodes), from)
			oracle := domain.Version{Purpose: purpose, EffectiveFrom: from, Nodes: pairs}
			if v+1 < len(starts) {
				oracle.EffectiveTo = starts[v+1]
			}
			versions[i] = append(versions[i], oracle)
		}
	}

	first, last := day(2024, 12, 1), day(2026, 12, 31)
	span := int(last.Sub(first).Hours()/24) + 1
	latencies := make([]time.Duration, 0, oracleSamples)
	mismatches := 0
	report := func(format string, args ...any) {
		if mismatches++; mismatches <= 5 {
			t.Errorf(format, args...)
		}
	}
	for range oracleSamples {
		i := rng.Intn(len(oraclePurposes))
		date := first.AddDate(0, 0, rng.Intn(span))
		unitID := units[rng.Intn(oracleUnits)]
		if rng.Intn(2) == 0 {
			in := versions[i][rng.Intn(len(starts))].Nodes
			unitID = in[rng.Intn(len(in))].ID
		}
		started := time.Now()
		got, err := s.HierarchyAsOf(ctx, typeIDs[i], date, unitID)
		latencies = append(latencies, time.Since(started))
		parent, ok := domain.ParentAsOf(versions[i], oraclePurposes[i], unitID, date)
		if !ok {
			wantCode := store.CodeHierarchyNodeAbsent
			if date.Before(starts[0]) {
				wantCode = store.CodeHierarchyNoVersion
			}
			if code := problemCode(t, err); code != wantCode {
				report("type %d unit %s on %s: error %s; want %s", i, unitID, date.Format(time.DateOnly), code, wantCode)
			}
			continue
		}
		if err != nil {
			report("type %d unit %s on %s: unexpected error %v", i, unitID, date.Format(time.DateOnly), err)
			continue
		}
		if (parent == "") != (got.ParentOrgUnitID == nil) || (parent != "" && *got.ParentOrgUnitID != parent) {
			report("type %d unit %s on %s: parent %v; want %q", i, unitID, date.Format(time.DateOnly), got.ParentOrgUnitID, parent)
		}
		wantAncestors := []store.AsOfRelative{}
		for up := parent; up != ""; {
			wantAncestors = append(wantAncestors, store.AsOfRelative{OrgUnitID: up, Depth: len(wantAncestors) + 1})
			up, _ = domain.ParentAsOf(versions[i], oraclePurposes[i], up, date)
		}
		if !reflect.DeepEqual(got.Ancestors, wantAncestors) {
			report("type %d unit %s on %s: ancestors %v; want %v", i, unitID, date.Format(time.DateOnly), got.Ancestors, wantAncestors)
		}
	}
	if mismatches != 0 {
		t.Fatalf("%d of %d samples disagree with domain.ParentAsOf", mismatches, oracleSamples)
	}
	slices.Sort(latencies)
	p95 := latencies[oracleSamples*95/100-1]
	t.Logf("as-of p95 = %v, max = %v", p95, latencies[oracleSamples-1])
	if os.Getenv(envPerfTest) == "" {
		t.Logf("p95 budget %v not asserted; set %s=1 to assert it", oracleP95Limit, envPerfTest)
		return
	}
	if p95 >= oracleP95Limit {
		t.Fatalf("as-of p95 = %v; want < %v", p95, oracleP95Limit)
	}
}
