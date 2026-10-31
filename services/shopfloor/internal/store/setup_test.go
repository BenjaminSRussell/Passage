package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func spc(s string) *string { return &s }

func dayRules() []domain.ProfileDayInput {
	return []domain.ProfileDayInput{
		{DayType: domain.DayTypeWeekday, ShiftStart: spc("07:00"), ShiftEnd: spc("15:30"), RoundingMinutes: 5, ToleranceBeforeMin: 10, ToleranceAfterMin: 5,
			Breaks: []domain.BreakInput{{From: "12:00", To: "12:30"}}},
		{DayType: domain.DayTypeSunday, RoundingMinutes: 1, ToleranceBeforeMin: 10, ToleranceAfterMin: 5, FlexAllowed: true},
	}
}

func agreementRules(t *testing.T, daily string) domain.Rules {
	t.Helper()
	r, f := domain.AgreementInput{DailyThreshold: daily, WeeklyThreshold: "40", OvertimeMultiplier: "1.5", SundayMultiplier: "2", WeekStart: 1}.Rules()
	if len(f) > 0 {
		t.Fatal(f)
	}
	return r
}

func TestTimeProfile_CreateUpdateVersionAndEvents(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(31)
	ctx := testkit.Ctx(tenantID)
	created, err := s.CreateTimeProfile(ctx, TimeProfileInput{Code: "DAY", Name: "Day shift", Active: true, Days: dayRules()}, "admin-1", idem("tp-1"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || len(created.Days) != 2 {
		t.Fatalf("created = %+v", created)
	}
	if _, err := s.CreateTimeProfile(ctx, TimeProfileInput{Code: "DAY", Name: "Again", Active: true}, "admin-1", idem("tp-2")); problemCode(err) != CodeSetupCodeTaken {
		t.Fatalf("duplicate code: %v", err)
	}
	got, err := s.GetTimeProfile(ctx, created.ID)
	if err != nil || len(got.Days) != 2 || got.Days[0].DayType != "weekday" || got.Days[0].Breaks[0].To != "12:30" || *got.Days[0].ShiftStart != "07:00" || got.Days[1].ShiftStart != nil || !got.Days[1].FlexAllowed {
		t.Fatalf("get = %+v, %v", got, err)
	}

	updated, err := s.UpdateTimeProfile(ctx, created.ID, 1, TimeProfileInput{Name: "Early shift", Active: true, Days: dayRules()[:1]}, "admin-2")
	if err != nil || updated.Version != 2 || updated.Name != "Early shift" || len(updated.Days) != 1 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := s.UpdateTimeProfile(ctx, created.ID, 1, TimeProfileInput{Name: "Stale", Active: true}, "admin-2"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.UpdateTimeProfile(ctx, ids.New(), 1, TimeProfileInput{Name: "x", Active: true}, "admin-2"); problemCode(err) != CodeProfileNotFound {
		t.Fatalf("unknown profile: %v", err)
	}
	list, err := s.ListTimeProfiles(ctx, false, 10)
	if err != nil || len(list) != 1 || len(list[0].Days) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.TimeProfileSaved); n != 2 {
		t.Fatalf("timeprofile.saved events = %d, want 2 (the refused calls publish nothing)", n)
	}
	var raw []byte
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select payload->'data' from outbox where type = $1 order by created_at desc, id desc limit 1`, sfevents.TimeProfileSaved).Scan(&raw)
	})
	var ev sfevents.TimeProfileSavedData
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Version != 2 || ev.SavedBy != "admin-2" || ev.ProfileID != created.ID {
		t.Fatalf("event = %s, %v", raw, err)
	}

	// Another tenant sees nothing (RLS).
	other := testkit.Ctx(testkit.TenantID(32))
	if _, err := s.GetTimeProfile(other, created.ID); problemCode(err) != CodeProfileNotFound {
		t.Fatalf("other tenant get: %v", err)
	}
	if l, _ := s.ListTimeProfiles(other, false, 10); len(l) != 0 {
		t.Fatalf("other tenant list: %+v", l)
	}
}

func TestPayAgreement_CreateUpdateVersionAndEvents(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(33)
	ctx := testkit.Ctx(tenantID)
	a, err := s.CreatePayAgreement(ctx, PayAgreementInput{Code: "US", Name: "US hourly", Active: true, Rules: agreementRules(t, "8")}, "admin-1", idem("pa-1"))
	if err != nil || a.Version != 1 || a.DailyThreshold.String() != "8" || a.WeekStart != 1 || a.OvertimeMultiplier.String() != "1.5" {
		t.Fatalf("create = %+v, %v", a, err)
	}
	if _, err := s.CreatePayAgreement(ctx, PayAgreementInput{Code: "US", Name: "x", Active: true, Rules: agreementRules(t, "8")}, "admin-1", idem("pa-2")); problemCode(err) != CodeSetupCodeTaken {
		t.Fatalf("duplicate code: %v", err)
	}
	u, err := s.UpdatePayAgreement(ctx, a.ID, 1, PayAgreementInput{Name: "US 10h", Active: true, Rules: agreementRules(t, "10")}, "admin-2")
	if err != nil || u.Version != 2 || u.DailyThreshold.String() != "10" {
		t.Fatalf("update = %+v, %v", u, err)
	}
	if _, err := s.UpdatePayAgreement(ctx, a.ID, 1, PayAgreementInput{Name: "x", Active: true, Rules: agreementRules(t, "9")}, "admin-2"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale: %v", err)
	}
	if _, err := s.GetPayAgreement(ctx, ids.New()); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if l, err := s.ListPayAgreements(ctx, true, 10); err != nil || len(l) != 1 {
		t.Fatalf("list = %+v, %v", l, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.PayAgreementSaved); n != 2 {
		t.Fatalf("payagreement.saved events = %d", n)
	}
	if l, _ := s.ListPayAgreements(testkit.Ctx(testkit.TenantID(34)), false, 10); len(l) != 0 {
		t.Fatalf("other tenant list: %+v", l)
	}
}

// A worker linked to a profile and an agreement is calculated with them: the weekday
// break comes off and the 10 h threshold applies, so 09:00-19:30 is 10.00 h of base pay.
// Unlinking returns to the defaults; an inactive or unknown link is refused.
func TestAssignWorkerSetup_DrivesCalculation(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(35)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "A-1", nil)
	prof, err := s.CreateTimeProfile(ctx, TimeProfileInput{Code: "P", Name: "P", Active: true, Days: dayRules()}, "admin", idem("p"))
	if err != nil {
		t.Fatal(err)
	}
	agr, err := s.CreatePayAgreement(ctx, PayAgreementInput{Code: "A", Name: "A", Active: true, Rules: agreementRules(t, "10")}, "admin", idem("a"))
	if err != nil {
		t.Fatal(err)
	}
	if setup, err := s.LoadPaySetup(ctx, w.ID); err != nil || setup.Rules != nil || setup.Profiles != nil {
		t.Fatalf("unlinked setup = %+v, %v", setup, err)
	}
	if _, err := s.AssignWorkerSetup(ctx, w.ID, 99, &prof.ID, &agr.ID, "admin", idem("as-0")); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale worker version: %v", err)
	}
	if _, err := s.AssignWorkerSetup(ctx, w.ID, w.Version, spc(ids.New()), nil, "admin", idem("as-1")); problemCode(err) != CodeProfileNotFound {
		t.Fatalf("unknown profile: %v", err)
	}
	if _, err := s.AssignWorkerSetup(ctx, w.ID, w.Version, nil, spc(ids.New()), "admin", idem("as-2")); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("unknown agreement: %v", err)
	}
	if _, err := s.AssignWorkerSetup(ctx, ids.New(), 1, nil, nil, "admin", idem("as-3")); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("unknown worker: %v", err)
	}
	assigned, err := s.AssignWorkerSetup(ctx, w.ID, w.Version, &prof.ID, &agr.ID, "admin", idem("as-4"))
	if err != nil || assigned.Version != w.Version+1 || assigned.ProfileID == nil || *assigned.ProfileID != prof.ID || *assigned.PayAgreementID != agr.ID {
		t.Fatalf("assigned = %+v, %v", assigned, err)
	}
	if got, err := s.GetWorker(ctx, w.ID); err != nil || got.ProfileID == nil || *got.ProfileID != prof.ID {
		t.Fatalf("worker read back = %+v, %v", got, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.WorkerAssigned); n != 1 {
		t.Fatalf("worker.assigned events = %d", n)
	}

	in := []Registration{clock(w.ID, "clock-in", day(9, 0)), clock(w.ID, "clock-out", day(19, 30))}
	if _, err := s.CreateRegistrations(ctx, in); err != nil {
		t.Fatal(err)
	}
	pay := func() []domain.DayPay {
		d := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
		setup, err := s.LoadPaySetup(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		punches, err := s.LoadPunches(ctx, w.ID, d, d.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		pf := func(at time.Time) domain.Profile { return setup.Profiles.For(at, time.UTC) }
		calc := domain.Calculation{ProfileFor: pf, Location: time.UTC, Rules: setup.EffectiveRules(), Rate: punches.Rate, Currency: punches.Currency}
		days, err := calc.Run(domain.BuildDaysFor(punches.Punches, time.UTC, pf))
		if err != nil {
			t.Fatal(err)
		}
		return days
	}
	if days := pay(); len(days) != 1 || len(days[0].Lines) != 1 || days[0].Lines[0].Type != domain.TypeBase || days[0].Lines[0].Hours.String() != "10" {
		t.Fatalf("linked pay lines = %+v", days)
	}

	// Deactivating keeps the link working for the worker but refuses new assignments.
	if _, err := s.UpdateTimeProfile(ctx, prof.ID, 1, TimeProfileInput{Name: "P", Active: false, Days: dayRules()}, "admin"); err != nil {
		t.Fatal(err)
	}
	if days := pay(); days[0].Lines[0].Hours.String() != "10" {
		t.Fatalf("deactivated profile changed pay: %+v", days)
	}
	other := mustWorker(t, s, ctx, "A-2", nil)
	if _, err := s.AssignWorkerSetup(ctx, other.ID, other.Version, &prof.ID, nil, "admin", idem("as-5")); problemCode(err) != CodeSetupInactive {
		t.Fatalf("inactive profile: %v", err)
	}

	// The worker keeps the deactivated profile while its agreement is changed (the web form
	// sends both links back).
	agr2, err := s.CreatePayAgreement(ctx, PayAgreementInput{Code: "A2", Name: "A2", Active: true, Rules: agreementRules(t, "9")}, "admin", idem("a2"))
	if err != nil {
		t.Fatal(err)
	}
	kept, err := s.AssignWorkerSetup(ctx, w.ID, assigned.Version, &prof.ID, &agr2.ID, "admin", idem("as-7"))
	if err != nil || kept.ProfileID == nil || *kept.ProfileID != prof.ID || *kept.PayAgreementID != agr2.ID {
		t.Fatalf("keeping the inactive profile = %+v, %v", kept, err)
	}

	cleared, err := s.AssignWorkerSetup(ctx, w.ID, kept.Version, nil, nil, "admin", idem("as-6"))
	if err != nil || cleared.ProfileID != nil || cleared.PayAgreementID != nil {
		t.Fatalf("cleared = %+v, %v", cleared, err)
	}
	if days := pay(); len(days[0].Lines) != 2 || days[0].Lines[0].Hours.String() != "8" {
		t.Fatalf("defaults after unlink: %+v", days)
	}
	// Replaying the keyed assignment returns the stored outcome without a second event.
	rep, err := s.LookupReplay(ctx, idem("as-4"))
	if err != nil || rep == nil || rep.Status != 200 {
		t.Fatalf("replay = %+v, %v", rep, err)
	}
}
