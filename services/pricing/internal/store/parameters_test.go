package store_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// fourEyesOn switches journal_four_eyes on for the test legal entity (stored version 0 -> 1).
func fourEyesOn(t *testing.T, ctx context.Context, s *store.Postgres) {
	t.Helper()
	if _, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, JournalFourEyes: true}, 0, ""); err != nil {
		t.Fatal(err)
	}
}

// validatedJournal creates and validates a one-line +1% journal on rec as ctx's user.
func validatedJournal(t *testing.T, ctx context.Context, s *store.Postgres, rec store.Entry, name string) store.Journal {
	t.Helper()
	j, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: name,
		Lines: []domain.JournalLine{adjust(rec.ID, rec.Version, "1")}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateJournal(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestPostgres_ParametersVersionedAndAnnounced(t *testing.T) {
	pool := appPool(t)
	ctx := asUser("admin-1")
	s := store.New(pool)
	p, err := s.GetParameters(ctx, legalEntity)
	if err != nil || p.Version != 0 || p.JournalFourEyes || p.UpdatedAt != nil {
		t.Fatalf("defaults = %+v, %v", p, err)
	}
	set, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, JournalFourEyes: true}, 0, "par-1")
	if err != nil || set.Version != 1 || !set.JournalFourEyes || set.UpdatedBy != "admin-1" || set.UpdatedAt == nil {
		t.Fatalf("set = %+v, %v", set, err)
	}
	if replay, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, JournalFourEyes: true}, 0, "par-1"); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v; want the first response", replay, err)
	}
	if _, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity}, 1, "par-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other request = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity}, 0, ""); problemCode(err) != store.CodeVersionConflict || problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale insert = %v; want 412 %s", err, store.CodeVersionConflict)
	}
	if _, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity}, 5, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update = %v; want %s", err, store.CodeVersionConflict)
	}
	// A caller scoped to another legal entity cannot see or change this one's row.
	scoped := principal(1, otherLegalEntity)
	if hidden, err := s.GetParameters(scoped, legalEntity); err != nil || hidden.Version != 0 {
		t.Fatalf("read from another legal entity's scope = %+v, %v; want the row hidden", hidden, err)
	}
	if _, err := s.SetParameters(scoped, store.Parameters{LegalEntityID: legalEntity}, 1, ""); err == nil {
		t.Fatal("update from another legal entity's scope succeeded")
	}
	off, err := s.SetParameters(asUser("admin-2"), store.Parameters{LegalEntityID: legalEntity}, 1, "")
	if err != nil || off.Version != 2 || off.JournalFourEyes || off.UpdatedBy != "admin-2" {
		t.Fatalf("update = %+v, %v", off, err)
	}
	// Every change is audited through the outbox: who, what and the version; the replay,
	// the conflicts and the refused write add nothing.
	var changes []map[string]any
	for _, r := range outboxRows(t, ctx, pool) {
		if r.Type == "dev.passage.pricing.parameters.changed.v1" {
			changes = append(changes, r.Data)
		}
	}
	if len(changes) != 2 || changes[0]["journal_four_eyes"] != true || changes[0]["updated_by"] != "admin-1" ||
		changes[1]["journal_four_eyes"] != false || changes[1]["updated_by"] != "admin-2" || changes[1]["version"] != float64(2) {
		t.Errorf("parameters.changed events = %v; want two, on by admin-1 then off by admin-2 at version 2", changes)
	}
	if other, err := s.GetParameters(principal(2, ""), legalEntity); err != nil || other.Version != 0 {
		t.Errorf("other tenant sees %+v, %v; want defaults", other, err)
	}
}

