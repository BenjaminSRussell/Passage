package api

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

// maxHandlingUnits bounds the handling units one job reports.
const maxHandlingUnits = 100000

type startJobRequest struct {
	Version int `json:"version"`
}

// startJob starts a kanban job on the work-cell board (FR-24.10, BR-24.6).
func (a *API) startJob(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req startJobRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.Version < 1 {
		invalid(w, r, map[string]string{"version": "must be the version you read (1 or more)"})
		return
	}
	j, fresh, err := a.store.StartJob(r.Context(), id, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, j)
}

type completeJobRequest struct {
	Version       int              `json:"version"`
	ReportedQty   *decimal.Decimal `json:"reportedQty"`
	ScrapQty      *decimal.Decimal `json:"scrapQty"`
	HandlingUnits *int             `json:"handlingUnits"`
}

// validReport is a reported or scrap quantity: zero or more, below 10^12,
// at most 6 decimals (numeric(18,6)).
func validReport(d decimal.Decimal) bool {
	return !d.IsNegative() && d.LessThan(maxQty()) && d.Exponent() >= -6
}

// completeJob completes a kanban job with the good quantity, the scrap and
// the handling units (FR-24.10). Scrap defaults to 0 and handling units to 1.
func (a *API) completeJob(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req completeJobRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	reported, scrap, units := decimal.Zero, decimal.Zero, 1
	if req.ReportedQty == nil {
		fields["reportedQty"] = "is required (the good quantity; 0 when everything was scrapped)"
	} else if reported = *req.ReportedQty; !validReport(reported) {
		fields["reportedQty"] = "must be zero or more, below 10^12, with at most 6 decimals"
	}
	if req.ScrapQty != nil {
		if scrap = *req.ScrapQty; !validReport(scrap) {
			fields["scrapQty"] = "must be zero or more, below 10^12, with at most 6 decimals"
		}
	}
	if req.HandlingUnits != nil {
		if units = *req.HandlingUnits; units < 1 || units > maxHandlingUnits {
			fields["handlingUnits"] = "must be 1 to 100000"
		}
	}
	if len(fields) == 0 && !reported.Add(scrap).IsPositive() {
		fields["reportedQty"] = "report a good or scrap quantity above zero"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	j, fresh, err := a.store.CompleteJob(r.Context(), id, req.Version, domain.Completion{ReportedQty: reported, ScrapQty: scrap, HandlingUnits: units}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, j)
}

// jobColumn is one column of the work-cell board: the jobs in one state.
type jobColumn struct {
	State domain.State `json:"state"`
	Jobs  []store.Job  `json:"jobs"`
}

// jobBoard answers GET /v1/lean/board?workCellId= (FR-24.10): one column per
// job state, Not planned to Completed.
func (a *API) jobBoard(w http.ResponseWriter, r *http.Request, f store.BoardFilter) {
	jobs, err := a.store.BoardJobs(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	var takt *taktDTO
	if f.FlowID != "" {
		if takt, err = a.flowTakt(r.Context(), f.FlowID); err != nil {
			fail(w, r, err)
			return
		}
	}
	cols := []jobColumn{}
	for _, s := range []domain.State{domain.NotPlanned, domain.Planned, domain.Prepared, domain.InProgress, domain.Completed} {
		c := jobColumn{State: s, Jobs: []store.Job{}}
		for _, j := range jobs {
			if j.State == s {
				c.Jobs = append(c.Jobs, j)
			}
		}
		cols = append(cols, c)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"workCellId": f.WorkCellID, "columns": cols, "truncated": len(jobs) == store.MaxBoard, "takt": takt})
}
