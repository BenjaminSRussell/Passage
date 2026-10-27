package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	assetevents "github.com/BenjaminSRussell/passage/services/assets/internal/events"
)

// CodeCapexOwnDecision refuses an approver decision by the user who created or
// requested the capex request (segregation of duties, PRD 7.37.17).
const CodeCapexOwnDecision = "AST-0028"

// CapexDecisionInput is one decision on a request at the version the caller
// read (If-Match). The deciding user is the principal; Comment is optional.
type CapexDecisionInput struct {
	Decision domain.CapexDecision
	Version  int
	Comment  *string
}

// CapexDecisionRecord is one row of a request's approver trail. Amount and
// ApprovalLevel are the request's requested amount and level at the decision.
type CapexDecisionRecord struct {
	ID            string           `json:"id"`
	Decision      string           `json:"decision"`
	FromStatus    string           `json:"fromStatus"`
	ToStatus      string           `json:"toStatus"`
	ApprovalLevel *string          `json:"approvalLevel"`
	Amount        *decimal.Decimal `json:"amount"`
	Comment       *string          `json:"comment"`
	DecidedBy     string           `json:"decidedBy"`
	DecidedAt     time.Time        `json:"decidedAt"`
}

// DecideCapexRequest applies one approver or owner decision (FR-37.17,
// BR-37.12) in one transaction: the request row is locked, the version and the
// move are checked, the decision is recorded and, on approve, the selected
// variant's total becomes the approved amount and capexrequest.approved.v1 is
// enqueued with its yearly amounts. Reopen (Postponed → Draft) clears the
// selected variant and routing so the request is submitted afresh. Which
// approver acts at which level stays with the wave-4 workflow (handoff).
func (s *Postgres) DecideCapexRequest(ctx context.Context, id string, in CapexDecisionInput) (CapexRequest, error) {
	p := principal(ctx)
	if p.UserID == "" {
		return CapexRequest{}, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "a capex decision needs an authenticated user")
	}
	var out CapexRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			status, requester, legalEntity string
			createdBy, level, program      *string
			requested                      *decimal.Decimal
			version                        int
		)
		err := tx.QueryRow(ctx, `select status, version, legal_entity_id, requester_id, created_by, approval_level,
			requested_amount, program_position_id from capex_request where tenant_id = $1 and id = $2 for update`, p.TenantID, id).
			Scan(&status, &version, &legalEntity, &requester, &createdBy, &level, &requested, &program)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeCapexNotFound, "no capex request with id "+id)
		}
		if err != nil {
			return err
		}
		if version != in.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
				fmt.Sprintf("the request changed since you read it (version %d, current %d); reload and try again", in.Version, version))
		}
		next, err := in.Decision.Apply(domain.CapexStatus(status))
		if err != nil {
			return err
		}
		if in.Decision.IsApproverDecision() && (requester == p.UserID || deref(createdBy) == p.UserID) {
			return httpx.NewProblem(http.StatusForbidden, CodeCapexOwnDecision,
				"you created or requested this capex request; another approver must decide it")
		}
		switch in.Decision {
		case domain.DecisionApprove:
			// Submit always sets the selected variant and requested amount
			// before a request reaches InApproval.
			_, err = tx.Exec(ctx, `update capex_request set status = $3, approved_amount = requested_amount, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, id, string(next))
		case domain.DecisionReopen:
			_, err = tx.Exec(ctx, `update capex_request set status = $3, selected_variant_id = null, approval_level = null, requested_amount = null,
				version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, string(next))
		default:
			_, err = tx.Exec(ctx, `update capex_request set status = $3, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, id, string(next))
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into capex_request_decision (tenant_id, id, request_id, legal_entity_id, decision, from_status, to_status,
			approval_level, amount, comment, decided_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			p.TenantID, ids.New(), id, legalEntity, string(in.Decision), status, string(next), level, requested, in.Comment, p.UserID); err != nil {
			return err
		}
		out, err = loadCapex(ctx, tx, p.TenantID, id)
		if err != nil || in.Decision != domain.DecisionApprove {
			return err
		}
		return enqueue(ctx, tx, "capex_request", id, assetevents.CapexApproved, p.TenantID, approvedEvent(out, program))
	})
	return out, err
}

// approvedEvent builds the capexrequest.approved payload from the approved
// request and its selected variant.
func approvedEvent(c CapexRequest, programPosition *string) assetevents.CapexApprovedData {
	data := assetevents.CapexApprovedData{RequestID: c.ID, LegalEntityID: c.LegalEntityID, Number: c.Number, Currency: c.Currency,
		VariantID: deref(c.SelectedVariantID), AmountsByYear: map[string]string{}, ProgramPositionID: programPosition}
	if c.ApprovedAmount != nil {
		data.ApprovedAmount = c.ApprovedAmount.String()
	}
	for _, v := range c.Variants {
		if v.ID != data.VariantID {
			continue
		}
		for y, amount := range v.AmountsByYear {
			data.AmountsByYear[y] = amount.String()
		}
		data.InServiceDate = v.InServiceDate.String()
		data.ProfileAssumption = v.ProfileAssumption
	}
	return data
}

func loadCapexDecisions(ctx context.Context, tx pgx.Tx, tenantID, requestID string) ([]CapexDecisionRecord, error) {
	rows, err := tx.Query(ctx, `select id, decision, from_status, to_status, approval_level, amount, comment, decided_by, decided_at
		from capex_request_decision where tenant_id = $1 and request_id = $2 order by decided_at, id`, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CapexDecisionRecord{}
	for rows.Next() {
		var r CapexDecisionRecord
		if err := rows.Scan(&r.ID, &r.Decision, &r.FromStatus, &r.ToStatus, &r.ApprovalLevel, &r.Amount, &r.Comment, &r.DecidedBy, &r.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
