package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

var loadStatuses = domain.LoadStatuses

type loadBuildRequest struct {
	Capacity  domain.Capacity       `json:"capacity"`
	Shipments []domain.LoadShipment `json:"shipments"`
	// LegalEntityID owns the loads; required for callers scoped to several entities or all of them.
	LegalEntityID string `json:"legalEntityId"`
}

// loadDetail is a load with its DG checks, newest first.
type loadDetail struct {
	domain.Load
	DGChecks []domain.DGCheck `json:"dgChecks"`
}

type screenRequest struct {
	CrossBorder bool                    `json:"crossBorder"`
	Parties     []domain.ScreeningParty `json:"parties"`
}

type screenResponse struct {
	LoadID      string `json:"loadId"`
	CrossBorder bool   `json:"crossBorder"`
	// HoldID names the screening hold a HOLD decision opened or refreshed.
	HoldID string `json:"holdId,omitempty"`
	domain.ScreeningDecision
}

type decisionRequest struct {
	Reason string `json:"reason"`
}

// buildLoads consolidates shipments into loads by rule (FR-34.1, BR-34.8).
func (a *API) buildLoads(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req loadBuildRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for i := range req.Shipments {
		s := &req.Shipments[i]
		s.ShipToRegion = strings.ToUpper(strings.TrimSpace(s.ShipToRegion))
		s.Mode = strings.ToUpper(strings.TrimSpace(s.Mode))
	}
	if fields := domain.ValidateLoadBuild(req.Shipments, req.Capacity); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	loads, err := domain.BuildLoads(req.Shipments, req.Capacity)
	var tooLarge *domain.TooLargeError
	if errors.As(err, &tooLarge) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeShipmentTooLarge,
			"shipment "+tooLarge.ShipmentID+" exceeds the load capacity on its own; raise the capacity or plan it by hand"))
		return
	}
	if err != nil {
		a.fail(w, r, "build loads failed", err)
		return
	}
	for i := range loads {
		loads[i].LegalEntityID = req.LegalEntityID
	}
	created, err := a.store.BuildLoads(r.Context(), loads, idem)
	if err != nil {
		a.fail(w, r, "store loads failed", err)
		return
	}
	a.loadsBuilt.Add(int64(len(created)))
	httpx.JSON(w, http.StatusCreated, map[string]any{"items": created})
}

func (a *API) listLoads(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f := store.LoadFilter{Status: r.URL.Query().Get("status")}
	if f.Status != "" && !slices.Contains(loadStatuses, f.Status) {
		validationFailed(w, r, map[string]string{"status": "must be one of " + strings.Join(loadStatuses, ", ")})
		return
	}
	loads, err := a.store.ListLoads(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list loads failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": loads})
}

func (a *API) getLoad(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("load"))
		return
	}
	load, err := a.store.GetLoad(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get load failed", err)
		return
	}
	checks, err := a.store.ListDGChecks(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list dg checks failed", err)
		return
	}
	if checks == nil {
		checks = []domain.DGCheck{}
	}
	// The release takes this version as If-Match.
	w.Header().Set("ETag", `"`+strconv.Itoa(load.Version)+`"`)
	httpx.JSON(w, http.StatusOK, loadDetail{Load: load, DGChecks: checks})
}

// checkLoadDG runs the transport DG check on a load and stores the result
// (FR-34.15, BR-34.15). The lines come in the request until the wave-31
// HazmatProfile lookup is wired.
func (a *API) checkLoadDG(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("load"))
		return
	}
	var req domain.DGRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Regulation = strings.ToUpper(strings.TrimSpace(req.Regulation))
	req.EmergencyResponsePhone = strings.ReplaceAll(strings.TrimSpace(req.EmergencyResponsePhone), " ", "")
	for i := range req.Lines {
		l := &req.Lines[i]
		l.UNNumber = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(l.UNNumber), " ", ""))
		l.Class = strings.ToUpper(strings.TrimSpace(l.Class))
		l.PackingGroup = strings.ToUpper(strings.TrimSpace(l.PackingGroup))
		l.ProperShippingName = strings.TrimSpace(l.ProperShippingName)
	}
	if fields := domain.ValidateDGRequest(req); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	result, points, findings := domain.CheckDG(req)
	check, err := a.store.RecordDGCheck(r.Context(), domain.DGCheck{
		LoadID:         id,
		Regulation:     req.Regulation,
		Result:         result,
		ADRPoints:      points,
		Findings:       findings,
		ShippingPapers: domain.ShippingPapers(req),
		CheckedAt:      a.now(),
	})
	if err != nil {
		a.fail(w, r, "record dg check failed", err)
		return
	}
	a.dgChecks.Add(1)
	httpx.JSON(w, http.StatusOK, check)
}

