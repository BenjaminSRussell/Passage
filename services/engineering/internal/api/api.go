package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const (
	readyTimeout      = 500 * time.Millisecond
	idempotencyKey    = "Idempotency-Key"
	dutyRead          = "engineering.read"
	dutyBomWrite      = "engineering.bom.write"
	dutyEcoApprove    = "engineering.eco.approve"
	dutyEcoWorkflow   = "engineering.eco.workflow"
	dutyEcrReview     = "engineering.ecr.review"
	dutyResourceWrite = "engineering.resource.write"
	dutyRouteWrite    = "engineering.route.write"
	dateLayout        = "2006-01-02"
	validationDetail  = "request failed validation"
	maxIDLength       = 64
	maxLines          = 5000
	maxOperations     = 500
	maxNumberLength   = 40
	maxReasonLength   = 200
	maxDecimalExp     = 15
	minDecimalExp     = -12
)

var (
	maxDecimal = decimal.New(1, maxDecimalExp)
	bomUsages  = map[string]bool{"production": true, "engineering": true, "costing": true, "maintenance": true, "sales": true, "universal": true}
	lineTypes  = map[string]bool{domain.LineItem: true, domain.LinePhantom: true, domain.LineCoProduct: true, domain.LineByProduct: true}
)

type API struct {
	store        store.Store
	approver     domain.Approver
	now          func() time.Time
	bomCreated   atomic.Int64
	bomFailed    atomic.Int64
	bomActivated atomic.Int64
	bomCancelled atomic.Int64
	bomUpdated   atomic.Int64
	bomConflicts atomic.Int64
	ecosReleased atomic.Int64
	ecosFailed   atomic.Int64
	ecrsCreated  atomic.Int64
	ecrsDecided  atomic.Int64
	ecrsFailed   atomic.Int64
	explosions   atomic.Int64
	stats        routeStats

	calendarsCreated  atomic.Int64
	resourcesCreated  atomic.Int64
	resourceFailed    atomic.Int64
	routeFailed       atomic.Int64
	routeActivated    atomic.Int64
	routeCancelled    atomic.Int64
	routeUpdated      atomic.Int64
	routeConflicts    atomic.Int64
	resourcesUpdated  atomic.Int64
	resourcesDeleted  atomic.Int64
	resourceConflicts atomic.Int64
	bomCopied         atomic.Int64
	routeCopied       atomic.Int64
}

func New(s store.Store, approver domain.Approver, now func() time.Time) *API {
	return &API{store: s, approver: approver, now: now, stats: routeStats{routes: map[string]*routeStat{}}}
}

