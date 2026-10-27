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

// Case is a service case with its resolved agreement and SLA clocks.
type Case struct {
	ID               string    `json:"id"`
	Number           string    `json:"number"`
	CustomerPartyID  string    `json:"customerPartyId"`
	LegalEntityID    string    `json:"legalEntityId"`
	ServiceObjectID  *string   `json:"serviceObjectId,omitempty"`
	Channel          string    `json:"channel"`
	Priority         int       `json:"priority"`
	Description      string    `json:"description"`
	AgreementID      *string   `json:"agreementId,omitempty"`
	SLAProfileID     string    `json:"slaProfileId"`
	EntitlementTrace []string  `json:"entitlementTrace"`
	OpenedAt         time.Time `json:"openedAt"`
	SLAResponseDue   time.Time `json:"slaResponseDue"`
	SLAResolutionDue time.Time `json:"slaResolutionDue"`
	SLAPausedMinutes int       `json:"slaPausedMinutes"`
	SLAState         string    `json:"slaState"`
	Status           string    `json:"status"`
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
		out = Case{
			ID: ids.New(), Number: number, CustomerPartyID: in.CustomerPartyID, LegalEntityID: in.LegalEntityID,
			ServiceObjectID: in.ServiceObjectID, Channel: in.Channel, Priority: in.Priority, Description: in.Description,
			AgreementID: agreementID, SLAProfileID: profileID, EntitlementTrace: trace, OpenedAt: in.OpenedAt,
			SLAResponseDue:   domain.Clock{Opened: in.OpenedAt, TargetMinutes: target.ResponseMinutes, Cal: cal}.Due(),
			SLAResolutionDue: domain.Clock{Opened: in.OpenedAt, TargetMinutes: target.ResolutionMinutes, Cal: cal}.Due(),
			SLAState:         domain.SLARunning, Status: "NEW",
		}
		traceJSON, err := json.Marshal(trace)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into service_case (tenant_id, id, number, customer_party_id, legal_entity_id, service_object_id, channel, priority,
			description, agreement_id, sla_profile_id, entitlement_trace, opened_at, sla_response_due, sla_resolution_due, sla_paused_minutes,
			sla_state, status, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 0, $16, $17, $18, $19)`,
			tid, out.ID, out.Number, out.CustomerPartyID, out.LegalEntityID, out.ServiceObjectID, out.Channel, out.Priority,
			out.Description, out.AgreementID, out.SLAProfileID, traceJSON, out.OpenedAt, out.SLAResponseDue, out.SLAResolutionDue,
			out.SLAState, out.Status, nullable(key), requestHash)
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

const caseColumns = `id::text, number, customer_party_id::text, legal_entity_id::text, service_object_id::text, channel, priority,
	description, agreement_id::text, sla_profile_id::text, entitlement_trace, opened_at, sla_response_due, sla_resolution_due,
	sla_paused_minutes, sla_state, status`

func scanCase(row pgx.Row) (Case, error) {
	var c Case
	var trace []byte
	if err := row.Scan(&c.ID, &c.Number, &c.CustomerPartyID, &c.LegalEntityID, &c.ServiceObjectID, &c.Channel, &c.Priority, &c.Description,
		&c.AgreementID, &c.SLAProfileID, &trace, &c.OpenedAt, &c.SLAResponseDue, &c.SLAResolutionDue, &c.SLAPausedMinutes,
		&c.SLAState, &c.Status); err != nil {
		return c, err
	}
	return c, json.Unmarshal(trace, &c.EntitlementTrace)
}

func getCase(ctx context.Context, tx pgx.Tx, tenantID, id string) (Case, error) {
	c, err := scanCase(tx.QueryRow(ctx, `select `+caseColumns+` from service_case where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NewProblem(http.StatusNotFound, CodeCaseUnknown, "service case not found")
	}
	return c, err
}
