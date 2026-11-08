package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes of the requisition approval flow (BR-17.1).
const (
	// CodeRequisitionSelfApproval is the 403 for a requester who tries to decide on their own requisition.
	CodeRequisitionSelfApproval = "PRC-REQ-009"
	// CodeRequisitionRepeatApprover is the 403 for an approver who already approved an earlier level in this round.
	CodeRequisitionRepeatApprover = "PRC-REQ-010"
)

// History actions.
const (
	HistorySubmitted = "SUBMITTED"
	HistoryApproved  = "APPROVED"
	HistoryRejected  = "REJECTED"
	HistoryReopened  = "REOPENED"
	HistoryCancelled = "CANCELLED"
)

// RequisitionHistory is one row of the requisition's audit trail.
type RequisitionHistory struct {
	ID                 string    `json:"id"`
	Action             string    `json:"action"`
	StepNo             int       `json:"stepNo,omitempty"`
	Role               string    `json:"role,omitempty"`
	ActorID            string    `json:"actorId,omitempty"`
	Comment            string    `json:"comment,omitempty"`
	FromStatus         string    `json:"fromStatus"`
	ToStatus           string    `json:"toStatus"`
	RequisitionVersion int       `json:"requisitionVersion"`
	CreatedAt          time.Time `json:"createdAt"`
}

// RequisitionAct is a decision on a requisition: approve or reject (an approver), reopen or cancel (the requester).
type RequisitionAct struct {
	Action  string
	ActorID string
	// Comment is the approver's note, the reject reason or the cancel reason.
	Comment string
}

type historyRow struct {
	Action     string
	StepNo     int
	Role       string
	ActorID    string
	Comment    string
	FromStatus string
	Version    int
}

// addRequisitionHistory appends a row to the audit trail; cur is the requisition after the change.
func addRequisitionHistory(ctx context.Context, tx pgx.Tx, cur Requisition, h historyRow) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `insert into purchase_requisition_history (tenant_id, id, requisition_id, legal_entity_id, action, step_no, role,
		actor_id, comment, from_status, to_status, requisition_version) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		p.TenantID, ids.New(), cur.ID, cur.LegalEntityID, h.Action, nullableInt(h.StepNo), nullable(h.Role), nullable(h.ActorID), h.Comment,
		h.FromStatus, cur.Status, h.Version)
	return err
}

func loadRequisitionHistory(ctx context.Context, tx pgx.Tx, requisitionID string) ([]RequisitionHistory, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select id, action, coalesce(step_no, 0), coalesce(role, ''), coalesce(actor_id::text, ''), comment, from_status,
		to_status, requisition_version, created_at from purchase_requisition_history
		where tenant_id = $1 and requisition_id = $2 order by created_at, id`, p.TenantID, requisitionID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RequisitionHistory, error) {
		var h RequisitionHistory
		err := row.Scan(&h.ID, &h.Action, &h.StepNo, &h.Role, &h.ActorID, &h.Comment, &h.FromStatus, &h.ToStatus, &h.RequisitionVersion, &h.CreatedAt)
		return h, err
	})
	if out == nil {
		out = []RequisitionHistory{}
	}
	return out, err
}

