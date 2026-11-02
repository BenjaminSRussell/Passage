package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

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
	rules := agreementRules(t, "10")
	rules.ShiftPremiums, _ = domain.ShiftPremiums([]domain.ShiftPremiumInput{{Name: "Night", From: "22:00", To: "06:00", Premium: "0.15"}})
	pu, err := s.UpdatePayAgreement(ctx, a.ID, 2, PayAgreementInput{Name: "US night", Active: true, Rules: rules}, "admin-2")
	if err != nil || pu.Version != 3 || len(pu.ShiftPremiums) != 1 || pu.ShiftPremiums[0].Premium != "0.15" || pu.ShiftPremiums[0].From != "22:00" {
		t.Fatalf("premium update = %+v, %v", pu, err)
	}
	if got, err := s.GetPayAgreement(ctx, a.ID); err != nil || len(got.ShiftPremiums) != 1 || got.ShiftPremiums[0].Name != "Night" {
		t.Fatalf("premium read back = %+v, %v", got, err)
	}
	if _, err := s.GetPayAgreement(ctx, ids.New()); problemCode(err) != CodeAgreementNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if l, err := s.ListPayAgreements(ctx, true, 10); err != nil || len(l) != 1 {
		t.Fatalf("list = %+v, %v", l, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.PayAgreementSaved); n != 3 {
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

// A worker linked to an agreement with a premium window is calculated with it: a 14:00-22:00
// shift has four hours in the 18:00-22:00 window, as a shift-premium line.
func TestLoadPaySetup_CarriesShiftPremiums(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(36))
	w := mustWorker(t, s, ctx, "P-1", nil)
	rules := agreementRules(t, "8")
	rules.ShiftPremiums, _ = domain.ShiftPremiums([]domain.ShiftPremiumInput{{Name: "Evening", From: "18:00", To: "22:00", Premium: "0.25"}})
	agr, err := s.CreatePayAgreement(ctx, PayAgreementInput{Code: "EVE", Name: "Evening", Active: true, Rules: rules}, "admin", idem("pe-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignWorkerSetup(ctx, w.ID, w.Version, nil, &agr.ID, "admin", idem("pe-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, "clock-in", day(14, 0)), clock(w.ID, "clock-out", day(22, 0))}); err != nil {
		t.Fatal(err)
	}
	setup, err := s.LoadPaySetup(ctx, w.ID)
	if err != nil || setup.Rules == nil || len(setup.Rules.ShiftPremiums) != 1 {
		t.Fatalf("setup = %+v, %v", setup, err)
	}
	d := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	punches, err := s.LoadPunches(ctx, w.ID, d, d.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	pf := func(at time.Time) domain.Profile { return setup.Profiles.For(at, time.UTC) }
	calc := domain.Calculation{ProfileFor: pf, Location: time.UTC, Rules: setup.EffectiveRules(), Rate: punches.Rate, Currency: punches.Currency}
	days, err := calc.Run(domain.BuildDaysFor(punches.Punches, time.UTC, pf))
	if err != nil || len(days) != 1 {
		t.Fatalf("days = %+v, %v", days, err)
	}
	var premium *domain.PayLine
	for i, l := range days[0].Lines {
		if l.Type == domain.TypeShiftPremium {
			premium = &days[0].Lines[i]
		}
	}
	if premium == nil || premium.Hours.String() != "4" || premium.Multiplier.String() != "0.25" {
		t.Fatalf("lines = %+v", days[0].Lines)
	}

	// The premium line is stored with the day, approved, and a later change of the
	// premium rate is applied to the approved day by a revision: the old premium line is
	// reversed and the new one added, while the unchanged base line stays.
	calcDay := func() []domain.DayPay {
		setup, err := s.LoadPaySetup(ctx, w.ID)
		if err != nil {
			t.Fatal(err)
		}
		calc := domain.Calculation{ProfileFor: pf, Location: time.UTC, Rules: setup.EffectiveRules(), Rate: punches.Rate, Currency: punches.Currency}
		days, err := calc.Run(domain.BuildDaysFor(punches.Punches, time.UTC, pf))
		if err != nil {
			t.Fatal(err)
		}
		return days
	}
	if err := s.SavePay(ctx, PayCalculation{WorkerID: w.ID, FromDate: "2026-10-20", ToDate: "2026-10-20", TimeZone: "UTC", Days: calcDay()}, Idem{}, nil); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApprovePay(ctx, w.ID, "2026-10-20", "2026-10-20", "supervisor-1", Idem{})
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{}
	for _, d := range approved.Days {
		for _, l := range d.PayLines {
			stored[l.Type] = l.Hours.String() + "@" + l.Multiplier.String()
		}
	}
	if stored[domain.TypeShiftPremium] != "4@0.25" || stored[domain.TypeBase] != "8@1" {
		t.Fatalf("approved lines = %+v", approved)
	}
	rules.ShiftPremiums, _ = domain.ShiftPremiums([]domain.ShiftPremiumInput{{Name: "Evening", From: "18:00", To: "22:00", Premium: "0.3"}})
	if _, err := s.UpdatePayAgreement(ctx, agr.ID, agr.Version, PayAgreementInput{Name: "Evening", Active: true, Rules: rules}, "admin"); err != nil {
		t.Fatal(err)
	}
	rev, err := s.RevisePay(ctx, PayCalculation{WorkerID: w.ID, FromDate: "2026-10-20", ToDate: "2026-10-20", TimeZone: "UTC", Days: calcDay()}, "night rate raised", "supervisor-1", idem("pe-rev"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Days) != 1 || len(rev.Days[0].ReversedLines) != 1 || len(rev.Days[0].AddedLines) != 1 {
		t.Fatalf("revision = %+v", rev)
	}
	r, a := rev.Days[0].ReversedLines[0], rev.Days[0].AddedLines[0]
	if r.Type != domain.TypeShiftPremium || r.Hours.String() != "-4" || r.Multiplier.String() != "0.25" ||
		a.Type != domain.TypeShiftPremium || a.Hours.String() != "4" || a.Multiplier.String() != "0.3" || !a.Amount.Equal(punches.Rate.Mul(decimal.RequireFromString("1.2")).Round(2)) {
		t.Fatalf("reversed %+v added %+v", r, a)
	}
}

// A worker's home resource narrows "my jobs" (GET jobs?workerId=) to that resource's jobs; a
// worker without one sees every job, an unknown worker is SF-0101, a stale version is SF-0143,
// a resource the tenant's shop floor does not know is SF-0156, and each assignment publishes
// worker.assigned with the resource in the same transaction.
func TestAssignWorkerResource_MyJobs(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(71)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "R-1", nil)
	home, away := ids.New(), ids.New()
	mine := mustJob(t, s, ctx, home, 8)
	mustJob(t, s, ctx, away, 7)

	all, err := s.ListJobs(ctx, JobFilter{WorkerID: w.ID})
	if err != nil || len(all) != 2 {
		t.Fatalf("worker without a home resource = %+v, %v", all, err)
	}
	if _, err := s.ListJobs(ctx, JobFilter{WorkerID: ids.New()}); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("unknown worker: %v", err)
	}
	if _, err := s.AssignWorkerResource(ctx, w.ID, 99, &home, "admin", idem("hr-0")); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := s.AssignWorkerResource(ctx, ids.New(), 1, &home, "admin", idem("hr-1")); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("unknown worker assign: %v", err)
	}
	// Only a resource this tenant's shop floor knows: one with a registered machine or dispatch
	// jobs. A resource known only to another tenant is as unknown as a made-up id.
	unknown, foreign, press := ids.New(), ids.New(), ids.New()
	mustJob(t, s, testkit.Ctx(testkit.TenantID(72)), foreign, 6)
	for _, r := range []string{unknown, foreign} {
		if _, err := s.AssignWorkerResource(ctx, w.ID, w.Version, &r, "admin", idem("hr-u-"+r)); problemCode(err) != CodeResourceUnknown {
			t.Fatalf("resource %s: %v", r, err)
		}
	}
	mustMachine(t, s, ctx, "PRESS-HR", press)
	byMachine, err := s.AssignWorkerResource(ctx, w.ID, w.Version, &press, "admin", idem("hr-m"))
	if err != nil || byMachine.DefaultResourceID == nil || *byMachine.DefaultResourceID != press {
		t.Fatalf("machine resource without jobs = %+v, %v", byMachine, err)
	}
	if mine, err := s.ListJobs(ctx, JobFilter{WorkerID: w.ID}); err != nil || len(mine) != 0 {
		t.Fatalf("my jobs on an idle machine = %+v, %v", mine, err)
	}
	w = byMachine
	set, err := s.AssignWorkerResource(ctx, w.ID, w.Version, &home, "admin", idem("hr-2"))
	if err != nil || set.DefaultResourceID == nil || *set.DefaultResourceID != home || set.Version != w.Version+1 {
		t.Fatalf("assigned = %+v, %v", set, err)
	}
	mine2, err := s.ListJobs(ctx, JobFilter{WorkerID: w.ID})
	if err != nil || len(mine2) != 1 || mine2[0].ID != mine.ID {
		t.Fatalf("my jobs = %+v, %v", mine2, err)
	}
	if got, err := s.GetWorker(ctx, w.ID); err != nil || got.DefaultResourceID == nil || *got.DefaultResourceID != home {
		t.Fatalf("get worker = %+v, %v", got, err)
	}
	// The kiosk learns the home resource at badge login, so it can say when it shows every job.
	if sess, err := s.BadgeLogin(ctx, "R-1"); err != nil || sess.DefaultResourceID == nil || *sess.DefaultResourceID != home {
		t.Fatalf("badge session = %+v, %v", sess, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.WorkerAssigned); n != 2 {
		t.Fatalf("worker.assigned events = %d", n)
	}
	cleared, err := s.AssignWorkerResource(ctx, w.ID, set.Version, nil, "admin", idem("hr-3"))
	if err != nil || cleared.DefaultResourceID != nil {
		t.Fatalf("cleared = %+v, %v", cleared, err)
	}
	if all, _ := s.ListJobs(ctx, JobFilter{WorkerID: w.ID}); len(all) != 2 {
		t.Fatalf("after clearing: %+v", all)
	}
	if l, err := s.ListJobs(testkit.Ctx(testkit.TenantID(72)), JobFilter{WorkerID: w.ID}); problemCode(err) != CodeWorkerNotFound || len(l) != 0 {
		t.Fatalf("other tenant: %+v, %v", l, err)
	}
}
