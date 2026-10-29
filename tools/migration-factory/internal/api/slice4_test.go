package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// The workspace catalog lists the nine FR-49.1 packs for the four legacy systems, and its
// "available" flag is the same rule the pack-run route applies.
func TestPackCatalog_ListsNinePacksAndMatchesThePackRunRoute(t *testing.T) {
	h := New(unreachableStore{t}).Routes()
	rec := do(t, h, request{method: "GET", path: "/api/v1/migration/packs", tenant: tenantA, duties: "MigrationView"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	cat := decode[struct{ Items []catalogEntryDTO }](t, rec)
	if len(cat.Items) != len(domain.Packs) {
		t.Fatalf("%d packs, want %d", len(cat.Items), len(domain.Packs))
	}
	for i, e := range cat.Items {
		if e.Pack != string(domain.Packs[i]) || strings.Join(e.SourceSystems, ",") != "AX2012,D365FO,SAPECC,S4HANA" || e.ControlTotals == "" {
			t.Fatalf("entry %d %+v", i, e)
		}
		// An unavailable pack is refused with MIG-0005; an available one gets past that check and
		// is refused by validation for its empty body, before the store.
		r := do(t, h, request{method: "POST", path: "/api/v1/migration/packs/" + e.Pack + "/runs", tenant: tenantA, duties: allDuties, body: `{}`})
		if got := problemCode(t, r); (got == CodeUnsupported) == e.Available {
			t.Fatalf("pack %s available=%v but the run route answered %d %s", e.Pack, e.Available, r.Code, r.Body)
		}
	}
}

// The plan response carries the timer state and the generated rollback plan (FR-49.6, FR-49.9).
func TestPlanDTO_TimesRunningStepsAndCarriesTheRollbackPlan(t *testing.T) {
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan, err := domain.GeneratePlan(domain.PlanInput{PlanID: "p", T0: t0, Packs: []domain.Pack{domain.PackOpenItems}, Entities: []string{"USMF"}, AbortOffsetH: 40})
	if err != nil {
		t.Fatal(err)
	}
	load, _ := plan.StepByName("Load pack open_items")
	if err := plan.Start(load.Seq, t0.Add(12*time.Hour), "owner"); err != nil {
		t.Fatal(err)
	}
	at := func(min int) planDTO {
		return New(unreachableStore{t}).WithClock(func() time.Time { return t0.Add(12*time.Hour + time.Duration(min)*time.Minute) }).toPlanDTO(plan, true)
	}
	stepOf := func(d planDTO, seq int) stepDTO {
		for _, s := range d.Steps {
			if s.Seq == seq {
				return s
			}
		}
		t.Fatalf("no step %d", seq)
		return stepDTO{}
	}
	if s := stepOf(at(30), load.Seq); s.StartedAt == nil || s.StartedBy != "owner" || s.ElapsedMin == nil || *s.ElapsedMin != 30 || s.Tone != domain.ToneOK || s.OverrunWarning != "" {
		t.Fatalf("running at 30 of 60 min: %+v", s)
	}
	if s := stepOf(at(66), load.Seq); s.Tone != domain.ToneAmber || !strings.Contains(s.OverrunWarning, "CUT-1602") || !strings.Contains(s.OverrunWarning, "110%") {
		t.Fatalf("running at 66 of 60 min: %+v", s)
	}
	if s := stepOf(at(75), load.Seq); s.Tone != domain.ToneRed {
		t.Fatalf("running at 75 of 60 min: %+v", s)
	}
	if s := stepOf(at(0), plan.Steps[0].Seq); s.ElapsedMin != nil || s.Tone != "" || s.StartedAt != nil {
		t.Fatalf("a step not started has no timing: %+v", s)
	}
	d := at(0)
	if len(d.RollbackSteps) != 6 || !d.RollbackSteps[0].DecisionPoint || d.RollbackSteps[0].OffsetMin != 40*60 || d.RollbackSteps[0].Name != domain.RollbackDecision {
		t.Fatalf("rollback plan %+v", d.RollbackSteps)
	}
	if list := New(unreachableStore{t}).toPlanDTO(plan, false); list.RollbackSteps != nil || list.Steps != nil {
		t.Fatalf("the list leaves the steps and the rollback plan out: %+v", list)
	}
}
