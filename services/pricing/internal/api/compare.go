package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// compareRequest prices one request twice, once under each procedure version (FR-26.13).
// Records is the inline record set a simulation takes; when it is absent the legal entity's
// ACTIVE records as of the pricing date are used, as resolve does.
type compareRequest struct {
	resolveRequest
	Records    []recordDTO `json:"records"`
	ProcedureA string      `json:"procedureIdA"`
	ProcedureB string      `json:"procedureIdB"`
}

type compareResponse struct {
	A          resolveResponse   `json:"a"`
	B          resolveResponse   `json:"b"`
	Comparison domain.Comparison `json:"comparison"`
}

// compare prices a tuple under two stored procedure versions, DRAFT included, and reports the
// differences. It stores nothing; both resolutions are pure functions of the same records.
func (a *API) compare(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req compareRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// The pricing date follows the legal entity's basis as on resolve; the defaults apply until
	// the legal entity is known to be a UUID (the fault is reported below).
	params := store.DefaultParameters(req.LegalEntityID)
	if ids.Valid(req.LegalEntityID) {
		var err error
		if params, err = a.entityParameters(r, req.LegalEntityID); err != nil {
			slog.Error("get pricing parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
	}
	header, lines, problem := a.documentInput(req.resolveRequest, params)
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProcedureA) {
		fields["procedureIdA"] = "must be a UUID"
	}
	if !ids.Valid(req.ProcedureB) {
		fields["procedureIdB"] = "must be a UUID"
	}
	if req.ProcedureA != "" && req.ProcedureA == req.ProcedureB {
		fields["procedureIdB"] = "must name a different version than procedureIdA"
	}
	// The versions are named explicitly, so the fields that choose a procedure for resolve would be
	// silently ignored here; reject them instead.
	if req.Procedure != "" {
		fields["procedure"] = "is not used by compare; name the versions with procedureIdA and procedureIdB"
	}
	if req.SalesChannel != "" {
		fields["salesChannel"] = "is not used by compare; name the versions with procedureIdA and procedureIdB"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// Inline records are checked before any read so an oversized or invalid body costs no queries.
	var inline []domain.Record
	if req.Records != nil {
		var p *httpx.Problem
		if inline, p = inlineRecords(req.Records); p != nil {
			httpx.Fail(w, r, p)
			return
		}
	}
	procs := [2]*domain.Procedure{}
	for i, id := range []string{req.ProcedureA, req.ProcedureB} {
		p, err := a.store.GetProcedure(r.Context(), id)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if p.LegalEntityID != req.LegalEntityID {
			validationFailed(w, r, map[string]string{[]string{"procedureIdA", "procedureIdB"}[i]: "belongs to another legal entity"})
			return
		}
		procs[i] = &p
	}
	in := domain.Input{Header: header, Lines: lines, SmallOrderThreshold: domain.ThresholdFor(params.SmallOrderThresholds, header.Currency),
		TotalDiscountBasis: params.TotalDiscountBasis}
	if req.Records != nil {
		in.Records = inline
	} else {
		loaded, err := a.resolveInput(r.Context(), store.ActiveQuery{LegalEntityID: req.LegalEntityID, Currency: req.Currency,
			CustomerGroupID: header.CustomerGroupID, AsOf: header.PricingDate})
		if err != nil {
			slog.Error("load active records failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
		in.Set, in.Records = loaded.Set, loaded.Records
	}
	explain := r.URL.Query().Get("explain") != "false"
	var results [2]domain.Result
	var views [2]resolveResponse
	for i, p := range procs {
		pin := in
		pin.Procedure = p
		results[i] = domain.Resolve(pin)
		var err error
		if views[i], err = a.responseOf(results[i], nil, explain); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, compareResponse{A: views[0], B: views[1], Comparison: domain.Compare(results[0], results[1])})
}