// With journal_four_eyes on the creator cannot post a journal and stays VALIDATED; another
// user can, and the poster is recorded; a post without a user identity is refused. With it
// off the creator posts freely. A reversal records its reverser as creator and poster.
func TestPostgres_JournalFourEyes(t *testing.T) {
	pool := appPool(t)
	s := store.New(pool)
	maker, checker := asUser("maker"), asUser("checker")

	rec := activePrice(t, maker, s, "C1", "10")
	off := validatedJournal(t, maker, s, rec, "off")
	if off.CreatedBy != "maker" {
		t.Fatalf("createdBy = %q; want maker", off.CreatedBy)
	}
	posted, err := s.PostJournal(maker, off.ID, "")
	if err != nil || posted.PostedBy != "maker" || posted.CreatedBy != "maker" {
		t.Fatalf("post with four eyes off = %+v, %v", posted, err)
	}

	fourEyesOn(t, checker, s)
	rec2 := activePrice(t, maker, s, "C2", "10")
	on := validatedJournal(t, maker, s, rec2, "on")
	if _, err := s.PostJournal(maker, on.ID, ""); problemCode(err) != domain.CodeFourEyes || problemStatus(err) != http.StatusForbidden {
		t.Fatalf("post by the creator = %v; want 403 PRC-0057", err)
	}
	if _, err := s.PostJournal(principal(1, ""), on.ID, ""); problemCode(err) != domain.CodeFourEyes {
		t.Fatalf("post without a user = %v; want 403 PRC-0057", err)
	}
	if got, err := s.GetJournal(maker, on.ID); err != nil || got.Status != domain.JournalValidated || got.PostedBy != "" {
		t.Fatalf("journal after the refusals = %+v, %v; want still VALIDATED", got, err)
	}
	posted, err = s.PostJournal(checker, on.ID, "")
	if err != nil || posted.Status != domain.JournalPosted || posted.PostedBy != "checker" || posted.CreatedBy != "maker" {
		t.Fatalf("post by another user = %+v, %v", posted, err)
	}

	// Reversal four-eyes is not built: the reverser creates and posts the reversal.
	rev, err := s.ReverseJournal(maker, on.ID, "")
	if err != nil || rev.CreatedBy != "maker" || rev.PostedBy != "maker" || rev.ReversesID != on.ID {
		t.Fatalf("reversal = %+v, %v; want created and posted by the reverser", rev, err)
	}
}

