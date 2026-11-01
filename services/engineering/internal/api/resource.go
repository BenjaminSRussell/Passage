package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const (
	maxClosedDates          = 2000
	maxResourceCapabilities = 100
	maxNameLength           = 200
	weekDays                = 7
)

var (
	hundred          = decimal.NewFromInt(100)
	maxCapacityPerDH = decimal.NewFromInt(1_000_000)
	resourceTypes    = map[string]bool{domain.TypeMachine: true, domain.TypeTool: true, domain.TypeLabor: true, domain.TypeVendor: true, domain.TypeLocation: true}
)

type calendarBody struct {
	Code        string           `json:"code"`
	Name        string           `json:"name"`
	Template    [][]domain.Shift `json:"template"`
	ClosedDates []string         `json:"closedDates"`
}

type calendarView struct {
	ID            string           `json:"id"`
	Code          string           `json:"code"`
	Name          string           `json:"name"`
	Template      [][]domain.Shift `json:"template"`
	ClosedDates   []string         `json:"closedDates"`
	Version       int              `json:"version"`
	GeneratedFrom string           `json:"generatedFrom,omitempty"`
	GeneratedTo   string           `json:"generatedTo,omitempty"`
	Warnings      []string         `json:"warnings"`
}

type rangeBody struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type generationBody struct {
	OpenDays   int             `json:"openDays"`
	TotalHours decimal.Decimal `json:"totalHours"`
}

type dayBody struct {
	Date   string          `json:"date"`
	Open   bool            `json:"open"`
	Shifts []domain.Shift  `json:"shifts"`
	TotalH decimal.Decimal `json:"totalH"`
}

type groupBody struct {
	ID         string `json:"id,omitempty"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	SiteID     string `json:"siteId"`
	CalendarID string `json:"calendarId,omitempty"`
}

type capabilityBody struct {
	ID   string `json:"id,omitempty"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type resourceCapabilityBody struct {
	CapabilityID  string `json:"capabilityId"`
	Level         int    `json:"level"`
	EffectiveFrom string `json:"effectiveFrom"`
	EffectiveTo   string `json:"effectiveTo,omitempty"`
}

type resourceBody struct {
	ID                  string                   `json:"id,omitempty"`
	Code                string                   `json:"code"`
	Name                string                   `json:"name"`
	Type                string                   `json:"type"`
	SiteID              string                   `json:"siteId"`
	GroupID             string                   `json:"groupId,omitempty"`
	CalendarID          string                   `json:"calendarId,omitempty"`
	EfficiencyPct       *decimal.Decimal         `json:"efficiencyPct,omitempty"`
	CapacityPerDayH     *decimal.Decimal         `json:"capacityPerDayH,omitempty"`
	Exclusive           bool                     `json:"exclusive"`
	VendorPartyID       string                   `json:"vendorPartyId,omitempty"`
	Capabilities        []resourceCapabilityBody `json:"capabilities"`
	EffectiveCalendarID string                   `json:"effectiveCalendarId,omitempty"`
	Version             int                      `json:"version,omitempty"`
}

func requireName(value string, fields map[string]string) {
	if n := len(strings.TrimSpace(value)); n == 0 || n > maxNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxNameLength)
	}
}

func checkUUID(field, value string, fields map[string]string) {
	if value != "" && !ids.Valid(value) {
		fields[field] = "must be a UUID"
	}
}

func dateRange(from, to string, fields map[string]string) (time.Time, time.Time) {
	f, okFrom := parseDate(from)
	t, okTo := parseDate(to)
	if !okFrom {
		fields["from"] = "is required as YYYY-MM-DD"
	}
	if !okTo {
		fields["to"] = "is required as YYYY-MM-DD"
	}
	return f, t
}

func readBody(w http.ResponseWriter, r *http.Request, body any) bool {
	if !validTenant(w, r) {
		return false
	}
	if err := httpx.Decode(r, body); err != nil {
		httpx.Fail(w, r, err)
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "id must be a UUID"))
		return "", false
	}
	return id, true
}

