package store_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/api"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyLearningAdmin   = "HcmLearningAdmin"
	dutyRecruitMaintain = "HcmRecruitMaintain"
)

func TestTeamViewsDoNotExposeIdentityLinks(t *testing.T) {
	o := newOrg(t)
	for name, target := range map[string]string{
		"list":   "/v1/hr/me/team",
		"member": "/v1/hr/me/team/" + o.workers["rep"].ID,
	} {
		rec := o.as(o.users["mgr"], http.MethodGet, target, "", dutyMssBase)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, o.workers["rep"].ID) {
			t.Fatalf("%s: status %d body %s", name, rec.Code, body)
		}
		for _, leaked := range []string{"userId", "partyPersonId", o.users["rep"], o.workers["rep"].PartyPersonID} {
			if strings.Contains(body, leaked) {
				t.Errorf("%s exposes %q: %s", name, leaked, body)
			}
		}
	}
}

func TestCourseApprovalIsLimitedToDirectReportsUnlessLearningAdmin(t *testing.T) {
	o := newOrg(t)
	course, _, err := o.st.CreateCourse(o.ctx, store.Course{Code: "SAFE-1", Name: "Safety induction", RequiresManagerApproval: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().AddDate(1, 0, 0)
	session, _, err := o.st.CreateSession(o.ctx, store.Session{CourseID: course.ID, StartsAt: start, EndsAt: start.Add(time.Hour),
		VirtualURL: &[]string{"https://learn.example/s1"}[0], Capacity: 10, WaitlistCapacity: 0}, "")
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]store.Registration{}
	for _, name := range []string{"rep", "peer", "ceo"} {
		reg, _, _, err := o.st.RegisterWorker(o.ctx, session.ID, o.workers[name].ID, false)
		if err != nil || reg.Status != domain.RegRequested {
			t.Fatalf("register %s: %+v %v", name, reg, err)
		}
		registered[name] = reg
	}
	approve := func(user, name string, duties ...string) *httptest.ResponseRecorder {
		return o.as(user, http.MethodPost, "/v1/hr/courses/registrations/"+registered[name].ID+"/approve", "", append([]string{dutyMssApprove}, duties...)...)
	}

	for _, name := range []string{"peer", "ceo"} {
		rec := approve(o.users["mgr"], name)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeRegistrationNotFound) {
			t.Errorf("manager approving %s's registration: status %d body %s, want the uniform 404", name, rec.Code, rec.Body.String())
		}
	}
	if rec := approve(o.users["mgr"], "rep"); rec.Code != http.StatusOK {
		t.Fatalf("manager approving a direct report's registration: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := approve(o.users["mgr"], "ceo"); rec.Code != http.StatusNotFound {
		t.Fatalf("a refused approval must stay refused: status %d", rec.Code)
	}
	if rec := approve(ids.New(), "peer"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeNoLinkedWorker) {
		t.Fatalf("unlinked approver: status %d body %s, want %s", rec.Code, rec.Body.String(), store.CodeNoLinkedWorker)
	}
	if rec := approve(o.users["peer"], "ceo", dutyLearningAdmin); rec.Code != http.StatusOK {
		t.Fatalf("learning admin approving outside their team: status %d body %s", rec.Code, rec.Body.String())
	}
	got, err := o.st.GetSession(o.ctx, session.ID)
	if err != nil || got.Confirmed != 2 || got.Requested != 1 {
		t.Fatalf("session = %+v err %v, want rep and ceo confirmed and peer still requested", got, err)
	}
}

type recordingSequencer struct {
	issued map[string]string
	calls  int
	err    error
	format string
}

func (s *recordingSequencer) Next(_ context.Context, _, key string) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	if s.issued == nil {
		s.issued = map[string]string{}
	}
	if _, ok := s.issued[key]; !ok {
		s.issued[key] = s.format + "-" + string(rune('0'+len(s.issued)+1))
	}
	return s.issued[key], nil
}

func (o *org) withSequencer(seq api.Sequencer) {
	r := chi.NewRouter()
	api.New(o.st).WithSequencer(seq).Register(r)
	o.router = r
}

func (o *org) requisitionBody(t *testing.T, number string) string {
	t.Helper()
	view, err := o.st.WorkerAsOf(o.ctx, o.workers["rep"].ID, time.Now().UTC(), time.Now().UTC().Format("2006-01-02"))
	if err != nil || view.PositionID == nil {
		t.Fatalf("rep position: %+v %v", view, err)
	}
	body := `{"positionId":"` + *view.PositionID + `","headcount":1,"hiringManagerWorkerId":"` + o.workers["mgr"].ID + `","plannedStartDate":"2030-01-01"`
	if number != "" {
		body += `,"requisitionNumber":"` + number + `"`
	}
	return body + `}`
}

