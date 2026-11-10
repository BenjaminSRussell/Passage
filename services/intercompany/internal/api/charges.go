package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

type postChargeRequest struct {
	chargeRequest
	SourceID    string         `json:"sourceId"`
	Version     int            `json:"version"`
	PostingDate string         `json:"postingDate"`
	Entities    []entityTarget `json:"entities"`
}

type chargeRecordResponse struct {
	ID                  string    `json:"id"`
	SourceID            string    `json:"sourceId"`
	Version             int       `json:"version"`
	LendingEntity       string    `json:"lendingEntity"`
	BorrowingEntity     string    `json:"borrowingEntity"`
	Category            string    `json:"category"`
	ResourceRole        string    `json:"resourceRole,omitempty"`
	TransactionDate     string    `json:"transactionDate"`
	Quantity            string    `json:"quantity"`
	UnitCost            string    `json:"unitCost"`
	Currency            string    `json:"currency"`
	Status              string    `json:"status"`
	TransferPriceMethod string    `json:"transferPriceMethod,omitempty"`
	Cost                string    `json:"cost,omitempty"`
	Price               string    `json:"price,omitempty"`
	Margin              string    `json:"margin,omitempty"`
	JournalID           string    `json:"journalId,omitempty"`
	FailureCode         string    `json:"failureCode,omitempty"`
	CreatedBy           string    `json:"createdBy"`
	CreatedAt           time.Time `json:"createdAt"`
}

type chargeListResponse struct {
	Items      []chargeRecordResponse `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

func toChargeResponse(c domain.ChargeRecord) chargeRecordResponse {
	places := money.MinorUnits(c.Currency)
	out := chargeRecordResponse{ID: c.ID, SourceID: c.SourceID, Version: c.Version, LendingEntity: c.Lender, BorrowingEntity: c.Borrower, Category: c.Category,
		ResourceRole: c.Role, TransactionDate: c.TransactionDate.Format(dateLayout), Quantity: c.Quantity.String(), UnitCost: c.UnitCost.String(), Currency: c.Currency,
		Status: string(c.Status), TransferPriceMethod: c.Method, JournalID: c.JournalID, FailureCode: c.FailureCode, CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt.UTC()}
	if c.Status == domain.ChargePosted || c.Status == domain.ChargeCompensated {
		out.Cost, out.Price, out.Margin = c.Cost.StringFixed(places), c.Price.StringFixed(places), c.Margin.StringFixed(places)
	}
	return out
}

// postCharge prices an IC charge from the transfer prices sent with it and
// posts the Journal-mode legs to both entities (FR-40.13). The charge is stored
// per sourceId and version: the same pair sent again replays the stored result,
// and a source with no applicable transfer price stays on suspense (202,
// IC-0821, BR-40.12) until the next version reprices it.
func (a *API) postCharge(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	var req postChargeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := parseCharge(req.chargeRequest)
	f := fieldParser(fields)
	if f == nil {
		f = fieldParser{}
	}
	if len(req.TransferPrices) == 0 {
		delete(f, "transferPrices") // a source without any price goes to suspense
	}
	source := strings.TrimSpace(req.SourceID)
	if source == "" || len(source) > 128 {
		f["sourceId"] = "is required, at most 128 characters"
	}
	if req.Version < 1 {
		f["version"] = "must be 1 or higher"
	}
	date := f.date("postingDate", req.PostingDate)
	targets := parseTargets(req.Entities, f)
	if !validation(w, r, f) {
		return
	}
	hash, err := store.HashOf("charge", req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rec, existing, err := a.charges.BeginCharge(r.Context(), domain.ChargeRecord{SourceID: source, Version: req.Version, Lender: in.lender, Borrower: in.borrower,
		Category: in.category, Role: in.role, TransactionDate: in.on, Quantity: in.quantity, UnitCost: in.unitCost, Currency: req.Currency}, hash)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if existing && rec.Status != domain.ChargePending {
		w.Header().Set("Idempotent-Replayed", "true")
		a.respondCharge(w, rec)
		return
	}

	tp, err := domain.ResolveTransferPrice(in.prices, in.lender, in.borrower, in.category, in.role, in.on)
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeTransferPrice {
		done, ferr := a.charges.FinishCharge(r.Context(), rec.ID, domain.ChargeSuspense, "", domain.ChargePricing{}, "", de.Code)
		if ferr != nil {
			httpx.Fail(w, r, problemFrom(ferr))
			return
		}
		a.chargesSuspended.Add(1)
		a.respondCharge(w, done)
		return
	}
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	pricing, err := domain.PriceWithTransfer(tp, in.quantity, in.unitCost)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	j, err := domain.ChargeJournal(in.matrix, ids.New(), in.lender, in.borrower, req.CostAccount, req.RecoveryAccount, req.MarginAccount, pricing)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	jrec, _, perr := a.poster.Post(r.Context(), posting.Input{Source: domain.SourceCharge, SourceRef: rec.ID, Key: "charge:" + rec.ID, Hash: hash, Journal: j, Targets: targets, PostingDate: date})
	status := domain.ChargePosted
	switch {
	case perr == nil && jrec.Status == domain.PostingCompensated:
		status = domain.ChargeCompensated
	case perr == nil:
	case errors.As(perr, &de) && de.Code == domain.CodeSecondSideFailed:
		status = domain.ChargeCompensated
	default:
		httpx.Fail(w, r, postProblem(jrec, perr))
		return
	}
	failure := ""
	if status == domain.ChargeCompensated {
		failure = domain.CodeSecondSideFailed
	}
	done, err := a.charges.FinishCharge(r.Context(), rec.ID, status, tp.Method, pricing, jrec.ID, failure)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if status == domain.ChargeCompensated {
		httpx.Fail(w, r, postProblem(jrec, &domain.Error{Code: domain.CodeSecondSideFailed, Detail: jrec.FailureDetail + "; the entries already posted were reversed"}))
		return
	}
	a.chargesPosted.Add(1)
	a.respondCharge(w, done)
}

func (a *API) respondCharge(w http.ResponseWriter, c domain.ChargeRecord) {
	w.Header().Set("Location", "/v1/interco/charges/"+c.ID)
	switch c.Status {
	case domain.ChargeSuspense:
		httpx.JSON(w, http.StatusAccepted, toChargeResponse(c))
	default:
		httpx.JSON(w, http.StatusCreated, toChargeResponse(c))
	}
}

func (a *API) listCharges(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	filter := store.ChargeFilter{Status: q.Get("status"), SourceID: strings.TrimSpace(q.Get("sourceId"))}
	switch filter.Status {
	case "", "pending", "posted", "compensated", "suspense":
	default:
		fields["status"] = "must be pending, posted, compensated or suspense"
	}
	after, limit := pageParams(w, r, fields)
	if !validation(w, r, fields) {
		return
	}
	page, err := a.charges.ListCharges(r.Context(), filter, after, limit)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out := chargeListResponse{Items: make([]chargeRecordResponse, 0, len(page.Items)), NextCursor: page.Next}
	for _, c := range page.Items {
		out.Items = append(out.Items, toChargeResponse(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getCharge(w http.ResponseWriter, r *http.Request) {
	if !a.postingReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeChargeNotFound, "no intercompany charge with id "+id))
		return
	}
	c, err := a.charges.GetCharge(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toChargeResponse(c))
}
