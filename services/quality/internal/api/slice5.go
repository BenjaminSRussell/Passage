package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

// dutyCoA generates certificates of analysis (section 6.33.17).
const dutyCoA = "quality.coa.generate"

// --- stability study lifecycle (FR-33.16) -----------------------------------

type studyTransitionRequest struct {
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// transitionStudy moves a study PLANNED -> ACTIVE -> COMPLETED, or to TERMINATED
// (with a reason) from PLANNED or ACTIVE. It requires If-Match with the study's
// ETag: 428 when absent, 412 when stale.
func (a *API) transitionStudy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeStudyMissing, "stability study not found"))
		return
	}
	var req studyTransitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	now := time.Now()
	st, err := a.store.TransitionStudy(r.Context(), id, version, func(cur domain.Study) (domain.Study, error) {
		next, err := cur.Transition(req.To, req.Reason, p.UserID, now)
		return next, problemFrom(err)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, st.Version)
	httpx.JSON(w, http.StatusOK, st)
}

// --- source inspection at receipt (FR-33.19, BR-33.16) ----------------------

// checkReceipt is called by receiving for each receipt line of a PO line and
// batch before it creates a receiving quality order. The answer uses up the
// covered quantity, so it takes an Idempotency-Key and quality.order.maintain.
// skip=true means no receiving order (QM-0128); otherwise receiving creates a
// PURCHASE_RECEIPT order for normalQty.
func (a *API) checkReceipt(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req domain.ReceiptCheckInput
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := domain.ValidateReceiptCheck(req); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	req.Batch = strings.TrimSpace(req.Batch)
	p, _ := tenant.FromContext(r.Context())
	now := time.Now()
	c, inserted, err := a.store.CheckReceipt(r.Context(), req, key, func(accepted, coveredBefore decimal.Decimal) (domain.ReceiptCheck, error) {
		c, err := domain.CheckReceipt(req, accepted, coveredBefore, p.UserID, now)
		return c, problemFrom(err)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.receiptChecks.Add(1)
	}
	httpx.JSON(w, statusFor(inserted), c)
}

// listReceiptChecks answers ?poLineId=&batch=&limit=, newest first.
func (a *API) listReceiptChecks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ReceiptCheckFilter{POLineID: q.Get("poLineId"), Batch: strings.TrimSpace(q.Get("batch"))}
	fields := map[string]string{}
	if f.POLineID != "" && !ids.Valid(f.POLineID) {
		fields["poLineId"] = "must be a UUID"
	}
	if len(f.Batch) > 60 {
		fields["batch"] = "must be at most 60 characters"
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	f.Limit = limit
	checks, err := a.store.ListReceiptChecks(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.ReceiptCheck]{Items: checks})
}

// --- certificates of analysis (FR-33.9, BR-33.10) ---------------------------

// generateCoA issues the next CoA revision of a closed order whose decision
// released quantity, for the batch or one shipment. The signature block is the
// decision user. A later revision of the same chain needs a reason.
func (a *API) generateCoA(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req domain.CoARequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := domain.ValidateCoARequest(req); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	order, err := a.store.GetOrder(r.Context(), req.QualityOrderID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	group, err := a.store.GetTestGroup(r.Context(), order.TestGroupID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	results, err := a.store.ListResults(r.Context(), order.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var decision *domain.Decision
	if d, found, err := a.store.GetDecision(r.Context(), order.ID); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if found {
		decision = &d
	}
	p, _ := tenant.FromContext(r.Context())
	now := time.Now()
	doc, inserted, err := a.store.IssueCoA(r.Context(), req, key, func(prev *domain.CoA) (domain.CoA, error) {
		doc, err := domain.BuildCoA(order, group, decision, results, prev, req, p.UserID, now)
		return doc, problemFrom(err)
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.coasIssued.Add(1)
	}
	httpx.JSON(w, statusFor(inserted), doc)
}

// listCoAs returns every CoA revision of an order: the batch chain, then each
// shipment's, oldest revision first.
func (a *API) listCoAs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found"))
		return
	}
	if _, err := a.store.GetOrder(r.Context(), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	docs, err := a.store.ListCoAs(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.CoA]{Items: docs})
}