// A journal created before migration 0012 has no recorded creator: four-eyes cannot tell who
// made it, so any identified user may post it.
func TestPostgres_FourEyesJournalWithoutCreator(t *testing.T) {
	admin, app := adminAndAppPools(t)
	s := store.New(app)
	maker := asUser("maker")
	rec := activePrice(t, maker, s, "C1", "10")
	j := validatedJournal(t, maker, s, rec, "legacy")
	if _, err := admin.Exec(context.Background(), `update price_journal set created_by = '' where id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	fourEyesOn(t, maker, s)
	posted, err := s.PostJournal(maker, j.ID, "")
	if err != nil || posted.CreatedBy != "" || posted.PostedBy != "maker" {
		t.Fatalf("post of a journal without a creator = %+v, %v; want posted by maker", posted, err)
	}
}

// The commodity run's journal is created by the runner. The runner's post is refused as
// PRC-0056 (CommodityPriceRun vs PricingJournalPost) whether four-eyes is on or not, so the
// two rules never disagree; any other user posts it.
func TestPostgres_FourEyesComposesWithCommodityRun(t *testing.T) {
	pool := appPool(t)
	s := store.New(pool)
	runner, poster := asUser("u-runner"), asUser("u-poster")
	basis, err := s.CreateCommodityBasis(runner, commodityBasis("CU-LME"), "")
	if err != nil {
		t.Fatal(err)
	}
	calc, err := s.CreateCommodityCalc(runner, commodityCalc(basis.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	rec := entry("2026-01-01", "2026-12-31")
	rec.CustomerKind, rec.CustomerKey = domain.CustomerKindAll, ""
	created, err := s.CreateRecord(runner, rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRecord(runner, created.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordIndexValue(runner, index(basis.ID, "2026-06-08", "9100"), ""); err != nil {
		t.Fatal(err)
	}
	fourEyesOn(t, poster, s)
	run, err := s.RunCommodityCalc(runner, calc.ID, day("2026-06-10"), "")
	if err != nil || run.JournalID == "" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	j, err := s.GetJournal(runner, run.JournalID)
	if err != nil || j.CreatedBy != "u-runner" || j.Status != domain.JournalValidated {
		t.Fatalf("run journal status %s created by %q, %v; want VALIDATED, created by u-runner", j.Status, j.CreatedBy, err)
	}
	if _, err := s.PostJournal(runner, run.JournalID, ""); problemCode(err) != domain.CodeRunOwnJournal || problemStatus(err) != http.StatusForbidden {
		t.Fatalf("post by the runner = %v; want 403 PRC-0056", err)
	}
	posted, err := s.PostJournal(poster, run.JournalID, "")
	if err != nil || posted.PostedBy != "u-poster" || posted.CreatedBy != "u-runner" {
		t.Fatalf("post by another user = %+v, %v", posted, err)
	}
	if got, err := s.GetRecord(poster, created.ID); err != nil || !got.Amount.Equal(decimal.RequireFromString("12.012")) {
		t.Fatalf("record after post = %+v, %v", got, err)
	}
}

// The pricing date basis, the total discount basis and the smart rounding table are stored per
// legal entity, announced with their values, and the journal rounds with the entity's table.
func TestPostgres_ParameterBasesAndSmartRoundingBands(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	def, err := s.GetParameters(ctx, legalEntity)
	if err != nil || def.PricingDateBasis != domain.DateBasisShip || def.TotalDiscountBasis != domain.TotalBasisAfterLineDisc || len(def.SmartRoundingBands) != 3 {
		t.Fatalf("defaults = %+v, %v", def, err)
	}
	bands := []domain.BandSpec{{UpTo: "20", Ending: "0.5"}, {Ending: "0"}}
	set, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, PricingDateBasis: domain.DateBasisDelivery,
		TotalDiscountBasis: domain.TotalBasisGross, SmartRoundingBands: bands}, 0, "")
	if err != nil || set.PricingDateBasis != domain.DateBasisDelivery || set.TotalDiscountBasis != domain.TotalBasisGross || len(set.SmartRoundingBands) != 2 || set.SmartRoundingBands[0].UpTo != "20" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	if _, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, PricingDateBasis: "TODAY"}, 1, ""); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("bad basis = %v; want 422", err)
	}
	if _, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, SmartRoundingBands: []domain.BandSpec{{UpTo: "5", Ending: "0.5"}}}, 1, ""); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("bad bands = %v; want 422", err)
	}
	ev := outboxRows(t, ctx, pool)
	last := ev[len(ev)-1]
	if last.Type != "dev.passage.pricing.parameters.changed.v1" || last.Data["pricing_date_basis"] != "DELIVERY_DATE" || last.Data["total_discount_basis"] != "GROSS" || len(last.Data["smart_rounding_bands"].([]any)) != 2 {
		t.Fatalf("event = %+v", last)
	}

	// 7.41 + 5% = 7.7805 rounds up to 8.5 under the entity's table (7.99 under the default).
	rec := activePrice(t, ctx, s, "C1", "7.41")
	j, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Own bands", SmartRound: true,
		Lines: []domain.JournalLine{adjust(rec.ID, rec.Version, "5")}}, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ValidateJournal(ctx, j.ID)
	if err != nil || v.ErrorCount != 0 || !v.Lines[0].After.Amount.Equal(decimal.RequireFromString("8.5")) {
		t.Fatalf("validate = %+v, %v; want 8.5 under the entity's bands", v, err)
	}
	// Saving the default table stores NULL: the entity follows the default again.
	// The table is stored in its canonical form, so the default written another way is the default.
	back, err := s.SetParameters(ctx, store.Parameters{LegalEntityID: legalEntity, SmartRoundingBands: []domain.BandSpec{
		{UpTo: "10.00", Ending: "0.990"}, {UpTo: "100", Ending: "0.95"}, {Ending: "0.0"}}}, 1, "")
	if err != nil || len(back.SmartRoundingBands) != 3 || back.SmartRoundingBands[0] != domain.DefaultBandSpecs()[0] {
		t.Fatalf("default table = %+v, %v", back, err)
	}
	var isNull bool
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select smart_rounding_bands is null from pricing_parameter where legal_entity_id = $1`, legalEntity).Scan(&isNull)
	}); err != nil || !isNull {
		t.Fatalf("default table stored as NULL = %v, %v", isNull, err)
	}
}
