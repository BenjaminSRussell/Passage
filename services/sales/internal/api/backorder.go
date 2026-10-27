package api

import (
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

const (
	maxBackorderLines = 10000
	defaultLoseThresh = 3
	codeDeconfirm     = "SLS-0015"
)

type backorderLineRequest struct {
	ID            string `json:"id"`
	PriorityClass int    `json:"priorityClass"`
	RequestedDate string `json:"requestedDate"`
	OrderValue    string `json:"orderValue"`
	Demand        string `json:"demand"`
	Confirmed     string `json:"confirmed"`
}

// backorderPreviewRequest describes one backorder run for one product and site
// (FR-27.17). loseThreshold defaults to 3 when omitted.
type backorderPreviewRequest struct {
	Strategy      string                 `json:"strategy"`
	Available     string                 `json:"available"`
	LoseThreshold int                    `json:"loseThreshold"`
	Lines         []backorderLineRequest `json:"lines"`
}

type plannedLineResponse struct {
	ID          string          `json:"id"`
	Before      decimal.Decimal `json:"before"`
	After       decimal.Decimal `json:"after"`
	Changed     bool            `json:"changed"`
	Deconfirmed bool            `json:"deconfirmed"`
}

// backorderPreviewResponse is always a preview: nothing is committed (BR-27.11).
type backorderPreviewResponse struct {
	Preview          bool                  `json:"preview"`
	Strategy         string                `json:"strategy"`
	Allocated        decimal.Decimal       `json:"allocated"`
	DeconfirmedCount int                   `json:"deconfirmedCount"`
	Warning          string                `json:"warning,omitempty"`
	WarningCode      string                `json:"warningCode,omitempty"`
	Lines            []plannedLineResponse `json:"lines"`
}

// previewBackorder computes what a backorder run would confirm under the
// requested strategy. Lines in the response are in processing order.
func (a *API) previewBackorder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req backorderPreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := map[string]string{}
	available := parseDecimal(f, "available", req.Available)
	if len(req.Lines) == 0 || len(req.Lines) > maxBackorderLines {
		f["lines"] = fmt.Sprintf("must have 1 to %d lines", maxBackorderLines)
	}
	threshold := req.LoseThreshold
	if threshold == 0 {
		threshold = defaultLoseThresh
	}
	lines := make([]domain.BackorderLine, 0, len(req.Lines))
	for i, l := range req.Lines {
		path := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		lines = append(lines, domain.BackorderLine{
			ID:            l.ID,
			Priority:      l.PriorityClass,
			RequestedDate: l.RequestedDate,
			OrderValue:    parseDecimal(f, path("orderValue"), l.OrderValue),
			Demand:        parseDecimal(f, path("demand"), l.Demand),
			Confirmed:     parseDecimal(f, path("confirmed"), l.Confirmed),
		})
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	plan, err := domain.PlanBackorder(req.Strategy, available, threshold, lines)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeInvalidRun, err.Error()))
		return
	}
	out := backorderPreviewResponse{Preview: true, Strategy: plan.Strategy, Allocated: plan.Allocated,
		DeconfirmedCount: plan.DeconfirmedCount, Lines: make([]plannedLineResponse, 0, len(plan.Lines))}
	if plan.DeconfirmedCount > 0 {
		out.WarningCode = codeDeconfirm
		out.Warning = fmt.Sprintf("backorder run would deconfirm %d lines", plan.DeconfirmedCount)
	}
	for _, l := range plan.Lines {
		out.Lines = append(out.Lines, plannedLineResponse{ID: l.ID, Before: l.Before, After: l.After, Changed: l.Changed, Deconfirmed: l.Deconfirmed})
	}
	httpx.JSON(w, http.StatusOK, out)
}
