package api

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// actionCancelLine is the custom method that cancels one line of an APPROVED requisition (BR-17.1).
const actionCancelLine = "cancel-line"

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
	WarehouseID        string `json:"warehouseId,omitempty"`
	PreferredVendorID  string `json:"preferredVendorId,omitempty"`
	// GLAccount and CostCenter are the budget dimensions the line is charged to; budget control checks a line that has both.
	GLAccount  string `json:"glAccount,omitempty"`
	CostCenter string `json:"costCenter,omitempty"`
	// CatalogEntryID picks the line from a published procurement catalog (FR-17.18).
	CatalogEntryID string `json:"catalogEntryId,omitempty"`
	// CatalogSource PUNCHOUT marks a line returned from a supplier's punchout catalog (#35, 35-3), with SupplierPartID; NeedsApproval
	// is its BR-35.13 approval flag, which keeps the requisition from being approved automatically.
	CatalogSource  string `json:"catalogSource,omitempty"`
	SupplierPartID string `json:"supplierPartId,omitempty"`
	NeedsApproval  bool   `json:"needsApproval,omitempty"`
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
	if err := punchoutFieldsAllowed(p, body.Lines); err != nil {
		fail(w, r, err)
		return
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

var budgetDimension = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

func buildRequisitionLine(no int, l requisitionLineBody, minor int32) (store.RequisitionLine, error) {
	field := func(name string) string { return "lines[" + strconv.Itoa(no-1) + "]." + name }
	if l.ProductID == "" && (strings.TrimSpace(l.Description) == "" || strings.TrimSpace(l.Category) == "") {
		return store.RequisitionLine{}, invalid(field("productId"), "or description and category are required")
	}
	for _, f := range []struct{ name, value string }{{"productId", l.ProductID}, {"siteId", l.SiteID}, {"warehouseId", l.WarehouseID}, {"preferredVendorId", l.PreferredVendorID}, {"catalogEntryId", l.CatalogEntryID}} {
		if err := uuidField(field(f.name), f.value, false); err != nil {
			return store.RequisitionLine{}, err
		}
	}
	for _, f := range []struct{ name, value string }{{"glAccount", l.GLAccount}, {"costCenter", l.CostCenter}} {
		if f.value != "" && !budgetDimension.MatchString(f.value) {
			return store.RequisitionLine{}, invalid(field(f.name), "must be 1 to 20 letters, digits, dots, hyphens or underscores")
		}
	}
	if (l.GLAccount == "") != (l.CostCenter == "") {
		return store.RequisitionLine{}, invalid(field("costCenter"), "glAccount and costCenter go together: name both, or neither for a line that is not budget controlled")
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
	partID := strings.TrimSpace(l.SupplierPartID)
	switch {
	case l.CatalogSource != "" && l.CatalogSource != store.CatalogSourcePunchout:
		return store.RequisitionLine{}, invalid(field("catalogSource"), "must be PUNCHOUT or left out")
	case l.CatalogSource == "" && (partID != "" || l.NeedsApproval):
		return store.RequisitionLine{}, invalid(field("catalogSource"), "is required with supplierPartId or needsApproval: they describe a punchout line")
	case len(partID) > 100:
		return store.RequisitionLine{}, invalid(field("supplierPartId"), "must be at most 100 characters")
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
		WarehouseID:        l.WarehouseID,
		PreferredVendorID:  l.PreferredVendorID,
		CatalogEntryID:     l.CatalogEntryID,
		GLAccount:          l.GLAccount,
		CostCenter:         l.CostCenter,
		CatalogSource:      l.CatalogSource,
		SupplierPartID:     partID,
		NeedsApproval:      l.NeedsApproval,
	}, nil
}

// estimatePrices defaults blank estimates (FR-17.1): a catalog line with a
// preferred vendor takes the trade agreement price for its quantity on the
// requested date; a product line the agreement could not price falls back to the net price of the
// last approved order of the product in the same legal entity, currency and unit (warning
// PRC-REQ-012 names the order); anything left at zero carries warning PRC-REQ-003.
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
		if needsEstimate[i] && l.EstimatedUnitPrice.IsZero() && l.ProductID != "" {
			last, err := a.store.LastOrderPrice(ctx, req.LegalEntityID, l.ProductID, req.Currency, l.Unit)
			if err != nil {
				return err
			}
			if last != nil {
				// The order's own line math (domain.ComputeLine): qty × unit price ÷ price unit, net of the
				// discount percent, rounded half-even once; the estimate is that net price per one unit.
				la, err := domain.ComputeLine(domain.LineInput{Qty: l.Qty, UnitPrice: last.UnitPrice, PriceUnit: last.PriceUnit,
					DiscountPct: last.DiscountPct, Tax: decimal.Zero}, minor)
				if err != nil {
					return err
				}
				hundred := decimal.NewFromInt(100)
				l.EstimatedUnitPrice = last.UnitPrice.Mul(hundred.Sub(last.DiscountPct)).Div(hundred).DivRound(last.PriceUnit, 6)
				l.Amount = la.Amount
				req.Warnings = append(req.Warnings, store.Warning{Code: codeLastPrice, LineNo: l.LineNo,
					Detail: "Estimated price taken from the last order of " + l.ProductID + ", " + last.Number + " (" + last.OrderDate.String() + ")"})
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

// requisitionAction handles POST /requisitions/{id}:submit|approve|reject|reopen|cancel|withdraw|cancel-line. The chi segment holds
// "<id>:<action>"; each action checks its own duty.
func (a *API) requisitionAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || (action != "submit" && action != actionCancelLine && !slices.Contains(domain.ReqActions, action)) || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown requisition action"))
		return
	}
	if approving := action == domain.ReqActionApprove || action == domain.ReqActionReject; approving && !p.HasDuty(dutyRequisitionApprove) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+dutyRequisitionApprove))
		return
	} else if !approving && !p.HasDuty(dutyRequisitionCreate) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+dutyRequisitionCreate))
		return
	}
	userID, err := caller(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if action == actionCancelLine {
		a.cancelRequisitionLine(w, r, id, userID)
		return
	}
	if action != "submit" {
		a.decideRequisition(w, r, id, action, userID)
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
	chain := a.approvalChain(cur)
	// FR-17.17 / BR-17.15: a requester whose spending limit is below the total (or whose limit
	// is past its recertification date) gets one more approval level, not a rejection.
	if cur.Status == store.RequisitionDraft {
		limits, err := a.store.SigningLimitsFor(r.Context(), cur.LegalEntityID, cur.RequesterID, domain.DocRequisition)
		if err != nil {
			fail(w, r, err)
			return
		}
		if escalate, warn := domain.RequesterEscalates(limits, cur.Total, cur.Currency, a.today()); escalate {
			chain = append(chain, domain.RoleNextApprover)
			if warn != nil {
				warnings = append(warnings, store.Warning{Code: warn.Code, Detail: warn.Detail})
			}
		}
	}
	out, err := a.store.SubmitRequisition(r.Context(), id, version, chain, domain.DefaultPolicyVersion, idem)
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
	// A punchout line priced above its catalog price plus tolerance (BR-35.13) is never approved automatically, nor is a requisition
	// that held one when it was created (an edit cannot drop the approval).
	if len(chain) == 0 && (r.NeedsApproval || slices.ContainsFunc(r.Lines, func(l store.RequisitionLine) bool { return l.NeedsApproval })) {
		chain = []string{domain.RoleManager}
	}
	return chain
}

// punchoutIssuer is the service whose token may create punchout lines: the EDI engine turning a returned cart into a requisition.
const punchoutIssuer = "edi"

func hasPunchoutFields(l requisitionLineBody) bool {
	return l.CatalogSource != "" || strings.TrimSpace(l.SupplierPartID) != "" || l.NeedsApproval
}

// punchoutFieldsAllowed refuses punchout fields from anyone but the EDI engine: only a returned punchout cart says a line came from
// a supplier's catalog and whether its price needs approval (#35, 35-3).
func punchoutFieldsAllowed(p tenant.Principal, lines []requisitionLineBody) error {
	if p.Service == punchoutIssuer {
		return nil
	}
	for i, l := range lines {
		if hasPunchoutFields(l) {
			return invalid("lines["+strconv.Itoa(i)+"].catalogSource", "is set by the punchout return only")
		}
	}
	return nil
}

// keepPunchoutFields makes the punchout fields of an edited requisition sticky: an edited line that is a punchout line of the
// requisition (by supplierPartId when given, else by description and unit) takes the catalog source, supplier part and approval
// flag from it, whatever the edit sent, and an edit may not mark any other line as a punchout line.
func keepPunchoutFields(cur store.Requisition, lines []requisitionLineBody) ([]requisitionLineBody, error) {
	byPart := map[string]store.RequisitionLine{}
	byText := map[string]store.RequisitionLine{}
	textKey := func(desc, unit string) string {
		return strings.ToLower(strings.TrimSpace(desc)) + "|" + strings.ToLower(strings.TrimSpace(unit))
	}
	for _, l := range cur.Lines {
		if l.CatalogSource != store.CatalogSourcePunchout {
			continue
		}
		if l.SupplierPartID != "" {
			byPart[l.SupplierPartID] = l
		}
		byText[textKey(l.Description, l.Unit)] = l
	}
	out := make([]requisitionLineBody, len(lines))
	for i, l := range lines {
		var (
			src store.RequisitionLine
			ok  bool
		)
		if part := strings.TrimSpace(l.SupplierPartID); part != "" {
			if src, ok = byPart[part]; !ok {
				return nil, invalid("lines["+strconv.Itoa(i)+"].supplierPartId", "names no punchout line of this requisition; only the punchout return adds punchout lines")
			}
		} else if hasPunchoutFields(l) {
			return nil, invalid("lines["+strconv.Itoa(i)+"].catalogSource", "is set by the punchout return only")
		} else {
			src, ok = byText[textKey(l.Description, l.Unit)]
		}
		if ok {
			l.CatalogSource, l.SupplierPartID, l.NeedsApproval = src.CatalogSource, src.SupplierPartID, src.NeedsApproval
		}
		out[i] = l
	}
	return out, nil
}
