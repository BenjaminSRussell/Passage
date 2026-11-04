package store_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func activeProcedureWith(t *testing.T, ctx context.Context, s *store.Postgres, le, name string, steps []domain.ProcedureStep) domain.Procedure {
	t.Helper()
	p, err := s.CreateProcedure(ctx, domain.Procedure{LegalEntityID: le, Name: name, Steps: steps}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p, err = s.ActivateProcedure(ctx, p.ID, ""); err != nil {
		t.Fatal(err)
	}
	return p
}

func bestGroupSteps() []domain.ProcedureStep {
	return []domain.ProcedureStep{
		{StepNo: 10, ConditionType: domain.TypePrice},
		{StepNo: 20, ConditionType: domain.TypeLineDisc, ExclusionGroup: "DISC", ExclusionMode: domain.ExclusionBest},
		{StepNo: 30, ConditionType: domain.TypeMultiline, ExclusionGroup: "DISC", ExclusionMode: domain.ExclusionBest},
	}
}

func TestPostgres_ProcedureDeterminationByChannel(t *testing.T) {
	pool := appPool(t)
	ctx := asUser("admin-1")
	s := store.New(pool)

	if p, err := s.DetermineProcedure(ctx, legalEntity, "WEB", ""); err != nil || p != nil {
		t.Fatalf("nothing assigned = %+v, %v; want the reference order (nil)", p, err)
	}
	// A procedure with no ACTIVE version cannot be assigned or named.
	if _, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, ProcedureName: "AX_STANDARD"}, 0, ""); problemCode(err) != domain.CodeProcedureNotActive {
		t.Fatalf("assign a missing procedure = %v; want %s", err, domain.CodeProcedureNotActive)
	}
	if _, err := s.DetermineProcedure(ctx, legalEntity, "", "AX_STANDARD"); problemCode(err) != domain.CodeProcedureNotActive {
		t.Fatalf("name a missing procedure = %v; want %s", err, domain.CodeProcedureNotActive)
	}
	std := activeProcedureWith(t, ctx, s, legalEntity, "AX_STANDARD", bestGroupSteps())
	web := activeProcedureWith(t, ctx, s, legalEntity, "WEB_PRICING", bestGroupSteps()[:2])
	// A draft is not ACTIVE.
	if _, err := s.CreateProcedure(ctx, domain.Procedure{LegalEntityID: legalEntity, Name: "DRAFT_ONLY", Steps: bestGroupSteps()}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, ProcedureName: "DRAFT_ONLY"}, 0, ""); problemCode(err) != domain.CodeProcedureNotActive {
		t.Fatalf("assign a draft-only procedure = %v; want %s", err, domain.CodeProcedureNotActive)
	}

	def, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, ProcedureName: "AX_STANDARD"}, 0, "asg-1")
	if err != nil || def.Version != 1 || def.UpdatedBy != "admin-1" || def.UpdatedAt == nil {
		t.Fatalf("set default = %+v, %v", def, err)
	}
	if replay, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, ProcedureName: "AX_STANDARD"}, 0, "asg-1"); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v; want the first response", replay, err)
	}
	if _, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, ProcedureName: "WEB_PRICING"}, 0, "asg-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other request = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, ProcedureName: "WEB_PRICING"}, 0, ""); problemCode(err) != store.CodeVersionConflict || problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale insert = %v; want 412 %s", err, store.CodeVersionConflict)
	}
	if _, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, SalesChannel: "WEB", ProcedureName: "WEB_PRICING"}, 0, ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.DetermineProcedure(ctx, legalEntity, "WEB", "")
	if err != nil || got == nil || got.ID != web.ID || len(got.Steps) != 2 || got.Steps[1].ExclusionMode != domain.ExclusionBest {
		t.Fatalf("WEB = %+v, %v; want WEB_PRICING with its steps", got, err)
	}
	if got, err = s.DetermineProcedure(ctx, legalEntity, "EDI", ""); err != nil || got == nil || got.ID != std.ID {
		t.Fatalf("EDI = %+v, %v; want the default AX_STANDARD", got, err)
	}
	if got, err = s.DetermineProcedure(ctx, legalEntity, "WEB", "AX_STANDARD"); err != nil || got == nil || got.ID != std.ID {
		t.Fatalf("named = %+v, %v; want AX_STANDARD over the WEB assignment", got, err)
	}
	// Activating a new version moves the assignment to it without changing the assignment.
	v2, err := s.CreateProcedure(ctx, domain.Procedure{LegalEntityID: legalEntity, Name: "AX_STANDARD", Steps: bestGroupSteps()[:1]}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActivateProcedure(ctx, v2.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, err = s.DetermineProcedure(ctx, legalEntity, "", ""); err != nil || got == nil || got.ID != v2.ID || got.Version != 2 {
		t.Fatalf("default after v2 = %+v, %v; want AX_STANDARD version 2", got, err)
	}
	// Another legal entity or tenant sees none of it.
	if got, err = s.DetermineProcedure(ctx, otherLegalEntity, "", ""); err != nil || got != nil {
		t.Fatalf("other legal entity = %+v, %v; want nil", got, err)
	}
	if rows, err := s.ListProcedureAssignments(principal(2, ""), legalEntity); err != nil || len(rows) != 0 {
		t.Fatalf("other tenant list = %+v, %v", rows, err)
	}
	if rows, err := s.ListProcedureAssignments(principal(1, otherLegalEntity), legalEntity); err != nil || len(rows) != 0 {
		t.Fatalf("other scope list = %+v, %v; want the rows hidden", rows, err)
	}

	rows, err := s.ListProcedureAssignments(ctx, legalEntity)
	if err != nil || len(rows) != 2 || rows[0].SalesChannel != "" || rows[1].SalesChannel != "WEB" {
		t.Fatalf("list = %+v, %v; want the default, then WEB", rows, err)
	}
	// Change WEB at its version, then clear it.
	chg, err := s.SetProcedureAssignment(ctx, store.ProcedureAssignment{LegalEntityID: legalEntity, SalesChannel: "WEB", ProcedureName: "AX_STANDARD"}, 1, "")
	if err != nil || chg.Version != 2 || chg.ProcedureName != "AX_STANDARD" {
		t.Fatalf("change WEB = %+v, %v", chg, err)
	}
	if err := s.ClearProcedureAssignment(ctx, legalEntity, "WEB", 1, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("clear at a stale version = %v; want %s", err, store.CodeVersionConflict)
	}
	if err := s.ClearProcedureAssignment(ctx, legalEntity, "WEB", 2, "clr-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearProcedureAssignment(ctx, legalEntity, "WEB", 2, "clr-1"); err != nil {
		t.Fatalf("replayed clear = %v; want the first success", err)
	}
	if err := s.ClearProcedureAssignment(ctx, legalEntity, "WEB", 2, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("clear of nothing = %v; want %s", err, store.CodeVersionConflict)
	}
	if got, err = s.DetermineProcedure(ctx, legalEntity, "WEB", ""); err != nil || got == nil || got.ID != v2.ID {
		t.Fatalf("WEB after clear = %+v, %v; want the default again", got, err)
	}

	// Every change is announced: two sets, a change and a clear; replays and refusals add nothing.
	var changes []map[string]any
	for _, r := range outboxRows(t, ctx, pool) {
		if r.Type == "dev.passage.pricing.procedureassignment.changed.v1" {
			changes = append(changes, r.Data)
		}
	}
	if len(changes) != 4 || changes[0]["procedure_name"] != "AX_STANDARD" || changes[0]["cleared"] != false || changes[0]["updated_by"] != "admin-1" ||
		changes[3]["cleared"] != true || changes[3]["sales_channel"] != "WEB" || changes[3]["version"] != float64(2) {
		t.Errorf("procedureassignment.changed events = %v; want 4, the last clearing WEB at version 2", changes)
	}
}