// ActOnRequisition applies approve, reject, reopen or cancel (BR-17.1) under the requisition's row lock and the
// caller's version. Approving moves the requisition one level up its chain, to APPROVED after the last one
// (requisition.approved.v1); an approver cannot be the requester (PRC-REQ-009) or have approved an earlier level
// of the same round (PRC-REQ-010); rejecting writes requisition.rejected.v1; reopening returns a REJECTED
// requisition to DRAFT with its chain cleared; cancelling a DRAFT, REJECTED or APPROVED requisition cancels its
// OPEN lines and writes requisition.cancelled.v1, and an APPROVED one only while every line is still OPEN and no
// running RFQ holds one. Every action adds an audit row. A replayed Idempotency-Key returns the requisition as it is.
func (s *Postgres) ActOnRequisition(ctx context.Context, id string, version int, act RequisitionAct, idem Idem) (Requisition, error) {
	p, _ := tenant.FromContext(ctx)
	var out Requisition
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select 1 from purchase_requisition where tenant_id = $1 and id = $2 for update`, p.TenantID, id); err != nil {
			return err
		}
		cur, err := loadRequisition(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, replay, err := claimKey(ctx, tx, idem, id); err != nil || replay {
			out = cur
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("requisition is at version %d, not %d; reload and retry", cur.Version, version))
		}
		from := cur.Status
		next, steps, err := domain.RequisitionTransition(cur.Status, act.Action, cur.ApprovedSteps, len(cur.ApproverChain))
		if err != nil {
			return httpx.NewProblem(http.StatusConflict, CodeRequisitionState, err.Error())
		}
		h := historyRow{FromStatus: from, ActorID: act.ActorID, Comment: act.Comment, Version: cur.Version + 1}
		var cancelled []string
		switch act.Action {
		case domain.ReqActionApprove, domain.ReqActionReject:
			if act.ActorID == cur.RequesterID {
				return httpx.NewProblem(http.StatusForbidden, CodeRequisitionSelfApproval, "the requester cannot decide on their own requisition")
			}
			h.StepNo, h.Role = cur.ApprovedSteps+1, domain.NextApprover(cur.ApproverChain, cur.ApprovedSteps)
			h.Action = HistoryApproved
			if act.Action == domain.ReqActionReject {
				h.Action = HistoryRejected
			} else if again, err := approvedThisRound(ctx, tx, id, act.ActorID); err != nil {
				return err
			} else if again {
				return httpx.NewProblem(http.StatusForbidden, CodeRequisitionRepeatApprover,
					"you already approved an earlier level of this requisition; the next level needs another approver")
			}
		case domain.ReqActionReopen:
			h.Action = HistoryReopened
		case domain.ReqActionCancel:
			h.Action = HistoryCancelled
			if cancelled, err = cancellableLines(ctx, tx, cur); err != nil {
				return err
			}
		}
		chain := cur.ApproverChain
		if act.Action == domain.ReqActionReopen {
			chain = []string{}
		}
		tag, err := tx.Exec(ctx, `update purchase_requisition set status = $3, approved_steps = $4, approver_chain = $5, version = version + 1,
			updated_at = now() where tenant_id = $1 and id = $2 and version = $6`, p.TenantID, id, next, steps, chain, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "requisition changed since it was read; reload and retry")
		}
		if len(cancelled) > 0 {
			if _, err := tx.Exec(ctx, `update purchase_requisition_line set status = 'CANCELLED' where tenant_id = $1 and requisition_id = $2
				and id = any($3::uuid[]) and status = 'OPEN'`, p.TenantID, id, cancelled); err != nil {
				return err
			}
		}
		cur.Status, cur.ApprovedSteps, cur.ApproverChain, cur.Version = next, steps, chain, version+1
		if err := addRequisitionHistory(ctx, tx, cur, h); err != nil {
			return err
		}
		if err := enqueueRequisitionDecision(ctx, tx, cur, act, h, cancelled); err != nil {
			return err
		}
		out, err = loadRequisition(ctx, tx, id)
		return err
	})
	return out, err
}

// approvedThisRound reports whether the user approved a level since the requisition was last submitted. The round
// is told by the requisition version each history row records, which only moves forward under the row lock, rather
// than by timestamps, which two transactions can take out of order.
func approvedThisRound(ctx context.Context, tx pgx.Tx, requisitionID, userID string) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	var again bool
	err := tx.QueryRow(ctx, `select exists (select 1 from purchase_requisition_history h where h.tenant_id = $1 and h.requisition_id = $2
		and h.action = 'APPROVED' and h.actor_id::text = $3 and h.requisition_version > coalesce((select max(s.requisition_version)
		from purchase_requisition_history s where s.tenant_id = $1 and s.requisition_id = $2 and s.action = 'SUBMITTED'), 0))`,
		p.TenantID, requisitionID, userID).Scan(&again)
	return again, err
}

// cancellableLines returns the OPEN lines a cancel will cancel. An APPROVED requisition can be cancelled only while
// every line is OPEN (a line already ordered stays with its PO) and none is held by a running RFQ.
func cancellableLines(ctx context.Context, tx pgx.Tx, cur Requisition) ([]string, error) {
	out := []string{}
	for _, l := range cur.Lines {
		if l.Status == "OPEN" {
			out = append(out, l.ID)
		}
	}
	if cur.Status != RequisitionApproved {
		return out, nil
	}
	if len(out) != len(cur.Lines) {
		return nil, httpx.NewProblem(http.StatusConflict, CodeRequisitionState, fmt.Sprintf(
			"requisition %s has lines that are already ordered or cancelled; cancel it only while every line is OPEN", cur.Number))
	}
	for _, l := range cur.Lines {
		if number, held, err := liveRFQOf(ctx, tx, l.ID); err != nil {
			return nil, err
		} else if held {
			return nil, httpx.NewProblem(http.StatusConflict, CodeRequisitionOnRFQ, fmt.Sprintf(
				"requisition %s line %d is being sourced on %s; award or cancel the RFQ first", cur.Number, l.LineNo, number))
		}
	}
	return out, nil
}

func enqueueRequisitionDecision(ctx context.Context, tx pgx.Tx, cur Requisition, act RequisitionAct, h historyRow, cancelled []string) error {
	switch act.Action {
	case domain.ReqActionApprove:
		if cur.Status == RequisitionApproved {
			return enqueue(ctx, tx, "requisition", cur.ID, purchasingevents.RequisitionApproved, requisitionData(cur))
		}
	case domain.ReqActionReject:
		return enqueue(ctx, tx, "requisition", cur.ID, purchasingevents.RequisitionRejected, purchasingevents.RequisitionRejectedData{
			RequisitionID: cur.ID, Number: cur.Number, LegalEntityID: cur.LegalEntityID, RequesterID: cur.RequesterID, Currency: cur.Currency,
			Total: cur.Total.String(), StepNo: h.StepNo, Role: h.Role, RejectedBy: act.ActorID, Reason: act.Comment, Version: cur.Version})
	case domain.ReqActionCancel:
		return enqueue(ctx, tx, "requisition", cur.ID, purchasingevents.RequisitionCancelled, purchasingevents.RequisitionCancelledData{
			RequisitionID: cur.ID, Number: cur.Number, LegalEntityID: cur.LegalEntityID, RequesterID: cur.RequesterID, Currency: cur.Currency,
			Total: cur.Total.String(), PreviousStatus: h.FromStatus, CancelledLineIDs: cancelled, CancelledBy: act.ActorID, Reason: act.Comment,
			Version: cur.Version})
	}
	return nil
}

// ApprovalQueueFilter narrows ListRequisitionApprovals; empty fields do not filter.
type ApprovalQueueFilter struct {
	LegalEntityID string
	// Role keeps requisitions whose next level is this role.
	Role string
	// ExcludeRequesterID hides the caller's own requisitions, which they cannot decide on.
	ExcludeRequesterID string
}

// RequisitionApprovalItem is a requisition waiting for an approval level.
type RequisitionApprovalItem struct {
	ID            string          `json:"id"`
	Number        string          `json:"number"`
	LegalEntityID string          `json:"legalEntityId"`
	RequesterID   string          `json:"requesterId"`
	Currency      string          `json:"currency"`
	Total         decimal.Decimal `json:"total"`
	LineCount     int             `json:"lineCount"`
	ApprovedSteps int             `json:"approvedSteps"`
	ChainLength   int             `json:"chainLength"`
	NextApprover  string          `json:"nextApprover"`
	Version       int             `json:"version"`
	SubmittedAt   time.Time       `json:"submittedAt"`
}

// ListRequisitionApprovals returns the IN_REVIEW requisitions with the role that decides next, newest first, after
// the cursor id (the approver's inbox).
func (s *Postgres) ListRequisitionApprovals(ctx context.Context, f ApprovalQueueFilter, after string, limit int) (Page[RequisitionApprovalItem], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []RequisitionApprovalItem
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id, r.number, r.legal_entity_id, r.requester_id, r.currency, r.total,
			(select count(*) from purchase_requisition_line l where l.tenant_id = r.tenant_id and l.requisition_id = r.id),
			r.approved_steps, cardinality(r.approver_chain), coalesce(r.approver_chain[r.approved_steps + 1], ''), r.version, r.updated_at
			from purchase_requisition r
			where r.tenant_id = $1 and r.status = 'IN_REVIEW'
			and ($2 = '' or r.legal_entity_id::text = $2) and ($3 = '' or r.approver_chain[r.approved_steps + 1] = $3)
			and ($4 = '' or r.requester_id::text <> $4) and ($5::uuid is null or r.id < $5::uuid)
			order by r.id desc limit $6`, p.TenantID, f.LegalEntityID, f.Role, f.ExcludeRequesterID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RequisitionApprovalItem, error) {
			var it RequisitionApprovalItem
			err := row.Scan(&it.ID, &it.Number, &it.LegalEntityID, &it.RequesterID, &it.Currency, &it.Total, &it.LineCount, &it.ApprovedSteps,
				&it.ChainLength, &it.NextApprover, &it.Version, &it.SubmittedAt)
			return it, err
		})
		return err
	})
	if err != nil {
		return Page[RequisitionApprovalItem]{}, err
	}
	if items == nil {
		items = []RequisitionApprovalItem{}
	}
	return pageOf(items, limit, func(r RequisitionApprovalItem) string { return r.ID }), nil
}
