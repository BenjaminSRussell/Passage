package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// Process message duties (PRD 4.25.17). DutyProcessDeliver is held by the
// service identities of the consuming contexts (w21, w24, w33, w35), not by people.
const (
	DutyProcessCreate  = "shopfloor.process.create"
	DutyProcessDeliver = "shopfloor.process.deliver"
)

const (
	defaultProcessList = 100
	maxProcessList     = 500
	maxReprocessReason = 500
	maxDeliveryError   = 1000
)

var processStatuses = map[string]bool{
	"": true, domain.MessageReceived: true, domain.MessageDelivered: true, domain.MessagePartial: true, domain.MessageError: true,
}

const destinationRule = "must be one of ebr, journal, quality, kafka, webhook"

type processMessageRequest struct {
	Source          string                  `json:"source"`
	SourceMessageID string                  `json:"sourceMessageId"`
	CategoryCode    string                  `json:"categoryCode"`
	OrderID         string                  `json:"orderId"`
	PhasePath       string                  `json:"phasePath"`
	OccurredAt      time.Time               `json:"occurredAt"`
	Values          []domain.Characteristic `json:"values"`
}

type deliveryReportRequest struct {
	Outcome string  `json:"outcome"`
	Error   *string `json:"error"`
}

type reprocessRequest struct {
	Destination string `json:"destination"`
	Reason      string `json:"reason"`
}

// registerProcess mounts the process message routes (FR-25.17).
func (a *API) registerProcess(r chi.Router) {
	r.With(authz.RequireDuty(DutyProcessCreate)).Post("/api/v1/shopfloor/process-messages", a.createProcessMessage)
	r.With(authz.RequireDuty(DutyView)).Get("/api/v1/shopfloor/process-messages", a.listProcessMessages)
	r.With(authz.RequireDuty(DutyView)).Get("/api/v1/shopfloor/process-messages/{id}", a.getProcessMessage)
	r.With(authz.RequireDuty(DutyProcessDeliver)).Post("/api/v1/shopfloor/process-messages/{id}/deliveries/{destination}:report", a.reportDelivery)
	r.With(authz.RequireDuty(DutyProcessCreate)).Post("/api/v1/shopfloor/process-messages/{id}:reprocess", a.reprocessDelivery)
}

// createProcessMessage stores a message from a kiosk PI sheet, the edge or manual
// entry. It needs no Idempotency-Key: (source, sourceMessageId) is the key, and a
// replay answers 200 with the stored message and duplicate=true.
func (a *API) createProcessMessage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req processMessageRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.NewMessage{Source: req.Source, SourceMessageID: req.SourceMessageID, CategoryCode: req.CategoryCode,
		OrderID: req.OrderID, PhasePath: req.PhasePath, OccurredAt: req.OccurredAt, Values: req.Values}
	if fields := in.FieldErrors(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	cat, _ := domain.CategoryByCode(in.CategoryCode)
	values, err := domain.ApplyLimits(cat, in.Values)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
			return
		}
		a.fail(w, r, "check process message values", err)
		return
	}
	in.Values = values
	res, err := a.store.IngestProcessMessage(r.Context(), in, cat, a.now().UTC())
	if err != nil {
		a.fail(w, r, "ingest process message", err)
		return
	}
	if res.Duplicate {
		a.processReplays.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.processStored.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) listProcessMessages(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if !processStatuses[q.Get("status")] {
		fields["status"] = "must be one of received, delivered, partially-delivered, error"
	}
	if c := q.Get("category"); c != "" {
		if _, ok := domain.CategoryByCode(c); !ok {
			fields["category"] = "is not a known category"
		}
	}
	if d := q.Get("destination"); d != "" && !domain.KnownDestination(d) {
		fields["destination"] = destinationRule
	}
	if o := q.Get("orderId"); o != "" && !ids.Valid(o) {
		fields["orderId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r, defaultProcessList, maxProcessList)
	if !ok {
		return
	}
	items, err := a.store.ListProcessMessages(r.Context(), store.ProcessFilter{
		Status: q.Get("status"), Category: q.Get("category"), Destination: q.Get("destination"), OrderID: q.Get("orderId"), Limit: limit,
	})
	if err != nil {
		a.fail(w, r, "list process messages", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.ProcessMessage]{Items: items})
}

func (a *API) getProcessMessage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeProcessNotFound, "process message not found"))
		return
	}
	m, err := a.store.GetProcessMessage(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get process message", err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

// reportDelivery records the outcome a consuming context reports for its
// destination. It needs an Idempotency-Key, so a report retried after a lost
// response does not count a failure twice.
func (a *API) reportDelivery(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	id, dest := chi.URLParam(r, "id"), chi.URLParam(r, "destination")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeProcessNotFound, "process message not found"))
		return
	}
	var req deliveryReportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.KnownDestination(dest) {
		fields["destination"] = destinationRule
	}
	if req.Outcome != domain.OutcomeDelivered && req.Outcome != domain.OutcomeFailed {
		fields["outcome"] = "must be delivered or failed"
	}
	if req.Error != nil {
		trimmed := strings.TrimSpace(*req.Error)
		if len([]rune(trimmed)) > maxDeliveryError {
			fields["error"] = fmt.Sprintf("must be at most %d characters", maxDeliveryError)
		}
		req.Error = &trimmed
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	m, err := a.store.ReportDelivery(r.Context(), id, dest, req.Outcome, req.Error, a.now().UTC(), idem)
	if err != nil {
		a.fail(w, r, "report process message delivery", err)
		return
	}
	if req.Outcome == domain.OutcomeFailed {
		for _, d := range m.Deliveries {
			if d.Destination == dest && d.Status != domain.DeliveryDelivered {
				// SF-0016 (warning): the delivery is retried on the schedule, then waits for a reprocess.
				slog.Warn("SF-0016 process message delivery failed", "messageId", id, "destination", dest, "attempt", d.Attempts,
					"status", d.Status, "correlationId", middleware.GetReqID(r.Context()))
			}
		}
	}
	httpx.JSON(w, http.StatusOK, m)
}

// reprocessDelivery returns a destination in error to pending with a reason
// and publishes the message again for that destination.
func (a *API) reprocessDelivery(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeProcessNotFound, "process message not found"))
		return
	}
	var req reprocessRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.KnownDestination(req.Destination) {
		fields["destination"] = destinationRule
	}
	reason := strings.TrimSpace(req.Reason)
	if n := len([]rune(reason)); n < 1 || n > maxReprocessReason {
		fields["reason"] = fmt.Sprintf("must be 1 to %d characters", maxReprocessReason)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	m, err := a.store.ReprocessDelivery(r.Context(), id, req.Destination, reason, a.now().UTC(), idem)
	if err != nil {
		a.fail(w, r, "reprocess process message delivery", err)
		return
	}
	a.processReprocessed.Add(1)
	httpx.JSON(w, http.StatusOK, m)
}