func (a *API) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(a.instrument)
		r.Get("/readyz", a.ready)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/bom-versions", a.createBomVersion)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/bom-versions", a.listBomVersions)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/bom-versions/{id}", a.getBomVersion)
		r.With(authz.RequireDuty(dutyBomWrite)).Put("/v1/engineering/bom-versions/{id}", a.updateBomVersion)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/bom-versions/{id}:activate", a.activateBomVersion)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/bom-versions/{id}:cancel", a.cancelBomVersion)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/bom-versions/{id}:copy", a.copyBomVersion)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/route-versions", a.listRouteVersions)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/route-versions:active", a.activeRouteVersion)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/route-versions/{id}", a.getRouteVersion)
		r.With(authz.RequireDuty(dutyRouteWrite)).Put("/v1/engineering/route-versions/{id}", a.updateRouteVersion)
		r.With(authz.RequireDuty(dutyRouteWrite)).Post("/v1/engineering/route-versions/{id}:activate", a.activateRouteVersion)
		r.With(authz.RequireDuty(dutyRouteWrite)).Post("/v1/engineering/route-versions/{id}:cancel", a.cancelRouteVersion)
		r.With(authz.RequireDuty(dutyRouteWrite)).Post("/v1/engineering/route-versions/{id}:copy", a.copyRouteVersion)
		r.With(authz.RequireDuty(dutyRouteWrite)).Post("/v1/engineering/route-versions", a.createRouteVersion)
		r.With(authz.RequireDuty(dutyRead)).Post("/v1/engineering/boms:explode", a.explode)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/boms:where-used", a.whereUsed)
		r.With(authz.RequireDuty(dutyRead)).Post("/v1/engineering/routes:lead-time", a.leadTime)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/ecos", a.createEco)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/ecrs", a.createEcr)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/ecrs", a.listEcrs)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/ecrs/{id}", a.getEcr)
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/ecrs/{id}:submit", a.moveEcr("submit"))
		r.With(authz.RequireDuty(dutyBomWrite)).Post("/v1/engineering/ecrs/{id}:cancel", a.moveEcr("cancel"))
		ecrDecider := r.With(authz.RequireDuty(dutyEcrReview))
		ecrDecider.Post("/v1/engineering/ecrs/{id}:review", a.moveEcr("review"))
		ecrDecider.Post("/v1/engineering/ecrs/{id}:approve", a.moveEcr("approve"))
		ecrDecider.Post("/v1/engineering/ecrs/{id}:reject", a.moveEcr("reject"))
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/ecos", a.listEcos)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/ecos/{id}", a.getEco)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/ecos/{id}/impact", a.ecoImpact)
		r.With(authz.RequireAnyDuty(dutyEcoApprove, dutyEcoWorkflow)).Post("/v1/engineering/ecos/{id}:release", a.releaseEco)
		ecoDecider := r.With(authz.RequireDuty(dutyEcoWorkflow))
		ecoDecider.Post("/v1/engineering/ecos/{id}:approve", a.decideEco(domain.EcoApproved))
		ecoDecider.Post("/v1/engineering/ecos/{id}:reject", a.decideEco(domain.EcoRejected))
		ecoDecider.Post("/v1/engineering/ecos/{id}:cancel", a.decideEco(domain.EcoCancelled))
		r.With(authz.RequireDuty(dutyResourceWrite)).Post("/v1/engineering/calendars", a.createCalendar)
		r.With(authz.RequireDuty(dutyResourceWrite)).Post("/v1/engineering/calendars/{id}:generate", a.generateCalendar)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/calendars/{id}/days", a.calendarDays)
		r.With(authz.RequireDuty(dutyResourceWrite)).Post("/v1/engineering/resource-groups", a.createResourceGroup)
		r.With(authz.RequireDuty(dutyResourceWrite)).Post("/v1/engineering/capabilities", a.createCapability)
		r.With(authz.RequireDuty(dutyResourceWrite)).Post("/v1/engineering/resources", a.createResource)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/resources", a.listResources)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/resources/{id}", a.getResource)
		r.With(authz.RequireDuty(dutyResourceWrite)).Put("/v1/engineering/resources/{id}", a.updateResource)
		r.With(authz.RequireDuty(dutyResourceWrite)).Delete("/v1/engineering/resources/{id}", a.deleteResource)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/resource-groups", a.listResourceGroups)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/capabilities", a.listCapabilities)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/capabilities/{id}", a.getCapability)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/resource-groups/{id}", a.getResourceGroup)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/calendars", a.listCalendars)
		r.With(authz.RequireDuty(dutyRead)).Get("/v1/engineering/calendars/{id}", a.getCalendar)
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.ErrorContext(r.Context(), "readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type bomLineBody struct {
	LineNo             int              `json:"lineNo,omitempty"`
	ComponentProductID string           `json:"componentProductId"`
	Quantity           decimal.Decimal  `json:"quantity"`
	Series             *decimal.Decimal `json:"series,omitempty"`
	LineType           string           `json:"lineType,omitempty"`
	ScrapConst         decimal.Decimal  `json:"scrapConst"`
	ScrapVarPct        decimal.Decimal  `json:"scrapVarPct"`
	RoundUp            string           `json:"roundUp,omitempty"`
	EffectiveFrom      string           `json:"effectiveFrom,omitempty"`
	EffectiveTo        string           `json:"effectiveTo,omitempty"`
	// FR-9.20 alternative group and discontinuation data; explosion returns them unresolved.
	AltGroup             string           `json:"altGroup,omitempty"`
	AltPriority          int              `json:"altPriority,omitempty"`
	AltStrategy          string           `json:"altStrategy,omitempty"`
	UsageProbabilityPct  *decimal.Decimal `json:"usageProbabilityPct,omitempty"`
	DiscontinuationGroup string           `json:"discontinuationGroup,omitempty"`
	DiscontinueType      string           `json:"discontinueType,omitempty"`
	FollowUpGroup        string           `json:"followUpGroup,omitempty"`
	FollowUpComponentID  string           `json:"followUpComponentId,omitempty"`
	DiscontinueOn        string           `json:"discontinueOn,omitempty"`
}

