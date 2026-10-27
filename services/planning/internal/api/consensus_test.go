package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// statusOf reads a fake model's consensus status; an unset status is a draft.
func statusOf(m store.ForecastModel) string {
	if m.Status == "" {
		return domain.ConsensusDraft
	}
	return m.Status
}

func (f *fakeStore) recordEditor(ctx context.Context, code string) {
	p, _ := tenant.FromContext(ctx)
	if p.UserID == "" {
		return
	}
	if f.editors == nil {
		f.editors = map[string][]string{}
	}
	if !slices.Contains(f.editors[code], p.UserID) {
		f.editors[code] = append(f.editors[code], p.UserID)
	}
}

// fakeConsensusProblem maps domain errors as the Postgres store does.
func fakeConsensusProblem(err error) error {
	switch {
	case errors.Is(err, domain.ErrConsensusSoD):
		return httpx.NewProblem(http.StatusForbidden, store.CodeConsensusSoD, "sod")
	case errors.Is(err, domain.ErrConsensusEmpty):
		return httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeConsensusEmpty, "empty")
	}
	return httpx.NewProblem(http.StatusConflict, store.CodeConsensusState, "state")
}

func (f *fakeStore) lockModel(code string, expected int) (store.ForecastModel, error) {
	m, ok := f.models[code]
	if !ok {
		return m, httpx.NewProblem(http.StatusNotFound, store.CodeForecastNotFound, "forecast model not found")
	}
	if m.Version != expected {
		return m, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	return m, nil
}

// SubmitForecastConsensus mirrors the store; a model with a version has cells.
func (f *fakeStore) SubmitForecastConsensus(ctx context.Context, code string, expected int) (store.ForecastModel, error) {
	m, err := f.lockModel(code, expected)
	if err != nil {
		return store.ForecastModel{}, err
	}
	p, _ := tenant.FromContext(ctx)
	status, err := domain.SubmitConsensus(statusOf(m), p.UserID, m.Version)
	if err != nil {
		return store.ForecastModel{}, fakeConsensusProblem(err)
	}
	user := p.UserID
	m.Status, m.SubmittedBy, m.Version = status, &user, m.Version+1
	f.models[code] = m
	return m, nil
}

func (f *fakeStore) DecideForecastConsensus(ctx context.Context, code string, expected int, approve bool) (store.ForecastModel, *store.ForecastPublication, error) {
	m, err := f.lockModel(code, expected)
	if err != nil {
		return store.ForecastModel{}, nil, err
	}
	p, _ := tenant.FromContext(ctx)
	submitter := ""
	if m.SubmittedBy != nil {
		submitter = *m.SubmittedBy
	}
	status, err := domain.DecideConsensus(statusOf(m), submitter, f.editors[code], p.UserID, approve)
	if err != nil {
		return store.ForecastModel{}, nil, fakeConsensusProblem(err)
	}
	user := p.UserID
	m.Status, m.DecidedBy, m.Version = status, &user, m.Version+1
	f.models[code] = m
	if !approve {
		return m, nil, nil
	}
	return m, &store.ForecastPublication{Items: 2, HorizonPeriods: 3, PeriodFrom: "2026-11-01", PeriodTo: "2027-01-31"}, nil
}

const (
	submitPath    = "/v1/planning/forecasts/BASE:submit"
	publishPath   = "/v1/planning/forecasts/BASE:publish"
	rejectPath    = "/v1/planning/forecasts/BASE:reject"
	dutyApprove   = "planning.forecast.approve"
	forecastDuty2 = dutyForecasts + "," + dutyApprove
)

// doAs sends a request as user with duties and an optional If-Match.
func doAs(t *testing.T, st store.Store, method, path, body, user, duties, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	newRouter(st).Register(router)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	if user != "" {
		req.Header.Set("X-Passage-User", user)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type consensusBody struct {
	Model       store.ForecastModel        `json:"model"`
	Publication *store.ForecastPublication `json:"publication"`
}

func decodeConsensus(t *testing.T, rec *httptest.ResponseRecorder) consensusBody {
	t.Helper()
	var b consensusBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %s: %v", rec.Body.String(), err)
	}
	return b
}

// FR-22.9 end to end over the handlers: alice writes and submits, the cells
// lock, alice cannot publish her own consensus, bob publishes it.
func TestConsensus_SubmitLockPublish(t *testing.T) {
	st := &fakeStore{}
	if rec := doAs(t, st, http.MethodPut, forecastPath, forecastBody, "alice", dutyForecasts, `"0"`); rec.Code != http.StatusOK {
		t.Fatalf("first write: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doAs(t, st, http.MethodPost, submitPath, "", "alice", dutyForecasts, ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("submit without If-Match: %d", rec.Code)
	}
	rec := doAs(t, st, http.MethodPost, submitPath, "", "alice", dutyForecasts, `"1"`)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("submit: %d %s etag %q", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if b := decodeConsensus(t, rec); b.Model.Status != domain.ConsensusSubmitted || b.Model.SubmittedBy == nil || *b.Model.SubmittedBy != "alice" {
		t.Fatalf("submitted model = %+v", b.Model)
	}
	if again := doAs(t, st, http.MethodPost, submitPath, "", "alice", dutyForecasts, `"2"`); again.Code != http.StatusConflict ||
		problemOf(t, again).Code != store.CodeConsensusState {
		t.Fatalf("second submit: %d %s", again.Code, again.Body.String())
	}
	if locked := doAs(t, st, http.MethodPut, forecastPath, forecastBody, "alice", dutyForecasts, `"2"`); locked.Code != http.StatusConflict ||
		problemOf(t, locked).Code != store.CodeForecastLocked {
		t.Fatalf("write on a submitted model: %d %s", locked.Code, locked.Body.String())
	}
	if self := doAs(t, st, http.MethodPost, publishPath, "", "alice", forecastDuty2, `"2"`); self.Code != http.StatusForbidden ||
		problemOf(t, self).Code != store.CodeConsensusSoD {
		t.Fatalf("self publish: %d %s", self.Code, self.Body.String())
	}
	pub := doAs(t, st, http.MethodPost, publishPath, "", "bob", dutyApprove, `"2"`)
	if pub.Code != http.StatusOK || pub.Header().Get("ETag") != `"3"` {
		t.Fatalf("publish: %d %s", pub.Code, pub.Body.String())
	}
	b := decodeConsensus(t, pub)
	if b.Model.Status != domain.ConsensusPublished || b.Publication == nil || b.Publication.HorizonPeriods != 3 || b.Publication.Items != 2 {
		t.Fatalf("published = %+v", b)
	}
	if rej := doAs(t, st, http.MethodPost, rejectPath, "", "bob", dutyApprove, `"3"`); rej.Code != http.StatusConflict {
		t.Fatalf("reject of a published model: %d", rej.Code)
	}
}

// A writer of a cell may not decide even when someone else submitted.
func TestConsensus_EditorCannotDecide(t *testing.T) {
	st := &fakeStore{}
	doAs(t, st, http.MethodPut, forecastPath, forecastBody, "carol", dutyForecasts, `"0"`)
	doAs(t, st, http.MethodPost, submitPath, "", "alice", dutyForecasts, `"1"`)
	if rec := doAs(t, st, http.MethodPost, publishPath, "", "carol", dutyApprove, `"2"`); rec.Code != http.StatusForbidden ||
		problemOf(t, rec).Code != store.CodeConsensusSoD {
		t.Fatalf("editor publish: %d %s", rec.Code, rec.Body.String())
	}
}

// Rejecting returns the model to draft, so its cells can be written again.
func TestConsensus_RejectReopensCells(t *testing.T) {
	st := &fakeStore{}
	doAs(t, st, http.MethodPut, forecastPath, forecastBody, "alice", dutyForecasts, `"0"`)
	doAs(t, st, http.MethodPost, submitPath, "", "alice", dutyForecasts, `"1"`)
	rec := doAs(t, st, http.MethodPost, rejectPath, "", "bob", dutyApprove, `"2"`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	if b := decodeConsensus(t, rec); b.Model.Status != domain.ConsensusDraft || b.Publication != nil {
		t.Fatalf("rejected = %+v", b)
	}
	if w := doAs(t, st, http.MethodPut, forecastPath, forecastBody, "alice", dutyForecasts, `"3"`); w.Code != http.StatusOK {
		t.Fatalf("write after reject: %d %s", w.Code, w.Body.String())
	}
}

func TestConsensus_DutiesAndInput(t *testing.T) {
	st := &fakeStore{}
	doAs(t, st, http.MethodPut, forecastPath, forecastBody, "alice", dutyForecasts, `"0"`)
	tests := []struct {
		name, method, path, user, duties, ifMatch string
		want                                      int
		code                                      string
	}{
		{"publish needs the approve duty", http.MethodPost, publishPath, "bob", dutyForecasts, `"1"`, http.StatusForbidden, ""},
		{"reject needs the approve duty", http.MethodPost, rejectPath, "bob", dutyForecasts, `"1"`, http.StatusForbidden, ""},
		{"submit needs the maintain duty", http.MethodPost, submitPath, "alice", dutyApprove, `"1"`, http.StatusForbidden, ""},
		{"an unknown model is 404", http.MethodPost, "/v1/planning/forecasts/NOPE:submit", "alice", dutyForecasts, `"1"`, http.StatusNotFound, store.CodeForecastNotFound},
		{"a malformed code is 400", http.MethodPost, "/v1/planning/forecasts/x:submit", "alice", dutyForecasts, `"1"`, http.StatusBadRequest, codeValidation},
		{"If-Match 0 names no existing model", http.MethodPost, submitPath, "alice", dutyForecasts, `"0"`, http.StatusBadRequest, codeValidation},
		{"a stale If-Match is 412", http.MethodPost, submitPath, "alice", dutyForecasts, `"7"`, http.StatusPreconditionFailed, store.CodeVersionConflict},
		{"an anonymous submit is refused", http.MethodPost, submitPath, "", dutyForecasts, `"1"`, http.StatusForbidden, store.CodeConsensusSoD},
		{"publishing a draft is a state conflict", http.MethodPost, publishPath, "bob", dutyApprove, `"1"`, http.StatusConflict, store.CodeConsensusState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doAs(t, st, tc.method, tc.path, "", tc.user, tc.duties, tc.ifMatch)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.code != "" && problemOf(t, rec).Code != tc.code {
				t.Fatalf("code %q, want %q", problemOf(t, rec).Code, tc.code)
			}
		})
	}
}
