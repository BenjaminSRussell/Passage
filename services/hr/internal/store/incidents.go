package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeIncidentNotFound  = "HCM-INC-NOT-FOUND"
	CodeIncidentCorrected = "HCM-INC-CORRECTED"
	CodeCorrectionDuty    = "HCM-INC-CORRECTION-DUTY"
	CodeIncidentEntity    = "HCM-INC-ENTITY"
	CodeSelfCorrection    = "HCM-INC-SELF-CORRECT"

	dutyIncidentAdmin = "hr.incident.admin"

	fieldIncidentDetails = "hr_incident.details"
	maxOSHALines         = 5000
	privacyCaseName      = "Privacy case"
)

// IncidentDetails is the sealed medical part of an incident (P3): free text
// that can name a worker's injury, treatment or witnesses.
type IncidentDetails struct {
	Description      string   `json:"description"`
	Witnesses        []string `json:"witnesses,omitempty"`
	Treatments       []string `json:"treatments,omitempty"`
	RootCause        string   `json:"rootCause,omitempty"`
	OverrideReason   string   `json:"overrideReason,omitempty"`
	CorrectiveAction string   `json:"correctiveAction,omitempty"`
}

// IncidentInput is a reported incident. RecordableOverride is the caller's
// answer to HCM-INC-001; nil takes the BR-42.11 default.
type IncidentInput struct {
	WorkerID           string
	LegalEntityID      string
	SiteID             string
	IncidentType       string
	OccurredAt         time.Time
	ReportedAt         time.Time
	Location           string
	BodyParts          []string
	Facts              domain.IncidentFacts
	RecordableOverride *bool
	OverrideReason     string
	PrivacyCase        bool
	AssetID            *string
	SupersedesID       *string
	Details            IncidentDetails
}

// Incident is the register row without its sealed details. WorkerID and
// WorkerName are nil in lists and extracts when the case is a privacy case.
type Incident struct {
	ID                  string           `json:"id"`
	CaseNo              int              `json:"caseNo"`
	WorkerID            *string          `json:"workerId,omitempty"`
	WorkerName          *string          `json:"workerName,omitempty"`
	LegalEntityID       string           `json:"legalEntityId"`
	SiteID              string           `json:"siteId"`
	IncidentType        string           `json:"incidentType"`
	OccurredAt          time.Time        `json:"occurredAt"`
	ReportedAt          time.Time        `json:"reportedAt"`
	Location            string           `json:"location"`
	BodyParts           []string         `json:"bodyParts"`
	Death               bool             `json:"death"`
	LossOfConsciousness bool             `json:"lossOfConsciousness"`
	MedicalTreatment    bool             `json:"medicalTreatment"`
	DaysAway            int              `json:"daysAway"`
	DaysRestricted      int              `json:"daysRestricted"`
	OSHARecordable      bool             `json:"oshaRecordable"`
	Outcome             string           `json:"outcome"`
	PrivacyCase         bool             `json:"privacyCase"`
	AssetID             *string          `json:"assetId,omitempty"`
	Status              string           `json:"status"`
	SupersedesID        *string          `json:"supersedesId,omitempty"`
	ReportedBy          string           `json:"reportedBy"`
	ReviewedBy          *string          `json:"reviewedBy,omitempty"`
	ClosedBy            *string          `json:"closedBy,omitempty"`
	ClosedAt            *time.Time       `json:"closedAt,omitempty"`
	CreatedAt           time.Time        `json:"createdAt"`
	Details             *IncidentDetails `json:"details,omitempty"`
	sealed              []byte
}

const incidentSelect = `select i.id::text, i.case_no, i.worker_id::text, w.legal_name, i.legal_entity_id::text, i.site_id::text, i.incident_type,
		i.occurred_at, i.reported_at, i.location_text, i.body_parts, i.death, i.loss_of_consciousness, i.medical_treatment, i.days_away,
		i.days_restricted, i.osha_recordable, i.outcome, i.privacy_case, i.asset_id::text, i.status,
		i.supersedes_id::text, i.reported_by, i.reviewed_by, i.closed_by, i.closed_at, i.created_at, i.details_sealed
	from hr_incident i join worker w on w.tenant_id = i.tenant_id and w.id = i.worker_id`

