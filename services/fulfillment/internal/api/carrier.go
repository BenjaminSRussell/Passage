package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/carrier"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// Carrier-gateway error codes (#482).
const (
	CodeCarrierUnconfigured = "FUL-0070"
	CodeCarrierRefused      = "FUL-0071"
	CodeCarrierUnavailable  = "FUL-0072"
	CodeCarrierShipment     = "FUL-0073"
)

// labelKey is the label's Idempotency-Key: carrier-gateway takes 128 visible ASCII characters, and the shipment id
// and a colon come first.
var labelKey = regexp.MustCompile(`^[\x21-\x7e]{1,64}$`)

// routesCarrier mounts rate shopping, label purchase and the tracking pull through carrier-gateway (wave 28 D3).
func (a *API) routesCarrier(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.ship")).Post("/v1/fulfillment/shipments/{id}:carrier-rates", a.carrierRates)
	r.With(authz.RequireDuty("fulfillment.warehouse.ship")).Post("/v1/fulfillment/shipments/{id}:carrier-label", a.carrierLabel)
	r.With(authz.RequireDuty("fulfillment.warehouse.ship")).Post("/v1/fulfillment/shipments/{id}:carrier-track", a.carrierTrack)
}

type carrierRatesRequest struct {
	Carriers []string `json:"carriers"`
	Services []string `json:"services"`
	carrier.Shipment
}

type carrierLabelRequest struct {
	Carrier   string `json:"carrier"`
	Service   string `json:"service"`
	Reference string `json:"reference"`
	carrier.Shipment
}

type carrierTrackRequest struct {
	Carrier        string `json:"carrier"`
	TrackingNumber string `json:"trackingNumber"`
}

type carrierLabelResponse struct {
	carrier.Label
	TrackingEvent store.TrackingEvent `json:"trackingEvent"`
}

type carrierTrackResponse struct {
	Carrier        string                `json:"carrier"`
	TrackingNumber string                `json:"trackingNumber"`
	Status         string                `json:"status"`
	Recorded       int                   `json:"recorded"`
	Events         []store.TrackingEvent `json:"events"`
}

// carrierShipment loads the shipment a carrier call is for; a cancelled shipment ships nothing.
func (a *API) carrierShipment(w http.ResponseWriter, r *http.Request) (store.Shipment, bool) {
	if a.opts.Carrier == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeCarrierUnconfigured, "carrier-gateway is not configured (CARRIER_GATEWAY_URL)"))
		return store.Shipment{}, false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShipmentNotFound, "shipment not found"))
		return store.Shipment{}, false
	}
	sh, err := a.store.GetShipment(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return store.Shipment{}, false
	}
	if sh.Status == domain.StatusCancelled {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeCarrierShipment, "the shipment is cancelled"))
		return store.Shipment{}, false
	}
	return sh, true
}

// carrierFail maps a gateway refusal: its field errors and not-found or unprocessable answers pass through under
// FUL-0071; anything else (an authentication or configuration fault between the services, an outage) is 502 or 503.
func carrierFail(w http.ResponseWriter, r *http.Request, err error) {
	var ge *carrier.Error
	switch {
	case errors.As(err, &ge) && ge.Status == http.StatusBadRequest:
		p := httpx.NewProblem(http.StatusBadRequest, CodeValidation, validationDetail)
		p.Errors = ge.Errors
		httpx.Fail(w, r, p)
	case errors.As(err, &ge) && (ge.Status == http.StatusNotFound || ge.Status == http.StatusUnprocessableEntity):
		httpx.Fail(w, r, httpx.NewProblem(ge.Status, CodeCarrierRefused, "carrier-gateway "+ge.Code+": "+ge.Detail))
	case errors.As(err, &ge):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeCarrierUnavailable, "carrier-gateway refused the call ("+ge.Code+")"))
	case errors.Is(err, carrier.ErrUnavailable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeCarrierUnavailable, "carrier-gateway is unavailable; retry"))
	default:
		httpx.Fail(w, r, err)
	}
}

