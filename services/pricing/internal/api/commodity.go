package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	basesPath       = "/v1/pricing/commodity/bases"
	basisPath       = "/v1/pricing/commodity/bases/{id}"
	indexValuesPath = "/v1/pricing/commodity/bases/{id}/index-values"
	calcsPath       = "/v1/pricing/commodity/calculations"
	calcPath        = "/v1/pricing/commodity/calculations/{id}"
	calcWithdraw    = "/v1/pricing/commodity/calculations/{id}:withdraw"
	calcPreview     = "/v1/pricing/commodity/calculations/{id}:preview"
	calcRun         = "/v1/pricing/commodity/calculations/{id}:run"
	calcRunsPath    = "/v1/pricing/commodity/calculations/{id}/runs"
)

// registerCommodityRoutes mounts commodity pricing (FR-26.21). PricingMaintain keeps the cost
// basis types, index values and calculations and starts a run (which only creates a
// VALIDATED journal); PricingJournalPost, a different duty, posts that journal, and the user
// who ran it cannot (PRC-0056 in the store). Reads and the read-only preview take
// PricingView. The PRD's CommodityPriceRun duty is not seeded in the policy bundle yet, so
// PricingMaintain stands in for it.
func (a *API) registerCommodityRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyMaintain)).Post(basesPath, a.createBasis)
	r.With(authz.RequireDuty(dutyView)).Get(basesPath, a.listBases)
	r.With(authz.RequireDuty(dutyView)).Get(basisPath, a.getBasis)
	r.With(authz.RequireDuty(dutyMaintain)).Post(indexValuesPath, a.recordIndex)
	r.With(authz.RequireDuty(dutyView)).Get(indexValuesPath, a.listIndex)
	r.With(authz.RequireDuty(dutyMaintain)).Post(calcsPath, a.createCalc)
	r.With(authz.RequireDuty(dutyView)).Get(calcsPath, a.listCalcs)
	r.With(authz.RequireDuty(dutyView)).Get(calcPath, a.getCalc)
	r.With(authz.RequireDuty(dutyMaintain)).Post(calcWithdraw, a.withdrawCalc)
	r.With(authz.RequireDuty(dutyView)).Post(calcPreview, a.previewRun)
	r.With(authz.RequireDuty(dutyMaintain)).Post(calcRun, a.startRun)
	r.With(authz.RequireDuty(dutyView)).Get(calcRunsPath, a.listRuns)
}

type basisRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
}

type basisView struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
	Version       int    `json:"version"`
}

func basisResponse(b domain.CommodityBasis) basisView {
	return basisView{ID: b.ID, LegalEntityID: b.LegalEntityID, Code: b.Code, Name: b.Name, Currency: b.Currency, Unit: b.Unit, Version: b.Version}
}

