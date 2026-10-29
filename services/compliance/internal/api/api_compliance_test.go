package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

// fakePrivacy is the subject request half of fakeStore: one request in memory,
// moved by the real domain state machine.
type fakePrivacy struct {
	req       *store.SubjectRequest
	lastKey   string
	lastList  store.RequestFilter
	lastNow   time.Time
	lastInput store.RequestActionInput
}

func (f *fakeStore) Intake(ctx context.Context, in store.IntakeInput, key string, at time.Time) (store.SubjectRequest, error) {
	f.privacy.lastKey = key
	received := in.ReceivedAt
	if received.IsZero() {
		received = at
	}
	plan, err := domain.PlanPrivacyRequest(domain.PrivacyPlanInput{Type: in.Type, Regulation: in.Regulation, ReceivedAt: received, DeadlineDays: in.DeadlineDays, Now: at})
	if err != nil {
		return store.SubjectRequest{}, err
	}
	p, _ := tenant.FromContext(ctx)
	r := store.SubjectRequest{RequestID: ids.New(), Type: in.Type, Regulation: in.Regulation, SubjectRef: in.SubjectRef, ReceivedAt: received,
		DeadlineAt: plan.DeadlineAt, State: domain.PrivacyReceived, HoldIDs: []string{}, CreatedBy: p.UserID, CreatedAt: at, UpdatedAt: at, Version: 1}
	f.privacy.req = &r
	return r, nil
}

func (f *fakeStore) ListRequests(_ context.Context, fl store.RequestFilter) (store.RequestPage, error) {
	f.privacy.lastList = fl
	if f.privacy.req == nil {
		return store.RequestPage{Items: []store.SubjectRequest{}}, nil
	}
	return store.RequestPage{Items: []store.SubjectRequest{*f.privacy.req}}, nil
}

func (f *fakeStore) GetRequest(_ context.Context, id string) (store.SubjectRequest, error) {
	if f.privacy.req == nil || f.privacy.req.RequestID != id {
		return store.SubjectRequest{}, httpx.NewProblem(http.StatusNotFound, store.CodeRequestNotFound, "no subject request with that id")
	}
	return *f.privacy.req, nil
}

func (f *fakeStore) ActOnRequest(ctx context.Context, id string, expected int, in store.RequestActionInput, at time.Time) (store.SubjectRequest, error) {
	f.privacy.lastNow, f.privacy.lastInput = at, in
	r, err := f.GetRequest(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Version != expected {
		return r, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeRequestVersion, "stale")
	}
	holds := r.HoldIDs
	if in.Action == domain.ActionDiscover || in.Action == domain.ActionReleaseHold {
		holds = domain.PrivacyHolds(in.HoldIDs)
	}
	next, err := domain.ApplyPrivacyTransition(domain.PrivacyTransitionInput{Type: r.Type, From: r.State, Action: in.Action, HoldIDs: holds,
		MatchedAttributes: in.MatchedAttributes, EmailLinkVerified: in.EmailLinkVerified, Justification: in.Justification, DeadlineAt: r.DeadlineAt, Now: at})
	if err != nil {
		return r, err
	}
	r.State, r.HoldIDs, r.Late, r.Version = next.State, holds, next.Late, r.Version+1
	f.privacy.req = &r
	return r, nil
}

func storedRequest(typ domain.PrivacyRequestType, state domain.PrivacyState, holds ...string) *store.SubjectRequest {
	return &store.SubjectRequest{RequestID: ids.New(), Type: typ, Regulation: domain.RegulationGDPR, SubjectRef: "party:42", ReceivedAt: now,
		DeadlineAt: now.AddDate(0, 0, 30), State: state, HoldIDs: append([]string{}, holds...), Version: 3}
}

