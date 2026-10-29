package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// Case is a service case with its resolved agreement and SLA clocks. Due and
// warning instants are absolute; they move when a pause ends (BR-45.2).
type Case struct {
	ID                  string     `json:"id"`
	Number              string     `json:"number"`
	CustomerPartyID     string     `json:"customerPartyId"`
	LegalEntityID       string     `json:"legalEntityId"`
	ServiceObjectID     *string    `json:"serviceObjectId,omitempty"`
	Channel             string     `json:"channel"`
	Priority            int        `json:"priority"`
	Description         string     `json:"description"`
	AgreementID         *string    `json:"agreementId,omitempty"`
	SLAProfileID        string     `json:"slaProfileId"`
	EntitlementTrace    []string   `json:"entitlementTrace"`
	OpenedAt            time.Time  `json:"openedAt"`
	RespondedAt         *time.Time `json:"respondedAt,omitempty"`
	ResolvedAt          *time.Time `json:"resolvedAt,omitempty"`
	SLAResponseDue      time.Time  `json:"slaResponseDue"`
	SLAResponseWarnAt   time.Time  `json:"slaResponseWarnAt"`
	SLAResolutionDue    time.Time  `json:"slaResolutionDue"`
	SLAResolutionWarnAt time.Time  `json:"slaResolutionWarnAt"`
	SLAPausedMinutes    int        `json:"slaPausedMinutes"`
	SLAPausedAt         *time.Time `json:"slaPausedAt,omitempty"`
	SLAState            string     `json:"slaState"`
	// SLAAlerts are the warnings and breaches already emitted, e.g. RESOLUTION_WARNING.
	SLAAlerts []string `json:"slaAlerts"`
	Status    string   `json:"status"`
	// Version is the optimistic-concurrency version; it is the case's ETag.
	Version int `json:"version"`
}

// clocks returns the SLA state of the case for the domain rules.
func (c Case) clocks() domain.CaseClocks {
	return domain.CaseClocks{Status: domain.CaseStatus(c.Status), State: c.SLAState, OpenedAt: c.OpenedAt, RespondedAt: c.RespondedAt,
		ResponseDue: c.SLAResponseDue, ResponseWarnAt: c.SLAResponseWarnAt, ResolvedAt: c.ResolvedAt, ResolutionDue: c.SLAResolutionDue,
		ResolutionWarnAt: c.SLAResolutionWarnAt, PausedAt: c.SLAPausedAt, PausedMinutes: c.SLAPausedMinutes, Alerts: c.SLAAlerts}
}

// OpenCaseInput is a validated request to open a case.
type OpenCaseInput struct {
	CustomerPartyID      string
	LegalEntityID        string
	ServiceObjectID      *string
	Channel              string
	Priority             int
	Description          string
	OpenedAt             time.Time
	FallbackSLAProfileID *string
}

