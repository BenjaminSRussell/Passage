package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

type routingRuleRequest struct {
	DocumentType string `json:"documentType"`
	UserID       string `json:"userId,omitempty"`
	Zone         string `json:"zone,omitempty"`
	WarehouseID  string `json:"warehouseId,omitempty"`
	QueueCode    string `json:"queueCode"`
	Sequence     int    `json:"sequence"`
}

// createRoutingRule adds a document routing rule (FR-32.13). Idempotency-Key is required.
func (a *API) createRoutingRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req routingRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := domain.RoutingRule{DocumentType: req.DocumentType, UserID: req.UserID, Zone: req.Zone, WarehouseID: req.WarehouseID,
		QueueCode: req.QueueCode, Sequence: req.Sequence}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, _, err := a.store.CreateRoutingRule(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

// listRoutingRules returns the tenant's rules in resolution order.
func (a *API) listRoutingRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListRoutingRules(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []domain.RoutingRule `json:"items"`
	}{Items: items})
}
