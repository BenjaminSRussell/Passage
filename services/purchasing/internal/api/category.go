package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// dutyCategoryMaintain maintains category managers and vendor assignments (FR-17.20, PRD
// 3.17.17). Reading them is open to PurchaseOrderInquire too.
const dutyCategoryMaintain = "CategoryMaintain"

// maxCategoryLen bounds a category name (the column check is the same).
const maxCategoryLen = 200

type categoryVendorBody struct {
	LegalEntityID string      `json:"legalEntityId"`
	Category      string      `json:"category"`
	VendorID      string      `json:"vendorId"`
	Status        string      `json:"status"`
	ValidFrom     *domain.Day `json:"validFrom,omitempty"`
	ValidTo       *domain.Day `json:"validTo,omitempty"`
	Notes         string      `json:"notes,omitempty"`
}

type categoryVendorChangeBody struct {
	Status    string      `json:"status"`
	ValidFrom *domain.Day `json:"validFrom,omitempty"`
	ValidTo   *domain.Day `json:"validTo,omitempty"`
	Notes     string      `json:"notes,omitempty"`
}

type categoryManagerBody struct {
	LegalEntityID string `json:"legalEntityId"`
	Category      string `json:"category"`
	WorkerID      string `json:"workerId"`
}

func categoryField(raw string) (string, error) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return "", invalid("category", "is required")
	}
	if utf8.RuneCountInString(c) > maxCategoryLen {
		return "", invalid("category", "must be at most 200 characters")
	}
	return c, nil
}

// assignmentWindow checks the status and the validity window of an assignment; a missing
// validFrom is today.
func (a *API) assignmentWindow(status string, from, to *domain.Day) (domain.Day, error) {
	if !domain.ValidAssignmentStatus(status) {
		return domain.Day{}, invalid("status", "must be APPROVED, PROVISIONAL or BLOCKED")
	}
	validFrom := a.today()
	if from != nil {
		validFrom = *from
	}
	if to != nil && to.Before(validFrom.Time) {
		return domain.Day{}, invalid("validTo", "must not be before validFrom")
	}
	return validFrom, nil
}

