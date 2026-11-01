package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

const (
	maxJournalLines = 5000
	maxAccount      = 64
	maxItemGroup    = 40
)

// ---- journals ----

type issueRequest struct {
	LineNo   string           `json:"lineNo"`
	Quantity decimal.Decimal  `json:"quantity"`
	UnitCost *decimal.Decimal `json:"unitCost"`
}

type hoursRequest struct {
	OperNumber int              `json:"operNumber"`
	Hours      decimal.Decimal  `json:"hours"`
	QtyGood    *decimal.Decimal `json:"qtyGood"`
	QtyError   *decimal.Decimal `json:"qtyError"`
	Finished   bool             `json:"finished"`
}

// journalRequest creates a journal (PRD 4.21.5). Without issues or hours the
// lines are proposed; quantity and errorQuantity belong to report as finished.
type journalRequest struct {
	Type          domain.JournalType `json:"type"`
	PostingDate   *string            `json:"postingDate"`
	Proposal      domain.Proposal    `json:"proposal"`
	Quantity      *decimal.Decimal   `json:"quantity"`
	ErrorQuantity *decimal.Decimal   `json:"errorQuantity"`
	Issues        []issueRequest     `json:"issues"`
	Hours         []hoursRequest     `json:"hours"`
	// CoProducts are the co-product receipts of a report as finished (FR-21.14).
	CoProducts []coProductReportRequest `json:"coProducts,omitempty"`
}

type coProductReportRequest struct {
	LineNo   string          `json:"lineNo"`
	Quantity decimal.Decimal `json:"quantity"`
}

func (a *API) createJournal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req journalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	in := store.NewJournal{OrderID: id, Type: req.Type, Proposal: req.Proposal, AutoStart: a.cfg.AutoStartOnFeedback, Actor: p.UserID, IdempotencyKey: key}
	validateJournal(req, &in, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	in.RequestHash = hashOf(struct {
		Order string
		Req   journalRequest
	}{id, req})
	j, created, err := a.store.CreateJournal(r.Context(), in)
	if err != nil {
		a.fail(w, r, "create production journal", err)
		return
	}
	w.Header().Set("Location", "/v1/production/journals/"+j.ID)
	if !created {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, j)
		return
	}
	httpx.JSON(w, http.StatusCreated, j)
}

func validateJournal(req journalRequest, in *store.NewJournal, fields map[string]string) {
	if !req.Type.Valid() {
		fields["type"] = "must be picking-list, route-card or report-as-finished"
	}
	if req.PostingDate != nil {
		if !isDate(*req.PostingDate) {
			fields["postingDate"] = "must be a date (YYYY-MM-DD)"
		}
		in.PostingDate = *req.PostingDate
	}
	switch {
	case req.Proposal == "" && req.Type == domain.JournalReportFinished:
		in.Proposal = domain.ProposeProportional
	case req.Proposal == "":
		// Left empty: the store applies the site's picking-list or route-card default (PRD 4.21.13).
	case !req.Proposal.Valid():
		fields["proposal"] = "must be remaining, proportional or none"
	}
	if req.Type == domain.JournalReportFinished {
		if req.Quantity == nil {
			fields["quantity"] = "is required: the good quantity reported as finished"
		} else {
			checkQuantity("quantity", *req.Quantity, true, fields)
			in.QtyGood = *req.Quantity
		}
		if req.ErrorQuantity != nil {
			checkQuantity("errorQuantity", *req.ErrorQuantity, false, fields)
			in.QtyError = *req.ErrorQuantity
		}
	} else if req.Quantity != nil || req.ErrorQuantity != nil {
		fields["quantity"] = "is accepted only by a report-as-finished journal"
	}
	if len(req.Issues)+len(req.Hours) > maxJournalLines {
		fields["lines"] = fmt.Sprintf("at most %d lines", maxJournalLines)
	}
	switch {
	case len(req.CoProducts) > 0 && req.Type != domain.JournalReportFinished:
		fields["coProducts"] = "only a report-as-finished journal receipts co-products"
	case len(req.CoProducts) > maxCoProducts:
		fields["coProducts"] = fmt.Sprintf("at most %d co-product lines", maxCoProducts)
	}
	seenCo := map[string]bool{}
	for i, c := range req.CoProducts {
		at := fmt.Sprintf("coProducts[%d]", i)
		if c.LineNo == "" || len(c.LineNo) > maxLineNo || seenCo[c.LineNo] {
			fields[at+".lineNo"] = fmt.Sprintf("is required, at most %d characters, listed once", maxLineNo)
		}
		seenCo[c.LineNo] = true
		checkQuantity(at+".quantity", c.Quantity, true, fields)
		in.CoProducts = append(in.CoProducts, store.CoProductInput{LineNo: c.LineNo, Qty: c.Quantity})
	}
	if len(req.Issues) > 0 && req.Type == domain.JournalRouteCard {
		fields["issues"] = "a route card reports hours, not issues"
	}
	if len(req.Hours) > 0 && req.Type != domain.JournalRouteCard {
		fields["hours"] = "only a route card reports hours"
	}
	for i, is := range req.Issues {
		at := fmt.Sprintf("issues[%d]", i)
		if is.LineNo == "" || len(is.LineNo) > maxLineNo {
			fields[at+".lineNo"] = fmt.Sprintf("is required, at most %d characters", maxLineNo)
		}
		checkQuantity(at+".quantity", is.Quantity, true, fields)
		if is.UnitCost != nil {
			checkQuantity(at+".unitCost", *is.UnitCost, false, fields)
		}
		in.Issues = append(in.Issues, store.IssueInput{LineNo: is.LineNo, Qty: is.Quantity, UnitCost: is.UnitCost})
	}
	for i, h := range req.Hours {
		at := fmt.Sprintf("hours[%d]", i)
		if h.OperNumber < 1 {
			fields[at+".operNumber"] = "must be a positive whole number"
		}
		line := store.HoursInput{OperNumber: h.OperNumber, Hours: h.Hours, Finished: h.Finished}
		if h.QtyGood != nil {
			checkQuantity(at+".qtyGood", *h.QtyGood, false, fields)
			line.QtyGood = *h.QtyGood
		}
		if h.QtyError != nil {
			checkQuantity(at+".qtyError", *h.QtyError, false, fields)
			line.QtyError = *h.QtyError
		}
		switch {
		case h.Hours.IsZero():
			if !line.QtyGood.IsPositive() && !line.QtyError.IsPositive() && !h.Finished {
				fields[at+".hours"] = "report hours, a quantity, or finish the operation"
			}
		case domain.CheckHours(h.Hours) != nil || !h.Hours.LessThan(maxQty):
			fields[at+".hours"] = "must be at least 0.01 with at most 2 decimals"
		}
		in.Hours = append(in.Hours, line)
	}
}

