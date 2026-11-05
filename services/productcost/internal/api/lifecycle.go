package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

// The version lifecycle and standard cost conversion routes (slice 3) are
// stateless: the caller supplies the current version or checklist and the
// service applies the rule and returns the next state. Persistence, the
// outbox and the PRD's /versions/{id}/... paths arrive with the store.

// domainFailure maps a domain error to a problem: coded rule violations are
// 422 with their catalog code, anything else is a 400 on the given field.
func domainFailure(w http.ResponseWriter, r *http.Request, err error, field string) {
	var ce *domain.CodedError
	if errors.As(err, &ce) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, ce.Code, ce.Detail))
		return
	}
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = map[string]string{field: err.Error()}
	httpx.Fail(w, r, p)
}

type versionRequest struct {
	Version domain.Version `json:"version"`
	Action  string         `json:"action"`
}

type versionResponse struct {
	Version domain.Version `json:"version"`
}

// applyVersion validates the version and applies action to it.
func applyVersion(w http.ResponseWriter, r *http.Request, v domain.Version, action string, fields map[string]string) {
	if v.ID == "" {
		fields["version.id"] = "is required"
	}
	if !validation(w, r, fields) {
		return
	}
	if err := v.Validate(); err != nil {
		domainFailure(w, r, err, "version")
		return
	}
	next, err := domain.Transition(v, action)
	if err != nil {
		domainFailure(w, r, err, "action")
		return
	}
	httpx.JSON(w, http.StatusOK, versionResponse{Version: next})
}

// transitionVersion blocks, unblocks or closes a version (BR-39.2, duty
// productcost.maintain). Activation is a separate route under productcost.activate,
// because the two duties are segregated (section 7.39.17).
func (a *API) transitionVersion(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req versionRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	switch req.Action {
	case domain.ActionBlock, domain.ActionUnblock, domain.ActionClose:
	case domain.ActionActivate:
		fields["action"] = "activate with POST /v1/product-costing/versions:activate (duty productcost.activate)"
	default:
		fields["action"] = "must be block, unblock or close"
	}
	applyVersion(w, r, req.Version, req.Action, fields)
}

// activateVersion moves an Open or Blocked version to Active (BR-39.2, duty
// productcost.activate). Simulation versions cannot activate (CST-0711).
func (a *API) activateVersion(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req versionResponse
	if !decode(w, r, &req) {
		return
	}
	applyVersion(w, r, req.Version, domain.ActionActivate, map[string]string{})
}

// knownBlockers are the conversion blocker names of costing.conversionBlockers.
var knownBlockers = map[string]bool{
	domain.BlockNegativeOnHand: true,
	domain.BlockInTransit:      true,
	domain.BlockStartedOrders:  true,
}

// checkConversionInput checks the shape of a conversion request before the
// domain validates the rules. prefix is "" or "input.".
func checkConversionInput(in domain.ConversionInput, prefix string, fields map[string]string) {
	if in.Currency != "" && !currencyCode.MatchString(in.Currency) {
		fields[prefix+"currency"] = "must be an ISO 4217 code such as USD"
	}
	if _, err := time.Parse("2006-01-02", in.ConversionDate); err != nil {
		fields[prefix+"conversionDate"] = "must be a date YYYY-MM-DD"
	}
	if in.CloseThrough != "" {
		if _, err := time.Parse("2006-01-02", in.CloseThrough); err != nil {
			fields[prefix+"closeThrough"] = "must be a date YYYY-MM-DD"
		}
	}
	for i, b := range in.Blockers {
		if !knownBlockers[b] {
			fields[fmt.Sprintf("%sblockers[%d]", prefix, i)] = "must be NegativeOnHand, InTransit or StartedOrders"
		}
	}
	switch {
	case len(in.Positions) == 0:
		fields[prefix+"positions"] = "must contain at least one position"
	case len(in.Positions) > MaxRevalLines:
		fields[prefix+"positions"] = fmt.Sprintf("must contain at most %d positions", MaxRevalLines)
	}
	seen := make(map[string]bool, len(in.Positions))
	for i, p := range in.Positions {
		at := func(key string) string { return fmt.Sprintf("%spositions[%d].%s", prefix, i, key) }
		if p.ItemID == "" {
			fields[at("itemId")] = "is required"
		}
		if p.SiteID == "" {
			fields[at("siteId")] = "is required"
		}
		if key := p.ItemID + "\x00" + p.SiteID; seen[key] {
			fields[at("itemId")] = "item and site must be unique"
		} else {
			seen[key] = true
		}
		if p.StandardUnit.IsNegative() {
			fields[at("standardUnit")] = "must not be negative"
		}
		if p.InTransit < 0 {
			fields[at("inTransit")] = "must not be negative"
		}
		if p.StartedOrders < 0 {
			fields[at("startedOrders")] = "must not be negative"
		}
	}
}

func (a *API) validateConversion(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var in domain.ConversionInput
	if !decode(w, r, &in) {
		return
	}
	fields := map[string]string{}
	checkConversionInput(in, "", fields)
	if !validation(w, r, fields) {
		return
	}
	rep, err := domain.ValidateConversion(in)
	if err != nil {
		domainFailure(w, r, err, "conversionDate")
		return
	}
	a.previews.Add(1)
	httpx.JSON(w, http.StatusOK, rep)
}

type conversionAdvanceRequest struct {
	Run   domain.ConversionRun   `json:"run"`
	Step  string                 `json:"step"`
	Input domain.ConversionInput `json:"input"`
}

type conversionAdvanceResponse struct {
	Run    domain.ConversionRun    `json:"run"`
	Report domain.ConversionReport `json:"report"`
}

// advanceConversion records one checklist step (FR-39.12). The user is the
// calling principal and the time is the server's, so the checklist records
// who ran each step and when; neither is taken from the body.
func (a *API) advanceConversion(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "the checklist records the user who runs a step; no user in the request"))
		return
	}
	var req conversionAdvanceRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	checkConversionInput(req.Input, "input.", fields)
	known := false
	for _, s := range domain.ConversionSteps {
		if s == req.Step {
			known = true
		}
	}
	if !known {
		fields["step"] = "must be select, roll, validate, preview, close or activate"
	}
	if i := domain.CheckChecklist(req.Run); i >= 0 {
		fields[fmt.Sprintf("run.checklist[%d]", i)] = "must be the next checklist step in order, with status Done and a user"
	}
	if !validation(w, r, fields) {
		return
	}
	rep, err := domain.ValidateConversion(req.Input)
	if err != nil {
		domainFailure(w, r, err, "input.conversionDate")
		return
	}
	run, err := domain.Advance(req.Run, req.Step, p.UserID, time.Now().UTC(), rep)
	if err != nil {
		domainFailure(w, r, err, "step")
		return
	}
	httpx.JSON(w, http.StatusOK, conversionAdvanceResponse{Run: run, Report: rep})
}
