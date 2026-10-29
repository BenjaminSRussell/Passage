package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// fixtureSite is the site of the fixture asset (setup).
var fixtureSite = testkit.TenantID(9)

func (f fixture) location(t *testing.T, code, parent, key string) FunctionalLocation {
	t.Helper()
	in := FunctionalLocation{Code: code, Name: code, SiteID: fixtureSite}
	if parent != "" {
		in.ParentID = &parent
	}
	l, err := f.s.CreateLocation(f.ctx, in, key)
	if err != nil {
		t.Fatalf("location %s: %v", code, err)
	}
	return l
}

func (f fixture) assetVersion(t *testing.T, id string) int {
	t.Helper()
	a, err := f.s.GetAsset(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Version
}

// TestLocations_Hierarchy covers FR-44.1 and EAM-FL-001: roots and children per
// site, a depth-12 chain accepted and depth 13 refused, a path unique per site
// (EAM-DUP-001) but reusable at another site, a parent in another site refused
// (EAM-FL-002), the path-ordered list with paging and subtree filter, and the
// detail with children.
func TestLocations_Hierarchy(t *testing.T) {
	f := setup(t)
	root := f.location(t, "PLANT-1", "", "fl-root")
	if root.Depth != 1 || root.Path != "PLANT-1" || root.ParentID != nil {
		t.Fatalf("root = %+v", root)
	}
	if again, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "PLANT-1", Name: "x", SiteID: fixtureSite}, "fl-root"); err != nil || again.ID != root.ID {
		t.Fatalf("replayed key = %+v %v, want the stored root", again, err)
	}
	if _, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "PLANT-1", Name: "dup", SiteID: fixtureSite}, "fl-dup"); problemCode(err) != CodeDuplicate {
		t.Fatalf("duplicate path: %v, want %s", err, CodeDuplicate)
	}
	other := testkit.TenantID(10)
	if _, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "PLANT-1", Name: "Plant 1 of site 10", SiteID: other}, "fl-other"); err != nil {
		t.Fatalf("same path at another site: %v", err)
	}
	if _, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "X", Name: "x", SiteID: other, ParentID: &root.ID}, "fl-x"); problemCode(err) != domain.CodeLocationRule {
		t.Fatalf("parent in another site: %v, want %s", err, domain.CodeLocationRule)
	}
	missing := testkit.TenantID(77)
	if _, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "X", Name: "x", SiteID: fixtureSite, ParentID: &missing}, "fl-missing"); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown parent: %v, want %s", err, CodeNotFound)
	}

	parent := root
	for depth := 2; depth <= domain.MaxLocationDepth; depth++ {
		parent = f.location(t, fmt.Sprintf("L%02d", depth), parent.ID, fmt.Sprintf("fl-%d", depth))
		if parent.Depth != depth {
			t.Fatalf("depth = %d, want %d", parent.Depth, depth)
		}
	}
	if parent.Path != "PLANT-1/L02/L03/L04/L05/L06/L07/L08/L09/L10/L11/L12" {
		t.Fatalf("deepest path = %q", parent.Path)
	}
	if _, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "TOO_DEEP", Name: "x", SiteID: fixtureSite,
		ParentID: &parent.ID}, "fl-13"); problemCode(err) != domain.CodeLocationDepth {
		t.Fatalf("depth 13: %v, want %s", err, domain.CodeLocationDepth)
	}
	// A sibling whose code holds the LIKE wildcard "_" stays out of PLANT-1's subtree.
	f.location(t, "PLANT_1", "", "fl-wild")
	l02 := f.location(t, "L02B", root.ID, "fl-l02b")

	first, err := f.s.ListLocations(f.ctx, LocationListOptions{SiteID: fixtureSite, Limit: 5})
	if err != nil || len(first.Items) != 5 || first.NextCursor == "" {
		t.Fatalf("first page = %+v %v", first, err)
	}
	if first.Items[0].Path != "PLANT-1" || first.Items[1].Path != "PLANT-1/L02" {
		t.Fatalf("not in path order: %q, %q", first.Items[0].Path, first.Items[1].Path)
	}
	var all []FunctionalLocation
	all = append(all, first.Items...)
	cursor := first.NextCursor
	for cursor != "" {
		next, err := f.s.ListLocations(f.ctx, LocationListOptions{SiteID: fixtureSite, Limit: 5, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, next.Items...)
		cursor = next.NextCursor
	}
	if len(all) != domain.MaxLocationDepth+2 {
		t.Fatalf("site 9 has %d locations over the pages, want %d", len(all), domain.MaxLocationDepth+2)
	}
	sub, err := f.s.ListLocations(f.ctx, LocationListOptions{Under: root.ID, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range sub.Items {
		if l.Path == "PLANT_1" || l.SiteID != fixtureSite {
			t.Fatalf("subtree of PLANT-1 holds %q of site %s", l.Path, l.SiteID)
		}
	}
	if len(sub.Items) != domain.MaxLocationDepth+1 {
		t.Fatalf("subtree has %d locations, want %d", len(sub.Items), domain.MaxLocationDepth+1)
	}
	if _, err := f.s.ListLocations(f.ctx, LocationListOptions{SiteID: other, Under: root.ID, Limit: 5}); problemCode(err) != domain.CodeLocationRule {
		t.Fatalf("subtree of another site's location: %v, want %s", err, domain.CodeLocationRule)
	}
	d, err := f.s.GetLocation(f.ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Children) != 2 || d.Children[0].Code != "L02" || d.Children[1].ID != l02.ID || len(d.Assets) != 0 {
		t.Fatalf("root detail = %+v", d)
	}
}

// TestLocations_PlacementHistory covers FR-44.2 install/remove history: install,
// replay, key reuse, no-op and other-site refusals (EAM-FL-002), stale versions,
// move and remove, the newest-first history with paths, and disposal removing
// the asset from its location.
func TestLocations_PlacementHistory(t *testing.T) {
	f := setup(t)
	bay1 := f.location(t, "BAY-1", "", "fl-bay1")
	bay2 := f.location(t, "BAY-2", "", "fl-bay2")
	elsewhere, err := f.s.CreateLocation(f.ctx, FunctionalLocation{Code: "BAY-9", Name: "Other site", SiteID: testkit.TenantID(10)}, "fl-bay9")
	if err != nil {
		t.Fatal(err)
	}
	v := f.assetVersion(t, f.asset)
	installed, err := f.s.PlaceAsset(f.ctx, f.asset, bay1.ID, v, "place-1")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Version != v+1 || installed.FunctionalLocationID == nil || *installed.FunctionalLocationID != bay1.ID {
		t.Fatalf("installed = %+v", installed)
	}
	if replay, err := f.s.PlaceAsset(f.ctx, f.asset, bay1.ID, v, "place-1"); err != nil || replay.Version != installed.Version {
		t.Fatalf("replay = %+v %v, want the current asset", replay, err)
	}
	other, err := f.s.CreateAsset(f.ctx, Asset{Name: "Spare pump", AssetType: "PUMP", SiteID: fixtureSite, Criticality: 2}, "asset-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PlaceAsset(f.ctx, other.ID, bay1.ID, other.Version, "place-1"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key reused on another asset: %v, want %s", err, CodeKeyReuse)
	}
	if _, err := f.s.PlaceAsset(f.ctx, f.asset, bay1.ID, installed.Version, "place-2"); problemCode(err) != domain.CodeLocationRule {
		t.Fatalf("no-op move: %v, want %s", err, domain.CodeLocationRule)
	}
	if _, err := f.s.PlaceAsset(f.ctx, f.asset, elsewhere.ID, installed.Version, "place-3"); problemCode(err) != domain.CodeLocationRule {
		t.Fatalf("location of another site: %v, want %s", err, domain.CodeLocationRule)
	}
	if _, err := f.s.PlaceAsset(f.ctx, f.asset, bay2.ID, v, "place-4"); problemCode(err) != CodeVersion {
		t.Fatalf("stale version: %v, want %s", err, CodeVersion)
	}
	moved, err := f.s.PlaceAsset(f.ctx, f.asset, bay2.ID, installed.Version, "place-5")
	if err != nil {
		t.Fatal(err)
	}
	if detail, err := f.s.GetLocation(f.ctx, bay2.ID); err != nil || len(detail.Assets) != 1 || detail.Assets[0].ID != f.asset {
		t.Fatalf("BAY-2 assets = %+v %v", detail.Assets, err)
	}
	if a, err := f.s.GetAsset(f.ctx, f.asset); err != nil || a.Location == nil || a.Location.Path != "BAY-2" {
		t.Fatalf("asset detail location = %+v %v", a.Location, err)
	}
	removed, err := f.s.PlaceAsset(f.ctx, f.asset, "", moved.Version, "place-6")
	if err != nil || removed.FunctionalLocationID != nil {
		t.Fatalf("remove = %+v %v", removed, err)
	}
	if _, err := f.s.PlaceAsset(f.ctx, f.asset, "", removed.Version, "place-7"); problemCode(err) != domain.CodeLocationRule {
		t.Fatalf("removing an asset that is not installed: %v, want %s", err, domain.CodeLocationRule)
	}
	history, err := f.s.ListLocationHistory(f.ctx, f.asset)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range history {
		from, to := "", ""
		if h.FromPath != nil {
			from = *h.FromPath
		}
		if h.ToPath != nil {
			to = *h.ToPath
		}
		got = append(got, h.Action+" "+from+">"+to)
		if h.Actor != "user-test" {
			t.Fatalf("history actor = %q", h.Actor)
		}
	}
	if want := []string{"REMOVE BAY-2>", "MOVE BAY-1>BAY-2", "INSTALL >BAY-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v, want %v", got, want)
	}

	// Disposal removes an installed asset from its location; a DISPOSED asset cannot be placed.
	spare, err := f.s.PlaceAsset(f.ctx, other.ID, bay1.ID, other.Version, "place-8")
	if err != nil {
		t.Fatal(err)
	}
	disposed, err := f.s.ChangeAssetLifecycle(f.ctx, other.ID, "DISPOSED", spare.Version)
	if err != nil || disposed.FunctionalLocationID != nil {
		t.Fatalf("disposed = %+v %v, want no location", disposed, err)
	}
	if h, err := f.s.ListLocationHistory(f.ctx, other.ID); err != nil || len(h) != 2 || h[0].Action != "REMOVE" || *h[0].FromPath != "BAY-1" {
		t.Fatalf("disposed asset history = %+v %v", h, err)
	}
	if _, err := f.s.PlaceAsset(f.ctx, other.ID, bay1.ID, disposed.Version, "place-9"); problemCode(err) != CodeAssetState {
		t.Fatalf("placing a DISPOSED asset: %v, want %s", err, CodeAssetState)
	}
}

