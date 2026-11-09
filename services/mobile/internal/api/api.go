// Package api exposes the mobile service HTTP surface under /v1/mobile:
// device sessions, role and site menus, versioned flows and their step runner,
// scan parsing and offline replay with the supervisor conflict queue. Handlers
// decode, validate, call the store, the flow runner or the warehouse client
// and answer with httpx.JSON or httpx.Problem.
package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/replay"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
)

const (
	dutyExecute   = "warehouse.work.execute"
	dutyAdmin     = "warehouse.mobile-flow.admin"
	dutySupervise = "warehouse.work.supervise"
	dutyResolve   = "warehouse.offline-conflict.resolve"
)

const (
	codeValidation   = "COM-0001"
	codeTenant       = "AUTH-0001"
	codeNotFound     = "MOB-NOT-FOUND"
	codeDuplicate    = "MOB-DUPLICATE"
	codeSessionBusy  = "MOB-SESSION-ACTIVE"
	codeSessionEnded = "MOB-SESSION-ENDED"
	codeNotOwner     = "MOB-NOT-OWNER"
	codeWarehouse    = "MOB-WAREHOUSE-UNAVAILABLE"
	codeBlocked      = "MOB-REPLAY-BLOCKED"
	codeStale        = "MOB-VERSION-CONFLICT"
	codeStep         = "MOB-STEP"
	codeReuse        = "idempotency_key_reuse"
	codeConflict     = "MOB-0107"
	codeGraph        = "MOB-0113"
	headerKey        = "Idempotency-Key"
	headerReplayed   = "Idempotent-Replayed"
	maxKeyLength     = 200
	maxBody          = 4 << 20
	defaultPage      = 100
	maxPage          = 200
)

// API holds the handlers, the per-tenant publish hub and the counters.
type API struct {
	st    *store.Store
	wh    warehouse.Caller
	run   replay.Runner
	idle  time.Duration
	now   func() time.Time
	hub   *hub
	stats counters
}

type counters struct {
	sessions, scans, scanErrors, steps, published, applied, conflicts, rejected atomic.Int64
}

// New returns the API over st and the warehouse client wh. idle is the device
// session idle timeout after which a session is STALE.
func New(st *store.Store, wh warehouse.Caller, idle time.Duration) *API {
	return &API{st: st, wh: wh, run: replay.Runner{Ledger: st, Warehouse: wh}, idle: idle, now: time.Now, hub: newHub()}
}

// WithClock replaces the wall clock, for tests.
func (a *API) WithClock(now func() time.Time) *API {
	a.now = now
	return a
}

// Register mounts the routes on r. Each one first checks that the tenant is a
// UUID, as row-level security needs.
func (a *API) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(requireTenant)
		const v = "/v1/mobile"
		exec, admin, sup, resolve := authz.RequireDuty(dutyExecute), authz.RequireDuty(dutyAdmin), authz.RequireDuty(dutySupervise), authz.RequireDuty(dutyResolve)

		r.With(exec).Post(v+"/sessions", a.startSession)
		r.With(sup).Get(v+"/sessions", a.listSessions)
		r.With(sup).Post(v+"/sessions:sweep", a.sweep)
		r.With(exec).Post(v+"/sessions/{id}:heartbeat", a.heartbeat)
		r.With(exec).Post(v+"/sessions/{id}:end", a.endSession)
		r.With(sup).Post(v+"/sessions/{id}:force-signout", a.forceSignOut)
		r.With(exec).Post(v+"/sessions/{id}:assign-work", a.assignWork)
		r.With(exec).Post(v+"/sessions/{id}/runs", a.startRun)
		r.With(exec).Post(v+"/sessions/{id}/runs/current/steps/{step}:submit", a.submitStep)

		r.With(admin).Post(v+"/devices", a.registerDevice)
		r.With(admin).Get(v+"/devices", a.listDevices)

		r.With(exec).Get(v+"/menus/{id}", a.getMenu)
		r.With(admin).Get(v+"/menus", a.listMenus)
		r.With(admin).Post(v+"/menus", a.upsertMenu)

		r.With(admin).Post(v+"/flows", a.saveDraft)
		r.With(admin).Get(v+"/flows", a.listFlows)
		r.With(admin).Post(v+"/flows:install-core", a.installCore)
		r.With(admin).Get(v+"/flows/{id}", a.getFlow)
		r.With(admin).Post(v+"/flows/{id}:publish", a.publishFlow)

		r.With(exec).Post(v+"/scan:parse", a.parseScan)
		r.With(exec).Post(v+"/offline:replay", a.replay)
		r.With(exec).Get(v+"/events", a.events)
		r.With(resolve).Get(v+"/offline/conflicts", a.listConflicts)
		r.With(resolve).Post(v+"/offline/conflicts/*", a.resolveConflict)
	})
}