func idemOf(r *http.Request, body any) store.Idem {
	return store.Idem{Key: r.Header.Get(idempotencyKey), Hash: hashOf(body)}
}

func (b calendarBody) toDomain() (store.Calendar, map[string]string) {
	fields := map[string]string{}
	validID("code", b.Code, fields)
	requireName(b.Name, fields)
	cal := store.Calendar{ID: ids.New(), WorkCalendar: domain.WorkCalendar{Code: b.Code, Name: b.Name}}
	if len(b.Template) != weekDays {
		fields["template"] = fmt.Sprintf("must hold %d day patterns, Monday first", weekDays)
	}
	for i, shifts := range b.Template {
		for _, s := range shifts {
			if s.EfficiencyPct != nil && !finite(*s.EfficiencyPct) {
				fields["template"] = "shift efficiencyPct must be a decimal within range"
			}
		}
		if i < weekDays {
			cal.Template[i] = shifts
		}
	}
	if len(b.ClosedDates) > maxClosedDates {
		fields["closedDates"] = fmt.Sprintf("must hold at most %d dates", maxClosedDates)
	}
	for _, raw := range b.ClosedDates {
		d, ok := parseDate(raw)
		if !ok {
			fields["closedDates"] = "every entry must be a date as YYYY-MM-DD"
		}
		cal.ClosedDates = append(cal.ClosedDates, d)
	}
	return cal, fields
}

func bodyFromCalendar(c store.Calendar, warnings []string) calendarView {
	view := calendarView{
		ID: c.ID, Code: c.Code, Name: c.Name, Version: c.Version, GeneratedFrom: formatDate(c.GeneratedFrom), GeneratedTo: formatDate(c.GeneratedTo),
		Template: make([][]domain.Shift, weekDays), ClosedDates: make([]string, len(c.ClosedDates)), Warnings: append([]string{}, warnings...),
	}
	for i, shifts := range c.Template {
		view.Template[i] = append([]domain.Shift{}, shifts...)
	}
	for i, d := range c.ClosedDates {
		view.ClosedDates[i] = formatDate(d)
	}
	return view
}

func (a *API) createCalendar(w http.ResponseWriter, r *http.Request) {
	var body calendarBody
	if !readBody(w, r, &body) {
		return
	}
	calendar, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, warnings, err := a.store.CreateCalendar(r.Context(), calendar, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create calendar", err, &a.resourceFailed)
		return
	}
	a.calendarsCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, bodyFromCalendar(created, warnings))
}