func (a *API) listJournals(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	journals, err := a.store.ListJournals(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list production journals", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": id, "items": journals})
}

func journalID(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	if !ids.Valid(raw) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeJournalNotFound, "production journal not found"))
		return "", false
	}
	return raw, true
}

func (a *API) getJournal(w http.ResponseWriter, r *http.Request) {
	id, ok := journalID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	j, err := a.store.GetJournal(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read production journal", err)
		return
	}
	httpx.JSON(w, http.StatusOK, j)
}

func (a *API) deleteJournal(w http.ResponseWriter, r *http.Request) {
	id, ok := journalID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if err := a.store.DeleteJournal(r.Context(), id); err != nil {
		a.fail(w, r, "delete production journal", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// postJournal serves POST /journals/{id}:post (FR-21.5 to FR-21.7).
func (a *API) postJournal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	raw, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || action != "post" || !ids.Valid(raw) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, unknownActionCode, "use :post on a production journal"))
		return
	}
	key, fields := idempotencyKey(r)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	j, replayed, err := a.store.PostJournal(r.Context(), store.PostRequest{JournalID: raw, OverConsumptionDuty: p.HasDuty(DutyOverConsume),
		OverConsumptionPct: a.cfg.OverConsumptionPct, RAFTolerancePct: a.cfg.RAFTolerancePct, AutoStart: a.cfg.AutoStartOnFeedback,
		Actor: p.UserID, TraceID: middleware.GetReqID(r.Context()), IdempotencyKey: key})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	if replayed {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		a.posted.Add(1)
	}
	httpx.JSON(w, http.StatusOK, j)
}

// ---- posting profiles ----