type bomVersionBody struct {
	ID            string           `json:"id,omitempty"`
	ProductID     string           `json:"productId"`
	SiteID        string           `json:"siteId,omitempty"`
	EffectiveFrom string           `json:"effectiveFrom"`
	EffectiveTo   string           `json:"effectiveTo,omitempty"`
	FromQty       *decimal.Decimal `json:"fromQty,omitempty"`
	ToQty         *decimal.Decimal `json:"toQty,omitempty"`
	Status        string           `json:"status,omitempty"`
	Lines         []bomLineBody    `json:"lines,omitempty"`
	// Usage and Category are FR-9.19; they default to production and material.
	Usage    string `json:"usage,omitempty"`
	Category string `json:"category,omitempty"`
	// CopiedFrom is read-only (FR-9.12 lineage): create ignores it, only :copy sets it.
	CopiedFrom string `json:"copiedFromVersionId,omitempty"`
	// Version is the draft's optimistic-locking counter (BR-9.3). Reads return it; create ignores it and
	// PUT requires the value the editor loaded.
	Version int `json:"version,omitempty"`
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

func finite(ds ...decimal.Decimal) bool {
	for _, d := range ds {
		if d.Exponent() < minDecimalExp || d.Exponent() > maxDecimalExp || !d.Abs().LessThan(maxDecimal) {
			return false
		}
	}
	return true
}

func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, s)
	return t, err == nil
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dateLayout)
}

func optionalDate(field, raw string, fields map[string]string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, ok := parseDate(raw)
	if !ok {
		fields[field] = "must be a date as YYYY-MM-DD"
	}
	return t
}

func validID(field, value string, fields map[string]string) {
	if n := len(strings.TrimSpace(value)); n == 0 || n > maxIDLength {
		fields[field] = fmt.Sprintf("must be 1 to %d characters", maxIDLength)
	}
}

func (b bomVersionBody) toDomain() (domain.BomVersion, map[string]string) {
	fields := map[string]string{}
	validID("productId", b.ProductID, fields)
	v := domain.BomVersion{ID: ids.New(), ProductID: b.ProductID, SiteID: b.SiteID, FromQty: b.FromQty, ToQty: b.ToQty, Status: b.Status, Usage: b.Usage, Category: b.Category}
	if v.Usage == "" {
		v.Usage = domain.UsageProduction
	}
	if v.Category == "" {
		v.Category = domain.CategoryMaterial
	}
	if !bomUsages[v.Usage] {
		fields["usage"] = "must be production, engineering, costing, maintenance, sales or universal"
	}
	if v.Category != domain.CategoryMaterial && v.Category != domain.CategoryDocument {
		fields["category"] = "must be material or document (equipment, functional location, sales order and project BOMs arrive with their waves)"
	}
	if v.Status == "" {
		v.Status = domain.StatusDraft
	}
	if v.Status != domain.StatusDraft && v.Status != domain.StatusActive {
		fields["status"] = "must be draft or active"
	}
	var ok bool
	if v.EffectiveFrom, ok = parseDate(b.EffectiveFrom); !ok {
		fields["effectiveFrom"] = "is required as YYYY-MM-DD"
	}
	v.EffectiveTo = optionalDate("effectiveTo", b.EffectiveTo, fields)
	if !v.EffectiveTo.IsZero() && v.EffectiveTo.Before(v.EffectiveFrom) {
		fields["effectiveTo"] = "must not be before effectiveFrom"
	}
	for field, q := range map[string]*decimal.Decimal{"fromQty": b.FromQty, "toQty": b.ToQty} {
		if q != nil && (q.IsNegative() || !finite(*q)) {
			fields[field] = "must be a non-negative decimal"
		}
	}
	if b.FromQty != nil && b.ToQty != nil && b.FromQty.GreaterThan(*b.ToQty) {
		fields["toQty"] = "must not be below fromQty"
	}
	if len(b.Lines) == 0 || len(b.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must contain 1 to %d lines", maxLines)
	}
	seen := map[int]bool{}
	for i, lb := range b.Lines {
		line, problem := lb.toDomain(i)
		if problem != "" {
			fields[fmt.Sprintf("lines[%d]", i)] = problem
		} else if seen[line.LineNo] {
			fields[fmt.Sprintf("lines[%d]", i)] = "lineNo is duplicated"
		}
		seen[line.LineNo] = true
		v.Lines = append(v.Lines, line)
	}
	return v, fields
}