func (a *API) createCategoryVendor(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body categoryVendorBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	for _, f := range []struct{ name, v string }{{"legalEntityId", body.LegalEntityID}, {"vendorId", body.VendorID}} {
		if err := uuidField(f.name, f.v, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	category, err := categoryField(body.Category)
	if err != nil {
		fail(w, r, err)
		return
	}
	from, err := a.assignmentWindow(body.Status, body.ValidFrom, body.ValidTo)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateCategoryVendor(r.Context(), domain.CategoryVendor{LegalEntityID: body.LegalEntityID, Category: category,
		VendorID: body.VendorID, Status: body.Status, ValidFrom: from, ValidTo: body.ValidTo, Notes: strings.TrimSpace(body.Notes)},
		store.Idem{Key: key, Hash: hashBody("category-vendor.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.categoryVendorsSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getCategoryVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetCategoryVendor(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listCategoryVendors(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.CategoryVendorFilter{LegalEntityID: q.Get("legalEntityId"), Category: strings.TrimSpace(q.Get("category")),
		VendorID: q.Get("vendorId"), Status: q.Get("status")}
	for _, u := range []struct{ name, v string }{{"legalEntityId", f.LegalEntityID}, {"vendorId", f.VendorID}} {
		if err := uuidField(u.name, u.v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.Status != "" && !domain.ValidAssignmentStatus(f.Status) {
		fail(w, r, invalid("status", "must be APPROVED, PROVISIONAL or BLOCKED"))
		return
	}
	page, err := a.store.ListCategoryVendors(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// changeCategoryVendor handles PUT /category-vendors/{id}: it replaces the status, validity
// window and notes under If-Match. The vendor and category of an assignment do not change.
func (a *API) changeCategoryVendor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body categoryVendorChangeBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetCategoryVendor(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	from := cur.ValidFrom
	if body.ValidFrom != nil {
		from = *body.ValidFrom
	}
	if _, err := a.assignmentWindow(body.Status, &from, body.ValidTo); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.UpdateCategoryVendor(r.Context(), id, want, body.Status, from, body.ValidTo, strings.TrimSpace(body.Notes))
	if err != nil {
		fail(w, r, err)
		return
	}
	a.categoryVendorsSaved.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) createCategoryManager(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body categoryManagerBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	for _, f := range []struct{ name, v string }{{"legalEntityId", body.LegalEntityID}, {"workerId", body.WorkerID}} {
		if err := uuidField(f.name, f.v, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	category, err := categoryField(body.Category)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, body.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateCategoryManager(r.Context(), domain.CategoryManager{LegalEntityID: body.LegalEntityID, Category: category,
		WorkerID: body.WorkerID}, store.Idem{Key: key, Hash: hashBody("category-manager.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listCategoryManagers(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.CategoryManagerFilter{LegalEntityID: q.Get("legalEntityId"), Category: strings.TrimSpace(q.Get("category")), WorkerID: q.Get("workerId")}
	for _, u := range []struct{ name, v string }{{"legalEntityId", f.LegalEntityID}, {"workerId", f.WorkerID}} {
		if err := uuidField(u.name, u.v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	page, err := a.store.ListCategoryManagers(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) deleteCategoryManager(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := a.store.DeleteCategoryManager(r.Context(), id); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkCategoryAccess applies the legal entity's category access mode to uses (FR-17.20).
// It returns the warnings (SOFT violations, PROVISIONAL vendors) or, in HARD mode, a
// PRC-CAT-002 error. Uses without a category or vendor are skipped before the store is read.
func (a *API) checkCategoryAccess(ctx context.Context, legalEntityID string, uses []domain.AccessUse) ([]store.Warning, error) {
	categories := []string{}
	seen := map[string]bool{}
	for _, u := range uses {
		if u.Category != "" && u.VendorID != "" && !seen[u.Category] {
			seen[u.Category] = true
			categories = append(categories, u.Category)
		}
	}
	if len(categories) == 0 {
		return nil, nil
	}
	pol, err := a.store.GetPolicy(ctx, legalEntityID)
	if err != nil {
		return nil, err
	}
	if pol.CategoryAccessMode == domain.AccessOff {
		return nil, nil
	}
	assignments, err := a.store.CategoryAssignments(ctx, legalEntityID, categories)
	if err != nil {
		return nil, err
	}
	findings, err := domain.CheckCategoryAccess(pol.CategoryAccessMode, assignments, uses, a.today())
	if err != nil {
		return nil, err
	}
	var out []store.Warning
	for _, f := range findings {
		out = append(out, store.Warning{Code: f.Code, LineNo: f.Use.LineNo, Detail: f.Detail})
	}
	return out, nil
}

// requisitionUses are the category uses of a requisition: each line with a preferred vendor.
func requisitionUses(r store.Requisition) []domain.AccessUse {
	var out []domain.AccessUse
	for _, l := range r.Lines {
		if l.PreferredVendorID != "" && l.Category != "" {
			out = append(out, domain.AccessUse{LineNo: l.LineNo, Ref: "requisition " + r.Number + " line " + strconv.Itoa(l.LineNo),
				Category: l.Category, VendorID: l.PreferredVendorID})
		}
	}
	return out
}

// orderCategories settles the procurement category of each line of a PO body before the
// order is built (FR-17.20): a line that orders a requisition line takes that line's
// category (another category in the request is refused), an existing line of a PATCH keeps
// the category it was written with (it cannot change), and any other line keeps the
// category it was given, trimmed. A requisition line that is not found is left for
// buildOrder and the store to report.
func (a *API) orderCategories(ctx context.Context, body *orderBody) error {
	var lineIDs []string
	for _, l := range body.Lines {
		if l.keep == nil && ids.Valid(l.RequisitionLineID) {
			lineIDs = append(lineIDs, l.RequisitionLineID)
		}
	}
	byID := map[string]store.SourcingLine{}
	if len(lineIDs) > 0 {
		sources, err := a.store.RequisitionLinesByID(ctx, lineIDs)
		if err != nil {
			return err
		}
		for _, s := range sources {
			byID[s.ID] = s
		}
	}
	for i := range body.Lines {
		l := &body.Lines[i]
		field := "lines[" + strconv.Itoa(i) + "].category"
		given := strings.TrimSpace(l.Category)
		if utf8.RuneCountInString(given) > maxCategoryLen {
			return invalid(field, "must be at most 200 characters")
		}
		if l.keep != nil {
			if given != "" && given != l.keep.Category {
				return invalid(field, "cannot change on an existing line; cancel the line and add a new one")
			}
			l.Category = l.keep.Category
			continue
		}
		if s, ok := byID[l.RequisitionLineID]; ok {
			if given != "" && given != s.Category {
				return invalid(field, "must be the requisition line's category ("+s.Category+") or blank")
			}
			given = s.Category
		}
		l.Category = given
	}
	return nil
}

// checkOrderCategoryAccess applies the category access rule to the lines of a built PO
// that check selects (by index): the PO vendor against each line's category (FR-17.20).
// It returns the SOFT and PROVISIONAL warnings or, under HARD, the PRC-CAT-002 refusal.
// Under HARD a selected line with neither a product nor a category is refused (the PRD
// line carries a product or a category), so a description-only line cannot step around
// the rule. A line with a product but no category is not checked: the product's
// procurement category lives in the wave 8 product master.
func (a *API) checkOrderCategoryAccess(ctx context.Context, po store.PurchaseOrder, check func(int) bool) ([]store.Warning, error) {
	var uses []domain.AccessUse
	bare := -1
	for i, l := range po.Lines {
		switch {
		case !check(i):
		case l.Category != "":
			uses = append(uses, domain.AccessUse{LineNo: l.LineNo, Ref: "purchase order line " + strconv.Itoa(l.LineNo),
				Category: l.Category, VendorID: po.VendorID})
		case l.ProductID == "" && bare < 0:
			bare = i
		}
	}
	if bare >= 0 {
		pol, err := a.store.GetPolicy(ctx, po.LegalEntityID)
		if err != nil {
			return nil, err
		}
		if pol.CategoryAccessMode == domain.AccessHard {
			return nil, invalid("lines["+strconv.Itoa(bare)+"].category", "is required for a line without a product while category access is HARD")
		}
	}
	return a.checkCategoryAccess(ctx, po.LegalEntityID, uses)
}

// registerCategoryRoutes mounts the FR-17.20 routes under /v1/procurement.
func (a *API) registerCategoryRoutes(r chi.Router) {
	write := r.With(authz.RequireDuty(dutyCategoryMaintain))
	read := r.With(authz.Require(anyDuty{dutyCategoryMaintain, dutyPurchaseOrderInquire},
		dutyCategoryMaintain+" or "+dutyPurchaseOrderInquire, ""))
	write.Post("/category-vendors", a.createCategoryVendor)
	read.Get("/category-vendors", a.listCategoryVendors)
	read.Get("/category-vendors/{id}", a.getCategoryVendor)
	write.Put("/category-vendors/{id}", a.changeCategoryVendor)
	write.Post("/category-managers", a.createCategoryManager)
	read.Get("/category-managers", a.listCategoryManagers)
	write.Delete("/category-managers/{id}", a.deleteCategoryManager)
}
