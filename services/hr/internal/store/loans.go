package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeLoanNotFound = "HCM-LOAN-NOT-FOUND"
	CodeLoanState    = "HCM-LOAN-STATE"
	CodeLoanDate     = "HCM-LOAN-DATE"

	checkViolation = "23514"
)

// LoanedItem is an item on loan to a worker (FR-42.17): free text or a wave-44
// asset (assetId is stored and not checked until wave 44 exists).
type LoanedItem struct {
	ID              string    `json:"id"`
	WorkerID        string    `json:"workerId"`
	LoanType        string    `json:"loanType"`
	ItemDescription string    `json:"itemDescription"`
	AssetID         *string   `json:"assetId,omitempty"`
	Serial          *string   `json:"serial,omitempty"`
	IssuedOn        string    `json:"issuedOn"`
	DueOn           *string   `json:"dueOn,omitempty"`
	ClosedOn        *string   `json:"closedOn,omitempty"`
	ConditionOut    *string   `json:"conditionOut,omitempty"`
	ConditionIn     *string   `json:"conditionIn,omitempty"`
	LegalEntityID   *string   `json:"legalEntityId,omitempty"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
}

const loanColumns = `id::text, worker_id::text, loan_type, item_description, asset_id::text, serial, to_char(issued_on, 'YYYY-MM-DD'),
	to_char(due_on, 'YYYY-MM-DD'), to_char(closed_on, 'YYYY-MM-DD'), condition_out, condition_in, legal_entity_id::text, status, created_at`

func scanLoan(row pgx.Row) (LoanedItem, error) {
	var l LoanedItem
	err := row.Scan(&l.ID, &l.WorkerID, &l.LoanType, &l.ItemDescription, &l.AssetID, &l.Serial, &l.IssuedOn, &l.DueOn, &l.ClosedOn,
		&l.ConditionOut, &l.ConditionIn, &l.LegalEntityID, &l.Status, &l.CreatedAt)
	l.CreatedAt = utc(l.CreatedAt)
	return l, err
}

func loanEvent(l LoanedItem) hrevents.LoanedItemChangedData {
	return hrevents.LoanedItemChangedData{ID: l.ID, WorkerID: l.WorkerID, LoanType: l.LoanType, AssetID: l.AssetID, Status: l.Status,
		IssuedOn: l.IssuedOn, DueOn: l.DueOn, ClosedOn: l.ClosedOn}
}

// IssueLoanedItem lends an item to a worker; it starts OUT. A replayed
// Idempotency-Key returns the stored loan.
func (s *Postgres) IssueLoanedItem(ctx context.Context, in LoanedItem, idempotencyKey string) (LoanedItem, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out LoanedItem
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanLoan(tx.QueryRow(ctx, `select `+loanColumns+` from hr_loaned_item where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
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
		entity, err := workerEntity(ctx, tx, p.TenantID, in.WorkerID, in.IssuedOn, in.LegalEntityID)
		if err != nil {
			return err
		}
		out, err = scanLoan(tx.QueryRow(ctx, `insert into hr_loaned_item (tenant_id, id, worker_id, legal_entity_id, loan_type, item_description, asset_id, serial,
				issued_on, due_on, condition_out, idempotency_key)
			values ($1, $2, $3, $12, $4, $5, $6, $7, $8::date, $9::date, $10, $11) returning `+loanColumns,
			p.TenantID, ids.New(), in.WorkerID, in.LoanType, in.ItemDescription, in.AssetID, in.Serial, in.IssuedOn, in.DueOn, in.ConditionOut, key, entity))
		if err != nil {
			return err
		}
		inserted = true
		return enqueue(ctx, tx, "loaneditem", out.ID, hrevents.LoanedItemChanged, p.TenantID, loanEvent(out))
	})
	if idempotencyRace(err) {
		return LoanedItem{}, false, idempotencyInFlight()
	}
	if pgCode(err, rlsViolation) {
		return LoanedItem{}, false, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	if pgCode(err, checkViolation) {
		return LoanedItem{}, false, problem(http.StatusUnprocessableEntity, CodeLoanDate, "the due date cannot be before the issue date")
	}
	if err == nil && !inserted && (out.WorkerID != in.WorkerID || out.LoanType != in.LoanType || out.ItemDescription != in.ItemDescription || out.IssuedOn != in.IssuedOn) {
		return LoanedItem{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// CloseLoanedItem records an item RETURNED or LOST on a date. Closing again
// with the same status changes nothing; a different status is HCM-LOAN-STATE.
func (s *Postgres) CloseLoanedItem(ctx context.Context, id, status, closedOn string, conditionIn *string) (LoanedItem, bool, error) {
	p := principal(ctx)
	var out LoanedItem
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanLoan(tx.QueryRow(ctx, `select `+loanColumns+` from hr_loaned_item where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeLoanNotFound, "loaned item not found")
		}
		if err != nil {
			return err
		}
		if out.Status != domain.LoanOut {
			if out.Status == status {
				return nil
			}
			return problem(http.StatusConflict, CodeLoanState, "the item is already "+out.Status)
		}
		out, err = scanLoan(tx.QueryRow(ctx, `update hr_loaned_item set status = $3, closed_on = $4::date, condition_in = $5, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+loanColumns, p.TenantID, id, status, closedOn, conditionIn))
		if err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "loaneditem", out.ID, hrevents.LoanedItemChanged, p.TenantID, loanEvent(out))
	})
	if pgCode(err, checkViolation) {
		return LoanedItem{}, false, problem(http.StatusUnprocessableEntity, CodeLoanDate, "the item cannot be closed before it was issued")
	}
	return out, changed, err
}

