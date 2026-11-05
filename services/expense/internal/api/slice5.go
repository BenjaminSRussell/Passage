package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	platformmoney "github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

// Duties of the slice-5 routes (8.43.17). Requests and the card pool use the EssExpense
// duty (expense.report.write); decisions need hr.team.approve; recording the AP payment and
// cash returns is ExpenseAdmin's; card import keeps expense.card.import.
const (
	dutyApprove      = "expense.approve"
	dutyAdvanceAdmin = "expense.advance.admin"
)

// Limits and the 8.43.13 parameter defaults. Legal-entity overrides need the parameter
// store of a later slice (NOT-IMPLEMENTED.md).
const (
	maxDestinations   = 20
	maxEstimates      = 100
	maxCardholders    = 5000
	maxCategoryLength = 32
	maxPlaceLength    = 128
)

var (
	defaultAdvanceMax = decimal.RequireFromString("2000.00")
	// maxEstimatedTotal keeps a requisition's summed estimate inside the stored
	// numeric(20,6) column, the same bound each amount has.
	maxEstimatedTotal = decimal.NewFromInt(1_000_000_000_000)
	last4Pattern      = regexp.MustCompile(`^[0-9]{4}$`)
	advanceStatuses   = map[string]bool{"DRAFT": true, "SUBMITTED": true, "APPROVED": true, "REJECTED": true, "PAID": true, "PARTIALLY_SETTLED": true, "SETTLED": true}
	requisitionStates = map[string]bool{"DRAFT": true, "SUBMITTED": true, "APPROVED": true, "REJECTED": true, "CLOSED": true}
	cardStatuses      = map[string]bool{"UNASSIGNED": true, "ASSIGNED": true, "DISPUTED": true, "PERSONAL": true}
)

// travelStore is the cash advance, travel requisition and card feed persistence the
// slice-5 routes need (satisfied by *store.Store).
type travelStore interface {
	RequestAdvance(ctx context.Context, key, hash string, in store.NewAdvance, pol store.AdvancePolicy, asOf time.Time) (store.Advance, bool, error)
	GetAdvance(ctx context.Context, id string) (store.Advance, error)
	ListAdvances(ctx context.Context, f store.AdvanceFilter, after string, limit int) ([]store.Advance, string, error)
	DecideAdvance(ctx context.Context, id string, approve bool, c store.Command) (store.Advance, bool, error)
	PayAdvance(ctx context.Context, id, prepaymentID string, c store.Command) (store.Advance, bool, error)
	ReturnAdvance(ctx context.Context, id string, amount decimal.Decimal, c store.Command) (store.Advance, bool, error)

	CreateRequisition(ctx context.Context, key, hash string, in store.NewRequisition) (store.Requisition, bool, error)
	GetRequisition(ctx context.Context, id string) (store.Requisition, error)
	ListRequisitions(ctx context.Context, f store.RequisitionFilter, after string, limit int) ([]store.Requisition, string, error)
	SubmitRequisition(ctx context.Context, id string, c store.Command) (store.Requisition, bool, error)
	DecideRequisition(ctx context.Context, id string, approve bool, c store.Command) (store.Requisition, bool, error)

	ImportCardFeed(ctx context.Context, key, hash string, in store.NewImport) (store.Import, bool, error)
	ListCardTransactions(ctx context.Context, f store.CardFilter, after string, limit int) ([]store.CardTransaction, string, error)
}

// WithTravel attaches cash advance, travel requisition and card feed persistence.
// Without it those routes answer 503 and the rest of the API still runs.
func (a *API) WithTravel(s travelStore) *API {
	a.travel = s
	return a
}

