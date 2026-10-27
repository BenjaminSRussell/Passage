package store

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

const (
	dehp = "117-81-7"
	lead = "7439-92-1"
)

func outboxTypes(t *testing.T, pool *pgxpool.Pool, aggregateID string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by created_at, id`, aggregateID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}

// TestPostgres_ComplianceRoundTrip covers list versions with their event,
// composition versions, the BOM tree and result versioning (FR-33.13, BR-33.11).
func TestPostgres_ComplianceRoundTrip(t *testing.T) {
	s, pool, ctx := setup(t)
	list := domain.ComplianceList{Code: "REACH-SVHC", Name: "REACH Candidate List", Kind: domain.ListReporting, BlockMode: domain.BlockModeWarn,
		Regions: []string{"EU"}, Limits: []domain.LimitLine{{CAS: dehp, ThresholdPct: decimal.RequireFromString("0.1")}}}
	v1, inserted, err := s.CreateComplianceList(ctx, list, "l-1")
	if err != nil || !inserted || v1.Version != 1 || v1.Name != list.Name {
		t.Fatalf("create list: %+v %v", v1, err)
	}
	if replay, again, err := s.CreateComplianceList(ctx, list, "l-1"); err != nil || again || replay.ID != v1.ID {
		t.Fatalf("a replayed key returns the stored version: %+v %v", replay, err)
	}
	v2, _, err := s.CreateComplianceList(ctx, list, "l-2")
	if err != nil || v2.Version != 2 {
		t.Fatalf("second version: %+v %v", v2, err)
	}
	if types := outboxTypes(t, pool, v1.ID); len(types) != 1 || types[0] != qevents.ComplianceListCreated {
		t.Fatalf("list event: %v", types)
	}
	if all, err := s.ListComplianceLists(ctx, 10); err != nil || len(all) != 2 || all[0].Version != 2 {
		t.Fatalf("list versions newest first: %+v %v", all, err)
	}
	if cur, err := s.CurrentComplianceLists(ctx); err != nil || len(cur) != 1 || cur[0].ID != v2.ID {
		t.Fatalf("current lists: %+v %v", cur, err)
	}

	article, plasticiser, casing := ids.New(), ids.New(), ids.New()
	if _, _, err := s.SaveComposition(ctx, domain.Constituent{ItemID: plasticiser,
		Direct: map[string]decimal.Decimal{dehp: decimal.RequireFromString("0.024")}}, ""); err != nil {
		t.Fatal(err)
	}
	parts := []domain.MassPart{{ItemID: plasticiser, MassFraction: decimal.RequireFromString("0.05")}, {ItemID: casing, MassFraction: decimal.RequireFromString("0.9")}}
	if _, _, err := s.SaveComposition(ctx, domain.Constituent{ItemID: article, Parts: parts[:1]}, "c-1"); err != nil {
		t.Fatal(err)
	}
	cv2, _, err := s.SaveComposition(ctx, domain.Constituent{ItemID: article, Parts: parts}, "c-2")
	if err != nil || cv2.Version != 2 {
		t.Fatalf("second composition version = %+v %v", cv2, err)
	}
	if cur, err := s.GetComposition(ctx, article); err != nil || cur.Version != 2 || len(cur.Parts) != 2 || cur.Direct == nil {
		t.Fatalf("current composition: %+v %v", cur, err)
	}
	if _, err := s.GetComposition(ctx, ids.New()); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown composition must be 404, got %v", err)
	}

	tree, err := s.LoadCompositionTree(ctx, article)
	if err != nil || tree.RootVersion != 2 || len(tree.Nodes) != 3 || len(tree.Undeclared) != 1 || tree.Undeclared[0] != casing {
		t.Fatalf("tree: %+v %v", tree, err)
	}
	if _, err := s.LoadCompositionTree(ctx, casing); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("an undeclared root must be 404, got %v", err)
	}
	lists, _ := s.CurrentComplianceLists(ctx)
	res, err := domain.EvaluateItem(article, tree.Nodes, lists, "EU")
	if err != nil || len(res) != 1 || res[0].Code != domain.CodeReportingHit {
		t.Fatalf("0.05 × 2.4 %% = 0.12 %% w/w is a reporting hit: %+v %v", res, err)
	}
	stored, err := s.RecordComplianceResults(ctx, article, tree.RootVersion, res)
	if err != nil || stored[0].Version != 1 {
		t.Fatalf("first result: %+v %v", stored, err)
	}
	again, err := s.RecordComplianceResults(ctx, article, tree.RootVersion, res)
	if err != nil || again[0].Version != 1 {
		t.Fatalf("an unchanged outcome keeps version 1: %+v %v", again, err)
	}
	cleared := []domain.ComplianceResult{{ListCode: res[0].ListCode, ListVersion: res[0].ListVersion, ListKind: res[0].ListKind, Region: "EU", Applies: true, Hits: []domain.Hit{}}}
	next, err := s.RecordComplianceResults(ctx, article, 3, cleared)
	if err != nil || next[0].Version != 2 {
		t.Fatalf("a changed outcome is version 2: %+v %v", next, err)
	}
	if types := outboxTypes(t, pool, article); len(types) != 2 || types[0] != qevents.ComplianceResultRecorded {
		t.Fatalf("one result event per stored version: %v", types)
	}
}

// TestPostgres_StabilityStudyRoundTrip stores a study with its pull points and event.
func TestPostgres_StabilityStudyRoundTrip(t *testing.T) {
	s, pool, ctx := setup(t)
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "STAB-LT", Tests: []domain.TestLine{
		{TestID: ids.New(), Kind: domain.KindAttribute, Required: true, PassingOutcomes: []string{ids.New()}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	accel, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "STAB-ACC", Tests: group.Tests}, "")
	if err != nil {
		t.Fatal(err)
	}
	item := ids.New()
	st, err := domain.NewStudy(domain.StudySpec{ItemID: item, BatchID: "B-9", Protocol: "ICH Q1A(R2)", Condition: "25C/60RH",
		StartDate: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), TestGroupID: group.ID, PullMonths: []int{0, 3, 6},
		PullTestGroups: map[int]string{6: accel.ID}, LeadDays: 7, WindowDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	st.ID = ids.New()
	saved, inserted, err := s.CreateStudy(ctx, st, "st-1")
	if err != nil || !inserted || saved.ID != st.ID || len(saved.Pulls) != 3 || saved.Version != 1 || saved.CreatedAt.IsZero() {
		t.Fatalf("create study: %+v %v", saved, err)
	}
	replay := st
	replay.ID = ids.New()
	if again, ins, err := s.CreateStudy(ctx, replay, "st-1"); err != nil || ins || again.ID != saved.ID {
		t.Fatalf("a replayed key returns the stored study: %+v %v", again, err)
	}
	got, err := s.GetStudy(ctx, saved.ID)
	if err != nil || got.Status != domain.StudyPlanned || got.Pulls[1].PullDate.Format(time.DateOnly) != "2026-04-30" ||
		got.Pulls[1].OrderDate.Format(time.DateOnly) != "2026-04-23" || got.Pulls[2].TestGroupID != accel.ID || got.PullTestGroups[6] != accel.ID ||
		got.StartDate.Format(time.DateOnly) != "2026-01-31" {
		t.Fatalf("get study: %+v %v", got, err)
	}
	if types := outboxTypes(t, pool, saved.ID); len(types) != 1 || types[0] != qevents.StabilityStudyCreated {
		t.Fatalf("study event: %v", types)
	}
	if list, err := s.ListStudies(ctx, StudyFilter{Status: domain.StudyPlanned, ItemID: item, Limit: 10}); err != nil || len(list) != 1 || len(list[0].Pulls) != 3 {
		t.Fatalf("list studies: %+v %v", list, err)
	}
	if list, err := s.ListStudies(ctx, StudyFilter{Status: domain.StudyActive, Limit: 10}); err != nil || len(list) != 0 {
		t.Fatalf("no ACTIVE study: %+v %v", list, err)
	}
	if _, err := s.GetStudy(ctx, ids.New()); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("unknown study must be 404, got %v", err)
	}
	orphan := st
	orphan.ID, orphan.TestGroupID = ids.New(), ids.New()
	for i := range orphan.Pulls {
		orphan.Pulls[i].TestGroupID = orphan.TestGroupID
	}
	if _, _, err := s.CreateStudy(ctx, orphan, ""); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("an unknown test group must be 404, got %v", err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "QM_VIEW")
	if theirs, err := s.ListStudies(other, StudyFilter{Limit: 10}); err != nil || len(theirs) != 0 {
		t.Fatalf("tenant 2 sees no study of tenant 1: %d %v", len(theirs), err)
	}
}

// TestPostgres_SPCObservations reads first-attempt results of the newest orders of
// one item and test, whole orders only, oldest first.
func TestPostgres_SPCObservations(t *testing.T) {
	s, _, ctx := setup(t)
	testID := ids.New()
	group, _, err := s.CreateTestGroup(ctx, domain.TestGroup{Code: "SPC-1", Tests: []domain.TestLine{
		{TestID: testID, Kind: domain.KindVariable, Required: true, Decimals: 2, Min: dptr("9"), Max: dptr("11")}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	item := ids.New()
	var orderIDs []string
	for i := 0; i < 4; i++ {
		o := domain.Order{ID: ids.New(), Trigger: domain.TriggerManual, SourceRef: domain.SourceRef{Type: "manual", ID: ids.New()},
			ItemID: item, SiteID: ids.New(), WarehouseID: ids.New(), Dims: map[string]string{}, LotQty: decimal.NewFromInt(10),
			SampleQty: decimal.NewFromInt(2), UOM: "EA", TestGroupID: group.ID, TestGroupVersion: 1, SamplingMethod: domain.MethodFixed,
			Severity: domain.SeverityNormal}
		created, _, err := s.CreateOrder(ctx, o, "")
		if err != nil {
			t.Fatal(err)
		}
		orderIDs = append(orderIDs, created.ID)
		at := time.Date(2026, 3, 1, 8+i, 0, 0, 0, time.UTC)
		batch := []domain.Result{
			{TestID: testID, SampleNo: 1, Value: dptr("10.1"), Passed: true, RecordedBy: "u1", RecordedAt: at},
			{TestID: testID, SampleNo: 2, Value: dptr("9.9"), Passed: true, RecordedBy: "u1", RecordedAt: at},
		}
		if i == 3 {
			batch = append(batch, domain.Result{TestID: testID, SampleNo: 1, RetestNo: 1, Value: dptr("10.5"), Passed: true, RecordedBy: "u1", RecordedAt: at})
		}
		if _, _, err := s.RecordResults(ctx, created.ID, 1, domain.StatusPending, batch, ""); err != nil {
			t.Fatal(err)
		}
	}
	obs, err := s.ListSPCObservations(ctx, item, testID, 3)
	if err != nil || len(obs) != 6 {
		t.Fatalf("three newest orders of two first-attempt samples: %d %v", len(obs), err)
	}
	if obs[0].QualityOrderID != orderIDs[1] || obs[5].QualityOrderID != orderIDs[3] || !obs[0].HasValue || !obs[0].Value.Equal(decimal.RequireFromString("10.1")) {
		t.Fatalf("oldest of the newest first: %+v", obs)
	}
	if none, err := s.ListSPCObservations(ctx, ids.New(), testID, 3); err != nil || len(none) != 0 {
		t.Fatalf("no results: %+v %v", none, err)
	}
}

func dptr(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}