func TestComplianceDutiesAreEnforced(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	id := ids.New()
	for _, tc := range []struct {
		method, path, duty string
	}{
		{http.MethodPost, "/v1/privacy/requests", DutyPrivacyIntake},
		{http.MethodGet, "/v1/privacy/requests", DutyPrivacyOfficer},
		{http.MethodGet, "/v1/privacy/requests/" + id, DutyPrivacyOfficer},
		{http.MethodPost, "/v1/privacy/requests/" + id + "/actions", DutyPrivacyOfficer},
		{http.MethodPost, "/v1/privacy/requests/" + id + "/erasure-preview", DutyPrivacyOfficer},
		{http.MethodPost, "/v1/privacy/retention:purge-check", DutyPrivacyOfficer},
		{http.MethodPost, "/v1/sod/campaigns:plan", DutySoDAdmin},
		{http.MethodPost, "/v1/sod/campaigns:close", DutySoDAdmin},
		{http.MethodPost, "/v1/security/incidents:transition", DutyIncidentManage},
		{http.MethodPost, "/v1/compliance/export-governance:evaluate", DutyComplianceView},
		{http.MethodPost, "/v1/compliance/read-log:classify", DutyComplianceView},
	} {
		// An intake clerk cannot act as the officer, and neighbouring duties are not enough.
		neighbour := DutyPrivacyIntake
		if tc.duty == DutyPrivacyIntake {
			neighbour = DutyPrivacyOfficer
		}
		rec := serve(t, h, call{method: tc.method, path: tc.path, duties: []string{neighbour, DutySoDReview}})
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" {
			t.Errorf("%s %s without %s: %d", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}

func TestIntakeRequest_ValidatesAndStores(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	intake := func(key string, body any) int {
		c := call{method: http.MethodPost, path: "/v1/privacy/requests", duties: []string{DutyPrivacyIntake}, body: body, user: ids.New()}
		if key != "" {
			c.headers = map[string]string{"Idempotency-Key": key}
		}
		return serve(t, h, c).Code
	}
	good := map[string]any{"type": "ACCESS", "regulation": "CCPA", "subjectRef": "contact:7"}
	if code := intake("", good); code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d", code)
	}
	for _, bad := range []map[string]any{
		{"type": "DELETE", "regulation": "GDPR", "subjectRef": "x"},
		{"type": "ACCESS", "regulation": "LGPD", "subjectRef": "x"},
		{"type": "ACCESS", "regulation": "GDPR", "subjectRef": " "},
		{"type": "ACCESS", "regulation": "GDPR", "subjectRef": "x", "receivedAt": now.Add(time.Hour)},
		{"type": "ACCESS", "regulation": "GDPR", "subjectRef": "x", "deadlineDays": -1},
	} {
		if code := intake("k", bad); code != http.StatusBadRequest {
			t.Errorf("%v: %d", bad, code)
		}
	}
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/privacy/requests", duties: []string{DutyPrivacyIntake}, body: good,
		headers: map[string]string{"Idempotency-Key": "k-1"}})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || f.privacy.lastKey != "k-1" {
		t.Fatalf("intake: %d %s", rec.Code, rec.Body)
	}
	var out store.SubjectRequest
	decodeInto(t, rec.Body.Bytes(), &out)
	if out.State != domain.PrivacyReceived || !out.DeadlineAt.Equal(now.AddDate(0, 0, 45)) {
		t.Fatalf("CCPA deadline is 45 days from the server time: %+v", out)
	}
	longer := map[string]any{"type": "ACCESS", "regulation": "GDPR", "subjectRef": "x", "deadlineDays": 31}
	rec = serve(t, h, call{method: http.MethodPost, path: "/v1/privacy/requests", duties: []string{DutyPrivacyIntake}, body: longer,
		headers: map[string]string{"Idempotency-Key": "k-2"}})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodePrivacyDeadline {
		t.Fatalf("override past 30 days: %d %s", rec.Code, rec.Body)
	}
}

