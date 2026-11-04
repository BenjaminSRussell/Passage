package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// Tag counting routes (FR-16.18). Generating, entering, voiding and
// transferring tags is CycleCountMaintain; marking open tags MISSING at the
// transfer also needs CycleCountApprove (the supervisor of BR-16.15).
const countTagsPath = "/v1/inventory/count-tags"

type generateTagsRequest struct {
	Count int `json:"count"`
}

type transferTagsRequest struct {
	MarkOpenMissing bool `json:"markOpenMissing"`
}

type useTagRequest struct {
	ProductID  string            `json:"productId"`
	Dimensions map[string]string `json:"dimensions"`
	CountedQty string            `json:"countedQty"`
}

type tagResponse struct {
	ID         string            `json:"id"`
	TagNumber  string            `json:"tagNumber"`
	PlanID     string            `json:"planId"`
	Status     string            `json:"status"`
	ProductID  string            `json:"productId,omitempty"`
	Dimensions map[string]string `json:"dimensions"`
	CountedQty *string           `json:"countedQty"`
	CountedBy  string            `json:"countedBy,omitempty"`
	SheetID    string            `json:"sheetId,omitempty"`
	Version    int               `json:"version"`
}

func (a *API) registerTags(r chi.Router) {
	r.With(authz.RequireDuty(dutyCountWrite)).Post(countPlansPath+"/{id}:generate-tags", a.generateTags)
	r.With(authz.RequireDuty(dutyRead)).Get(countPlansPath+"/{id}/tags", a.listTags)
	r.With(authz.RequireDuty(dutyCountWrite)).Post(countPlansPath+"/{id}:transfer-tags", a.transferTags)
	r.With(authz.RequireDuty(dutyCountWrite)).Post(countTagsPath+"/{id}:use", a.useTag)
	r.With(authz.RequireDuty(dutyCountWrite)).Post(countTagsPath+"/{id}:void", a.voidTag)
}

// planID checks the tenant and the plan id of a tag route.
func planID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeCountNotFound, "count plan not found"))
		return "", false
	}
	return id, true
}

// tagRoute checks the tenant, the tag id and If-Match of a tag command.
func tagRoute(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	if !validTenant(w, r) {
		return "", 0, false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeTagNotFound, "count tag not found"))
		return "", 0, false
	}
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return "", 0, false
	}
	return id, version, true
}

