package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	intakeErrors = "/v1/sales/intake-errors"
	// maxResolutionNote bounds the note that closes an intake error.
	maxResolutionNote = 500
	// maxExternalRef is the longest reference an order accepts; a longer one
	// is refused and logged cut to this length.
	maxExternalRef = 100
	// uiChannel is the channel of orders typed in the web app; only other
	// channels (EDI, portal, API clients) are logged when refused.
	uiChannel = "UI"
)

// registerIntakeErrors mounts the intake error routes (PRD 5.27.14).
func (a *API) registerIntakeErrors(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(intakeErrors, a.listIntakeErrors)
	r.With(authz.RequireDuty(dutyRead)).Get(intakeErrors+"/{id}", a.getIntakeError)
	r.With(authz.RequireDuty(dutyWrite)).Post(intakeErrors+"/{id}:resolve", a.resolveIntakeError)
	r.With(authz.RequireDuty(dutyWrite)).Post(intakeErrors+"/{id}:replay", a.replayIntakeError)
	r.With(authz.RequireDuty(dutyWrite)).Post(intakeErrors+":replay", a.replayIntakeErrors)
}

// logIntakeFailure records why a channel order was refused, best effort: a
// failure to log it is only logged and never changes the answer. Orders typed
// in the UI, requests without a valid legal entity in the caller's scope,
// server faults (status 5xx) and the 409 of a concurrent request that stored
// the same order first (a retry returns it) are not logged. A reference that
// failed validation is kept to its first maxExternalRef characters.
func (a *API) logIntakeFailure(ctx context.Context, req createOrderRequest, key string, err error) {
	channel := req.Channel
	if channel == "" || channel == uiChannel || !channelName.MatchString(channel) || !ids.Valid(req.LegalEntityID) {
		return
	}
	p, _ := tenant.FromContext(ctx)
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		return
	}
	var prob *httpx.Problem
	if !errors.As(err, &prob) || prob.Status >= http.StatusInternalServerError || store.IsConcurrentIntake(err) {
		return
	}
	hash, herr := domain.RequestHash(req)
	if herr != nil {
		return
	}
	payload, merr := json.Marshal(req)
	if merr != nil {
		return
	}
	message := problemMessage(prob)
	in := store.NewIntakeError{LegalEntityID: req.LegalEntityID, Channel: channel, RequestHash: hash, ErrorCode: prob.Code, Message: message,
		Payload: payload, IdempotencyKey: key, PersonalData: req.OneTime != nil}
	if req.ExternalRef != nil {
		in.ExternalRef = *req.ExternalRef
		if ref := []rune(in.ExternalRef); len(ref) > maxExternalRef {
			in.ExternalRef = string(ref[:maxExternalRef])
		}
	}
	if _, serr := a.store.RecordIntakeError(ctx, in, a.now()); serr != nil {
		slog.Error("record intake error failed", "err", serr)
	}
}

// problemMessage is a refusal's detail followed by its field errors in a
// stable order.
func problemMessage(prob *httpx.Problem) string {
	message := prob.Detail
	if len(prob.Errors) > 0 {
		keys := make([]string, 0, len(prob.Errors))
		for k, v := range prob.Errors {
			keys = append(keys, k+" "+v)
		}
		sort.Strings(keys)
		message += ": " + strings.Join(keys, "; ")
	}
	return message
}

func (a *API) listIntakeErrors(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.IntakeErrorFilter{LegalEntityID: legalEntity, Channel: q.Get("channel"), Status: q.Get("status"), ErrorCode: q.Get("errorCode")}
	fields := map[string]string{}
	if f.Channel != "" && !channelName.MatchString(f.Channel) {
		fields["channel"] = "must be 1 to 40 letters, digits, - or _"
	}
	if f.Status != "" && f.Status != store.IntakeErrorOpen && f.Status != store.IntakeErrorResolvedStatus {
		fields["status"] = "must be OPEN or RESOLVED"
	}
	if len(f.ErrorCode) > 40 {
		fields["errorCode"] = "must be at most 40 characters"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	items, err := a.store.ListIntakeErrors(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list intake errors", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func intakeErrorRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeIntakeErrorNotFound, "intake error not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getIntakeError(w http.ResponseWriter, r *http.Request) {
	intakeErrorRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		e, err := a.store.GetIntakeError(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get intake error", err)
			return
		}
		setETag(w, e.Version)
		httpx.JSON(w, http.StatusOK, e)
	})(w, r)
}

type resolveIntakeErrorRequest struct {
	Note string `json:"note"`
}

// resolveIntakeError closes an OPEN intake error with a required note.
func (a *API) resolveIntakeError(w http.ResponseWriter, r *http.Request) {
	intakeErrorRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req resolveIntakeErrorRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		note := strings.TrimSpace(req.Note)
		if note == "" || len([]rune(note)) > maxResolutionNote {
			validationFailed(w, r, map[string]string{"note": fmt.Sprintf("must be 1 to %d characters", maxResolutionNote)})
			return
		}
		p, _ := tenant.FromContext(r.Context())
		e, err := a.store.ResolveIntakeError(r.Context(), id, version, note, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "resolve intake error", err)
			return
		}
		setETag(w, e.Version)
		httpx.JSON(w, http.StatusOK, e)
	})(w, r)
}

const (
	// defaultReplayBatch and maxReplayBatch bound one bulk replay.
	defaultReplayBatch = 50
	maxReplayBatch     = 100
)

