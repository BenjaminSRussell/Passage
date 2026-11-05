package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

func asUser(ctx context.Context, user string) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.UserID = user
	return tenant.With(ctx, p)
}

func withDuty(ctx context.Context, duty string) context.Context {
	p, _ := tenant.FromContext(ctx)
	d := map[string]struct{}{duty: {}}
	for k := range p.Duties {
		d[k] = struct{}{}
	}
	p.Duties = d
	return tenant.With(ctx, p)
}

func incidentFor(w Worker, le, site string, facts domain.IncidentFacts) IncidentInput {
	return IncidentInput{
		WorkerID: w.ID, LegalEntityID: le, SiteID: site, IncidentType: "INJURY",
		OccurredAt: day(time.March, 2).Add(9 * time.Hour), ReportedAt: day(time.March, 2).Add(11 * time.Hour),
		Location: "Press line 2", BodyParts: []string{"HAND"}, Facts: facts,
		Details: IncidentDetails{Description: "Hand caught in the guard", Witnesses: []string{"R. Okafor"}, Treatments: []string{"sutures"}},
	}
}

func mustIncident(t *testing.T, s *Postgres, ctx context.Context, in IncidentInput) Incident {
	t.Helper()
	i, _, err := s.ReportIncident(ctx, in, "")
	if err != nil {
		t.Fatalf("report incident: %v", err)
	}
	return i
}

