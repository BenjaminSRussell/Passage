package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

const maxDismissReason = 500

var intakeStatuses = map[string]bool{store.IntakeOpen: true, store.IntakeConverted: true, store.IntakeDismissed: true}

func intakeOrderID(i *store.PlannedIntake) string {
	if i == nil {
		return ""
	}
	return i.TargetID
}

func intakeRowID(i *store.PlannedIntake) string {
	if i == nil {
		return ""
	}
	return i.ID
}

func (a *API) listIntake(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.IntakeFilter{Status: q.Get("status"), SiteID: q.Get("siteId"), Cursor: q.Get("cursor"), Limit: defaultPageSize}
	fields := map[string]string{}
	if f.Status != "" && !intakeStatuses[f.Status] {
		fields["status"] = "must be open, converted or dismissed"
	}
	for name, v := range map[string]string{"siteId": f.SiteID, "cursor": f.Cursor} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = "must be a whole number from 1 to " + strconv.Itoa(maxPageSize)
		}
		f.Limit = n
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	page, err := a.store.ListPlannedIntake(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list planned order intake", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func intakeIDParam(w http.ResponseWriter, r *http.Request, raw string) bool {
	if !ids.Valid(raw) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeIntakeNotFound, "planned order intake not found"))
		return false
	}
	return true
}

func (a *API) getIntake(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := principal(w, r); !ok || !intakeIDParam(w, r, id) {
		return
	}
	row, err := a.store.GetPlannedIntake(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read planned order intake", err)
		return
	}
	w.Header().Set("ETag", etag(row.Version))
	httpx.JSON(w, http.StatusOK, row)
}

// intakeCommand serves `{id}:convert` and `{id}:dismiss`, both with the order write duty (the route's).
func (a *API) intakeCommand(w http.ResponseWriter, r *http.Request) {
	raw, name, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || !ids.Valid(raw) || (name != "convert" && name != "dismiss") {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, unknownActionCode, "use :convert or :dismiss on a planned order intake row"))
		return
	}
	if name == "convert" {
		a.convertIntake(w, r, raw)
		return
	}
	a.dismissIntake(w, r, raw)
}

// convertIntake creates the production order of a firmed planned order (FR-22.10). The body is the
// create request minus what the planned order fixes: product, site, quantity, delivery date and
// origin come from the row (originType PlannedOrder, originId the planned order), the order id is the
// planned order's target id, the legal entity is the row's (a body naming another is refused; only a
// row without one takes the body's or the caller's), and the warehouse is the row's unless it has none.
// The BOM and route snapshot and the currency come in the body, as on a manual create. Whether the row
// is still open is decided in the create transaction, after the Idempotency-Key lookup, so a retry of a
// conversion that succeeded replays the order instead of answering 409.
func (a *API) convertIntake(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	row, err := a.store.GetPlannedIntake(r.Context(), id)
	if err != nil {
		a.fail(w, r, "read planned order intake", err)
		return
	}
	refused := map[string]string{}
	if req.ReworkReference != nil {
		refused["reworkReference"] = "is not accepted on a planned order conversion"
	}
	if req.ConfigurationID != nil {
		refused["configurationId"] = "is not accepted on a planned order conversion"
	}
	if len(refused) > 0 {
		invalid(w, r, refused)
		return
	}
	if row.LegalEntityID != nil {
		if req.LegalEntityID != "" && !strings.EqualFold(req.LegalEntityID, *row.LegalEntityID) {
			invalid(w, r, map[string]string{"legalEntityId": "must be the planned order's legal entity"})
			return
		}
		req.LegalEntityID = *row.LegalEntityID
	}
	req.ProductID, req.SiteID, req.Quantity = row.ProductID, row.SiteID, row.Quantity
	delivery := row.DeliveryDate
	req.DeliveryDate = &delivery
	req.OriginType, req.OriginID = "PlannedOrder", &row.PlannedOrderID
	if row.WarehouseID != nil {
		req.WarehouseID = *row.WarehouseID
	}
	a.createWith(w, r, p, req, false, &row)
}

type intakeDismissRequest struct {
	Reason string `json:"reason"`
}

func (a *API) dismissIntake(w http.ResponseWriter, r *http.Request, id string) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	var req intakeDismissRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || len(req.Reason) > maxDismissReason {
		invalid(w, r, map[string]string{"reason": "is required, at most 500 characters"})
		return
	}
	row, err := a.store.DismissPlannedIntake(r.Context(), store.IntakeDismiss{ID: id, IfMatch: ifMatch, Reason: req.Reason, Actor: p.UserID})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(row.Version))
	httpx.JSON(w, http.StatusOK, row)
}
