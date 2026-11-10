package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/replay"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
)

type replayItem struct {
	Seq            int64           `json:"seq"`
	IdempotencyKey string          `json:"idempotencyKey"`
	Command        string          `json:"command"`
	Payload        json.RawMessage `json:"payload"`
	CapturedAt     time.Time       `json:"capturedAt"`
}

type replayRequest struct {
	replayItem
	DeviceID     string       `json:"deviceId"`
	SessionID    string       `json:"sessionId"`
	Transactions []replayItem `json:"transactions"`
}

func tenantOf(r *http.Request) string {
	p, _ := tenant.FromContext(r.Context())
	return p.TenantID
}

// replay accepts a device's queue as a batch (deviceId and transactions) or
// one transaction at a time, as the browser device does (idempotencyKey is
// "deviceId:seq"). A batch answers 200 with a status per transaction. A single
// transaction answers 200 APPLIED, 409 CONFLICT, 422 REJECTED, or 429 when it
// is still waiting behind an earlier unresolved one; the device keeps those
// PENDING. A warehouse outage or a skewed clock answers 503.
func (a *API) replay(w http.ResponseWriter, r *http.Request) {
	var req replayRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	single := len(req.Transactions) == 0
	items, fields := []replay.Item{}, map[string]string{}
	deviceID := req.DeviceID
	if single {
		dev, seq, ok := splitKey(req.IdempotencyKey)
		if !ok {
			fields["idempotencyKey"] = "must be deviceId:seq"
		}
		if h := r.Header.Get(headerKey); h != "" && h != req.IdempotencyKey {
			fields[headerKey] = "must equal idempotencyKey"
		}
		deviceID = dev
		items = append(items, replay.Item{Seq: seq, Command: req.Command, Payload: req.Payload, CapturedAt: req.CapturedAt})
	} else {
		for i, t := range req.Transactions {
			if t.Seq < 1 {
				fields["transactions."+strconv.Itoa(i)+".seq"] = "must be at least 1"
			}
			items = append(items, replay.Item{Seq: t.Seq, Command: t.Command, Payload: t.Payload, CapturedAt: t.CapturedAt})
		}
	}
	if deviceID == "" || strings.Contains(deviceID, ":") {
		fields["deviceId"] = "is required and must not contain ':'"
	}
	for _, it := range items {
		if it.Command == "" || len(it.Payload) == 0 || it.CapturedAt.IsZero() {
			fields["transaction"] = "command, payload and capturedAt are required"
		}
	}
	if len(items) > replay.MaxBatch {
		fields["transactions"] = "at most 500 per request"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	ctx := r.Context()
	if err := missingDuty(ctx, items); err != nil {
		fail(w, r, err)
		return
	}
	own, err := a.st.SessionOnDevice(ctx, deviceID, req.SessionID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotOwner, "the caller has no open session on this device"))
		return
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	results, err := a.run.Run(ctx, deviceID, own.SessionID, items)
	for _, res := range results {
		switch res.Status {
		case store.TxApplied:
			a.stats.applied.Add(1)
		case store.TxConflict:
			a.stats.conflicts.Add(1)
		case store.TxRejected:
			a.stats.rejected.Add(1)
		}
	}
	if err != nil {
		fail(w, r, replayFailure(err))
		return
	}
	if !single {
		httpx.JSON(w, http.StatusOK, map[string]any{"results": results})
		return
	}
	a.answerSingle(w, r, results)
}

// missingDuty refuses a batch holding a command whose warehouse operation the
// caller lacks the duty for, before anything is recorded.
func missingDuty(ctx context.Context, items []replay.Item) error {
	p, _ := tenant.FromContext(ctx)
	for _, it := range items {
		op, _, err := replay.Map(it.Command, it.Payload)
		if err != nil {
			continue
		}
		if duty, known := warehouse.DutyFor(op); known && !p.HasDuty(duty) {
			return httpx.NewProblem(http.StatusForbidden, warehouse.CodeMissingDuty, "missing duty "+duty)
		}
	}
	return nil
}

func replayFailure(err error) error {
	var wh replay.WarehouseError
	switch {
	case errors.Is(err, replay.ErrBatchTooLarge):
		return httpx.NewProblem(http.StatusRequestEntityTooLarge, "COM-0413", "at most 500 transactions per request")
	case errors.As(err, &wh):
		return transportError{err}
	}
	return err
}

func (a *API) answerSingle(w http.ResponseWriter, r *http.Request, results []replay.Result) {
	if len(results) == 0 {
		fail(w, r, store.ErrNotFound)
		return
	}
	res := results[0]
	switch res.Status {
	case store.TxApplied:
		httpx.JSON(w, http.StatusOK, map[string]string{"status": store.TxApplied})
	case store.TxConflict:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeConflict, res.Reason))
	case "PENDING":
		w.Header().Set("Retry-After", "30")
		httpx.Fail(w, r, httpx.NewProblem(http.StatusTooManyRequests, codeBlocked, "waiting behind an earlier transaction that is not applied yet"))
	case replay.StatusGap:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, replay.CodeGap, res.Reason))
	case replay.StatusReused:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeReuse, res.Reason))
	default:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "MOB-REJECTED", res.Reason))
	}
}

func splitKey(key string) (string, int64, bool) {
	i := strings.LastIndex(key, ":")
	if i < 1 {
		return "", 0, false
	}
	seq, err := strconv.ParseInt(key[i+1:], 10, 64)
	return key[:i], seq, err == nil && seq > 0
}

func (a *API) listConflicts(w http.ResponseWriter, r *http.Request) {
	after, afterKey, ok := decodeCursor(r.URL.Query().Get("cursor"))
	if !ok {
		validationFailed(w, r, map[string]string{"cursor": "is not a cursor from this API"})
		return
	}
	limit := limitOf(r)
	items, err := a.st.ListConflicts(r.Context(), after, afterKey, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := map[string]any{"items": nonNil(items)}
	if len(items) == limit {
		out["nextCursor"] = encodeCursor(items[len(items)-1])
	}
	httpx.JSON(w, http.StatusOK, out)
}

type resolveRequest struct {
	Action string `json:"action"`
}

// resolveConflict handles POST /offline/conflicts/{key}:resolve. The key holds
// a colon, so the route is a wildcard and the suffix is checked here.
func (a *API) resolveConflict(w http.ResponseWriter, r *http.Request) {
	key, found := strings.CutSuffix(chi.URLParam(r, "*"), ":resolve")
	if !found || key == "" {
		fail(w, r, store.ErrNotFound)
		return
	}
	var req resolveRequest
	fields := map[string]string{}
	idem, err := command(r, &req, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	version := ifMatch(r, fields, true)
	action := strings.ToUpper(req.Action)
	if action != store.ResolveReapply && action != store.ResolveReject {
		fields["action"] = "must be reapply or reject"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.Resolve(r.Context(), idem, key, action, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	answer(w, http.StatusOK, replayed, out)
}
