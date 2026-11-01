package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type requisitionBody struct {
	LegalEntityID   string                `json:"legalEntityId"`
	OperatingUnitID string                `json:"operatingUnitId,omitempty"`
	Currency        string                `json:"currency"`
	Lines           []requisitionLineBody `json:"lines"`
}

type requisitionLineBody struct {
	ProductID          string `json:"productId,omitempty"`
	Description        string `json:"description,omitempty"`
	Category           string `json:"category,omitempty"`
	Qty                string `json:"qty"`
	Unit               string `json:"unit"`
	EstimatedUnitPrice string `json:"estimatedUnitPrice,omitempty"`
	RequestedDate      string `json:"requestedDate"`
	SiteID             string `json:"siteId,omitempty"`
	PreferredVendorID  string `json:"preferredVendorId,omitempty"`
	// CatalogEntryID picks the line from a published procurement catalog (FR-17.18).
	CatalogEntryID string `json:"catalogEntryId,omitempty"`
}

func (a *API) createRequisition(w http.ResponseWriter, r *http.Request) {
	userID, err := caller(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body requisitionBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	p, _ := principal(r.Context())
	// Scope first, so a catalog entry is never looked up for a legal entity the caller cannot use.
	if ids.Valid(body.LegalEntityID) {
		if err := inScope(p, body.LegalEntityID); err != nil {
			fail(w, r, err)
			return
		}
	}
	resolved, err := a.applyCatalogEntries(r, body)
	if err != nil {
		fail(w, r, err)
		return
	}
	req, estimate, err := buildRequisition(resolved, userID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	if err := a.estimatePrices(r.Context(), &req, estimate); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateRequisition(r.Context(), req, store.Idem{Key: key, Hash: hashBody("requisition.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.requisitionsCreated.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// buildRequisition validates the body and prices each line at its estimate
// (amount = qty × estimated unit price, rounded half-even once to the
// currency's minor unit). needsEstimate[i] is true when line i left the
// estimate blank, so estimatePrices can default it (FR-17.1).
func buildRequisition(body requisitionBody, requester string) (store.Requisition, []bool, error) {
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		return store.Requisition{}, nil, err
	}
	if err := uuidField("operatingUnitId", body.OperatingUnitID, false); err != nil {
		return store.Requisition{}, nil, err
	}
	cur, err := currencyField(body.Currency)
	if err != nil {
		return store.Requisition{}, nil, err
	}
	if len(body.Lines) == 0 {
		return store.Requisition{}, nil, invalid("lines", "needs at least one line")
	}
	if len(body.Lines) > maxRequisitionLines {
		return store.Requisition{}, nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeTooManyLines,
			"requisition has more than "+strconv.Itoa(maxRequisitionLines)+" lines")
	}
	minor := money.MinorUnits(cur)
	out := store.Requisition{LegalEntityID: body.LegalEntityID, OperatingUnitID: body.OperatingUnitID, RequesterID: requester, Currency: cur,
		Total: decimal.Zero}
	needsEstimate := make([]bool, len(body.Lines))
	for i, l := range body.Lines {
		line, err := buildRequisitionLine(i+1, l, minor)
		if err != nil {
			return store.Requisition{}, nil, err
		}
		needsEstimate[i] = strings.TrimSpace(l.EstimatedUnitPrice) == ""
		out.Lines = append(out.Lines, line)
		out.Total = out.Total.Add(line.Amount)
	}
	return out, needsEstimate, nil
}

func buildRequisitionLine(no int, l requisitionLineBody, minor int32) (store.RequisitionLine, error) {
	field := func(name string) string { return "lines[" + strconv.Itoa(no-1) + "]." + name }
	if l.ProductID == "" && (strings.TrimSpace(l.Description) == "" || strings.TrimSpace(l.Category) == "") {
		return store.RequisitionLine{}, invalid(field("productId"), "or description and category are required")
	}
	for _, f := range []struct{ name, value string }{{"productId", l.ProductID}, {"siteId", l.SiteID}, {"preferredVendorId", l.PreferredVendorID}, {"catalogEntryId", l.CatalogEntryID}} {
		if err := uuidField(field(f.name), f.value, false); err != nil {
			return store.RequisitionLine{}, err
		}
	}
	qty, err := decimalField(field("qty"), l.Qty, "")
	if err != nil {
		return store.RequisitionLine{}, err
	}
	if qty.Sign() <= 0 {
		return store.RequisitionLine{}, invalid(field("qty"), "must be positive")
	}
	if strings.TrimSpace(l.Unit) == "" {
		return store.RequisitionLine{}, invalid(field("unit"), "is required")
	}
	price, err := decimalField(field("estimatedUnitPrice"), strings.TrimSpace(l.EstimatedUnitPrice), "0")
	if err != nil {
		return store.RequisitionLine{}, err
	}
	if price.Sign() < 0 {
		return store.RequisitionLine{}, invalid(field("estimatedUnitPrice"), "must not be negative")
	}
	date, err := domain.ParseDay(l.RequestedDate)
	if err != nil {
		return store.RequisitionLine{}, invalid(field("requestedDate"), "must be YYYY-MM-DD")
	}
	return store.RequisitionLine{
		LineNo:             no,
		ProductID:          l.ProductID,
		Description:        strings.TrimSpace(l.Description),
		Category:           strings.TrimSpace(l.Category),
		Qty:                qty,
		Unit:               strings.TrimSpace(l.Unit),
		EstimatedUnitPrice: price,
		Amount:             qty.Mul(price).RoundBank(minor),
		RequestedDate:      date,
		SiteID:             l.SiteID,
		PreferredVendorID:  l.PreferredVendorID,
		CatalogEntryID:     l.CatalogEntryID,
	}, nil
}

// estimatePrices defaults blank estimates (FR-17.1): a catalog line with a
// preferred vendor takes the trade agreement price for its quantity on the
// requested date; anything left at zero carries warning PRC-REQ-003. The
// last-PO-price fallback needs PO history by product and is listed in
// NOT-IMPLEMENTED.md.
func (a *API) estimatePrices(ctx context.Context, req *store.Requisition, needsEstimate []bool) error {
	minor := money.MinorUnits(req.Currency)
	req.Total = decimal.Zero
	for i := range req.Lines {
		l := &req.Lines[i]
		if needsEstimate[i] && l.ProductID != "" && l.PreferredVendorID != "" {
			res, err := a.resolve(ctx, domain.Query{Relation: domain.RelationPrice, VendorID: l.PreferredVendorID, ProductID: l.ProductID,
				Qty: l.Qty, Currency: req.Currency, Unit: l.Unit, Date: l.RequestedDate}, nil, nil)
			if err != nil {
				return err
			}
			if res.Winner != nil {
				l.EstimatedUnitPrice = res.Winner.Price.DivRound(res.Winner.PriceUnit, 6)
				l.Amount = l.Qty.Mul(res.Winner.Price).DivRound(res.Winner.PriceUnit, 12).RoundBank(minor)
			}
		}
		if needsEstimate[i] && l.EstimatedUnitPrice.IsZero() {
			name := l.ProductID
			if name == "" {
				name = l.Description
			}
			req.Warnings = append(req.Warnings, store.Warning{Code: codeNoEstimate, LineNo: l.LineNo,
				Detail: "No price found for " + name + "; estimated price set to 0"})
		}
		req.Total = req.Total.Add(l.Amount)
	}
	return nil
}

func (a *API) getRequisition(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetRequisition(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func validRequisitionStatus(s string) bool {
	switch s {
	case "DRAFT", "IN_REVIEW", "APPROVED", "REJECTED", "CLOSED", "CANCELLED":
		return true
	}
	return false
}

// listRequisitions lists headers newest first; ?mine=true narrows to the caller's own.
func (a *API) listRequisitions(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.RequisitionFilter{Status: q.Get("status")}
	if f.Status != "" && !validRequisitionStatus(f.Status) {
		fail(w, r, invalid("status", "must be DRAFT, IN_REVIEW, APPROVED, REJECTED, CLOSED or CANCELLED"))
		return
	}
	if q.Get("mine") == "true" {
		if f.RequesterID, err = caller(r.Context()); err != nil {
			fail(w, r, err)
			return
		}
	}
	page, err := a.store.ListRequisitions(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// requisitionAction handles POST /requisitions/{id}:submit. The chi segment
// holds "<id>:<action>"; only submit is defined in this slice.
func (a *API) requisitionAction(w http.ResponseWriter, r *http.Request) {
	userID, err := caller(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || action != "submit" || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown requisition action"))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetRequisition(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if cur.RequesterID != userID {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeNotRequester, "only the requester can submit this requisition"))
		return
	}
	// FR-17.20: a DRAFT is checked against the category access mode before it is routed.
	var warnings []store.Warning
	if cur.Status == store.RequisitionDraft {
		if warnings, err = a.checkCategoryAccess(r.Context(), cur.LegalEntityID, requisitionUses(cur)); err != nil {
			fail(w, r, err)
			return
		}
	}
	// The chain is computed from the version read here; the store refuses the
	// submit if the requisition moved past that version in between.
	idem := store.Idem{Key: key, Hash: hashBody("requisition.submit", map[string]any{"id": id, "version": version})}
	out, err := a.store.SubmitRequisition(r.Context(), id, version, a.approvalChain(cur), domain.DefaultPolicyVersion, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.requisitionsSubmitted.Add(1)
	out.Warnings = warnings
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// approvalChain routes a requisition: each distinct line category is routed on
// the requisition total and the most senior chain wins, so a category rule
// (FR-17.2) can add approvers but never remove them.
func (a *API) approvalChain(r store.Requisition) []string {
	var chain []string
	seen := map[string]bool{}
	for _, l := range r.Lines {
		if seen[l.Category] {
			continue
		}
		seen[l.Category] = true
		if c := a.policy.Route(r.Total, l.Category); len(c) > len(chain) {
			chain = c
		}
	}
	return chain
}