// generateTags creates a range of OPEN tags for the plan.
func (a *API) generateTags(w http.ResponseWriter, r *http.Request) {
	id, ok := planID(w, r)
	if !ok {
		return
	}
	var req generateTagsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Count < 1 || req.Count > domain.MaxTagRange {
		fields["count"] = fmt.Sprintf("must be between 1 and %d", domain.MaxTagRange)
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	tags, replayed, err := a.store.GenerateTags(r.Context(), id, key, req.Count)
	if err != nil {
		a.failed(w, r, "generate count tags", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeTagList(w, status, tags)
}

func (a *API) listTags(w http.ResponseWriter, r *http.Request) {
	id, ok := planID(w, r)
	if !ok {
		return
	}
	status := domain.TagStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !slices.Contains(domain.TagStatuses, status) {
		fieldProblem(w, r, map[string]string{"status": "must be OPEN, USED, VOID or MISSING"})
		return
	}
	tags, err := a.store.ListTags(r.Context(), id, status)
	if err != nil {
		a.failed(w, r, "list count tags", err)
		return
	}
	writeTagList(w, http.StatusOK, tags)
}

// transferTags moves the plan's USED tags to a new count sheet.
func (a *API) transferTags(w http.ResponseWriter, r *http.Request) {
	id, ok := planID(w, r)
	if !ok {
		return
	}
	var req transferTagsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	sheet, replayed, err := a.store.TransferTags(r.Context(), id, key, req.MarkOpenMissing, p.HasDuty(dutyCountApprove))
	if err != nil {
		a.failed(w, r, "transfer count tags", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeCountSheet(w, r, status, sheet)
}

// useTag records a count on one tag; the tag version comes from If-Match.
func (a *API) useTag(w http.ResponseWriter, r *http.Request) {
	id, version, ok := tagRoute(w, r)
	if !ok {
		return
	}
	var req useTagRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	qty, err := decimal.NewFromString(strings.TrimSpace(req.CountedQty))
	if err != nil || qty.IsNegative() {
		fields["countedQty"] = "must be a decimal number of zero or more"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	tag, err := a.store.UseTag(r.Context(), id, version, req.ProductID, domain.Dimensions(req.Dimensions), qty)
	if err != nil {
		a.failed(w, r, "use count tag", err)
		return
	}
	writeTag(w, tag)
}

func (a *API) voidTag(w http.ResponseWriter, r *http.Request) {
	id, version, ok := tagRoute(w, r)
	if !ok {
		return
	}
	tag, err := a.store.VoidTag(r.Context(), id, version)
	if err != nil {
		a.failed(w, r, "void count tag", err)
		return
	}
	writeTag(w, tag)
}

func toTagResponse(t domain.CountTag) tagResponse {
	out := tagResponse{ID: t.ID, TagNumber: t.Number, PlanID: t.PlanID, Status: string(t.Status), ProductID: t.ProductID,
		Dimensions: map[string]string(t.Dims), CountedBy: t.CountedBy, SheetID: t.SheetID, Version: t.Version}
	if out.Dimensions == nil {
		out.Dimensions = map[string]string{}
	}
	if t.Status == domain.TagUsed || (t.Status == domain.TagVoid && t.ProductID != "") {
		q := t.Qty.String()
		out.CountedQty = &q
	}
	return out
}

func writeTagList(w http.ResponseWriter, status int, tags []domain.CountTag) {
	items := make([]tagResponse, 0, len(tags))
	for _, t := range tags {
		items = append(items, toTagResponse(t))
	}
	httpx.JSON(w, status, map[string]any{"items": items})
}

// writeTag answers a tag command with the tag and its version as the ETag.
func writeTag(w http.ResponseWriter, t domain.CountTag) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(t.Version)))
	httpx.JSON(w, http.StatusOK, toTagResponse(t))
}

// reverseRequest is the body of journals/{id}:reverse.
type reverseRequest struct {
	PostingDate string `json:"postingDate"`
}

// reverseJournal creates the DRAFT reversal of a POSTED journal (BR-16.12).
// The reversal is posted with :post like any journal. A journal has one
// reversal: a repeated request answers 200 with it and Idempotent-Replayed.
func (a *API) reverseJournal(w http.ResponseWriter, r *http.Request, id string) {
	var req reverseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	date, err := time.Parse(dateLayout, strings.TrimSpace(req.PostingDate))
	if err != nil {
		fieldProblem(w, r, map[string]string{"postingDate": "must be a date (YYYY-MM-DD)"})
		return
	}
	j, inserted, err := a.store.ReverseJournal(r.Context(), id, date)
	if err != nil {
		a.failed(w, r, "reverse journal", err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJournal(w, status, j)
}

// copyRequest is the body of journals/{id}:copy. An empty postingDate copies
// the source's posting date.
type copyRequest struct {
	PostingDate string `json:"postingDate"`
}

// copyJournal creates a DRAFT journal with the header and lines of an existing
// journal of any status. Idempotency-Key is required: a repeated request
// answers 200 with the first copy and Idempotent-Replayed.
func (a *API) copyJournal(w http.ResponseWriter, r *http.Request, id string) {
	var req copyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	var date time.Time
	if d := strings.TrimSpace(req.PostingDate); d != "" {
		var err error
		if date, err = time.Parse(dateLayout, d); err != nil {
			fields["postingDate"] = "must be a date (YYYY-MM-DD)"
		}
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyHeader] = fmt.Sprintf("is required (1 to %d characters)", maxKeyLength)
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	// A copy of an OWNERSHIP_CHANGE journal is a new one: it needs the
	// consignment duty, as POST /journals does (mayKeyJournal).
	p, _ := tenant.FromContext(r.Context())
	j, inserted, err := a.store.CopyJournal(r.Context(), id, date, key, p.HasDuty(dutyConsignmentWrite))
	if err != nil {
		a.failed(w, r, "copy journal", err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJournal(w, status, j)
}