func (a *API) registerTravel(r chi.Router, duty func(http.Handler) http.Handler) {
	approve := authz.RequireDuty(dutyApprove)
	admin := authz.RequireDuty(dutyAdvanceAdmin)
	r.With(duty).Get("/v1/expense/cash-advances", a.listAdvances)
	r.With(duty).Post("/v1/expense/cash-advances", a.requestAdvance)
	r.With(duty).Get("/v1/expense/cash-advances/{id}", a.getAdvance)
	r.With(approve).Post("/v1/expense/cash-advances/{id}/decide", a.decideAdvance)
	r.With(admin).Post("/v1/expense/cash-advances/{id}/pay", a.payAdvance)
	r.With(admin).Post("/v1/expense/cash-advances/{id}/return", a.returnAdvance)
	r.With(duty).Get("/v1/expense/travel-requisitions", a.listRequisitions)
	r.With(duty).Post("/v1/expense/travel-requisitions", a.createRequisition)
	r.With(duty).Get("/v1/expense/travel-requisitions/{id}", a.getRequisition)
	r.With(duty).Post("/v1/expense/travel-requisitions/{id}/submit", a.submitRequisition)
	r.With(approve).Post("/v1/expense/travel-requisitions/{id}/decide", a.decideRequisition)
	r.With(duty).Get("/v1/expense/card-transactions", a.listCardTransactions)
	r.With(authz.RequireDuty(dutyCardImport)).Post("/v1/expense/card-transactions/import", a.importCardFeed)
}

// travelReady refuses the slice-5 routes when no store is attached.
func (a *API) travelReady(w http.ResponseWriter, r *http.Request) bool {
	if a.travel == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "cash advance, requisition and card persistence is not configured"))
		return false
	}
	return true
}

// command reads the Idempotency-Key and If-Match of a custom method and hashes the
// operation, the target and the body, so the key cannot be replayed onto another
// action or another record.
func command(w http.ResponseWriter, r *http.Request, op, id string, body any) (store.Command, bool) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return store.Command{}, false
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return store.Command{}, false
	}
	return store.Command{Key: key, ExpectedVersion: expected,
		Hash: hashOf(map[string]any{"op": op, "id": id, "body": body, "ifMatch": expected})}, true
}

// ---- cash advances (FR-43.15) ----

type advanceRequest struct {
	LegalEntityID       string `json:"legalEntityId"`
	WorkerID            string `json:"workerId"`
	Amount              string `json:"amount"`
	Currency            string `json:"currency"`
	Purpose             string `json:"purpose"`
	TravelRequisitionID string `json:"travelRequisitionId"`
	TripEnd             string `json:"tripEnd"`
}

type cashAdvanceDTO struct {
	ID                  string  `json:"id"`
	LegalEntityID       string  `json:"legalEntityId"`
	WorkerID            string  `json:"workerId"`
	Amount              string  `json:"amount"`
	Currency            string  `json:"currency"`
	Purpose             string  `json:"purpose"`
	TravelRequisitionID *string `json:"travelRequisitionId"`
	TripEnd             string  `json:"tripEnd"`
	DueDate             string  `json:"dueDate"`
	Overdue             bool    `json:"overdue"`
	Status              string  `json:"status"`
	Paid                string  `json:"paid"`
	Settled             string  `json:"settled"`
	Returned            string  `json:"returned"`
	Recovered           string  `json:"recovered"`
	Open                string  `json:"open"`
	APPrepaymentID      *string `json:"apPrepaymentId"`
	Version             int     `json:"version"`
	CreatedAt           string  `json:"createdAt"`
}

type advancePage struct {
	Items      []cashAdvanceDTO `json:"items"`
	NextCursor string           `json:"nextCursor"`
}

func (a *API) requestAdvance(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req advanceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Purpose = strings.TrimSpace(req.Purpose)
	fields := map[string]string{}
	requireID(fields, "legalEntityId", req.LegalEntityID)
	requireID(fields, "workerId", req.WorkerID)
	amount := positiveMoney(fields, "amount", req.Amount)
	if !validCurrency(req.Currency) {
		fields["currency"] = "must be a three-letter upper-case ISO 4217 code"
	} else if _, bad := fields["amount"]; !bad && !minorUnitsFit(amount, req.Currency) {
		fields["amount"] = "has more decimals than the currency allows"
	}
	if n := len(req.Purpose); n == 0 || n > 500 {
		fields["purpose"] = "is required and at most 500 characters"
	}
	if req.TravelRequisitionID != "" {
		requireID(fields, "travelRequisitionId", req.TravelRequisitionID)
	}
	tripEnd := parseDate(fields, "tripEnd", req.TripEnd)
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	adv, replayed, err := a.travel.RequestAdvance(r.Context(), key, hashOf(req), store.NewAdvance{
		LegalEntityID: req.LegalEntityID, WorkerID: req.WorkerID, Amount: amount, Currency: req.Currency,
		Purpose: req.Purpose, TravelRequisitionID: req.TravelRequisitionID, TripEnd: tripEnd,
	}, store.AdvancePolicy{MaxAmount: defaultAdvanceMax, DueDays: defaultAdvanceLim}, a.today())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !replayed {
		a.advancesRequested.Add(1)
	}
	a.writeAdvance(w, adv, replayed, http.StatusCreated)
}