// abcSequence is the BR-44.10 example on the fixture asset.
func (f fixture) abcSequence(t *testing.T, code, key string) MaintenanceSequence {
	t.Helper()
	seq, err := f.s.CreateSequence(f.ctx, MaintenanceSequence{Code: code, Name: "Pump sequence", AssetID: &f.asset,
		BaseIntervalValue: 250, CounterID: &f.counter, Steps: []domain.SequenceStep{
			{Code: "A", JobType: "LUBE", Multiple: 1, Hierarchy: 1},
			{Code: "B", JobType: "INSPECT", Multiple: 4, Hierarchy: 2, Includes: []string{"A"}},
			{Code: "C", JobType: "OVERHAUL", Multiple: 12, Hierarchy: 3, Includes: []string{"A", "B"}},
		}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// TestSequences_AC4410 covers AC-44.10 end to end: occurrences 1-12 of A×1,
// B×4 (includes A), C×12 (includes A, B) open work orders for A, A, A, B, A, A,
// A, B, A, A, A, C; a new occurrence waits for the previous order (EAM-SEQ-002);
// a repeated key returns the stored occurrence; the simulation matches.
func TestSequences_AC4410(t *testing.T) {
	f := setup(t)
	seq := f.abcSequence(t, "PUMP-SEQ", "seq-1")
	if seq.LastOccurrenceNumber != 0 || len(seq.Steps) != 3 || seq.Steps[0].Includes == nil {
		t.Fatalf("created = %+v", seq)
	}
	sim, err := f.s.SimulateSequence(f.ctx, seq.ID, 12)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A", "A", "A", "B", "A", "A", "A", "B", "A", "A", "A", "C"}
	var simulated, generated []string
	for _, o := range sim {
		simulated = append(simulated, o.Steps[0])
	}
	if !reflect.DeepEqual(simulated, want) {
		t.Fatalf("simulation = %v, want %v", simulated, want)
	}
	var first SequenceOccurrence
	for k := 1; k <= 12; k++ {
		occ, err := f.s.GenerateOccurrence(f.ctx, seq.ID, OccurrenceInput{Currency: "USD"}, fmt.Sprintf("occ-%d", k))
		if err != nil {
			t.Fatalf("occurrence %d: %v", k, err)
		}
		if k == 1 {
			first = occ
			if _, err := f.s.GenerateOccurrence(f.ctx, seq.ID, OccurrenceInput{Currency: "USD"}, "occ-early"); problemCode(err) != CodeSequenceOpen {
				t.Fatalf("occurrence while the previous order is open: %v, want %s", err, CodeSequenceOpen)
			}
		}
		if occ.Number != k || occ.WorkOrderID == nil || len(occ.Jobs) != 1 {
			t.Fatalf("occurrence %d = %+v", k, occ)
		}
		wo, err := f.s.GetWorkOrder(f.ctx, *occ.WorkOrderID)
		if err != nil {
			t.Fatal(err)
		}
		if wo.Type != "PREVENTIVE" || wo.JobType != occ.Jobs[0] || wo.AssetID != f.asset {
			t.Fatalf("occurrence %d order = %+v", k, wo.WorkOrder)
		}
		generated = append(generated, occ.Steps[0])
		if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCanceled, Version: wo.Version, Actor: "planner-1"},
			fmt.Sprintf("cancel-%d", k)); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(generated, want) {
		t.Fatalf("generated = %v, want %v", generated, want)
	}
	if replay, err := f.s.GenerateOccurrence(f.ctx, seq.ID, OccurrenceInput{Currency: "USD"}, "occ-1"); err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %+v %v, want occurrence 1", replay, err)
	}
	after, err := f.s.GetSequence(f.ctx, seq.ID)
	if err != nil || after.LastOccurrenceNumber != 12 || after.Version != 13 {
		t.Fatalf("sequence after 12 = %+v %v", after, err)
	}
	occs, err := f.s.ListOccurrences(f.ctx, seq.ID)
	if err != nil || len(occs) != 12 || occs[0].Number != 12 || occs[0].Jobs[0] != "OVERHAUL" {
		t.Fatalf("occurrences = %d %v", len(occs), err)
	}
	// Every work order came with its created event, in the same transaction.
	if n := f.count(t, `select count(*) from outbox where type = $1 and aggregate_id in
	    (select work_order_id::text from sequence_occurrence where sequence_id = $2)`, eamevents.WorkOrderCreated, seq.ID); n != 12 {
		t.Fatalf("workorder.created events = %d, want 12", n)
	}
	other := f.abcSequence(t, "PUMP-SEQ-2", "seq-2")
	if _, err := f.s.GenerateOccurrence(f.ctx, other.ID, OccurrenceInput{Currency: "USD"}, "occ-1"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key reused on another sequence: %v, want %s", err, CodeKeyReuse)
	}
}

// TestSequences_Rules covers the sequence definition rules: an extra due step not
// included by the selected one is kept, asset type sequences are templates,
// counters belong to the sequence's asset, codes are unique, and invalid steps
// or scope are refused with EAM-SEQ-001.
func TestSequences_Rules(t *testing.T) {
	f := setup(t)
	days := "DAYS"
	pump := "PUMP"
	seq, err := f.s.CreateSequence(f.ctx, MaintenanceSequence{Code: "SAFETY", Name: "Safety and test", AssetID: &f.asset,
		BaseIntervalValue: 7, BaseIntervalUnit: &days, Steps: []domain.SequenceStep{
			{Code: "S", JobType: "SAFETY", Multiple: 1, Hierarchy: 1},
			{Code: "T", JobType: "TEST", Multiple: 1, Hierarchy: 2},
		}}, "seq-st")
	if err != nil {
		t.Fatal(err)
	}
	occ, err := f.s.GenerateOccurrence(f.ctx, seq.ID, OccurrenceInput{Currency: "EUR"}, "occ-st")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(occ.Steps, []string{"T", "S"}) || !reflect.DeepEqual(occ.Jobs, []string{"TEST", "SAFETY"}) {
		t.Fatalf("occurrence = %+v, want T then the extra S", occ)
	}
	if wo, err := f.s.GetWorkOrder(f.ctx, *occ.WorkOrderID); err != nil || wo.JobType != "TEST" || wo.Currency != "EUR" {
		t.Fatalf("order = %+v %v", wo.WorkOrder, err)
	}

	template, err := f.s.CreateSequence(f.ctx, MaintenanceSequence{Code: "PUMP-TPL", Name: "Pump template", AssetType: &pump,
		BaseIntervalValue: 3, BaseIntervalUnit: ptr("MONTHS"), Steps: []domain.SequenceStep{{Code: "A", JobType: "LUBE", Multiple: 1, Hierarchy: 1}}}, "seq-tpl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GenerateOccurrence(f.ctx, template.ID, OccurrenceInput{Currency: "USD"}, "occ-tpl"); problemCode(err) != domain.CodeSequenceStep {
		t.Fatalf("occurrence on an asset type sequence: %v, want %s", err, domain.CodeSequenceStep)
	}
	if sim, err := f.s.SimulateSequence(f.ctx, template.ID, 3); err != nil || len(sim) != 3 || sim[2].Number != 3 {
		t.Fatalf("template simulation = %+v %v", sim, err)
	}

	other, err := f.s.CreateAsset(f.ctx, Asset{Name: "Fan", AssetType: "FAN", SiteID: fixtureSite, Criticality: 2}, "asset-fan")
	if err != nil {
		t.Fatal(err)
	}
	one := []domain.SequenceStep{{Code: "A", JobType: "LUBE", Multiple: 1, Hierarchy: 1}}
	cases := []struct {
		name string
		in   MaintenanceSequence
		code string
	}{
		{"duplicate code", MaintenanceSequence{Code: "SAFETY", Name: "x", AssetID: &f.asset, BaseIntervalValue: 1, BaseIntervalUnit: &days, Steps: one}, CodeDuplicate},
		{"counter of another asset", MaintenanceSequence{Code: "FAN-SEQ", Name: "x", AssetID: &other.ID, BaseIntervalValue: 1, CounterID: &f.counter, Steps: one}, domain.CodeSequenceStep},
		{"asset and asset type", MaintenanceSequence{Code: "BOTH", Name: "x", AssetID: &f.asset, AssetType: &pump, BaseIntervalValue: 1, BaseIntervalUnit: &days, Steps: one}, domain.CodeSequenceStep},
		{"unit and counter", MaintenanceSequence{Code: "UNIT-CTR", Name: "x", AssetID: &f.asset, BaseIntervalValue: 1, BaseIntervalUnit: &days, CounterID: &f.counter, Steps: one}, domain.CodeSequenceStep},
		{"counter on an asset type", MaintenanceSequence{Code: "TYPE-CTR", Name: "x", AssetType: &pump, BaseIntervalValue: 1, CounterID: &f.counter, Steps: one}, domain.CodeSequenceStep},
		{"unknown include", MaintenanceSequence{Code: "BAD-INC", Name: "x", AssetID: &f.asset, BaseIntervalValue: 1, BaseIntervalUnit: &days,
			Steps: []domain.SequenceStep{{Code: "A", JobType: "LUBE", Multiple: 1, Hierarchy: 1, Includes: []string{"Z"}}}}, domain.CodeSequenceStep},
	}
	for _, tc := range cases {
		if _, err := f.s.CreateSequence(f.ctx, tc.in, "seq-"+tc.in.Code); problemCode(err) != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
	}
	// A sequence on an asset that is not ACTIVE cannot open work orders (EAM-AST-001).
	fan, err := f.s.CreateSequence(f.ctx, MaintenanceSequence{Code: "FAN-TIME", Name: "Fan", AssetID: &other.ID,
		BaseIntervalValue: 1, BaseIntervalUnit: &days, Steps: one}, "seq-fan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GenerateOccurrence(f.ctx, fan.ID, OccurrenceInput{Currency: "USD"}, "occ-fan"); problemCode(err) != CodeAssetState {
		t.Fatalf("occurrence on a CREATED asset: %v, want %s", err, CodeAssetState)
	}
	page, err := f.s.ListSequences(f.ctx, ListOptions{AssetID: f.asset, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != seq.ID {
		t.Fatalf("sequences of the pump = %+v %v", page, err)
	}
}

// TestGrants_AppRoleLocationsSequences runs the slice 4 paths as passage_app (no
// BYPASSRLS, migration 0006 grants): locations, placement with its history, a
// sequence row lock and occurrence with its work order, and the append-only
// history and occurrence tables refusing updates and deletes.
func TestGrants_AppRoleLocationsSequences(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(app, func() time.Time { return clock })
	ctx := testkit.Ctx(testkit.TenantID(1))
	a, err := s.CreateAsset(ctx, Asset{Name: "Press", AssetType: "PRESS", SiteID: fixtureSite, Criticality: 3}, "g-asset")
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.ActivateAsset(ctx, a.ID, a.Version)
	if err != nil {
		t.Fatal(err)
	}
	root, err := s.CreateLocation(ctx, FunctionalLocation{Code: "PLANT", Name: "Plant", SiteID: fixtureSite}, "g-root")
	if err != nil {
		t.Fatalf("location insert as passage_app: %v", err)
	}
	line, err := s.CreateLocation(ctx, FunctionalLocation{Code: "LINE-1", Name: "Line 1", SiteID: fixtureSite, ParentID: &root.ID}, "g-line")
	if err != nil {
		t.Fatal(err)
	}
	if page, err := s.ListLocations(ctx, LocationListOptions{Under: root.ID, Limit: 10}); err != nil || len(page.Items) != 2 {
		t.Fatalf("subtree as passage_app = %+v %v", page, err)
	}
	placed, err := s.PlaceAsset(ctx, a.ID, line.ID, a.Version, "g-place")
	if err != nil {
		t.Fatalf("placement as passage_app: %v", err)
	}
	if _, err := s.PlaceAsset(ctx, a.ID, root.ID, placed.Version, "g-move"); err != nil {
		t.Fatal(err)
	}
	if h, err := s.ListLocationHistory(ctx, a.ID); err != nil || len(h) != 2 {
		t.Fatalf("history as passage_app = %+v %v", h, err)
	}
	days := "DAYS"
	seq, err := s.CreateSequence(ctx, MaintenanceSequence{Code: "PRESS-SEQ", Name: "Press", AssetID: &a.ID, BaseIntervalValue: 30,
		BaseIntervalUnit: &days, Steps: []domain.SequenceStep{{Code: "A", JobType: "LUBE", Multiple: 1, Hierarchy: 1}}}, "g-seq")
	if err != nil {
		t.Fatalf("sequence insert as passage_app: %v", err)
	}
	if occ, err := s.GenerateOccurrence(ctx, seq.ID, OccurrenceInput{Currency: "USD"}, "g-occ"); err != nil || occ.WorkOrderID == nil {
		t.Fatalf("occurrence (row lock, update, work order) as passage_app = %+v %v", occ, err)
	}
	for _, stmt := range []string{
		`update asset_location_history set actor = actor`,
		`delete from asset_location_history`,
		`update sequence_occurrence set actor = actor`,
		`delete from sequence_occurrence`,
		`update functional_location set name = name`,
		`delete from maintenance_sequence`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
	// Another tenant sees none of it.
	two := testkit.Ctx(testkit.TenantID(2))
	if _, err := s.GetLocation(two, root.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 reading tenant 1's location: %v, want %s", err, CodeNotFound)
	}
	if page, err := s.ListLocations(two, LocationListOptions{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 locations = %+v %v, want none", page, err)
	}
	if _, err := s.GetSequence(two, seq.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 reading tenant 1's sequence: %v, want %s", err, CodeNotFound)
	}
	if _, err := s.ListLocationHistory(two, a.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 reading tenant 1's history: %v, want %s", err, CodeNotFound)
	}
}