func (lb bomLineBody) toDomain(index int) (domain.BomLine, string) {
	line := domain.BomLine{
		LineNo: lb.LineNo, ComponentProductID: lb.ComponentProductID, Quantity: lb.Quantity, Series: decimal.NewFromInt(1),
		LineType: lb.LineType, ScrapConst: lb.ScrapConst, ScrapVarPct: lb.ScrapVarPct, RoundUp: lb.RoundUp,
	}
	if line.LineNo == 0 {
		line.LineNo = index + 1
	}
	if lb.Series != nil {
		line.Series = *lb.Series
	}
	if line.LineType == "" {
		line.LineType = domain.LineItem
	}
	dates := map[string]string{}
	line.EffectiveFrom, line.EffectiveTo = optionalDate("effectiveFrom", lb.EffectiveFrom, dates), optionalDate("effectiveTo", lb.EffectiveTo, dates)
	line.Alt = domain.Alternative{Group: lb.AltGroup, Priority: lb.AltPriority, Strategy: lb.AltStrategy, ProbabilityPct: lb.UsageProbabilityPct}
	line.Discontinuation = domain.Discontinuation{
		Group: lb.DiscontinuationGroup, Type: lb.DiscontinueType, FollowUpGroup: lb.FollowUpGroup, FollowUpComponent: lb.FollowUpComponentID,
		DiscontinueOn: optionalDate("discontinueOn", lb.DiscontinueOn, dates),
	}
	switch {
	case len(dates) > 0:
		return line, "effectiveFrom, effectiveTo and discontinueOn must be dates as YYYY-MM-DD"
	case len(lb.FollowUpComponentID) > maxIDLength || (lb.UsageProbabilityPct != nil && !finite(*lb.UsageProbabilityPct)):
		return line, "followUpComponentId or usageProbabilityPct is out of range"
	case !line.EffectiveTo.IsZero() && line.EffectiveTo.Before(line.EffectiveFrom):
		return line, "effectiveTo must not be before effectiveFrom"
	case !lineTypes[line.LineType] || (line.RoundUp != "" && line.RoundUp != domain.RoundUpQuantity):
		return line, "lineType or roundUp is not recognised"
	case len(strings.TrimSpace(line.ComponentProductID)) == 0 || len(line.ComponentProductID) > maxIDLength:
		return line, fmt.Sprintf("componentProductId must be 1 to %d characters", maxIDLength)
	case !finite(line.Quantity, line.Series, line.ScrapConst, line.ScrapVarPct):
		return line, "quantities must be decimals within range"
	}
	if err := domain.ValidateLine(line); err != nil {
		return line, err.Error()
	}
	return line, ""
}

func bodyFromVersion(v domain.BomVersion) bomVersionBody {
	body := bomVersionBody{
		ID: v.ID, ProductID: v.ProductID, SiteID: v.SiteID, EffectiveFrom: formatDate(v.EffectiveFrom), EffectiveTo: formatDate(v.EffectiveTo),
		FromQty: v.FromQty, ToQty: v.ToQty, Status: v.Status, CopiedFrom: v.CopiedFrom, Version: v.Version, Usage: v.Usage, Category: v.Category,
	}
	for _, l := range v.Lines {
		series := l.Series
		body.Lines = append(body.Lines, bomLineBody{
			LineNo: l.LineNo, ComponentProductID: l.ComponentProductID, Quantity: l.Quantity, Series: &series, LineType: l.LineType,
			ScrapConst: l.ScrapConst, ScrapVarPct: l.ScrapVarPct, RoundUp: l.RoundUp, EffectiveFrom: formatDate(l.EffectiveFrom), EffectiveTo: formatDate(l.EffectiveTo),
			AltGroup: l.Alt.Group, AltPriority: l.Alt.Priority, AltStrategy: l.Alt.Strategy, UsageProbabilityPct: l.Alt.ProbabilityPct,
			DiscontinuationGroup: l.Discontinuation.Group, DiscontinueType: l.Discontinuation.Type, FollowUpGroup: l.Discontinuation.FollowUpGroup,
			FollowUpComponentID: l.Discontinuation.FollowUpComponent, DiscontinueOn: formatDate(l.Discontinuation.DiscontinueOn),
		})
	}
	return body
}

func checkVersion(v domain.BomVersion) func(store.Snapshot) error {
	return func(existing store.Snapshot) error {
		if err := domain.CheckAlternatives(v.Lines); err != nil {
			return err
		}
		for _, l := range v.Lines {
			if err := domain.CheckCycle(existing, v.ProductID, l.ComponentProductID, v.EffectiveFrom); err != nil {
				return err
			}
		}
		if v.Status != domain.StatusActive {
			return nil
		}
		if err := domain.CheckNoOverlap(existing.VersionsFor(v.ProductID), v); err != nil {
			return err
		}
		return domain.CheckDirectActivation(existing.VersionsFor(v.ProductID), v)
	}
}

