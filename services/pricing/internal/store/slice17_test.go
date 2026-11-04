package store_test

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// fromToday is the day n days from today (the store compares with the database's current_date;
// the tests keep a margin of days so a time zone difference does not matter).
func fromToday(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }

// FR-26.10 against Postgres: a revise changes the terms at the version, the new validity gates
// resolve, a stale version and invalid merged terms are refused, a replay writes one event.
func TestPostgres_CampaignRevise(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	future := testCampaign("RETAIL")
	future.ValidFrom, future.ValidTo = day(fromToday(10)), day(fromToday(40))
	c, err := s.CreateCampaign(ctx, future, "")
	if err != nil {
		t.Fatal(err)
	}
	in := entry(fromToday(1), fromToday(400))
	in.CampaignID = c.ID
	rec, err := s.CreateRecord(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRecord(ctx, rec.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if got := activeIDs(t, s, "RETAIL", fromToday(25)); !got[rec.ID] {
		t.Fatalf("before the revise = %v; want the campaign record", got)
	}

	name, to, budget, group := "Spring push II", day(fromToday(20)), decimal.RequireFromString("7500.25"), ""
	rev := domain.CampaignRevision{ID: c.ID, Version: 1, Name: &name, ValidTo: &to, Budget: &budget, TargetCustomerGroup: &group}
	out, err := s.ReviseCampaign(ctx, rev, "crev-1")
	if err != nil || out.Version != 2 || out.Name != name || out.ValidTo.Format("2006-01-02") != fromToday(20) || !out.Budget.Equal(budget) ||
		out.TargetCustomerGroup != "" || out.Currency != "USD" {
		t.Fatalf("revise = %+v err %v", out, err)
	}
	if again, err := s.ReviseCampaign(ctx, rev, "crev-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed revise = %+v err %v; want the first response", again, err)
	}
	other := "Other"
	if _, err := s.ReviseCampaign(ctx, domain.CampaignRevision{ID: c.ID, Version: 1, Name: &other}, "crev-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other body = %v; want idempotency_key_reuse", err)
	}
	if _, err := s.ReviseCampaign(ctx, domain.CampaignRevision{ID: c.ID, Version: 1, Name: &other}, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale revise = %v; want conflict_version", err)
	}
	badTo := day(fromToday(5))
	neg := decimal.NewFromInt(-1)
	if _, err := s.ReviseCampaign(ctx, domain.CampaignRevision{ID: c.ID, Version: 2, ValidTo: &badTo, Budget: &neg}, ""); problemCode(err) != domain.CodeValidation {
		t.Fatalf("invalid merged terms = %v; want COM-0001", err)
	}
	if _, err := s.ReviseCampaign(ctx, domain.CampaignRevision{ID: "00000000-0000-0000-0011-0000000000ff", Version: 1, Name: &other}, ""); problemCode(err) != domain.CodeCampaignNotFound {
		t.Fatalf("unknown campaign = %v; want PRC-0041", err)
	}
	if got := activeIDs(t, s, "RETAIL", fromToday(25)); got[rec.ID] {
		t.Fatalf("after the campaign was shortened = %v; want no campaign record", got)
	}
	if got := activeIDs(t, s, "WHOLESALE", fromToday(15)); !got[rec.ID] {
		t.Fatalf("after the target group was cleared = %v; want the record for every group", got)
	}

	revised := 0
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == pevents.CampaignRevised {
			revised++
			if row.Data["campaign_id"] != c.ID || row.Data["valid_to"] != fromToday(20) || row.Data["budget"] != "7500.25" || row.Data["version"] != float64(2) {
				t.Fatalf("revised event = %v", row.Data)
			}
		}
	}
	if revised != 1 {
		t.Fatalf("campaign.revised events = %d; want 1 (a replay writes nothing)", revised)
	}
	if _, err := s.ReviseCampaign(principal(2, ""), domain.CampaignRevision{ID: c.ID, Version: 2, Name: &other}, ""); problemCode(err) != domain.CodeCampaignNotFound {
		t.Fatalf("another tenant must not revise the campaign: %v", err)
	}
}

