package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	dutyCollections = "CollectionsMaintain"
	maxOwner        = 120
	maxNote         = 2000
)

var (
	reasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z_]{1,31}$`)
	disputeStatuses   = map[string]bool{"OPEN": true, "UNDER_REVIEW": true, "RESOLVED_CREDIT": true, "RESOLVED_PAID": true,
		"RESOLVED_WRITE_OFF": true, "REJECTED": true}
)

// collectionsRoutes mounts disputes and promises to pay (FR-30.9).
func (a *API) collectionsRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyCollections)).Post("/disputes", a.openDispute)
	r.With(authz.RequireDuty(dutyView)).Get("/disputes", a.listDisputes)
	r.With(authz.RequireDuty(dutyView)).Get("/disputes/{ref}", a.getDispute)
	// :review and :resolve share one route; disputeAction dispatches and checks the duty.
	r.Post("/disputes/{ref}", a.disputeAction)
	r.With(authz.RequireDuty(dutyCollections)).Post("/promises", a.createPromise)
	r.With(authz.RequireDuty(dutyView)).Get("/promises", a.listPromises)
}

type openDisputeRequest struct {
	LegalEntityID  string          `json:"legalEntityId"`
	OpenItemID     string          `json:"openItemId"`
	ReasonCode     string          `json:"reasonCode"`
	DisputedAmount decimal.Decimal `json:"disputedAmount"`
	Owner          string          `json:"owner"`
}

// openDispute handles POST /disputes.
func (a *API) openDispute(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req openDisputeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Owner = strings.TrimSpace(req.Owner)
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.OpenItemID) {
		fields["openItemId"] = "must be a UUID"
	}
	if !reasonCodePattern.MatchString(req.ReasonCode) {
		fields["reasonCode"] = "must be an upper-case code such as PRICE or DAMAGE"
	}
	if !req.DisputedAmount.IsPositive() {
		fields["disputedAmount"] = "must be a positive decimal"
	}
	if n := len(req.Owner); n < 1 || n > maxOwner {
		fields["owner"] = fmt.Sprintf("must be 1 to %d characters", maxOwner)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.OpenDispute(r.Context(), store.Dispute{LegalEntityID: req.LegalEntityID, OpenItemID: req.OpenItemID,
		ReasonCode: req.ReasonCode, DisputedAmount: req.DisputedAmount, Owner: req.Owner}, key)
	if err != nil {
		a.fail("open dispute", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// listDisputes handles GET /disputes?status=&customer=&openItem=. status=active
// keeps OPEN and UNDER_REVIEW.
func (a *API) listDisputes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.DisputeFilter{CustomerID: q.Get("customer"), OpenItemID: q.Get("openItem")}
	switch status := q.Get("status"); {
	case status == "active":
		f.Active = true
	case status == "" || disputeStatuses[status]:
		f.Status = status
	default:
		badQuery(w, r, "status", "must be active or a dispute status")
		return
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	if f.OpenItemID != "" && !ids.Valid(f.OpenItemID) {
		badQuery(w, r, "openItem", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDisputes(r.Context(), f, limit)
	if err != nil {
		a.fail("list disputes", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getDispute handles GET /disputes/{id}.
func (a *API) getDispute(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDisputeNotFound, "dispute not found"))
		return
	}
	out, err := a.store.GetDispute(r.Context(), id)
	if err != nil {
		a.fail("get dispute", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type resolveRequest struct {
	Resolution string  `json:"resolution"`
	Note       *string `json:"note"`
}

// disputeAction handles POST /disputes/{id}:review and POST /disputes/{id}:resolve.
func (a *API) disputeAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || (action != "review" && action != "resolve") {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDisputeNotFound, "unknown dispute or action"))
		return
	}
	if !require(w, r, dutyCollections) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	to := domain.DisputeUnderReview
	var note *string
	if action == "resolve" {
		var req resolveRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		status, ok := domain.ResolutionStatus(req.Resolution)
		if !ok {
			writeFields(w, r, map[string]string{"resolution": "must be CREDIT, PAID, WRITE_OFF or REJECTED"})
			return
		}
		if req.Note != nil && len(*req.Note) > maxNote {
			writeFields(w, r, map[string]string{"note": fmt.Sprintf("must be at most %d characters", maxNote)})
			return
		}
		to, note = status, req.Note
	}
	out, err := a.store.TransitionDispute(r.Context(), id, version, to, note)
	if err != nil {
		a.fail("transition dispute", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type promiseRequest struct {
	LegalEntityID string          `json:"legalEntityId"`
	OpenItemID    string          `json:"openItemId"`
	Amount        decimal.Decimal `json:"amount"`
	PromiseDate   string          `json:"promiseDate"`
}

// createPromise handles POST /promises. A promise dated in the past would never
// hold, so the date must be today or later. "Today" allows one day before the
// UTC date, for a clerk west of UTC whose evening is already tomorrow in UTC.
func (a *API) createPromise(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req promiseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.OpenItemID) {
		fields["openItemId"] = "must be a UUID"
	}
	if !req.Amount.IsPositive() {
		fields["amount"] = "must be a positive decimal"
	}
	if d, err := store.ParseDay(req.PromiseDate); err != nil {
		fields["promiseDate"] = "must be a YYYY-MM-DD date"
	} else if d.Before(time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)) {
		fields["promiseDate"] = "must be today or later"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.CreatePromise(r.Context(), store.Promise{LegalEntityID: req.LegalEntityID, OpenItemID: req.OpenItemID,
		Amount: req.Amount, PromiseDate: req.PromiseDate}, key)
	if err != nil {
		a.fail("create promise", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// listPromises handles GET /promises?customer=&openItem=.
func (a *API) listPromises(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.PromiseFilter{CustomerID: q.Get("customer"), OpenItemID: q.Get("openItem")}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	if f.OpenItemID != "" && !ids.Valid(f.OpenItemID) {
		badQuery(w, r, "openItem", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListPromises(r.Context(), f, limit)
	if err != nil {
		a.fail("list promises", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// requiredKey reads the Idempotency-Key a create command must carry (1 to 200 characters).
func requiredKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > maxKeyLength {
		writeFields(w, r, map[string]string{idempotencyKey: fmt.Sprintf("is required, 1 to %d characters", maxKeyLength)})
		return "", false
	}
	return key, true
}

// writeFields answers 400 COM-0001 with per-field errors.
func writeFields(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}
