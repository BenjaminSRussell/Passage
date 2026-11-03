package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Change approval problem codes (README lists the full catalogue).
const (
	CodeChangePending      = "SLS-0043"
	CodeChangeDecided      = "SLS-0044"
	CodeChangeSelfApproval = "SLS-0045"
	CodeChangeStale        = "SLS-0046"
	CodeChangeNotFound     = "SLS-0112"
	CodeChangeNotRequester = "SLS-0114"

	ChangePending  = "PENDING"
	ChangeApproved = "APPROVED"
	ChangeRejected = "REJECTED"
	// ChangeWithdrawn is a request its requester took back before a decision.
	ChangeWithdrawn = "WITHDRAWN"

	// KindChange is a PATCH waiting for approval; KindCancel a cancellation.
	KindChange = "CHANGE"
	KindCancel = "CANCEL"

	maxChangeRequestsListed = 200
)

// ChangePolicy is the change approval switch of one customer in one legal
// entity (FR-27.9, PRD 5.27.13 change_approval_required). Version is 0 for a
// customer that never set it: no approval is needed.
type ChangePolicy struct {
	LegalEntityID    string     `json:"legalEntityId"`
	CustomerID       string     `json:"customerId"`
	ApprovalRequired bool       `json:"approvalRequired"`
	Version          int        `json:"version"`
	UpdatedBy        string     `json:"updatedBy,omitempty"`
	UpdatedAt        *time.Time `json:"updatedAt,omitempty"`
}

// OrderChangeRequest is a change of a CONFIRMED order that waits for a
// decision. Change is the PATCH body, Diff what it would change, and
// BaseVersion the order version it was made against: it can only be approved
// while the order is still at that version.
type OrderChangeRequest struct {
	ID              string               `json:"id"`
	LegalEntityID   string               `json:"legalEntityId"`
	OrderID         string               `json:"orderId"`
	OrderNumber     string               `json:"orderNumber"`
	CustomerID      string               `json:"customerId"`
	Kind            string               `json:"kind"`
	Status          string               `json:"status"`
	BaseVersion     int                  `json:"baseVersion"`
	Reason          string               `json:"reason"`
	Change          ChangeRequest        `json:"change"`
	Diff            []domain.FieldChange `json:"diff"`
	RequestedBy     string               `json:"requestedBy"`
	RequestedAt     time.Time            `json:"requestedAt"`
	DecidedBy       *string              `json:"decidedBy,omitempty"`
	DecidedAt       *time.Time           `json:"decidedAt,omitempty"`
	DecisionComment string               `json:"decisionComment"`
	AppliedVersion  *int                 `json:"appliedVersion,omitempty"`
	Version         int                  `json:"version"`
}

// ChangeRequestFilter narrows the change request list. Empty fields do not
// filter; a legal entity outside the caller's scope is hidden by row-level
// security.
type ChangeRequestFilter struct {
	LegalEntityID string
	Status        string
	OrderID       string
	Limit         int
}

// ChangeDecision is the result of an approval: the request as decided and the
// order after the change was applied.
type ChangeDecision struct {
	Request OrderChangeRequest `json:"request"`
	Order   Order              `json:"order"`
}

func changeRequestNotFound() error {
	return problem(http.StatusNotFound, CodeChangeNotFound, "change request not found")
}

// changeApprovalRequired reports whether the customer's changes to confirmed
// orders need approval.
func changeApprovalRequired(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID string) (bool, error) {
	var required bool
	err := tx.QueryRow(ctx, `select approval_required from customer_change_policy
		where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3`, tenantID, legalEntityID, customerID).Scan(&required)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return required, err
}