// A revise keeps the days a started campaign already priced: a resolve for a past pricing
// date gives the same answer before and after, while name, budget and the future still change.
func TestPostgres_CampaignReviseKeepsThePast(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	running := testCampaign("RETAIL")
	running.ValidFrom, running.ValidTo = day(fromToday(-20)), day(fromToday(20))
	c, err := s.CreateCampaign(ctx, running, "")
	if err != nil {
		t.Fatal(err)
	}
	in := entry(fromToday(-60), fromToday(60))
	in.CampaignID = c.ID
	rec, err := s.CreateRecord(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRecord(ctx, rec.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	from, earlyTo, empty := day(fromToday(-10)), day(fromToday(-10)), ""
	for name, tc := range map[string]struct {
		rev   domain.CampaignRevision
		field string
	}{
		"move the start":         {domain.CampaignRevision{ValidFrom: &from}, "validFrom"},
		"end before priced days": {domain.CampaignRevision{ValidTo: &earlyTo}, "validTo"},
		"change the group":       {domain.CampaignRevision{TargetCustomerGroup: &empty}, "targetCustomerGroup"},
	} {
		tc.rev.ID, tc.rev.Version = c.ID, 1
		_, err := s.ReviseCampaign(ctx, tc.rev, "")
		var p *httpx.Problem
		if !errors.As(err, &p) || p.Code != domain.CodeValidation || p.Errors[tc.field] == "" {
			t.Fatalf("%s = %v; want 422 COM-0001 naming %s", name, err, tc.field)
		}
	}
	name, budget, endsSoon := "Running push", decimal.NewFromInt(100), day(fromToday(5))
	out, err := s.ReviseCampaign(ctx, domain.CampaignRevision{ID: c.ID, Version: 1, Name: &name, Budget: &budget, ValidTo: &endsSoon}, "")
	if err != nil || out.Version != 2 || out.Name != name {
		t.Fatalf("name, budget and a later end = %+v err %v", out, err)
	}
	if got := activeIDs(t, s, "RETAIL", fromToday(-15)); !got[rec.ID] {
		t.Fatalf("a past pricing date after the revise = %v; want the campaign record still", got)
	}
	if got := activeIDs(t, s, "RETAIL", fromToday(10)); got[rec.ID] {
		t.Fatalf("a day after the new end = %v; want no campaign record", got)
	}
}

// BR-26.8 reversed against Postgres: a CLOSED agreement reopens with a later last day, accrues
// invoices after the old close date again, keeps its accruals, and writes one reopened event.
func TestPostgres_RebateReopen(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenAgreement(ctx, a.ID, 1, day("2027-03-31"), ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("reopen an ACTIVE agreement = %v; want PRC-0031", err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1", "2026-06-15", "10000")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseAgreement(ctx, a.ID, 1, day("2026-09-30"), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-2", "2026-10-15", "1000")); problemCode(err) != domain.CodeValidation {
		t.Fatalf("invoice after the close = %v; want %s", err, domain.CodeValidation)
	}
	if _, err := s.ReopenAgreement(ctx, a.ID, 1, day("2027-03-31"), ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale reopen = %v; want conflict_version", err)
	}
	if _, err := s.ReopenAgreement(ctx, a.ID, 2, day("2026-09-30"), ""); problemCode(err) != domain.CodeValidation {
		t.Fatalf("reopen to the close date = %v; want COM-0001", err)
	}
	r, err := s.ReopenAgreement(ctx, a.ID, 2, day("2026-12-31"), "ro-1")
	if err != nil || r.Status != domain.AgreementActive || r.Version != 3 || dayStrOf(r) != "2026-12-31" {
		t.Fatalf("reopen = %+v err %v", r, err)
	}
	if again, err := s.ReopenAgreement(ctx, a.ID, 2, day("2026-12-31"), "ro-1"); err != nil || again.Version != 3 {
		t.Fatalf("replayed reopen = %+v err %v; want the first response", again, err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-2", "2026-10-15", "1000")); err != nil {
		t.Fatalf("invoice after the reopen = %v; want accrued", err)
	}
	// The period booked before the close is not accrued again: a redelivered line replays.
	if acc, created, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1", "2026-06-15", "10000")); err != nil || created || !acc.Accrued.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("redelivered pre-close line after the reopen = %+v created %v err %v; want the booked 200, not created", acc, created, err)
	}
	booked, err := s.ListAccruals(ctx, store.AccrualFilter{AgreementID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, row := range booked {
		sum = sum.Add(row.Accrued)
	}
	if len(booked) != 2 || !sum.Equal(decimal.NewFromInt(220)) {
		t.Fatalf("accruals after the reopen = %d rows, %s; want 2 rows, 220 (200 before the close, 20 after)", len(booked), sum)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-3", "2027-01-05", "1000")); problemCode(err) != domain.CodeValidation {
		t.Fatalf("invoice after the new end = %v; want %s", err, domain.CodeValidation)
	}
	// Closing again is allowed: the agreement is ACTIVE.
	if _, err := s.CloseAgreement(ctx, a.ID, 3, day("2026-11-30"), ""); err != nil {
		t.Fatalf("close after reopen = %v", err)
	}
	if _, err := s.ReopenAgreement(principal(2, ""), a.ID, 4, day("2027-03-31"), ""); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("another tenant must not reopen the agreement: %v", err)
	}
	reopened := 0
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == pevents.RebateAgreementReopened {
			reopened++
			if row.Data["status"] != "ACTIVE" || row.Data["valid_to"] != "2026-12-31" || row.Data["agreement_id"] != a.ID {
				t.Fatalf("reopened event = %v", row.Data)
			}
		}
	}
	if reopened != 1 {
		t.Fatalf("rebateagreement.reopened events = %d; want 1", reopened)
	}
}

// ExportRecords streams every matching record past the list's 100-row page, in list order, and
// says truncated only when more records match than the cap; another tenant sees none.
func TestPostgres_ExportRecords(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	for i := 0; i < store.MaxListLimit+5; i++ {
		in := entry("2026-01-01", "2026-12-31")
		in.ProductKey = "EXP-" + string(rune('A'+i%26)) + string(rune('A'+i/26))
		if _, err := s.CreateRecord(ctx, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	export := func(ctx context.Context, f store.Filter, maxRows int) (bool, []string) {
		t.Helper()
		truncated, starts := false, 0
		var ids []string
		err := s.ExportRecords(ctx, f, maxRows, func(tr bool) error {
			starts++
			if len(ids) > 0 {
				t.Fatal("start ran after a row")
			}
			truncated = tr
			return nil
		}, func(e store.Entry) error {
			ids = append(ids, e.ID)
			return nil
		})
		if err != nil || starts != 1 {
			t.Fatalf("export err %v, start ran %d times", err, starts)
		}
		return truncated, ids
	}
	total := store.MaxListLimit + 5
	if tr, ids := export(ctx, store.Filter{}, store.MaxExportRows); tr || len(ids) != total || !sort.StringsAreSorted(ids) {
		t.Fatalf("export = %d rows truncated %v; want %d in id order", len(ids), tr, total)
	}
	if tr, ids := export(ctx, store.Filter{}, total); tr || len(ids) != total {
		t.Fatalf("export at exactly the cap = %d rows truncated %v; want all, not truncated", len(ids), tr)
	}
	if tr, ids := export(ctx, store.Filter{}, total-1); !tr || len(ids) != total-1 {
		t.Fatalf("export over the cap = %d rows truncated %v; want %d and truncated", len(ids), tr, total-1)
	}
	if tr, ids := export(ctx, store.Filter{Product: "EXP-AA", Limit: 1}, store.MaxExportRows); tr || len(ids) != 1 {
		t.Fatalf("filtered export = %d rows truncated %v; want 1", len(ids), tr)
	}
	if tr, ids := export(principal(2, ""), store.Filter{}, store.MaxExportRows); tr || len(ids) != 0 {
		t.Fatalf("another tenant's export = %d rows; want none", len(ids))
	}
	stop := errors.New("client gone")
	n := 0
	if err := s.ExportRecords(ctx, store.Filter{}, store.MaxExportRows, func(bool) error { return nil }, func(store.Entry) error {
		n++
		return stop
	}); !errors.Is(err, stop) || n != 1 {
		t.Fatalf("row error = %v after %d rows; want the error after the first row", err, n)
	}
}
