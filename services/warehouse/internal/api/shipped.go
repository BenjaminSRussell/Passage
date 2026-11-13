package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
)

const (
	codeShipmentNotFound = "WMS-SHIPMENT-NOT-FOUND"
	// codeShipmentShipped is a second confirmation of a shipment: a split load ships by packing slips in fulfillment.
	codeShipmentShipped = "WMS-SHIPMENT-SHIPPED"
	maxShippedSSCCs     = 500
	// shipClockSkew is how far in the future a ship time may be: a handheld's clock may run a little ahead.
	shipClockSkew = 15 * time.Minute
)

var carrierCode = regexp.MustCompile(`^[A-Z0-9]{2,4}$`)

func (a *API) registerShipped(r chi.Router) {
	r.With(authz.RequireDuty(dutyShipConfirm)).Post("/v1/warehouse/shipments/{id}:ship", a.confirmShipped)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/shipments/{id}/shipped", a.getShipped)
}

type shipInput struct {
	WarehouseID   string     `json:"warehouseId"`
	LegalEntityID string     `json:"legalEntityId"`
	CarrierCode   string     `json:"carrierCode"`
	ShippedAt     *time.Time `json:"shippedAt"`
	SSCCs         []string   `json:"ssccs"`
}

// confirmShipped records that a fulfillment shipment left the dock without a packing slip posted for it and publishes
// shipment.shipped (#35, 35-1), so the EDI engine still sends the shipment's 856 or DESADV. The legal entity defaults to the
// caller's and must be in its scope.
func (a *API) confirmShipped(w http.ResponseWriter, r *http.Request) {
	shipmentID := strings.ToLower(chi.URLParam(r, "id"))
	var in shipInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !ids.Valid(shipmentID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(in.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	le := strings.ToLower(strings.TrimSpace(in.LegalEntityID))
	if le == "" {
		le = p.LegalEntityID
	}
	if !ids.Valid(le) || !tenant.InLegalEntityScope(p, le) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if in.CarrierCode != "" && !carrierCode.MatchString(in.CarrierCode) {
		fields["carrierCode"] = "must be 2 to 4 upper-case letters or digits (the SCAC)"
	}
	now := time.Now().UTC()
	shippedAt := now
	if in.ShippedAt != nil {
		shippedAt = in.ShippedAt.UTC()
		if shippedAt.After(now.Add(shipClockSkew)) {
			fields["shippedAt"] = "must not be in the future"
		}
	}
	if len(in.SSCCs) > maxShippedSSCCs {
		fields["ssccs"] = fmt.Sprintf("must hold at most %d containers", maxShippedSSCCs)
	}
	seen := map[string]bool{}
	for i, s := range in.SSCCs {
		switch {
		case !domain.ValidSSCC(s):
			fields[fmt.Sprintf("ssccs[%d]", i)] = "must be an 18-digit SSCC with a valid check digit"
		case seen[s]:
			fields[fmt.Sprintf("ssccs[%d]", i)] = "is listed twice"
		}
		seen[s] = true
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.ConfirmShipped(r.Context(), idem, store.ShipConfirmation{ShipmentID: shipmentID, LegalEntityID: le,
		WarehouseID: strings.ToLower(in.WarehouseID), CarrierCode: in.CarrierCode, ShippedAt: shippedAt.Truncate(time.Second),
		SSCCs: in.SSCCs, ConfirmedBy: p.UserID})
	if errors.Is(err, store.ErrShipmentShipped) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeShipmentShipped,
			"the shipment was already confirmed shipped; a shipment that leaves in several loads is shipped by packing slips in fulfillment"))
		return
	}
	if err != nil {
		fail(w, r, err, codeShipmentNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, out)
}

func (a *API) getShipped(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeShipmentNotFound, "not found"))
		return
	}
	out, err := a.st.GetShipConfirmation(r.Context(), id)
	if err != nil {
		fail(w, r, err, codeShipmentNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
