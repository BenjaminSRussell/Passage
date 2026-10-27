package store

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	ccevents "github.com/BenjaminSRussell/passage/services/costacct/internal/events"
)

func (f ledgerFixture) hierarchy(t *testing.T, code, kind string) string {
	t.Helper()
	status, raw, err := f.st.CreateHierarchy(f.ctx, ids.New(), "h", Hierarchy{LegalEntityID: f.le, Code: code, Name: code, Type: kind})
	if err != nil || status != http.StatusCreated {
		t.Fatalf("create hierarchy %s: %v", code, err)
	}
	return idOf(t, raw)
}

func (f ledgerFixture) node(t *testing.T, hierarchyID, parentID, objectID, basis string) string {
	t.Helper()
	_, raw, err := f.st.CreateHierarchyNode(f.ctx, ids.New(), "h", HierarchyNode{HierarchyID: hierarchyID, ParentNodeID: parentID, CostObjectID: objectID,
		Collects: basis != "", DistributionBasis: basis})
	if err != nil {
		t.Fatalf("create node: %v", err)
	}
	return idOf(t, raw)
}

func (f ledgerFixture) collect(t *testing.T, nodeID, amount string) (Collection, error) {
	t.Helper()
	_, raw, err := f.st.CollectAtNode(f.ctx, ids.New(), "h", CollectRequest{NodeID: nodeID, Period: "2026-09", Currency: "USD",
		CostElementID: f.primary, Amount: dec(amount)})
	var c Collection
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}

func (f ledgerFixture) distribute(t *testing.T, key, nodeID string, children ...domain.HierarchyChild) (DistributionRun, error) {
	t.Helper()
	_, raw, err := f.st.RunHierarchyDistribution(f.ctx, key, "h-"+nodeID, DistributionRequest{NodeID: nodeID, Period: "2026-09", Currency: "USD",
		CostElementID: f.primary, Children: children})
	var r DistributionRun
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	return r, err
}

func child(id, basis string) domain.HierarchyChild {
	return domain.HierarchyChild{NodeID: id, Basis: dec(basis)}
}