// carrierRates shops rates for the shipment's parcels across the configured carriers, cheapest first.
func (a *API) carrierRates(w http.ResponseWriter, r *http.Request) {
	var in carrierRatesRequest
	if err := decodeQuery(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sh, ok := a.carrierShipment(w, r)
	if !ok {
		return
	}
	rates, err := a.opts.Carrier.Rates(r.Context(), sh.LegalEntityID, carrier.RateQuery{Carriers: in.Carriers, Services: in.Services, Shipment: in.Shipment})
	if err != nil {
		carrierFail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rates)
}

// carrierLabel buys the carrier label for the shipment and records it as a LABELLED tracking event. The
// Idempotency-Key names the label: a retry returns the same label and records nothing twice.
func (a *API) carrierLabel(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(headerIdempotency)
	if !labelKey.MatchString(key) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeIdemMissing, "Idempotency-Key header is required (1 to 64 visible ASCII characters)"))
		return
	}
	var in carrierLabelRequest
	if err := decodeQuery(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sh, ok := a.carrierShipment(w, r)
	if !ok {
		return
	}
	code := in.Carrier
	if code == "" && sh.CarrierCode != nil {
		code = *sh.CarrierCode
	}
	if !providerRE.MatchString(code) {
		fail(w, r, "carrier", "is required when the shipment has no carrier: 2 to 32 lowercase letters, digits or hyphens")
		return
	}
	ref := in.Reference
	if ref == "" {
		ref = sh.ShipmentNumber
	}
	label, err := a.opts.Carrier.Label(r.Context(), sh.LegalEntityID, sh.ID+":"+key, carrier.LabelOrder{Carrier: code, Service: in.Service, Reference: ref, Shipment: in.Shipment})
	if err != nil {
		carrierFail(w, r, err)
		return
	}
	ev, _, err := a.store.RecordTracking(r.Context(), sh.ID, store.TrackingEvent{Provider: label.Carrier, EventKey: "label:" + label.TrackingNumber,
		TrackingNumber: label.TrackingNumber, Status: "LABELLED", Description: "Label " + label.Service + " bought through carrier-gateway", EventTime: label.CreatedAt.UTC()})
	if err != nil {
		// The label is bought; a retry with the same key gets it back and records the event then.
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if label.Created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, carrierLabelResponse{Label: label, TrackingEvent: ev})
}

// carrierTrack pulls the carrier's tracking for the shipment and records every event it has not stored yet (the
// provider event key keeps it idempotent; DELIVERED moves a SHIPPED shipment to DELIVERED). Without a tracking
// number it tracks the shipment's newest one.
func (a *API) carrierTrack(w http.ResponseWriter, r *http.Request) {
	var in carrierTrackRequest
	if err := decodeQuery(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sh, ok := a.carrierShipment(w, r)
	if !ok {
		return
	}
	if in.TrackingNumber == "" {
		events, err := a.store.ListTracking(r.Context(), sh.ID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		for _, ev := range events {
			if in.Carrier == "" || ev.Provider == in.Carrier {
				in.Carrier, in.TrackingNumber = ev.Provider, ev.TrackingNumber
				break
			}
		}
		if in.TrackingNumber == "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeCarrierShipment, "the shipment has no tracking number yet; buy a label or name one"))
			return
		}
	}
	if !providerRE.MatchString(in.Carrier) {
		fail(w, r, "carrier", "is required with trackingNumber: 2 to 32 lowercase letters, digits or hyphens")
		return
	}
	t, err := a.opts.Carrier.Track(r.Context(), sh.LegalEntityID, in.Carrier, in.TrackingNumber)
	if err != nil {
		carrierFail(w, r, err)
		return
	}
	out := carrierTrackResponse{Carrier: t.Carrier, TrackingNumber: t.TrackingNumber, Status: t.Status, Events: []store.TrackingEvent{}}
	for _, ev := range t.Events {
		if !trackingStatus[ev.Status] || ev.EventKey == "" || len(ev.EventKey) > 128 {
			continue
		}
		desc := ev.Description
		if len(desc) > 500 {
			desc = desc[:500]
		}
		stored, created, err := a.store.RecordTracking(r.Context(), sh.ID, store.TrackingEvent{Provider: in.Carrier, EventKey: ev.EventKey,
			TrackingNumber: t.TrackingNumber, Status: ev.Status, Description: desc, EventTime: ev.EventTime.UTC()})
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if created {
			out.Recorded++
		}
		out.Events = append(out.Events, stored)
	}
	httpx.JSON(w, http.StatusOK, out)
}
