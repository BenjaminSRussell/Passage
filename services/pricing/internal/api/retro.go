package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const retroRecomputePath = rebateAgreementsPath + "/{id}:retro-recompute"

type retroLineDTO struct {
	InvoiceLineID string `json:"invoiceLineId"`
	InvoiceDate   string `json:"invoiceDate"`
	Value         string `json:"value"`
	Quantity      string `json:"quantity"`
	Currency      string `json:"currency"`
	// ProductID and ProductGroupID are required to match when the agreement has a product scope.
	ProductID      string `json:"productId"`
	ProductGroupID string `json:"productGroupId"`
}

type retroRequest struct {
	Lines []retroLineDTO `json:"lines"`
}

type retroDeltaView struct {
	PeriodID string `json:"periodId"`
	Target   string `json:"target"`
	Posted   string `json:"posted"`
	Delta    string `json:"delta"`
}

type noticeView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type retroView struct {
	AgreementID    string           `json:"agreementId"`
	ActivatedOn    string           `json:"activatedOn"`
	ReaccruedLines int              `json:"reaccruedLines"`
	Replayed       bool             `json:"replayed"`
	Deltas         []retroDeltaView `json:"deltas"`
	Notice         *noticeView      `json:"notice,omitempty"`
}

// retroRecompute replays the invoices posted between a retroactive agreement's valid_from
// and its activation, and posts the accrual delta per period with retroactive=true
// (FR-26.23, BR-26.14, AC-26.14). The Idempotency-Key names the run and is required; a
// repeated key returns the stored deltas. Until the w29/w30 invoice ledger exists, the
// caller supplies the posted invoice lines (as /rebates/accruals stands in for the consumer).
func (a *API) retroRecompute(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id))
		return
	}
	var req retroRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	runKey := r.Header.Get(idempotencyKey)
	if runKey == "" || len(runKey) > 64 {
		fields["Idempotency-Key"] = "is required and at most 64 characters"
	}
	if len(req.Lines) > store.MaxRetroLines {
		fields["lines"] = "at most " + strconv.Itoa(store.MaxRetroLines) + " invoice lines per request"
	}
	in := store.RetroInput{AgreementID: id, RunKey: runKey, Lines: make([]store.RetroLine, 0, len(req.Lines))}
	for i, l := range req.Lines {
		if len(fields) > 0 {
			break
		}
		key := "lines[" + strconv.Itoa(i) + "]."
		if l.InvoiceLineID == "" || len(l.InvoiceLineID) > maxInvoiceLineID {
			fields[key+"invoiceLineId"] = "must be 1 to 128 characters"
		}
		day, err := time.Parse(dateLayout, l.InvoiceDate)
		if err != nil {
			fields[key+"invoiceDate"] = "must be a date in YYYY-MM-DD form"
		}
		value, err1 := decimal.NewFromString(orDefault(l.Value, "0"))
		qty, err2 := decimal.NewFromString(orDefault(l.Quantity, "0"))
		if err1 != nil {
			fields[key+"value"] = "must be a decimal"
		}
		if err2 != nil {
			fields[key+"quantity"] = "must be a decimal"
		}
		if !currencyCode.MatchString(l.Currency) {
			fields[key+"currency"] = "must be an ISO 4217 code"
		}
		if l.ProductID != "" && !ids.Valid(l.ProductID) {
			fields[key+"productId"] = "must be a UUID"
		}
		if l.ProductGroupID != "" && !ids.Valid(l.ProductGroupID) {
			fields[key+"productGroupId"] = "must be a UUID"
		}
		in.Lines = append(in.Lines, store.RetroLine{InvoiceLineID: l.InvoiceLineID, InvoiceDate: day, Value: value, Quantity: qty, Currency: l.Currency,
			ProductID: l.ProductID, ProductGroupID: l.ProductGroupID})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, err := a.store.RecomputeRetro(r.Context(), in)
	if err != nil {
		slog.Error("retroactive recompute failed", "err", err, "agreementId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	out := retroView{AgreementID: id, ActivatedOn: res.ActivatedOn.Format(dateLayout), ReaccruedLines: res.ReaccruedLines,
		Replayed: res.Replayed, Deltas: make([]retroDeltaView, 0, len(res.Deltas))}
	for _, d := range res.Deltas {
		out.Deltas = append(out.Deltas, retroDeltaView{PeriodID: d.Period, Target: d.Target.String(), Posted: d.Posted.String(), Delta: d.Delta.String()})
	}
	if res.ReaccruedLines > 0 {
		out.Notice = &noticeView{Code: domain.CodeRetroactive,
			Message: fmt.Sprintf("Retroactive activation: %d posted invoices were re-accrued", res.ReaccruedLines)}
	}
	if !res.Replayed {
		a.retroRuns.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// registerRetroRoutes mounts the retroactive recompute (FR-26.23).
func (a *API) registerRetroRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyRebateMaintain)).Post(retroRecomputePath, a.retroRecompute)
}