func (a *API) listAdvances(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	fields := map[string]string{}
	limit, after := pageQuery(fields, r)
	f := store.AdvanceFilter{WorkerID: optionalID(fields, r, "workerId"), Status: r.URL.Query().Get("status")}
	if f.Status != "" && !advanceStatuses[f.Status] {
		fields["status"] = "must be DRAFT, SUBMITTED, APPROVED, REJECTED, PAID, PARTIALLY_SETTLED or SETTLED"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	advs, next, err := a.travel.ListAdvances(r.Context(), f, after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]cashAdvanceDTO, 0, len(advs))
	for _, adv := range advs {
		items = append(items, a.toAdvanceDTO(adv))
	}
	httpx.JSON(w, http.StatusOK, advancePage{Items: items, NextCursor: next})
}

func (a *API) getAdvance(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	adv, err := a.travel.GetAdvance(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.writeAdvance(w, adv, false, http.StatusOK)
}

type decideRequest struct {
	Decision string `json:"decision"`
}

// decision validates an APPROVE / REJECT body.
func decision(w http.ResponseWriter, r *http.Request) (decideRequest, bool) {
	var req decideRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return req, false
	}
	if req.Decision != "APPROVE" && req.Decision != "REJECT" {
		refuse(w, r, map[string]string{"decision": "must be APPROVE or REJECT"})
		return req, false
	}
	return req, true
}

func (a *API) decideAdvance(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	req, ok := decision(w, r)
	if !ok {
		return
	}
	c, ok := command(w, r, "advance.decide", id, req)
	if !ok {
		return
	}
	adv, replayed, err := a.travel.DecideAdvance(r.Context(), id, req.Decision == "APPROVE", c)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.writeAdvance(w, adv, replayed, http.StatusOK)
}

type payAdvanceRequest struct {
	PrepaymentID string `json:"prepaymentId"`
}

// payAdvance records the wave-18 AP prepayment that paid an APPROVED advance. Until AP
// publishes its prepayment events this is the entry point the AP side (or an expense
// administrator) calls.
func (a *API) payAdvance(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req payAdvanceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	requireID(fields, "prepaymentId", req.PrepaymentID)
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	c, ok := command(w, r, "advance.pay", id, req)
	if !ok {
		return
	}
	adv, replayed, err := a.travel.PayAdvance(r.Context(), id, req.PrepaymentID, c)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.writeAdvance(w, adv, replayed, http.StatusOK)
}

type returnAdvanceRequest struct {
	Amount string `json:"amount"`
}

func (a *API) returnAdvance(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req returnAdvanceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	amount := positiveMoney(fields, "amount", req.Amount)
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	c, ok := command(w, r, "advance.return", id, req)
	if !ok {
		return
	}
	adv, replayed, err := a.travel.ReturnAdvance(r.Context(), id, amount, c)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.writeAdvance(w, adv, replayed, http.StatusOK)
}

func (a *API) writeAdvance(w http.ResponseWriter, adv store.Advance, replayed bool, status int) {
	writeVersioned(w, adv.Version, replayed, status, a.toAdvanceDTO(adv))
}

