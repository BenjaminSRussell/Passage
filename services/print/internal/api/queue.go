package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// Queue management (FR-32.13, §6.32.6): edit and retire a printer, edit and delete a routing
// rule, and re-dispatch jobs left QUEUED by a process that died mid-dispatch (§6.32.16).

const (
	// StaleJobAge is how long a job may sit QUEUED before the sweep dispatches it again.
	// Inline dispatch ends in seconds (two attempts per device with a 2 s transport timeout),
	// so a minute leaves a live request alone.
	StaleJobAge    = time.Minute
	sweepBatchSize = 50
)

// printerPatch carries the fields of PATCH /printers/{id}. An omitted field keeps its value;
// the code never changes.
type printerPatch struct {
	Name         *string `json:"name"`
	Dpi          *int    `json:"dpi"`
	MediaSize    *string `json:"mediaSize"`
	Host         *string `json:"host"`
	Port         *int    `json:"port"`
	QueueCode    *string `json:"queueCode"`
	FailoverRank *int    `json:"failoverRank"`
}

func pathID(w http.ResponseWriter, r *http.Request, code, what string) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, what+" not found"))
		return "", false
	}
	return id, true
}

// updatePrinter edits an active printer, including moving it to another queue or failover rank.
func (a *API) updatePrinter(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodePrinterNotFound, "printer")
	if !ok {
		return
	}
	var req printerPatch
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	cur, err := a.store.GetPrinter(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if cur.RetiredAt != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found or already retired"))
		return
	}
	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.Dpi != nil {
		cur.Dpi = *req.Dpi
	}
	if req.MediaSize != nil {
		cur.MediaSize = *req.MediaSize
	}
	if req.Host != nil {
		cur.Host = *req.Host
	}
	if req.Port != nil {
		cur.Port = *req.Port
	}
	if req.QueueCode != nil {
		cur.QueueCode = *req.QueueCode
	}
	if req.FailoverRank != nil {
		cur.FailoverRank = *req.FailoverRank
	}
	if fields := cur.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	updated, err := a.store.UpdatePrinter(r.Context(), cur)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

// retirePrinter takes a printer out of its queue; later jobs fail over past it.
func (a *API) retirePrinter(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodePrinterNotFound, "printer")
	if !ok {
		return
	}
	retired, err := a.store.RetirePrinter(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, retired)
}

// updateRoutingRule replaces a rule (PUT: every field is given).
func (a *API) updateRoutingRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeRuleNotFound, "routing rule")
	if !ok {
		return
	}
	var req routingRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.RoutingRule{ID: id, DocumentType: req.DocumentType, UserID: req.UserID, Zone: req.Zone, WarehouseID: req.WarehouseID,
		QueueCode: req.QueueCode, Sequence: req.Sequence}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	updated, err := a.store.UpdateRoutingRule(r.Context(), in)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, updated)
}

func (a *API) deleteRoutingRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeRuleNotFound, "routing rule")
	if !ok {
		return
	}
	if err := a.store.DeleteRoutingRule(r.Context(), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SweepResult is the outcome of one sweep.
type SweepResult struct {
	Claimed int `json:"claimed"`
	Printed int `json:"printed"`
	Failed  int `json:"failed"`
}

// SweepStale dispatches the tenant's jobs that have sat QUEUED for at least minAge. A swept job
// has no template data, so it prints the stored layout, like a reprint.
func (a *API) SweepStale(ctx context.Context, minAge time.Duration) (SweepResult, error) {
	jobs, err := a.store.ClaimStaleJobs(ctx, time.Now().Add(-minAge), sweepBatchSize)
	if err != nil {
		return SweepResult{}, err
	}
	res := SweepResult{Claimed: len(jobs)}
	for i := range jobs {
		if err := a.dispatchJob(ctx, &jobs[i], nil); err != nil {
			return res, err
		}
		if jobs[i].Status == domain.JobPrinted {
			res.Printed++
		} else {
			res.Failed++
		}
	}
	return res, nil
}

// SweepClaimed is SweepStale at the default age, for the scheduled sweep in internal/jobs.
func (a *API) SweepClaimed(ctx context.Context) (int, error) {
	res, err := a.SweepStale(ctx, StaleJobAge)
	return res.Claimed, err
}

// sweepJobs serves POST /print/v1/jobs:sweep: re-dispatch the tenant's stale QUEUED jobs.
func (a *API) sweepJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	if _, ok := requireKey(w, r); !ok {
		return
	}
	res, err := a.SweepStale(context.WithoutCancel(r.Context()), StaleJobAge)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
