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
	Session store.Session `json:"session"`
	Resumed bool          `json:"resumed"`
	Notice  *notice       `json:"notice,omitempty"`
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
	s, resumed, err := a.st.StartSession(r.Context(), store.StartInput{DeviceID: req.DeviceID, WarehouseID: req.WarehouseID, MenuKey: req.MenuKey, Now: a.now(), Idle: a.idle})
	if err != nil {
		fail(w, r, err)
		return
	}
	out := sessionResponse{Session: s, Resumed: resumed}
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

// heartbeat records liveness and the device clock. A STALE session that comes
// back keeps its work only if the warehouse still has it OPEN or ASSIGNED and
// no other ACTIVE session holds it; otherwise the session lets it go (BR-32.6).
func (a *API) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req heartbeatRequest
	if err := httpx.Decode(r, &req); err != nil && r.ContentLength != 0 {
		httpx.Fail(w, r, err)
		return
	}
	ctx, id, now := r.Context(), chi.URLParam(r, "id"), a.now()
	s, wasStale, err := a.st.Heartbeat(ctx, id, now, req.DeviceTime)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := heartbeatResponse{Session: s, Resumed: wasStale}
	if wasStale && s.CurrentWorkID != "" {
		relocked, reason := a.relock(ctx, s, now)
		out.Relocked, out.Reason = relocked, reason
		if !relocked {
			if out.Session, err = a.st.SaveRun(ctx, id, s.Version, s.State, "", now); err != nil {
				fail(w, r, err)
				return
			}
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) relock(ctx context.Context, s store.Session, now time.Time) (bool, string) {
	out, err := a.wh.Do(ctx, "get-work", "", map[string]string{"workId": s.CurrentWorkID})
	if err != nil {
		return false, "the warehouse could not confirm the work"
	}
	if !out.OK() {
		return false, "the work no longer exists"
	}
	work, ok := warehouse.WorkFrom(out.Body)
	if !ok {
		return false, "the work no longer exists"
	}
	holders, err := a.st.SessionsForWork(ctx, s.CurrentWorkID)
	if err != nil {
		return false, "the work lock could not be checked"
	}
	views := make([]session.View, len(holders))
	for i, h := range holders {
		views[i] = h.View()
	}
	return session.Resumed(s.View(), s.CurrentWorkID, session.WorkStatus(work.Status), views, now, a.idle)
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
	s, ok := a.ownSession(w, r, id)
	if !ok {
		return
	}
	out, err := a.wh.Do(ctx, "assign", idem.Key, map[string]string{"warehouseId": s.WarehouseID})
	if err != nil {
		fail(w, r, transportError{err})
		return
	}
	if !out.OK() {
		fail(w, r, httpx.NewProblem(out.Status, out.Code, out.Detail))
		return
	}
	work, found := warehouse.WorkFrom(out.Body)
	if !found {
		httpx.JSON(w, http.StatusOK, assignResponse{Work: json.RawMessage("null"), Session: s})
		return
	}
	doc := docOf(s)
	doc.Seed, doc.Run = work.Values(), nil
	saved, err := a.st.SaveRun(ctx, id, s.Version, doc.raw(), work.ID, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	var wrapped struct {
		Work json.RawMessage `json:"work"`
	}
	raw := out.Body
	if json.Unmarshal(out.Body, &wrapped) == nil && len(wrapped.Work) > 0 {
		raw = wrapped.Work
	}
	answer(w, http.StatusOK, out.Replayed, assignResponse{Work: raw, Session: saved})
}

func (a *API) ownSession(w http.ResponseWriter, r *http.Request, id string) (store.Session, bool) {
	s, err := a.st.GetSession(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return store.Session{}, false
	}
	if s.UserID != userOf(r) {
		fail(w, r, store.ErrNotOwner)
		return store.Session{}, false
	}
	if s.Status == string(session.Ended) {
		fail(w, r, store.ErrSessionEnded)
		return store.Session{}, false
	}
	return s, true
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
	s, ok := a.ownSession(w, r, id)
	if !ok {
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
	if _, err := a.st.SaveRun(ctx, id, s.Version, doc.raw(), s.CurrentWorkID, a.now()); err != nil {
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
// the request's Idempotency-Key, and moves on only when the call succeeds.
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
	ctx, id := r.Context(), chi.URLParam(r, "id")
	s, ok := a.ownSession(w, r, id)
	if !ok {
		return
	}
	doc := docOf(s)
	if doc.Run == nil {
		fail(w, r, httpx.NewProblem(http.StatusConflict, codeStep, "the session has no flow run in progress"))
		return
	}
	pinned, err := a.st.FlowVersion(ctx, doc.Run.FlowCode, doc.Run.Version)
	if err != nil {
		fail(w, r, err)
		return
	}
	draft := pinned.Draft()
	res, err := flows.Submit(draft, *doc.Run, chi.URLParam(r, "step"), flows.Input{Value: req.Value, Outcome: req.Outcome}, gs1.ClassifyOptions{Options: gs1.Options{Now: a.now()}})
	if err != nil {
		fail(w, r, stepProblem(err))
		return
	}
	a.stats.steps.Add(1)
	if res.Verdict != nil {
		a.stats.scanErrors.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, res.Verdict.Code, res.Verdict.Message))
		return
	}
	next, workID := res.State, s.CurrentWorkID
	if res.Call != nil {
		out, err := a.wh.Do(ctx, res.Call.Op, idem.Key, res.Call.Params)
		if err != nil {
			fail(w, r, transportError{err})
			return
		}
		if !out.OK() {
			fail(w, r, httpx.NewProblem(out.Status, out.Code, out.Detail))
			return
		}
		produced := map[string]string{}
		if work, found := warehouse.WorkFrom(out.Body); found {
			produced, workID = work.Values(), work.ID
		}
		if next, err = flows.Resolve(draft, res.State, produced); err != nil {
			fail(w, r, stepProblem(err))
			return
		}
	}
	doc.Run = &next
	if _, err := a.st.SaveRun(ctx, id, s.Version, doc.raw(), workID, a.now()); err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, viewOf(draft, next))
}

func stepProblem(err error) error {
	switch {
	case errors.Is(err, flows.ErrFlowFinished):
		return httpx.NewProblem(http.StatusConflict, codeStep, err.Error())
	case errors.Is(err, flows.ErrWrongStep):
		return httpx.NewProblem(http.StatusConflict, codeStep, err.Error())
	case errors.Is(err, flows.ErrNoOutcome), errors.Is(err, flows.ErrValueNeeded):
		return httpx.NewProblem(http.StatusBadRequest, codeValidation, err.Error())
	}
	return err
}
