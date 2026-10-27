package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	readyTimeout   = 500 * time.Millisecond
	idempotencyKey = "Idempotency-Key"

	maxIdempotencyKeyLength = 200
	dutyRead                = "org.read"
	dutyLegalEntity         = "org.legal-entity.write"
	dutyReference           = "org.reference.write"
	maxNameLength           = 120
	maxRegistration         = 40
	maxQuantityExp          = 18
	minQuantityExp          = -24
	validationDetail        = "request failed validation"
)

var (
	legalEntityCode = regexp.MustCompile(`^[A-Z0-9]{2,8}$`)
	languageTag     = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	countryCode     = regexp.MustCompile(`^[A-Z]{2}$`)
	currencyCode    = regexp.MustCompile(`^[A-Z]{3}$`)
	reservedCodes   = map[string]bool{"DAT": true, "SYS": true, "ALL": true}
	maxQuantity     = decimal.New(1, maxQuantityExp)
)

type API struct {
	store   store.Store
	created atomic.Int64
	failed  atomic.Int64
	updated atomic.Int64

	referenceChanged  atomic.Int64
	unitCreated       atomic.Int64
	conversionCreated atomic.Int64
	ouCreated         atomic.Int64
	teamTypeCreated   atomic.Int64
	teamCreated       atomic.Int64
	teamMemberAdded   atomic.Int64
	teamUpdated       atomic.Int64
	teamMemberEnded   atomic.Int64
	teamMemberRemoved atomic.Int64
	ouUpdated         atomic.Int64
	siteCreated       atomic.Int64
	siteUpdated       atomic.Int64
	warehouseCreated  atomic.Int64
	warehouseUpdated  atomic.Int64
	locationCreated   atomic.Int64
	locationUpdated   atomic.Int64
	addressCreated    atomic.Int64
	addressRevised    atomic.Int64
	addressSearches   atomic.Int64
	geocoder          domain.Geocoder

	siteDefaultWarehouses atomic.Int64

	fiscalCalendarCreated atomic.Int64
	fiscalYearGenerated   atomic.Int64
	fiscalCalendarUpdated atomic.Int64
	postings              PostingChecker

	hierarchyTypeCreated   atomic.Int64
	hierarchyDraftCreated  atomic.Int64
	hierarchyNodesReplaced atomic.Int64
	hierarchyDraftsDeleted atomic.Int64

	hierarchyVersionsPublished   atomic.Int64
	hierarchyAsOfQueries         atomic.Int64
	hierarchyMakerCheckerRefused atomic.Int64
	now                          func() time.Time

	roundingChanged atomic.Int64

	translationsPut     atomic.Int64
	translationsDeleted atomic.Int64
}

type createLegalEntityRequest struct {
	Code               string  `json:"code"`
	Name               string  `json:"name"`
	Country            string  `json:"country"`
	Currency           string  `json:"currency"`
	TimeZone           string  `json:"timeZone"`
	DefaultLanguage    string  `json:"defaultLanguage"`
	RegistrationNumber *string `json:"registrationNumber"`
}

type updateLegalEntityRequest struct {
	Version *int `json:"version"`
	store.LegalEntityPatch
}

type referenceStatusRequest struct {
	Status string `json:"status"`
}

type referenceList struct {
	Items []store.ReferenceItem `json:"items"`
}

var referenceCollections = []struct {
	collection string
	kind       store.ReferenceKind
}{
	{"countries", store.KindCountry},
	{"currencies", store.KindCurrency},
	{"regions", store.KindRegion},
	{"languages", store.KindLanguage},
}