func (a *API) generateCalendar(w http.ResponseWriter, r *http.Request) {
	var body rangeBody
	if !readBody(w, r, &body) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	from, to := dateRange(body.From, body.To, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	result, err := a.store.GenerateCalendar(r.Context(), id, from, to)
	if err != nil {
		a.fail(w, r, "generate calendar", err, &a.resourceFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, generationBody{OpenDays: result.OpenDays, TotalHours: result.TotalHours})
}

func (a *API) calendarDays(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	fields := map[string]string{}
	from, to := dateRange(r.URL.Query().Get("from"), r.URL.Query().Get("to"), fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	days, err := a.store.ListCalendarDays(r.Context(), id, from, to)
	if err != nil {
		a.fail(w, r, "list calendar days", err, &a.resourceFailed)
		return
	}
	body := page[dayBody]{Items: make([]dayBody, len(days))}
	for i, d := range days {
		body.Items[i] = dayBody{Date: formatDate(d.Date), Open: d.Open, Shifts: append([]domain.Shift{}, d.Shifts...), TotalH: d.TotalH}
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (b groupBody) toDomain() (domain.ResourceGroup, map[string]string) {
	fields := map[string]string{}
	validID("code", b.Code, fields)
	requireName(b.Name, fields)
	validID("siteId", b.SiteID, fields)
	checkUUID("calendarId", b.CalendarID, fields)
	return domain.ResourceGroup{ID: ids.New(), Code: b.Code, Name: b.Name, SiteID: b.SiteID, CalendarID: b.CalendarID}, fields
}

func (a *API) createResourceGroup(w http.ResponseWriter, r *http.Request) {
	var body groupBody
	if !readBody(w, r, &body) {
		return
	}
	group, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateResourceGroup(r.Context(), group, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create resource group", err, &a.resourceFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, groupBody{ID: created.ID, Code: created.Code, Name: created.Name, SiteID: created.SiteID, CalendarID: created.CalendarID})
}

func (b capabilityBody) toDomain() (domain.Capability, map[string]string) {
	fields := map[string]string{}
	validID("code", b.Code, fields)
	requireName(b.Name, fields)
	return domain.Capability{ID: ids.New(), Code: b.Code, Name: b.Name}, fields
}

func (a *API) createCapability(w http.ResponseWriter, r *http.Request) {
	var body capabilityBody
	if !readBody(w, r, &body) {
		return
	}
	capability, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateCapability(r.Context(), capability, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create capability", err, &a.resourceFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, capabilityBody{ID: created.ID, Code: created.Code, Name: created.Name})
}

func (c resourceCapabilityBody) toDomain() (domain.ResourceCapability, string) {
	dates := map[string]string{}
	capability := domain.ResourceCapability{CapabilityID: c.CapabilityID, Level: c.Level, EffectiveTo: optionalDate("effectiveTo", c.EffectiveTo, dates)}
	var ok bool
	capability.EffectiveFrom, ok = parseDate(c.EffectiveFrom)
	switch {
	case !ids.Valid(c.CapabilityID):
		return capability, "capabilityId must be a UUID"
	case c.Level < 1:
		return capability, "level must be at least 1"
	case !ok || len(dates) > 0:
		return capability, "effectiveFrom is required and effectiveTo optional, both as YYYY-MM-DD"
	case !capability.EffectiveTo.IsZero() && capability.EffectiveTo.Before(capability.EffectiveFrom):
		return capability, "effectiveTo must not be before effectiveFrom"
	}
	return capability, ""
}

func (b resourceBody) toDomain() (domain.Resource, map[string]string) {
	fields := map[string]string{}
	validID("code", b.Code, fields)
	requireName(b.Name, fields)
	validID("siteId", b.SiteID, fields)
	checkUUID("groupId", b.GroupID, fields)
	checkUUID("calendarId", b.CalendarID, fields)
	if !resourceTypes[b.Type] {
		fields["type"] = "must be machine, tool, labor, vendor or location"
	}
	if len(b.VendorPartyID) > maxIDLength {
		fields["vendorPartyId"] = fmt.Sprintf("must be at most %d characters", maxIDLength)
	}
	r := domain.Resource{
		ID: ids.New(), Code: b.Code, Name: b.Name, Type: b.Type, SiteID: b.SiteID, GroupID: b.GroupID, CalendarID: b.CalendarID,
		EfficiencyPct: hundred, VendorPartyID: b.VendorPartyID, Exclusive: b.Exclusive,
	}
	if e := b.EfficiencyPct; e != nil {
		if !finite(*e) || !e.IsPositive() || e.GreaterThan(hundred) || !e.Round(2).Equal(*e) {
			fields["efficiencyPct"] = "must be above 0 and at most 100 with at most two decimals"
		}
		r.EfficiencyPct = *e
	}
	if c := b.CapacityPerDayH; c != nil {
		if !finite(*c) || c.IsNegative() || !c.LessThan(maxCapacityPerDH) || !c.Round(4).Equal(*c) {
			fields["capacityPerDayH"] = "must be a non-negative decimal below 1000000 with at most four decimals"
		}
		r.CapacityPerDayH = *c
	}
	if len(b.Capabilities) > maxResourceCapabilities {
		fields["capabilities"] = fmt.Sprintf("must hold at most %d entries", maxResourceCapabilities)
	}
	for i, cb := range b.Capabilities {
		capability, problem := cb.toDomain()
		if problem != "" {
			fields[fmt.Sprintf("capabilities[%d]", i)] = problem
		}
		r.Capabilities = append(r.Capabilities, capability)
	}
	return r, fields
}

func bodyFromResource(rec store.ResourceRecord) resourceBody {
	r := rec.Resource
	efficiency, capacity := r.EfficiencyPct, r.CapacityPerDayH
	body := resourceBody{
		ID: r.ID, Code: r.Code, Name: r.Name, Type: r.Type, SiteID: r.SiteID, GroupID: r.GroupID, CalendarID: r.CalendarID,
		EfficiencyPct: &efficiency, CapacityPerDayH: &capacity, Exclusive: r.Exclusive, VendorPartyID: r.VendorPartyID,
		Capabilities: make([]resourceCapabilityBody, len(r.Capabilities)), EffectiveCalendarID: rec.EffectiveCalendarID, Version: r.Version,
	}
	for i, c := range r.Capabilities {
		body.Capabilities[i] = resourceCapabilityBody{CapabilityID: c.CapabilityID, Level: c.Level, EffectiveFrom: formatDate(c.EffectiveFrom), EffectiveTo: formatDate(c.EffectiveTo)}
	}
	return body
}

func (a *API) createResource(w http.ResponseWriter, r *http.Request) {
	var body resourceBody
	if !readBody(w, r, &body) {
		return
	}
	resource, fields := body.toDomain()
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateResource(r.Context(), resource, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "create resource", err, &a.resourceFailed)
		return
	}
	a.resourcesCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, bodyFromResource(created))
}

func (a *API) getResource(w http.ResponseWriter, r *http.Request) {
	getByID(a, w, r, "get resource", a.store.GetResource, bodyFromResource)
}

// updateResource saves an edited resource: the request carries the whole editable record plus the
// version number the editor loaded. Capabilities are replaced as a whole. The code and site are the
// resource's identity and cannot change (they may be omitted). A stale version is 409
// ENG-RES-VERSION-CONFLICT and nothing is written.
func (a *API) updateResource(w http.ResponseWriter, r *http.Request) {
	var body resourceBody
	if !readBody(w, r, &body) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	stored, err := a.store.GetResource(r.Context(), id)
	if err != nil {
		a.fail(w, r, "update resource", err, &a.resourceFailed)
		return
	}
	for _, c := range []struct{ name, got, want string }{{"code", body.Code, stored.Resource.Code}, {"siteId", body.SiteID, stored.Resource.SiteID}} {
		if c.got != "" && c.got != c.want {
			invalid(w, r, map[string]string{c.name: "cannot be changed; create another resource instead"})
			return
		}
	}
	body.Code, body.SiteID = stored.Resource.Code, stored.Resource.SiteID
	resource, fields := body.toDomain()
	if body.Version < 1 {
		fields["version"] = "is required: the version number you loaded"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	resource.ID = id
	updated, err := a.store.UpdateResource(r.Context(), resource, body.Version, copyIdem(r, id, body))
	if err != nil {
		if domain.HasCode(err, domain.CodeResConflict) {
			a.resourceConflicts.Add(1)
		}
		a.fail(w, r, "update resource", err, &a.resourceFailed)
		return
	}
	a.resourcesUpdated.Add(1)
	httpx.JSON(w, http.StatusOK, bodyFromResource(updated))
}

// deleteResource removes a resource (BR-9.11). It is 409 ENG-RES-IN-USE while an active route
// version requires the resource directly.
func (a *API) deleteResource(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteResource(r.Context(), id); err != nil {
		a.fail(w, r, "delete resource", err, &a.resourceFailed)
		return
	}
	a.resourcesDeleted.Add(1)
	w.WriteHeader(http.StatusNoContent)
}
