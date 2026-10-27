package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	dutyFiscalCalendar = "org.fiscal-calendar.write"
	fiscalCalendars    = "/v1/org/fiscal-calendars"
	minFiscalYear      = 1900
	maxFiscalYear      = 2999
)

type PostingChecker interface {
	HasPostings(ctx context.Context, calendarID string, fiscalYear int) (bool, error)
}

type noPostings struct{}

func (noPostings) HasPostings(context.Context, string, int) (bool, error) { return false, nil }

func (a *API) WithPostingChecker(c PostingChecker) *API {
	a.postings = c
	return a
}

type createFiscalCalendarRequest struct {
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	Pattern        string  `json:"pattern"`
	StartMonth     *int    `json:"startMonth"`
	WeekStart      *string `json:"weekStart"`
	OpeningClosing *bool   `json:"openingClosing"`
	SpecialPeriods *int    `json:"specialPeriods"`
}

type generateFiscalYearRequest struct {
	FiscalYear *int `json:"fiscalYear"`
	Force      bool `json:"force"`
	Periods    []struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"periods"`
}

type updateFiscalCalendarRequest struct {
	Version *int    `json:"version"`
	Name    *string `json:"name"`
	Status  *string `json:"status"`
}

type fiscalCalendarList struct {
	Items      []store.FiscalCalendar `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

type fiscalPeriodList struct {
	Items []store.FiscalPeriod `json:"items"`
}

func (a *API) registerFiscalCalendars(r chi.Router) {
	r.With(authz.RequireDuty(dutyFiscalCalendar)).Post(fiscalCalendars, a.createFiscalCalendar)
	r.With(authz.RequireDuty(dutyRead)).Get(fiscalCalendars, a.listFiscalCalendars)
	r.With(authz.RequireDuty(dutyFiscalCalendar)).Patch(fiscalCalendars+"/{id}", fiscalCalendarRoute(a.updateFiscalCalendar))
	r.With(authz.RequireDuty(dutyRead)).Get(fiscalCalendars+"/{id}", fiscalCalendarRoute(a.getFiscalCalendar))
	r.With(authz.RequireDuty(dutyFiscalCalendar)).Post(fiscalCalendars+"/{id}:generate", fiscalCalendarRoute(a.generateFiscalYear))
	r.With(authz.RequireDuty(dutyRead)).Get(fiscalCalendars+"/{id}/periods", fiscalCalendarRoute(a.listFiscalPeriods))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/org/legal-entities/{id}/fiscal-period", legalEntityRoute(a.resolveLegalEntityPeriod))
	r.With(authz.RequireDuty(dutyRead)).Get(sites+"/{id}/fiscal-period", siteRoute(a.resolveSitePeriod))
}

func resolveDate(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	date, err := time.Parse(dateLayout, r.URL.Query().Get("date"))
	if err != nil {
		badQuery(w, r, "date", "must be a date in YYYY-MM-DD form")
	}
	return date, err == nil
}

func (a *API) resolveLegalEntityPeriod(w http.ResponseWriter, r *http.Request, id string) {
	date, ok := resolveDate(w, r)
	if !ok {
		return
	}
	resolution, err := a.store.ResolveFiscalPeriod(r.Context(), id, date)
	if err != nil {
		slog.Error("resolve fiscal period failed", "err", err, "legalEntityId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resolution)
}

func (a *API) resolveSitePeriod(w http.ResponseWriter, r *http.Request, id string) {
	date, ok := resolveDate(w, r)
	if !ok {
		return
	}
	resolution, err := a.store.ResolveSiteFiscalPeriod(r.Context(), id, date)
	if err != nil {
		slog.Error("resolve site fiscal period failed", "err", err, "siteId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, resolution)
}

func fiscalCalendarRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !canonicalUUID(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeFiscalCalendarNotFound, "fiscal calendar not found"))
			return
		}
		next(w, r, id)
	}
}

func rejectScopedCaller(w http.ResponseWriter, r *http.Request, action string) bool {
	if p, _ := tenant.FromContext(r.Context()); p.LegalEntityID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a legal-entity-scoped caller cannot "+action))
		return true
	}
	return false
}