func scenarioIncidents(t *testing.T, s *Postgres, ctx context.Context, tag string) {
	t.Helper()
	le, site := ids.New(), ids.New()
	w := mustWorker(t, s, ctx, "Inc"+tag)
	other := mustWorker(t, s, ctx, "Inc2"+tag)
	mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
	mustEmployment(t, s, ctx, other.ID, le, "PRIMARY", day(time.January, 1))
	elsewhere := incidentFor(w, ids.New(), site, domain.IncidentFacts{DaysAway: 1})
	_, _, err0 := s.ReportIncident(ctx, elsewhere, "")
	requireCode(t, err0, CodeIncidentEntity)

	away := mustIncident(t, s, ctx, incidentFor(w, le, site, domain.IncidentFacts{DaysAway: 3, MedicalTreatment: true}))
	if !away.OSHARecordable || away.Outcome != domain.OutcomeDaysAway || away.CaseNo < 1 {
		t.Fatalf("incident with 3 days away = %+v, want recordable DAYS_AWAY", away)
	}
	privacyIn := incidentFor(other, le, site, domain.IncidentFacts{MedicalTreatment: true})
	privacyIn.PrivacyCase = true
	privacy := mustIncident(t, s, ctx, privacyIn)
	firstAid := mustIncident(t, s, ctx, incidentFor(other, le, site, domain.IncidentFacts{}))
	if firstAid.OSHARecordable || firstAid.Outcome != domain.OutcomeNone || privacy.CaseNo != away.CaseNo+1 {
		t.Fatalf("first aid = %+v, privacy case no %d after %d", firstAid, privacy.CaseNo, away.CaseNo)
	}

	no := false
	denied := incidentFor(w, le, site, domain.IncidentFacts{DaysAway: 2})
	denied.RecordableOverride = &no
	_, _, err := s.ReportIncident(ctx, denied, "")
	requireCode(t, err, domain.CodeIncidentRecordable)
	denied.OverrideReason = "confirmed first aid by the plant physician"
	o := mustIncident(t, s, ctx, denied)
	if d, err := s.GetIncident(asUser(ctx, "ehs-reader"), o.ID); o.OSHARecordable || err != nil || d.Details.OverrideReason != denied.OverrideReason {
		t.Fatalf("justified override = %+v %v", o, err)
	}

	extract, err := s.OSHAExtract(ctx, 2026, &site)
	if err != nil {
		t.Fatal(err)
	}
	if len(extract.Log) != 2 || extract.Summary.DaysAwayCases != 1 || extract.Summary.TotalDaysAway != 3 || extract.Summary.OtherRecordable != 1 {
		t.Fatalf("extract = %+v", extract)
	}
	line, masked := extract.Log[0], extract.Log[1]
	if line.DaysAway != 3 || line.EmployeeName != w.LegalName || line.Description != "Hand caught in the guard" {
		t.Errorf("300 line = %+v", line)
	}
	if masked.EmployeeName != privacyCaseName || masked.WorkerID != nil || !masked.PrivacyCase {
		t.Errorf("privacy line = %+v, want the name and worker hidden", masked)
	}
	if empty, err := s.OSHAExtract(ctx, 2025, nil); err != nil || len(empty.Log) != 0 {
		t.Errorf("2025 extract = %+v, %v", empty, err)
	}
	page, err := s.ListIncidents(ctx, "", 0, 2)
	if err != nil || len(page.Items) != 2 || page.NextAfter == "" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	for _, i := range page.Items {
		if i.PrivacyCase && (i.WorkerID != nil || i.WorkerName != nil) {
			t.Errorf("list leaked the worker of privacy case %d", i.CaseNo)
		}
	}

	admin := asUser(ctx, "ehs-admin")
	got, err := s.GetIncident(admin, away.ID)
	if err != nil || got.Details == nil || got.Details.Description != "Hand caught in the guard" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	_, _, err = s.AdvanceIncident(admin, away.ID, domain.IncidentClosed)
	requireCode(t, err, domain.CodeIncidentState)
	if _, changed, err := s.AdvanceIncident(admin, away.ID, domain.IncidentUnderReview); err != nil || !changed {
		t.Fatalf("review: %v", err)
	}
	if _, changed, err := s.AdvanceIncident(admin, away.ID, domain.IncidentUnderReview); err != nil || changed {
		t.Fatalf("repeating the review must change nothing: %v", err)
	}
	_, _, err = s.AdvanceIncident(ctx, away.ID, domain.IncidentClosed)
	requireCode(t, err, domain.CodeIncidentSelfClose)
	closed, _, err := s.AdvanceIncident(admin, away.ID, domain.IncidentClosed)
	if err != nil || closed.Status != domain.IncidentClosed || closed.ClosedBy == nil || *closed.ClosedBy != "ehs-admin" {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update hr_incident set updated_at = now() where id = $1`, away.ID)
		return err
	})
	if err == nil || !pgCode(err, "23000") {
		t.Errorf("updating a CLOSED incident = %v, want the immutability error", err)
	}

	open := mustIncident(t, s, ctx, incidentFor(w, le, site, domain.IncidentFacts{DaysRestricted: 4}))
	fix := incidentFor(w, le, site, domain.IncidentFacts{DaysAway: 5})
	fix.SupersedesID = &away.ID
	_, _, err = s.ReportIncident(ctx, fix, "")
	requireCode(t, err, CodeCorrectionDuty)
	_, _, err = s.ReportIncident(withDuty(ctx, "hr.incident.admin"), fix, "")
	requireCode(t, err, CodeSelfCorrection)
	_, _, err = s.ReportIncident(withDuty(admin, "hr.incident.admin"), fix, "")
	requireCode(t, err, CodeSelfCorrection)
	lead := withDuty(asUser(ctx, "ehs-lead"), "hr.incident.admin")
	fix.SupersedesID = &open.ID
	_, _, err = s.ReportIncident(lead, fix, "")
	requireCode(t, err, domain.CodeIncidentState)
	fix.SupersedesID = &away.ID
	correction, _, err := s.ReportIncident(lead, fix, "")
	if err != nil || correction.SupersedesID == nil {
		t.Fatalf("correction = %+v, %v", correction, err)
	}
	_, _, err = s.ReportIncident(lead, fix, "")
	requireCode(t, err, CodeIncidentCorrected)
	onLog := func() bool {
		x, err := s.OSHAExtract(ctx, 2026, &site)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range x.Log {
			if l.IncidentID == away.ID {
				return true
			}
		}
		return false
	}
	if !onLog() {
		t.Fatal("a correction that is not yet CLOSED removed the original from the OSHA log")
	}
	chief := asUser(ctx, "ehs-chief")
	if _, _, err := s.AdvanceIncident(chief, correction.ID, domain.IncidentUnderReview); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdvanceIncident(chief, correction.ID, domain.IncidentClosed); err != nil {
		t.Fatal(err)
	}
	if onLog() {
		t.Errorf("the superseded incident is still on the log after its correction closed")
	}
	if _, err := s.GetIncident(ctx, ids.New()); err == nil {
		t.Error("unknown incident found")
	}
}

func TestIncidentRegisterAndOSHAExtractAC4211(t *testing.T) {
	s, ctx := newStore(t)
	scenarioIncidents(t, s, ctx, "A")
}

func TestIncidentSealedAuditedAndEventsCarryNoMedicalText(t *testing.T) {
	s, ctx := newStore(t)
	w := mustWorker(t, s, ctx, "Sealed")
	le, site := ids.New(), ids.New()
	mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
	in := incidentFor(w, le, site, domain.IncidentFacts{DaysAway: 3})
	inc, inserted, err := s.ReportIncident(ctx, in, "inc-key-1")
	if err != nil || !inserted {
		t.Fatalf("report: %v", err)
	}
	replay, inserted, err := s.ReportIncident(ctx, in, "inc-key-1")
	if err != nil || inserted || replay.ID != inc.ID {
		t.Fatalf("replay = %+v inserted=%v %v", replay, inserted, err)
	}
	other := incidentFor(w, le, site, domain.IncidentFacts{})
	other.IncidentType = "RESPIRATORY"
	_, _, err = s.ReportIncident(ctx, other, "inc-key-1")
	requireCode(t, err, CodeIdempotencyReuse)

	var sealed []byte
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select details_sealed from hr_incident where id = $1`, inc.ID).Scan(&sealed)
	})
	if err != nil || len(sealed) == 0 || strings.Contains(string(sealed), "Hand caught") {
		t.Fatalf("details are not sealed: %v", err)
	}
	if _, err := s.GetIncident(ctx, inc.ID); err != nil {
		t.Fatal(err)
	}
	var reads int
	_ = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from audit_event where entity = 'incident' and entity_id = $1 and action = 'p3_read'`, inc.ID).Scan(&reads)
	})
	if reads != 1 {
		t.Errorf("incident reads audited = %d, want 1", reads)
	}
	rows := outboxFor(t, s, ctx, inc.ID)
	if len(rows) != 1 || rows[0].Type != hrevents.IncidentChanged || rows[0].Data["status"] != "REPORTED" {
		t.Fatalf("events = %+v", rows)
	}
	for k, v := range rows[0].Data {
		if strings.Contains(fmt.Sprint(v), "guard") || k == "description" || k == "legal_name" {
			t.Errorf("event leaks %s=%v", k, v)
		}
	}
	hidden := incidentFor(w, le, site, domain.IncidentFacts{DaysAway: 1})
	hidden.PrivacyCase = true
	pc := mustIncident(t, s, ctx, hidden)
	pcEvents := outboxFor(t, s, ctx, pc.ID)
	for _, k := range []string{"worker_id", "incident_type", "outcome"} {
		if _, leaked := pcEvents[0].Data[k]; leaked {
			t.Errorf("privacy-case event carries %s", k)
		}
	}
}

func staffed(t *testing.T, s *Postgres, ctx context.Context, name, dept, le string, position Position) Worker {
	t.Helper()
	w := mustWorker(t, s, ctx, name)
	emp := mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
	mustAssignment(t, s, ctx, position.ID, w.ID, emp.ID, "0.05", day(time.January, 1), nil)
	return w
}

func scenarioOffboarding(t *testing.T, s *Postgres, ctx context.Context, tag string) {
	t.Helper()
	w := mustWorker(t, s, ctx, "Off"+tag)
	due := "2026-03-20"
	laptop, _, err := s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "LAPTOP", ItemDescription: "ThinkPad T14", Serial: strp("SN-1"), IssuedOn: "2026-01-05", DueOn: &due}, "")
	if err != nil || laptop.Status != domain.LoanOut {
		t.Fatalf("issue: %+v %v", laptop, err)
	}
	if _, _, err := s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "BADGE", ItemDescription: "x", IssuedOn: "2026-02-02", DueOn: strp("2026-02-01")}, ""); err == nil {
		t.Error("due before issue accepted")
	}
	outstanding, err := s.ListOutstandingLoans(ctx, "2026-03-31")
	if err != nil || len(outstanding) < 1 {
		t.Fatalf("outstanding = %v %v", outstanding, err)
	}

	tpl, _, err := s.CreateChecklistTemplate(ctx, ChecklistTemplate{Code: "OFF-" + tag, Kind: "OFFBOARDING", Name: "Leaver", Tasks: []TemplateTask{
		{Code: "REVOKE_ACCESS", Title: "Revoke access", OwnerRole: "IT", DueOffsetDays: 0, EvidenceRequired: true},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	eff := day(time.March, 15)
	list, created, err := s.StartChecklist(ctx, w.ID, tpl.ID, eff, nil)
	if err != nil || !created || len(list.Tasks) != 2 {
		t.Fatalf("start = %+v %v", list, err)
	}
	if again, created, err := s.StartChecklist(ctx, w.ID, tpl.ID, eff, nil); err != nil || created || again.ID != list.ID {
		t.Fatalf("starting twice must return the stored checklist: %+v %v", again, err)
	}
	var returnTask, access WorkerTask
	for _, k := range list.Tasks {
		if k.TaskCode == domain.ReturnLoanedItemsCode {
			returnTask = k
		} else {
			access = k
		}
	}
	_, _, err = s.FinishTask(ctx, returnTask.ID, domain.TaskDone, nil)
	requireCode(t, err, domain.CodeLoanedItemsOut)
	_, _, err = s.FinishTask(ctx, returnTask.ID, domain.TaskSkipped, nil)
	requireCode(t, err, domain.CodeLoanedItemsOut)
	_, _, err = s.FinishTask(ctx, access.ID, domain.TaskDone, nil)
	requireCode(t, err, CodeTaskEvidence)
	if _, changed, err := s.FinishTask(ctx, access.ID, domain.TaskDone, strp("ticket-42")); err != nil || !changed {
		t.Fatalf("finish access: %v", err)
	}
	if _, changed, err := s.FinishTask(ctx, access.ID, domain.TaskDone, strp("ticket-42")); err != nil || changed {
		t.Fatalf("finishing twice must change nothing: %v", err)
	}
	_, _, err = s.CompleteChecklist(ctx, list.ID)
	requireCode(t, err, CodeOpenTasks)

	closed, changed, err := s.CloseLoanedItem(ctx, laptop.ID, domain.LoanReturned, "2026-03-14", strp("scratched lid"))
	if err != nil || !changed || closed.Status != domain.LoanReturned || closed.ConditionIn == nil {
		t.Fatalf("return: %+v %v", closed, err)
	}
	if _, changed, err := s.CloseLoanedItem(ctx, laptop.ID, domain.LoanReturned, "2026-03-14", nil); err != nil || changed {
		t.Fatalf("returning twice must change nothing: %v", err)
	}
	_, _, err = s.CloseLoanedItem(ctx, laptop.ID, domain.LoanLost, "2026-03-14", nil)
	requireCode(t, err, CodeLoanState)

	phone, _, err := s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "PHONE", ItemDescription: "Pixel", IssuedOn: "2026-03-16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FinishTask(ctx, returnTask.ID, domain.TaskDone, nil); err == nil {
		t.Fatal("a second item went out and the return task still completed")
	}
	if _, _, err := s.CloseLoanedItem(ctx, phone.ID, domain.LoanLost, "2026-03-17", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FinishTask(ctx, returnTask.ID, domain.TaskDone, nil); err != nil {
		t.Fatalf("return task after all items closed: %v", err)
	}
	third, _, err := s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "KEYS", ItemDescription: "Gate keys", IssuedOn: "2026-03-18"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CompleteChecklist(ctx, list.ID)
	requireCode(t, err, domain.CodeLoanedItemsOut)
	if _, _, err := s.CloseLoanedItem(ctx, third.ID, domain.LoanReturned, "2026-03-19", nil); err != nil {
		t.Fatal(err)
	}
	done, changed, err := s.CompleteChecklist(ctx, list.ID)
	if err != nil || !changed || done.Status != domain.ChecklistCompleted {
		t.Fatalf("complete = %+v %v", done, err)
	}
	if loans, err := s.ListWorkerLoans(ctx, w.ID); err != nil || len(loans) != 3 {
		t.Errorf("worker loans = %v %v", loans, err)
	}
	if lists, err := s.ListWorkerChecklists(ctx, w.ID); err != nil || len(lists) != 1 || lists[0].Status != domain.ChecklistCompleted {
		t.Errorf("worker checklists = %+v %v", lists, err)
	}
}

func TestLoanedItemBlocksOffboardingAC4215(t *testing.T) {
	s, ctx := newStore(t)
	scenarioOffboarding(t, s, ctx, "A")
}

func scenarioOnboarding(t *testing.T, s *Postgres, ctx context.Context, tag string) {
	t.Helper()
	le, dept := ids.New(), ids.New()
	boss := mustPosition(t, s, ctx, "POS-B"+tag, dept, le, "1.00", true, day(time.January, 1), nil)
	desk := mustPosition(t, s, ctx, "POS-D"+tag, dept, le, "1.00", true, day(time.January, 1), &boss.ID)
	manager := staffed(t, s, ctx, "Mgr"+tag, dept, le, boss)
	hire := staffed(t, s, ctx, "Hire"+tag, dept, le, desk)
	tpl, _, err := s.CreateChecklistTemplate(ctx, ChecklistTemplate{Code: "ON-" + tag, Kind: "ONBOARDING", Name: "Joiner", Tasks: []TemplateTask{
		{Code: "WELCOME", Title: "Welcome call", OwnerRole: "MANAGER", DueOffsetDays: -5},
		{Code: "SIGN", Title: "Sign handbook", OwnerRole: "WORKER", DueOffsetDays: 3},
		{Code: "LAPTOP", Title: "Order laptop", OwnerRole: "IT", DueOffsetDays: -10},
	}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateChecklistTemplate(ctx, ChecklistTemplate{Code: "ON-" + tag, Kind: "ONBOARDING", Name: "Again", Tasks: tpl.Tasks}, ""); err == nil {
		t.Error("duplicate template code accepted")
	}
	list, _, err := s.StartChecklist(ctx, hire.ID, tpl.ID, day(time.November, 2), nil)
	if err != nil || len(list.Tasks) != 3 {
		t.Fatalf("start = %+v %v", list, err)
	}
	byCode := map[string]WorkerTask{}
	for _, k := range list.Tasks {
		byCode[k.TaskCode] = k
	}
	welcome := byCode["WELCOME"]
	if welcome.DueOn != "2026-10-28" || welcome.OwnerWorkerID == nil || *welcome.OwnerWorkerID != manager.ID {
		t.Errorf("welcome task = %+v, want due 2026-10-28 owned by the manager %s", welcome, manager.ID)
	}
	if sign := byCode["SIGN"]; sign.DueOn != "2026-11-05" || sign.OwnerWorkerID == nil || *sign.OwnerWorkerID != hire.ID {
		t.Errorf("sign task = %+v", sign)
	}
	if it := byCode["LAPTOP"]; it.OwnerWorkerID != nil || it.DueOn != "2026-10-23" {
		t.Errorf("IT task = %+v, want unassigned", it)
	}
	owned, err := s.ListOwnedTasks(ctx, manager.ID, domain.TaskOpen)
	if err != nil || len(owned) != 1 || owned[0].TaskCode != "WELCOME" {
		t.Errorf("manager tasks = %+v %v", owned, err)
	}
	if _, _, err := s.StartChecklist(ctx, hire.ID, ids.New(), day(time.November, 2), nil); err == nil {
		t.Error("unknown template accepted")
	}
	if tpls, err := s.ListChecklistTemplates(ctx, "ONBOARDING"); err != nil || len(tpls) < 1 {
		t.Errorf("onboarding templates = %v %v", tpls, err)
	}
}

func TestOnboardingTaskDueAndOwnerAC4212(t *testing.T) {
	s, ctx := newStore(t)
	scenarioOnboarding(t, s, ctx, "A")
}

func TestChecklistAndLoanEvents(t *testing.T) {
	s, ctx := newStore(t)
	w := mustWorker(t, s, ctx, "Evt")
	tpl, _, err := s.CreateChecklistTemplate(ctx, ChecklistTemplate{Code: "EVT", Kind: "TRANSFER", Name: "Move", Tasks: []TemplateTask{{Code: "T1", Title: "t", OwnerRole: "WORKER"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := s.StartChecklist(ctx, w.ID, tpl.ID, day(time.March, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FinishTask(ctx, list.Tasks[0].ID, domain.TaskSkipped, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteChecklist(ctx, list.ID); err != nil {
		t.Fatal(err)
	}
	rows := outboxFor(t, s, ctx, list.ID)
	if len(rows) != 2 || rows[0].Type != hrevents.ChecklistChanged || rows[0].Data["status"] != "OPEN" || rows[1].Data["status"] != "COMPLETED" {
		t.Fatalf("checklist events = %+v", rows)
	}
	item, _, err := s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "BADGE", ItemDescription: "badge", IssuedOn: "2026-03-01"}, "loan-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "BADGE", ItemDescription: "badge", IssuedOn: "2026-03-01"}, "loan-key"); err != nil || inserted {
		t.Fatalf("replay inserted=%v %v", inserted, err)
	}
	_, _, err = s.IssueLoanedItem(ctx, LoanedItem{WorkerID: w.ID, LoanType: "BADGE", ItemDescription: "other", IssuedOn: "2026-03-01"}, "loan-key")
	requireCode(t, err, CodeIdempotencyReuse)
	if _, _, err := s.CloseLoanedItem(ctx, item.ID, domain.LoanReturned, "2026-03-02", nil); err != nil {
		t.Fatal(err)
	}
	ev := outboxFor(t, s, ctx, item.ID)
	if len(ev) != 2 || ev[0].Data["status"] != "OUT" || ev[1].Data["status"] != "RETURNED" {
		t.Fatalf("loan events = %+v", ev)
	}
}

func peopleWorker(t *testing.T, s *Postgres, ctx context.Context, n int, le string, position Position) Worker {
	t.Helper()
	user := testkit.TenantID(100 + n)
	w, _, err := s.CreateWorker(ctx, WorkerInput{
		Worker:     Worker{PersonnelNumber: fmt.Sprintf("WRK-Q%02d", n), WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: fmt.Sprintf("Respondent %d", n), UserID: &user},
		NationalID: "123-45-6789",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	emp := mustEmployment(t, s, ctx, w.ID, le, "PRIMARY", day(time.January, 1))
	mustAssignment(t, s, ctx, position.ID, w.ID, emp.ID, "0.05", day(time.January, 1), nil)
	return w
}

func answersDoc(scale int) map[string]json.RawMessage {
	return map[string]json.RawMessage{"ENGAGE": []byte(fmt.Sprint(scale)), "NOTE": []byte(`"fine"`)}
}

func scenarioQuestionnaire(t *testing.T, s *Postgres, ctx context.Context, tag string) {
	t.Helper()
	at(s, day(time.March, 10))
	le, dept := ids.New(), ids.New()
	pos := mustPosition(t, s, ctx, "POS-Q"+tag, dept, le, "1.00", true, day(time.January, 1), nil)
	var workers []Worker
	for n := 1; n <= 12; n++ {
		workers = append(workers, peopleWorker(t, s, ctx, n, le, pos))
	}
	userCtx := func(n int) context.Context { return asUser(ctx, testkit.TenantID(100+n)) }

	design := Questionnaire{Code: "PULSE-" + tag, Name: "Pulse", Anonymous: true, Questions: []domain.Question{
		{Code: "ENGAGE", Text: "I would recommend this team", Type: domain.QuestionScale, ScaleMax: 5, Weight: "1", Mandatory: true, ResultGroup: "ENGAGEMENT"},
		{Code: "NOTE", Text: "Anything else", Type: domain.QuestionText, Weight: "1"},
	}}
	q, _, err := s.CreateQuestionnaire(ctx, design, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateQuestionnaire(ctx, design, ""); err == nil {
		t.Error("duplicate questionnaire code accepted")
	}
	if _, _, err := s.CreateSchedule(ctx, q.ID, `employment_type ==`, day(time.March, 1), day(time.March, 31), ""); err == nil {
		t.Error("broken population rule accepted")
	}
	sched, _, err := s.CreateSchedule(ctx, q.ID, `employment_type == "FULL_TIME"`, day(time.March, 1), day(time.March, 31), "")
	if err != nil || sched.InvitedCount < 12 || sched.Status != "OPEN" {
		t.Fatalf("schedule = %+v %v", sched, err)
	}
	if open, err := s.ListOpenSchedules(ctx); err != nil || len(open) != 1 {
		t.Fatalf("open schedules = %v %v", open, err)
	}
	if v, err := s.GetSchedule(ctx, sched.ID); err != nil || len(v.Questionnaire.Questions) != 2 {
		t.Fatalf("schedule view = %+v %v", v, err)
	}

	if err := s.SubmitAnswers(userCtx(2), sched.ID, workers[0].ID, answersDoc(4)); err == nil {
		t.Error("answered for another worker")
	}
	if err := s.SubmitAnswers(userCtx(1), sched.ID, workers[0].ID, map[string]json.RawMessage{"ENGAGE": []byte("9")}); err == nil {
		t.Error("out of range answer accepted")
	}
	for n := 1; n <= 4; n++ {
		if err := s.SubmitAnswers(userCtx(n), sched.ID, workers[n-1].ID, answersDoc(n)); err != nil {
			t.Fatalf("answer %d: %v", n, err)
		}
	}
	requireCode(t, s.SubmitAnswers(userCtx(1), sched.ID, workers[0].ID, answersDoc(4)), CodeAlreadyAnswered)

	var withRespondent int
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*)::int from hr_questionnaire_session where schedule_id = $1 and respondent_worker_id is not null`, sched.ID).Scan(&withRespondent)
	}); err != nil || withRespondent != 0 {
		t.Fatalf("anonymous sessions carrying a respondent id = %d, %v", withRespondent, err)
	}
	_, err = s.QuestionnaireResults(everyEntity(ctx), sched.ID, false)
	requireCode(t, err, CodeResultsOpen)
	_, err = s.QuestionnaireResults(everyEntity(ctx), sched.ID, true)
	requireCode(t, err, CodeResultsOpen)
	var sessionIDs []string
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from hr_questionnaire_session where schedule_id = $1`, sched.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			sessionIDs = append(sessionIDs, id)
		}
		return rows.Err()
	}); err != nil || len(sessionIDs) != 4 {
		t.Fatalf("sessions = %v %v", sessionIDs, err)
	}
	for _, id := range sessionIDs {
		if id[14] != '4' {
			t.Errorf("anonymous session id %s is not a random v4 uuid", id)
		}
	}
	var finer int
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*)::int from information_schema.columns where table_name = 'hr_questionnaire_session'
			and (column_name like '%worker%' and column_name <> 'respondent_worker_id' or data_type like 'timestamp%')`).Scan(&finer)
	}); err != nil || finer != 0 {
		t.Fatalf("session table has a worker link or a timestamp column: %d %v", finer, err)
	}
	at(s, day(time.April, 2))
	res, err := s.QuestionnaireResults(everyEntity(ctx), sched.ID, false)
	if err != nil || res.Responded != 4 || res.Invited < 12 || !res.Suppressed || len(res.Overall.Questions) != 0 || len(res.Overall.ResultGroups) != 0 {
		t.Fatalf("4 of 12 anonymous responses must report counts only: %+v %v", res, err)
	}
	_, err = s.QuestionnaireResults(everyEntity(ctx), sched.ID, true)
	requireCode(t, err, domain.CodeQuestionnaireThreshold)

	at(s, day(time.March, 10))
	if err := s.SubmitAnswers(userCtx(5), sched.ID, workers[4].ID, answersDoc(5)); err != nil {
		t.Fatal(err)
	}
	at(s, day(time.April, 2))
	full, err := s.QuestionnaireResults(everyEntity(ctx), sched.ID, false)
	if err != nil || full.Suppressed || len(full.Overall.ResultGroups) != 1 || *full.Overall.ResultGroups[0].MeanScore != "3.00" {
		t.Fatalf("5 responses show the overall figures: %+v %v", full, err)
	}
	small, _, err := s.CreateQuestionnaire(ctx, Questionnaire{Code: "SMALL-" + tag, Name: "Small", Anonymous: true, Questions: design.Questions}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.CreateSchedule(ctx, small.ID, `legal_entity == "`+ids.New()+`"`, day(time.March, 1), day(time.March, 31), "")
	requireCode(t, err, domain.CodeQuestionnaireThreshold)
	grouped, err := s.QuestionnaireResults(everyEntity(ctx), sched.ID, true)
	if err != nil || len(grouped.Groups) != 1 || grouped.Groups[0].Stats.Respondents != 5 || grouped.Groups[0].DepartmentID == nil || *grouped.Groups[0].DepartmentID != dept {
		t.Fatalf("grouped = %+v %v", grouped, err)
	}

	entityOnly := tenant.With(ctx, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "scoped", Duties: testkit.ScopeDuties(le)})
	_, err = s.QuestionnaireResults(entityOnly, sched.ID, false)
	requireCode(t, err, CodeQuestionnaireScope)
	_, err = s.QuestionnaireResults(entityOnly, sched.ID, true)
	requireCode(t, err, CodeQuestionnaireScope)

	requireCode(t, s.SubmitAnswers(userCtx(6), sched.ID, workers[5].ID, answersDoc(3)), domain.CodeQuestionnaireClosed)
	at(s, day(time.March, 10))
}