// replayFailure is why a replay was refused again.
type replayFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// replayResult is the outcome of replaying one refused order. Replayed is true
// when the order was stored (OrderID, OrderNumber) and the intake error closed;
// otherwise the intake error stays OPEN with one more attempt and Failure says
// why the order was refused again.
type replayResult struct {
	IntakeError store.IntakeError `json:"intakeError"`
	Replayed    bool              `json:"replayed"`
	OrderID     *string           `json:"orderId,omitempty"`
	OrderNumber *string           `json:"orderNumber,omitempty"`
	Failure     *replayFailure    `json:"failure,omitempty"`
}

// replayOne sends a kept refusal through order intake again as the caller,
// with the caller's legal-entity scope and duties. A refusal of the order
// (4xx) is counted on the row and returned as a result; any other error is
// returned as an error and counts nothing: a fault, a lost row, a caller the
// order is out of scope for (401/403), and the 409 SLS-0007 of a concurrent
// replay or retry that is storing the same order under the same key (the next
// replay finds that order and closes the row).
func (a *API) replayOne(ctx context.Context, p tenant.Principal, rep store.IntakeReplay) (replayResult, error) {
	var req createOrderRequest
	if err := json.Unmarshal(rep.Payload, &req); err != nil {
		return replayResult{}, httpx.NewProblem(http.StatusConflict, store.CodeIntakeNotReplayable, "the kept request cannot be read; resolve this error instead")
	}
	key := rep.IdempotencyKey
	if key == "" {
		key = "replay:" + rep.ID
	}
	order, created, err := a.submitOrder(ctx, p, req, key)
	var prob *httpx.Problem
	if err != nil {
		if !errors.As(err, &prob) || prob.Status >= http.StatusInternalServerError || prob.Status == http.StatusUnauthorized ||
			prob.Status == http.StatusForbidden || store.IsConcurrentIntake(err) {
			return replayResult{}, err
		}
		e, ferr := a.store.RecordReplayFailure(ctx, rep.ID, prob.Code, problemMessage(prob), a.now())
		if ferr != nil {
			return replayResult{}, ferr
		}
		return replayResult{IntakeError: e, Failure: &replayFailure{Code: prob.Code, Message: problemMessage(prob)}}, nil
	}
	if created {
		a.created.Add(1)
	}
	e, err := a.store.ResolveReplayed(ctx, rep.ID, order.ID, order.OrderNumber, p.UserID, a.now())
	if err != nil {
		return replayResult{}, err
	}
	return replayResult{IntakeError: e, Replayed: true, OrderID: &order.ID, OrderNumber: &order.OrderNumber}, nil
}

// replayIntakeError replays one OPEN refusal (PRD 5.27.10). The kept request
// goes through the order intake rules again under the kept Idempotency-Key, so
// a replay that already stored its order returns that order.
func (a *API) replayIntakeError(w http.ResponseWriter, r *http.Request) {
	intakeErrorRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		p, _ := tenant.FromContext(r.Context())
		rep, err := a.store.GetIntakeReplay(r.Context(), id)
		if err != nil {
			a.fail(w, r, "load intake replay", err)
			return
		}
		res, err := a.replayOne(r.Context(), p, rep)
		if err != nil {
			a.fail(w, r, "replay intake error", err)
			return
		}
		setETag(w, res.IntakeError.Version)
		httpx.JSON(w, http.StatusOK, res)
	})(w, r)
}

// replayIntakeErrors replays the OPEN refusals of a legal entity, oldest first,
// up to the batch limit. Rows that failed MaxReplayAttempts times are the dead
// letters: counted in skipped and left for an operator. It is safe to repeat,
// which is what a scheduled retry does.
func (a *API) replayIntakeErrors(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.IntakeErrorFilter{LegalEntityID: legalEntity, Channel: q.Get("channel"), ErrorCode: q.Get("errorCode"), Limit: defaultReplayBatch}
	fields := map[string]string{}
	if f.Channel != "" && !channelName.MatchString(f.Channel) {
		fields["channel"] = "must be 1 to 40 letters, digits, - or _"
	}
	if len(f.ErrorCode) > 40 {
		fields["errorCode"] = "must be at most 40 characters"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxReplayBatch {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxReplayBatch)
		} else {
			f.Limit = n
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	rows, skipped, err := a.store.ListIntakeReplays(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list intake replays", err)
		return
	}
	out := struct {
		Attempted int            `json:"attempted"`
		Replayed  int            `json:"replayed"`
		Failed    int            `json:"failed"`
		Skipped   int            `json:"skipped"`
		Items     []replayResult `json:"items"`
	}{Skipped: skipped, Items: []replayResult{}}
	for _, rep := range rows {
		res, err := a.replayOne(r.Context(), p, rep)
		if err != nil {
			var prob *httpx.Problem
			if errors.As(err, &prob) && prob.Status < http.StatusInternalServerError {
				// Resolved or lost between the list and the replay, or being
				// stored by a concurrent replay: not an attempt.
				continue
			}
			a.fail(w, r, "replay intake errors", err)
			return
		}
		out.Attempted++
		if res.Replayed {
			out.Replayed++
		} else {
			out.Failed++
		}
		out.Items = append(out.Items, res)
	}
	httpx.JSON(w, http.StatusOK, out)
}