func (a *API) createFiscalCalendar(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || rejectScopedCaller(w, r, "create fiscal calendars") {
		return
	}
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
		return
	}
	var req createFiscalCalendarRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.FiscalCalendar{Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name), Pattern: req.Pattern, StartMonth: 1, WeekStart: "monday", OpeningClosing: true}
	fields := validateShared(&in.Name, nil, nil, nil)
	if !siteCode.MatchString(in.Code) {
		fields["code"] = "must be 1 to 10 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if _, err := domain.ParsePattern(req.Pattern); err != nil {
		fields["pattern"] = "must be calendar, 4-4-5, 4-5-4, 5-4-4, 13-period or custom"
	}
	if req.StartMonth != nil {
		if in.StartMonth = *req.StartMonth; in.StartMonth < 1 || in.StartMonth > 12 {
			fields["startMonth"] = "must be 1 to 12"
		}
	}
	if req.WeekStart != nil {
		if in.WeekStart = *req.WeekStart; !validWeekday(in.WeekStart) {
			fields["weekStart"] = "must be a lower-case day name from sunday to saturday"
		}
	}
	if req.SpecialPeriods != nil {
		if in.SpecialPeriods = *req.SpecialPeriods; in.SpecialPeriods < 0 || in.SpecialPeriods > 4 {
			fields["specialPeriods"] = "must be 0 to 4"
		}
	}
	if req.OpeningClosing != nil {
		in.OpeningClosing = *req.OpeningClosing
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	calendar, err := a.store.CreateFiscalCalendar(r.Context(), in, idemKey)
	if err != nil {
		slog.Error("create fiscal calendar failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.fiscalCalendarCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, calendar)
}

func validWeekday(name string) bool {
	_, ok := store.WeekdayNumber(name)
	return ok
}

func (a *API) getFiscalCalendar(w http.ResponseWriter, r *http.Request, id string) {
	calendar, err := a.store.GetFiscalCalendar(r.Context(), id)
	if err != nil {
		slog.Error("get fiscal calendar failed", "err", err, "calendarId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, calendar)
}

func (a *API) listFiscalCalendars(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.FiscalCalendarFilter{Status: q.Get("status"), Pattern: q.Get("pattern")}
	if f.Status != "" && !validStatus(f.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	if _, err := domain.ParsePattern(f.Pattern); f.Pattern != "" && err != nil {
		badQuery(w, r, "pattern", "must be calendar, 4-4-5, 4-5-4, 5-4-4, 13-period or custom")
		return
	}
	page, err := a.store.ListFiscalCalendars(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list fiscal calendars failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := fiscalCalendarList{Items: page.Items}
	if page.NextCode != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextCode))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) updateFiscalCalendar(w http.ResponseWriter, r *http.Request, id string) {
	if rejectScopedCaller(w, r, "update fiscal calendars") {
		return
	}
	var req updateFiscalCalendarRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
		fields = validateShared(req.Name, nil, nil, nil)
	}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.Status != nil && !validStatus(*req.Status) {
		fields["status"] = "must be active or inactive"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	calendar, err := a.store.UpdateFiscalCalendar(r.Context(), id, *req.Version, store.FiscalCalendarPatch{Name: req.Name, Status: req.Status})
	if err != nil {
		slog.Error("update fiscal calendar failed", "err", err, "calendarId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if calendar.Version != *req.Version {
		a.fiscalCalendarUpdated.Add(1)
	}
	httpx.JSON(w, http.StatusOK, calendar)
}

func (a *API) generateFiscalYear(w http.ResponseWriter, r *http.Request, id string) {
	if rejectScopedCaller(w, r, "generate fiscal years") {
		return
	}
	var req generateFiscalYearRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.FiscalYear == nil || *req.FiscalYear < minFiscalYear || *req.FiscalYear > maxFiscalYear {
		fields["fiscalYear"] = fmt.Sprintf("is required and must be an integer from %d to %d", minFiscalYear, maxFiscalYear)
	}
	custom := make([]domain.DateRange, 0, len(req.Periods))
	for i, p := range req.Periods {
		start, startErr := time.Parse(dateLayout, p.Start)
		end, endErr := time.Parse(dateLayout, p.End)
		if startErr != nil {
			fields[fmt.Sprintf("periods[%d].start", i)] = "must be a yyyy-mm-dd date"
		}
		if endErr != nil {
			fields[fmt.Sprintf("periods[%d].end", i)] = "must be a yyyy-mm-dd date"
		}
		custom = append(custom, domain.DateRange{Start: start, End: end})
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	year := *req.FiscalYear
	if req.Force {
		locked, err := a.postings.HasPostings(r.Context(), id, year)
		if err != nil {
			slog.Error("fiscal year posting check failed", "err", err, "calendarId", id, "fiscalYear", year, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "the ledger posting check is unavailable"))
			return
		}
		if locked {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeFiscalYearLocked, fmt.Sprintf("Fiscal year %d has postings and cannot be regenerated.", year)))
			return
		}
	}
	result, err := a.store.GenerateFiscalYear(r.Context(), id, year, custom, req.Force)
	if err != nil {
		slog.Error("generate fiscal year failed", "err", err, "calendarId", id, "fiscalYear", year, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	status := http.StatusOK
	if result.Generated {
		a.fiscalYearGenerated.Add(1)
		status = http.StatusCreated
	}
	httpx.JSON(w, status, result)
}

func (a *API) listFiscalPeriods(w http.ResponseWriter, r *http.Request, id string) {
	year := 0
	if raw := r.URL.Query().Get("fiscalYear"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < minFiscalYear || n > maxFiscalYear {
			badQuery(w, r, "fiscalYear", fmt.Sprintf("must be an integer from %d to %d", minFiscalYear, maxFiscalYear))
			return
		}
		year = n
	}
	periods, err := a.store.ListFiscalPeriods(r.Context(), id, year)
	if err != nil {
		slog.Error("list fiscal periods failed", "err", err, "calendarId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, fiscalPeriodList{Items: periods})
}
