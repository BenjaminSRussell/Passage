package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	// OrderScheduledType is production's order.scheduled.v1 (wave 21, FR-21.2).
	OrderScheduledType  = "dev.passage.production.order.scheduled.v1"
	OrderScheduledTopic = "passage.production.order"
	// OrderStatusChangedType is production's order.statuschanged.v1, on the same
	// topic and partition (keyed by order id) as order.scheduled.v1. With no
	// dates it says the order's schedule was withdrawn (reset, cancel, merge).
	OrderStatusChangedType = "dev.passage.production.order.statuschanged.v1"
	// OrderImportConsumerName is the consumer (and Kafka group) of the order
	// intake and the service principal it runs as, service:scheduling-order-import.
	// It is its own consumer, separate from the EAM downtime one, so it holds
	// only the order intake duty, is audited under its own actor and parks its
	// failures on its own dead-letter topic.
	OrderImportConsumerName = "scheduling-order-import"
	// DutyOrderImport lets the order intake register, change and remove the
	// operations of production orders. Only its service principal holds it.
	DutyOrderImport = "scheduling.order.import"

	orderImportTimeout = 30 * time.Second
	// orderReasonFinite marks an order.scheduled.v1 that production wrote when
	// it applied scheduling's finite-capacity dates; the intake ignores it.
	orderReasonFinite = "finite"
)

// orderScheduledData is the part of order.scheduled.v1 scheduling reads.
type orderScheduledData struct {
	OrderID      string  `json:"orderId"`
	OrderNumber  string  `json:"orderNumber"`
	Reason       string  `json:"reason"`
	Start        string  `json:"start"`
	End          string  `json:"end"`
	DeliveryDate *string `json:"deliveryDate"`
	OrderVersion int     `json:"orderVersion"`
	Jobs         []struct {
		OperNumber   int    `json:"operNumber"`
		Seq          int    `json:"seq"`
		JobType      string `json:"jobType"`
		ResourceID   string `json:"resourceId"`
		PlannedHours string `json:"plannedHours"`
	} `json:"jobs"`
}

// OrderConsumerHandlers handles production's order.scheduled.v1 and the
// withdrawing order.statuschanged.v1 (FR-23.10) on the order intake consumer,
// OrderImportConsumerName.
func (a *API) OrderConsumerHandlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{
		OrderScheduledType:     eventing.HandlerFunc(a.importScheduledOrder),
		OrderStatusChangedType: eventing.HandlerFunc(a.withdrawScheduledOrder),
	}
}

