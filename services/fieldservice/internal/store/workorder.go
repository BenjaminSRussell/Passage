package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// WorkOrder is a service work order (FR-45.6). Lines carry their coverage only
// after posting; the posting is stored with the order.
type WorkOrder struct {
	ID                 string            `json:"id"`
	Number             string            `json:"number"`
	CaseID             *string           `json:"caseId,omitempty"`
	AgreementID        *string           `json:"agreementId,omitempty"`
	CustomerPartyID    string            `json:"customerPartyId"`
	LegalEntityID      string            `json:"legalEntityId"`
	TerritoryID        string            `json:"territoryId"`
	IncidentTypeID     *string           `json:"incidentTypeId,omitempty"`
	EstDurationMinutes int               `json:"estDurationMinutes"`
	Currency           string            `json:"currency"`
	Status             string            `json:"status"`
	Lines              []domain.WorkLine `json:"lines"`
	Posting            *domain.Posting   `json:"posting,omitempty"`
	// FollowUpOf links a follow-up order to the finished order it follows up (BR-45.5).
	FollowUpOf *string `json:"followUpOf,omitempty"`
	// CompletedAt is when the order reached COMPLETED.
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	// FirstTimeFix is set at completion and cleared when a follow-up is opened
	// (PRD 8.45.4, BR-45.5); it is absent before completion.
	FirstTimeFix *bool `json:"firstTimeFix,omitempty"`
	Version      int   `json:"version"`
	// Scheduling data (PRD 8.45.4): the skills a visit needs, the customer's promised
	// window, the site address and position, and the one active assignment.
	RequiredSkills   []string   `json:"requiredSkills"`
	WindowStart      *time.Time `json:"windowStart,omitempty"`
	WindowEnd        *time.Time `json:"windowEnd,omitempty"`
	Address          string     `json:"address"`
	Latitude         *float64   `json:"latitude,omitempty"`
	Longitude        *float64   `json:"longitude,omitempty"`
	TechnicianID     *string    `json:"technicianId,omitempty"`
	AssignedStart    *time.Time `json:"assignedStart,omitempty"`
	AssignedEnd      *time.Time `json:"assignedEnd,omitempty"`
	AssignmentPinned bool       `json:"assignmentPinned"`
	// Completion data (BR-45.6, SVC-WO-001): the customer signature captured on the
	// device (SignatureRef points into the object store), whether the incident type
	// exempts the order from it, and the technician's note.
	SignatureExempt bool       `json:"signatureExempt"`
	SignedBy        *string    `json:"signedBy,omitempty"`
	SignedAt        *time.Time `json:"signedAt,omitempty"`
	SignatureRef    *string    `json:"signatureRef,omitempty"`
	TechnicianNote  string     `json:"technicianNote"`
	// SignedLatitude and SignedLongitude are where the customer signed, when the device knew.
	SignedLatitude  *float64 `json:"signedLatitude,omitempty"`
	SignedLongitude *float64 `json:"signedLongitude,omitempty"`
	// Checklist is what the technician ticks off on the visit; the ticks are in the action log.
	Checklist []domain.ChecklistItem `json:"checklist"`
	noteAt    *time.Time
	postKey   string
}

// CreateWorkOrder stores a work order in UNSCHEDULED status.
func (s *Postgres) CreateWorkOrder(ctx context.Context, in WorkOrder, key, requestHash string) (WorkOrder, bool, error) {
	tid := tenantOf(ctx)
	var out WorkOrder
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_work_order", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getWorkOrder(ctx, tx, tid, id)
			return err
		}
		if err := linkWorkOrder(ctx, tx, tid, &in); err != nil {
			return err
		}
		number, err := nextNumber(ctx, tx, tid, "workorder", "SWO")
		if err != nil {
			return err
		}
		lines, err := json.Marshal(in.Lines)
		if err != nil {
			return err
		}
		if in.Checklist == nil {
			in.Checklist = []domain.ChecklistItem{}
		}
		checklist, err := json.Marshal(in.Checklist)
		if err != nil {
			return err
		}
		out = in
		if out.Lines == nil {
			out.Lines = []domain.WorkLine{}
		}
		out.ID = ids.New()
		out.Number = number
		out.Status = string(domain.WOUnscheduled)
		out.Version = 1
		if in.RequiredSkills == nil {
			in.RequiredSkills = []string{}
		}
		out.RequiredSkills = in.RequiredSkills
		_, err = tx.Exec(ctx, `insert into service_work_order (tenant_id, id, number, case_id, agreement_id, customer_party_id, legal_entity_id,
			territory_id, incident_type_id, est_duration_minutes, currency, status, lines, version, idempotency_key, request_hash,
			required_skills, window_start, window_end, address, geo_lat, geo_lon, signature_exempt, checklist)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)`,
			tid, out.ID, number, in.CaseID, in.AgreementID, in.CustomerPartyID, in.LegalEntityID, in.TerritoryID, in.IncidentTypeID,
			in.EstDurationMinutes, in.Currency, out.Status, lines, nullable(key), requestHash,
			in.RequiredSkills, in.WindowStart, in.WindowEnd, in.Address, in.Latitude, in.Longitude, in.SignatureExempt, checklist)
		return err
	})
	if err != nil {
		return WorkOrder{}, false, err
	}
	return out, replayed, err
}

