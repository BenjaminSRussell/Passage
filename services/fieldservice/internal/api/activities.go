package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// dutyActivity guards activities on any readable record (PRD 8.45.17 ActivityMaintain).
const dutyActivity = "ActivityMaintain"

const (
	maxSubject         = 200
	maxAssociations    = 20
	maxReminderMinutes = 10080
	// maxCalendarEntries bounds one calendar export.
	maxCalendarEntries = 500
	calendarMIMEType   = "text/calendar; charset=utf-8"
)

func (a *API) registerActivities(r chi.Router) {
	r.With(authz.RequireDuty(dutyActivity)).Post("/v1/service/activities", a.createActivity)
	r.With(authz.RequireDuty(dutyActivity)).Get("/v1/service/activities", a.listActivities)
	r.With(authz.RequireDuty(dutyActivity)).Get("/v1/service/activities:calendar", a.activityCalendar)
	r.With(authz.RequireDuty(dutyActivity)).Get("/v1/service/activities/{id}", a.getActivity)
	r.With(authz.RequireDuty(dutyActivity)).Post("/v1/service/activities/{id}:complete", a.completeActivity)
	r.With(authz.RequireDuty(dutyActivity)).Post("/v1/service/activities/{id}:cancel", a.cancelActivity)
	r.With(authz.RequireDuty(dutyActivity)).Post("/v1/service/activities/{id}:reschedule", a.rescheduleActivity)
}

type scheduleRequest struct {
	DueAt   *string `json:"dueAt"`
	StartAt *string `json:"startAt"`
	EndAt   *string `json:"endAt"`
}

type activityRequest struct {
	Kind            string              `json:"kind"`
	Subject         string              `json:"subject"`
	OwnerWorkerID   string              `json:"ownerWorkerId"`
	DueAt           *string             `json:"dueAt"`
	StartAt         *string             `json:"startAt"`
	EndAt           *string             `json:"endAt"`
	ReminderMinutes *int                `json:"reminderMinutes"`
	Associations    []store.Association `json:"associations"`
}

// scheduleOf parses and checks the three activity dates.
func scheduleOf(fields map[string]string, s scheduleRequest) store.Schedule {
	out := store.Schedule{DueAt: instantField(fields, "dueAt", s.DueAt), StartAt: instantField(fields, "startAt", s.StartAt),
		EndAt: instantField(fields, "endAt", s.EndAt)}
	if out.EndAt != nil && out.StartAt != nil && out.EndAt.Before(*out.StartAt) {
		fields["endAt"] = "must not be before startAt"
	}
	return out
}

func (a *API) createActivity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req activityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidActivityKind(req.Kind) {
		fields["kind"] = "must be one of " + strings.Join(domain.ActivityKinds, ", ")
	}
	subject := strings.TrimSpace(req.Subject)
	if n := len([]rune(subject)); n < 1 || n > maxSubject {
		fields["subject"] = fmt.Sprintf("must be 1 to %d characters", maxSubject)
	}
	uuidField(fields, "ownerWorkerId", req.OwnerWorkerID)
	when := scheduleOf(fields, scheduleRequest{DueAt: req.DueAt, StartAt: req.StartAt, EndAt: req.EndAt})
	if req.ReminderMinutes != nil && (*req.ReminderMinutes < 0 || *req.ReminderMinutes > maxReminderMinutes) {
		fields["reminderMinutes"] = fmt.Sprintf("must be 0 to %d", maxReminderMinutes)
	}
	if len(req.Associations) > maxAssociations {
		fields["associations"] = fmt.Sprintf("must hold at most %d records", maxAssociations)
	}
	seen := map[store.Association]bool{}
	for i, as := range req.Associations {
		name := "associations[" + strconv.Itoa(i) + "]"
		if !domain.ValidAssociation(as.EntityType) {
			fields[name+".entityType"] = "must be one of " + strings.Join(domain.AssociableTypes, ", ")
		}
		uuidField(fields, name+".entityId", as.EntityID)
		if seen[as] {
			fields[name] = "names the same record twice"
		}
		seen[as] = true
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	act, replayed, err := a.store.CreateActivity(r.Context(), store.Activity{
		Kind: req.Kind, Subject: subject, OwnerWorkerID: req.OwnerWorkerID,
		DueAt: when.DueAt, StartAt: when.StartAt, EndAt: when.EndAt, ReminderMinutes: req.ReminderMinutes,
		Associations: req.Associations,
	}, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create activity", err)
		return
	}
	setETag(w, act.Version)
	a.respond(w, replayed, http.StatusCreated, act)
}