func (a *API) createBomVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body bomVersionBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	version, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateBomVersion(r.Context(), version, store.Idem{Key: r.Header.Get(idempotencyKey), Hash: hashOf(body)}, checkVersion(version))
	if err != nil {
		a.fail(w, r, "create bom version", err, &a.bomFailed)
		return
	}
	a.bomCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, bodyFromVersion(created))
}

func (a *API) listBomVersions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.BomVersionFilter{ProductID: q.Get("productId"), SiteID: q.Get("siteId"), Status: q.Get("status"), Usage: q.Get("usage"), Category: q.Get("category")}
	fields := map[string]string{}
	for field, value := range map[string]string{"productId": f.ProductID, "siteId": f.SiteID} {
		if len(value) > maxIDLength {
			fields[field] = fmt.Sprintf("must be at most %d characters", maxIDLength)
		}
	}
	switch f.Status {
	case "", domain.StatusDraft, domain.StatusActive, domain.StatusExpired, domain.StatusSuperseded, domain.StatusCancelled:
	default:
		fields["status"] = "must be draft, active, expired, superseded or cancelled"
	}
	if f.Usage != "" && !bomUsages[f.Usage] {
		fields["usage"] = "must be production, engineering, costing, maintenance, sales or universal"
	}
	if f.Category != "" && f.Category != domain.CategoryMaterial && f.Category != domain.CategoryDocument {
		fields["category"] = "must be material or document"
	}
	f.AsOf = optionalDate("asOf", q.Get("asOf"), fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	listPage(a, w, r, "list bom versions", &a.bomFailed, func(ctx context.Context, after string, limit int) (store.BomVersionPage, error) {
		return a.store.ListBomVersions(ctx, f, after, limit)
	}, bodyFromVersion)
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
		if err != nil || !ids.Valid(string(decoded)) {
			return 0, "", httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor is invalid")
		}
		after = string(decoded)
	}
	return limit, after, nil
}

type explodeRequest struct {
	ProductID string           `json:"productId"`
	SiteID    string           `json:"siteId"`
	AsOf      string           `json:"asOf"`
	Quantity  *decimal.Decimal `json:"quantity"`
	Levels    int              `json:"levels"`
	Usage     string           `json:"usage,omitempty"`
}

type nodeBody struct {
	ProductID string          `json:"productId"`
	LineNo    int             `json:"lineNo,omitempty"`
	Level     int             `json:"level"`
	Quantity  decimal.Decimal `json:"quantity"`
	Children  []nodeBody      `json:"children,omitempty"`
	// Alternative and Discontinuation are the line's FR-9.20 data, returned unresolved.
	Alternative     *altBody             `json:"alternative,omitempty"`
	Discontinuation *discontinuationBody `json:"discontinuation,omitempty"`
}

type altBody struct {
	Group          string           `json:"group"`
	Priority       int              `json:"priority,omitempty"`
	Strategy       string           `json:"strategy"`
	ProbabilityPct *decimal.Decimal `json:"usageProbabilityPct,omitempty"`
}

type discontinuationBody struct {
	Group             string `json:"group,omitempty"`
	Type              string `json:"type"`
	FollowUpGroup     string `json:"followUpGroup,omitempty"`
	FollowUpComponent string `json:"followUpComponentId,omitempty"`
	DiscontinueOn     string `json:"discontinueOn,omitempty"`
}

type altMemberBody struct {
	ParentProductID string          `json:"parentProductId"`
	ProductID       string          `json:"productId"`
	LineNo          int             `json:"lineNo"`
	Quantity        decimal.Decimal `json:"quantity"`
	Alternative     altBody         `json:"alternative"`
}

func bodyFromAlt(a domain.Alternative) *altBody {
	if a.Group == "" {
		return nil
	}
	return &altBody{Group: a.Group, Priority: a.Priority, Strategy: a.Strategy, ProbabilityPct: a.ProbabilityPct}
}

func bodyFromDiscontinuation(d domain.Discontinuation) *discontinuationBody {
	if d.Type == "" {
		return nil
	}
	return &discontinuationBody{Group: d.Group, Type: d.Type, FollowUpGroup: d.FollowUpGroup, FollowUpComponent: d.FollowUpComponent, DiscontinueOn: formatDate(d.DiscontinueOn)}
}

type flatBody struct {
	ProductID string          `json:"productId"`
	Quantity  decimal.Decimal `json:"quantity"`
}

type explosionBody struct {
	Root nodeBody   `json:"root"`
	Flat []flatBody `json:"flat"`
	// Alternatives lists leaf lines in an alternative group; they are left out of flat.
	Alternatives []altMemberBody `json:"alternatives,omitempty"`
}