func scanIncident(row pgx.Row) (Incident, error) {
	var i Incident
	err := row.Scan(&i.ID, &i.CaseNo, &i.WorkerID, &i.WorkerName, &i.LegalEntityID, &i.SiteID, &i.IncidentType, &i.OccurredAt, &i.ReportedAt,
		&i.Location, &i.BodyParts, &i.Death, &i.LossOfConsciousness, &i.MedicalTreatment, &i.DaysAway, &i.DaysRestricted, &i.OSHARecordable,
		&i.Outcome, &i.PrivacyCase, &i.AssetID, &i.Status, &i.SupersedesID, &i.ReportedBy, &i.ReviewedBy,
		&i.ClosedBy, &i.ClosedAt, &i.CreatedAt, &i.sealed)
	i.OccurredAt, i.ReportedAt, i.CreatedAt = utc(i.OccurredAt), utc(i.ReportedAt), utc(i.CreatedAt)
	i.ClosedAt = utcPtr(i.ClosedAt)
	return i, err
}

func (i Incident) hidingPrivacyCase() Incident {
	if i.PrivacyCase {
		i.WorkerID, i.WorkerName = nil, nil
	}
	return i
}

func incidentEvent(i Incident) hrevents.IncidentChangedData {
	e := hrevents.IncidentChangedData{ID: i.ID, CaseNo: i.CaseNo, WorkerID: *i.WorkerID, LegalEntityID: i.LegalEntityID, SiteID: i.SiteID,
		IncidentType: i.IncidentType, Status: i.Status, Outcome: i.Outcome, OSHARecordable: i.OSHARecordable, OccurredAt: i.OccurredAt.Format(time.RFC3339)}
	if i.PrivacyCase {
		e.WorkerID, e.IncidentType, e.Outcome = "", "", ""
	}
	return e
}