func TestAnonymousQuestionnaireThresholdAC4210(t *testing.T) {
	s, ctx := newStore(t)
	scenarioQuestionnaire(t, s, ctx, "A")
}

func TestQuestionnaireNamedRespondentAndPopulation(t *testing.T) {
	s, ctx := newStore(t)
	at(s, day(time.March, 10))
	le := ids.New()
	pos := mustPosition(t, s, ctx, "POS-N", ids.New(), le, "1.00", true, day(time.January, 1), nil)
	in, out := peopleWorker(t, s, ctx, 1, le, pos), peopleWorker(t, s, ctx, 2, le, pos)
	q, _, err := s.CreateQuestionnaire(ctx, Questionnaire{Code: "NAMED", Name: "Named", Questions: []domain.Question{
		{Code: "SCORE", Text: "n", Type: domain.QuestionNumeric, Weight: "1"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	sched, _, err := s.CreateSchedule(ctx, q.ID, `service_days >= 0 && legal_entity == "`+le+`"`, day(time.March, 1), day(time.March, 31), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SubmitAnswers(asUser(ctx, testkit.TenantID(101)), sched.ID, in.ID, map[string]json.RawMessage{"SCORE": []byte("12.5")}); err != nil {
		t.Fatal(err)
	}
	res, err := s.QuestionnaireResults(ctx, sched.ID, true)
	if err != nil || len(res.Groups) != 1 || res.Groups[0].Stats.Respondents != 1 {
		t.Fatalf("named questionnaires have no threshold: %+v %v", res, err)
	}
	q2, _, _ := s.CreateQuestionnaire(ctx, Questionnaire{Code: "ELSE", Name: "Else", Questions: q.Questions}, "")
	other, _, err := s.CreateSchedule(ctx, q2.ID, `legal_entity == "`+ids.New()+`"`, day(time.March, 1), day(time.March, 31), "")
	if err != nil || other.InvitedCount != 0 {
		t.Fatalf("schedule = %+v %v", other, err)
	}
	requireCode(t, s.SubmitAnswers(asUser(ctx, testkit.TenantID(102)), other.ID, out.ID, map[string]json.RawMessage{"SCORE": []byte("1")}), CodeNotInvited)
	early, _, _ := s.CreateSchedule(ctx, q2.ID, "", day(time.April, 1), day(time.April, 30), "")
	requireCode(t, s.SubmitAnswers(asUser(ctx, testkit.TenantID(102)), early.ID, out.ID, map[string]json.RawMessage{"SCORE": []byte("1")}), CodeQuestionnaireNotOpen)
}

func TestGrants_PeopleSafetyPathsWorkAsAppRole(t *testing.T) {
	s, ctx := appStore(t)
	scenarioIncidents(t, s, ctx, "G")
	scenarioOffboarding(t, s, ctx, "G")
	scenarioOnboarding(t, s, ctx, "G")
	scenarioQuestionnaire(t, s, ctx, "G")
	other := ids.New()
	foreign := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: other, UserID: "scoped", Duties: testkit.ScopeDuties(other)})
	for _, table := range []string{"hr_questionnaire_session", "hr_questionnaire_responded", "hr_incident", "hr_loaned_item", "hr_checklist", "hr_worker_task"} {
		var all, scoped int
		if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*)::int from `+table).Scan(&all)
		}); err != nil || all == 0 {
			t.Fatalf("%s: %d rows for the all-entity caller, %v", table, all, err)
		}
		if err := dbx.WithTenantTx(foreign, s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(foreign, `select count(*)::int from `+table).Scan(&scoped)
		}); err != nil || scoped != 0 {
			t.Errorf("%s: a caller scoped to another legal entity sees %d rows, %v", table, scoped, err)
		}
	}
}

func everyEntity(ctx context.Context) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID, p.Duties = "", testkit.ScopeDuties("")
	return tenant.With(ctx, p)
}