func bodyFromNode(n *domain.Node) nodeBody {
	body := nodeBody{ProductID: n.ProductID, LineNo: n.LineNo, Level: n.Level, Quantity: n.Quantity, Alternative: bodyFromAlt(n.Alt), Discontinuation: bodyFromDiscontinuation(n.Discontinuation)}
	for _, c := range n.Children {
		body.Children = append(body.Children, bodyFromNode(c))
	}
	return body
}

func (a *API) explode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req explodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("productId", req.ProductID, fields)
	asOf := a.today()
	if req.AsOf != "" {
		var ok bool
		if asOf, ok = parseDate(req.AsOf); !ok {
			fields["asOf"] = "must be a date as YYYY-MM-DD"
		}
	}
	quantity := decimal.NewFromInt(1)
	if req.Quantity != nil {
		quantity = *req.Quantity
	}
	if !finite(quantity) {
		fields["quantity"] = "must be a decimal within range"
	}
	if req.Usage != "" && !bomUsages[req.Usage] {
		fields["usage"] = "must be production, engineering, costing, maintenance, sales or universal"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	levels := req.Levels
	if levels == 0 {
		levels = domain.MaxLevels
	}
	snapshot, err := a.store.Catalog(r.Context(), []string{req.ProductID}, false)
	if err != nil {
		a.fail(w, r, "load bom catalog", err, &a.bomFailed)
		return
	}
	result, err := domain.Explode(snapshot, domain.ExplodeRequest{ProductID: req.ProductID, SiteID: req.SiteID, AsOf: asOf, Quantity: quantity, Levels: levels, Usage: req.Usage})
	if err != nil {
		a.fail(w, r, "explode bom", err, &a.bomFailed)
		return
	}
	a.explosions.Add(1)
	body := explosionBody{Root: bodyFromNode(result.Root), Flat: make([]flatBody, len(result.Flat))}
	for i, f := range result.Flat {
		body.Flat[i] = flatBody{ProductID: f.ProductID, Quantity: f.Quantity}
	}
	for _, m := range result.Alternatives {
		body.Alternatives = append(body.Alternatives, altMemberBody{ParentProductID: m.ParentProductID, ProductID: m.ProductID, LineNo: m.LineNo, Quantity: m.Quantity, Alternative: *bodyFromAlt(m.Alt)})
	}
	httpx.JSON(w, http.StatusOK, body)
}

type usedByBody struct {
	ProductID string       `json:"productId"`
	VersionID string       `json:"versionId"`
	SiteID    string       `json:"siteId,omitempty"`
	Level     int          `json:"level"`
	Parents   []usedByBody `json:"parents,omitempty"`
}

func bodyFromUsedBy(u *domain.UsedBy) usedByBody {
	body := usedByBody{ProductID: u.ProductID, VersionID: u.VersionID, SiteID: u.SiteID, Level: u.Level}
	for _, p := range u.Parents {
		body.Parents = append(body.Parents, bodyFromUsedBy(p))
	}
	return body
}