// OpenCase resolves the entitlement, starts the SLA clocks and stores the case
// with its opened event in one transaction (FR-45.5, BR-45.1, BR-45.2).
func (s *Postgres) OpenCase(ctx context.Context, in OpenCaseInput, key, requestHash string) (Case, bool, error) {
	tid := tenantOf(ctx)
	var out Case
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_case", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getCase(ctx, tx, tid, id)
			return err
		}
		if in.ServiceObjectID != nil {
			if err := checkCaseObject(ctx, tx, tid, *in.ServiceObjectID, in.CustomerPartyID); err != nil {
				return err
			}
		}
		cands, err := customerAgreements(ctx, tx, tid, in.CustomerPartyID, in.LegalEntityID)
		if err != nil {
			return err
		}
		objectID := ""
		if in.ServiceObjectID != nil {
			objectID = *in.ServiceObjectID
		}
		var profileID string
		var agreementID *string
		trace := []string{}
		if ent, ok := domain.Resolve(cands, in.OpenedAt, objectID, ""); ok {
			profileID = ent.SLAProfileID
			agreementID = &ent.AgreementID
			trace = append(trace, ent.Trace...)
		} else {
			if in.FallbackSLAProfileID == nil {
				return unprocessable(CodeNoEntitlement, "no agreement matched and no fallback SLA profile was supplied")
			}
			profileID = *in.FallbackSLAProfileID
			trace = append(trace, "SVC-ENT-001: no agreement matched; fallback SLA profile applied, lines billable")
		}
		profile, err := getProfile(ctx, tx, tid, profileID)
		if err != nil {
			return err
		}
		target, ok := profile.Target(in.Priority)
		if !ok {
			return unprocessable(CodeNoTarget, "the SLA profile has no target for this priority")
		}
		cal, err := profile.Calendar()
		if err != nil {
			return unprocessable(CodeInvalidCalendar, "the SLA profile calendar is not valid")
		}
		number, err := nextNumber(ctx, tx, tid, "case", "CS")
		if err != nil {
			return err
		}
		clocks := domain.StartClocks(in.OpenedAt, cal, target)
		out = Case{
			ID: ids.New(), Number: number, CustomerPartyID: in.CustomerPartyID, LegalEntityID: in.LegalEntityID,
			ServiceObjectID: in.ServiceObjectID, Channel: in.Channel, Priority: in.Priority, Description: in.Description,
			AgreementID: agreementID, SLAProfileID: profileID, EntitlementTrace: trace, OpenedAt: in.OpenedAt,
			SLAResponseDue: clocks.ResponseDue, SLAResponseWarnAt: clocks.ResponseWarnAt,
			SLAResolutionDue: clocks.ResolutionDue, SLAResolutionWarnAt: clocks.ResolutionWarnAt,
			SLAState: clocks.State, SLAAlerts: []string{}, Status: string(clocks.Status), Version: 1,
		}
		traceJSON, err := json.Marshal(trace)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into service_case (tenant_id, id, number, customer_party_id, legal_entity_id, service_object_id, channel, priority,
			description, agreement_id, sla_profile_id, entitlement_trace, opened_at, sla_response_due, sla_resolution_due, sla_paused_minutes,
			sla_state, status, idempotency_key, request_hash, sla_response_warn_at, sla_resolution_warn_at, version)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 0, $16, $17, $18, $19, $20, $21, 1)`,
			tid, out.ID, out.Number, out.CustomerPartyID, out.LegalEntityID, out.ServiceObjectID, out.Channel, out.Priority,
			out.Description, out.AgreementID, out.SLAProfileID, traceJSON, out.OpenedAt, out.SLAResponseDue, out.SLAResolutionDue,
			out.SLAState, out.Status, nullable(key), requestHash, out.SLAResponseWarnAt, out.SLAResolutionWarnAt)
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "case", out.ID, svcevents.CaseOpened, tid, svcevents.CaseOpenedData{
			CaseID: out.ID, Number: out.Number, CustomerPartyID: out.CustomerPartyID, Priority: out.Priority,
			AgreementID: out.AgreementID, ResponseDue: out.SLAResponseDue.UTC().Format(time.RFC3339),
			ResolutionDue: out.SLAResolutionDue.UTC().Format(time.RFC3339),
		})
	})
	return out, replayed, err
}

// GetCase loads one case.
func (s *Postgres) GetCase(ctx context.Context, id string) (Case, error) {
	tid := tenantOf(ctx)
	var c Case
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = getCase(ctx, tx, tid, id)
		return err
	})
	return c, err
}

// caseColumns lists the columns scanCase reads, in scan order. Cases opened
// before warning instants were stored warn at their due instant.
const caseColumns = `id::text, number, customer_party_id::text, legal_entity_id::text, service_object_id::text, channel, priority,
	description, agreement_id::text, sla_profile_id::text, entitlement_trace, opened_at, responded_at, resolved_at,
	sla_response_due, coalesce(sla_response_warn_at, sla_response_due), sla_resolution_due, coalesce(sla_resolution_warn_at, sla_resolution_due),
	sla_paused_minutes, sla_paused_at, sla_state, sla_alerts, status, version`

func scanCase(row pgx.Row) (Case, error) {
	var c Case
	var trace []byte
	if err := row.Scan(&c.ID, &c.Number, &c.CustomerPartyID, &c.LegalEntityID, &c.ServiceObjectID, &c.Channel, &c.Priority, &c.Description,
		&c.AgreementID, &c.SLAProfileID, &trace, &c.OpenedAt, &c.RespondedAt, &c.ResolvedAt,
		&c.SLAResponseDue, &c.SLAResponseWarnAt, &c.SLAResolutionDue, &c.SLAResolutionWarnAt,
		&c.SLAPausedMinutes, &c.SLAPausedAt, &c.SLAState, &c.SLAAlerts, &c.Status, &c.Version); err != nil {
		return c, err
	}
	if c.SLAAlerts == nil {
		c.SLAAlerts = []string{}
	}
	return c, json.Unmarshal(trace, &c.EntitlementTrace)
}

func getCase(ctx context.Context, tx pgx.Tx, tenantID, id string) (Case, error) {
	return queryCase(ctx, tx, tenantID, id, "")
}

// lockCase loads a case FOR UPDATE, so a transition, the monitor and a
// dispatch that records the response each see and write one version.
func lockCase(ctx context.Context, tx pgx.Tx, tenantID, id string) (Case, error) {
	return queryCase(ctx, tx, tenantID, id, " for update")
}

func queryCase(ctx context.Context, tx pgx.Tx, tenantID, id, lock string) (Case, error) {
	c, err := scanCase(tx.QueryRow(ctx, `select `+caseColumns+` from service_case where tenant_id = $1 and id = $2`+lock, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NewProblem(http.StatusNotFound, CodeCaseUnknown, "service case not found")
	}
	return c, err
}