// submitChangeRequestTx checks that req would change the locked order o, then
// stores it as a PENDING request and writes orderchange.requested. A request
// that changes nothing stores nothing and returns nil. An order has at most
// one PENDING request (SLS-0043).
func submitChangeRequestTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, req ChangeRequest, actor string, now time.Time) (*OrderChangeRequest, error) {
	trial := o
	trial.Lines = append([]Line(nil), o.Lines...)
	// The stored diff shows the confirmed ship date the delivery date control
	// gives today (BR-27.2), as the PATCH without approval would.
	rule, err := loadDateRule(ctx, tx, tenantID, o.LegalEntityID, now)
	if err != nil {
		return nil, err
	}
	trial.dates = rule
	diff, _, err := ApplyChange(&trial, req)
	if err != nil {
		return nil, problem(http.StatusUnprocessableEntity, CodeInvalidChange, err.Error())
	}
	if len(diff) == 0 {
		return nil, nil
	}
	var pending bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from order_change_request where tenant_id = $1 and order_id = $2 and status = 'PENDING')`,
		tenantID, o.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, problem(http.StatusConflict, CodeChangePending, "the order already has a change waiting for approval; decide it first")
	}
	change, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	diffJSON, err := json.Marshal(diff)
	if err != nil {
		return nil, err
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into order_change_request (tenant_id, legal_entity_id, id, order_id, base_version, reason, change, diff, requested_by, requested_at)
		values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10)`,
		tenantID, o.LegalEntityID, id, o.ID, o.Version, req.Reason, string(change), string(diffJSON), actor, now); err != nil {
		return nil, err
	}
	if err := enqueue(ctx, tx, "orderchange", id, salesevents.OrderChangeRequested, tenantID, salesevents.OrderChangeRequestedData{
		RequestID: id, LegalEntityID: o.LegalEntityID, OrderID: o.ID, CustomerID: o.CustomerID, Kind: KindChange, BaseVersion: o.Version, Reason: req.Reason, Actor: actor, Diff: salesevents.DiffOf(diff),
	}); err != nil {
		return nil, err
	}
	r, err := loadChangeRequest(ctx, tx, tenantID, id, false)
	return &r, err
}

const changeRequestSelect = `select r.id::text, r.legal_entity_id::text, r.order_id::text, o.order_number, o.customer_id::text, r.kind, r.status, r.base_version,
		r.reason, r.change::text, r.diff::text, r.requested_by, r.requested_at, r.decided_by, r.decided_at, r.decision_comment, r.applied_version, r.version
	from order_change_request r join sales_order o on o.tenant_id = r.tenant_id and o.id = r.order_id`

func scanChangeRequest(row pgx.Row) (OrderChangeRequest, error) {
	var r OrderChangeRequest
	var change, diff string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.OrderID, &r.OrderNumber, &r.CustomerID, &r.Kind, &r.Status, &r.BaseVersion, &r.Reason, &change, &diff,
		&r.RequestedBy, &r.RequestedAt, &r.DecidedBy, &r.DecidedAt, &r.DecisionComment, &r.AppliedVersion, &r.Version); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(change), &r.Change); err != nil {
		return r, err
	}
	if r.Change.Lines == nil {
		r.Change.Lines = []LineChange{}
	}
	if err := json.Unmarshal([]byte(diff), &r.Diff); err != nil {
		return r, err
	}
	return r, nil
}

