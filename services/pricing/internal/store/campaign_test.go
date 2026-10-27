package store_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func testCampaign(group string) domain.Campaign {
	return domain.Campaign{LegalEntityID: legalEntity, Name: "Spring push", ValidFrom: day("2026-03-01"), ValidTo: day("2026-03-31"),
		TargetCustomerGroup: group, Budget: decimal.RequireFromString("5000.50"), Currency: "USD"}
}

func activeIDs(t *testing.T, s *store.Postgres, group, asOf string) map[string]bool {
	t.Helper()
	recs, err := s.ActiveRecords(principal(1, ""), store.ActiveQuery{LegalEntityID: legalEntity, Currency: "USD", CustomerGroupID: group, AsOf: day(asOf)})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range recs {
		out[r.ID] = true
	}
	return out
}

// TestPostgres_CampaignGatesResolve covers FR-26.10 against the database with RLS enforced:
// a campaign record joins resolve only while its campaign is live, inside the campaign
// validity and for the target group; the kill switch takes effect on the next read and is
// announced through the outbox; the campaign is part of the record key.
func TestPostgres_CampaignGatesResolve(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	c, err := s.CreateCampaign(ctx, testCampaign("RETAIL"), "cmp-1")
	if err != nil || c.Version != 1 || c.Killed || !c.Budget.Equal(decimal.RequireFromString("5000.5")) {
		t.Fatalf("create = %+v err %v", c, err)
	}
	if again, err := s.CreateCampaign(ctx, testCampaign("RETAIL"), "cmp-1"); err != nil || again.ID != c.ID {
		t.Fatalf("replayed create = %s err %v; want %s", again.ID, err, c.ID)
	}
	if _, err := s.CreateCampaign(ctx, testCampaign("WHOLESALE"), "cmp-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other body = %v; want idempotency_key_reuse", err)
	}

	plain, err := s.CreateRecord(ctx, entry("2026-01-01", "2026-12-31"), "")
	if err != nil {
		t.Fatal(err)
	}
	promoIn := entry("2026-01-01", "2026-12-31")
	promoIn.CampaignID, promoIn.Amount = c.ID, decimal.RequireFromString("90")
	promo, err := s.CreateRecord(ctx, promoIn, "")
	if err != nil || promo.CampaignID != c.ID {
		t.Fatalf("campaign record = %+v err %v", promo, err)
	}
	// Same key, currency and validity as the plain record: the campaign keeps them apart (FR-26.2).
	for _, id := range []string{plain.ID, promo.ID} {
		if _, err := s.ActivateRecord(ctx, id, 1, ""); err != nil {
			t.Fatalf("activate %s: %v", id, err)
		}
	}

	if got := activeIDs(t, s, "RETAIL", "2026-03-15"); !got[plain.ID] || !got[promo.ID] {
		t.Fatalf("live campaign, target group = %v; want both records", got)
	}
	if got := activeIDs(t, s, "WHOLESALE", "2026-03-15"); !got[plain.ID] || got[promo.ID] {
		t.Fatalf("other group = %v; want the plain record only", got)
	}
	if got := activeIDs(t, s, "RETAIL", "2026-04-01"); got[promo.ID] {
		t.Fatalf("after the campaign = %v; want the plain record only", got)
	}

	if _, err := s.SetCampaignKilled(ctx, c.ID, 2, true, ""); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale kill = %v; want 412", err)
	}
	killed, err := s.SetCampaignKilled(ctx, c.ID, 1, true, "kill-1")
	if err != nil || !killed.Killed || killed.Version != 2 {
		t.Fatalf("kill = %+v err %v", killed, err)
	}
	if again, err := s.SetCampaignKilled(ctx, c.ID, 1, true, "kill-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed kill = %+v err %v; want the first response", again, err)
	}
	if _, err := s.SetCampaignKilled(ctx, c.ID, 2, true, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("kill a killed campaign = %v; want PRC-0031", err)
	}
	if got := activeIDs(t, s, "RETAIL", "2026-03-15"); got[promo.ID] || !got[plain.ID] {
		t.Fatalf("killed campaign = %v; want the plain record only", got)
	}
	if _, err := s.SetCampaignKilled(ctx, c.ID, 2, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := activeIDs(t, s, "RETAIL", "2026-03-15"); !got[promo.ID] {
		t.Fatalf("restored campaign = %v; want the campaign record back", got)
	}

	var types []string
	for _, row := range outboxRows(t, ctx, pool) {
		types = append(types, row.Type)
		if row.Type == pevents.ConditionRecordActivated && row.Data["id"] == promo.ID && row.Data["campaign_id"] != c.ID {
			t.Fatalf("activated event of the campaign record = %v; want campaign_id", row.Data)
		}
		if row.Type == pevents.CampaignKilled && (row.Data["campaign_id"] != c.ID || row.Data["killed"] != true || row.Data["version"] != float64(2)) {
			t.Fatalf("killed event = %v", row.Data)
		}
	}
	want := []string{pevents.ConditionRecordActivated, pevents.ConditionRecordActivated, pevents.CampaignKilled, pevents.CampaignRestored}
	if len(types) != len(want) {
		t.Fatalf("outbox = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("outbox = %v; want %v", types, want)
		}
	}

	inCampaign, err := s.ListRecords(ctx, store.Filter{CampaignID: c.ID})
	if err != nil || len(inCampaign) != 1 || inCampaign[0].ID != promo.ID {
		t.Fatalf("records of the campaign = %+v err %v; want the campaign record only", inCampaign, err)
	}

	list, err := s.ListCampaigns(ctx, legalEntity, 0)
	if err != nil || len(list) != 1 || list[0].Version != 3 {
		t.Fatalf("list = %+v err %v", list, err)
	}
	if _, err := s.GetCampaign(principal(2, ""), c.ID); problemCode(err) != domain.CodeCampaignNotFound {
		t.Fatalf("another tenant must not see the campaign: %v", err)
	}
	if _, err := s.GetCampaign(principal(1, otherLegalEntity), c.ID); problemCode(err) != domain.CodeCampaignNotFound {
		t.Fatalf("a caller scoped to another legal entity must not see the campaign: %v", err)
	}
}