type indexRequest struct {
	AsOf   string `json:"asOf"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

type indexView struct {
	BasisID  string `json:"basisId"`
	AsOf     string `json:"asOf"`
	Revision int    `json:"revision"`
	Value    string `json:"value"`
	Source   string `json:"source"`
	// AffectedAgreements is only in the quotes inquiry.
	AffectedAgreements *int `json:"affectedAgreements,omitempty"`
}

func indexResponse(v domain.IndexValue) indexView {
	return indexView{BasisID: v.BasisID, AsOf: v.AsOf.Format(dateLayout), Revision: v.Revision, Value: v.Value.String(), Source: v.Source}
}

type calcRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	Name            string `json:"name"`
	BasisID         string `json:"basisId"`
	Factor          string `json:"factor"`
	MarginKind      string `json:"marginKind"`
	Margin          string `json:"margin"`
	Rounding        *int   `json:"rounding"`
	MaxIndexAgeDays *int   `json:"maxIndexAgeDays"`
	CustomerKind    string `json:"customerKind"`
	CustomerKey     string `json:"customerKey"`
	ProductID       string `json:"productId"`
	Unit            string `json:"unit"`
	Currency        string `json:"currency"`
}

type calcView struct {
	ID              string `json:"id"`
	LegalEntityID   string `json:"legalEntityId"`
	Name            string `json:"name"`
	BasisID         string `json:"basisId"`
	Factor          string `json:"factor"`
	MarginKind      string `json:"marginKind"`
	Margin          string `json:"margin"`
	Rounding        int    `json:"rounding"`
	MaxIndexAgeDays int    `json:"maxIndexAgeDays"`
	CustomerKind    string `json:"customerKind"`
	CustomerKey     string `json:"customerKey"`
	ProductID       string `json:"productId"`
	Unit            string `json:"unit"`
	Currency        string `json:"currency"`
	Withdrawn       bool   `json:"withdrawn"`
	Version         int    `json:"version"`
}

func calcResponse(c domain.CommodityCalc) calcView {
	return calcView{ID: c.ID, LegalEntityID: c.LegalEntityID, Name: c.Name, BasisID: c.BasisID, Factor: c.Factor.String(),
		MarginKind: c.MarginKind, Margin: c.Margin.String(), Rounding: c.Rounding, MaxIndexAgeDays: c.MaxIndexAgeDays,
		CustomerKind: string(c.CustomerKind), CustomerKey: c.CustomerKey, ProductID: c.ProductID, Unit: c.Unit,
		Currency: c.Currency, Withdrawn: c.Withdrawn, Version: c.Version}
}

type runRequest struct {
	AsOf string `json:"asOf"`
}

type changeView struct {
	RecordID      string `json:"recordId"`
	Version       int    `json:"version"`
	CurrentAmount string `json:"currentAmount"`
	NewAmount     string `json:"newAmount"`
}

type warningView struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type previewView struct {
	CalculationID  string        `json:"calculationId"`
	AsOf           string        `json:"asOf"`
	Index          indexView     `json:"index"`
	Price          string        `json:"price"`
	Stale          bool          `json:"stale"`
	IndexAgeDays   int           `json:"indexAgeDays"`
	Warnings       []warningView `json:"warnings"`
	Changes        []changeView  `json:"changes"`
	UnchangedCount int           `json:"unchangedCount"`
}

type runView struct {
	ID             string        `json:"id"`
	LegalEntityID  string        `json:"legalEntityId"`
	CalculationID  string        `json:"calculationId"`
	AsOf           string        `json:"asOf"`
	IndexAsOf      string        `json:"indexAsOf"`
	IndexRevision  int           `json:"indexRevision"`
	IndexValue     string        `json:"indexValue"`
	Price          string        `json:"price"`
	ChangedCount   int           `json:"changedCount"`
	UnchangedCount int           `json:"unchangedCount"`
	Stale          bool          `json:"stale"`
	JournalID      string        `json:"journalId,omitempty"`
	RunBy          string        `json:"runBy"`
	Warnings       []warningView `json:"warnings"`
	CreatedAt      time.Time     `json:"createdAt"`
}

func staleWarning(stale bool, calc domain.CommodityCalc, indexAsOf string, days int) []warningView {
	out := []warningView{}
	if stale {
		out = append(out, warningView{Code: domain.CodeIndexStale, Detail: "the newest index value is from " + indexAsOf +
			", older than the " + strconv.Itoa(calc.MaxIndexAgeDays) + " days this calculation allows (" + strconv.Itoa(days) + " days old); load the index before the run"})
	}
	return out
}

func runResponse(r store.CommodityRun, calc domain.CommodityCalc) runView {
	return runView{ID: r.ID, LegalEntityID: r.LegalEntityID, CalculationID: r.CalculationID, AsOf: r.AsOf.Format(dateLayout),
		IndexAsOf: r.IndexAsOf.Format(dateLayout), IndexRevision: r.IndexRevision, IndexValue: r.IndexValue.String(), Price: r.Price.String(),
		ChangedCount: r.ChangedCount, UnchangedCount: r.UnchangedCount, Stale: r.Stale, JournalID: r.JournalID, RunBy: r.RunBy,
		Warnings: staleWarning(r.Stale, calc, r.IndexAsOf.Format(dateLayout), int(r.AsOf.Sub(r.IndexAsOf).Hours()/24)), CreatedAt: r.CreatedAt}
}

func (a *API) createBasis(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req basisRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	b := domain.CommodityBasis{LegalEntityID: req.LegalEntityID, Code: req.Code, Name: req.Name, Currency: req.Currency, Unit: req.Unit}
	fields := domain.ValidateBasis(b)
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CreateCommodityBasis(r.Context(), b, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, basisResponse(out))
}

func legalEntityQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return "", false
	}
	return le, true
}

func (a *API) listBases(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le, ok := legalEntityQuery(w, r)
	if !ok {
		return
	}
	rows, err := a.store.ListCommodityBases(r.Context(), le, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]basisView, 0, len(rows))
	for _, b := range rows {
		items = append(items, basisResponse(b))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getBasis(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeBasisNotFound, "commodity cost basis type")
	if !ok {
		return
	}
	b, err := a.store.GetCommodityBasis(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, basisResponse(b))
}

func (a *API) recordIndex(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeBasisNotFound, "commodity cost basis type")
	if !ok {
		return
	}
	var req indexRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v := domain.IndexValue{BasisID: id, Source: req.Source}
	fields := map[string]string{}
	if d, err := time.Parse(dateLayout, req.AsOf); err != nil {
		fields["asOf"] = "must be a date in YYYY-MM-DD form"
	} else {
		v.AsOf = d
	}
	if d, err := decimal.NewFromString(req.Value); err != nil {
		fields["value"] = "must be a decimal"
	} else {
		v.Value = d
	}
	if len(fields) == 0 {
		fields = domain.ValidateIndexValue(v)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.RecordIndexValue(r.Context(), v, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, indexResponse(out))
}

func (a *API) listIndex(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeBasisNotFound, "commodity cost basis type")
	if !ok {
		return
	}
	rows, err := a.store.ListIndexValues(r.Context(), id, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]indexView, 0, len(rows))
	for _, v := range rows {
		view := indexResponse(v)
		n := v.AffectedAgreements
		view.AffectedAgreements = &n
		items = append(items, view)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createCalc(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req calcRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.BasisID) {
		fields["basisId"] = "must be a UUID"
	}
	factor, err := decimal.NewFromString(req.Factor)
	if err != nil {
		fields["factor"] = "must be a decimal"
	}
	margin := optionalDecimal(req.Margin, "margin", fields)
	// commodity_index_rounding defaults to 6 decimals (§5.26.13) and the staleness window to
	// 7 days (weekly schedule); there is no parameter table yet, so a calculation carries them.
	rounding, age := domain.DefaultIndexRounding, domain.DefaultMaxIndexAgeDays
	if req.Rounding != nil {
		rounding = *req.Rounding
	}
	if req.MaxIndexAgeDays != nil {
		age = *req.MaxIndexAgeDays
	}
	c := domain.CommodityCalc{LegalEntityID: req.LegalEntityID, Name: req.Name, BasisID: req.BasisID, Factor: factor,
		MarginKind: req.MarginKind, Margin: margin, Rounding: rounding, MaxIndexAgeDays: age,
		CustomerKind: domain.CustomerKind(req.CustomerKind), CustomerKey: req.CustomerKey, ProductID: req.ProductID, Unit: req.Unit, Currency: req.Currency}
	for k, v := range domain.ValidateCalc(c) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CreateCommodityCalc(r.Context(), c, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, calcResponse(out))
}

func (a *API) listCalcs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le, ok := legalEntityQuery(w, r)
	if !ok {
		return
	}
	basis := r.URL.Query().Get("basisId")
	if basis != "" && !ids.Valid(basis) {
		validationFailed(w, r, map[string]string{"basisId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListCommodityCalcs(r.Context(), le, basis, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]calcView, 0, len(rows))
	for _, c := range rows {
		items = append(items, calcResponse(c))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getCalc(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeCalculationNotFound, "commodity price calculation")
	if !ok {
		return
	}
	c, err := a.store.GetCommodityCalc(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, calcResponse(c))
}

func (a *API) withdrawCalc(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeCalculationNotFound, "commodity price calculation")
	if !ok {
		return
	}
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Version < 1 {
		validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
		return
	}
	c, err := a.store.WithdrawCommodityCalc(r.Context(), id, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, calcResponse(c))
}

// runAsOf reads the run date; the engine reads no clock, so the caller supplies it.
func runAsOf(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return time.Time{}, false
	}
	d, err := time.Parse(dateLayout, req.AsOf)
	if err != nil {
		validationFailed(w, r, map[string]string{"asOf": "must be a date in YYYY-MM-DD form"})
		return time.Time{}, false
	}
	return d, true
}

func (a *API) previewRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeCalculationNotFound, "commodity price calculation")
	if !ok {
		return
	}
	asOf, ok := runAsOf(w, r)
	if !ok {
		return
	}
	calc, plan, err := a.store.PreviewCommodityRun(r.Context(), id, asOf)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	changes := make([]changeView, 0, len(plan.Changes))
	for _, ch := range plan.Changes {
		changes = append(changes, changeView{RecordID: ch.Record.ID, Version: ch.Record.Version,
			CurrentAmount: ch.Record.Amount.String(), NewAmount: ch.NewAmount.String()})
	}
	httpx.JSON(w, http.StatusOK, previewView{CalculationID: id, AsOf: asOf.Format(dateLayout), Index: indexResponse(plan.Index),
		Price: plan.Price.String(), Stale: plan.Stale, IndexAgeDays: plan.AgeDays,
		Warnings: staleWarning(plan.Stale, calc, plan.Index.AsOf.Format(dateLayout), plan.AgeDays), Changes: changes, UnchangedCount: plan.Unchanged})
}

func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeCalculationNotFound, "commodity price calculation")
	if !ok {
		return
	}
	asOf, ok := runAsOf(w, r)
	if !ok {
		return
	}
	run, err := a.store.RunCommodityCalc(r.Context(), id, asOf, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	calc, err := a.store.GetCommodityCalc(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, runResponse(run, calc))
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeCalculationNotFound, "commodity price calculation")
	if !ok {
		return
	}
	calc, err := a.store.GetCommodityCalc(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rows, err := a.store.ListCommodityRuns(r.Context(), id, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]runView, 0, len(rows))
	for _, x := range rows {
		items = append(items, runResponse(x, calc))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
