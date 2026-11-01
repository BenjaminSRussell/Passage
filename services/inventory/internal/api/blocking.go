package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Blocking and quarantine routes (FR-16.8).
const (
	dutyBlockingWrite = "inventory.blocking.write"
	// dutyBlockingScrap is checked inside the release handler: scrapping writes
	// value off to the adjustment account, which blocking.write alone does not allow.
	dutyBlockingScrap = "inventory.blocking.scrap"
	blockingsPath     = "/v1/inventory/blockings"
	maxNoteLength     = 500
)

func (a *API) registerBlocking(r chi.Router) {
	r.With(authz.RequireDuty(dutyBlockingWrite)).Post(blockingsPath, a.createBlocking)
	r.With(authz.RequireDuty(dutyRead)).Get(blockingsPath, a.listBlockings)
	r.With(authz.RequireDuty(dutyBlockingWrite)).Post(blockingsPath+"/{id}:release", a.releaseBlocking)
	r.With(authz.RequireDuty(dutyBlockingWrite)).Post(blockingsPath+"/{id}:block", a.escalateBlocking)
}

type blockingRequest struct {
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Qty           string            `json:"qty"`
	Unit          string            `json:"unit"`
	TargetStatus  string            `json:"targetStatus"`
	Reason        string            `json:"reason"`
	Note          string            `json:"note"`
	PhysicalDate  string            `json:"physicalDate"`
	// ExpectedReleaseDate is when the hold is expected to end (YYYY-MM-DD).
	ExpectedReleaseDate string `json:"expectedReleaseDate"`
}

type releaseRequest struct {
	Disposition string `json:"disposition"`
}

type blockingResponse struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Qty           string            `json:"qty"`
	Unit          string            `json:"unit"`
	TargetStatus  string            `json:"targetStatus"`
	Reason        string            `json:"reason"`
	Note          string            `json:"note,omitempty"`
	State         string            `json:"state"`
	Disposition   string            `json:"disposition,omitempty"`
	PhysicalDate  string            `json:"physicalDate"`
	Version       int               `json:"version"`
	// ExpectedReleaseDate and ScrapJournalID are omitted when empty.
	ExpectedReleaseDate string `json:"expectedReleaseDate,omitempty"`
	ScrapJournalID      string `json:"scrapJournalId,omitempty"`
}

func (a *API) createBlocking(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req blockingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	qty, err := decimal.NewFromString(req.Qty)
	if err != nil || !qty.IsPositive() {
		fields["qty"] = "must be a positive decimal number"
	}
	if !unitCode.MatchString(req.Unit) {
		fields["unit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
	}
	if !domain.IsRestricted(req.TargetStatus) {
		fields["targetStatus"] = "must be BLOCKED or QUARANTINE"
	}
	if !slices.Contains(domain.BlockReasons, req.Reason) {
		fields["reason"] = "must be one of " + strings.Join(domain.BlockReasons, ", ")
	}
	req.Note = strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(req.Note) > maxNoteLength {
		fields["note"] = "must be at most 500 characters"
	}
	date := time.Now().UTC().Truncate(24 * time.Hour)
	if req.PhysicalDate != "" {
		if date, err = time.Parse(dateLayout, req.PhysicalDate); err != nil {
			fields["physicalDate"] = "must be a date (YYYY-MM-DD)"
		}
	}
	if req.ExpectedReleaseDate != "" {
		if expected, perr := time.Parse(dateLayout, req.ExpectedReleaseDate); perr != nil {
			fields["expectedReleaseDate"] = "must be a date (YYYY-MM-DD)"
		} else if expected.Before(date) {
			fields["expectedReleaseDate"] = "must not be before the blocking date"
		}
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	b, replayed, err := a.store.CreateBlocking(r.Context(), store.BlockingInput{IdempotencyKey: key, LegalEntityID: req.LegalEntityID,
		ProductID: req.ProductID, Dims: domain.Dimensions(req.Dimensions), Qty: qty, Unit: req.Unit, Target: req.TargetStatus,
		Reason: req.Reason, Note: req.Note, PhysicalDate: date, ExpectedReleaseDate: req.ExpectedReleaseDate})
	if err != nil {
		a.failed(w, r, "create blocking", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeBlocking(w, status, b)
}

// listBlockings is the blocking and quarantine inquiry, newest first.
func (a *API) listBlockings(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.BlockingFilter{State: q.Get("state"), ProductID: q.Get("product")}
	fields := map[string]string{}
	if f.State != "" && f.State != domain.BlockingActive && f.State != domain.BlockingReleased {
		fields["state"] = "must be ACTIVE or RELEASED"
	}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["product"] = "must be a UUID"
	}
	f.Limit = pageLimit(r, 200, 200, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	found, err := a.store.ListBlockings(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list blockings", err)
		return
	}
	items := make([]blockingResponse, 0, len(found))
	for _, b := range found {
		items = append(items, toBlockingResponse(b))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// releaseBlocking ends a blocking with a disposition; the version comes from If-Match.
func (a *API) releaseBlocking(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeBlockingNotFound, "blocking not found"))
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req releaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	disposition := strings.ToUpper(strings.TrimSpace(req.Disposition))
	if p, _ := tenant.FromContext(r.Context()); disposition == domain.DispositionScrap && !p.HasDuty(dutyBlockingScrap) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeScrapDuty, "scrapping blocked stock writes it off and needs the "+dutyBlockingScrap+" duty"))
		return
	}
	b, err := a.store.ReleaseBlocking(r.Context(), id, version, disposition, time.Now().UTC().Truncate(24*time.Hour), a.post)
	if err != nil {
		a.failed(w, r, "release blocking", err)
		return
	}
	writeBlocking(w, http.StatusOK, b)
}

// escalateBlocking moves an active quarantine to blocked (QUARANTINE to
// BLOCKED); the version comes from If-Match.
func (a *API) escalateBlocking(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeBlockingNotFound, "blocking not found"))
		return
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	b, err := a.store.EscalateBlocking(r.Context(), id, version, time.Now().UTC().Truncate(24*time.Hour))
	if err != nil {
		a.failed(w, r, "escalate blocking", err)
		return
	}
	writeBlocking(w, http.StatusOK, b)
}

func toBlockingResponse(b domain.Blocking) blockingResponse {
	return blockingResponse{ID: b.ID, LegalEntityID: b.LegalEntityID, ProductID: b.ProductID, Dimensions: b.Dims, Qty: b.Qty.String(), Unit: b.Unit,
		TargetStatus: b.Target, Reason: b.Reason, Note: b.Note, State: b.State, Disposition: b.Disposition, PhysicalDate: b.PhysicalDate, Version: b.Version,
		ExpectedReleaseDate: b.ExpectedReleaseDate, ScrapJournalID: b.ScrapJournalID}
}

func writeBlocking(w http.ResponseWriter, status int, b domain.Blocking) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(b.Version)))
	httpx.JSON(w, status, toBlockingResponse(b))
}