func TestPostgres_RecordCampaignChecks(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	c, err := s.CreateCampaign(ctx, testCampaign(""), "")
	if err != nil {
		t.Fatal(err)
	}
	unknown := entry("2026-01-01", "2026-12-31")
	unknown.CampaignID = "00000000-0000-0000-0000-00000000dead"
	if _, err := s.CreateRecord(ctx, unknown, ""); problemCode(err) != domain.CodeCampaignNotFound {
		t.Fatalf("unknown campaign = %v; want PRC-0041", err)
	}
	other := entry("2026-01-01", "2026-12-31")
	other.LegalEntityID, other.CampaignID = otherLegalEntity, c.ID
	if _, err := s.CreateRecord(ctx, other, ""); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("campaign of another legal entity = %v; want 422", err)
	}
	if _, err := s.CreateRecord(principal(2, ""), func() store.Entry { e := entry("2026-01-01", "2026-12-31"); e.CampaignID = c.ID; return e }(), ""); problemCode(err) != domain.CodeCampaignNotFound {
		t.Fatalf("another tenant's campaign = %v; want PRC-0041", err)
	}
}

func referenceSteps() []domain.ProcedureStep {
	return []domain.ProcedureStep{{StepNo: 10, ConditionType: domain.TypePrice}, {StepNo: 20, ConditionType: domain.TypeLineDisc,
		ExclusionGroup: "EG1", ExclusionMode: domain.ExclusionBest}, {StepNo: 40, ConditionType: domain.TypeTotalDisc, RequirementCEL: "order.net > 0"}}
}

