package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
	Version            int               `json:"version"`
	postKey            string
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
		out = in
		if out.Lines == nil {
			out.Lines = []domain.WorkLine{}
		}
		out.ID = ids.New()
		out.Number = number
		out.Status = string(domain.WOUnscheduled)
		out.Version = 1
		_, err = tx.Exec(ctx, `insert into service_work_order (tenant_id, id, number, case_id, agreement_id, customer_party_id, legal_entity_id,
			territory_id, incident_type_id, est_duration_minutes, currency, status, lines, version, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1, $14, $15)`,
			tid, out.ID, number, in.CaseID, in.AgreementID, in.CustomerPartyID, in.LegalEntityID, in.TerritoryID, in.IncidentTypeID,
			in.EstDurationMinutes, in.Currency, out.Status, lines, nullable(key), requestHash)
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
func (s *Postgres) TransitionWorkOrder(ctx context.Context, id string, version int, to domain.WOStatus) (WorkOrder, error) {
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
		if _, err := tx.Exec(ctx, `update service_work_order set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			tid, id, string(to)); err != nil {
			return err
		}
		wo, err = getWorkOrder(ctx, tx, tid, id)
		return err
	})
	return wo, err
}

// PostWorkOrder bills a completed work order (FR-45.10, BR-45.4). A replay with
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
		cover, err := agreementCover(ctx, tx, tid, current)
		if err != nil {
			return err
		}
		posting, err := domain.Post(domain.PostInput{Status: domain.WOStatus(current.Status), Currency: current.Currency,
			Lines: current.Lines, Agreement: cover})
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
// agreement that is no longer ACTIVE covers nothing (BR-45.1).
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
	territory_id::text, incident_type_id::text, est_duration_minutes, currency, status, lines, posting, version, coalesce(post_key, '')`

func scanWorkOrder(row pgx.Row) (WorkOrder, error) {
	var wo WorkOrder
	var lines, posting []byte
	if err := row.Scan(&wo.ID, &wo.Number, &wo.CaseID, &wo.AgreementID, &wo.CustomerPartyID, &wo.LegalEntityID, &wo.TerritoryID,
		&wo.IncidentTypeID, &wo.EstDurationMinutes, &wo.Currency, &wo.Status, &lines, &posting, &wo.Version, &wo.postKey); err != nil {
		return wo, err
	}
	if err := json.Unmarshal(lines, &wo.Lines); err != nil {
		return wo, err
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