// Metrics serves the admin-port counters in Prometheus text format.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, help string
			v          *atomic.Int64
		}{
			{"mobile_sessions_started_total", "Device sessions started.", &a.stats.sessions},
			{"mobile_scans_parsed_total", "Scans parsed through scan:parse.", &a.stats.scans},
			{"mobile_scan_errors_total", "Scans refused by a step check or by scan:parse.", &a.stats.scanErrors},
			{"mobile_steps_submitted_total", "Flow steps submitted.", &a.stats.steps},
			{"mobile_flows_published_total", "Flow versions published.", &a.stats.published},
			{"mobile_offline_applied_total", "Offline transactions applied.", &a.stats.applied},
			{"mobile_offline_conflicts_total", "Offline transactions parked as CONFLICT.", &a.stats.conflicts},
			{"mobile_offline_rejected_total", "Offline transactions rejected.", &a.stats.rejected},
		} {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", m.name, m.help, m.name, m.name, m.v.Load())
		}
	})
}

func requireTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := tenant.FromContext(r.Context()); !ok || !ids.Valid(p.TenantID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, codeTenant, "missing or malformed tenant"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, codeValidation, "request failed validation")
	p.Errors = fields
	httpx.Fail(w, r, p)
}

type transportError struct{ error }

func (e transportError) Unwrap() error { return e.error }

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var graph *store.GraphError
	var skew *replay.SkewError
	var tr transportError
	var prob *httpx.Problem
	switch {
	case errors.As(err, &prob):
		httpx.Fail(w, r, prob)
	case errors.As(err, &graph):
		p := httpx.NewProblem(http.StatusUnprocessableEntity, codeGraph, graph.Error())
		p.Errors = map[string]string{}
		for _, i := range graph.Issues {
			p.Errors[i.StepID+"#"+strconv.Itoa(len(p.Errors))] = i.Message
		}
		httpx.Fail(w, r, p)
	case errors.As(err, &skew):
		w.Header().Set("Retry-After", "60")
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, replay.CodeSkew, skew.Error()))
	case errors.As(err, &tr):
		w.Header().Set("Retry-After", "5")
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeWarehouse, "the warehouse could not be reached; retry"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeNotFound, "not found"))
	case errors.Is(err, store.ErrConflict):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeDuplicate, "a record with the same key already exists"))
	case errors.Is(err, store.ErrVersionConflict):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionFailed, codeStale, "the record changed since it was read"))
	case errors.Is(err, store.ErrIdempotencyReuse):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeReuse, "Idempotency-Key was already used with a different request"))
	case errors.Is(err, store.ErrSessionActive):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeSessionBusy, "another user has an active session on this device"))
	case errors.Is(err, store.ErrSessionEnded):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeSessionEnded, "the session has ended; sign in again"))
	case errors.Is(err, store.ErrNotOwner):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotOwner, "the session belongs to another user"))
	default:
		httpx.Fail(w, r, err)
	}
}

// command reads a command's optional JSON body into v and returns its replay
// identity: the Idempotency-Key header and a hash of method, path, user and
// body. A missing or oversized key is a field error.
func command(r *http.Request, v any, fields map[string]string) (store.Idem, error) {
	key := r.Header.Get(headerKey)
	if key == "" || len(key) > maxKeyLength {
		fields[headerKey] = fmt.Sprintf("is required, at most %d characters", maxKeyLength)
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeValidation, "request body is too large or unreadable")
	}
	if len(bytes.TrimSpace(body)) > 0 && v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return store.Idem{}, httpx.NewProblem(http.StatusBadRequest, codeValidation, "malformed JSON body: "+err.Error())
		}
	}
	p, _ := tenant.FromContext(r.Context())
	h := sha256.New()
	for _, part := range []string{r.Method, r.URL.Path, p.UserID, string(body)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return store.Idem{Key: key, Hash: hex.EncodeToString(h.Sum(nil))}, nil
}

func answer(w http.ResponseWriter, status int, replayed bool, v any) {
	if replayed {
		w.Header().Set(headerReplayed, "true")
	}
	httpx.JSON(w, status, v)
}

func ifMatch(r *http.Request, fields map[string]string, required bool) int {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		if required {
			fields["If-Match"] = "is required"
		}
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		fields["If-Match"] = "must be the record version, for example \"3\""
		return 0
	}
	return n
}

func limitOf(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return defaultPage
	}
	return min(n, maxPage)
}

func uuidField(fields map[string]string, name, v string) {
	if !ids.Valid(v) {
		fields[name] = "must be a UUID"
	}
}