func auditEntity(ctx context.Context, tx pgx.Tx, p tenant.Principal, action, entity, entityID string, fields []string, reason string) error {
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	_, err := tx.Exec(ctx, `insert into audit_event (tenant_id, id, actor, action, entity, entity_id, fields, reason)
		values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, ids.New(), p.UserID, action, entity, entityID, fields, reasonPtr)
	return err
}

// ReportIncident records an incident (FR-42.16). osha_recordable defaults from
// the facts (BR-42.11) and an override needs a justification. The medical text
// is sealed. A correction names the CLOSED incident it supersedes. A replayed
// Idempotency-Key returns the stored incident.
func (s *Postgres) ReportIncident(ctx context.Context, in IncidentInput, idempotencyKey string) (Incident, bool, error) {
	p := principal(ctx)
	recordable, rerr := domain.ResolveRecordable(in.Facts, in.RecordableOverride, in.OverrideReason)
	if rerr != nil {
		return Incident{}, false, crsProblem(rerr, http.StatusUnprocessableEntity)
	}
	if in.SupersedesID != nil && !p.HasDuty(dutyIncidentAdmin) {
		return Incident{}, false, problem(http.StatusForbidden, CodeCorrectionDuty, "correcting a closed incident needs the hr.incident.admin duty")
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var inc Incident
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanIncident(tx.QueryRow(ctx, incidentSelect+` where i.tenant_id = $1 and i.idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				inc = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, in.WorkerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		if in.SupersedesID != nil {
			var status, reportedBy string
			var closedBy *string
			err := tx.QueryRow(ctx, `select status, reported_by, closed_by from hr_incident where tenant_id = $1 and id = $2`, p.TenantID, *in.SupersedesID).Scan(&status, &reportedBy, &closedBy)
			if errors.Is(err, pgx.ErrNoRows) {
				return problem(http.StatusNotFound, CodeIncidentNotFound, "the incident to correct does not exist")
			}
			if err != nil {
				return err
			}
			if status != domain.IncidentClosed {
				return problem(http.StatusUnprocessableEntity, domain.CodeIncidentState, "only a CLOSED incident is corrected by a new one; this one is "+status)
			}
			if p.UserID == reportedBy || (closedBy != nil && p.UserID == *closedBy) {
				return problem(http.StatusForbidden, CodeSelfCorrection, "the person who reported or closed an incident cannot file its correction")
			}
		}
		var employed bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from employment where tenant_id = $1 and worker_id = $2 and legal_entity_id = $3 and validity @> $4::timestamptz)`,
			p.TenantID, in.WorkerID, in.LegalEntityID, in.OccurredAt).Scan(&employed); err != nil {
			return err
		}
		if !employed {
			return problem(http.StatusUnprocessableEntity, CodeIncidentEntity, "the worker had no employment in this legal entity when the incident occurred")
		}
		details := in.Details
		if in.RecordableOverride != nil && *in.RecordableOverride != in.Facts.Recordable() {
			details.OverrideReason = in.OverrideReason
		}
		raw, err := json.Marshal(details)
		if err != nil {
			return err
		}
		sealed, err := s.sealer.Seal(p.TenantID, fieldIncidentDetails, string(raw))
		if err != nil {
			return err
		}
		var caseNo int
		if err := tx.QueryRow(ctx, `insert into hr_incident_counter (tenant_id, last_case_no) values ($1, 1)
			on conflict (tenant_id) do update set last_case_no = hr_incident_counter.last_case_no + 1 returning last_case_no`, p.TenantID).Scan(&caseNo); err != nil {
			return err
		}
		bodyParts := in.BodyParts
		if bodyParts == nil {
			bodyParts = []string{}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_incident (tenant_id, id, case_no, worker_id, legal_entity_id, site_id, incident_type, occurred_at, reported_at,
				location_text, body_parts, death, loss_of_consciousness, medical_treatment, days_away, days_restricted, osha_recordable,
				outcome, privacy_case, details_sealed, asset_id, supersedes_id, reported_by, idempotency_key)
			values ($1, $2, $24, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
				$16, $17, $18, $19, $20, $21, $22, $23)`,
			p.TenantID, id, in.WorkerID, in.LegalEntityID, in.SiteID, in.IncidentType, in.OccurredAt, in.ReportedAt, in.Location, bodyParts,
			in.Facts.Death, in.Facts.LossOfConsciousness, in.Facts.MedicalTreatment, in.Facts.DaysAway, in.Facts.DaysRestricted, recordable,
			in.Facts.Outcome(), in.PrivacyCase, sealed, in.AssetID, in.SupersedesID, p.UserID, key, caseNo); err != nil {
			return err
		}
		inc, err = scanIncident(tx.QueryRow(ctx, incidentSelect+` where i.tenant_id = $1 and i.id = $2`, p.TenantID, id))
		if err != nil {
			return err
		}
		inserted = true
		if err := auditEntity(ctx, tx, p, "p3_change", "incident", inc.ID, []string{"details"}, ""); err != nil {
			return err
		}
		return enqueue(ctx, tx, "incident", inc.ID, hrevents.IncidentChanged, p.TenantID, incidentEvent(inc))
	})
	if idempotencyRace(err) {
		return Incident{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) && in.SupersedesID != nil {
		return Incident{}, false, problem(http.StatusConflict, CodeIncidentCorrected, "this incident already has a correction")
	}
	if err == nil && !inserted && (inc.WorkerID == nil || *inc.WorkerID != in.WorkerID || !inc.OccurredAt.Equal(in.OccurredAt) || inc.IncidentType != in.IncidentType) {
		return Incident{}, false, idempotencyReuse()
	}
	return inc, inserted, err
}

