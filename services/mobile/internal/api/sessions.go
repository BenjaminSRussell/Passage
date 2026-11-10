package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/flows"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/session"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
)

const (
	codeResumed     = "MOB-0117"
	defaultMenuKey  = "default"
	signOutReason   = "SIGN_OUT"
	forcedReason    = "FORCE_SIGNOUT"
	maxDeviceIDLen  = 80
	maxReasonLength = 200
)

// runDoc is a session's saved state: the values seeded from the assigned work
// and the flow run in progress.
type runDoc struct {
	Seed map[string]string `json:"seed,omitempty"`
	Run  *flows.State      `json:"run,omitempty"`
	Last *lastCommand      `json:"last,omitempty"`
}

// lastCommand is the answer to the last keyed command, saved in the same
// transaction as its effect, so a retry after the key's own record was lost
// gets that answer instead of a wrong-step conflict.
type lastCommand struct {
	Key     string          `json:"key"`
	Command string          `json:"command"`
	Work    json.RawMessage `json:"work,omitempty"`
	Run     *runResponse    `json:"run,omitempty"`
}

func (d runDoc) answered(key, command string) *lastCommand {
	if d.Last != nil && d.Last.Key == key && d.Last.Command == command {
		return d.Last
	}
	return nil
}

func docOf(s store.Session) runDoc {
	var d runDoc
	_ = json.Unmarshal(s.State, &d)
	return d
}

func (d runDoc) raw() json.RawMessage {
	b, _ := json.Marshal(d)
	return b
}

type startSessionRequest struct {
	DeviceID    string `json:"deviceId"`
	WarehouseID string `json:"warehouseId"`
	MenuKey     string `json:"menuKey"`
}

type notice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type sessionResponse struct {
	Session  store.Session `json:"session"`
	Resumed  bool          `json:"resumed"`
	Relocked bool          `json:"relocked"`
	Reason   string        `json:"reason,omitempty"`
	Notice   *notice       `json:"notice,omitempty"`
}

