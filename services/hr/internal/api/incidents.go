package api

import (
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyIncidentReport = "HcmIncidentReport"
	dutyIncidentAdmin  = "HcmIncidentAdmin"

	maxIncidentDays = 365
	maxListEntries  = 20
)

type incidentRequest struct {
	WorkerID            string   `json:"workerId"`
	LegalEntityID       string   `json:"legalEntityId"`
	SiteID              string   `json:"siteId"`
	IncidentType        string   `json:"incidentType"`
	OccurredAt          string   `json:"occurredAt"`
	ReportedAt          string   `json:"reportedAt"`
	Location            string   `json:"location"`
	BodyParts           []string `json:"bodyParts"`
	Death               bool     `json:"death"`
	LossOfConsciousness bool     `json:"lossOfConsciousness"`
	MedicalTreatment    bool     `json:"medicalTreatment"`
	DaysAway            int      `json:"daysAway"`
	DaysRestricted      int      `json:"daysRestricted"`
	OSHARecordable      *bool    `json:"oshaRecordable"`
	OverrideReason      string   `json:"overrideReason"`
	PrivacyCase         bool     `json:"privacyCase"`
	AssetID             *string  `json:"assetId"`
	SupersedesID        *string  `json:"supersedesId"`
	Description         string   `json:"description"`
	Witnesses           []string `json:"witnesses"`
	Treatments          []string `json:"treatments"`
	RootCause           string   `json:"rootCause"`
	CorrectiveAction    string   `json:"correctiveAction"`
}

type incidentStatusRequest struct {
	Status string `json:"status"`
}

func tooLong(s string, max int) bool { return utf8.RuneCountInString(s) > max }

func textList(name string, list []string, maxEntries, maxLen int, fields map[string]string) {
	if len(list) > maxEntries {
		fields[name] = "at most " + strconv.Itoa(maxEntries) + " entries"
		return
	}
	for _, v := range list {
		if v == "" || tooLong(v, maxLen) {
			fields[name] = "each entry must be 1 to " + strconv.Itoa(maxLen) + " characters"
			return
		}
	}
}