func (a *API) toAdvanceDTO(adv store.Advance) cashAdvanceDTO {
	return cashAdvanceDTO{
		ID: adv.ID, LegalEntityID: adv.LegalEntityID, WorkerID: adv.WorkerID,
		Amount: fixed(adv.Amount, adv.Currency), Currency: adv.Currency, Purpose: adv.Purpose,
		TravelRequisitionID: optional(adv.TravelRequisitionID), TripEnd: adv.TripEnd.Format(dateLayout),
		DueDate: adv.DueDate.Format(dateLayout), Overdue: adv.Overdue(a.today()), Status: adv.Status,
		Paid: fixed(adv.Paid, adv.Currency), Settled: fixed(adv.Settled, adv.Currency),
		Returned: fixed(adv.Returned, adv.Currency), Recovered: fixed(adv.Recovered, adv.Currency),
		Open: fixed(adv.Domain().Open(), adv.Currency), APPrepaymentID: optional(adv.APPrepaymentID),
		Version: adv.Version, CreatedAt: adv.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// ---- travel requisitions (FR-43.16) ----

type estimateDTO struct {
	Category string `json:"category"`
	Amount   string `json:"amount"`
}

type requisitionRequest struct {
	LegalEntityID string              `json:"legalEntityId"`
	WorkerID      string              `json:"workerId"`
	Purpose       string              `json:"purpose"`
	Currency      string              `json:"currency"`
	TripFrom      string              `json:"tripFrom"`
	TripTo        string              `json:"tripTo"`
	Destinations  []store.Destination `json:"destinations"`
	Estimates     []estimateDTO       `json:"estimates"`
}

type requisitionDTO struct {
	ID             string              `json:"id"`
	LegalEntityID  string              `json:"legalEntityId"`
	WorkerID       string              `json:"workerId"`
	Purpose        string              `json:"purpose"`
	Currency       string              `json:"currency"`
	Status         string              `json:"status"`
	TripFrom       string              `json:"tripFrom"`
	TripTo         string              `json:"tripTo"`
	Destinations   []store.Destination `json:"destinations"`
	Estimates      []estimateDTO       `json:"estimates"`
	EstimatedTotal string              `json:"estimatedTotal"`
	Version        int                 `json:"version"`
	CreatedAt      string              `json:"createdAt"`
	SubmittedAt    *string             `json:"submittedAt"`
	DecidedAt      *string             `json:"decidedAt"`
}

type requisitionPage struct {
	Items      []requisitionDTO `json:"items"`
	NextCursor string           `json:"nextCursor"`
}

func (a *API) createRequisition(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req requisitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Purpose = strings.TrimSpace(req.Purpose)
	fields := map[string]string{}
	requireID(fields, "legalEntityId", req.LegalEntityID)
	requireID(fields, "workerId", req.WorkerID)
	if n := len(req.Purpose); n == 0 || n > 500 {
		fields["purpose"] = "is required and at most 500 characters"
	}
	currencyOK := validCurrency(req.Currency)
	if !currencyOK {
		fields["currency"] = "must be a three-letter upper-case ISO 4217 code"
	}
	from := parseDate(fields, "tripFrom", req.TripFrom)
	to := parseDate(fields, "tripTo", req.TripTo)
	if fields["tripFrom"] == "" && fields["tripTo"] == "" && to.Before(from) {
		fields["tripTo"] = "must not be before tripFrom"
	}
	if n := len(req.Destinations); n == 0 || n > maxDestinations {
		fields["destinations"] = fmt.Sprintf("must hold 1 to %d destinations", maxDestinations)
	}
	dests := make([]store.Destination, 0, len(req.Destinations))
	for i, d := range req.Destinations {
		d.Country, d.City = strings.TrimSpace(d.Country), strings.TrimSpace(d.City)
		if d.Country == "" || d.City == "" || len(d.Country) > maxPlaceLength || len(d.City) > maxPlaceLength {
			fields[fmt.Sprintf("destinations[%d]", i)] = fmt.Sprintf("country and city are required, at most %d characters each", maxPlaceLength)
		}
		dests = append(dests, d)
	}
	if n := len(req.Estimates); n == 0 || n > maxEstimates {
		fields["estimates"] = fmt.Sprintf("must hold 1 to %d estimate lines", maxEstimates)
	}
	estimates := make([]store.Estimate, 0, len(req.Estimates))
	for i, e := range req.Estimates {
		prefix := fmt.Sprintf("estimates[%d].", i)
		e.Category = strings.ToUpper(strings.TrimSpace(e.Category))
		if e.Category == "" || len(e.Category) > maxCategoryLength {
			fields[prefix+"category"] = fmt.Sprintf("is required and at most %d characters", maxCategoryLength)
		}
		amount := money(fields, prefix+"amount", e.Amount)
		if _, bad := fields[prefix+"amount"]; !bad && currencyOK && !minorUnitsFit(amount, req.Currency) {
			fields[prefix+"amount"] = "has more decimals than the currency allows"
		}
		estimates = append(estimates, store.Estimate{Category: e.Category, Amount: amount})
	}
	total := decimal.Zero
	for _, e := range estimates {
		total = total.Add(e.Amount)
	}
	if total.GreaterThan(maxEstimatedTotal) {
		fields["estimates"] = "must not add up to more than " + maxEstimatedTotal.String()
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	rq, replayed, err := a.travel.CreateRequisition(r.Context(), key, hashOf(req), store.NewRequisition{
		LegalEntityID: req.LegalEntityID, WorkerID: req.WorkerID, Purpose: req.Purpose, Currency: req.Currency,
		TripFrom: from, TripTo: to, Destinations: dests, Estimates: estimates,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !replayed {
		a.requisitionsCreated.Add(1)
	}
	writeRequisition(w, rq, replayed, http.StatusCreated)
}

func (a *API) listRequisitions(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	fields := map[string]string{}
	limit, after := pageQuery(fields, r)
	f := store.RequisitionFilter{WorkerID: optionalID(fields, r, "workerId"), Status: r.URL.Query().Get("status")}
	if f.Status != "" && !requisitionStates[f.Status] {
		fields["status"] = "must be DRAFT, SUBMITTED, APPROVED, REJECTED or CLOSED"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	rqs, next, err := a.travel.ListRequisitions(r.Context(), f, after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]requisitionDTO, 0, len(rqs))
	for _, rq := range rqs {
		items = append(items, toRequisitionDTO(rq))
	}
	httpx.JSON(w, http.StatusOK, requisitionPage{Items: items, NextCursor: next})
}

func (a *API) getRequisition(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	rq, err := a.travel.GetRequisition(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeRequisition(w, rq, false, http.StatusOK)
}

func (a *API) submitRequisition(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	c, ok := command(w, r, "requisition.submit", id, nil)
	if !ok {
		return
	}
	rq, replayed, err := a.travel.SubmitRequisition(r.Context(), id, c)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeRequisition(w, rq, replayed, http.StatusOK)
}

func (a *API) decideRequisition(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	req, ok := decision(w, r)
	if !ok {
		return
	}
	c, ok := command(w, r, "requisition.decide", id, req)
	if !ok {
		return
	}
	rq, replayed, err := a.travel.DecideRequisition(r.Context(), id, req.Decision == "APPROVE", c)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeRequisition(w, rq, replayed, http.StatusOK)
}

func writeRequisition(w http.ResponseWriter, rq store.Requisition, replayed bool, status int) {
	writeVersioned(w, rq.Version, replayed, status, toRequisitionDTO(rq))
}

func toRequisitionDTO(rq store.Requisition) requisitionDTO {
	ests := make([]estimateDTO, 0, len(rq.Estimates))
	for _, e := range rq.Estimates {
		ests = append(ests, estimateDTO{Category: e.Category, Amount: fixed(e.Amount, rq.Currency)})
	}
	dests := rq.Destinations
	if dests == nil {
		dests = []store.Destination{}
	}
	return requisitionDTO{
		ID: rq.ID, LegalEntityID: rq.LegalEntityID, WorkerID: rq.WorkerID, Purpose: rq.Purpose,
		Currency: rq.Currency, Status: rq.Status, TripFrom: rq.TripFrom.Format(dateLayout),
		TripTo: rq.TripTo.Format(dateLayout), Destinations: dests, Estimates: ests,
		EstimatedTotal: fixed(rq.Estimated, rq.Currency), Version: rq.Version,
		CreatedAt: rq.CreatedAt.UTC().Format(time.RFC3339), SubmittedAt: timestamp(rq.SubmittedAt), DecidedAt: timestamp(rq.DecidedAt),
	}
}

// ---- card feed import (FR-43.13) ----

type cardImportRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Format        string `json:"format"`
	Content       string `json:"content"`
	// Cardholders maps a card's last four digits to the worker who holds it. Rows of
	// a card not in the map are quarantined.
	Cardholders map[string]string `json:"cardholders"`
}

type cardImportDTO struct {
	ID            string        `json:"id"`
	LegalEntityID string        `json:"legalEntityId"`
	Format        string        `json:"format"`
	FileHash      string        `json:"fileHash"`
	Imported      int           `json:"imported"`
	Quarantined   []rowErrorDTO `json:"quarantined"`
	CreatedAt     string        `json:"createdAt"`
}

// importCardFeed parses a CSV or OFX statement, assigns each row to the worker who
// holds its card and stores the import with its quarantine (rows the parser rejected
// and rows of unknown cards). The file hash is the import's natural key: the same
// file again is EXP-CARD-002.
func (a *API) importCardFeed(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req cardImportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	requireID(fields, "legalEntityId", req.LegalEntityID)
	if req.Content == "" || len(req.Content) > maxFeedBytes {
		fields["content"] = fmt.Sprintf("must hold 1 to %d bytes", maxFeedBytes)
	}
	if n := len(req.Cardholders); n == 0 || n > maxCardholders {
		fields["cardholders"] = fmt.Sprintf("must map 1 to %d cards to their workers", maxCardholders)
	}
	for last4, worker := range req.Cardholders {
		if !last4Pattern.MatchString(last4) {
			fields["cardholders"] = "keys must be the last four digits of a card"
		} else if !ids.Valid(worker) {
			fields["cardholders."+last4] = "must be a worker id (UUID)"
		}
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	feed, err := domain.ParseCardFeed(req.Format, []byte(req.Content))
	switch {
	case errors.Is(err, domain.ErrUnsupportedFormat):
		refuse(w, r, map[string]string{"format": "must be CSV or OFX"})
		return
	case errors.Is(err, domain.ErrMissingColumns):
		refuse(w, r, map[string]string{"content": "CSV header needs card_last4, txn_date, post_date, merchant, amount and currency"})
		return
	case errors.Is(err, domain.ErrMalformedFeed):
		refuse(w, r, map[string]string{"content": "is not an OFX statement"})
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	}
	rows, quarantined := assignFeed(feed, req.Cardholders)
	imp, replayed, err := a.travel.ImportCardFeed(r.Context(), key, hashOf(req), store.NewImport{
		LegalEntityID: req.LegalEntityID, Format: feed.Format, FileHash: feed.FileHash, Rows: rows, Quarantined: quarantined,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := cardImportDTO{ID: imp.ID, LegalEntityID: imp.LegalEntityID, Format: imp.Format, FileHash: imp.FileHash,
		Imported: imp.Imported, Quarantined: make([]rowErrorDTO, 0, len(imp.Quarantined)), CreatedAt: imp.CreatedAt.UTC().Format(time.RFC3339)}
	for _, e := range imp.Quarantined {
		out.Quarantined = append(out.Quarantined, rowErrorDTO{Line: e.Line, Reason: e.Reason})
	}
	status := http.StatusCreated
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	} else {
		a.cardImports.Add(1)
	}
	httpx.JSON(w, status, out)
}

// assignFeed gives each parsed row to the worker holding its card. Rows the parser
// rejected and rows of a card with no cardholder are quarantined, in file order.
func assignFeed(feed domain.Feed, cardholders map[string]string) ([]store.NewCardRow, []store.RowError) {
	rows := make([]store.NewCardRow, 0, len(feed.Transactions))
	quarantined := make([]store.RowError, 0, len(feed.Rejected))
	for _, e := range feed.Rejected {
		quarantined = append(quarantined, store.RowError{Line: e.Line, Reason: e.Reason})
	}
	for _, t := range feed.Transactions {
		worker, ok := cardholders[t.CardLast4]
		if !ok {
			quarantined = append(quarantined, store.RowError{Line: t.Line, Reason: "card ending " + t.CardLast4 + " has no cardholder"})
			continue
		}
		rows = append(rows, store.NewCardRow{
			Line: t.Line, WorkerID: worker, CardLast4: t.CardLast4, TxnDate: t.TxnDate, PostDate: t.PostDate,
			Merchant: t.Merchant, MCC: t.MCC, Amount: t.Amount, Currency: t.Currency,
		})
	}
	sortRowErrors(quarantined)
	return rows, quarantined
}

func sortRowErrors(rs []store.RowError) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Line < rs[j-1].Line; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

type cardTxnDTO struct {
	ID        string `json:"id"`
	ImportID  string `json:"importId"`
	WorkerID  string `json:"workerId"`
	Line      int    `json:"line"`
	CardLast4 string `json:"cardLast4"`
	TxnDate   string `json:"txnDate"`
	PostDate  string `json:"postDate"`
	Merchant  string `json:"merchant"`
	MCC       string `json:"mcc"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
}

type cardTxnPage struct {
	Items      []cardTxnDTO `json:"items"`
	NextCursor string       `json:"nextCursor"`
}

func (a *API) listCardTransactions(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) || !a.travelReady(w, r) {
		return
	}
	fields := map[string]string{}
	limit, after := pageQuery(fields, r)
	f := store.CardFilter{WorkerID: optionalID(fields, r, "workerId"), Status: r.URL.Query().Get("status")}
	if f.Status != "" && !cardStatuses[f.Status] {
		fields["status"] = "must be UNASSIGNED, ASSIGNED, DISPUTED or PERSONAL"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	txns, next, err := a.travel.ListCardTransactions(r.Context(), f, after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]cardTxnDTO, 0, len(txns))
	for _, c := range txns {
		items = append(items, cardTxnDTO{
			ID: c.ID, ImportID: c.ImportID, WorkerID: c.WorkerID, Line: c.Line, CardLast4: c.CardLast4,
			TxnDate: c.TxnDate.Format(dateLayout), PostDate: c.PostDate.Format(dateLayout), Merchant: c.Merchant,
			MCC: c.MCC, Amount: fixed(c.Amount, c.Currency), Currency: c.Currency, Status: c.Status,
		})
	}
	httpx.JSON(w, http.StatusOK, cardTxnPage{Items: items, NextCursor: next})
}

// ---- shared helpers ----

// writeVersioned writes a versioned resource with its ETag; a replay answers 200 with
// Idempotent-Replayed.
func writeVersioned(w http.ResponseWriter, version int, replayed bool, status int, body any) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(version)))
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	httpx.JSON(w, status, body)
}

// positiveMoney parses a decimal amount that must be greater than zero.
func positiveMoney(fields map[string]string, field, raw string) decimal.Decimal {
	d := money(fields, field, raw)
	if _, bad := fields[field]; !bad && !d.IsPositive() {
		fields[field] = "must be greater than zero"
	}
	return d
}

// minorUnitsFit is true when the amount has no more decimals than the currency's
// minor unit (platform money), so a stored amount is never silently rounded.
func minorUnitsFit(d decimal.Decimal, currency string) bool {
	return d.Equal(d.Round(platformmoney.MinorUnits(currency)))
}

// optionalID reads an optional UUID query parameter; empty means no filter.
func optionalID(fields map[string]string, r *http.Request, name string) string {
	v := r.URL.Query().Get(name)
	if v != "" && !ids.Valid(v) {
		fields[name] = "must be a UUID"
	}
	return v
}

// pageQuery reads the limit and cursor query parameters of the list routes.
func pageQuery(fields map[string]string, r *http.Request) (int, string) {
	q := r.URL.Query()
	limit := defaultPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
		} else {
			limit = n
		}
	}
	after := q.Get("cursor")
	if after != "" && !ids.Valid(after) {
		fields["cursor"] = "must be a nextCursor value from an earlier page"
	}
	return limit, after
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timestamp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