// IncidentPage is one keyset page of the register, newest case first.
type IncidentPage struct {
	Items     []Incident
	NextAfter string
}

// ListIncidents pages the register by case number, newest first. The worker of
// a privacy case is hidden (BR-42.11). status filters when set.
func (s *Postgres) ListIncidents(ctx context.Context, status string, beforeCaseNo, limit int) (IncidentPage, error) {
	p := principal(ctx)
	page := IncidentPage{Items: []Incident{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, incidentSelect+` where i.tenant_id = $1 and ($2::text = '' or i.status = $2) and ($3::int = 0 or i.case_no < $3)
			order by i.case_no desc limit $4`, p.TenantID, status, beforeCaseNo, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i, err := scanIncident(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, i.hidingPrivacyCase())
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = strconv.Itoa(page.Items[limit-1].CaseNo)
	}
	return page, err
}

func (s *Postgres) openDetails(tenantID string, sealed []byte) (IncidentDetails, error) {
	var d IncidentDetails
	plain, err := s.sealer.Open(tenantID, fieldIncidentDetails, sealed)
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal([]byte(plain), &d)
}

// GetIncident returns one incident with its opened details. The read is audited
// in the same transaction and fails closed if the audit write fails.
func (s *Postgres) GetIncident(ctx context.Context, id string) (Incident, error) {
	p := principal(ctx)
	var inc Incident
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		inc, err = scanIncident(tx.QueryRow(ctx, incidentSelect+` where i.tenant_id = $1 and i.id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeIncidentNotFound, "incident not found")
		}
		if err != nil {
			return err
		}
		details, err := s.openDetails(p.TenantID, inc.sealed)
		if err != nil {
			return err
		}
		inc.Details = &details
		return auditEntity(ctx, tx, p, "p3_read", "incident", inc.ID, []string{"details"}, "")
	})
	return inc, err
}

// AdvanceIncident moves an incident one step through REPORTED, UNDER_REVIEW,
// CLOSED. The reporter cannot close it. Asking for the status it already has
// changes nothing.
func (s *Postgres) AdvanceIncident(ctx context.Context, id, to string) (Incident, bool, error) {
	p := principal(ctx)
	var inc Incident
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		inc, err = scanIncident(tx.QueryRow(ctx, incidentSelect+` where i.tenant_id = $1 and i.id = $2 for update of i`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeIncidentNotFound, "incident not found")
		}
		if err != nil {
			return err
		}
		if inc.Status == to {
			return nil
		}
		if derr := domain.NextIncidentStatus(inc.Status, to, inc.ReportedBy, p.UserID); derr != nil {
			status := http.StatusUnprocessableEntity
			if derr.Code == domain.CodeIncidentSelfClose {
				status = http.StatusForbidden
			}
			return crsProblem(derr, status)
		}
		column := "reviewed_by"
		closedAt := "null"
		if to == domain.IncidentClosed {
			column, closedAt = "closed_by", "now()"
		}
		if _, err := tx.Exec(ctx, `update hr_incident set status = $3, `+column+` = $4, closed_at = `+closedAt+`, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, to, p.UserID); err != nil {
			return err
		}
		if inc, err = scanIncident(tx.QueryRow(ctx, incidentSelect+` where i.tenant_id = $1 and i.id = $2`, p.TenantID, id)); err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "incident", inc.ID, hrevents.IncidentChanged, p.TenantID, incidentEvent(inc))
	})
	return inc, changed, err
}

