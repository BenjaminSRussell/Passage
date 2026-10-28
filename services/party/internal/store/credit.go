package store

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	partyevents "github.com/BenjaminSRussell/passage/services/party/internal/events"
)

const (
	CodeCustomerNotFound = "PTY-CUSTOMER-NOT-FOUND"
	CodeChangeNotFound   = "PTY-CHANGE-NOT-FOUND"
	CodeChangePending    = "PTY-CHANGE-PENDING"
	CodeChangeDecided    = "PTY-CHANGE-DECIDED"
	CodeSelfApproval     = "PTY-PENDING-SELF-APPROVAL"
	CodeChangeStale      = "PTY-PENDING-STALE"
	CodeCreditInvalid    = "PTY-CREDIT-INVALID"
	changeColumns        = `id::text, customer_id::text, legal_entity_id::text, field, current_value::text, requested_value::text, status, requested_by, decided_by, decision_reason, requested_at, decided_at`
)

var creditLimitPattern = regexp.MustCompile(`^[0-9]{1,16}(\.[0-9]{1,2})?$`)

type CreditStore interface {
	RequestCreditLimit(ctx context.Context, customerID, limit string) (domain.CreditLimitResult, error)
	DecidePendingChange(ctx context.Context, changeID string, approve bool, reason string) (domain.PendingChange, error)
}

const maxReasonLength = 500

func requireUser(caller tenant.Principal) error {
	if strings.TrimSpace(caller.UserID) == "" {
		return httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing user")
	}
	return nil
}

func scanChange(row pgx.Row) (domain.PendingChange, error) {
	var c domain.PendingChange
	err := row.Scan(&c.ID, &c.CustomerID, &c.LegalEntityID, &c.Field, &c.CurrentValue, &c.RequestedValue, &c.Status, &c.RequestedBy, &c.DecidedBy, &c.Reason, &c.RequestedAt, &c.DecidedAt)
	return c, err
}

func notFound(code, detail string) error {
	return httpx.NewProblem(http.StatusNotFound, code, detail)
}

func enqueueCustomer(ctx context.Context, tx pgx.Tx, tenantID, customerID, eventType string, data any) error {
	evt, err := events.New(eventType, domain.EventSource, customerID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, domain.AggregateCustomer, customerID, evt)
}

func changeEvent(c domain.PendingChange, decidedBy string) partyevents.ChangeData {
	return partyevents.ChangeData{
		ChangeID: c.ID, CustomerID: c.CustomerID, LegalEntityID: c.LegalEntityID, Field: c.Field,
		CurrentValue: c.CurrentValue, RequestedValue: c.RequestedValue, RequestedBy: c.RequestedBy, DecidedBy: decidedBy, Reason: c.Reason,
	}
}