// linkWorkOrder checks the case and agreement a new work order names. A work
// order created from a case inherits the agreement the case resolved to
// (FR-45.6, BR-45.1), and both must belong to the work order's customer and
// legal entity, so a work order never bills against another customer's terms.
func linkWorkOrder(ctx context.Context, tx pgx.Tx, tenantID string, in *WorkOrder) error {
	if in.CaseID != nil {
		var customer, legalEntity string
		var agreement *string
		err := tx.QueryRow(ctx, `select customer_party_id::text, legal_entity_id::text, agreement_id::text from service_case where tenant_id = $1 and id = $2`,
			tenantID, *in.CaseID).Scan(&customer, &legalEntity, &agreement)
		if errors.Is(err, pgx.ErrNoRows) {
			return unprocessable(CodeCaseUnknown, "service case not found")
		}
		if err != nil {
			return err
		}
		if customer != in.CustomerPartyID || legalEntity != in.LegalEntityID {
			return unprocessable(CodeCaseCustomer, "the case belongs to another customer or legal entity")
		}
		if in.AgreementID == nil {
			in.AgreementID = agreement
		}
	}
	if in.AgreementID != nil {
		a, err := getAgreement(ctx, tx, tenantID, *in.AgreementID, false)
		if err != nil {
			return err
		}
		if a.CustomerPartyID != in.CustomerPartyID || a.LegalEntityID != in.LegalEntityID {
			return unprocessable(CodeAgreementCustomer, "the agreement belongs to another customer or legal entity")
		}
	}
	return nil
}

// GetWorkOrder loads one work order with its posting, if any.
func (s *Postgres) GetWorkOrder(ctx context.Context, id string) (WorkOrder, error) {
	tid := tenantOf(ctx)
	var wo WorkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		wo, err = getWorkOrder(ctx, tx, tid, id)
		return err
	})
	return wo, err
}

// TransitionWorkOrder moves a work order along the status machine (BR-45.5).
// Requesting the status it already has is a no-op that does not bump the version.
// COMPLETED stamps completed_at and sets first_time_fix; DISPATCHED stops the
// response clock of the order's case if nobody has responded yet (BR-45.2).
func (s *Postgres) TransitionWorkOrder(ctx context.Context, id string, version int, to domain.WOStatus) (WorkOrder, error) {
	if to == domain.WOScheduled || to == domain.WODispatched {
		return WorkOrder{}, unprocessable(CodeWOInvalid, fmt.Sprintf("a work order is never moved to %s by hand: SCHEDULED comes from :assign and DISPATCHED from publishing the territory-day", to))
	}
	return s.move(ctx, id, version, to, true)
}

// move is the status machine without the guard on SCHEDULED and DISPATCHED; fixtures in
// tests reach those states through it. A completion needs the customer's signature
// (SVC-WO-001) when requireSignature is set.
func (s *Postgres) move(ctx context.Context, id string, version int, to domain.WOStatus, requireSignature bool) (WorkOrder, error) {
	tid := tenantOf(ctx)
	var wo WorkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return versionConflict(current.Version)
		}
		if current.Status == string(to) {
			wo = current
			return nil
		}
		if !domain.CanTransition(domain.WOStatus(current.Status), to) {
			return unprocessable(CodeWOInvalid, fmt.Sprintf("a work order cannot move from %s to %s", current.Status, to))
		}
		if requireSignature && to == domain.WOCompleted && !current.SignatureExempt && current.SignedAt == nil {
			return unprocessable(domain.CodeSignatureRequired, "the customer's signature is required to complete this work order")
		}
		now := time.Now().UTC()
		if _, err := tx.Exec(ctx, `update service_work_order set status = $3, version = version + 1, updated_at = $4,
			completed_at = case when $3 = 'COMPLETED' then $4 else completed_at end,
			first_time_fix = case when $3 = 'COMPLETED' then coalesce(first_time_fix, true) else first_time_fix end
			where tenant_id = $1 and id = $2`, tid, id, string(to), now); err != nil {
			return err
		}
		if to == domain.WODispatched && current.CaseID != nil {
			if err := recordResponse(ctx, tx, tid, *current.CaseID, now); err != nil {
				return err
			}
		}
		wo, err = getWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if to == domain.WOCompleted {
			return enqueueCompleted(ctx, tx, tid, wo, now)
		}
		return nil
	})
	return wo, err
}