func (a *API) whereUsed(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	validID("component", q.Get("component"), fields)
	asOf := a.today()
	if raw := q.Get("asOf"); raw != "" {
		var ok bool
		if asOf, ok = parseDate(raw); !ok {
			fields["asOf"] = "must be a date as YYYY-MM-DD"
		}
	}
	levels := domain.MaxLevels
	if raw := q.Get("levels"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > domain.MaxLevels {
			fields["levels"] = fmt.Sprintf("must be 1 to %d", domain.MaxLevels)
		}
		levels = n
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	snapshot, err := a.store.Catalog(r.Context(), []string{q.Get("component")}, true)
	if err != nil {
		a.fail(w, r, "load where-used catalog", err, &a.bomFailed)
		return
	}
	used, err := domain.WhereUsed(snapshot, q.Get("component"), asOf, levels)
	if err != nil {
		a.fail(w, r, "where-used", err, &a.bomFailed)
		return
	}
	body := page[usedByBody]{Items: make([]usedByBody, len(used))}
	for i, u := range used {
		body.Items[i] = bodyFromUsedBy(u)
	}
	httpx.JSON(w, http.StatusOK, body)
}

type leadTimeRequest struct {
	Operations  []domain.RouteOperation `json:"operations"`
	Quantity    decimal.Decimal         `json:"quantity"`
	HoursPerDay decimal.Decimal         `json:"hoursPerDay"`
}

type leadTimeBody struct {
	Hours        decimal.Decimal `json:"hours"`
	CalendarDays int             `json:"calendarDays"`
}

func (a *API) leadTime(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req leadTimeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Operations) == 0 || len(req.Operations) > maxOperations {
		fields["operations"] = fmt.Sprintf("must contain 1 to %d operations", maxOperations)
	}
	if !finite(req.Quantity, req.HoursPerDay) {
		fields["quantity"] = "quantity and hoursPerDay must be decimals within range"
	}
	for _, o := range req.Operations {
		if !finite(o.SetupH, o.RunH, o.ProcessQty, o.QueueBeforeH, o.QueueAfterH, o.TransitH, o.EfficiencyPct, o.OverlapQty) {
			fields["operations"] = "operation times must be decimals within range"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	result, err := domain.LeadTime(req.Operations, req.Quantity, req.HoursPerDay)
	if err != nil {
		a.fail(w, r, "route lead time", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, leadTimeBody{Hours: result.Hours, CalendarDays: result.CalendarDays})
}

type replacementBody struct {
	OldVersionID string `json:"oldVersionId"`
	NewVersionID string `json:"newVersionId"`
}

type ecoBody struct {
	ID             string            `json:"id,omitempty"`
	Number         string            `json:"number"`
	State          string            `json:"state,omitempty"`
	ReleaseDate    string            `json:"releaseDate,omitempty"`
	DecidedBy      string            `json:"decidedBy,omitempty"`
	CreatedBy      string            `json:"createdBy,omitempty"`
	DecisionReason string            `json:"decisionReason,omitempty"`
	EcrID          string            `json:"ecrId,omitempty"`
	Replaces       []replacementBody `json:"replaces"`
}

func bodyFromEco(e domain.Eco) ecoBody {
	body := ecoBody{ID: e.ID, Number: e.Number, State: string(e.State), ReleaseDate: formatDate(e.ReleaseDate), DecidedBy: e.DecidedBy, CreatedBy: e.CreatedBy, DecisionReason: e.DecisionReason, EcrID: e.EcrID, Replaces: make([]replacementBody, len(e.Replaces))}
	for i, r := range e.Replaces {
		body.Replaces[i] = replacementBody{OldVersionID: r.OldVersionID, NewVersionID: r.NewVersionID}
	}
	return body
}

func (b ecoBody) toDomain() (domain.Eco, map[string]string) {
	fields := map[string]string{}
	number := strings.TrimSpace(b.Number)
	if n := len(number); n == 0 || n > maxNumberLength {
		fields["number"] = fmt.Sprintf("must be 1 to %d characters", maxNumberLength)
	}
	eco := domain.Eco{ID: ids.New(), Number: number, State: domain.EcoSubmitted}
	if b.EcrID != "" {
		checkUUID("ecrId", b.EcrID, fields)
		eco.EcrID = b.EcrID
	}
	eco.ReleaseDate = optionalDate("releaseDate", b.ReleaseDate, fields)
	if len(b.Replaces) == 0 || len(b.Replaces) > maxLines {
		fields["replaces"] = fmt.Sprintf("must contain 1 to %d replacements", maxLines)
	}
	seen := map[string]bool{}
	for _, r := range b.Replaces {
		if !ids.Valid(r.OldVersionID) || !ids.Valid(r.NewVersionID) || r.OldVersionID == r.NewVersionID || seen[r.NewVersionID] {
			fields["replaces"] = "each replacement needs two different valid version ids and a new version used once"
		}
		seen[r.NewVersionID] = true
		eco.Replaces = append(eco.Replaces, domain.Replacement{OldVersionID: r.OldVersionID, NewVersionID: r.NewVersionID})
	}
	return eco, fields
}

func (a *API) createEco(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body ecoBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	eco, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if err := domain.CheckReleaseDate(eco.ReleaseDate, a.today()); err != nil {
		a.fail(w, r, "create eco", err, &a.ecosFailed)
		return
	}
	creator, _ := tenant.FromContext(r.Context())
	if creator.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "creating an ECO needs the acting user"))
		return
	}
	eco.CreatedBy = creator.UserID
	created, err := a.store.CreateEco(r.Context(), eco, store.Idem{Key: r.Header.Get(idempotencyKey), Hash: hashOf(body)})
	if err != nil {
		a.fail(w, r, "create eco", err, &a.ecosFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromEco(created))
}

func (a *API) releaseEco(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "ECO id must be a UUID"))
		return
	}
	record, err := a.store.GetEco(r.Context(), id)
	if err != nil {
		a.fail(w, r, "load eco", err, &a.ecosFailed)
		return
	}
	eco, previous := record.Eco, record.Eco.State
	if previous == domain.EcoReleased {
		httpx.JSON(w, http.StatusOK, bodyFromEco(eco))
		return
	}
	if eco.State == domain.EcoSubmitted {
		if err := eco.Review(r.Context(), a.approver); err != nil {
			a.fail(w, r, "review eco", err, &a.ecosFailed)
			return
		}
	}
	plan, err := eco.Release(record.Versions, a.today())
	if err != nil {
		if eco.State == domain.EcoRejected && previous != domain.EcoRejected {
			if persistErr := a.store.ReleaseEco(r.Context(), eco, previous, domain.ReleasePlan{}); persistErr != nil {
				a.fail(w, r, "persist eco rejection", persistErr, &a.ecosFailed)
				return
			}
		}
		a.fail(w, r, "release eco", err, &a.ecosFailed)
		return
	}
	if len(plan.Activate) > 0 {
		snapshot, err := a.store.Catalog(r.Context(), plan.ActivatedComponents(), false)
		if err == nil {
			err = plan.CheckCycles(snapshot)
		}
		if err != nil {
			a.fail(w, r, "check eco cycles", err, &a.ecosFailed)
			return
		}
	}
	if err := a.store.ReleaseEco(r.Context(), eco, previous, plan); err != nil {
		a.fail(w, r, "persist eco release", err, &a.ecosFailed)
		return
	}
	if eco.State == domain.EcoReleased {
		a.ecosReleased.Add(1)
	}
	httpx.JSON(w, http.StatusOK, bodyFromEco(eco))
}