func TestListAndGetRequests(t *testing.T) {
	f := &fakeStore{privacy: fakePrivacy{req: storedRequest(domain.RequestAccess, domain.PrivacyVerified)}}
	h := router(newAPI(f, true))
	officerDuty := []string{DutyPrivacyOfficer}
	for _, q := range []string{"?state=LIMBO", "?type=DELETE", "?limit=0", "?limit=201", "?cursor=zzz"} {
		if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/privacy/requests" + q, duties: officerDuty}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	rec := serve(t, h, call{method: http.MethodGet, path: "/v1/privacy/requests?state=VERIFIED&type=ACCESS&limit=5", duties: officerDuty})
	if rec.Code != http.StatusOK || f.privacy.lastList.State != "VERIFIED" || f.privacy.lastList.Type != "ACCESS" || f.privacy.lastList.Limit != 5 {
		t.Fatalf("list: %d %+v", rec.Code, f.privacy.lastList)
	}
	id := f.privacy.req.RequestID
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/privacy/requests/" + id, duties: officerDuty}); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/privacy/requests/" + ids.New(), duties: officerDuty}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeRequestNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/privacy/requests/nope", duties: officerDuty}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestActOnRequest_UsesStoredStateAndServerTime(t *testing.T) {
	f := &fakeStore{privacy: fakePrivacy{req: storedRequest(domain.RequestErasure, domain.PrivacyVerified)}}
	h := router(newAPI(f, true))
	id := f.privacy.req.RequestID
	act := func(etag string, body any) *httpCode {
		c := call{method: http.MethodPost, path: "/v1/privacy/requests/" + id + "/actions", duties: []string{DutyPrivacyOfficer}, body: body}
		if etag != "" {
			c.headers = map[string]string{"If-Match": etag}
		}
		rec := serve(t, h, c)
		return &httpCode{rec.Code, rec.Header().Get("ETag"), rec.Body.String()}
	}
	discover := map[string]any{"action": "DISCOVER", "holdIds": []string{"HOLD-7"}}
	if r := act("", discover); r.code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", r.code)
	}
	if r := act(`"3"`, map[string]any{"action": "PURGE"}); r.code != http.StatusBadRequest {
		t.Fatalf("unknown action: %d", r.code)
	}
	if r := act(`"3"`, map[string]any{"action": "DISCOVER", "holdIds": []string{""}}); r.code != http.StatusBadRequest {
		t.Fatalf("blank hold id: %d", r.code)
	}
	if r := act(`"2"`, discover); r.code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", r.code)
	}
	if r := act(`"3"`, discover); r.code != http.StatusOK || r.etag != `"4"` || f.privacy.req.State != domain.PrivacyHeld || !f.privacy.lastNow.Equal(now) {
		t.Fatalf("erasure under hold: %d %s %+v", r.code, r.body, f.privacy.req)
	}
	if r := act(`"4"`, map[string]any{"action": "APPROVE"}); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("a HELD erasure cannot be approved: %d %s", r.code, r.body)
	}
}

type httpCode struct {
	code int
	etag string
	body string
}

