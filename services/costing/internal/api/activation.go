package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	dutyVersionActivate = "costing.version.activate"
	ifMatchHeader       = "If-Match"
	codePrecondition    = "COM-0428"
	topDeltaCount       = 20
	maxOnHandLines      = 50000
	// maxQty bounds an on-hand quantity to numeric(28,6): 22 integer digits.
	maxQtyDigits = 22
)

var (
	versionStatuses = map[string]bool{"DRAFT": true, "ACTIVE": true, "ARCHIVED": true}
	maxQty          = decimal.New(1, maxQtyDigits)
)

// ---- reads ----

type versionListResponse struct {
	Items      []store.CostingVersion `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type itemCostListResponse struct {
	Items      []store.ItemCostRow `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

// pageParams reads limit (1..MaxPageSize, default 50) and an opaque cursor
// whose decoded form must be "<a>|<uuid>"; checkFirst says whether <a> is a UUID too.
func pageParams(r *http.Request, checkFirst bool) (int, string, map[string]string) {
	q := r.URL.Query()
	fields := map[string]string{}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", store.MaxPageSize)
		}
		limit = n
	}
	after := ""
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		first, second, ok := strings.Cut(string(decoded), "|")
		if err != nil || !ok || !ids.Valid(second) || (checkFirst && !ids.Valid(first)) {
			fields["cursor"] = "is not a cursor this endpoint returned"
		}
		after = string(decoded)
	}
	return limit, after, fields
}

func cursor(next string) string {
	if next == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(next))
}

// listVersions lists costing versions, optionally by legal entity, status and type.
func (a *API) listVersions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, fields := pageParams(r, false)
	q := r.URL.Query()
	f := store.VersionFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status"), Type: q.Get("type")}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.Status != "" && !versionStatuses[f.Status] {
		fields["status"] = "must be DRAFT, ACTIVE or ARCHIVED"
	}
	if f.Type != "" && !versionTypes[f.Type] {
		fields["type"] = "must be STANDARD or PLANNED"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListVersions(r.Context(), f, after, limit)
	if err != nil {
		a.fail(w, r, "list costing versions", err)
		return
	}
	httpx.JSON(w, http.StatusOK, versionListResponse{Items: page.Items, NextCursor: cursor(page.Next)})
}

// versionID reads the {id} path parameter; a malformed id is simply not found.
func versionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeVersionNotFound, "costing version not found"))
		return "", false
	}
	return id, true
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(version)))
}

// getVersion returns one version; the ETag carries its optimistic version.
func (a *API) getVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	v, err := a.store.GetVersion(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get costing version", err)
		return
	}
	setETag(w, v.Version)
	httpx.JSON(w, http.StatusOK, v)
}

// listItemCosts pages a version's item costs with the active cost they compare to.
func (a *API) listItemCosts(w http.ResponseWriter, r *http.Request) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	limit, after, fields := pageParams(r, true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListItemCosts(r.Context(), id, after, limit)
	if err != nil {
		a.fail(w, r, "list item costs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemCostListResponse{Items: page.Items, NextCursor: cursor(page.Next)})
}

// ---- activation ----

// onHandDTO is the physical on-hand of one product x site that the revaluation
// applies to (BR-20.7). bookValue is its current inventory value and is needed
// only where the product x site has no active standard cost yet.
type onHandDTO struct {
	ProductID string  `json:"productId"`
	SiteID    string  `json:"siteId"`
	Qty       string  `json:"qty"`
	BookValue *string `json:"bookValue,omitempty"`
}

type activateRequest struct {
	OnHand []onHandDTO `json:"onHand"`
}

type lineDTO struct {
	ProductID    string  `json:"productId"`
	SiteID       string  `json:"siteId"`
	PreviousCost *string `json:"previousCost,omitempty"`
	BookValue    *string `json:"bookValue,omitempty"`
	NewCost      string  `json:"newCost"`
	OnHandQty    string  `json:"onHandQty"`
	Revaluation  string  `json:"revaluation"`
}

type siteRevaluationDTO struct {
	SiteID      string `json:"siteId"`
	Revaluation string `json:"revaluation"`
}

// activationResponse is the revaluation preview or the committed activation.
// status is PREVIEW, ACTIVATED or ALREADY_ACTIVE; version is the costing
// version's optimistic version to send as If-Match when activating.
type activationResponse struct {
	VersionID        string               `json:"versionId"`
	VersionCode      string               `json:"versionCode"`
	Version          int                  `json:"version"`
	Status           string               `json:"status"`
	Currency         string               `json:"currency"`
	LineCount        int                  `json:"lineCount"`
	RevaluationTotal string               `json:"revaluationTotal"`
	Sites            []siteRevaluationDTO `json:"sites"`
	TopDeltas        []lineDTO            `json:"topDeltas"`
}

func (a *API) previewActivation(w http.ResponseWriter, r *http.Request) {
	a.activation(w, r, false)
}

func (a *API) activateVersion(w http.ResponseWriter, r *http.Request) {
	a.activation(w, r, true)
}

// ifMatchVersion reads the optimistic version from If-Match (required on activation).
func ifMatchVersion(r *http.Request) (int, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get(ifMatchHeader)), `"`)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, codePrecondition, "If-Match with the version from the activation preview is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		return 0, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "If-Match must be the version from the activation preview (the ETag)")
	}
	return v, nil
}