func validateIncident(req incidentRequest, now time.Time) (store.IncidentInput, map[string]string) {
	fields := map[string]string{}
	for name, v := range map[string]string{"workerId": req.WorkerID, "legalEntityId": req.LegalEntityID, "siteId": req.SiteID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	for name, v := range map[string]*string{"assetId": req.AssetID, "supersedesId": req.SupersedesID} {
		if v != nil && !ids.Valid(*v) {
			fields[name] = "must be a UUID"
		}
	}
	if !contains(domain.IncidentTypes, req.IncidentType) {
		fields["incidentType"] = "must be one of INJURY, SKIN_DISORDER, RESPIRATORY, POISONING, HEARING_LOSS, OTHER_ILLNESS"
	}
	occurred, err := time.Parse(time.RFC3339, req.OccurredAt)
	if err != nil {
		fields["occurredAt"] = "must be an RFC 3339 timestamp"
	} else if occurred.After(now) {
		fields["occurredAt"] = "cannot be in the future"
	}
	reported := now
	if req.ReportedAt != "" {
		if reported, err = time.Parse(time.RFC3339, req.ReportedAt); err != nil {
			fields["reportedAt"] = "must be an RFC 3339 timestamp"
		}
	}
	if fields["occurredAt"] == "" && fields["reportedAt"] == "" && reported.Before(occurred) {
		fields["reportedAt"] = "cannot be before occurredAt"
	}
	if req.Location == "" || tooLong(req.Location, 200) {
		fields["location"] = "must be 1 to 200 characters"
	}
	if req.Description == "" || tooLong(req.Description, 4000) {
		fields["description"] = "must be 1 to 4000 characters"
	}
	if tooLong(req.RootCause, 2000) {
		fields["rootCause"] = "at most 2000 characters"
	}
	if tooLong(req.CorrectiveAction, 2000) {
		fields["correctiveAction"] = "at most 2000 characters"
	}
	if tooLong(req.OverrideReason, 500) {
		fields["overrideReason"] = "at most 500 characters"
	}
	textList("bodyParts", req.BodyParts, maxListEntries, 60, fields)
	textList("witnesses", req.Witnesses, maxListEntries, 120, fields)
	textList("treatments", req.Treatments, maxListEntries, 200, fields)
	if req.DaysAway < 0 || req.DaysAway > maxIncidentDays {
		fields["daysAway"] = "must be 0 to 365"
	}
	if req.DaysRestricted < 0 || req.DaysRestricted > maxIncidentDays {
		fields["daysRestricted"] = "must be 0 to 365"
	}
	return store.IncidentInput{
		WorkerID: req.WorkerID, LegalEntityID: req.LegalEntityID, SiteID: req.SiteID, IncidentType: req.IncidentType,
		OccurredAt: occurred, ReportedAt: reported, Location: req.Location, BodyParts: req.BodyParts,
		Facts: domain.IncidentFacts{Death: req.Death, LossOfConsciousness: req.LossOfConsciousness, MedicalTreatment: req.MedicalTreatment,
			DaysAway: req.DaysAway, DaysRestricted: req.DaysRestricted},
		RecordableOverride: req.OSHARecordable, OverrideReason: req.OverrideReason, PrivacyCase: req.PrivacyCase, AssetID: req.AssetID, SupersedesID: req.SupersedesID,
		Details: store.IncidentDetails{Description: req.Description, Witnesses: req.Witnesses, Treatments: req.Treatments, RootCause: req.RootCause, CorrectiveAction: req.CorrectiveAction},
	}, fields
}

// reportIncident is POST /v1/hr/incidents (HcmIncidentReport).
func (a *API) reportIncident(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req incidentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := validateIncident(req, time.Now())
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.ReportIncident(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "report incident", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.incidentsReported.Add(1)
	}
	httpx.JSON(w, status, out)
}

// listIncidents is GET /v1/hr/incidents (HcmIncidentAdmin), newest case first.
func (a *API) listIncidents(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	status := r.URL.Query().Get("status")
	before := 0
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil || n < 1 {
			ok = false
		}
		before = n
	}
	if status != "" && status != domain.IncidentReported && status != domain.IncidentUnderReview && status != domain.IncidentClosed {
		validationFailed(w, r, map[string]string{"status": "must be REPORTED, UNDER_REVIEW or CLOSED"})
		return
	}
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListIncidents(r.Context(), status, before, limit)
	if err != nil {
		logFailure(r, "list incidents", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Incident]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

func incidentRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeIncidentNotFound, "incident not found", next)
}

// getIncident is GET /v1/hr/incidents/{id} (HcmIncidentAdmin); the sealed
// details are opened and the read is audited.
func (a *API) getIncident(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetIncident(r.Context(), id)
	if err != nil {
		logFailure(r, "get incident", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// advanceIncident is POST /v1/hr/incidents/{id}/status (HcmIncidentAdmin).
func (a *API) advanceIncident(w http.ResponseWriter, r *http.Request, id string) {
	var req incidentStatusRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Status != domain.IncidentUnderReview && req.Status != domain.IncidentClosed {
		validationFailed(w, r, map[string]string{"status": "must be UNDER_REVIEW or CLOSED"})
		return
	}
	out, changed, err := a.store.AdvanceIncident(r.Context(), id, req.Status)
	if err != nil {
		logFailure(r, "advance incident", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.incidentTransitions.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// oshaExtract is GET /v1/hr/incidents/osha-extract?year=&siteId= (HcmIncidentAdmin).
func (a *API) oshaExtract(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	year, err := strconv.Atoi(q.Get("year"))
	if err != nil || year < 1990 || year > 2200 {
		fields["year"] = "must be a four-digit year"
	}
	var site *string
	if v := q.Get("siteId"); v != "" {
		if !ids.Valid(v) {
			fields["siteId"] = "must be a UUID"
		}
		site = &v
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.OSHAExtract(r.Context(), year, site)
	if err != nil {
		logFailure(r, "osha extract", err)
		httpx.Fail(w, r, err)
		return
	}
	a.oshaExtracts.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}
