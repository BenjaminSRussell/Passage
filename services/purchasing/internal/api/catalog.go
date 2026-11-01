package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// dutyCatalogMaintain creates catalogs, changes their entries and publishes or retires
// them (FR-17.18, PRD 3.17.17). Reading catalogs also needs PurchaseOrderInquire.
const dutyCatalogMaintain = "CatalogMaintain"

// maxImageURL bounds an entry image reference; images live in the object store.
const maxImageURL = 2000

type catalogBody struct {
	LegalEntityID string             `json:"legalEntityId"`
	Name          string             `json:"name"`
	Description   string             `json:"description,omitempty"`
	ValidFrom     domain.Day         `json:"validFrom"`
	ValidTo       *domain.Day        `json:"validTo,omitempty"`
	OrgUnitIDs    []string           `json:"orgUnitIds,omitempty"`
	Entries       []catalogEntryBody `json:"entries,omitempty"`
}

type catalogEntryBody struct {
	VendorID     string `json:"vendorId"`
	VendorPartNo string `json:"vendorPartNo,omitempty"`
	ProductID    string `json:"productId,omitempty"`
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	Category     string `json:"category"`
	Unit         string `json:"unit"`
	UnitPrice    string `json:"unitPrice"`
	Currency     string `json:"currency"`
	LeadTimeDays int    `json:"leadTimeDays,omitempty"`
	Preferred    bool   `json:"preferred,omitempty"`
	ImageURL     string `json:"imageUrl,omitempty"`
}

type catalogEntriesBody struct {
	Entries []catalogEntryBody `json:"entries"`
}

func buildCatalogEntry(prefix string, i int, e catalogEntryBody) (domain.CatalogEntry, error) {
	field := func(n string) string { return prefix + "[" + strconv.Itoa(i) + "]." + n }
	for _, f := range []struct {
		name, v  string
		required bool
	}{{"vendorId", e.VendorID, true}, {"productId", e.ProductID, false}} {
		if err := uuidField(field(f.name), f.v, f.required); err != nil {
			return domain.CatalogEntry{}, err
		}
	}
	out := domain.CatalogEntry{VendorID: e.VendorID, VendorPartNo: strings.TrimSpace(e.VendorPartNo), ProductID: e.ProductID,
		Name: strings.TrimSpace(e.Name), Description: strings.TrimSpace(e.Description), Category: strings.TrimSpace(e.Category),
		Unit: strings.TrimSpace(e.Unit), LeadTimeDays: e.LeadTimeDays, Preferred: e.Preferred, ImageURL: strings.TrimSpace(e.ImageURL)}
	for _, f := range []struct{ name, v string }{{"name", out.Name}, {"category", out.Category}, {"unit", out.Unit}} {
		if f.v == "" {
			return domain.CatalogEntry{}, invalid(field(f.name), "is required")
		}
	}
	price, err := decimalField(field("unitPrice"), strings.TrimSpace(e.UnitPrice), "")
	if err != nil {
		return domain.CatalogEntry{}, err
	}
	if price.IsNegative() {
		return domain.CatalogEntry{}, invalid(field("unitPrice"), "must not be negative")
	}
	if price.Exponent() < -6 {
		return domain.CatalogEntry{}, invalid(field("unitPrice"), "has at most 6 decimal places")
	}
	out.UnitPrice = price
	if out.Currency, err = currencyField(e.Currency); err != nil {
		return domain.CatalogEntry{}, invalid(field("currency"), "must be a three-letter ISO 4217 code")
	}
	if e.LeadTimeDays < 0 || e.LeadTimeDays > 3650 {
		return domain.CatalogEntry{}, invalid(field("leadTimeDays"), "must be from 0 to 3650")
	}
	if out.ImageURL != "" {
		u, perr := url.Parse(out.ImageURL)
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(out.ImageURL) > maxImageURL {
			return domain.CatalogEntry{}, invalid(field("imageUrl"), "must be an http(s) URL of at most 2000 characters")
		}
	}
	return out, nil
}

