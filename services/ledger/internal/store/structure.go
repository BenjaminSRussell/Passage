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
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

const structureColumns = `id::text, name, version, priority, effective_from::text, status, segments, account_filter, constraints`

const dimValueColumns = `attribute, value, name, active_from::text, active_to::text, suspended`

// scanStructure reads one account_structure row; extra destinations follow the
// standard columns.
func scanStructure(row pgx.Row, extra ...any) (domain.Structure, error) {
	var s domain.Structure
	var from string
	var raw []byte
	dest := append([]any{&s.ID, &s.Name, &s.Version, &s.Priority, &from, &s.Status, &s.Segments, &s.AccountFilter, &raw}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.Structure{}, err
	}
	d, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return domain.Structure{}, err
	}
	s.EffectiveFrom = d
	if err := json.Unmarshal(raw, &s.Constraints); err != nil {
		return domain.Structure{}, err
	}
	if s.Segments == nil {
		s.Segments = []string{}
	}
	if s.AccountFilter == nil {
		s.AccountFilter = []string{}
	}
	return s, nil
}

// queryStructures returns the ledger's structure versions ordered by name and
// version. onlyActive restricts the result to versions that may govern postings.
func queryStructures(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, onlyActive bool) ([]domain.Structure, error) {
	q := `select ` + structureColumns + ` from account_structure where tenant_id = $1 and ledger_id = $2`
	if onlyActive {
		q += ` and status = 'active'`
	}
	rows, err := tx.Query(ctx, q+` order by name, version`, tenantID, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Structure{}
	for rows.Next() {
		s, err := scanStructure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// findStructure looks a structure up by id within the tenant. found is false
// when no such id exists; the ledger it belongs to is returned for the caller.
func findStructure(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.Structure, string, bool, error) {
	var ledgerID string
	s, err := scanStructure(tx.QueryRow(ctx, `select `+structureColumns+`, ledger_id::text from account_structure
		where tenant_id = $1 and id = $2`, tenantID, id), &ledgerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Structure{}, "", false, nil
	}
	return s, ledgerID, err == nil, err
}

// ListStructures returns every version of the ledger's account structures.
func (s *Postgres) ListStructures(ctx context.Context, ledgerID string) ([]domain.Structure, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Structure
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = queryStructures(ctx, tx, p.TenantID, ledgerID, false)
		return err
	})
	return out, err
}

// GetStructure returns one structure version or 404 LEDGER_STRUCTURE_NOT_FOUND.
func (s *Postgres) GetStructure(ctx context.Context, ledgerID, id string) (domain.Structure, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.Structure{}, err
	}
	var out domain.Structure
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The ledger read applies the legal-entity scope, which the structure
		// table (tenant policy only) does not carry.
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		st, owner, found, err := findStructure(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if !found || owner != ledgerID {
			return httpx.NewProblem(http.StatusNotFound, domain.CodeStructureNotFound, "account structure not found")
		}
		out = st
		return nil
	})
	return out, err
}

// PutStructure creates or replaces a structure version (FR-11.3). Drafts are
// freely editable and become effective when set to active; an active version
// may only be retired, and a retired one is final, because postings were
// validated and their combinations hashed under it (FR-11.4). created reports
// whether the id is new, so the API can answer 201 or 200.
func (s *Postgres) PutStructure(ctx context.Context, ledgerID, id string, in domain.Structure) (domain.Structure, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.Structure{}, false, err
	}
	in.ID = id
	if in.Segments == nil {
		in.Segments = []string{}
	}
	if in.AccountFilter == nil {
		in.AccountFilter = []string{}
	}
	if in.Constraints == nil {
		in.Constraints = []domain.Constraint{}
	}
	raw, err := json.Marshal(in.Constraints)
	if err != nil {
		return domain.Structure{}, false, err
	}
	var out domain.Structure
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The ledger row lock serializes structure edits per ledger.
		l, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		if vs := domain.ValidateStructure(in, l.DimensionAttributes); len(vs) > 0 {
			return &domain.RejectedError{Violations: vs}
		}
		prev, owner, found, err := findStructure(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if found && owner != ledgerID {
			return httpx.NewProblem(http.StatusNotFound, domain.CodeStructureNotFound, "account structure not found")
		}
		created = !found
		if found && !domain.StructureEditAllowed(prev, in) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeStructureLocked,
				"an active or retired structure version is immutable; create a new version (an active one may only be retired)")
		}
		row := tx.QueryRow(ctx, `insert into account_structure (tenant_id, id, ledger_id, name, version, priority,
			effective_from, status, segments, account_filter, constraints)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb)
			on conflict (tenant_id, id) do update set name = excluded.name, version = excluded.version,
				priority = excluded.priority, effective_from = excluded.effective_from, status = excluded.status,
				segments = excluded.segments, account_filter = excluded.account_filter,
				constraints = excluded.constraints, updated_at = now()
			returning `+structureColumns,
			p.TenantID, id, ledgerID, in.Name, in.Version, in.Priority, in.EffectiveFrom, in.Status, in.Segments,
			in.AccountFilter, string(raw))
		out, err = scanStructure(row)
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeStructureVersionExists,
				"this structure name and version already exist on the ledger")
		}
		return err
	})
	if err != nil {
		return domain.Structure{}, false, err
	}
	return out, created, nil
}