// ListWorkerLoans returns a worker's loans, newest issue first.
func (s *Postgres) ListWorkerLoans(ctx context.Context, workerID string) ([]LoanedItem, error) {
	return s.listLoans(ctx, `worker_id = $2 order by issued_on desc, id`, workerID, true)
}

// ListOutstandingLoans returns the items still OUT, soonest due first (undated
// last), optionally only those due on or before a date ("Loaned items
// outstanding", 8.42.14). At most 500 are returned.
func (s *Postgres) ListOutstandingLoans(ctx context.Context, dueBy string) ([]LoanedItem, error) {
	return s.listLoans(ctx, `status = 'OUT' and ($2 = '' or due_on <= nullif($2, '')::date) order by due_on nulls last, id`, dueBy, false)
}

func (s *Postgres) listLoans(ctx context.Context, where, arg string, workerExists bool) ([]LoanedItem, error) {
	p := principal(ctx)
	out := []LoanedItem{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if workerExists {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, arg).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
			}
		}
		rows, err := tx.Query(ctx, `select `+loanColumns+` from hr_loaned_item where tenant_id = $1 and `+where+` limit $3`, p.TenantID, arg, maxCourseRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLoan(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

const rlsViolation = "42501"

const (
	CodeEntityRequired = "HCM-ENTITY-REQUIRED"
	CodeEntityMismatch = "HCM-ENTITY-MISMATCH"
)

func entityList(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var le string
		if err := rows.Scan(&le); err != nil {
			return nil, err
		}
		out = append(out, le)
	}
	return out, rows.Err()
}

// workerEntity is the legal entity a worker's loan, checklist or answer
// belongs to: the entity of the employment valid on the date. When several are
// valid the caller must name one of them. With none valid on the date it falls
// back to the latest employment the caller can see; a worker with none yields
// nil, which only an all-entity caller can write or read.
func workerEntity(ctx context.Context, tx pgx.Tx, tenantID, workerID, on string, requested *string) (*string, error) {
	valid, err := entityList(ctx, tx, `select distinct legal_entity_id::text from employment where tenant_id = $1 and worker_id = $2 and validity @> $3::date::timestamptz`, tenantID, workerID, on)
	if err != nil {
		return nil, err
	}
	pick := func(list []string) (*string, error) {
		for _, le := range list {
			if requested != nil && le == *requested {
				return &le, nil
			}
		}
		return nil, problem(http.StatusUnprocessableEntity, CodeEntityMismatch, "the worker has no employment in that legal entity on that date")
	}
	switch {
	case len(valid) > 1:
		if requested == nil {
			return nil, problem(http.StatusUnprocessableEntity, CodeEntityRequired, "the worker is employed in several legal entities on that date; pass legalEntityId")
		}
		return pick(valid)
	case len(valid) == 1:
		if requested != nil {
			return pick(valid)
		}
		return &valid[0], nil
	}
	if requested != nil {
		any, err := entityList(ctx, tx, `select distinct legal_entity_id::text from employment where tenant_id = $1 and worker_id = $2`, tenantID, workerID)
		if err != nil {
			return nil, err
		}
		return pick(any)
	}
	latest, err := entityList(ctx, tx, `select legal_entity_id::text from employment where tenant_id = $1 and worker_id = $2 order by valid_from desc, id limit 1`, tenantID, workerID)
	if err != nil || len(latest) == 0 {
		return nil, err
	}
	return &latest[0], nil
}

func outstandingLoans(ctx context.Context, tx pgx.Tx, tenantID, workerID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `select hr_outstanding_loan_count($1, $2)`, tenantID, workerID).Scan(&n)
	return n, err
}
