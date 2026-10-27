package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

const (
	maxTemplateLines = 200
	maxMeasureValues = 5000
)

var measureCode = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,39}$`)

// registerTemplates mounts the statistical measure and template allocation
// routes (FR-38.17). Authors (controlling.maintain) save and dry-run templates;
// running a period needs controlling.run (PRD 7.38.17).
func (a *API) registerTemplates(r chi.Router) {
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/statistical-measures", a.createMeasure)
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/statistical-measures/{id}/values", a.loadMeasureValues)
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/allocation-templates", a.saveTemplate)
	r.With(authz.RequireDuty(DutyMaintain)).Post("/v1/controlling/allocation-templates/{id}:dry-run", a.dryRunTemplate)
	r.With(authz.RequireDuty(DutyRun)).Post("/v1/controlling/template-allocations/runs", a.runTemplate)
	view := r.With(authz.RequireDuty(DutyView))
	view.Get("/v1/controlling/statistical-measures", a.listMeasures)
	view.Get("/v1/controlling/statistical-measures/{id}/values", a.measureValues)
	view.Get("/v1/controlling/allocation-templates", a.listTemplates)
	view.Get("/v1/controlling/allocation-templates/{id}", a.getTemplate)
	view.Get("/v1/controlling/template-allocations/runs", a.listTemplateRuns)
	view.Get("/v1/controlling/template-allocations/runs/{id}", a.getTemplateRun)
}

type measureBody struct {
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Unit          string `json:"unit"`
}

func (a *API) createMeasure(w http.ResponseWriter, r *http.Request) {
	var b measureBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(b.LegalEntityID):
		invalid(w, r, "legalEntityId must be a UUID")
	case !measureCode.MatchString(b.Code):
		invalid(w, r, "code must start with a letter or _ and hold up to 40 letters, digits or _ (formulas reference m.<code>)")
	case strings.TrimSpace(b.Name) == "" || len(b.Name) > 200:
		invalid(w, r, "name is required and at most 200 characters")
	case strings.TrimSpace(b.Unit) == "" || len(b.Unit) > 20:
		invalid(w, r, "unit is required and at most 20 characters")
	default:
		status, raw, err := a.st.CreateMeasure(r.Context(), key, hash, store.StatisticalMeasure{LegalEntityID: b.LegalEntityID, Code: b.Code, Name: b.Name, Unit: b.Unit})
		writeCommand(w, r, status, raw, err)
	}
}

type measureValueBody struct {
	CostObjectID string          `json:"costObjectId"`
	Value        decimal.Decimal `json:"value"`
}

type measureValuesBody struct {
	LegalEntityID string             `json:"legalEntityId"`
	Period        string             `json:"period"`
	Values        []measureValueBody `json:"values"`
}

func (a *API) loadMeasureValues(w http.ResponseWriter, r *http.Request) {
	var b measureValuesBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	measureID := chi.URLParam(r, "id")
	switch {
	case !ids.Valid(measureID):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeMeasureNotFound, "statistical measure not found"))
		return
	case !ids.Valid(b.LegalEntityID):
		invalid(w, r, "legalEntityId must be a UUID")
		return
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
		return
	case len(b.Values) == 0 || len(b.Values) > maxMeasureValues:
		invalid(w, r, fmt.Sprintf("values must hold 1 to %d entries", maxMeasureValues))
		return
	}
	values := make([]store.MeasureValue, 0, len(b.Values))
	seen := make(map[string]bool, len(b.Values))
	for i, v := range b.Values {
		if !ids.Valid(v.CostObjectID) || seen[v.CostObjectID] {
			invalid(w, r, lineErr(i, "each value needs a distinct costObjectId UUID"))
			return
		}
		seen[v.CostObjectID] = true
		if v.Value.Sign() < 0 || v.Value.Exponent() < -4 {
			invalid(w, r, lineErr(i, "value must be zero or positive with at most 4 decimal places"))
			return
		}
		values = append(values, store.MeasureValue{CostObjectID: v.CostObjectID, Value: v.Value})
	}
	status, raw, err := a.st.LoadMeasureValues(r.Context(), key, hash, store.MeasureValuesRequest{
		MeasureID: measureID, LegalEntityID: b.LegalEntityID, Period: b.Period, Values: values,
	})
	writeCommand(w, r, status, raw, err)
}

// templateBody is the wire form of a template save. expectedVersion is the
// version the caller read (0 for a new code), so a concurrent edit is refused
// rather than overwritten.
type templateBody struct {
	LegalEntityID      string               `json:"legalEntityId"`
	Code               string               `json:"code"`
	Name               string               `json:"name"`
	Currency           string               `json:"currency"`
	SenderCostObjectID string               `json:"senderCostObjectId"`
	AllowNegative      bool                 `json:"allowNegative"`
	Lines              []store.TemplateLine `json:"lines"`
	ExpectedVersion    int                  `json:"expectedVersion"`
}

func (a *API) saveTemplate(w http.ResponseWriter, r *http.Request) {
	var b templateBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	if msg := validateTemplate(b); msg != "" {
		invalid(w, r, msg)
		return
	}
	// Formulas are parsed at save (FR-38.17); references are checked by the store.
	for i, l := range b.Lines {
		if _, err := domain.ParseFormula(l.Formula); err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeFormulaInvalid,
				fmt.Sprintf("Formula error in template %s line %d: %s", b.Code, i+1, err.Error())))
			return
		}
	}
	in := store.AllocationTemplate{LegalEntityID: b.LegalEntityID, Code: b.Code, Name: b.Name, Currency: b.Currency,
		SenderCostObjectID: b.SenderCostObjectID, AllowNegative: b.AllowNegative, Lines: b.Lines}
	status, raw, err := a.st.SaveTemplate(r.Context(), key, hash, in, b.ExpectedVersion)
	writeCommand(w, r, status, raw, err)
}

// validateTemplate checks the shape of a template; it returns "" when valid.
func validateTemplate(b templateBody) string {
	switch {
	case !ids.Valid(b.LegalEntityID) || !ids.Valid(b.SenderCostObjectID):
		return "legalEntityId and senderCostObjectId must be UUIDs"
	case !domain.ValidCode(b.Code):
		return "code must be 2 to 40 characters of A-Z, 0-9, _ or -"
	case strings.TrimSpace(b.Name) == "" || len(b.Name) > 200:
		return "name is required and at most 200 characters"
	case !validCurrency(b.Currency):
		return "currency must be a 3-letter ISO code"
	case b.ExpectedVersion < 0:
		return "expectedVersion must be 0 for a new template or the version last read"
	case len(b.Lines) < 1 || len(b.Lines) > maxTemplateLines:
		return fmt.Sprintf("lines must hold 1 to %d entries", maxTemplateLines)
	}
	for i, l := range b.Lines {
		switch {
		case !ids.Valid(l.ReceiverCostObjectID) || !ids.Valid(l.SecondaryElementID):
			return lineErr(i, "receiverCostObjectId and secondaryElementId must be UUIDs")
		case l.ReceiverCostObjectID == b.SenderCostObjectID:
			return lineErr(i, "the receiver must differ from the sender")
		case strings.TrimSpace(l.Formula) == "":
			return lineErr(i, "formula is required")
		}
	}
	return ""
}

type templateRunBody struct {
	TemplateID string `json:"templateId"`
	Period     string `json:"period"`
}

func (a *API) runTemplate(w http.ResponseWriter, r *http.Request) {
	var b templateRunBody
	key, hash, ok := prepare(w, r, &b)
	if !ok {
		return
	}
	switch {
	case !ids.Valid(b.TemplateID):
		invalid(w, r, "templateId must be a UUID")
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
	default:
		status, raw, err := a.st.RunTemplate(r.Context(), key, hash, store.TemplateRunRequest{TemplateID: b.TemplateID, Period: b.Period})
		writeCommand(w, r, status, raw, err)
	}
}

type dryRunBody struct {
	Period string `json:"period"`
}

// dryRunTemplate evaluates a template for a period without storing anything, so
// it takes no Idempotency-Key.
func (a *API) dryRunTemplate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var b dryRunBody
	if _, ok := readJSON(w, r, &b); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	switch {
	case !ids.Valid(id):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTemplateNotFound, "allocation template not found"))
	case !domain.ValidPeriod(b.Period):
		invalid(w, r, "period must be YYYY-MM")
	default:
		res, err := a.st.DryRunTemplate(r.Context(), store.TemplateRunRequest{TemplateID: id, Period: b.Period})
		one(w, r, res, err)
	}
}

func (a *API) listMeasures(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListMeasures(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) measureValues(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeMeasureNotFound, "statistical measure not found"))
		return
	}
	period := r.URL.Query().Get("period")
	if !domain.ValidPeriod(period) {
		invalid(w, r, "period is required as YYYY-MM")
		return
	}
	rows, err := a.st.MeasureValues(r.Context(), id, period)
	list(w, r, rows, err)
}

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	rows, err := a.st.ListTemplates(r.Context(), le)
	list(w, r, rows, err)
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTemplateNotFound, "allocation template not found"))
		return
	}
	t, err := a.st.GetTemplate(r.Context(), id)
	one(w, r, t, err)
}

func (a *API) listTemplateRuns(w http.ResponseWriter, r *http.Request) {
	le, ok := legalEntityFilter(w, r)
	if !ok {
		return
	}
	tpl := r.URL.Query().Get("templateId")
	if tpl != "" && !ids.Valid(tpl) {
		invalid(w, r, "templateId must be a UUID")
		return
	}
	rows, err := a.st.ListTemplateRuns(r.Context(), le, tpl)
	list(w, r, rows, err)
}

func (a *API) getTemplateRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTemplateRunNotFound, "template allocation run not found"))
		return
	}
	run, err := a.st.GetTemplateRun(r.Context(), id)
	one(w, r, run, err)
}