func TestPreviewErasure(t *testing.T) {
	record := map[string]any{"record": map[string]string{"email": "ada@example.com", "amount": "120.00"}}
	preview := func(req *store.SubjectRequest, body any) (int, string, string) {
		f := &fakeStore{privacy: fakePrivacy{req: req}}
		rec := serve(t, router(newAPI(f, true)), call{method: http.MethodPost, path: "/v1/privacy/requests/" + req.RequestID + "/erasure-preview",
			duties: []string{DutyPrivacyOfficer}, body: body})
		code := ""
		if rec.Code != http.StatusOK {
			code = problemCode(t, rec)
		}
		return rec.Code, code, rec.Body.String()
	}
	if status, code, _ := preview(storedRequest(domain.RequestErasure, domain.PrivacyHeld, "HOLD-7"), record); status != http.StatusUnprocessableEntity || code != domain.CodePrivacyLegalHold {
		t.Fatalf("held: %d %s", status, code)
	}
	if status, code, _ := preview(storedRequest(domain.RequestAccess, domain.PrivacyDiscovered), record); status != http.StatusUnprocessableEntity || code != domain.CodePrivacyTransition {
		t.Fatalf("access request: %d %s", status, code)
	}
	if status, code, _ := preview(storedRequest(domain.RequestErasure, domain.PrivacyReceived), record); status != http.StatusUnprocessableEntity || code != domain.CodePrivacyTransition {
		t.Fatalf("before discovery: %d %s", status, code)
	}
	if status, _, _ := preview(storedRequest(domain.RequestErasure, domain.PrivacyDiscovered), map[string]any{"record": map[string]string{}}); status != http.StatusBadRequest {
		t.Fatalf("empty record: %d", status)
	}
	status, _, body := preview(storedRequest(domain.RequestErasure, domain.PrivacyApproved), record)
	var out domain.PseudonymizeResult
	decodeInto(t, []byte(body), &out)
	if status != http.StatusOK || out.Record["amount"] != "120.00" || out.Record["email"] == "ada@example.com" || out.Record["erased"] != "true" {
		t.Fatalf("preview: %d %s", status, body)
	}
}

func TestPurgeCheckEndpoint(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	c := call{method: http.MethodPost, path: "/v1/privacy/retention:purge-check", duties: []string{DutyPrivacyOfficer}}
	c.body = map[string]any{"recordClass": ""}
	if rec := serve(t, h, c); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing class: %d", rec.Code)
	}
	c.body = map[string]any{"recordClass": "AR_INVOICE", "holdIds": []string{"HOLD-7"}}
	rec := serve(t, h, c)
	var d domain.PurgeDecision
	decodeInto(t, rec.Body.Bytes(), &d)
	if rec.Code != http.StatusOK || d.Purgeable || d.Code != domain.CodePrivacyPurge {
		t.Fatalf("held record must not purge: %d %+v", rec.Code, d)
	}
}

func TestCampaignEndpoints(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	admin := []string{DutySoDAdmin}
	start := now.AddDate(0, 0, -20)
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/sod/campaigns:plan", duties: admin, body: CampaignPlanRequest{StartsAt: start}})
	var plan CampaignPlanResponse
	decodeInto(t, rec.Body.Bytes(), &plan)
	if rec.Code != http.StatusOK || !plan.ClosesAt.Equal(start.AddDate(0, 0, 14)) {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/sod/campaigns:plan", duties: admin, body: CampaignPlanRequest{StartsAt: start, WindowDays: 90}}); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeCampaignWindow {
		t.Fatalf("90-day window: %d", rec.Code)
	}
	closeAt := func(body any) *httpCode {
		rec := serve(t, h, call{method: http.MethodPost, path: "/v1/sod/campaigns:close", duties: admin, body: body})
		return &httpCode{code: rec.Code, body: rec.Body.String()}
	}
	if r := closeAt(map[string]any{"entries": []any{}, "closesAt": plan.ClosesAt}); r.code != http.StatusBadRequest {
		t.Fatalf("no entries: %d", r.code)
	}
	if r := closeAt(map[string]any{"entries": []map[string]string{{"managerId": "m", "userId": "u", "roleName": "AP"}}}); r.code != http.StatusBadRequest {
		t.Fatalf("no closesAt: %d", r.code)
	}
	if r := closeAt(map[string]any{"entries": []map[string]string{{"managerId": "m", "userId": "m", "roleName": "AP"}}, "closesAt": plan.ClosesAt}); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("self-certification: %d", r.code)
	}
	// The caller cannot supply the clock.
	if r := closeAt(map[string]any{"entries": []map[string]string{{"managerId": "m", "userId": "u", "roleName": "AP"}}, "closesAt": plan.ClosesAt, "now": start}); r.code != http.StatusBadRequest {
		t.Fatalf("now in the body: %d", r.code)
	}
	// The window ended six days ago at the server time.
	r := closeAt(map[string]any{"entries": []map[string]string{{"managerId": "m", "userId": "u", "roleName": "AP"}}, "autoRevoke": true, "closesAt": plan.ClosesAt})
	var res CampaignCloseResponse
	decodeInto(t, []byte(r.body), &res)
	if r.code != http.StatusOK || !res.Closed || len(res.Revoke) != 1 || !res.AsOf.Equal(now) || res.Progress.OverdueByManager["m"] != 1 {
		t.Fatalf("close: %d %s", r.code, r.body)
	}
}

