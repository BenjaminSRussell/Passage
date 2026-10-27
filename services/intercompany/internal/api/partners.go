package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

// PartnerStore persists trading partners (FR-40.1). internal/store implements
// it over Postgres with row-level security and the outbox.
type PartnerStore interface {
	CreatePartner(ctx context.Context, p domain.Partner, key string) (domain.PartnerRecord, bool, error)
	ListPartners(ctx context.Context, f store.PartnerFilter, after string, limit int) (store.PartnerPage, error)
	GetPartner(ctx context.Context, id string) (domain.PartnerRecord, error)
	UpdatePartner(ctx context.Context, id string, version int, patch domain.PartnerPatch) (domain.PartnerRecord, error)
}

const (
	maxIdempotencyKey  = 255
	defaultPartnerPage = 50
	maxPartnerPage     = 200
)

// ---- wire shapes ----

type actionsBody struct {
	AutoCreate          bool `json:"autoCreate"`
	AutoConfirm         bool `json:"autoConfirm"`
	AutoPostPackingSlip bool `json:"autoPostPackingSlip"`
	AutoPostInvoice     bool `json:"autoPostInvoice"`
}

// toleranceBody is the BR-40.3 tolerance on the wire; omitted fields keep the
// default (create) or the stored value (update).
type toleranceBody struct {
	PricePct *string `json:"pricePct"`
	DateDays *int    `json:"dateDays"`
}

type createPartnerBody struct {
	SellerEntity    string         `json:"sellerEntity"`
	BuyerEntity     string         `json:"buyerEntity"`
	CustomerAccount string         `json:"customerAccount"`
	VendorAccount   string         `json:"vendorAccount"`
	Active          *bool          `json:"active"`
	Actions         *actionsBody   `json:"actions"`
	Pricing         *string        `json:"pricing"`
	MarkupPct       *string        `json:"markupPct"`
	Tolerance       *toleranceBody `json:"tolerance"`
}

// updatePartnerBody has no seller or buyer: the direction of a pairing never
// changes, and the decoder rejects unknown fields.
type updatePartnerBody struct {
	CustomerAccount *string        `json:"customerAccount"`
	VendorAccount   *string        `json:"vendorAccount"`
	Active          *bool          `json:"active"`
	Actions         *actionsBody   `json:"actions"`
	Pricing         *string        `json:"pricing"`
	MarkupPct       *string        `json:"markupPct"`
	Tolerance       *toleranceBody `json:"tolerance"`
}

type toleranceResponse struct {
	PricePct string `json:"pricePct"`
	DateDays int    `json:"dateDays"`
}

type partnerResponse struct {
	ID              string            `json:"id"`
	Version         int               `json:"version"`
	SellerEntity    string            `json:"sellerEntity"`
	BuyerEntity     string            `json:"buyerEntity"`
	CustomerAccount string            `json:"customerAccount"`
	VendorAccount   string            `json:"vendorAccount"`
	Active          bool              `json:"active"`
	Actions         actionsBody       `json:"actions"`
	Pricing         string            `json:"pricing"`
	MarkupPct       string            `json:"markupPct"`
	Tolerance       toleranceResponse `json:"tolerance"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

type partnerListResponse struct {
	Items      []partnerResponse `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func toPartnerResponse(rec domain.PartnerRecord) partnerResponse {
	a := rec.Actions
	return partnerResponse{
		ID: rec.ID, Version: rec.Version, SellerEntity: rec.SellerEntity, BuyerEntity: rec.BuyerEntity,
		CustomerAccount: rec.CustomerAccount, VendorAccount: rec.VendorAccount, Active: rec.Active,
		Actions: actionsBody{AutoCreate: a.AutoCreate, AutoConfirm: a.AutoConfirm, AutoPostPackingSlip: a.AutoPostPackingSlip, AutoPostInvoice: a.AutoPostInvoice},
		Pricing: string(rec.Pricing), MarkupPct: rec.MarkupPct.String(),
		Tolerance: toleranceResponse{PricePct: rec.Tolerance.PricePct.String(), DateDays: rec.Tolerance.DateDays},
		CreatedAt: rec.CreatedAt.UTC(), UpdatedAt: rec.UpdatedAt.UTC(),
	}
}

func (b actionsBody) policy() domain.ActionPolicy {
	return domain.ActionPolicy{AutoCreate: b.AutoCreate, AutoConfirm: b.AutoConfirm, AutoPostPackingSlip: b.AutoPostPackingSlip, AutoPostInvoice: b.AutoPostInvoice}
}

// code checks an entity code or account number: required, at most 64 characters.
func (f fieldParser) code(name, v string) string {
	v = f.text(name, v)
	if len(v) > domain.MaxCodeLength {
		f[name] = "must be at most 64 characters"
	}
	return v
}

func (f fieldParser) pricing(raw string) domain.PricingPolicy {
	p := domain.PricingPolicy(raw)
	if p != domain.PricePriceList && p != domain.PriceCostPlus && p != domain.PriceTable {
		f["pricing"] = "must be PriceList, CostPlus or TransferPriceTable"
	}
	return p
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf("%q", strconv.Itoa(version)))
}