func loadChangeRequest(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (OrderChangeRequest, error) {
	q := changeRequestSelect + ` where r.tenant_id = $1 and r.id = $2`
	if lock {
		q += ` for update of r`
	}
	r, err := scanChangeRequest(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, changeRequestNotFound()
	}
	return r, err
}

// GetChangeRequest returns one change request.
func (s *Postgres) GetChangeRequest(ctx context.Context, id string) (OrderChangeRequest, error) {
	p, _ := tenant.FromContext(ctx)
	var out OrderChangeRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadChangeRequest(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ListChangeRequests lists change requests: PENDING ones oldest first (the
// approval queue), any other filter newest first.
func (s *Postgres) ListChangeRequests(ctx context.Context, f ChangeRequestFilter) ([]OrderChangeRequest, error) {
	p, _ := tenant.FromContext(ctx)
	limit := f.Limit
	if limit < 1 || limit > maxChangeRequestsListed {
		limit = maxChangeRequestsListed
	}
	dir := "desc"
	if f.Status == ChangePending {
		dir = "asc"
	}
	out := []OrderChangeRequest{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, changeRequestSelect+` where r.tenant_id = $1
			and ($2 = '' or r.legal_entity_id::text = $2) and ($3 = '' or r.status = $3) and ($4 = '' or r.order_id::text = $4)
			order by r.requested_at `+dir+`, r.id limit $5`, p.TenantID, f.LegalEntityID, f.Status, f.OrderID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanChangeRequest(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ApproveChangeRequest applies a PENDING request to its order and records the
// approval, in one transaction (FR-27.9). version is the request's version
// (If-Match). The requester cannot approve their own request (SLS-0045); a
// request already decided is SLS-0044; an order that moved since the request
// was made is SLS-0046 and the request stays PENDING so it can be rejected. A
// change that no longer applies cleanly fails with its own problem and also
// leaves the request PENDING. The order gets its version through the same path
// as a direct PATCH, as the requester's change: order.changed names the
// requester, orderchange.approved the approver.
func (s *Postgres) ApproveChangeRequest(ctx context.Context, id string, version int, comment, actor string, now time.Time) (ChangeDecision, error) {
	p, _ := tenant.FromContext(ctx)
	var out ChangeDecision
	err := s.orderTx(ctx, p.TenantID, &out.Order, func(tx pgx.Tx) error {
		r, err := loadChangeRequest(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if r.Version != version {
			return versionConflict()
		}
		if r.Status != ChangePending {
			return problem(http.StatusConflict, CodeChangeDecided, "the change request was already "+r.Status)
		}
		if r.RequestedBy == actor {
			return problem(http.StatusForbidden, CodeChangeSelfApproval, "a change cannot be approved by the user who requested it")
		}
		o, err := loadOrder(ctx, tx, p.TenantID, r.OrderID, true)
		if err != nil {
			return err
		}
		if o.Version != r.BaseVersion || o.Status != domain.OrderConfirmed {
			return problem(http.StatusConflict, CodeChangeStale, "the order changed since the request was made; reject it and ask for a new change")
		}
		var applied Order
		if r.Kind == KindCancel {
			applied, err = cancelOrderTx(ctx, tx, p.TenantID, o, r.Reason, r.RequestedBy, now)
		} else {
			applied, err = applyChangeTx(ctx, tx, p.TenantID, o, r.OrderID, r.Change, r.RequestedBy, now)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update order_change_request set status = 'APPROVED', decided_by = $3, decided_at = $4, decision_comment = $5,
				applied_version = $6, version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, id, actor, now, comment, applied.Version); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "orderchange", id, salesevents.OrderChangeApproved, p.TenantID, salesevents.OrderChangeDecidedData{
			RequestID: id, LegalEntityID: r.LegalEntityID, OrderID: r.OrderID, Kind: r.Kind, RequestedBy: r.RequestedBy, Actor: actor, Comment: comment, AppliedVersion: &applied.Version,
		}); err != nil {
			return err
		}
		out.Order = applied
		out.Request, err = loadChangeRequest(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// submitCancelRequestTx stores the cancellation of the locked CONFIRMED order o
// as a PENDING request of kind CANCEL and writes orderchange.requested with a
// status diff. An order has at most one PENDING request (SLS-0043).
func submitCancelRequestTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, reason, actor string, now time.Time) (*OrderChangeRequest, error) {
	var pending bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from order_change_request where tenant_id = $1 and order_id = $2 and status = 'PENDING')`,
		tenantID, o.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, problem(http.StatusConflict, CodeChangePending, "the order already has a change waiting for approval; decide it first")
	}
	diff := domain.DiffHeader("status", o.Status, "CANCELLED")
	diffJSON, err := json.Marshal(diff)
	if err != nil {
		return nil, err
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into order_change_request (tenant_id, legal_entity_id, id, order_id, kind, base_version, reason, change, diff, requested_by, requested_at)
		values ($1, $2, $3, $4, 'CANCEL', $5, $6, '{}'::jsonb, $7::jsonb, $8, $9)`,
		tenantID, o.LegalEntityID, id, o.ID, o.Version, reason, string(diffJSON), actor, now); err != nil {
		return nil, err
	}
	if err := enqueue(ctx, tx, "orderchange", id, salesevents.OrderChangeRequested, tenantID, salesevents.OrderChangeRequestedData{
		RequestID: id, LegalEntityID: o.LegalEntityID, OrderID: o.ID, CustomerID: o.CustomerID, Kind: KindCancel, BaseVersion: o.Version, Reason: reason, Actor: actor, Diff: salesevents.DiffOf(diff),
	}); err != nil {
		return nil, err
	}
	r, err := loadChangeRequest(ctx, tx, tenantID, id, false)
	return &r, err
}

// WithdrawChangeRequest lets the requester take back their own PENDING
// request (FR-27.9): it ends as WITHDRAWN, the order stays as it is and
// orderchange.withdrawn is written in the same transaction. Anyone else is
// refused with SLS-0114 (an approver rejects instead); a request already
// decided is SLS-0044.
func (s *Postgres) WithdrawChangeRequest(ctx context.Context, id string, version int, comment, actor string, now time.Time) (OrderChangeRequest, error) {
	p, _ := tenant.FromContext(ctx)
	var out OrderChangeRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := loadChangeRequest(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if r.RequestedBy != actor {
			return problem(http.StatusForbidden, CodeChangeNotRequester, "only the user who requested a change can withdraw it; an approver rejects it instead")
		}
		if r.Version != version {
			return versionConflict()
		}
		if r.Status != ChangePending {
			return problem(http.StatusConflict, CodeChangeDecided, "the change request was already "+r.Status)
		}
		if _, err := tx.Exec(ctx, `update order_change_request set status = 'WITHDRAWN', decided_by = $3, decided_at = $4, decision_comment = $5,
				version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, actor, now, comment); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "orderchange", id, salesevents.OrderChangeWithdrawn, p.TenantID, salesevents.OrderChangeDecidedData{
			RequestID: id, LegalEntityID: r.LegalEntityID, OrderID: r.OrderID, Kind: r.Kind, RequestedBy: r.RequestedBy, Actor: actor, Comment: comment,
		}); err != nil {
			return err
		}
		out, err = loadChangeRequest(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// RejectChangeRequest records the rejection of a PENDING request; the order
// stays as it is. Anyone with the approve duty may reject, including the
// requester withdrawing their own change.
func (s *Postgres) RejectChangeRequest(ctx context.Context, id string, version int, comment, actor string, now time.Time) (OrderChangeRequest, error) {
	p, _ := tenant.FromContext(ctx)
	var out OrderChangeRequest
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := loadChangeRequest(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if r.Version != version {
			return versionConflict()
		}
		if r.Status != ChangePending {
			return problem(http.StatusConflict, CodeChangeDecided, "the change request was already "+r.Status)
		}
		if _, err := tx.Exec(ctx, `update order_change_request set status = 'REJECTED', decided_by = $3, decided_at = $4, decision_comment = $5,
				version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, actor, now, comment); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "orderchange", id, salesevents.OrderChangeRejected, p.TenantID, salesevents.OrderChangeDecidedData{
			RequestID: id, LegalEntityID: r.LegalEntityID, OrderID: r.OrderID, Kind: r.Kind, RequestedBy: r.RequestedBy, Actor: actor, Comment: comment,
		}); err != nil {
			return err
		}
		out, err = loadChangeRequest(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// GetChangePolicy returns the customer's change approval switch, or the
// default (off, version 0) when none was saved.
func (s *Postgres) GetChangePolicy(ctx context.Context, legalEntityID, customerID string) (ChangePolicy, error) {
	p, _ := tenant.FromContext(ctx)
	out := ChangePolicy{LegalEntityID: legalEntityID, CustomerID: customerID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return loadChangePolicy(ctx, tx, p.TenantID, &out)
	})
	return out, err
}

func loadChangePolicy(ctx context.Context, tx pgx.Tx, tenantID string, out *ChangePolicy) error {
	var updatedAt time.Time
	err := tx.QueryRow(ctx, `select approval_required, version, updated_by, updated_at from customer_change_policy
		where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3`, tenantID, out.LegalEntityID, out.CustomerID).
		Scan(&out.ApprovalRequired, &out.Version, &out.UpdatedBy, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	out.UpdatedAt = &updatedAt
	return nil
}

// PutChangePolicy creates the switch when version is 0 and otherwise replaces
// it when version matches the stored one; any other version is a version
// conflict. changepolicy.changed is written in the same transaction.
func (s *Postgres) PutChangePolicy(ctx context.Context, in ChangePolicy, version int, actor string) (ChangePolicy, error) {
	p, _ := tenant.FromContext(ctx)
	out := ChangePolicy{LegalEntityID: in.LegalEntityID, CustomerID: in.CustomerID}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stored int
		var err error
		if version == 0 {
			err = tx.QueryRow(ctx, `insert into customer_change_policy (tenant_id, legal_entity_id, customer_id, approval_required, updated_by)
				values ($1, $2, $3, $4, $5) on conflict do nothing returning version`,
				p.TenantID, in.LegalEntityID, in.CustomerID, in.ApprovalRequired, actor).Scan(&stored)
		} else {
			err = tx.QueryRow(ctx, `update customer_change_policy set approval_required = $4, updated_by = $5, version = version + 1, updated_at = now()
				where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3 and version = $6 returning version`,
				p.TenantID, in.LegalEntityID, in.CustomerID, in.ApprovalRequired, actor, version).Scan(&stored)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return versionConflict()
		}
		if err != nil {
			return err
		}
		if err := loadChangePolicy(ctx, tx, p.TenantID, &out); err != nil {
			return err
		}
		return enqueue(ctx, tx, "changepolicy", in.CustomerID, salesevents.ChangePolicyChanged, p.TenantID, salesevents.ChangePolicyChangedData{
			LegalEntityID: in.LegalEntityID, CustomerID: in.CustomerID, ApprovalRequired: out.ApprovalRequired, Actor: actor, Version: out.Version})
	})
	return out, err
}