func (a *API) startSession(w http.ResponseWriter, r *http.Request) {
	var req startSessionRequest
	fields := map[string]string{}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.DeviceID == "" || len(req.DeviceID) > maxDeviceIDLen || strings.Contains(req.DeviceID, ":") {
		fields["deviceId"] = "is required, at most 80 characters, without ':'"
	}
	uuidField(fields, "warehouseId", req.WarehouseID)
	if req.MenuKey == "" {
		req.MenuKey = defaultMenuKey
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	now := a.now()
	s, resumed, err := a.st.StartSession(r.Context(), store.StartInput{DeviceID: req.DeviceID, WarehouseID: req.WarehouseID, MenuKey: req.MenuKey, Now: now, Idle: a.idle})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := sessionResponse{Session: s, Resumed: resumed}
	if resumed && s.Lapsed(now, a.idle) {
		if out.Session, out.Relocked, out.Reason, err = a.resumeStale(r.Context(), s, now); err != nil {
			fail(w, r, err)
			return
		}
	}
	s = out.Session
	status := http.StatusCreated
	if resumed {
		status = http.StatusOK
		if run := docOf(s).Run; run != nil && !run.Done {
			out.Notice = &notice{Code: codeResumed, Message: fmt.Sprintf("Session resumed on flow version %d.", run.Version)}
		}
	} else {
		a.stats.sessions.Add(1)
	}
	httpx.JSON(w, status, out)
}

type heartbeatRequest struct {
	DeviceTime *time.Time `json:"deviceTime"`
}

type heartbeatResponse struct {
	Session  store.Session `json:"session"`
	Resumed  bool          `json:"resumed"`
	Relocked bool          `json:"relocked"`
	Reason   string        `json:"reason,omitempty"`
}

// heartbeat records liveness and the device clock. A session idle past the
// timeout, swept or not, that comes back keeps its work only if the warehouse still has it OPEN or ASSIGNED and
// no other ACTIVE session holds it; otherwise the session lets it go (BR-32.6).
func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := httpx.Decode(r, &req); err != nil && r.ContentLength != 0 {
		httpx.Fail(w, r, err)
		return
	}
	ctx, id, now := r.Context(), chi.URLParam(r, "id"), a.now()
	s, wasStale, err := a.st.Heartbeat(ctx, id, now, req.DeviceTime, a.idle)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := heartbeatResponse{Session: s, Resumed: wasStale}
	if wasStale && s.CurrentWorkID != "" {
		if out.Session, out.Relocked, out.Reason, err = a.relockOrRelease(ctx, s, now); err != nil {
			fail(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// resumeStale brings a lapsed session of the caller back on a new login: the
// heartbeat that the device would have sent, then the same work re-lock.
func (a *API) resumeStale(ctx context.Context, s store.Session, now time.Time) (store.Session, bool, string, error) {
	beaten, _, err := a.st.Heartbeat(ctx, s.SessionID, now, nil, a.idle)
	if err != nil || beaten.CurrentWorkID == "" {
		return beaten, false, "", err
	}
	return a.relockOrRelease(ctx, beaten, now)
}

type relockOutcome int

const (
	workRelocked relockOutcome = iota
	workUnconfirmed
	workReleased
)

// relockOrRelease lets go of the session's work, and the run seeded from it,
// only when the warehouse confirms the work is gone or held by someone else;
// when it cannot confirm, the session keeps both.
func (a *API) relockOrRelease(ctx context.Context, s store.Session, now time.Time) (store.Session, bool, string, error) {
	outcome, reason := a.relock(ctx, s, now)
	if outcome != workReleased {
		return s, outcome == workRelocked, reason, nil
	}
	doc := docOf(s)
	doc.Seed, doc.Run = nil, nil
	saved, err := a.st.SaveRun(ctx, s.SessionID, s.Version, doc.raw(), "", now, a.idle)
	return saved, false, reason, err
}

func (a *API) relock(ctx context.Context, s store.Session, now time.Time) (relockOutcome, string) {
	const unconfirmed = "the warehouse could not confirm the work"
	out, err := a.read(ctx, s.CurrentWorkID)
	switch {
	case err != nil:
		return workUnconfirmed, unconfirmed
	case out.Status == http.StatusNotFound || out.Status == http.StatusGone:
		return workReleased, "the work no longer exists"
	case !out.OK():
		return workUnconfirmed, unconfirmed
	}
	work, ok := warehouse.WorkFrom(out.Body)
	if !ok {
		return workUnconfirmed, unconfirmed
	}
	holders, err := a.st.SessionsForWork(ctx, s.CurrentWorkID)
	if err != nil {
		return workUnconfirmed, "the work lock could not be checked"
	}
	views := make([]session.View, len(holders))
	for i, h := range holders {
		views[i] = h.View()
	}
	if relocked, reason := session.Resumed(s.View(), s.CurrentWorkID, session.WorkStatus(work.Status), views, now, a.idle); !relocked {
		return workReleased, reason
	}
	return workRelocked, "re-locked"
}

type endRequest struct {
	Reason string `json:"reason"`
}

func (a *API) endSession(w http.ResponseWriter, r *http.Request) {
	var req endRequest
	if err := httpx.Decode(r, &req); err != nil && r.ContentLength != 0 {
		httpx.Fail(w, r, err)
		return
	}
	s, err := a.st.EndSession(r.Context(), chi.URLParam(r, "id"), signOutReason, false, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (a *API) forceSignOut(w http.ResponseWriter, r *http.Request) {
	var req endRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" || len(req.Reason) > maxReasonLength {
		validationFailed(w, r, map[string]string{"reason": "is required, at most 200 characters"})
		return
	}
	s, err := a.st.EndSession(r.Context(), chi.URLParam(r, "id"), forcedReason, true, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if workID := q.Get("workId"); workID != "" {
		items, err := a.st.SessionsForWork(r.Context(), workID)
		if err != nil {
			fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": nonNil(items)})
		return
	}
	items, err := a.st.ListSessions(r.Context(), strings.ToUpper(q.Get("status")), q.Get("warehouseId"), limitOf(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": nonNil(items)})
}

func (a *API) sweep(w http.ResponseWriter, r *http.Request) {
	n, err := a.st.SweepStale(r.Context(), a.now(), a.idle)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"marked": n})
}

type assignResponse struct {
	Work    json.RawMessage `json:"work"`
	Session store.Session   `json:"session"`
}

// assignWork asks the warehouse for the user's next work in the session's
// warehouse and records it on the session, so its lock follows the heartbeat.
// A repeat of the same Idempotency-Key returns the first answer.
func (a *API) assignWork(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	idem, err := command(r, nil, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	ctx, id := r.Context(), chi.URLParam(r, "id")
	out, replayed, err := store.Idempotent(ctx, a.st, idem, http.StatusOK, func() (assignResponse, error) { return a.applyAssign(ctx, id, idem.Key) })
	if err != nil {
		fail(w, r, err)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}

func (a *API) applyAssign(ctx context.Context, id, key string) (assignResponse, error) {
	s, err := a.ownSession(ctx, id)
	if err != nil {
		return assignResponse{}, err
	}
	if last := docOf(s).answered(key, "assign"); last != nil {
		return assignResponse{Work: last.Work, Session: s}, nil
	}
	out, err := a.wh.Do(ctx, "assign", key, map[string]string{"warehouseId": s.WarehouseID})
	if err != nil {
		return assignResponse{}, transportError{err}
	}
	if !out.OK() {
		return assignResponse{}, httpx.NewProblem(out.Status, out.Code, out.Detail)
	}
	work, found := warehouse.WorkFrom(out.Body)
	if !found {
		return assignResponse{Work: json.RawMessage("null"), Session: s}, nil
	}
	var wrapped struct {
		Work json.RawMessage `json:"work"`
	}
	raw := out.Body
	if json.Unmarshal(out.Body, &wrapped) == nil && len(wrapped.Work) > 0 {
		raw = wrapped.Work
	}
	doc := docOf(s)
	doc.Seed, doc.Run = work.Values(), nil
	doc.Last = &lastCommand{Key: key, Command: "assign", Work: raw}
	saved, err := a.st.SaveRun(ctx, id, s.Version, doc.raw(), work.ID, a.now(), a.idle)
	if err != nil {
		return assignResponse{}, err
	}
	return assignResponse{Work: raw, Session: saved}, nil
}

func (a *API) ownSession(ctx context.Context, id string) (store.Session, error) {
	s, err := a.st.GetSession(ctx, id)
	if err != nil {
		return store.Session{}, err
	}
	if s.UserID != userOf(ctx) {
		return store.Session{}, store.ErrNotOwner
	}
	if s.Status == string(session.Ended) {
		return store.Session{}, store.ErrSessionEnded
	}
	return s, nil
}

type stepView struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Prompt       string          `json:"prompt"`
	ExpectedScan []gs1.ScanClass `json:"expectedScan,omitempty"`
	Outcomes     []string        `json:"outcomes,omitempty"`
}

type runResponse struct {
	Run  flows.State `json:"run"`
	Step *stepView   `json:"step,omitempty"`
}

func viewOf(d flows.Draft, st flows.State) runResponse {
	out := runResponse{Run: st}
	if st.Done {
		return out
	}
	if s, ok := d.Step(st.StepID); ok {
		v := &stepView{ID: s.ID, Type: s.Type, Prompt: s.Prompt, ExpectedScan: s.ExpectedScan}
		if s.Type == flows.StepBranch {
			for k := range s.Next {
				v.Outcomes = append(v.Outcomes, k)
			}
		}
		out.Step = v
	}
	return out
}

type startRunRequest struct {
	FlowCode string `json:"flowCode"`
}

// startRun pins the run to the flow's published version at this moment, so a
// later publish never changes it (BR-32.9).
func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	var req startRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ctx, id := r.Context(), chi.URLParam(r, "id")
	s, err := a.ownSession(ctx, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	published, err := a.st.PublishedFor(ctx, s.WarehouseID, []string{req.FlowCode})
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(published) == 0 {
		fail(w, r, store.ErrNotFound)
		return
	}
	f := published[0]
	doc := docOf(s)
	state := flows.Start(f.Code, f.Version, f.Draft(), doc.Seed)
	doc.Run = &state
	if _, err := a.st.SaveRun(ctx, id, s.Version, doc.raw(), s.CurrentWorkID, a.now(), a.idle); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, viewOf(f.Draft(), state))
}

type submitRequest struct {
	Value   string `json:"value"`
	Outcome string `json:"outcome"`
}

// submitStep applies one scan, value or outcome to the run's current step. A
// scan that fails its check answers 422 with the catalog code and leaves the
// run where it was. A call step makes its warehouse call as the user, keyed by
// the request's Idempotency-Key, and moves on only when the call succeeds. A
// repeat of the same Idempotency-Key returns the first answer.
func (a *API) submitStep(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	ctx, id, step := r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "step")
	out, replayed, err := store.Idempotent(ctx, a.st, idem, http.StatusOK, func() (runResponse, error) { return a.applyStep(ctx, id, step, idem.Key, req) })
	if err != nil {
		fail(w, r, err)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}

func (a *API) applyStep(ctx context.Context, id, step, key string, req submitRequest) (runResponse, error) {
	s, err := a.ownSession(ctx, id)
	if err != nil {
		return runResponse{}, err
	}
	doc := docOf(s)
	if last := doc.answered(key, step); last != nil && last.Run != nil {
		return *last.Run, nil
	}
	if doc.Run == nil {
		return runResponse{}, httpx.NewProblem(http.StatusConflict, codeStep, "the session has no flow run in progress")
	}
	pinned, err := a.st.FlowVersion(ctx, doc.Run.FlowCode, doc.Run.Version)
	if err != nil {
		return runResponse{}, err
	}
	draft := pinned.Draft()
	res, err := flows.Submit(draft, *doc.Run, step, flows.Input{Value: req.Value, Outcome: req.Outcome}, gs1.ClassifyOptions{Options: gs1.Options{Now: a.now()}})
	if err != nil {
		return runResponse{}, stepProblem(err)
	}
	a.stats.steps.Add(1)
	if res.Verdict != nil {
		a.stats.scanErrors.Add(1)
		return runResponse{}, httpx.NewProblem(http.StatusUnprocessableEntity, res.Verdict.Code, res.Verdict.Message)
	}
	next, workID := res.State, s.CurrentWorkID
	if res.Call != nil {
		out, err := a.wh.Do(ctx, res.Call.Op, key, res.Call.Params)
		if err != nil {
			return runResponse{}, transportError{err}
		}
		if !out.OK() {
			return runResponse{}, httpx.NewProblem(out.Status, out.Code, out.Detail)
		}
		produced := map[string]string{}
		if work, found := warehouse.WorkFrom(out.Body); found {
			produced, workID = work.Values(), work.ID
		}
		if next, err = flows.Resolve(draft, res.State, produced); err != nil {
			return runResponse{}, stepProblem(err)
		}
	}
	result := viewOf(draft, next)
	doc.Run, doc.Last = &next, &lastCommand{Key: key, Command: step, Run: &result}
	if _, err := a.st.SaveRun(ctx, id, s.Version, doc.raw(), workID, a.now(), a.idle); err != nil {
		return runResponse{}, err
	}
	return result, nil
}

func stepProblem(err error) error {
	switch {
	case errors.Is(err, flows.ErrFlowFinished):
		return httpx.NewProblem(http.StatusConflict, codeStep, err.Error())
	case errors.Is(err, flows.ErrWrongStep):
		return httpx.NewProblem(http.StatusConflict, codeStep, err.Error())
	case errors.Is(err, flows.ErrNoOutcome), errors.Is(err, flows.ErrValueNeeded), errors.Is(err, flows.ErrValueTooLong):
		return httpx.NewProblem(http.StatusBadRequest, codeValidation, err.Error())
	}
	return err
}