// partnerID reads the {id} path parameter; anything but a UUID is not found.
func partnerID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodePartnerNotFound, "no intercompany partner with id "+id))
		return "", false
	}
	return id, true
}

// ---- POST /v1/interco/partners ----

func (a *API) createPartner(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > maxIdempotencyKey {
		validation(w, r, map[string]string{"Idempotency-Key": "header is required, at most 255 characters"})
		return
	}
	var body createPartnerBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	p := domain.Partner{
		SellerEntity:    f.code("sellerEntity", body.SellerEntity),
		BuyerEntity:     f.code("buyerEntity", body.BuyerEntity),
		CustomerAccount: f.code("customerAccount", body.CustomerAccount),
		VendorAccount:   f.code("vendorAccount", body.VendorAccount),
		// ic.partnerPolicy default: auto-create on, auto-post off.
		Active:    true,
		Actions:   domain.ActionPolicy{AutoCreate: true},
		Pricing:   domain.PricePriceList,
		Tolerance: domain.DefaultTolerance(),
	}
	if p.SellerEntity != "" && p.SellerEntity == p.BuyerEntity {
		f["buyerEntity"] = "must differ from sellerEntity"
	}
	if body.Active != nil {
		p.Active = *body.Active
	}
	if body.Actions != nil {
		p.Actions = body.Actions.policy()
	}
	if body.Pricing != nil {
		p.Pricing = f.pricing(*body.Pricing)
	}
	if body.MarkupPct != nil {
		p.MarkupPct = f.amount("markupPct", *body.MarkupPct, true)
	}
	if body.Tolerance != nil {
		if body.Tolerance.PricePct != nil {
			p.Tolerance.PricePct = f.amount("tolerance.pricePct", *body.Tolerance.PricePct, true)
		}
		if body.Tolerance.DateDays != nil {
			p.Tolerance.DateDays = *body.Tolerance.DateDays
		}
	}
	if !validation(w, r, f) {
		return
	}
	rec, replayed, err := a.partners.CreatePartner(r.Context(), p, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if !replayed {
		a.partnerChanges.Add(1)
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/interco/partners/"+rec.ID)
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusCreated, toPartnerResponse(rec))
}

// ---- GET /v1/interco/partners ----

func (a *API) listPartners(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	filter := store.PartnerFilter{Entity: strings.TrimSpace(q.Get("entity"))}
	switch q.Get("active") {
	case "":
	case "true", "false":
		active := q.Get("active") == "true"
		filter.Active = &active
	default:
		fields["active"] = "must be true or false"
	}
	after := q.Get("after")
	if after != "" && !ids.Valid(after) {
		fields["after"] = "must be the nextCursor of the previous page"
	}
	limit := defaultPartnerPage
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPartnerPage {
			fields["limit"] = fmt.Sprintf("must be a whole number from 1 to %d", maxPartnerPage)
		}
		limit = n
	}
	if !validation(w, r, fields) {
		return
	}
	page, err := a.partners.ListPartners(r.Context(), filter, after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := partnerListResponse{Items: make([]partnerResponse, 0, len(page.Items)), NextCursor: page.Next}
	for _, rec := range page.Items {
		out.Items = append(out.Items, toPartnerResponse(rec))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- GET /v1/interco/partners/{id} ----

func (a *API) getPartner(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	id, ok := partnerID(w, r)
	if !ok {
		return
	}
	rec, err := a.partners.GetPartner(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusOK, toPartnerResponse(rec))
}

// ---- PATCH /v1/interco/partners/{id} ----

// ifMatch reads the required If-Match version: missing is 428, malformed is
// 400, and a stale one is 412 from the store.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, domain.CodeVersionConflict, "If-Match with the pairing's current ETag is required"))
		return 0, false
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		validation(w, r, map[string]string{"If-Match": `must be an ETag such as "3"`})
		return 0, false
	}
	return v, true
}

func (a *API) updatePartner(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	id, ok := partnerID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var body updatePartnerBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	patch := domain.PartnerPatch{Active: body.Active}
	if body.CustomerAccount != nil {
		v := f.code("customerAccount", *body.CustomerAccount)
		patch.CustomerAccount = &v
	}
	if body.VendorAccount != nil {
		v := f.code("vendorAccount", *body.VendorAccount)
		patch.VendorAccount = &v
	}
	if body.Actions != nil {
		v := body.Actions.policy()
		patch.Actions = &v
	}
	if body.Pricing != nil {
		v := f.pricing(*body.Pricing)
		patch.Pricing = &v
	}
	if body.MarkupPct != nil {
		v := f.amount("markupPct", *body.MarkupPct, true)
		patch.MarkupPct = &v
	}
	if body.Tolerance != nil {
		if body.Tolerance.PricePct != nil {
			v := f.amount("tolerance.pricePct", *body.Tolerance.PricePct, true)
			patch.TolerancePricePct = &v
		}
		patch.ToleranceDateDays = body.Tolerance.DateDays
	}
	if !validation(w, r, f) {
		return
	}
	rec, err := a.partners.UpdatePartner(r.Context(), id, version, patch)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if rec.Version != version {
		a.partnerChanges.Add(1)
	}
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusOK, toPartnerResponse(rec))
}