// TestPostgres_ProcedureVersions covers FR-26.1: versions number per legal entity and name,
// a DRAFT is revised at its revision, activation supersedes the ACTIVE version in one
// transaction with procedure.activated, and activated versions are immutable (PRC-0008).
func TestPostgres_ProcedureVersions(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	proc := domain.Procedure{LegalEntityID: legalEntity, Name: "AX_STANDARD", Steps: referenceSteps()}
	v1, err := s.CreateProcedure(ctx, proc, "proc-1")
	if err != nil || v1.Version != 1 || v1.Status != domain.ProcedureDraft || v1.Revision != 1 || len(v1.Steps) != 3 ||
		v1.Steps[1].ExclusionMode != domain.ExclusionBest || v1.Steps[2].RequirementCEL != "order.net > 0" {
		t.Fatalf("create v1 = %+v err %v", v1, err)
	}
	if again, err := s.CreateProcedure(ctx, proc, "proc-1"); err != nil || again.ID != v1.ID {
		t.Fatalf("replayed create = %s err %v", again.ID, err)
	}
	changed := proc
	changed.Steps = changed.Steps[:1]
	if _, err := s.CreateProcedure(ctx, changed, "proc-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other steps = %v; want idempotency_key_reuse", err)
	}
	other := proc
	other.LegalEntityID = otherLegalEntity
	if o, err := s.CreateProcedure(ctx, other, ""); err != nil || o.Version != 1 {
		t.Fatalf("another legal entity numbers its own versions: %+v err %v", o, err)
	}

	// Concurrent creates of one name get distinct version numbers (advisory lock).
	var wg sync.WaitGroup
	versions := make(chan int, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.CreateProcedure(ctx, proc, "")
			if err != nil {
				t.Error(err)
				return
			}
			versions <- p.Version
		}()
	}
	wg.Wait()
	close(versions)
	seen := map[int]bool{}
	for v := range versions {
		if seen[v] || v < 2 || v > 5 {
			t.Fatalf("concurrent create produced version %d twice or out of 2-5", v)
		}
		seen[v] = true
	}

	if _, err := s.ReviseProcedure(ctx, v1.ID, 2, referenceSteps()[:2], ""); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale revise = %v; want 412", err)
	}
	revised, err := s.ReviseProcedure(ctx, v1.ID, 1, referenceSteps()[:2], "rev-1")
	if err != nil || revised.Revision != 2 || len(revised.Steps) != 2 {
		t.Fatalf("revise = %+v err %v", revised, err)
	}
	if again, err := s.ReviseProcedure(ctx, v1.ID, 1, referenceSteps()[:2], "rev-1"); err != nil || again.Revision != 2 {
		t.Fatalf("replayed revise = %+v err %v", again, err)
	}

	act1, err := s.ActivateProcedure(ctx, v1.ID, "act-1")
	if err != nil || act1.Status != domain.ProcedureActive {
		t.Fatalf("activate v1 = %+v err %v", act1, err)
	}
	if again, err := s.ActivateProcedure(ctx, v1.ID, "act-1"); err != nil || again.Status != domain.ProcedureActive {
		t.Fatalf("replayed activate = %+v err %v", again, err)
	}
	if _, err := s.ActivateProcedure(ctx, v1.ID, ""); problemCode(err) != domain.CodeProcedureImmutable {
		t.Fatalf("activate an ACTIVE version = %v; want PRC-0008", err)
	}
	if _, err := s.ReviseProcedure(ctx, v1.ID, 2, referenceSteps(), ""); problemCode(err) != domain.CodeProcedureImmutable {
		t.Fatalf("revise an ACTIVE version = %v; want PRC-0008", err)
	}
	all, err := s.ListProcedures(ctx, legalEntity, 0)
	if err != nil || len(all) != 5 || all[0].Version != 5 {
		t.Fatalf("list = %d err %v; want 5 versions, newest first", len(all), err)
	}
	v2 := all[3]
	if v2.Version != 2 {
		t.Fatalf("expected version 2 at index 3, got %d", v2.Version)
	}
	if _, err := s.ActivateProcedure(ctx, v2.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetProcedure(ctx, v1.ID); got.Status != domain.ProcedureSuperseded {
		t.Fatalf("v1 after v2 = %s; want SUPERSEDED", got.Status)
	}

	var acts []map[string]any
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == pevents.ProcedureActivated {
			acts = append(acts, row.Data)
		}
	}
	if len(acts) != 2 || acts[0]["procedure_id"] != v1.ID || acts[0]["superseded_id"] != nil ||
		acts[1]["procedure_id"] != v2.ID || acts[1]["superseded_id"] != v1.ID || acts[1]["version"] != float64(2) {
		t.Fatalf("procedure.activated events = %v", acts)
	}
	if _, err := s.GetProcedure(principal(2, ""), v1.ID); problemCode(err) != domain.CodeProcedureNotFound {
		t.Fatalf("another tenant must not see the procedure: %v", err)
	}
}