func scanDimValue(row pgx.Row, extra ...any) (domain.DimensionValue, error) {
	var v domain.DimensionValue
	var from, to *string
	dest := append([]any{&v.Attribute, &v.Value, &v.Name, &from, &to, &v.Suspended}, extra...)
	if err := row.Scan(dest...); err != nil {
		return domain.DimensionValue{}, err
	}
	var err error
	if v.ActiveFrom, err = parseOptionalDate(from); err != nil {
		return domain.DimensionValue{}, err
	}
	if v.ActiveTo, err = parseOptionalDate(to); err != nil {
		return domain.DimensionValue{}, err
	}
	return v, nil
}

func parseOptionalDate(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func queryDimensionValues(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string) ([]domain.DimensionValue, error) {
	rows, err := tx.Query(ctx, `select `+dimValueColumns+` from dimension_value
		where tenant_id = $1 and ledger_id = $2 order by attribute, value`, tenantID, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.DimensionValue{}
	for rows.Next() {
		v, err := scanDimValue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListDimensionValues returns the registered dimension values of a ledger.
func (s *Postgres) ListDimensionValues(ctx context.Context, ledgerID string) ([]domain.DimensionValue, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.DimensionValue
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = queryDimensionValues(ctx, tx, p.TenantID, ledgerID)
		return err
	})
	return out, err
}

// PutDimensionValue creates or replaces a registered value. created reports
// whether the value is new.
func (s *Postgres) PutDimensionValue(ctx context.Context, ledgerID string, in domain.DimensionValue) (domain.DimensionValue, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.DimensionValue{}, false, err
	}
	var out domain.DimensionValue
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false)
		if err != nil {
			return err
		}
		if vs := domain.ValidateDimensionValue(in, l.DimensionAttributes); len(vs) > 0 {
			return &domain.RejectedError{Violations: vs}
		}
		row := tx.QueryRow(ctx, `insert into dimension_value (tenant_id, ledger_id, attribute, value, name, active_from, active_to, suspended)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, ledger_id, attribute, value) do update set name = excluded.name,
				active_from = excluded.active_from, active_to = excluded.active_to, suspended = excluded.suspended,
				updated_at = now()
			returning `+dimValueColumns+`, (xmax = 0)`,
			p.TenantID, ledgerID, in.Attribute, in.Value, in.Name, in.ActiveFrom, in.ActiveTo, in.Suspended)
		var scanErr error
		out, scanErr = scanDimValue(row, &created)
		return scanErr
	})
	if err != nil {
		return domain.DimensionValue{}, false, err
	}
	return out, created, nil
}

// withStructures adds the ledger's active account structures (FR-11.3) and the
// registered values that the lines use (FR-11.2) to the posting rules r.
func withStructures(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, r domain.Rules, lines []domain.LineInput) (domain.Rules, error) {
	structs, err := queryStructures(ctx, tx, tenantID, ledgerID, true)
	if err != nil {
		return domain.Rules{}, err
	}
	r.Structures = structs
	r.DimensionValues = map[string]domain.DimensionValue{}
	used := usedValues(lines)
	if len(used) == 0 {
		return r, nil
	}
	rows, err := tx.Query(ctx, `select `+dimValueColumns+` from dimension_value
		where tenant_id = $1 and ledger_id = $2 and value = any($3)`, tenantID, ledgerID, used)
	if err != nil {
		return domain.Rules{}, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanDimValue(rows)
		if err != nil {
			return domain.Rules{}, err
		}
		r.DimensionValues[domain.ValueKey(v.Attribute, v.Value)] = v
	}
	return r, rows.Err()
}

// usedValues returns the distinct non-blank dimension values of the lines.
func usedValues(lines []domain.LineInput) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, l := range lines {
		for _, v := range l.Dimensions {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