// ---- POST /v1/interco/partners/{id}/lines:price (FR-40.4) ----

type lineTransferPrice struct {
	Item          string `json:"item"`
	Method        string `json:"method"`
	MarkupPct     string `json:"markupPct"`
	UnitPrice     string `json:"unitPrice"`
	EffectiveFrom string `json:"effectiveFrom"`
}

type priceLineRequest struct {
	Item           string              `json:"item"`
	Date           string              `json:"date"`
	Currency       string              `json:"currency"`
	Quantity       string              `json:"quantity"`
	ListPrice      string              `json:"listPrice"`
	UnitCost       string              `json:"unitCost"`
	TransferPrices []lineTransferPrice `json:"transferPrices"`
}

type pricingBasisResponse struct {
	Policy        string `json:"policy"`
	Method        string `json:"method"`
	MarkupPct     string `json:"markupPct,omitempty"`
	Source        string `json:"source"`
	EffectiveFrom string `json:"effectiveFrom,omitempty"`
}

type priceLineResponse struct {
	PartnerID      string               `json:"partnerId"`
	PartnerVersion int                  `json:"partnerVersion"`
	Item           string               `json:"item"`
	Quantity       string               `json:"quantity"`
	Currency       string               `json:"currency"`
	UnitPrice      string               `json:"unitPrice"`
	Extended       string               `json:"extended"`
	Basis          pricingBasisResponse `json:"basis"`
}