func TestIncidentTransitionEndpoint(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	mgr := []string{DutyIncidentManage}
	step := func(body any) *httpCode {
		rec := serve(t, h, call{method: http.MethodPost, path: "/v1/security/incidents:transition", duties: mgr, body: body})
		return &httpCode{code: rec.Code, body: rec.Body.String()}
	}
	r := step(map[string]any{"severity": "SEV1", "source": "ALERT", "to": "TRIAGE"})
	var out IncidentTransitionResponse
	decodeInto(t, []byte(r.body), &out)
	if r.code != http.StatusOK || !out.PageOnCall || out.Status != domain.IncidentTriage {
		t.Fatalf("open Sev-1: %d %s", r.code, r.body)
	}
	if r := step(map[string]any{"severity": "SEV1", "source": "ALERT", "to": "RECOVERED"}); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("skipping steps: %d", r.code)
	}
	aware := now.Add(-5 * 24 * time.Hour)
	breach := map[string]any{"severity": "SEV2", "source": "ALERT", "from": "RECOVERED", "to": "CLOSED", "personalDataBreach": true,
		"awareAt": aware, "regimes": []string{"GDPR"}}
	if r := step(breach); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("unsent GDPR notice past 72 h: %d %s", r.code, r.body)
	}
	breach["notifiedAt"] = map[string]any{"GDPR": now.Add(time.Hour)}
	if r := step(breach); r.code != http.StatusBadRequest {
		t.Fatalf("future notification: %d", r.code)
	}
	breach["notifiedAt"] = map[string]any{"GDPR": aware.Add(24 * time.Hour)}
	if r := step(breach); r.code != http.StatusOK {
		t.Fatalf("notified in time closes freely: %d %s", r.code, r.body)
	}
	breach["awareAt"] = now.Add(time.Hour)
	if r := step(breach); r.code != http.StatusBadRequest {
		t.Fatalf("future awareAt: %d", r.code)
	}
}

func TestExportGovernanceAndReadLogEndpoints(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	view := []string{DutyComplianceView}
	pii := map[string]any{"classification": "PII", "channel": "GRID", "rows": 3}
	rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/export-governance:evaluate", duties: view, body: pii})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeExportReason {
		t.Fatalf("PII without reason: %d %s", rec.Code, rec.Body)
	}
	pii["reason"] = "subject access check for ticket 4412"
	rec = serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/export-governance:evaluate", duties: view, body: pii})
	var dec domain.ExportDecision
	decodeInto(t, rec.Body.Bytes(), &dec)
	if rec.Code != http.StatusOK || !dec.Watermark || !dec.Allowed || !dec.Audit {
		t.Fatalf("PII with reason: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/read-log:classify", duties: view, body: ReadClassifyRequest{}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no fields: %d", rec.Code)
	}
	rec = serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/read-log:classify", duties: view, body: ReadClassifyRequest{Fields: []string{"hr.worker.salary", "hr.worker.name"}}})
	var rc domain.ReadClassification
	decodeInto(t, rec.Body.Bytes(), &rc)
	if rec.Code != http.StatusOK || !rc.Logged || len(rc.Fields) != 1 || rc.RetentionDays != 730 {
		t.Fatalf("read-log: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, h, call{method: http.MethodPost, path: "/v1/compliance/read-log:classify", duties: view, body: ReadClassifyRequest{Fields: []string{"a"}, RetentionDays: 30}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("retention under 90 days: %d", rec.Code)
	}
}