func (s *Postgres) RequestCreditLimit(ctx context.Context, customerID, limit string) (domain.CreditLimitResult, error) {
	caller, _ := tenant.FromContext(ctx)
	if err := requireUser(caller); err != nil {
		return domain.CreditLimitResult{}, err
	}
	id, err := canonicalID(customerID)
	if err != nil {
		return domain.CreditLimitResult{}, notFound(CodeCustomerNotFound, "customer not found")
	}
	if !creditLimitPattern.MatchString(limit) {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeCreditInvalid, "credit limit failed validation")
		p.Errors = map[string]string{"creditLimit": "must be a non-negative amount with at most 2 decimals"}
		return domain.CreditLimitResult{}, p
	}
	var res domain.CreditLimitResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var legalEntityID string
		var increase, changed bool
		err := tx.QueryRow(ctx, `select legal_entity_id::text, credit_limit::text, $3::text::numeric > coalesce(credit_limit, 0), $3::text::numeric is distinct from credit_limit
			from customer_role where tenant_id = $1 and id = $2 for update`, caller.TenantID, id, limit).Scan(&legalEntityID, &res.CreditLimit, &increase, &changed)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeCustomerNotFound, "customer not found")
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if !increase {
			if err := tx.QueryRow(ctx, `update customer_role set credit_limit = $3::text::numeric, version = version + 1
				where tenant_id = $1 and id = $2 returning credit_limit::text`, caller.TenantID, id, limit).Scan(&res.CreditLimit); err != nil {
				return err
			}
			return enqueueCustomer(ctx, tx, caller.TenantID, id, partyevents.CustomerUpdated, partyevents.CustomerUpdatedData{
				CustomerID: id, LegalEntityID: legalEntityID, ChangedFields: []string{"creditLimit"}, CreditLimit: limit,
			})
		}
		change, err := scanChange(tx.QueryRow(ctx, `insert into pending_change (tenant_id, id, legal_entity_id, customer_id, field, current_value, requested_value, requested_by)
			values ($1, $2, $3, $4, $5, $6::text::numeric, $7::text::numeric, $8) returning `+changeColumns,
			caller.TenantID, ids.New(), legalEntityID, id, domain.FieldCreditLimit, res.CreditLimit, limit, caller.UserID))
		if isUniqueViolation(err) {
			return httpx.NewProblem(http.StatusConflict, CodeChangePending, "a credit limit change is already awaiting approval for this customer")
		}
		if err != nil {
			return err
		}
		res.Pending = &change
		return enqueueCustomer(ctx, tx, caller.TenantID, id, partyevents.ChangeRequested, changeEvent(change, ""))
	})
	return res, err
}

func (s *Postgres) DecidePendingChange(ctx context.Context, changeID string, approve bool, reason string) (domain.PendingChange, error) {
	caller, _ := tenant.FromContext(ctx)
	if err := requireUser(caller); err != nil {
		return domain.PendingChange{}, err
	}
	if len(reason) > maxReasonLength {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeCreditInvalid, "decision failed validation")
		p.Errors = map[string]string{"reason": "must be at most 500 bytes"}
		return domain.PendingChange{}, p
	}
	id, err := canonicalID(changeID)
	if err != nil {
		return domain.PendingChange{}, notFound(CodeChangeNotFound, "pending change not found")
	}
	var change domain.PendingChange
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanChange(tx.QueryRow(ctx, `select `+changeColumns+` from pending_change where tenant_id = $1 and id = $2 for update`, caller.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeChangeNotFound, "pending change not found")
		}
		if err != nil {
			return err
		}
		wanted := domain.ChangeRejected
		if approve {
			wanted = domain.ChangeApproved
		}
		if cur.Status == wanted && cur.DecidedBy != nil && *cur.DecidedBy == caller.UserID {
			change = cur
			return nil
		}
		if cur.Status != domain.ChangePending {
			return httpx.NewProblem(http.StatusConflict, CodeChangeDecided, "pending change was already "+cur.Status)
		}
		if caller.UserID == cur.RequestedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeSelfApproval, "the requester cannot decide their own change")
		}
		status, eventType := domain.ChangeRejected, partyevents.ChangeRejected
		if approve {
			status, eventType = domain.ChangeApproved, partyevents.ChangeApproved
			tag, err := tx.Exec(ctx, `update customer_role set credit_limit = $3::text::numeric, version = version + 1
				where tenant_id = $1 and id = $2 and credit_limit is not distinct from $4::text::numeric`,
				caller.TenantID, cur.CustomerID, cur.RequestedValue, cur.CurrentValue)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return httpx.NewProblem(http.StatusConflict, CodeChangeStale, "the credit limit changed after this request; reject it and request again")
			}
		}
		change, err = scanChange(tx.QueryRow(ctx, `update pending_change set status = $3, decided_by = $4, decision_reason = nullif($5, ''), decided_at = now()
			where tenant_id = $1 and id = $2 returning `+changeColumns, caller.TenantID, id, status, caller.UserID, reason))
		if err != nil {
			return err
		}
		return enqueueCustomer(ctx, tx, caller.TenantID, change.CustomerID, eventType, changeEvent(change, caller.UserID))
	})
	return change, err
}