func (a *API) priceLine(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	id, ok := partnerID(w, r)
	if !ok {
		return
	}
	var req priceLineRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	line := domain.LineInput{
		Item:     f.code("item", req.Item),
		On:       f.date("date", req.Date),
		Currency: f.currency("currency", req.Currency),
		Quantity: f.positive("quantity", req.Quantity),
	}
	line.ListPrice = f.amount("listPrice", req.ListPrice, false)
	line.UnitCost = f.amount("unitCost", req.UnitCost, false)
	if len(req.TransferPrices) > MaxTransferPrices {
		f["transferPrices"] = fmt.Sprintf("must contain at most %d rows", MaxTransferPrices)
	}
	if !validation(w, r, f) {
		return
	}
	rec, err := a.partners.GetPartner(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if !rec.Active {
		httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeNoPolicy,
			Detail: fmt.Sprintf("no active IC partner policy between %s and %s", rec.SellerEntity, rec.BuyerEntity)}))
		return
	}
	// The inputs a policy prices from are required for that policy.
	switch rec.Pricing {
	case domain.PricePriceList:
		f.amount("listPrice", req.ListPrice, true)
	case domain.PriceCostPlus:
		f.amount("unitCost", req.UnitCost, true)
	case domain.PriceTable:
		f.amount("unitCost", req.UnitCost, true)
		if len(req.TransferPrices) == 0 {
			f["transferPrices"] = "must contain the item's transfer prices for a TransferPriceTable pairing"
		}
	}
	var table []domain.TransferPrice
	for i, row := range req.TransferPrices {
		at := func(name string) string { return fmt.Sprintf("transferPrices[%d].%s", i, name) }
		tp := domain.TransferPrice{
			Lender: rec.SellerEntity, Borrower: rec.BuyerEntity, Category: domain.CategoryItem, Role: f.code(at("item"), row.Item),
			Method: row.Method, Currency: line.Currency, EffectiveFrom: f.date(at("effectiveFrom"), row.EffectiveFrom),
		}
		switch row.Method {
		case domain.TPCost:
		case domain.TPCostPlus:
			tp.MarkupPct = f.amount(at("markupPct"), row.MarkupPct, true)
		case domain.TPTable:
			tp.UnitPrice = f.amount(at("unitPrice"), row.UnitPrice, true)
		default:
			f[at("method")] = "must be Cost, CostPlus or Table"
		}
		table = append(table, tp)
	}
	if !validation(w, r, f) {
		return
	}
	lp, err := domain.ResolveLinePrice(rec.Partner, table, line)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.linesPriced.Add(1)
	basis := pricingBasisResponse{Policy: string(lp.Basis.Policy), Method: lp.Basis.Method, Source: lp.Basis.Source}
	if lp.Basis.Method == domain.TPCostPlus {
		basis.MarkupPct = lp.Basis.MarkupPct.String()
	}
	if !lp.Basis.EffectiveFrom.IsZero() {
		basis.EffectiveFrom = lp.Basis.EffectiveFrom.Format(dateLayout)
	}
	httpx.JSON(w, http.StatusOK, priceLineResponse{
		PartnerID: rec.ID, PartnerVersion: rec.Version, Item: line.Item, Quantity: line.Quantity.String(), Currency: line.Currency,
		UnitPrice: lp.UnitPrice.String(), Extended: lp.Extended.Value.StringFixed(money.MinorUnits(line.Currency)), Basis: basis,
	})
}

// ---- POST /v1/interco/partners/{id}/changes:evaluate (BR-40.3) ----

type evaluateChangeRequest struct {
	OldQuantity string `json:"oldQuantity"`
	NewQuantity string `json:"newQuantity"`
	OldPrice    string `json:"oldPrice"`
	NewPrice    string `json:"newPrice"`
	OldDate     string `json:"oldDate"`
	NewDate     string `json:"newDate"`
}

type evaluateChangeResponse struct {
	PartnerID      string            `json:"partnerId"`
	PartnerVersion int               `json:"partnerVersion"`
	Action         string            `json:"action"`
	Code           string            `json:"code,omitempty"`
	Reasons        []string          `json:"reasons"`
	DateShiftDays  int               `json:"dateShiftDays"`
	Tolerance      toleranceResponse `json:"tolerance"`
}

func (a *API) evaluateChange(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	id, ok := partnerID(w, r)
	if !ok {
		return
	}
	var req evaluateChangeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	ch := domain.OrderChange{
		OldQty:   f.positive("oldQuantity", req.OldQuantity),
		NewQty:   f.positive("newQuantity", req.NewQuantity),
		OldPrice: f.amount("oldPrice", req.OldPrice, true),
		NewPrice: f.amount("newPrice", req.NewPrice, true),
	}
	oldDate, newDate := f.date("oldDate", req.OldDate), f.date("newDate", req.NewDate)
	if !validation(w, r, f) {
		return
	}
	ch.DateShiftDays = int(newDate.Sub(oldDate).Hours() / 24)
	rec, err := a.partners.GetPartner(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	d := domain.DecideOrderChange(rec.Tolerance, ch)
	a.changeEvaluations.Add(1)
	httpx.JSON(w, http.StatusOK, evaluateChangeResponse{
		PartnerID: rec.ID, PartnerVersion: rec.Version, Action: d.Action, Code: d.Code, Reasons: d.Reasons, DateShiftDays: ch.DateShiftDays,
		Tolerance: toleranceResponse{PricePct: rec.Tolerance.PricePct.String(), DateDays: rec.Tolerance.DateDays},
	})
}