type listResponse struct {
	Items      []store.LegalEntity `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

type convertResponse struct {
	From            string          `json:"from"`
	To              string          `json:"to"`
	Quantity        decimal.Decimal `json:"quantity"`
	DisplayQuantity decimal.Decimal `json:"displayQuantity"`
}

func New(s store.Store) *API {
	return &API{store: s, geocoder: domain.NullGeocoder{}, postings: noPostings{}, now: time.Now}
}

func boundIdempotencyKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Get(idempotencyKey)) > maxIdempotencyKeyLength {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "idempotency_key_too_long", "Idempotency-Key must be at most 200 characters"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) Register(r chi.Router) {
	r = r.With(boundIdempotencyKey)
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyLegalEntity)).Post("/v1/org/legal-entities", a.createLegalEntity)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/org/legal-entities", a.listLegalEntities)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/org/legal-entities/{id}", a.readByID("get legal entity", func(ctx context.Context, id string) (any, error) { return a.store.GetLegalEntity(ctx, id) }))
	r.With(authz.RequireDuty(dutyLegalEntity)).Patch("/v1/org/legal-entities/{id}", legalEntityRoute(a.updateLegalEntity))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/org/legal-entities/{id}/names", a.readByID("list legal entity names", func(ctx context.Context, id string) (any, error) {
		names, err := a.store.ListLegalEntityNames(ctx, id)
		return map[string]any{"items": names}, err
	}))
	a.registerOperatingUnits(r)
	a.registerTeams(r)
	a.registerSites(r)
	a.registerWarehouses(r)
	a.registerLocations(r)
	a.registerAddresses(r)
	a.registerFiscalCalendars(r)
	a.registerHierarchies(r)
	a.registerCurrencyRounding(r)
	a.registerTranslations(r)
	r.Get("/v1/org/uom/convert", a.convert)
	a.registerUnits(r)
	for _, c := range referenceCollections {
		base := "/v1/org/" + c.collection
		r.With(authz.RequireDuty(dutyRead)).Get(base, a.listReference(c.kind))
		r.With(authz.RequireDuty(dutyRead)).Get(base+"/{code}", a.getReference(c.kind))
		r.With(authz.RequireDuty(dutyReference)).Patch(base+"/{code}", a.patchReference(c.kind))
	}
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP org_legal_entities_created_total Legal entities created.\n# TYPE org_legal_entities_created_total counter\norg_legal_entities_created_total %d\n", a.created.Load())
		fmt.Fprintf(w, "# HELP org_legal_entities_failed_total Legal entity creations that failed in the store.\n# TYPE org_legal_entities_failed_total counter\norg_legal_entities_failed_total %d\n", a.failed.Load())
		fmt.Fprintf(w, "# HELP org_legal_entities_updated_total Legal entity updates that bumped the version.\n# TYPE org_legal_entities_updated_total counter\norg_legal_entities_updated_total %d\n", a.updated.Load())
		fmt.Fprintf(w, "# HELP org_reference_status_changed_total Reference rows whose tenant status was set.\n# TYPE org_reference_status_changed_total counter\norg_reference_status_changed_total %d\n", a.referenceChanged.Load())
		fmt.Fprintf(w, "# HELP org_uom_units_created_total Custom units of measure created.\n# TYPE org_uom_units_created_total counter\norg_uom_units_created_total %d\n", a.unitCreated.Load())
		fmt.Fprintf(w, "# HELP org_uom_conversions_created_total Custom unit conversions created; idempotent replays are not counted.\n# TYPE org_uom_conversions_created_total counter\norg_uom_conversions_created_total %d\n", a.conversionCreated.Load())
		fmt.Fprintf(w, "# HELP org_operating_units_created_total Operating units created.\n# TYPE org_operating_units_created_total counter\norg_operating_units_created_total %d\n", a.ouCreated.Load())
		fmt.Fprintf(w, "# HELP org_team_types_created_total Team types created; idempotent replays are not counted.\n# TYPE org_team_types_created_total counter\norg_team_types_created_total %d\n", a.teamTypeCreated.Load())
		fmt.Fprintf(w, "# HELP org_teams_created_total Teams created; idempotent replays are not counted.\n# TYPE org_teams_created_total counter\norg_teams_created_total %d\n", a.teamCreated.Load())
		fmt.Fprintf(w, "# HELP org_team_members_added_total Team members added; idempotent replays are not counted.\n# TYPE org_team_members_added_total counter\norg_team_members_added_total %d\n", a.teamMemberAdded.Load())
		fmt.Fprintf(w, "# HELP org_teams_updated_total Team updates that changed the team; unchanged patches are not counted.\n# TYPE org_teams_updated_total counter\norg_teams_updated_total %d\n", a.teamUpdated.Load())
		fmt.Fprintf(w, "# HELP org_team_members_ended_total Team members ended; re-sent end dates are not counted.\n# TYPE org_team_members_ended_total counter\norg_team_members_ended_total %d\n", a.teamMemberEnded.Load())
		fmt.Fprintf(w, "# HELP org_team_members_removed_total Team members removed as entered in error.\n# TYPE org_team_members_removed_total counter\norg_team_members_removed_total %d\n", a.teamMemberRemoved.Load())
		fmt.Fprintf(w, "# HELP org_operating_units_updated_total Operating unit updates that bumped the version.\n# TYPE org_operating_units_updated_total counter\norg_operating_units_updated_total %d\n", a.ouUpdated.Load())
		fmt.Fprintf(w, "# HELP org_sites_created_total Sites created.\n# TYPE org_sites_created_total counter\norg_sites_created_total %d\n", a.siteCreated.Load())
		fmt.Fprintf(w, "# HELP org_site_default_warehouses_total Sites created with a default warehouse; idempotent replays are not counted.\n# TYPE org_site_default_warehouses_total counter\norg_site_default_warehouses_total %d\n", a.siteDefaultWarehouses.Load())
		fmt.Fprintf(w, "# HELP org_sites_updated_total Site updates that bumped the version.\n# TYPE org_sites_updated_total counter\norg_sites_updated_total %d\n", a.siteUpdated.Load())
		fmt.Fprintf(w, "# HELP org_warehouses_created_total Warehouses created.\n# TYPE org_warehouses_created_total counter\norg_warehouses_created_total %d\n", a.warehouseCreated.Load())
		fmt.Fprintf(w, "# HELP org_warehouses_updated_total Warehouse updates that bumped the version.\n# TYPE org_warehouses_updated_total counter\norg_warehouses_updated_total %d\n", a.warehouseUpdated.Load())
		fmt.Fprintf(w, "# HELP org_locations_created_total Locations created.\n# TYPE org_locations_created_total counter\norg_locations_created_total %d\n", a.locationCreated.Load())
		fmt.Fprintf(w, "# HELP org_locations_updated_total Location updates that bumped the version.\n# TYPE org_locations_updated_total counter\norg_locations_updated_total %d\n", a.locationUpdated.Load())
		fmt.Fprintf(w, "# HELP org_addresses_created_total Postal addresses created.\n# TYPE org_addresses_created_total counter\norg_addresses_created_total %d\n", a.addressCreated.Load())
		fmt.Fprintf(w, "# HELP org_addresses_revised_total Postal address versions created by edits.\n# TYPE org_addresses_revised_total counter\norg_addresses_revised_total %d\n", a.addressRevised.Load())
		fmt.Fprintf(w, "# HELP org_address_searches_total Address list requests that carried a search text.\n# TYPE org_address_searches_total counter\norg_address_searches_total %d\n", a.addressSearches.Load())
		fmt.Fprintf(w, "# HELP org_fiscal_calendars_created_total Fiscal calendars created.\n# TYPE org_fiscal_calendars_created_total counter\norg_fiscal_calendars_created_total %d\n", a.fiscalCalendarCreated.Load())
		fmt.Fprintf(w, "# HELP org_fiscal_calendars_updated_total Fiscal calendar updates that bumped the version.\n# TYPE org_fiscal_calendars_updated_total counter\norg_fiscal_calendars_updated_total %d\n", a.fiscalCalendarUpdated.Load())
		fmt.Fprintf(w, "# HELP org_fiscal_years_generated_total Fiscal years generated or regenerated; idempotent replays are not counted.\n# TYPE org_fiscal_years_generated_total counter\norg_fiscal_years_generated_total %d\n", a.fiscalYearGenerated.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_types_created_total Hierarchy types created.\n# TYPE org_hierarchy_types_created_total counter\norg_hierarchy_types_created_total %d\n", a.hierarchyTypeCreated.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_drafts_created_total Draft hierarchy versions created.\n# TYPE org_hierarchy_drafts_created_total counter\norg_hierarchy_drafts_created_total %d\n", a.hierarchyDraftCreated.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_drafts_deleted_total Draft hierarchy versions deleted.\n# TYPE org_hierarchy_drafts_deleted_total counter\norg_hierarchy_drafts_deleted_total %d\n", a.hierarchyDraftsDeleted.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_node_sets_replaced_total Draft hierarchy node sets replaced.\n# TYPE org_hierarchy_node_sets_replaced_total counter\norg_hierarchy_node_sets_replaced_total %d\n", a.hierarchyNodesReplaced.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_versions_published_total Hierarchy versions published; idempotent replays are counted.\n# TYPE org_hierarchy_versions_published_total counter\norg_hierarchy_versions_published_total %d\n", a.hierarchyVersionsPublished.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_as_of_queries_total Hierarchy as-of queries answered.\n# TYPE org_hierarchy_as_of_queries_total counter\norg_hierarchy_as_of_queries_total %d\n", a.hierarchyAsOfQueries.Load())
		fmt.Fprintf(w, "# HELP org_hierarchy_publish_maker_checker_refused_total Security-purpose hierarchy publishes refused because the publisher was the last editor.\n# TYPE org_hierarchy_publish_maker_checker_refused_total counter\norg_hierarchy_publish_maker_checker_refused_total %d\n", a.hierarchyMakerCheckerRefused.Load())
		fmt.Fprintf(w, "# HELP org_currency_rounding_rules_changed_total Currency rounding rule puts that created or changed a row; identical repeats are not counted.\n# TYPE org_currency_rounding_rules_changed_total counter\norg_currency_rounding_rules_changed_total %d\n", a.roundingChanged.Load())
		fmt.Fprintf(w, "# HELP org_name_translations_put_total Name translation puts that created or changed a row; identical repeats are not counted.\n# TYPE org_name_translations_put_total counter\norg_name_translations_put_total %d\n", a.translationsPut.Load())
		fmt.Fprintf(w, "# HELP org_name_translations_deleted_total Name translations deleted.\n# TYPE org_name_translations_deleted_total counter\norg_name_translations_deleted_total %d\n", a.translationsDeleted.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) createLegalEntity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if p, _ := tenant.FromContext(r.Context()); p.LegalEntityID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a legal-entity-scoped caller cannot create legal entities"))
		return
	}
	var req createLegalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.LegalEntity{Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name), Country: req.Country, Currency: req.Currency, TimeZone: req.TimeZone,
		DefaultLanguage: req.DefaultLanguage, RegistrationNumber: req.RegistrationNumber}
	if in.DefaultLanguage == "" {
		in.DefaultLanguage = store.DefaultLanguage
	}
	if reservedCodes[in.Code] {
		p := httpx.NewProblem(http.StatusBadRequest, "ORG-LE-CODE-RESERVED", "legal entity code is reserved")
		p.Errors = map[string]string{"code": "must not be DAT, SYS or ALL"}
		httpx.Fail(w, r, p)
		return
	}
	if fields := validateLegalEntity(in); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	entity, err := a.store.CreateLegalEntity(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.failed.Add(1)
		slog.Error("create legal entity failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.created.Add(1)
	httpx.JSON(w, http.StatusCreated, entity)
}

func (a *API) listLegalEntities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.LegalEntityFilter{Status: q.Get("status"), Country: q.Get("country"), Currency: q.Get("currency")}
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"status", "must be active or inactive", f.Status != "" && !validStatus(f.Status)},
		{"country", "must be two upper-case letters", f.Country != "" && !countryCode.MatchString(f.Country)},
		{"currency", "must be three upper-case letters", f.Currency != "" && !currencyCode.MatchString(f.Currency)},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListLegalEntities(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list legal entities failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := listResponse{Items: page.Items}
	if page.NextCode != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextCode))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func legalEntityRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLegalEntityNotFound, "legal entity not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) readByID(op string, load func(ctx context.Context, id string) (any, error)) http.HandlerFunc {
	return legalEntityRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		body, err := load(r.Context(), id)
		if err != nil {
			slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, body)
	})
}

func (a *API) updateLegalEntity(w http.ResponseWriter, r *http.Request, id string) {
	var req updateLegalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	fields := validateShared(req.Name, req.TimeZone, req.DefaultLanguage, req.RegistrationNumber)
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.Status != nil && !validStatus(*req.Status) {
		fields["status"] = "must be active or inactive"
	}
	optionalID(fields, "primaryAddressId", req.PrimaryAddressID, true)
	optionalID(fields, "fiscalCalendarId", req.FiscalCalendarID, true)
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	entity, err := a.store.UpdateLegalEntity(r.Context(), id, *req.Version, req.LegalEntityPatch)
	if err != nil {
		slog.Error("update legal entity failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if entity.Version != *req.Version {
		a.updated.Add(1)
	}
	httpx.JSON(w, http.StatusOK, entity)
}

func validStatus(status string) bool { return status == "active" || status == "inactive" }

func badQuery(w http.ResponseWriter, r *http.Request, field, rule string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = map[string]string{field: rule}
	httpx.Fail(w, r, p)
}

func (a *API) listReference(kind store.ReferenceKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		q := r.URL.Query()
		f := store.ReferenceFilter{Country: q.Get("country"), Status: q.Get("status")}
		if f.Status != "" && !validStatus(f.Status) {
			badQuery(w, r, "status", "must be active or inactive")
			return
		}
		if f.Country != "" && (kind != store.KindRegion || !countryCode.MatchString(f.Country)) {
			badQuery(w, r, "country", "must be two upper-case letters and is accepted for regions only")
			return
		}
		items, err := a.store.ListReference(r.Context(), kind, f)
		if err != nil {
			slog.Error("list reference failed", "err", err, "kind", kind, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, referenceList{Items: items})
	}
}

func (a *API) getReference(kind store.ReferenceKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		item, err := a.store.GetReference(r.Context(), kind, chi.URLParam(r, "code"))
		if err != nil {
			slog.Error("get reference failed", "err", err, "kind", kind, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, item)
	}
}

func (a *API) patchReference(kind store.ReferenceKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		if p, _ := tenant.FromContext(r.Context()); p.LegalEntityID != "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a legal-entity-scoped caller cannot change reference status"))
			return
		}
		var req referenceStatusRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !validStatus(req.Status) {
			badQuery(w, r, "status", "must be active or inactive")
			return
		}
		item, err := a.store.SetReferenceStatus(r.Context(), kind, chi.URLParam(r, "code"), req.Status)
		if err != nil {
			slog.Error("set reference status failed", "err", err, "kind", kind, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, err)
			return
		}
		a.referenceChanged.Add(1)
		httpx.JSON(w, http.StatusOK, item)
	}
}

func pageParams(r *http.Request) (int, string, error) {
	q := r.URL.Query()
	limit := store.MaxPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			return 0, "", httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("limit must be 1 to %d", store.MaxPageSize))
		}
		limit = n
	}
	after := ""
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return 0, "", httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor is invalid")
		}
		after = string(decoded)
	}
	return limit, after, nil
}

func (a *API) convert(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	qty, err := decimal.NewFromString(q.Get("qty"))
	if from == "" || to == "" || err != nil || !decimalInBounds(qty) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "from, to and a finite decimal qty are required"))
		return
	}
	convs, precision, err := a.store.ConversionGraph(r.Context())
	if err != nil {
		slog.Error("load conversion graph failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	for _, code := range []string{from, to} {
		if _, ok := precision[code]; !ok {
			httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeNoPath, Detail: "unit " + code + " is unknown or inactive"}))
			return
		}
	}
	result, err := domain.Convert(convs, from, to, qty)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, convertResponse{From: from, To: to, Quantity: domain.Display(result, domain.DisplayPrecision), DisplayQuantity: domain.Display(result, precision[to])})
}

func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func validateShared(name, zone, language, registration *string) map[string]string {
	fields := map[string]string{}
	if name != nil && (*name == "" || utf8.RuneCountInString(*name) > maxNameLength) {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLength)
	}
	if zone != nil {
		if _, err := time.LoadLocation(*zone); err != nil || *zone == "" || *zone == "Local" {
			fields["timeZone"] = "must be an IANA time zone name"
		}
	}
	if language != nil && !languageTag.MatchString(*language) {
		fields["defaultLanguage"] = "must be a language tag such as en or en-US"
	}
	if registration != nil && utf8.RuneCountInString(*registration) > maxRegistration {
		fields["registrationNumber"] = fmt.Sprintf("must be at most %d characters", maxRegistration)
	}
	return fields
}

func validateLegalEntity(in store.LegalEntity) map[string]string {
	fields := validateShared(&in.Name, &in.TimeZone, &in.DefaultLanguage, in.RegistrationNumber)
	if !legalEntityCode.MatchString(in.Code) {
		fields["code"] = "must be 2 to 8 characters of A-Z or 0-9"
	}
	if !countryCode.MatchString(in.Country) {
		fields["country"] = "must be an ISO 3166-1 alpha-2 code"
	}
	if !currencyCode.MatchString(in.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	return fields
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}
