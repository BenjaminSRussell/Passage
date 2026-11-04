package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

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
	// CodeNotChangeApprover: the customer names its approvers and the caller is
	// not one of them (slice 27).
	CodeNotChangeApprover = "SLS-0150"

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
	LegalEntityID    string `json:"legalEntityId"`
	CustomerID       string `json:"customerId"`
	ApprovalRequired bool   `json:"approvalRequired"`
	// ApprovalThreshold, when set, limits approval to changes that move the
	// order total by at least this amount in ThresholdCurrency (a cancellation
	// moves it by the whole total); absent means every change needs approval.
	ApprovalThreshold *decimal.Decimal `json:"approvalThreshold,omitempty"`
	ThresholdCurrency string           `json:"thresholdCurrency,omitempty"`
	// ApproverIDs, when not empty, are the only users (canonical identity user
	// ids) who may approve or reject a request for this customer.
	ApproverIDs []string   `json:"approverIds"`
	Version     int        `json:"version"`
	UpdatedBy   string     `json:"updatedBy,omitempty"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
	// KeepThreshold and KeepApprovers make PutChangePolicy leave the stored
	// threshold (with its currency) or group as they are: a body that does not
	// name them, such as the slice 13 {"approvalRequired": ...}, must not
	// clear what a newer client set.
	KeepThreshold bool `json:"-"`
	KeepApprovers bool `json:"-"`
}

// Approval returns the policy as the domain rules take it.
func (p ChangePolicy) Approval() domain.ApprovalPolicy {
	return domain.ApprovalPolicy{Required: p.ApprovalRequired, Threshold: p.ApprovalThreshold, ThresholdCurrency: p.ThresholdCurrency, Approvers: p.ApproverIDs}
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

// loadApprovalPolicy returns the customer's approval policy; a customer that
// never saved one needs no approval.
func loadApprovalPolicy(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID string) (domain.ApprovalPolicy, error) {
	pol := ChangePolicy{LegalEntityID: legalEntityID, CustomerID: customerID}
	if err := loadChangePolicy(ctx, tx, tenantID, &pol); err != nil {
		return domain.ApprovalPolicy{}, err
	}
	return pol.Approval(), nil
}

// requireChangeApprover refuses a decision by a user outside the customer's
// named approver group (SLS-0150). The order's customer decides the group.
func requireChangeApprover(ctx context.Context, tx pgx.Tx, tenantID string, r OrderChangeRequest, actor string) error {
	pol, err := loadApprovalPolicy(ctx, tx, tenantID, r.LegalEntityID, r.CustomerID)
	if err != nil {
		return err
	}
	if !pol.MayDecide(actor) {
		return problem(http.StatusForbidden, CodeNotChangeApprover, "this customer's changes can be decided only by its named approvers")
	}
	return nil
}

// changeValue is the value req would move on the locked order o: the sum of
// each line's absolute net movement (a cancelled line counts as 0), computed
// on a copy of o the way the change would be applied. Summing per line means a
// rise on one line and an equal cut on another count twice, not zero.
func changeValue(ctx context.Context, tx pgx.Tx, tenantID string, o Order, req ChangeRequest, now time.Time) (decimal.Decimal, error) {
	trial := o
	trial.Lines = append([]Line(nil), o.Lines...)
	rule, err := loadDateRule(ctx, tx, tenantID, o.LegalEntityID, now)
	if err != nil {
		return decimal.Zero, err
	}
	trial.dates = rule
	req, err = prepareChange(ctx, tx, tenantID, o, req, "", now)
	if err != nil {
		return decimal.Zero, err
	}
	if _, _, err := ApplyChange(&trial, req); err != nil {
		return decimal.Zero, problem(http.StatusUnprocessableEntity, CodeInvalidChange, err.Error())
	}
	moved := decimal.Zero
	for i, l := range trial.Lines {
		// A line the change added moves its whole net.
		was := decimal.Zero
		if i < len(o.Lines) {
			was = effectiveNet(o.Lines[i])
		}
		moved = moved.Add(effectiveNet(l).Sub(was).Abs())
	}
	return moved, nil
}

func effectiveNet(l Line) decimal.Decimal {
	if l.LineStatus == domain.LineCancelled {
		return decimal.Zero
	}
	return l.LineNet
}

// unapprovedChangeValue is what the changes applied to the locked order
// without approval have moved so far.
func unapprovedChangeValue(ctx context.Context, tx pgx.Tx, tenantID, orderID string) (decimal.Decimal, error) {
	var moved decimal.Decimal
	err := tx.QueryRow(ctx, `select unapproved_change_value from sales_order where tenant_id = $1 and id = $2`, tenantID, orderID).Scan(&moved)
	return moved, err
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
	prepared, err := prepareChange(ctx, tx, tenantID, o, req, actor, now)
	if err != nil {
		return nil, err
	}
	diff, _, err := ApplyChange(&trial, prepared)
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
		if err := requireChangeApprover(ctx, tx, p.TenantID, r, actor); err != nil {
			return err
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
			// The approver holds sales.order.approve (the route) and is one the
			// customer lets decide, so a listing override the request carries
			// (listingOverrideReason) is accepted on their approval.
			change := r.Change
			change.Approver = true
			applied, err = applyChangeTx(ctx, tx, p.TenantID, o, r.OrderID, change, r.RequestedBy, now)
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
		if err := requireChangeApprover(ctx, tx, p.TenantID, r, actor); err != nil {
			return err
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
	out.ApproverIDs = []string{}
	err := tx.QueryRow(ctx, `select approval_required, approval_threshold, coalesce(threshold_currency, ''), approver_user_ids::text[], version, updated_by, updated_at
		from customer_change_policy where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3`, tenantID, out.LegalEntityID, out.CustomerID).
		Scan(&out.ApprovalRequired, &out.ApprovalThreshold, &out.ThresholdCurrency, &out.ApproverIDs, &out.Version, &out.UpdatedBy, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	out.UpdatedAt = &updatedAt
	return nil
}

// PutChangePolicy creates the policy when version is 0 and otherwise replaces
// it when version matches the stored one; any other version is a version
// conflict. The threshold (with its currency) and the group are kept as stored
// when in says so (KeepThreshold, KeepApprovers). changepolicy.changed is
// written in the same transaction.
func (s *Postgres) PutChangePolicy(ctx context.Context, in ChangePolicy, version int, actor string) (ChangePolicy, error) {
	p, _ := tenant.FromContext(ctx)
	out := ChangePolicy{LegalEntityID: in.LegalEntityID, CustomerID: in.CustomerID}
	approvers, invalid := domain.NormalizeApprovers(in.ApproverIDs)
	if len(invalid) > 0 {
		return out, problem(http.StatusUnprocessableEntity, CodeInvalidChange, "invalid approval policy: approverIds must be user ids (UUIDs)")
	}
	if in.KeepThreshold {
		in.ApprovalThreshold, in.ThresholdCurrency = nil, ""
	}
	if f := domain.ValidateApprovalPolicy(domain.ApprovalPolicy{Threshold: in.ApprovalThreshold, ThresholdCurrency: in.ThresholdCurrency, Approvers: approvers}); len(f) > 0 {
		return out, problem(http.StatusUnprocessableEntity, CodeInvalidChange, "invalid approval policy: "+firstField(f))
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stored int
		var err error
		if version == 0 {
			err = tx.QueryRow(ctx, `insert into customer_change_policy (tenant_id, legal_entity_id, customer_id, approval_required, approval_threshold, threshold_currency, approver_user_ids, updated_by)
				values ($1, $2, $3, $4, $5, nullif($6, ''), $7::text[]::uuid[], $8) on conflict do nothing returning version`,
				p.TenantID, in.LegalEntityID, in.CustomerID, in.ApprovalRequired, in.ApprovalThreshold, in.ThresholdCurrency, approvers, actor).Scan(&stored)
		} else {
			err = tx.QueryRow(ctx, `update customer_change_policy set approval_required = $4,
					approval_threshold = case when $10 then approval_threshold else $5 end,
					threshold_currency = case when $10 then threshold_currency else nullif($6, '') end,
					approver_user_ids = case when $11 then approver_user_ids else $7::text[]::uuid[] end,
					updated_by = $8, version = version + 1, updated_at = now()
				where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3 and version = $9 returning version`,
				p.TenantID, in.LegalEntityID, in.CustomerID, in.ApprovalRequired, in.ApprovalThreshold, in.ThresholdCurrency, approvers, actor, version,
				in.KeepThreshold, in.KeepApprovers).Scan(&stored)
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
			LegalEntityID: in.LegalEntityID, CustomerID: in.CustomerID, ApprovalRequired: out.ApprovalRequired, ApprovalThreshold: thresholdString(out.ApprovalThreshold),
			ThresholdCurrency: out.ThresholdCurrency, ApproverIDs: out.ApproverIDs, Actor: actor, Version: out.Version})
	})
	return out, err
}

func thresholdString(d *decimal.Decimal) string {
	if d == nil {
		return ""
	}
	return d.StringFixed(4)
}

// firstField names the first (alphabetical) invalid field of a validation map.
func firstField(f map[string]string) string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0] + " " + f[keys[0]]
}