// orderStatusChangedData is the part of order.statuschanged.v1 scheduling reads.
type orderStatusChangedData struct {
	OrderID   string  `json:"orderId"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	StartDate *string `json:"startDate"`
	EndDate   *string `json:"endDate"`
}

// withdrawnStatuses are the production statuses whose entry takes the schedule
// back (wave 21: a reset to Estimated or Created, a cancel, a merge cancelling
// the order into another); production clears the order's dates and jobs then.
var withdrawnStatuses = map[string]bool{"Created": true, "Estimated": true, "Cancelled": true}

// withdrawScheduledOrder withdraws the operations of an order production took
// out of schedule (FR-23.10): the order moved to Estimated, Created or
// Cancelled and its statuschanged event carries null dates. Null dates alone
// do not withdraw: production's journal posts (auto-start, report as finished)
// send statuschanged without dates for an order that is still dated. Any other
// status, an order scheduling never received, and an event older than the
// order's last applied production event change nothing. Same principal and
// duty as the import.
func (a *API) withdrawScheduledOrder(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(DutyOrderImport) {
		return eventing.Permanent(errors.New("order intake needs a principal holding " + DutyOrderImport))
	}
	var data orderStatusChangedData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		return eventing.Permanent(fmt.Errorf("order.statuschanged data: %w", err))
	}
	if !ids.Valid(data.OrderID) {
		return eventing.Permanent(errors.New("order.statuschanged needs orderId"))
	}
	if !withdrawnStatuses[data.To] || data.From == data.To || data.StartDate != nil || data.EndDate != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, orderImportTimeout)
	defer cancel()
	res, withdrawn, err := a.store.WithdrawProductionOrder(ctx, store.OrderWithdrawal{OrderID: strings.ToLower(data.OrderID), To: data.To, EventID: evt.ID, EventTime: evt.Time})
	if err != nil {
		return fmt.Errorf("withdraw production order: %w", err)
	}
	if res.Stale {
		slog.InfoContext(ctx, "production order withdrawal ignored: a newer production event was applied", "orderId", res.OrderID, "to", data.To,
			"eventId", evt.ID, "tenantId", evt.TenantID)
	}
	if withdrawn {
		slog.InfoContext(ctx, "production order withdrawn", "orderId", res.OrderID, "to", data.To, "removed", res.Removed, "keptFirmed", res.KeptFirmed,
			"eventId", evt.ID, "tenantId", evt.TenantID)
	}
	return nil
}

// importScheduledOrder registers the operations of a dated production order.
// The principal must hold DutyOrderImport; any other principal (the downtime
// consumer's included) is refused. Operations that cannot be registered are
// recorded on the order's import row and never fail the event.
func (a *API) importScheduledOrder(ctx context.Context, _ pgx.Tx, evt events.Event) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(DutyOrderImport) {
		return eventing.Permanent(errors.New("order intake needs a principal holding " + DutyOrderImport))
	}
	var data orderScheduledData
	if err := json.Unmarshal(evt.Data, &data); err != nil {
		return eventing.Permanent(fmt.Errorf("order.scheduled data: %w", err))
	}
	if data.Reason == orderReasonFinite {
		// Production applied scheduling's own finite-capacity dates back onto
		// the order (wave 21). Importing them again would loop between the
		// two services, so the event is acknowledged and changes nothing.
		slog.InfoContext(ctx, "production order event ignored: it carries scheduling's own dates", "orderId", data.OrderID, "reason", data.Reason,
			"eventId", evt.ID, "tenantId", evt.TenantID)
		return nil
	}
	start, startErr := time.Parse(time.RFC3339Nano, data.Start)
	end, endErr := time.Parse(time.RFC3339Nano, data.End)
	if !ids.Valid(data.OrderID) || startErr != nil || endErr != nil || data.OrderVersion < 0 || (data.Reason != "schedule" && data.Reason != "redate") {
		return eventing.Permanent(errors.New("order.scheduled needs orderId, start, end, orderVersion and reason schedule or redate"))
	}
	order := domain.ProductionOrder{OrderID: strings.ToLower(data.OrderID), OrderNumber: data.OrderNumber, Start: start.UTC()}
	if data.DeliveryDate != nil {
		d, err := time.Parse("2006-01-02", *data.DeliveryDate)
		if err != nil {
			return eventing.Permanent(fmt.Errorf("order.scheduled deliveryDate: %w", err))
		}
		order.DeliveryDate = &d
	}
	for _, j := range data.Jobs {
		if j.ResourceID != "" && !ids.Valid(j.ResourceID) {
			return eventing.Permanent(fmt.Errorf("order.scheduled job resourceId %q is not a UUID", j.ResourceID))
		}
		order.Jobs = append(order.Jobs, domain.ProductionJob{OperNumber: j.OperNumber, Seq: j.Seq, JobType: j.JobType,
			ResourceID: strings.ToLower(j.ResourceID), Hours: j.PlannedHours})
	}
	ctx, cancel := context.WithTimeout(ctx, orderImportTimeout)
	defer cancel()
	res, err := a.store.ImportProductionOrder(ctx, store.OrderImportRequest{Order: order, End: end.UTC(), Version: data.OrderVersion, Reason: data.Reason, EventID: evt.ID, EventTime: evt.Time})
	if err != nil {
		return fmt.Errorf("import production order: %w", err)
	}
	slog.InfoContext(ctx, "production order imported", "orderId", res.OrderID, "status", res.Status, "stale", res.Stale, "created", res.Created,
		"updated", res.Updated, "removed", res.Removed, "skipped", len(res.Skipped), "eventId", evt.ID, "tenantId", evt.TenantID)
	return nil
}

// listOrderImports lists the production orders received from wave 21.
func (a *API) listOrderImports(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.OrderImportFilter{Limit: 50}
	if v := q.Get("status"); v != "" {
		if v != store.ImportImported && v != store.ImportPartial && v != store.ImportRejected && v != store.ImportWithdrawn {
			invalid(w, r, map[string]string{"status": "must be imported, partial, rejected or withdrawn"})
			return
		}
		f.Status = v
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxOrderImports {
			invalid(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", store.MaxOrderImports)})
			return
		}
		f.Limit = n
	}
	items, err := a.store.ListOrderImports(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getOrderImport answers what scheduling did with one production order.
func (a *API) getOrderImport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "orderId"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderImportNotFound, "no production order with that id has been received"))
		return
	}
	o, err := a.store.GetOrderImport(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// reimportOrder imports the latest received event of a production order again
// against the resources and mappings that exist now. It needs no body and is
// naturally idempotent: a second call finds every operation unchanged.
func (a *API) reimportOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "orderId"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderImportNotFound, "no production order with that id has been received"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), orderImportTimeout)
	defer cancel()
	o, err := a.store.ReimportProductionOrder(ctx, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}