// PostWorkOrder bills a completed work order (FR-45.10, BR-45.4): each line is
// WARRANTY when the case's service object warranty covers it on the completion
// day, else AGREEMENT when the agreement covers it, else BILLABLE. A replay with
// the same Idempotency-Key returns the stored posting.
func (s *Postgres) PostWorkOrder(ctx context.Context, id string, version int, key string) (WorkOrder, bool, error) {
	tid := tenantOf(ctx)
	var wo WorkOrder
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if key != "" && current.postKey == key {
			wo, replayed = current, true
			return nil
		}
		if current.Version != version {
			return versionConflict(current.Version)
		}
		if current.Status == string(domain.WOCompleted) {
			consumed, err := consumedQty(ctx, tx, tid, id)
			if err != nil {
				return err
			}
			if open := domain.PartsOutstanding(current.Lines, consumed); len(open) > 0 {
				return unprocessable(domain.CodePartsNotConsumed, fmt.Sprintf("parts lines not consumed; cannot post (%d outstanding): consume or remove them", len(open)))
			}
		}
		cover, err := agreementCover(ctx, tx, tid, current)
		if err != nil {
			return err
		}
		warranty, err := warrantyCover(ctx, tx, tid, current)
		if err != nil {
			return err
		}
		posting, err := domain.Post(domain.PostInput{Status: domain.WOStatus(current.Status), Currency: current.Currency,
			Lines: current.Lines, Warranty: warranty, Agreement: cover})
		if err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				return unprocessable(de.Code, de.Detail)
			}
			return err
		}
		postJSON, err := json.Marshal(posting)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update service_work_order set status = 'POSTED', posting = $3, post_key = $4,
			billable_total = $5::numeric, warranty_cost = $6::numeric, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`,
			tid, id, postJSON, nullable(key), posting.BillableTotal.String(), posting.WarrantyCost.String()); err != nil {
			if isUnique(err) {
				return unprocessable(CodeIdempotencyReuse, "Idempotency-Key was already used to post another work order")
			}
			return err
		}
		if current.AgreementID != nil && posting.EntitlementConsumed.IsPositive() {
			if _, err := tx.Exec(ctx, `insert into service_entitlement_usage (tenant_id, id, agreement_id, work_order_id, hours)
				values ($1, $2, $3, $4, $5::numeric)`, tid, ids.New(), *current.AgreementID, id, posting.EntitlementConsumed.String()); err != nil {
				return err
			}
		}
		wo, err = getWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		lines := make([]svcevents.PostedLine, 0, len(posting.Lines))
		for _, l := range posting.Lines {
			lines = append(lines, svcevents.PostedLine{LineID: l.LineID, Kind: string(l.Kind), Coverage: string(l.Coverage), Qty: l.Qty.String(),
				Amount: l.Amount.String(), WarrantyCost: l.WarrantyCost.String(), AgreementHours: l.AgreementHours.String()})
		}
		return enqueue(ctx, tx, "workorder", id, svcevents.WorkOrderPosted, tid, svcevents.WorkOrderPostedData{
			WorkOrderID: id, Number: wo.Number, CustomerPartyID: wo.CustomerPartyID, LegalEntityID: wo.LegalEntityID,
			CaseID: wo.CaseID, AgreementID: wo.AgreementID, Currency: posting.Currency, BillableTotal: posting.BillableTotal.String(),
			WarrantyCost: posting.WarrantyCost.String(), EntitlementConsumed: posting.EntitlementConsumed.String(), Lines: lines,
		})
	})
	return wo, replayed, err
}

// agreementCover derives the coverage and remaining included hours of the
// agreement on a work order: the best-matching line less earlier usage. The
// agreement row is locked so two posts against it consume hours in turn. An
// agreement that is no longer ACTIVE, or whose term ended before the order was
// completed, covers nothing (BR-45.1, FR-45.3).
func agreementCover(ctx context.Context, tx pgx.Tx, tenantID string, wo WorkOrder) (*domain.AgreementCover, error) {
	if wo.AgreementID == nil {
		return nil, nil
	}
	a, err := getAgreement(ctx, tx, tenantID, *wo.AgreementID, true)
	if err != nil {
		return nil, err
	}
	if a.Status != domain.AgreementActive {
		return nil, nil
	}
	// Nor does an agreement whose term does not hold the completion day, even
	// before the expiry pass has marked it EXPIRED (FR-45.3).
	if wo.CompletedAt != nil && !domain.AgreementCovers(a.ValidFrom, a.ValidTo, wo.CompletedAt.UTC().Format(dateLayout)) {
		return nil, nil
	}
	objectID := ""
	if wo.CaseID != nil {
		var obj *string
		if err := tx.QueryRow(ctx, `select service_object_id::text from service_case where tenant_id = $1 and id = $2`, tenantID, *wo.CaseID).Scan(&obj); err != nil {
			return nil, err
		}
		if obj != nil {
			objectID = *obj
		}
	}
	line, _, ok := domain.BestLine(a.Lines, objectID, "")
	if !ok {
		return nil, nil
	}
	return &domain.AgreementCover{Parts: line.PartsCovered, Labor: line.LaborCovered, Travel: line.TravelCovered,
		HoursRemaining: line.IncludedHours.Sub(a.HoursConsumed)}, nil
}

func versionConflict(current int) error {
	return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("current version is %d", current))
}

const workOrderColumns = `id::text, number, case_id::text, agreement_id::text, customer_party_id::text, legal_entity_id::text,
	territory_id::text, incident_type_id::text, est_duration_minutes, currency, status, lines, posting, version, coalesce(post_key, ''),
	follow_up_of::text, completed_at, first_time_fix, required_skills, window_start, window_end, address, geo_lat::float8, geo_lon::float8,
	technician_id::text, assigned_start, assigned_end, assignment_pinned, signature_exempt, signed_by, signed_at, signature_ref, technician_note, note_at, signed_lat::float8, signed_lon::float8, checklist`

func scanWorkOrder(row pgx.Row) (WorkOrder, error) {
	var wo WorkOrder
	var lines, posting, checklist []byte
	if err := row.Scan(&wo.ID, &wo.Number, &wo.CaseID, &wo.AgreementID, &wo.CustomerPartyID, &wo.LegalEntityID, &wo.TerritoryID,
		&wo.IncidentTypeID, &wo.EstDurationMinutes, &wo.Currency, &wo.Status, &lines, &posting, &wo.Version, &wo.postKey,
		&wo.FollowUpOf, &wo.CompletedAt, &wo.FirstTimeFix, &wo.RequiredSkills, &wo.WindowStart, &wo.WindowEnd, &wo.Address, &wo.Latitude,
		&wo.Longitude, &wo.TechnicianID, &wo.AssignedStart, &wo.AssignedEnd, &wo.AssignmentPinned, &wo.SignatureExempt, &wo.SignedBy, &wo.SignedAt,
		&wo.SignatureRef, &wo.TechnicianNote, &wo.noteAt, &wo.SignedLatitude, &wo.SignedLongitude, &checklist); err != nil {
		return wo, err
	}
	if wo.RequiredSkills == nil {
		wo.RequiredSkills = []string{}
	}
	if err := json.Unmarshal(lines, &wo.Lines); err != nil {
		return wo, err
	}
	if err := json.Unmarshal(checklist, &wo.Checklist); err != nil {
		return wo, err
	}
	if wo.Checklist == nil {
		wo.Checklist = []domain.ChecklistItem{}
	}
	if posting != nil {
		wo.Posting = &domain.Posting{}
		return wo, json.Unmarshal(posting, wo.Posting)
	}
	return wo, nil
}

func getWorkOrder(ctx context.Context, tx pgx.Tx, tenantID, id string) (WorkOrder, error) {
	wo, err := scanWorkOrder(tx.QueryRow(ctx, `select `+workOrderColumns+` from service_work_order where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return wo, httpx.NewProblem(http.StatusNotFound, CodeWOUnknown, "service work order not found")
	}
	return wo, err
}

func lockWorkOrder(ctx context.Context, tx pgx.Tx, tenantID, id string) (WorkOrder, error) {
	wo, err := scanWorkOrder(tx.QueryRow(ctx, `select `+workOrderColumns+` from service_work_order where tenant_id = $1 and id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return wo, httpx.NewProblem(http.StatusNotFound, CodeWOUnknown, "service work order not found")
	}
	return wo, err
}