func buildCatalogEntries(prefix string, in []catalogEntryBody) ([]domain.CatalogEntry, error) {
	if len(in) > domain.MaxCatalogEntries {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCatalogTooManyEntry,
			"a catalog holds at most "+strconv.Itoa(domain.MaxCatalogEntries)+" entries")
	}
	out := make([]domain.CatalogEntry, 0, len(in))
	for i, e := range in {
		entry, err := buildCatalogEntry(prefix, i, e)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func buildCatalog(body catalogBody) (domain.Catalog, error) {
	if err := uuidField("legalEntityId", body.LegalEntityID, true); err != nil {
		return domain.Catalog{}, err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return domain.Catalog{}, invalid("name", "is required")
	}
	if body.ValidFrom.IsZero() {
		return domain.Catalog{}, invalid("validFrom", "is required")
	}
	if body.ValidTo != nil && body.ValidTo.Before(body.ValidFrom.Time) {
		return domain.Catalog{}, invalid("validTo", "must not be before validFrom")
	}
	seen := map[string]bool{}
	units := []string{}
	for i, id := range body.OrgUnitIDs {
		if err := uuidField("orgUnitIds["+strconv.Itoa(i)+"]", id, true); err != nil {
			return domain.Catalog{}, err
		}
		if !seen[id] {
			seen[id] = true
			units = append(units, id)
		}
	}
	entries, err := buildCatalogEntries("entries", body.Entries)
	if err != nil {
		return domain.Catalog{}, err
	}
	return domain.Catalog{LegalEntityID: body.LegalEntityID, Name: name, Description: strings.TrimSpace(body.Description),
		ValidFrom: body.ValidFrom, ValidTo: body.ValidTo, OrgUnitIDs: units, Entries: entries}, nil
}

func (a *API) createCatalog(w http.ResponseWriter, r *http.Request) {
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
	var body catalogBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	cat, err := buildCatalog(body)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cat.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.CreateCatalog(r.Context(), cat, store.Idem{Key: key, Hash: hashBody("catalog.create", body)})
	if err != nil {
		fail(w, r, err)
		return
	}
	a.catalogsCreated.Add(1)
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getCatalog(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetCatalog(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listCatalogs(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.CatalogFilter{Status: q.Get("status"), LegalEntityID: q.Get("legalEntityId")}
	switch f.Status {
	case "", domain.CatalogDraft, domain.CatalogPublished, domain.CatalogRetired:
	default:
		fail(w, r, invalid("status", "must be DRAFT, PUBLISHED or RETIRED"))
		return
	}
	if err := uuidField("legalEntityId", f.LegalEntityID, false); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListCatalogs(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// catalogAction handles POST /catalogs/{id}:publish and :retire with If-Match and an
// Idempotency-Key.
func (a *API) catalogAction(w http.ResponseWriter, r *http.Request) {
	if _, err := principal(r.Context()); err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if action != domain.CatalogPublish && action != domain.CatalogRetire {
		found = false
	}
	if !found || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown catalog action"))
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
	out, err := a.store.ActOnCatalog(r.Context(), id, version, action,
		store.Idem{Key: key, Hash: hashBody("catalog."+action, map[string]any{"id": id, "version": version})})
	if err != nil {
		fail(w, r, err)
		return
	}
	if action == domain.CatalogPublish {
		a.catalogsPublished.Add(1)
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// addCatalogEntries handles POST /catalogs/{id}/entries on a DRAFT catalog.
func (a *API) addCatalogEntries(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
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
	var body catalogEntriesBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if len(body.Entries) == 0 {
		fail(w, r, invalid("entries", "needs at least one entry"))
		return
	}
	entries, err := buildCatalogEntries("entries", body.Entries)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.AddCatalogEntries(r.Context(), id, version, entries,
		store.Idem{Key: key, Hash: hashBody("catalog.entries.add", map[string]any{"id": id, "version": version, "body": body})})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// removeCatalogEntry handles DELETE /catalogs/{id}/entries/{entryId} on a DRAFT catalog.
func (a *API) removeCatalogEntry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	entryID := chi.URLParam(r, "entryId")
	if !ids.Valid(entryID) {
		fail(w, r, invalid("entryId", "must be a UUID"))
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.RemoveCatalogEntry(r.Context(), id, entryID, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// searchCatalogEntries handles GET /catalog-entries, the requisition picker (FR-17.18):
// entries of catalogs published on date (default today) to the unit and legal entity.
func (a *API) searchCatalogEntries(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.CatalogSearch{LegalEntityID: q.Get("legalEntityId"), OrgUnitID: q.Get("operatingUnitId"), Date: a.today(),
		Text: strings.TrimSpace(q.Get("q")), Category: strings.TrimSpace(q.Get("category")), VendorID: q.Get("vendorId")}
	for _, c := range []struct {
		name, v  string
		required bool
	}{{"legalEntityId", f.LegalEntityID, true}, {"operatingUnitId", f.OrgUnitID, false}, {"vendorId", f.VendorID, false}} {
		if err := uuidField(c.name, c.v, c.required); err != nil {
			fail(w, r, err)
			return
		}
	}
	if raw := q.Get("date"); raw != "" {
		if f.Date, err = domain.ParseDay(raw); err != nil {
			fail(w, r, invalid("date", "must be YYYY-MM-DD"))
			return
		}
	}
	if err := inScope(p, f.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.SearchCatalogEntries(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// applyCatalogEntries resolves the lines of a requisition body that name a catalog
// entry (FR-17.18): the entry must be orderable from a PUBLISHED catalog for the
// requisition's legal entity, unit and currency today, and fills the blanks of the
// line (product, description, category, unit, estimated price, preferred vendor). A
// unit or product that differs from the entry's is refused, and an entry the caller
// cannot see is a field error on the line. It returns a copy of body.
func (a *API) applyCatalogEntries(r *http.Request, body requisitionBody) (requisitionBody, error) {
	out := body
	out.Lines = append([]requisitionLineBody(nil), body.Lines...)
	if !ids.Valid(body.LegalEntityID) {
		return out, nil // buildRequisition reports it
	}
	for i := range out.Lines {
		l := &out.Lines[i]
		if l.CatalogEntryID == "" {
			continue
		}
		line := func(name string) string { return "lines[" + strconv.Itoa(i) + "]." + name }
		if !ids.Valid(l.CatalogEntryID) {
			return out, invalid(line("catalogEntryId"), "must be a UUID")
		}
		cat, e, err := a.store.GetCatalogEntry(r.Context(), l.CatalogEntryID)
		var pr *httpx.Problem
		if errors.As(err, &pr) && pr.Code == store.CodeCatalogEntryNotFound {
			return out, invalid(line("catalogEntryId"), "is not a catalog entry of this legal entity")
		}
		if err != nil {
			return out, err
		}
		if err := cat.CheckForRequisition(e, body.LegalEntityID, body.OperatingUnitID, strings.TrimSpace(body.Currency), a.today()); err != nil {
			return out, err
		}
		if u := strings.TrimSpace(l.Unit); u != "" && u != e.Unit {
			return out, invalid(line("unit"), "must be the catalog unit "+e.Unit)
		}
		if l.ProductID != "" && l.ProductID != e.ProductID {
			return out, invalid(line("productId"), "must be the catalog entry's product or blank")
		}
		l.Unit = e.Unit
		if l.ProductID == "" {
			l.ProductID = e.ProductID
		}
		if strings.TrimSpace(l.Description) == "" {
			l.Description = e.Name
		}
		if strings.TrimSpace(l.Category) == "" {
			l.Category = e.Category
		}
		if strings.TrimSpace(l.EstimatedUnitPrice) == "" {
			l.EstimatedUnitPrice = e.UnitPrice.String()
		}
		if l.PreferredVendorID == "" {
			l.PreferredVendorID = e.VendorID
		}
	}
	return out, nil
}
