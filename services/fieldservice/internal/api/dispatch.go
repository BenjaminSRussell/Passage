package api

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	dutyDispatch = "fieldservice.technician.dispatch"

	maxSkills        = 30
	maxTechSkills    = 40
	maxTerritories   = 50
	maxAddressLength = 500
)

var (
	technicianCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)
	skillCode      = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,39}$`)
)

func (a *API) registerDispatch(r chi.Router) {
	r.With(authz.RequireDuty(dutyDispatch)).Post("/v1/service/technicians", a.createTechnician)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/technicians", a.listTechnicians)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/technicians/{id}", a.getTechnician)
	r.With(authz.RequireDuty(dutyDispatch)).Put("/v1/service/technicians/{id}", a.updateTechnician)
	r.With(authz.RequireDuty(dutyWorkOrder)).Put("/v1/service/work-orders/{id}/scheduling", a.updateScheduling)
	r.With(authz.RequireDuty(dutyDispatch)).Post("/v1/service/work-orders/{id}:assign", a.assignWorkOrder)
	r.With(authz.RequireDuty(dutyDispatch)).Post("/v1/service/work-orders/{id}:unassign", a.unassignWorkOrder)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/dispatch/board", a.dispatchBoard)
	r.With(authz.RequireDuty(dutyDispatch)).Post("/v1/service/dispatch/publish", a.publishDispatch)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/dispatch/runs", a.listDispatchRuns)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/dispatch/runs/{id}", a.getDispatchRun)
	a.registerOptimizations(r)
}

// normalizeSkills upper-cases, deduplicates and sorts skill codes; it reports a field
// error for a malformed code or too many.
func normalizeSkills(fields map[string]string, name string, in []string, limit int) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.ToUpper(strings.TrimSpace(s))
		if !skillCode.MatchString(s) {
			fields[name] = "skills are 1 to 40 characters of A-Z, 0-9, _, . or -"
			return out
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > limit {
		fields[name] = fmt.Sprintf("must hold at most %d skills", limit)
	}
	slices.Sort(out)
	return out
}

// geoPair checks an optional latitude and longitude that come together.
func geoPair(fields map[string]string, latName string, lat, lon *float64) {
	if (lat == nil) != (lon == nil) {
		fields[latName] = "latitude and longitude come together"
		return
	}
	if lat != nil && (*lat < -90 || *lat > 90) {
		fields[latName] = "must be between -90 and 90"
	}
	if lon != nil && (*lon < -180 || *lon > 180) {
		fields["longitude"] = "must be between -180 and 180"
	}
}

type technicianFields struct {
	Name             string   `json:"name"`
	WorkerID         *string  `json:"workerId"`
	LegalEntityID    string   `json:"legalEntityId"`
	TerritoryIDs     []string `json:"territoryIds"`
	Skills           []string `json:"skills"`
	TimeZone         string   `json:"timeZone"`
	ShiftDays        []int    `json:"shiftDays"`
	ShiftStartMinute *int     `json:"shiftStartMinute"`
	ShiftEndMinute   *int     `json:"shiftEndMinute"`
	HomeLatitude     *float64 `json:"homeLatitude"`
	HomeLongitude    *float64 `json:"homeLongitude"`
	TruckLocationID  *string  `json:"truckLocationId"`
	TruckSiteID      *string  `json:"truckSiteId"`
	TruckWarehouseID *string  `json:"truckWarehouseId"`
	MaxDailyMinutes  *int     `json:"maxDailyMinutes"`
	OvertimeAllowed  bool     `json:"overtimeAllowed"`
	Active           *bool    `json:"active"`
}

type technicianRequest struct {
	Code string `json:"code"`
	technicianFields
}

// technicianOf validates the shared fields and applies the defaults: Monday to Friday,
// 08:00 to 17:00, 480 daily minutes, active.
func technicianOf(fields map[string]string, f technicianFields) store.Technician {
	t := store.Technician{Name: strings.TrimSpace(f.Name), WorkerID: f.WorkerID, LegalEntityID: f.LegalEntityID, TimeZone: f.TimeZone,
		HomeLatitude: f.HomeLatitude, HomeLongitude: f.HomeLongitude, TruckLocationID: f.TruckLocationID, TruckSiteID: f.TruckSiteID, TruckWarehouseID: f.TruckWarehouseID, OvertimeAllowed: f.OvertimeAllowed,
		ShiftDays: f.ShiftDays, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true}
	if n := len([]rune(t.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	optionalUUID(fields, "workerId", f.WorkerID)
	optionalUUID(fields, "truckLocationId", f.TruckLocationID)
	optionalUUID(fields, "truckSiteId", f.TruckSiteID)
	optionalUUID(fields, "truckWarehouseId", f.TruckWarehouseID)
	if (f.TruckSiteID == nil) != (f.TruckWarehouseID == nil) || (f.TruckLocationID != nil && f.TruckSiteID == nil) {
		fields["truckSiteId"] = "the truck needs both truckSiteId and truckWarehouseId (and them before a truckLocationId)"
	}
	uuidField(fields, "legalEntityId", f.LegalEntityID)
	if len(f.TerritoryIDs) < 1 || len(f.TerritoryIDs) > maxTerritories {
		fields["territoryIds"] = fmt.Sprintf("must hold 1 to %d territories", maxTerritories)
	}
	for _, id := range f.TerritoryIDs {
		if !ids.Valid(id) {
			fields["territoryIds"] = "must be UUIDs"
		}
	}
	t.TerritoryIDs = slices.Compact(slices.Sorted(slices.Values(f.TerritoryIDs)))
	t.Skills = normalizeSkills(fields, "skills", f.Skills, maxTechSkills)
	if loc, err := time.LoadLocation(f.TimeZone); err != nil || f.TimeZone == "" || f.TimeZone == "Local" || loc == nil {
		fields["timeZone"] = "must be an IANA time zone name"
	}
	if t.ShiftDays == nil {
		t.ShiftDays = []int{1, 2, 3, 4, 5}
	}
	days := slices.Compact(slices.Sorted(slices.Values(t.ShiftDays)))
	if len(days) < 1 || days[0] < 1 || days[len(days)-1] > 7 {
		fields["shiftDays"] = "must hold ISO weekdays 1 (Monday) to 7 (Sunday)"
	}
	t.ShiftDays = days
	if f.ShiftStartMinute != nil {
		t.ShiftStartMinute = *f.ShiftStartMinute
	}
	if f.ShiftEndMinute != nil {
		t.ShiftEndMinute = *f.ShiftEndMinute
	}
	if t.ShiftStartMinute < 0 || t.ShiftStartMinute >= t.ShiftEndMinute || t.ShiftEndMinute > 1440 {
		fields["shiftStartMinute"] = "the shift must satisfy 0 <= start < end <= 1440"
	}
	if f.MaxDailyMinutes != nil {
		t.MaxDailyMinutes = *f.MaxDailyMinutes
	}
	if t.MaxDailyMinutes < 60 || t.MaxDailyMinutes > 1440 {
		fields["maxDailyMinutes"] = "must be 60 to 1440"
	}
	if f.Active != nil {
		t.Active = *f.Active
	}
	geoPair(fields, "homeLatitude", f.HomeLatitude, f.HomeLongitude)
	return t
}

// createTechnician registers a field technician (PRD 8.45.4).
func (a *API) createTechnician(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req technicianRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !technicianCode.MatchString(req.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	in := technicianOf(fields, req.technicianFields)
	in.Code = req.Code
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	created, replayed, err := a.store.CreateTechnician(r.Context(), in, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create technician", err)
		return
	}
	setETag(w, created.Version)
	a.respond(w, replayed, http.StatusCreated, created)
}

func (a *API) getTechnician(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeTechnicianUnknown, "technician not found")
	if !ok {
		return
	}
	t, err := a.store.GetTechnician(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get technician", err)
		return
	}
	setETag(w, t.Version)
	httpx.JSON(w, http.StatusOK, t)
}

func (a *API) listTechnicians(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := listFilter(w, r, map[string][]string{"active": {"true", "false"}}, "territoryId")
	if !ok {
		return
	}
	f.Active = r.URL.Query().Get("active")
	page, err := a.store.ListTechnicians(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list technicians", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// updateTechnician replaces a technician's editable fields at the version read.
func (a *API) updateTechnician(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeTechnicianUnknown, "technician not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req technicianFields
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := technicianOf(fields, req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	t, err := a.store.UpdateTechnician(r.Context(), id, version, in)
	if err != nil {
		a.fail(w, r, "update technician", err)
		return
	}
	setETag(w, t.Version)
	httpx.JSON(w, http.StatusOK, t)
}

type schedulingRequest struct {
	RequiredSkills     []string   `json:"requiredSkills"`
	WindowStart        *time.Time `json:"windowStart"`
	WindowEnd          *time.Time `json:"windowEnd"`
	Address            string     `json:"address"`
	Latitude           *float64   `json:"latitude"`
	Longitude          *float64   `json:"longitude"`
	EstDurationMinutes int        `json:"estDurationMinutes"`
}

// schedulingOf validates the scheduling data shared by work-order create and update.
func schedulingOf(fields map[string]string, req schedulingRequest) store.Scheduling {
	sc := store.Scheduling{Address: strings.TrimSpace(req.Address), Latitude: req.Latitude, Longitude: req.Longitude, EstDurationMinutes: req.EstDurationMinutes}
	sc.RequiredSkills = normalizeSkills(fields, "requiredSkills", req.RequiredSkills, maxSkills)
	if (req.WindowStart == nil) != (req.WindowEnd == nil) {
		fields["windowStart"] = "the window start and end come together"
	} else if req.WindowStart != nil {
		start, end := req.WindowStart.UTC(), req.WindowEnd.UTC()
		if !end.After(start) {
			fields["windowEnd"] = "must be after windowStart"
		}
		sc.WindowStart, sc.WindowEnd = &start, &end
	}
	if len([]rune(sc.Address)) > maxAddressLength {
		fields["address"] = fmt.Sprintf("must be at most %d characters", maxAddressLength)
	}
	geoPair(fields, "latitude", req.Latitude, req.Longitude)
	if req.EstDurationMinutes < 1 || req.EstDurationMinutes > 1440 {
		fields["estDurationMinutes"] = "must be 1 to 1440"
	}
	return sc
}

// updateScheduling sets the skills, customer window, address, position and estimate of
// an unscheduled work order.
func (a *API) updateScheduling(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req schedulingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	sc := schedulingOf(fields, req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	wo, err := a.store.UpdateWorkOrderScheduling(r.Context(), id, version, sc)
	if err != nil {
		a.fail(w, r, "update work order scheduling", err)
		return
	}
	setETag(w, wo.Version)
	httpx.JSON(w, http.StatusOK, wo)
}

type assignRequest struct {
	TechnicianID string    `json:"technicianId"`
	Start        time.Time `json:"start"`
	Pinned       bool      `json:"pinned"`
}

// assignWorkOrder places a work order on a technician at a start time (FR-45.7,
// BR-45.3); a broken hard constraint is 422 with every violation in errors.
func (a *API) assignWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req assignRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "technicianId", req.TechnicianID)
	if req.Start.IsZero() {
		fields["start"] = "must be an RFC 3339 instant"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	wo, err := a.store.AssignWorkOrder(r.Context(), id, version, store.AssignInput{TechnicianID: req.TechnicianID, Start: req.Start.UTC(), Pinned: req.Pinned}, a.now())
	if err != nil {
		a.fail(w, r, "assign work order", err)
		return
	}
	setETag(w, wo.Version)
	httpx.JSON(w, http.StatusOK, wo)
}

// unassignWorkOrder takes a SCHEDULED work order off the schedule.
func (a *API) unassignWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	wo, err := a.store.UnassignWorkOrder(r.Context(), id, version, a.now())
	if err != nil {
		a.fail(w, r, "unassign work order", err)
		return
	}
	setETag(w, wo.Version)
	httpx.JSON(w, http.StatusOK, wo)
}

// zoneOf reads a required IANA time zone parameter or body field.
func zoneOf(fields map[string]string, name, value string) string {
	if value == "" {
		value = "UTC"
	}
	if _, err := time.LoadLocation(value); err != nil || value == "Local" {
		fields[name] = "must be an IANA time zone name"
	}
	return value
}

// dispatchBoard returns the board of a territory and local day: technicians with their
// assignments and booked minutes, and the territory's unscheduled work orders.
func (a *API) dispatchBoard(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	uuidField(fields, "territoryId", q.Get("territoryId"))
	if _, ok := parseDate(q.Get("date")); !ok {
		fields["date"] = "must be a date as YYYY-MM-DD"
	}
	zone := zoneOf(fields, "timeZone", q.Get("timeZone"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	b, err := a.store.Board(r.Context(), q.Get("territoryId"), q.Get("date"), zone)
	if err != nil {
		a.fail(w, r, "dispatch board", err)
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

type publishRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	TerritoryID   string `json:"territoryId"`
	Date          string `json:"date"`
	TimeZone      string `json:"timeZone"`
}

// publishDispatch dispatches a territory-day's SCHEDULED work orders (FR-45.7).
func (a *API) publishDispatch(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req publishRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	uuidField(fields, "territoryId", req.TerritoryID)
	if _, ok := parseDate(req.Date); !ok {
		fields["date"] = "must be a date as YYYY-MM-DD"
	}
	zone := zoneOf(fields, "timeZone", req.TimeZone)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	run, replayed, err := a.store.PublishDispatch(r.Context(), store.PublishInput{LegalEntityID: req.LegalEntityID, TerritoryID: req.TerritoryID,
		Date: req.Date, TimeZone: zone}, a.now(), key, hashOf(req))
	if err != nil {
		a.fail(w, r, "publish dispatch", err)
		return
	}
	if !replayed {
		a.published.Add(1)
	}
	a.respond(w, replayed, http.StatusCreated, run)
}

func (a *API) listDispatchRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := listFilter(w, r, nil, "territoryId")
	if !ok {
		return
	}
	page, err := a.store.ListDispatchRuns(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list dispatch runs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getDispatchRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeDispatchRunUnknown, "dispatch run not found")
	if !ok {
		return
	}
	run, err := a.store.GetDispatchRun(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get dispatch run", err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}
