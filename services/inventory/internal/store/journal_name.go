package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

const journalNameColumns = `id::text, legal_entity_id::text, code, description, journal_type, requires_approval, coalesce(offset_account_id, ''),
	active, version, created_at, updated_at`

func scanJournalName(row pgx.Row) (domain.JournalName, error) {
	var n domain.JournalName
	var t string
	err := row.Scan(&n.ID, &n.LegalEntityID, &n.Code, &n.Description, &t, &n.RequiresApproval, &n.OffsetAccountID, &n.Active, &n.Version, &n.CreatedAt, &n.UpdatedAt)
	n.Type = domain.JournalType(t)
	return n, err
}

func loadJournalName(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domain.JournalName, error) {
	tp, _ := tenant.FromContext(ctx)
	q := `select ` + journalNameColumns + ` from inventory_journal_name where tenant_id = $1 and id = $2`
	if forUpdate {
		q += " for update"
	}
	n, err := scanJournalName(tx.QueryRow(ctx, q, tp.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, problem(http.StatusNotFound, domain.CodeJournalNameNotFound, "journal name not found")
	}
	return n, err
}

// checkNamePostable applies the approval rule of the journal's name (BR-16.11):
// a name that requires approval refuses a post from DRAFT. A journal without a
// name posts from DRAFT or APPROVED as before.
func checkNamePostable(ctx context.Context, tx pgx.Tx, j domain.Journal) error {
	if j.JournalNameID == "" {
		return nil
	}
	n, err := loadJournalName(ctx, tx, j.JournalNameID, false)
	if err != nil {
		return err
	}
	if err := n.CheckPostable(j.Status); err != nil {
		return domainProblem(err)
	}
	return nil
}

func journalNameData(n domain.JournalName) invevents.JournalNameData {
	d := invevents.JournalNameData{JournalNameID: n.ID, LegalEntityID: n.LegalEntityID, Code: n.Code, Description: n.Description,
		JournalType: string(n.Type), RequiresApproval: n.RequiresApproval, Active: n.Active, Version: n.Version}
	if n.OffsetAccountID != "" {
		d.OffsetAccountID = &n.OffsetAccountID
	}
	return d
}

// CreateJournalName stores a journal name. The code is unique per legal entity
// (INV-JNLN-001); a repeated Idempotency-Key returns the stored name and a
// different body under it is idempotency_key_reuse.
func (s *Postgres) CreateJournalName(ctx context.Context, key string, in domain.JournalName) (domain.JournalName, bool, error) {
	in = domain.NormalizeJournalName(in)
	if err := in.Validate(); err != nil {
		return domain.JournalName{}, false, domainProblem(err)
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.JournalName
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "journalname", tp.TenantID, key); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from inventory_journal_name where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, key).Scan(&existing)
		if err == nil {
			replayed = true
			if out, err = loadJournalName(ctx, tx, existing, false); err != nil {
				return err
			}
			if out.LegalEntityID != in.LegalEntityID || out.Code != in.Code || out.Description != in.Description || out.Type != in.Type ||
				out.RequiresApproval != in.RequiresApproval || out.OffsetAccountID != in.OffsetAccountID {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var offset *string
		if in.OffsetAccountID != "" {
			offset = &in.OffsetAccountID
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into inventory_journal_name (tenant_id, legal_entity_id, id, idempotency_key, code, description, journal_type,
			requires_approval, offset_account_id, created_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			tp.TenantID, in.LegalEntityID, id, key, in.Code, in.Description, string(in.Type), in.RequiresApproval, offset, tp.UserID); err != nil {
			switch {
			case isUniqueOn(err, "idempotency_key"):
				// The key is held by a name RLS hides (a legal entity outside the caller's scope).
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			case isUniqueOn(err, "code"):
				return problem(http.StatusConflict, domain.CodeJournalNameDup, "journal name "+in.Code+" already exists in this legal entity")
			}
			return scopeProblem(err)
		}
		if out, err = loadJournalName(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "journalname", id, invevents.JournalNameCreated, tp.TenantID, journalNameData(out))
	})
	return out, replayed, err
}

// GetJournalName returns one journal name.
func (s *Postgres) GetJournalName(ctx context.Context, id string) (domain.JournalName, error) {
	var out domain.JournalName
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadJournalName(ctx, tx, id, false)
		return err
	})
	return out, err
}

// JournalNameFilter narrows ListJournalNames.
type JournalNameFilter struct {
	LegalEntityID string
	JournalType   string
	Active        *bool
	Limit         int
}

// ListJournalNames returns journal names by legal entity then code.
func (s *Postgres) ListJournalNames(ctx context.Context, f JournalNameFilter) ([]domain.JournalName, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []domain.JournalName{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		limit := f.Limit
		if limit <= 0 {
			limit = 200
		}
		rows, err := tx.Query(ctx, `select `+journalNameColumns+` from inventory_journal_name
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or journal_type = $3) and ($4::boolean is null or active = $4)
			order by legal_entity_id, code limit $5`, tp.TenantID, f.LegalEntityID, f.JournalType, f.Active, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanJournalName(rows)
			if err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// JournalNamePatch holds the changeable fields of a journal name; nil keeps the
// stored value. The code, legal entity and journal type are fixed once journals
// may reference the name.
type JournalNamePatch struct {
	Description      *string
	RequiresApproval *bool
	OffsetAccountID  *string
	Active           *bool
}

// UpdateJournalName changes a journal name. The version must match (If-Match).
// A change applies to journals created or posted afterwards: a DRAFT journal
// already under the name meets the new approval rule when it is posted.
func (s *Postgres) UpdateJournalName(ctx context.Context, id string, version int, patch JournalNamePatch) (domain.JournalName, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.JournalName
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := loadJournalName(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if n.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "journal name changed since it was read; reread and retry")
		}
		if patch.Description != nil {
			n.Description = strings.TrimSpace(*patch.Description)
		}
		if patch.RequiresApproval != nil {
			n.RequiresApproval = *patch.RequiresApproval
		}
		if patch.OffsetAccountID != nil {
			n.OffsetAccountID = strings.TrimSpace(*patch.OffsetAccountID)
		}
		if patch.Active != nil {
			n.Active = *patch.Active
		}
		if err := n.Validate(); err != nil {
			return domainProblem(err)
		}
		var offset *string
		if n.OffsetAccountID != "" {
			offset = &n.OffsetAccountID
		}
		if _, err := tx.Exec(ctx, `update inventory_journal_name set description = $3, requires_approval = $4, offset_account_id = $5, active = $6,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			tp.TenantID, id, n.Description, n.RequiresApproval, offset, n.Active); err != nil {
			return err
		}
		if out, err = loadJournalName(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "journalname", id, invevents.JournalNameUpdated, tp.TenantID, journalNameData(out))
	})
	return out, err
}

// isUniqueOn reports a unique violation of a constraint whose name contains column.
func isUniqueOn(err error, column string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && strings.Contains(pgErr.ConstraintName, column)
}