func (o *org) postRequisition(user, body, key string) *httptest.ResponseRecorder {
	p := tenant.Principal{TenantID: tid(), UserID: user, Duties: map[string]struct{}{tenant.DutyAllScope: {}, dutyRecruitMaintain: {}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/hr/requisitions", strings.NewReader(body)).WithContext(tenant.With(context.Background(), p))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	o.router.ServeHTTP(rec, req)
	return rec
}

func TestRequisitionNumberComesFromTheSequenceWhenOmitted(t *testing.T) {
	o := newOrg(t)
	seq := &recordingSequencer{format: "REQ"}
	o.withSequencer(seq)
	body := o.requisitionBody(t, "")

	first := o.postRequisition(o.users["mgr"], body, "key-1")
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), `"requisitionNumber":"REQ-1"`) {
		t.Fatalf("create: status %d body %s", first.Code, first.Body.String())
	}
	replay := o.postRequisition(o.users["mgr"], body, "key-1")
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"requisitionNumber":"REQ-1"`) || len(seq.issued) != 1 {
		t.Fatalf("replay: status %d body %s issued %v, want the same number from the same key", replay.Code, replay.Body.String(), seq.issued)
	}
	if second := o.postRequisition(o.users["mgr"], body, ""); second.Code != http.StatusCreated || !strings.Contains(second.Body.String(), `"requisitionNumber":"REQ-2"`) {
		t.Fatalf("keyless create: status %d body %s, want the next number", second.Code, second.Body.String())
	}

	calls := seq.calls
	manual := o.postRequisition(o.users["mgr"], o.requisitionBody(t, "man-1"), "")
	if manual.Code != http.StatusCreated || !strings.Contains(manual.Body.String(), `"requisitionNumber":"MAN-1"`) || seq.calls != calls {
		t.Fatalf("supplied number: status %d body %s, sequence calls %d -> %d", manual.Code, manual.Body.String(), calls, seq.calls)
	}
}

func TestRequisitionCreationFailsClosedWhenNumberingFailsOrIssuesABadNumber(t *testing.T) {
	o := newOrg(t)
	o.withSequencer(&recordingSequencer{err: httpx.NewProblem(http.StatusServiceUnavailable, "HCM-NUMBERING-UNAVAILABLE", "numbering service unavailable")})
	rec := o.postRequisition(o.users["mgr"], o.requisitionBody(t, ""), "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "HCM-NUMBERING-UNAVAILABLE") {
		t.Fatalf("numbering down: status %d body %s", rec.Code, rec.Body.String())
	}

	o.withSequencer(&recordingSequencer{err: errors.New("boom")})
	if rec := o.postRequisition(o.users["mgr"], o.requisitionBody(t, ""), ""); rec.Code == http.StatusCreated {
		t.Fatalf("a failing sequencer still created a requisition: %s", rec.Body.String())
	}

	o.withSequencer(&recordingSequencer{format: "bad number!"})
	rec = o.postRequisition(o.users["mgr"], o.requisitionBody(t, ""), "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "HCM-REQ-NUMBER-INVALID") {
		t.Fatalf("malformed number: status %d body %s", rec.Code, rec.Body.String())
	}

	o.router = func() http.Handler { r := chi.NewRouter(); api.New(o.st).Register(r); return r }()
	if rec := o.postRequisition(o.users["mgr"], o.requisitionBody(t, ""), ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no sequencer configured: status %d, want 503", rec.Code)
	}
	if items, _, err := o.st.ListRequisitions(o.ctx, "", "", 10); err != nil || len(items) != 0 {
		t.Fatalf("%d requisitions exist (err %v), want none since every attempt failed", len(items), err)
	}
}

func TestManagerWorksheetShowsAndAcceptsOnlyDirectReports(t *testing.T) {
	o := newOrg(t)
	plan, _, err := o.st.CreateCompensationPlan(o.ctx, store.CompensationPlan{Code: "SAL-USD", Name: "Salary", PayBasis: "ANNUAL", Currency: "USD"}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mgr", "rep", "peer"} {
		emps, err := o.st.ListEmployments(o.ctx, o.workers[name].ID)
		if err != nil || len(emps) != 1 {
			t.Fatalf("%s employments: %v %v", name, emps, err)
		}
		_, _, err = o.st.SubmitAction(o.ctx, store.ActionInput{ActionType: "COMP_CHANGE", WorkerID: o.workers[name].ID, EmploymentID: &emps[0].ID,
			EffectiveDate: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Payload: store.ActionPayload{CompPlanID: plan.ID, Amount: "60000.00", Currency: "USD"}}, "")
		if err != nil {
			t.Fatalf("pay for %s: %v", name, err)
		}
	}
	view, err := o.st.WorkerAsOf(o.ctx, o.workers["rep"].ID, time.Now().UTC(), time.Now().UTC().Format("2006-01-02"))
	if err != nil || view.PositionID == nil {
		t.Fatalf("rep position: %+v %v", view, err)
	}
	position, err := o.st.GetPosition(o.ctx, *view.PositionID)
	if err != nil {
		t.Fatal(err)
	}
	dept := position.DepartmentID
	cycle, _, err := o.st.CreateCompCycle(o.ctx, store.CompCycle{Name: "Merit 2026", Kind: "MERIT", EffectiveDate: "2026-07-01", Currency: "USD", AllowOverBudget: true,
		Budgets: []store.CycleBudget{{DepartmentID: dept, BudgetAmount: "100000.00"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.st.AdvanceCompCycle(o.ctx, cycle.ID, "MANAGER_INPUT"); err != nil {
		t.Fatal(err)
	}

	sheet := "/v1/hr/me/compensation/cycles/" + cycle.ID + "/worksheet"
	row := func(name string) string {
		return `{"workerId":"` + o.workers[name].ID + `","rating":3,"proposedPct":"3","justification":"merit"}`
	}
	save := func(user string, names ...string) *httptest.ResponseRecorder {
		rows := make([]string, len(names))
		for i, n := range names {
			rows[i] = row(n)
		}
		return o.as(user, http.MethodPut, sheet, `{"rows":[`+strings.Join(rows, ",")+`]}`, dutyMssApprove)
	}

	if rec := save(o.users["mgr"], "rep", "peer"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeWorkerNotFound) {
		t.Fatalf("a row for a worker outside the team: status %d body %s, want 404", rec.Code, rec.Body.String())
	}
	if saved, err := o.st.GetWorksheet(o.ctx, cycle.ID, o.workers["mgr"].ID); err != nil || len(saved) != 0 {
		t.Fatalf("a refused save wrote %d rows (err %v), want none", len(saved), err)
	}
	if rec := save(o.users["mgr"], "mgr"); rec.Code != http.StatusNotFound {
		t.Fatalf("a manager's own row: status %d, want 404 since they do not report to themselves", rec.Code)
	}
	ok := save(o.users["mgr"], "rep")
	if got := decodeBody[listOf[store.WorksheetRow]](t, ok); ok.Code != http.StatusOK || len(got.Items) != 1 || got.Items[0].WorkerID != o.workers["rep"].ID {
		t.Fatalf("save: status %d body %s", ok.Code, ok.Body.String())
	}
	if rec := save(ids.New(), "rep"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeNoLinkedWorker) {
		t.Fatalf("unlinked manager: status %d body %s", rec.Code, rec.Body.String())
	}

	if _, err := o.st.SaveWorksheet(o.ctx, cycle.ID, o.workers["mgr"].ID, []store.WorksheetRowInput{{WorkerID: o.workers["peer"].ID, Rating: 3, ProposedPct: "3"}}); err != nil {
		t.Fatal(err)
	}
	if all, err := o.st.GetWorksheet(o.ctx, cycle.ID, o.workers["mgr"].ID); err != nil || len(all) != 2 {
		t.Fatalf("admin view has %d rows (err %v), want rep and the stale peer row", len(all), err)
	}
	read := o.as(o.users["mgr"], http.MethodGet, sheet, "", dutyMssApprove)
	if got := decodeBody[listOf[store.WorksheetRow]](t, read); read.Code != http.StatusOK || len(got.Items) != 1 || got.Items[0].WorkerID != o.workers["rep"].ID {
		t.Fatalf("manager view: status %d body %s, want only rep", read.Code, read.Body.String())
	}

	if rec := o.as(o.users["mgr"], http.MethodGet, sheet, "", dutyEssBase); rec.Code != http.StatusForbidden {
		t.Fatalf("without MssApprove: status %d, want 403", rec.Code)
	}
	if rec := o.as(o.users["mgr"], http.MethodGet, "/v1/hr/compensation/cycles/"+cycle.ID+"/worksheets/"+o.workers["mgr"].ID, "", dutyMssApprove); rec.Code != http.StatusForbidden {
		t.Fatalf("a manager on the admin worksheet route: status %d, want 403", rec.Code)
	}
}