// OSHALine is one case of the OSHA 300 log with the 301 detail fields. The
// worker of a privacy case is replaced by "Privacy case" (BR-42.11).
type OSHALine struct {
	IncidentID     string   `json:"incidentId"`
	CaseNo         int      `json:"caseNo"`
	WorkerID       *string  `json:"workerId,omitempty"`
	EmployeeName   string   `json:"employeeName"`
	LegalEntityID  string   `json:"legalEntityId"`
	SiteID         string   `json:"siteId"`
	OccurredOn     string   `json:"occurredOn"`
	ReportedOn     string   `json:"reportedOn"`
	Location       string   `json:"location"`
	IncidentType   string   `json:"incidentType"`
	BodyParts      []string `json:"bodyParts"`
	Outcome        string   `json:"outcome"`
	Death          bool     `json:"death"`
	DaysAway       int      `json:"daysAway"`
	DaysRestricted int      `json:"daysRestricted"`
	PrivacyCase    bool     `json:"privacyCase"`
	Description    string   `json:"description"`
	Witnesses      []string `json:"witnesses"`
	Treatments     []string `json:"treatments"`
}

// OSHAExtract is the 300 log, the 300A summary and the 301 detail for a
// calendar year (FR-42.16). The wave-48 US pack renders and submits it.
type OSHAExtract struct {
	Year      int                `json:"year"`
	SiteID    *string            `json:"siteId,omitempty"`
	Log       []OSHALine         `json:"log"`
	Summary   domain.OSHASummary `json:"summary"`
	Truncated bool               `json:"truncated"`
}

// OSHAExtract lists the recordable incidents that occurred in the year, in
// case order, leaving out any incident a later correction supersedes. Opening
// the sealed details is audited as one export row.
func (s *Postgres) OSHAExtract(ctx context.Context, year int, siteID *string) (OSHAExtract, error) {
	p := principal(ctx)
	out := OSHAExtract{Year: year, SiteID: siteID, Log: []OSHALine{}}
	from := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, incidentSelect+` where i.tenant_id = $1 and i.osha_recordable and i.occurred_at >= $2 and i.occurred_at < $3
			and ($4::uuid is null or i.site_id = $4::uuid)
			and not exists (select 1 from hr_incident c where c.tenant_id = i.tenant_id and c.supersedes_id = i.id and c.status = 'CLOSED')
			order by i.case_no limit $5`, p.TenantID, from, from.AddDate(1, 0, 0), siteID, maxOSHALines+1)
		if err != nil {
			return err
		}
		var incidents []Incident
		for rows.Next() {
			i, err := scanIncident(rows)
			if err != nil {
				rows.Close()
				return err
			}
			incidents = append(incidents, i)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(incidents) > maxOSHALines {
			incidents, out.Truncated = incidents[:maxOSHALines], true
		}
		var summary []domain.OSHARow
		for _, i := range incidents {
			d, err := s.openDetails(p.TenantID, i.sealed)
			if err != nil {
				return err
			}
			line := OSHALine{IncidentID: i.ID, CaseNo: i.CaseNo, LegalEntityID: i.LegalEntityID, SiteID: i.SiteID, OccurredOn: i.OccurredAt.Format(dateLayoutStore),
				ReportedOn: i.ReportedAt.Format(dateLayoutStore), Location: i.Location, IncidentType: i.IncidentType, BodyParts: i.BodyParts, Outcome: i.Outcome,
				Death: i.Death, DaysAway: i.DaysAway, DaysRestricted: i.DaysRestricted, PrivacyCase: i.PrivacyCase, Description: d.Description,
				Witnesses: nonNil(d.Witnesses), Treatments: nonNil(d.Treatments), WorkerID: i.WorkerID, EmployeeName: deref(i.WorkerName)}
			if i.PrivacyCase {
				line.WorkerID, line.EmployeeName = nil, privacyCaseName
			}
			out.Log = append(out.Log, line)
			summary = append(summary, domain.OSHARow{Outcome: i.Outcome, IncidentType: i.IncidentType, DaysAway: i.DaysAway, DaysRestricted: i.DaysRestricted})
		}
		out.Summary = domain.Summarize(summary)
		if len(incidents) == 0 {
			return nil
		}
		return auditEntity(ctx, tx, p, "export", "incident", ids.Nil, []string{"details", "worker_name"}, "OSHA extract year "+strconv.Itoa(year))
	})
	return out, err
}

const dateLayoutStore = "2006-01-02"

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