func (a *API) getActivity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeActivityNotFound, "activity not found")
	if !ok {
		return
	}
	act, err := a.store.GetActivity(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get activity", err)
		return
	}
	setETag(w, act.Version)
	httpx.JSON(w, http.StatusOK, act)
}

// completeActivity marks an OPEN activity DONE with a timestamp (AC-45.15).
func (a *API) completeActivity(w http.ResponseWriter, r *http.Request) {
	a.activityMove(w, r, "complete activity", a.store.CompleteActivity)
}

// cancelActivity marks an OPEN activity CANCELED.
func (a *API) cancelActivity(w http.ResponseWriter, r *http.Request) {
	a.activityMove(w, r, "cancel activity", a.store.CancelActivity)
}

func (a *API) activityMove(w http.ResponseWriter, r *http.Request, op string, move func(context.Context, string, int) (store.Activity, error)) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeActivityNotFound, "activity not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	act, err := move(r.Context(), id, version)
	if err != nil {
		a.fail(w, r, op, err)
		return
	}
	setETag(w, act.Version)
	httpx.JSON(w, http.StatusOK, act)
}

// rescheduleActivity replaces the due, start and end of an OPEN activity.
func (a *API) rescheduleActivity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeActivityNotFound, "activity not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req scheduleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	when := scheduleOf(fields, req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	act, err := a.store.RescheduleActivity(r.Context(), id, version, when)
	if err != nil {
		a.fail(w, r, "reschedule activity", err)
		return
	}
	setETag(w, act.Version)
	httpx.JSON(w, http.StatusOK, act)
}

// listActivities is "My activities" (owner) or the activities on one record
// (entityType and entityId), earliest due first (PRD 8.45.14).
func (a *API) listActivities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := activityFilter(w, r)
	if !ok {
		return
	}
	page, err := a.store.ListActivities(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list activities", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// activityCalendar exports dated activities as iCalendar (FR-45.14): the start,
// else the due instant, of at most 500 activities of the filter.
func (a *API) activityCalendar(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := activityFilter(w, r)
	if !ok {
		return
	}
	f.Limit, f.Before = maxCalendarEntries, ""
	page, err := a.store.ListActivities(r.Context(), f)
	if err != nil {
		a.fail(w, r, "export activity calendar", err)
		return
	}
	entries := []domain.CalendarEntry{}
	for _, act := range page.Items {
		start := act.StartAt
		if start == nil {
			start = act.DueAt
		}
		if start == nil {
			continue
		}
		e := domain.CalendarEntry{UID: act.ID, Summary: act.Subject, Start: *start, End: act.EndAt, Status: act.Status}
		if act.ReminderMinutes != nil {
			e.ReminderMinutes = *act.ReminderMinutes
		}
		entries = append(entries, e)
	}
	w.Header().Set("Content-Type", calendarMIMEType)
	w.Header().Set("Content-Disposition", `attachment; filename="activities.ics"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(domain.ICalendar(entries, a.now())))
}

// activityFilter reads the list filter: an owner, a record (entityType with
// entityId) or both are required, so no call lists every activity of the tenant.
func activityFilter(w http.ResponseWriter, r *http.Request) (store.ActivityFilter, bool) {
	q := r.URL.Query()
	owner, status, entityType, entityID := q.Get("owner"), q.Get("status"), q.Get("entityType"), q.Get("entityId")
	fields := map[string]string{}
	if owner != "" {
		uuidField(fields, "owner", owner)
	}
	if entityType != "" || entityID != "" {
		if !domain.ValidAssociation(entityType) {
			fields["entityType"] = "must be one of " + strings.Join(domain.AssociableTypes, ", ")
		}
		uuidField(fields, "entityId", entityID)
	}
	if owner == "" && entityType == "" && entityID == "" {
		fields["owner"] = "give owner, or entityType with entityId"
	}
	if status != "" && !oneOf(status, domain.ActivityStatuses...) {
		fields["status"] = "must be one of " + strings.Join(domain.ActivityStatuses, ", ")
	}
	cursor, limit := pageParams(fields, q.Get("cursor"), q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return store.ActivityFilter{}, false
	}
	return store.ActivityFilter{OwnerWorkerID: owner, Status: status, EntityType: entityType, EntityID: entityID, Before: cursor, Limit: limit}, true
}

// instantField parses an optional RFC 3339 timestamp; an invalid value records a field error.
func instantField(fields map[string]string, name string, v *string) *time.Time {
	if v == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		fields[name] = "must be an RFC 3339 timestamp"
		return nil
	}
	t = t.UTC()
	return &t
}