func (a *API) decideEco(to domain.EcoState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "ECO id must be a UUID"))
			return
		}
		record, err := a.store.GetEco(r.Context(), id)
		if err != nil {
			a.fail(w, r, "load eco", err, &a.ecosFailed)
			return
		}
		var decision struct {
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &decision); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		if len([]rune(decision.Reason)) > maxReasonLength {
			invalid(w, r, map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxReasonLength)})
			return
		}
		eco, previous := record.Eco, record.Eco.State
		if previous == to {
			httpx.JSON(w, http.StatusOK, bodyFromEco(eco))
			return
		}
		p, _ := tenant.FromContext(r.Context())
		if err := eco.Decide(to, p.UserID, strings.TrimSpace(decision.Reason)); err != nil {
			a.fail(w, r, "decide eco", err, &a.ecosFailed)
			return
		}
		if err := a.store.ReleaseEco(r.Context(), eco, previous, domain.ReleasePlan{}); err != nil {
			a.fail(w, r, "persist eco decision", err, &a.ecosFailed)
			return
		}
		slog.InfoContext(r.Context(), "eco decided", "ecoId", eco.ID, "state", string(to), "decidedBy", p.UserID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusOK, bodyFromEco(eco))
	}
}

func (a *API) today() time.Time {
	y, m, d := a.now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, operation string, err error, failures *atomic.Int64) {
	var de *domain.Error
	var problem *httpx.Problem
	switch {
	case errors.As(err, &de):
		if de.Code == domain.CodeEcoApproverUnavailable {
			failures.Add(1)
			slog.ErrorContext(r.Context(), operation+" failed", "code", de.Code, "err", de.Cause, "correlationId", middleware.GetReqID(r.Context()))
		} else {
			slog.WarnContext(r.Context(), operation+" rejected", "code", de.Code, "detail", de.Detail, "correlationId", middleware.GetReqID(r.Context()))
		}
		err = problemFrom(de)
	case errors.As(err, &problem) && problem.Status < http.StatusInternalServerError:
	default:
		failures.Add(1)
		slog.ErrorContext(r.Context(), operation+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}

func problemFrom(de *domain.Error) *httpx.Problem {
	status := http.StatusUnprocessableEntity
	switch de.Code {
	case domain.CodeEcoState, domain.CodeEcrState, domain.CodeEcrConverted, domain.CodeEcrNotApproved, domain.CodeRouteState, domain.CodeRouteOverlap, domain.CodeBomState, domain.CodeBomInEco, domain.CodeBomVersionConflict, domain.CodeRouteConflict, domain.CodeResInUse, domain.CodeResConflict:
		status = http.StatusConflict
	case domain.CodeEcoApproverUnavailable:
		status = http.StatusServiceUnavailable
	case domain.CodeEcoSelfApproval, domain.CodeEcrSelfApproval, domain.CodeEcrNotRequester, domain.CodeEcoCreatorUnknown, domain.CodeEcoBackdateDuty:
		status = http.StatusForbidden
	}
	return httpx.NewProblem(status, de.Code, de.Detail)
}

func invalid(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func hashOf(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