// screenLoad screens the parties of a load through the ComplianceScreeningProvider
// hook (FR-34.19, BR-34.17). A HOLD decision holds the load: the hold is stored
// (or refreshed when one is open) and blocks tendering until a
// transport.screening-hold.release user releases it. CLEAR and WARN are returned, not
// stored, and never clear an open hold.
func (a *API) screenLoad(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("load"))
		return
	}
	var req screenRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for i := range req.Parties {
		p := &req.Parties[i]
		p.Role = strings.ToUpper(strings.TrimSpace(p.Role))
		p.Name = strings.TrimSpace(p.Name)
		p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	}
	if fields := domain.ValidateScreeningParties(req.Parties); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if _, err := a.store.GetLoad(r.Context(), id); err != nil {
		a.fail(w, r, "load lookup for screening failed", err)
		return
	}
	cross := req.CrossBorder || domain.CrossBorder(req.Parties)
	decision := domain.ScreenParties(r.Context(), a.screening, req.Parties, cross)
	a.screenings.Add(1)
	resp := screenResponse{LoadID: id, CrossBorder: cross, ScreeningDecision: decision}
	if decision.Decision == domain.DecisionHold {
		hold, err := a.store.RecordScreeningHold(r.Context(), id, decision.Parties)
		if err != nil {
			a.fail(w, r, "record screening hold failed", err)
			return
		}
		a.holdsOpened.Add(1)
		resp.HoldID = hold.ID
	}
	httpx.JSON(w, http.StatusOK, resp)
}

func (a *API) approveFreightBill(w http.ResponseWriter, r *http.Request) {
	a.decideFreightBill(w, r, domain.ActionApprove)
}

func (a *API) disputeFreightBill(w http.ResponseWriter, r *http.Request) {
	a.decideFreightBill(w, r, domain.ActionDispute)
}

// decideFreightBill approves or disputes a bill (BR-34.4). Both need If-Match
// with the bill's version; an Idempotency-Key makes a retry safe.
func (a *API) decideFreightBill(w http.ResponseWriter, r *http.Request, action string) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("freight bill"))
		return
	}
	version, err := ifMatch(r, "freight bill")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req decisionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	switch {
	case len(reason) > domain.MaxReasonLength:
		validationFailed(w, r, map[string]string{"reason": "must be at most 500 characters"})
		return
	case action == domain.ActionDispute && reason == "":
		validationFailed(w, r, map[string]string{"reason": "is required: tell the carrier what is disputed"})
		return
	}
	bill, err := a.store.DecideFreightBill(r.Context(), id, action, reason, version, idem, a.now())
	if err != nil {
		a.fail(w, r, strings.ToLower(action)+" freight bill failed", err)
		return
	}
	if action == domain.ActionApprove {
		a.billsApproved.Add(1)
	} else {
		a.billsDisputed.Add(1)
	}
	w.Header().Set("ETag", `"`+strconv.Itoa(bill.Version)+`"`)
	httpx.JSON(w, http.StatusOK, bill)
}

// ifMatch reads the required If-Match header with the version of the named
// record (428 when absent), as in receivables.
func ifMatch(r *http.Request, record string) (int, error) {
	raw := strings.Trim(r.Header.Get("If-Match"), `W/" `)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, "COM-0428", "If-Match header with the "+record+" version is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		validation := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		validation.Errors = map[string]string{"If-Match": "must be the " + record + " version, such as \"2\""}
		return 0, validation
	}
	return v, nil
}

// transferRule returns the default goods-in-transit transfer event for an
// Incoterms 2020 rule (BR-34.11): the origin rules open a goods-in-transit order
// at origin shipment (TMS-0121), the delivered rules transfer at receipt.
func (a *API) transferRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	rule, err := domain.TransferFor(chi.URLParam(r, "incoterm"))
	if errors.Is(err, domain.ErrUnknownIncoterm) {
		validationFailed(w, r, map[string]string{"incoterm": "must be an Incoterms 2020 rule: EXW, FCA, FAS, FOB, CFR, CIF, CPT, CIP, DAP, DPU or DDP"})
		return
	}
	if err != nil {
		a.fail(w, r, "transfer rule failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, rule)
}
