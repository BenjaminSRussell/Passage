package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/internal/upstream"
)

const (
	dutyPlanProduction = "sales.order.plan-production"

	codeConfigurationInvalid     = "SLS-0047"
	codeConfigurationUnavailable = "SLS-0048"
	codeNotPlannable             = "SLS-0049"
	codeProductionUnavailable    = "SLS-0050"
	codeProductionConflict       = "SLS-0051"
)

// ConfigurationChecker confirms a saved configuration exists, is valid and belongs
// to the product (the product service owns configurations).
type ConfigurationChecker interface {
	Check(ctx context.Context, legalEntityID, configurationID, productID string) error
}

// LinePlanner asks production to build the order of one configured line, once per line.
type LinePlanner interface {
	CreateForLine(ctx context.Context, in upstream.ProductionRequest) (orderID string, created bool, err error)
}

// WithConfigurations sets the checker that validates the configurationId of order lines.
func (a *API) WithConfigurations(c ConfigurationChecker) *API {
	a.configs = c
	return a
}

// WithProduction sets the client that plans production orders for configured lines.
func (a *API) WithProduction(p LinePlanner) *API {
	a.production = p
	return a
}

// checkConfigurations refuses an order whose configured line names a configuration
// the product service does not confirm for that line's product. Each pair is asked once.
func (a *API) checkConfigurations(ctx context.Context, legalEntityID string, lines []store.Line) error {
	checked := map[[2]string]bool{}
	for _, l := range lines {
		if l.ConfigurationID == nil {
			continue
		}
		key := [2]string{*l.ConfigurationID, l.ProductID}
		if checked[key] {
			continue
		}
		if a.configs == nil {
			return httpx.NewProblem(http.StatusServiceUnavailable, codeConfigurationUnavailable, "configurations cannot be validated: the product service is not configured")
		}
		err := a.configs.Check(ctx, legalEntityID, *l.ConfigurationID, l.ProductID)
		switch {
		case errors.Is(err, upstream.ErrConfigurationInvalid):
			return httpx.NewProblem(http.StatusUnprocessableEntity, codeConfigurationInvalid, "line "+strconv.Itoa(l.LineNo)+": the configuration does not exist, is not valid or does not belong to the product")
		case err != nil:
			slog.ErrorContext(ctx, "configuration check failed", "err", err, "configurationId", *l.ConfigurationID, "correlationId", middleware.GetReqID(ctx))
			return httpx.NewProblem(http.StatusServiceUnavailable, codeConfigurationUnavailable, "the product service could not confirm the configuration; retry")
		}
		checked[key] = true
	}
	return nil
}

// PlannedLine is the outcome of :plan-production for one configured line.
type PlannedLine struct {
	LineID            string `json:"lineId"`
	LineNo            int    `json:"lineNo"`
	ConfigurationID   string `json:"configurationId"`
	ProductionOrderID string `json:"productionOrderId"`
	Created           bool   `json:"created"`
}

// planProduction creates, once per line, the production order of every configured
// line of a CONFIRMED order and links it to the line. A repeat plans nothing twice:
// linked lines are reported as they are and production answers the same order for
// a line it already built.
func (a *API) planProduction(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		p, _ := tenant.FromContext(r.Context())
		order, err := a.store.GetOrder(r.Context(), id)
		if err != nil {
			a.fail(w, r, "load order for production", err)
			return
		}
		if order.Status != domain.OrderConfirmed {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeNotPlannable, "order is "+order.Status+"; only a confirmed order is planned for production"))
			return
		}
		out := []PlannedLine{}
		for _, l := range order.Lines {
			if l.ConfigurationID == nil || l.LineStatus == "CANCELLED" {
				continue
			}
			planned := PlannedLine{LineID: l.ID, LineNo: l.LineNo, ConfigurationID: *l.ConfigurationID}
			if l.ProductionOrderID != nil {
				planned.ProductionOrderID = *l.ProductionOrderID
				out = append(out, planned)
				continue
			}
			if a.production == nil {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeProductionUnavailable, "the production service is not configured"))
				return
			}
			date := l.RequestedShipDate
			if l.ConfirmedShipDate != nil {
				date = *l.ConfirmedShipDate
			}
			productionID, created, err := a.production.CreateForLine(r.Context(), upstream.ProductionRequest{LegalEntityID: order.LegalEntityID, ProductID: l.ProductID,
				SiteID: l.SiteID, WarehouseID: l.WarehouseID, Currency: order.Currency, Quantity: l.Quantity.String(), DeliveryDate: date,
				OriginID: l.ID, ConfigurationID: *l.ConfigurationID})
			if errors.Is(err, upstream.ErrOriginConflict) {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeProductionConflict, "line "+strconv.Itoa(l.LineNo)+" already has a production order for another product or configuration"))
				return
			}
			if err != nil {
				a.failed.Add(1)
				slog.ErrorContext(r.Context(), "plan production failed", "err", err, "orderId", order.ID, "lineId", l.ID, "correlationId", middleware.GetReqID(r.Context()))
				httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, codeProductionUnavailable, "the production service could not plan line "+strconv.Itoa(l.LineNo)+"; lines planned so far are kept, retry"))
				return
			}
			if err := a.store.RecordLineProduction(r.Context(), store.LineProduction{OrderID: order.ID, LineID: l.ID, ConfigurationID: *l.ConfigurationID,
				ProductionOrderID: productionID, Actor: p.UserID}); err != nil {
				a.fail(w, r, "record line production", err)
				return
			}
			if created {
				a.planned.Add(1)
			}
			planned.ProductionOrderID, planned.Created = productionID, created
			out = append(out, planned)
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"orderId": order.ID, "lines": out})
	})(w, r)
}