// parseOnHand validates the on-hand lines into the store's map.
func parseOnHand(fields map[string]string, lines []onHandDTO) store.OnHand {
	if len(lines) > maxOnHandLines {
		fields["onHand"] = fmt.Sprintf("must hold at most %d lines", maxOnHandLines)
		return nil
	}
	out := make(store.OnHand, len(lines))
	for i, oh := range lines {
		key := fmt.Sprintf("onHand[%d]", i)
		if !ids.Valid(oh.ProductID) {
			fields[key+".productId"] = "must be a UUID"
		}
		if !ids.Valid(oh.SiteID) {
			fields[key+".siteId"] = "must be a UUID"
		}
		// Physical on-hand can be negative (negative stock); it must fit numeric(28,6).
		qty := parseDecimal(fields, key+".qty", oh.Qty, false)
		if _, bad := fields[key+".qty"]; !bad && (!qty.Equal(qty.Truncate(6)) || qty.Abs().GreaterThanOrEqual(maxQty)) {
			fields[key+".qty"] = fmt.Sprintf("must have at most %d integer digits and 6 decimal places", maxQtyDigits)
		}
		entry := store.OnHandQty{Qty: qty}
		if oh.BookValue != nil {
			bv := parseDecimal(fields, key+".bookValue", *oh.BookValue, false)
			if _, bad := fields[key+".bookValue"]; !bad && (!bv.Equal(bv.Truncate(6)) || bv.Abs().GreaterThanOrEqual(domain.MaxUnitCost)) {
				fields[key+".bookValue"] = "must have at most 14 integer digits and 6 decimal places"
			}
			entry.BookValue = &bv
		}
		k := store.OnHandKey(strings.ToLower(oh.ProductID), strings.ToLower(oh.SiteID))
		if _, dup := out[k]; dup {
			fields[key] = "duplicates an earlier product and site"
		}
		out[k] = entry
	}
	return out
}

// activation previews (commit=false) or commits an activation. Both run the
// same plan; only the commit writes rows and cost.changed events.
func (a *API) activation(w http.ResponseWriter, r *http.Request, commit bool) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	expected := 0
	if commit {
		var err error
		if expected, err = ifMatchVersion(r); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	var req activateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	onHand := parseOnHand(fields, req.OnHand)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	var (
		res store.ActivationResult
		err error
	)
	if commit {
		res, err = a.store.ActivateVersion(r.Context(), id, expected, onHand)
	} else {
		res, err = a.store.PreviewActivation(r.Context(), id, onHand)
	}
	if err != nil {
		a.fail(w, r, "activate costing version", err)
		return
	}
	status := "PREVIEW"
	switch {
	case res.AlreadyActive:
		status = "ALREADY_ACTIVE"
	case commit:
		status = "ACTIVATED"
		a.activations.Add(1)
		a.costsActivated.Add(int64(len(res.Plan.Lines)))
	}
	setETag(w, res.Version.Version)
	httpx.JSON(w, http.StatusOK, activationDTO(res, status))
}

func activationDTO(res store.ActivationResult, status string) activationResponse {
	scale := money.MinorUnits(res.Version.Currency)
	out := activationResponse{VersionID: res.Version.ID, VersionCode: res.Version.Code, Version: res.Version.Version, Status: status,
		Currency: res.Version.Currency, LineCount: len(res.Plan.Lines), RevaluationTotal: res.Plan.Total.StringFixed(scale),
		Sites: []siteRevaluationDTO{}, TopDeltas: []lineDTO{}}
	for _, s := range res.Plan.Sites {
		out.Sites = append(out.Sites, siteRevaluationDTO{SiteID: s.SiteID, Revaluation: s.Amount.StringFixed(scale)})
	}
	for _, l := range domain.TopDeltas(res.Plan.Lines, topDeltaCount) {
		d := lineDTO{ProductID: l.ProductID, SiteID: l.SiteID, NewCost: l.NewCost.String(), OnHandQty: l.OnHandQty.String(),
			Revaluation: l.Revaluation.StringFixed(scale)}
		if l.HasPrevious {
			prev := l.PreviousCost.String()
			d.PreviousCost = &prev
		} else {
			book := l.BookValue.StringFixed(scale)
			d.BookValue = &book
		}
		out.TopDeltas = append(out.TopDeltas, d)
	}
	return out
}

// ---- copy with percent change ----

type copyItemCostsRequest struct {
	SourceVersionID string `json:"sourceVersionId"`
	PercentChange   string `json:"percentChange"`
}

// copyItemCosts copies a version's costs into a DRAFT version, moved by a
// percent change (FR-20.5). The percent is a decimal string, e.g. "2.5" or "-1".
func (a *API) copyItemCosts(w http.ResponseWriter, r *http.Request) {
	targetID, ok := versionID(w, r)
	if !ok {
		return
	}
	var req copyItemCostsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.SourceVersionID) {
		fields["sourceVersionId"] = "must be a UUID"
	}
	percent := parseDecimal(fields, "percentChange", req.PercentChange, false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	n, err := a.store.CopyItemCosts(r.Context(), targetID, strings.ToLower(req.SourceVersionID), percent)
	if err != nil {
		a.fail(w, r, "copy item costs", err)
		return
	}
	a.itemsCopied.Add(int64(n))
	httpx.JSON(w, http.StatusOK, map[string]any{"versionId": targetID, "copied": n})
}

// ---- cost lookup ----

// currentCost answers the standard cost of a product x site in force on a date
// (default today, UTC). Waves 21 and 27 read it; none is 404 CST-COST-001.
func (a *API) currentCost(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	productID := chi.URLParam(r, "id")
	siteID := r.URL.Query().Get("site")
	date := r.URL.Query().Get("date")
	fields := map[string]string{}
	if !ids.Valid(productID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(siteID) {
		fields["site"] = "is required and must be a UUID"
	}
	if date == "" {
		date = time.Now().UTC().Format(time.DateOnly)
	} else {
		parseDate(fields, "date", date)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CurrentCost(r.Context(), productID, siteID, date)
	if err != nil {
		a.fail(w, r, "current cost", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