// profileList is the posting-profile read (FR-21.13): the profiles of a legal
// entity and, with siteId, the one that resolves for the item group and site.
type profileList struct {
	LegalEntityID string                 `json:"legalEntityId"`
	Items         []store.PostingProfile `json:"items"`
	Resolved      *domain.Profile        `json:"resolved,omitempty"`
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	if le == "" {
		le = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	site, group := q.Get("siteId"), q.Get("itemGroup")
	if site != "" && !ids.Valid(site) {
		fields["siteId"] = "must be a UUID"
	}
	if group != "" && site == "" {
		fields["siteId"] = "is required to resolve a profile"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListPostingProfiles(r.Context(), le)
	if err != nil {
		a.fail(w, r, "list posting profiles", err)
		return
	}
	out := profileList{LegalEntityID: le, Items: items}
	if site != "" {
		profiles := make([]domain.Profile, len(items))
		for i, it := range items {
			profiles[i] = it.Profile
		}
		resolved, err := domain.ResolveProfile(profiles, group, site)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		out.Resolved = &resolved
	}
	httpx.JSON(w, http.StatusOK, out)
}

type profileRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	domain.Profile
}

func (a *API) saveProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch := 0
	if raw := r.Header.Get("If-Match"); raw != "" {
		v, err := parseIfMatch(raw)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		ifMatch = v
	}
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	if req.ItemGroup == "" {
		req.ItemGroup = domain.AllKey
	}
	if req.SiteID == "" {
		req.SiteID = domain.AllKey
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		fields["legalEntityId"] = "must be the legal entity of the caller"
	}
	if len(req.ItemGroup) > maxItemGroup {
		fields["itemGroup"] = fmt.Sprintf("at most %d characters, or All", maxItemGroup)
	}
	if req.SiteID != domain.AllKey && !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID or All"
	}
	for name, v := range map[string]string{"wipAccount": req.WIP, "inventoryAccount": req.Inventory, "finishedGoodsAccount": req.FinishedGoods,
		"laborAbsorptionAccount": req.LaborAbsorption, "overheadAbsorptionAccount": req.OverheadAbsorption,
		"lotSizeVarianceAccount": req.VarLotSize, "quantityVarianceAccount": req.VarQuantity, "priceVarianceAccount": req.VarPrice,
		"substitutionVarianceAccount": req.VarSubstitution} {
		if strings.TrimSpace(v) == "" || len(v) > maxAccount {
			fields[name] = fmt.Sprintf("is required, at most %d characters", maxAccount)
		}
	}
	if req.VarRework != strings.TrimSpace(req.VarRework) || len(req.VarRework) > maxAccount {
		fields["reworkVarianceAccount"] = fmt.Sprintf("at most %d characters without surrounding spaces; needed to end a rework order", maxAccount)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	saved, created, err := a.store.SavePostingProfile(r.Context(), store.ProfileSave{LegalEntityID: req.LegalEntityID, Profile: req.Profile,
		IfMatch: ifMatch, Actor: p.UserID})
	if err != nil {
		a.fail(w, r, "save posting profile", err)
		return
	}
	w.Header().Set("ETag", etag(saved.Version))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}

// ---- costs ----

// costView is the cost read (FR-21.11): planned cost from the estimate and
// actual cost from posted journals by group, both to the currency's minor
// unit, the rows behind them, the WIP balance, and the variances posted at End.
type costView struct {
	OrderID       string                      `json:"orderId"`
	Currency      string                      `json:"currency"`
	Totals        map[domain.CostGroup]string `json:"totals"`
	Total         string                      `json:"total"`
	Rows          []domain.CostRow            `json:"rows"`
	ActualTotals  map[domain.CostGroup]string `json:"actualTotals"`
	ActualTotal   string                      `json:"actualTotal"`
	ActualRows    []domain.CostRow            `json:"actualRows"`
	WIPBalance    string                      `json:"wipBalance"`
	Variances     []domain.Variance           `json:"variances"`
	VarianceTotal string                      `json:"varianceTotal"`
}

func groupTotals(rows []domain.CostRow, currency string) (map[domain.CostGroup]string, string) {
	places := money.MinorUnits(currency)
	sums := map[domain.CostGroup]decimal.Decimal{}
	total := decimal.Zero
	for _, c := range rows {
		sums[c.Group] = sums[c.Group].Add(c.Amount)
		total = total.Add(c.Amount)
	}
	out := map[domain.CostGroup]string{}
	for g, v := range sums {
		out[g] = v.StringFixed(places)
	}
	return out, total.StringFixed(places)
}

func (a *API) getCosts(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	c, err := a.store.Costs(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read production costs", err)
		return
	}
	places := money.MinorUnits(c.Currency)
	v := costView{OrderID: id, Currency: c.Currency, Rows: c.Planned, ActualRows: c.Actual, Variances: c.Variances,
		WIPBalance: c.WIPBalance.StringFixed(places)}
	v.Totals, v.Total = groupTotals(c.Planned, c.Currency)
	v.ActualTotals, v.ActualTotal = groupTotals(c.Actual, c.Currency)
	sum := decimal.Zero
	for _, x := range c.Variances {
		sum = sum.Add(x.Amount)
	}
	v.VarianceTotal = sum.StringFixed(places)
	httpx.JSON(w, http.StatusOK, v)
}