// FR-38.18: costs collected at a product group node are distributed at period
// end to its products by fixed percentage, a residual stays on the node with
// CO-0645, and a collecting product node passes its share down to an order.
// Every run nets to zero, so the cost ledger total never changes.
func TestHierarchy_CollectAndDistribute(t *testing.T) {
	f := newLedger(t, 6)
	group := f.object(t, "Product", "PG-PUMPS")
	pumpA := f.object(t, "Product", "P-100")
	pumpB := f.object(t, "Product", "P-200")
	order := f.object(t, "InternalOrder", "PO-7781")
	h := f.hierarchy(t, "PUMPS", domain.HierarchyCollective)
	root := f.node(t, h, "", group, domain.BasisFixedPercent)
	nodeA := f.node(t, h, root, pumpA, domain.BasisActualOutput)
	nodeB := f.node(t, h, root, pumpB, "")
	nodeOrder := f.node(t, h, nodeA, order, "")

	// Structure rules.
	if _, _, err := f.st.CreateHierarchyNode(f.ctx, ids.New(), "h", HierarchyNode{HierarchyID: h, ParentNodeID: root, CostObjectID: pumpA}); !hasCode(err, CodeNodeExists) {
		t.Fatalf("same object twice: %v, want %s", err, CodeNodeExists)
	}
	reporting := f.hierarchy(t, "REPORT", domain.HierarchyReporting)
	if _, _, err := f.st.CreateHierarchyNode(f.ctx, ids.New(), "h", HierarchyNode{HierarchyID: reporting, CostObjectID: group, Collects: true,
		DistributionBasis: domain.BasisFixedPercent}); !hasCode(err, CodeNodeNotCollecting) {
		t.Fatalf("collecting node in a Reporting hierarchy: %v, want %s", err, CodeNodeNotCollecting)
	}
	if _, _, err := f.st.CreateHierarchyNode(f.ctx, ids.New(), "h", HierarchyNode{HierarchyID: reporting, ParentNodeID: root, CostObjectID: pumpA}); !hasCode(err, CodeNodeNotFound) {
		t.Fatalf("parent from another hierarchy: %v, want %s", err, CodeNodeNotFound)
	}
	// The same cost object may be a node of another hierarchy.
	f.node(t, reporting, "", group, "")
	if _, _, err := f.st.CreateHierarchy(f.ctx, ids.New(), "h", Hierarchy{LegalEntityID: f.le, Code: "PUMPS", Name: "again", Type: domain.HierarchyReporting}); !hasCode(err, CodeDuplicateCode) {
		t.Fatalf("duplicate hierarchy code: %v", err)
	}

	// Collection: only collecting nodes, and a correction may not go below zero.
	if _, err := f.collect(t, nodeB, "10.00"); !hasCode(err, CodeNodeNotCollecting) {
		t.Fatalf("collect on a non-collecting node: %v", err)
	}
	if _, err := f.collect(t, root, "-0.01"); !hasCode(err, CodeCollectionBelowZero) {
		t.Fatalf("correction below zero: %v", err)
	}
	if c, err := f.collect(t, root, "1000.00"); err != nil || !c.Balance.Equal(dec("1000")) || c.Behavior != "Unclassified" {
		t.Fatalf("collect: %+v (%v)", c, err)
	}
	if c, err := f.collect(t, root, "-100.00"); err != nil || !c.Balance.Equal(dec("900")) {
		t.Fatalf("correction: %+v (%v)", c, err)
	}

	// Root distributes 60 % / 30 %: 540.00 / 270.00, 90.00 stays with CO-0645.
	key := ids.New()
	run, err := f.distribute(t, key, root, child(nodeA, "60"), child(nodeB, "30"))
	if err != nil {
		t.Fatal(err)
	}
	if !run.Amount.Equal(dec("900")) || !run.Distributed.Equal(dec("810")) || !run.Residual.Equal(dec("90")) ||
		!run.Shares[0].Amount.Equal(dec("540")) || run.Shares[0].CostObjectID != pumpA || !run.Shares[1].Amount.Equal(dec("270")) {
		t.Fatalf("run %+v", run)
	}
	if len(run.Warnings) != 1 || run.Warnings[0].Code != CodeUndistributed {
		t.Fatalf("warnings %+v, want CO-0645", run.Warnings)
	}
	// A replay of the key returns the same run and posts nothing more.
	if again, err := f.distribute(t, key, root, child(nodeA, "60"), child(nodeB, "30")); err != nil || again.ID != run.ID {
		t.Fatalf("replay %+v (%v)", again, err)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "hierarchy_run_id = $1", run.ID); !got.IsZero() {
		t.Fatalf("run entries net to %s, want 0", got)
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "cost_object_id = $1", pumpB); !got.Equal(dec("270")) {
		t.Fatalf("P-200 holds %s, want 270", got)
	}
	var residual, shares string
	if err := f.st.pool.QueryRow(f.ctx, `select payload->'data'->>'residual', jsonb_array_length(payload->'data'->'shares')::text from outbox
		where type = $1 and aggregate_id = $2`, ccevents.HierarchyDistributed, run.ID).Scan(&residual, &shares); err != nil || !dec(residual).Equal(dec("90")) || shares != "2" {
		t.Fatalf("hierarchy.distributed event: residual %q shares %q (%v)", residual, shares, err)
	}

	// Receivers must be children of the node; a grandchild is not.
	if _, err := f.distribute(t, ids.New(), root, child(nodeOrder, "100")); !hasCode(err, CodeInvalidDistribution) {
		t.Fatalf("grandchild receiver: %v, want %s", err, CodeInvalidDistribution)
	}
	if _, err := f.distribute(t, ids.New(), root, child(nodeA, "70"), child(nodeB, "40")); !hasCode(err, CodeInvalidDistribution) {
		t.Fatalf("percentages over 100: %v, want %s", err, CodeInvalidDistribution)
	}
	if _, err := f.distribute(t, ids.New(), nodeB, child(nodeA, "1")); !hasCode(err, CodeNodeNotCollecting) {
		t.Fatalf("distributing a non-collecting node: %v", err)
	}

	// The collecting product node passes what it received down by actual output.
	down, err := f.distribute(t, ids.New(), nodeA, child(nodeOrder, "12"))
	if err != nil || !down.Distributed.Equal(dec("540")) || !down.Residual.IsZero() || len(down.Warnings) != 0 {
		t.Fatalf("product to order: %+v (%v)", down, err)
	}
	// A later run distributes the residual; then nothing is left.
	if rest, err := f.distribute(t, ids.New(), root, child(nodeA, "50"), child(nodeB, "50")); err != nil || !rest.Amount.Equal(dec("90")) || !rest.Residual.IsZero() {
		t.Fatalf("residual run: %+v (%v)", rest, err)
	}
	if _, err := f.distribute(t, ids.New(), root, child(nodeA, "50"), child(nodeB, "50")); !hasCode(err, CodeNothingToDistribute) {
		t.Fatalf("empty node: %v, want %s", err, CodeNothingToDistribute)
	}

	// The ledger total on the element is the 900.00 collected, wherever it sits.
	if got := sumEntries(t, f.ctx, f.st.pool, "cost_element_id = $1", f.primary); !got.Equal(dec("900")) {
		t.Fatalf("ledger total %s, want 900", got)
	}
	balances, err := f.st.HierarchyBalances(f.ctx, h, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	held := map[string]string{}
	for _, b := range balances {
		held[b.NodeID] = b.Amount.StringFixed(2)
	}
	if len(held) != 3 || held[nodeA] != "45.00" || held[nodeB] != "315.00" || held[nodeOrder] != "540.00" {
		t.Fatalf("balances %v", held)
	}

	// Reads.
	got, err := f.st.GetHierarchy(f.ctx, h)
	if err != nil || got.NodeCount != 4 || len(got.Nodes) != 4 || got.Nodes[0].ID != root || got.Nodes[1].ParentNodeID != root || got.Nodes[0].DistributionBasis != domain.BasisFixedPercent {
		t.Fatalf("hierarchy %+v (%v)", got, err)
	}
	if _, err := f.st.GetHierarchy(f.ctx, ids.New()); !hasCode(err, CodeHierarchyNotFound) {
		t.Fatalf("missing hierarchy: %v", err)
	}
	if list, err := f.st.ListHierarchies(f.ctx, f.le); err != nil || len(list) != 2 || list[0].Code != "PUMPS" {
		t.Fatalf("hierarchies %+v (%v)", list, err)
	}
	if runs, err := f.st.ListDistributionRuns(f.ctx, "", h); err != nil || len(runs) != 3 {
		t.Fatalf("runs %d (%v)", len(runs), err)
	}
	stored, err := f.st.GetDistributionRun(f.ctx, run.ID)
	if err != nil || !stored.Residual.Equal(dec("90")) || len(stored.Warnings) != 1 || stored.Shares[1].CostObjectID != pumpB {
		t.Fatalf("stored run %+v (%v)", stored, err)
	}
	if _, err := f.st.GetDistributionRun(f.ctx, ids.New()); !hasCode(err, CodeHierarchyRunNotFound) {
		t.Fatalf("missing run: %v", err)
	}
	entries, err := f.st.EntriesFor(f.ctx, order, "2026-09")
	if err != nil || len(entries) != 1 || entries[0].Origin != "HierarchyDistribution" || entries[0].HierRunID != down.ID || entries[0].NodeID != nodeOrder {
		t.Fatalf("order entries %+v (%v)", entries, err)
	}
}

// Two distributions of the same node race: the node row lock lets one take the
// balance; the other finds nothing left.
func TestHierarchy_ConcurrentDistributionsDoNotDoubleSpend(t *testing.T) {
	f := newLedger(t, 7)
	h := f.hierarchy(t, "ORDERS", domain.HierarchyCollective)
	root := f.node(t, h, "", f.object(t, "CostCenter", "CC-PLANT"), domain.BasisStandardCost)
	kid := f.node(t, h, root, f.object(t, "InternalOrder", "IO-1"), "")
	if _, err := f.collect(t, root, "250.00"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.distribute(t, ids.New(), root, child(kid, "1"))
		}(i)
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("concurrent distributions: %v / %v, want exactly one to succeed", errs[0], errs[1])
	}
	if got := sumEntries(t, f.ctx, f.st.pool, "hierarchy_node_id = $1", kid); !got.Equal(dec("250")) {
		t.Fatalf("child holds %s, want 250", got)
	}
}

// Every hierarchy table is tenant-isolated: RLS enabled and forced, with a
// tenant policy and a legal entity scope.
func TestHierarchyTables_RowLevelSecurity(t *testing.T) {
	f := newLedger(t, 8)
	for _, table := range []string{"cost_object_hierarchy", "cost_hierarchy_node", "hierarchy_distribution_run"} {
		var enabled, forced bool
		var policies int
		if err := f.st.pool.QueryRow(f.ctx, `select c.relrowsecurity, c.relforcerowsecurity,
				(select count(*) from pg_policies p where p.schemaname = current_schema() and p.tablename = c.relname)
			from pg_class c where c.relname = $1 and c.relnamespace = current_schema()::regnamespace`, table).Scan(&enabled, &forced, &policies); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if !enabled || !forced || policies != 2 {
			t.Errorf("%s: rls %v forced %v policies %d", table, enabled, forced, policies)
		}
	}
}
