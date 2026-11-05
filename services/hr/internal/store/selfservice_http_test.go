package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/api"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
	"github.com/BenjaminSRussell/passage/services/hr/migrations"
)

const (
	dutyEssBase      = "hr.profile.self"
	dutyMssBase      = "hr.team.manage"
	dutyEssAbsence   = "hr.absence.self"
	dutyMssApprove   = "hr.team.approve"
	dutyAbsenceAdmin = "hr.absence.admin"
)

func tid() string { return testkit.TenantID(1) }

type listOf[T any] struct {
	Items []T `json:"items"`
}

type profileBody struct {
	ID  string `json:"id"`
	PII struct {
		NationalID string `json:"nationalId"`
		Masked     bool   `json:"masked"`
	} `json:"pii"`
	AsOf struct {
		ManagerWorkerID *string `json:"managerWorkerId"`
	} `json:"asOf"`
}

type org struct {
	st                        *store.Postgres
	ctx                       context.Context
	users                     map[string]string
	workers                   map[string]store.Worker
	requests                  map[string]store.AbsenceRequest
	plan                      store.AbsencePlan
	router                    http.Handler
	ceoUser, mgrUser, repUser string
}

func newOrg(t *testing.T) *org {
	t.Helper()
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tid(), UserID: "seed", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := domain.NewSealer(keys, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(pool, sealer, 10)
	o := &org{st: st, ctx: ctx, users: map[string]string{}, workers: map[string]store.Worker{}, requests: map[string]store.AbsenceRequest{}}
	o.seed(t)
	r := chi.NewRouter()
	api.New(st).Register(r)
	o.router = r
	return o
}

func (o *org) seed(t *testing.T) {
	t.Helper()
	le, dept := ids.New(), ids.New()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seats := map[string]store.Position{}
	for _, name := range []string{"ceo", "mgr", "rep", "peer"} {
		user := ids.New()
		o.users[name] = user
		w, _, err := o.st.CreateWorker(o.ctx, store.WorkerInput{
			Worker:     store.Worker{PersonnelNumber: "W-" + strings.ToUpper(name), WorkerType: "EMPLOYEE", PartyPersonID: ids.New(), LegalName: name, UserID: &user},
			NationalID: "123-45-6789",
		}, "")
		if err != nil {
			t.Fatal(err)
		}
		o.workers[name] = w
		var parent *string
		switch name {
		case "mgr", "peer":
			parent = &[]string{seats["ceo"].ID}[0]
		case "rep":
			parent = &[]string{seats["mgr"].ID}[0]
		}
		pos, _, err := o.st.CreatePosition(o.ctx, store.Position{PositionNumber: "P-" + name, JobCode: "J", DepartmentID: dept, LegalEntityID: le, FTE: "1.00", ValidFrom: from, ReportsToID: parent}, "")
		if err != nil {
			t.Fatal(err)
		}
		seats[name] = pos
		emp, _, err := o.st.CreateEmployment(o.ctx, w.ID, store.Employment{LegalEntityID: le, EmploymentType: "FULL_TIME", AssignmentKind: "PRIMARY", HireDate: "2026-01-01", ValidFrom: from}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := o.st.CreateAssignment(o.ctx, pos.ID, store.PositionAssignment{WorkerID: w.ID, EmploymentID: emp.ID, FTEShare: "1.00", IsPrimary: true, ValidFrom: from}, ""); err != nil {
			t.Fatal(err)
		}
	}
	plan, _, err := o.st.CreateAbsencePlan(o.ctx, store.AbsencePlan{Code: "PTO", Unit: "DAYS", AccrualFrequency: "MONTHLY", AccrualRate: "1.00", AccrualDay: "LAST_DAY",
		Proration: "DAYS_IN_PERIOD", MaxBalance: "22.50", CarryOverMax: "5.00", CarryOverExpiryMonths: 3, PlanYearStart: "01-01", NegativeBalanceAllowed: true}, "")
	if err != nil {
		t.Fatal(err)
	}
	o.plan = plan
	for i, name := range []string{"mgr", "rep", "peer", "ceo"} {
		d := time.Date(2027, 2, 1+i, 0, 0, 0, 0, time.UTC)
		req, _, _, err := o.st.CreateAbsenceRequest(o.ctx, store.RequestInput{WorkerID: o.workers[name].ID, PlanID: plan.ID, From: d, To: d, Quantity: decimal.NewFromInt(1)}, "", false)
		if err != nil {
			t.Fatalf("request for %s: %v", name, err)
		}
		o.requests[name] = req
	}
}

func (o *org) as(user, method, target, body string, duties ...string) *httptest.ResponseRecorder {
	p := tenant.Principal{TenantID: tid(), UserID: user, Duties: map[string]struct{}{tenant.DutyAllScope: {}}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(tenant.With(context.Background(), p))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	o.router.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestMyProfileIsAlwaysTheCallersOwnMaskedRecord(t *testing.T) {
	o := newOrg(t)
	rec := o.as(o.users["rep"], http.MethodGet, "/v1/hr/me", "", dutyEssBase)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	got := decodeBody[profileBody](t, rec)
	if got.ID != o.workers["rep"].ID || !got.PII.Masked || got.PII.NationalID != "***-**-6789" {
		t.Fatalf("profile = %+v, want rep's record with masked PII", got)
	}
	if got.AsOf.ManagerWorkerID == nil || *got.AsOf.ManagerWorkerID != o.workers["mgr"].ID {
		t.Fatalf("asOf manager = %v, want mgr", got.AsOf.ManagerWorkerID)
	}

	other := o.as(o.users["rep"], http.MethodGet, "/v1/hr/me?workerId="+o.workers["peer"].ID+"&id="+o.workers["peer"].ID, "", dutyEssBase)
	if decodeBody[profileBody](t, other).ID != o.workers["rep"].ID {
		t.Fatal("a worker id in the query must not change whose record is read")
	}

	unlinked := o.as(ids.New(), http.MethodGet, "/v1/hr/me", "", dutyEssBase)
	if unlinked.Code != http.StatusNotFound || !strings.Contains(unlinked.Body.String(), store.CodeNoLinkedWorker) {
		t.Fatalf("unlinked sign-in: status %d body %s, want 404 %s", unlinked.Code, unlinked.Body.String(), store.CodeNoLinkedWorker)
	}
	if rec := o.as("", http.MethodGet, "/v1/hr/me", "", dutyEssBase); rec.Code != http.StatusNotFound {
		t.Fatalf("no user id: status = %d, want 404", rec.Code)
	}
}

func TestManagerSeesOnlyDirectReports(t *testing.T) {
	o := newOrg(t)
	list := o.as(o.users["mgr"], http.MethodGet, "/v1/hr/me/team", "", dutyMssBase)
	team := decodeBody[listOf[store.Worker]](t, list)
	if list.Code != http.StatusOK || len(team.Items) != 1 || team.Items[0].ID != o.workers["rep"].ID {
		t.Fatalf("team = %d %s, want only rep", list.Code, list.Body.String())
	}

	own := o.as(o.users["mgr"], http.MethodGet, "/v1/hr/me/team/"+o.workers["rep"].ID, "", dutyMssBase)
	if own.Code != http.StatusOK || strings.Contains(own.Body.String(), "nationalId") {
		t.Fatalf("direct report: status %d body %s, want 200 without PII", own.Code, own.Body.String())
	}
	for name, id := range map[string]string{
		"peer":       o.workers["peer"].ID,
		"own boss":   o.workers["ceo"].ID,
		"self":       o.workers["mgr"].ID,
		"unknown":    ids.New(),
		"not a uuid": "rep",
	} {
		rec := o.as(o.users["mgr"], http.MethodGet, "/v1/hr/me/team/"+id, "", dutyMssBase)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeWorkerNotFound) {
			t.Errorf("%s: status %d body %s, want the uniform 404", name, rec.Code, rec.Body.String())
		}
	}

	if rec := o.as(o.users["rep"], http.MethodGet, "/v1/hr/me/team/"+o.workers["mgr"].ID, "", dutyMssBase); rec.Code != http.StatusNotFound {
		t.Fatalf("a report reading their manager's record: status = %d, want 404", rec.Code)
	}
}

func TestManagerCannotActOnAbsenceOutsideTheirTeam(t *testing.T) {
	o := newOrg(t)
	duties := []string{dutyEssAbsence, dutyMssApprove}
	decide := func(user, name, decision string) *httptest.ResponseRecorder {
		return o.as(user, http.MethodPost, "/v1/hr/absence/requests/"+o.requests[name].ID+"/decide", `{"decision":"`+decision+`"}`, duties...)
	}

	for _, name := range []string{"peer", "ceo"} {
		rec := decide(o.users["mgr"], name, "REJECT")
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeAbsenceRequestNotFound) {
			t.Errorf("deciding %s's request: status %d body %s, want 404", name, rec.Code, rec.Body.String())
		}
	}
	st, err := o.st.ListAbsenceRequests(o.ctx, store.RequestFilter{Status: domain.RequestSubmitted}, "", 10)
	if err != nil || len(st.Items) != 4 {
		t.Fatalf("after refused decisions %d requests are still submitted (err=%v), want 4", len(st.Items), err)
	}

	if rec := decide(o.users["mgr"], "rep", "REJECT"); rec.Code != http.StatusOK {
		t.Fatalf("deciding a direct report's request: status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := decide(o.users["mgr"], "mgr", "APPROVE"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "HCM-ABS-SELF-APPROVAL") {
		t.Fatalf("deciding their own request: status = %d, want the self-decision refusal", rec.Code)
	}
	if rec := decide(o.users["rep"], "mgr", "REJECT"); rec.Code != http.StatusNotFound {
		t.Fatalf("a report deciding their manager's request: status = %d, want 404", rec.Code)
	}

	balances := func(user, name string) int {
		return o.as(user, http.MethodGet, "/v1/hr/workers/"+o.workers[name].ID+"/absence/balances", "", duties...).Code
	}
	if got := balances(o.users["mgr"], "rep"); got != http.StatusOK {
		t.Errorf("direct report's balances: status = %d, want 200", got)
	}
	if got := balances(o.users["mgr"], "mgr"); got != http.StatusOK {
		t.Errorf("own balances: status = %d, want 200", got)
	}
	for _, name := range []string{"peer", "ceo"} {
		if got := balances(o.users["mgr"], name); got != http.StatusNotFound {
			t.Errorf("%s's balances: status = %d, want 404", name, got)
		}
	}

	list := o.as(o.users["mgr"], http.MethodGet, "/v1/hr/absence/requests?status=REJECTED", "", duties...)
	if rejected := decodeBody[listOf[store.AbsenceRequest]](t, list); list.Code != http.StatusOK || len(rejected.Items) != 1 || rejected.Items[0].WorkerID != o.workers["rep"].ID {
		t.Fatalf("rejected list = %d %s", list.Code, list.Body.String())
	}
	all := decodeBody[listOf[store.AbsenceRequest]](t, o.as(o.users["mgr"], http.MethodGet, "/v1/hr/absence/requests", "", duties...))
	if len(all.Items) != 2 {
		t.Fatalf("manager's queue has %d requests, want their own and rep's", len(all.Items))
	}
	probe := decodeBody[listOf[store.AbsenceRequest]](t, o.as(o.users["mgr"], http.MethodGet, "/v1/hr/absence/requests?workerId="+o.workers["peer"].ID, "", duties...))
	if len(probe.Items) != 0 {
		t.Fatalf("filtering the queue to a peer returned %d requests, want 0", len(probe.Items))
	}
}

func TestAbsenceAdministratorStillSeesEveryRequest(t *testing.T) {
	o := newOrg(t)
	rec := o.as(o.users["peer"], http.MethodGet, "/v1/hr/absence/requests", "", dutyEssAbsence, dutyMssApprove, dutyAbsenceAdmin)
	if all := decodeBody[listOf[store.AbsenceRequest]](t, rec); len(all.Items) != 4 {
		t.Fatalf("admin queue has %d requests, want 4", len(all.Items))
	}
	rec = o.as(o.users["peer"], http.MethodPost, "/v1/hr/absence/requests/"+o.requests["rep"].ID+"/decide", `{"decision":"REJECT"}`, dutyEssAbsence, dutyMssApprove, dutyAbsenceAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin decision: status %d body %s", rec.Code, rec.Body.String())
	}
}
