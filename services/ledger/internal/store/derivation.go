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
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Problem codes of derivation maintenance (FR-11.22).
const (
	CodeLookupNotFound      = "LEDGER_LOOKUP_TABLE_NOT_FOUND"
	CodeLookupInvalid       = "LEDGER_LOOKUP_TABLE_INVALID"
	CodeLookupNameDuplicate = "LEDGER_LOOKUP_TABLE_NAME_DUPLICATE"
	CodeLookupInUse         = "LEDGER_LOOKUP_TABLE_IN_USE"
	CodeStrategyNotFound    = "LEDGER_DERIVATION_STRATEGY_NOT_FOUND"
	CodeStrategyNotDraft    = "LEDGER_DERIVATION_STRATEGY_NOT_DRAFT"
	CodeStrategyInvalid     = domain.CodeDerivationInvalid
)

// ---- lookup tables ---------------------------------------------------------------

const lookupColumns = `t.id::text, t.ledger_id::text, t.name, t.source_fields, t.target, t.version, t.created_at, t.updated_at,
	(select count(*) from lookup_table_row r where r.tenant_id = t.tenant_id and r.lookup_table_id = t.id)`

func scanLookup(row pgx.Row) (domain.LookupTable, error) {
	var t domain.LookupTable
	err := row.Scan(&t.ID, &t.LedgerID, &t.Name, &t.SourceFields, &t.Target, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.RowCount)
	return t, err
}

func lookupNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeLookupNotFound, "lookup table not found")
}

// ListLookupTables returns the lookup tables of a ledger by name, without rows.
func (s *Postgres) ListLookupTables(ctx context.Context, ledgerID string) ([]domain.LookupTable, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.LookupTable{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+lookupColumns+` from lookup_table t where t.tenant_id = $1 and t.ledger_id = $2 order by t.name`,
			p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanLookup(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func loadLookupWithRows(ctx context.Context, tx pgx.Tx, tenantID, ledgerID, id string) (domain.LookupTable, error) {
	t, err := scanLookup(tx.QueryRow(ctx, `select `+lookupColumns+` from lookup_table t where t.tenant_id = $1 and t.ledger_id = $2 and t.id = $3`,
		tenantID, ledgerID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, lookupNotFound()
	}
	if err != nil {
		return t, err
	}
	rows, err := tx.Query(ctx, `select source_values, target_value, valid_from::text, valid_to::text from lookup_table_row
		where tenant_id = $1 and lookup_table_id = $2 order by row_no`, tenantID, id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	t.Rows = []domain.LookupRow{}
	for rows.Next() {
		var r domain.LookupRow
		if err := rows.Scan(&r.Source, &r.Target, &r.ValidFrom, &r.ValidTo); err != nil {
			return t, err
		}
		t.Rows = append(t.Rows, r)
	}
	return t, rows.Err()
}

// GetLookupTable returns one lookup table with its rows.
func (s *Postgres) GetLookupTable(ctx context.Context, ledgerID, id string) (domain.LookupTable, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.LookupTable{}, err
	}
	var out domain.LookupTable
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = loadLookupWithRows(ctx, tx, p.TenantID, ledgerID, id)
		return err
	})
	return out, err
}

// PutLookupTable creates or replaces a lookup table and all its rows. A replace
// checks version when it is above zero (412 LEDGER_VERSION_CONFLICT). The
// definition (source fields, target) of an existing table cannot change while a
// strategy step references it. Announces rules.changed (kind lookup_table).
func (s *Postgres) PutLookupTable(ctx context.Context, ledgerID, id string, version int, in domain.LookupTable) (domain.LookupTable, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.LookupTable{}, false, err
	}
	var out domain.LookupTable
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false)
		if err != nil {
			return err
		}
		if problems := domain.ValidateLookupTable(in, ledger.DimensionAttributes); len(problems) > 0 {
			var vs []domain.Violation
			for field, msg := range problems {
				vs = append(vs, domain.Violation{Code: CodeLookupInvalid, Field: field, Message: msg})
			}
			sortViolations(vs)
			return &domain.RejectedError{Violations: vs}
		}
		var current int
		var oldFields []string
		var oldTarget string
		err = tx.QueryRow(ctx, `select version, source_fields, target from lookup_table where tenant_id = $1 and ledger_id = $2 and id = $3 for update`,
			p.TenantID, ledgerID, id).Scan(&current, &oldFields, &oldTarget)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			created = true
			_, err = tx.Exec(ctx, `insert into lookup_table (tenant_id, id, legal_entity_id, ledger_id, name, source_fields, target, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, id, ledger.LegalEntityID, ledgerID, in.Name, in.SourceFields, in.Target, p.UserID)
		case err != nil:
			return err
		default:
			if version > 0 && version != current {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the lookup table changed since it was read; reload and retry")
			}
			if !equalStrings(oldFields, in.SourceFields) || oldTarget != in.Target {
				var used bool
				if err := tx.QueryRow(ctx, `select exists(select 1 from derivation_strategy d, jsonb_array_elements(d.steps) st
					where d.tenant_id = $1 and d.ledger_id = $2 and st->>'lookupTableId' = $3)`, p.TenantID, ledgerID, id).Scan(&used); err != nil {
					return err
				}
				if used {
					return httpx.NewProblem(http.StatusConflict, CodeLookupInUse,
						"a derivation strategy step uses this table; its source fields and target cannot change")
				}
			}
			_, err = tx.Exec(ctx, `update lookup_table set name = $4, source_fields = $5, target = $6, version = version + 1, updated_at = now()
				where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledgerID, id, in.Name, in.SourceFields, in.Target)
			if err == nil {
				_, err = tx.Exec(ctx, `delete from lookup_table_row where tenant_id = $1 and lookup_table_id = $2`, p.TenantID, id)
			}
		}
		if err != nil {
			if isUnique(err) {
				return httpx.NewProblem(http.StatusConflict, CodeLookupNameDuplicate, "a lookup table with this name already exists on the ledger")
			}
			return err
		}
		if len(in.Rows) > 0 {
			raw, err := json.Marshal(in.Rows)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into lookup_table_row (tenant_id, legal_entity_id, lookup_table_id, row_no, source_values, target_value, valid_from, valid_to)
				select $1, $2, $3, ord::int, array(select jsonb_array_elements_text(e->'source')), e->>'target', (e->>'validFrom')::date,
					nullif(e->>'validTo', '')::date
				from jsonb_array_elements($4::jsonb) with ordinality as t(e, ord)`, p.TenantID, ledger.LegalEntityID, id, string(raw)); err != nil {
				return err
			}
		}
		out, err = loadLookupWithRows(ctx, tx, p.TenantID, ledgerID, id)
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, "lookup_table", id, ledgerevents.RulesChanged, p.TenantID, ledgerevents.RulesChangedData{
			LedgerID: ledgerID, Kind: "lookup_table", ID: id, Version: out.Version, Actor: p.UserID})
	})
	return out, created, err
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortViolations(vs []domain.Violation) {
	for i := 1; i < len(vs); i++ {
		for j := i; j > 0 && vs[j].Field < vs[j-1].Field; j-- {
			vs[j], vs[j-1] = vs[j-1], vs[j]
		}
	}
}

// ---- strategies --------------------------------------------------------------------

const strategyColumns = `id::text, ledger_id::text, version, lock_version, name, status, steps, coalesce(activated_by, ''), activated_at, created_at, updated_at`

func scanStrategy(row pgx.Row) (domain.DerivationStrategy, error) {
	var d domain.DerivationStrategy
	var steps []byte
	err := row.Scan(&d.ID, &d.LedgerID, &d.Version, &d.LockVersion, &d.Name, &d.Status, &steps, &d.ActivatedBy, &d.ActivatedAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(steps, &d.Steps); err != nil {
		return d, err
	}
	for i := range d.Steps {
		if d.Steps[i].SourceFields == nil {
			d.Steps[i].SourceFields = []string{}
		}
	}
	return d, nil
}

func strategyNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeStrategyNotFound, "derivation strategy version not found")
}

// ListDerivationStrategies returns the versions of a ledger's strategy, newest first.
func (s *Postgres) ListDerivationStrategies(ctx context.Context, ledgerID string) ([]domain.DerivationStrategy, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.DerivationStrategy{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+strategyColumns+` from derivation_strategy where tenant_id = $1 and ledger_id = $2 order by version desc`,
			p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanStrategy(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDerivationStrategy returns one version.
func (s *Postgres) GetDerivationStrategy(ctx context.Context, ledgerID string, version int) (domain.DerivationStrategy, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.DerivationStrategy{}, err
	}
	var out domain.DerivationStrategy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = scanStrategy(tx.QueryRow(ctx, `select `+strategyColumns+` from derivation_strategy where tenant_id = $1 and ledger_id = $2 and version = $3`,
			p.TenantID, ledgerID, version))
		if errors.Is(err, pgx.ErrNoRows) {
			return strategyNotFound()
		}
		return err
	})
	return out, err
}

func strategyViolation(details []string) error {
	vs := make([]domain.Violation, 0, len(details))
	for _, d := range details {
		vs = append(vs, domain.Violation{Code: CodeStrategyInvalid, Field: "steps", Message: d})
	}
	return &domain.RejectedError{Violations: vs}
}

// PutDerivationStrategy creates or replaces a draft version. The version number
// is the caller's: a new number must be the next one (highest + 1) so versions
// stay dense. Step ids are assigned by the service (a draft keeps its own). Only drafts are replaced (409 LEDGER_DERIVATION_STRATEGY_NOT_DRAFT);
// a replace checks lockVersion when it is above zero (412).
func (s *Postgres) PutDerivationStrategy(ctx context.Context, ledgerID string, version, lockVersion int, in domain.DerivationStrategy) (domain.DerivationStrategy, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.DerivationStrategy{}, false, err
	}
	var out domain.DerivationStrategy
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		if details := domain.ValidateStrategy(in, ledger.DimensionAttributes, nil, false); len(details) > 0 {
			return strategyViolation(details)
		}
		var status string
		var current int
		var oldSteps []domain.DerivationStep
		err = tx.QueryRow(ctx, `select status, lock_version, steps from derivation_strategy where tenant_id = $1 and ledger_id = $2 and version = $3 for update`,
			p.TenantID, ledgerID, version).Scan(&status, &current, &oldSteps)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Step ids name the step in derived_by_step_ids, so they must be unique across
		// versions: a draft keeps the ids it already has, any other id (omitted, or
		// copied from another version) is replaced by a new one.
		known := map[string]bool{}
		for _, st := range oldSteps {
			known[st.ID] = true
		}
		for i := range in.Steps {
			if !known[in.Steps[i].ID] {
				in.Steps[i].ID = ids.New()
			}
		}
		raw, err := json.Marshal(in.Steps)
		if err != nil {
			return err
		}
		switch {
		case !exists:
			var highest int
			if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) from derivation_strategy where tenant_id = $1 and ledger_id = $2`,
				p.TenantID, ledgerID).Scan(&highest); err != nil {
				return err
			}
			if version != highest+1 {
				return &domain.RejectedError{Violations: []domain.Violation{{Code: CodeStrategyInvalid, Field: "version",
					Message: "a new version must be the next number after the highest existing one"}}}
			}
			created = true
			_, err = tx.Exec(ctx, `insert into derivation_strategy (tenant_id, id, legal_entity_id, ledger_id, version, name, status, steps, created_by)
				values ($1, $2, $3, $4, $5, $6, 'draft', $7::jsonb, $8)`, p.TenantID, ids.New(), ledger.LegalEntityID, ledgerID, version, in.Name, string(raw), p.UserID)
		default:
			if status != domain.StrategyDraft {
				return httpx.NewProblem(http.StatusConflict, CodeStrategyNotDraft, "only a draft version can be replaced; create the next version instead")
			}
			if lockVersion > 0 && lockVersion != current {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the derivation strategy changed since it was read; reload and retry")
			}
			_, err = tx.Exec(ctx, `update derivation_strategy set name = $4, steps = $5::jsonb, lock_version = lock_version + 1, updated_at = now()
				where tenant_id = $1 and ledger_id = $2 and version = $3`, p.TenantID, ledgerID, version, in.Name, string(raw))
		}
		if err != nil {
			return err
		}
		out, err = scanStrategy(tx.QueryRow(ctx, `select `+strategyColumns+` from derivation_strategy where tenant_id = $1 and ledger_id = $2 and version = $3`,
			p.TenantID, ledgerID, version))
		return err
	})
	return out, created, err
}

// ActivateDerivationStrategy validates a draft against the ledger and its lookup
// tables (BR-11.14, 422 LEDGER_DERIVATION_INVALID), retires the active version
// and activates this one. Activating the active version again is a no-op. It
// announces rules.changed in the same transaction.
func (s *Postgres) ActivateDerivationStrategy(ctx context.Context, ledgerID string, version, lockVersion int) (domain.DerivationStrategy, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.DerivationStrategy{}, err
	}
	var out domain.DerivationStrategy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		d, err := scanStrategy(tx.QueryRow(ctx, `select `+strategyColumns+` from derivation_strategy where tenant_id = $1 and ledger_id = $2 and version = $3 for update`,
			p.TenantID, ledgerID, version))
		if errors.Is(err, pgx.ErrNoRows) {
			return strategyNotFound()
		}
		if err != nil {
			return err
		}
		if d.Status == domain.StrategyActive {
			out = d
			return nil
		}
		if d.Status != domain.StrategyDraft {
			return httpx.NewProblem(http.StatusConflict, CodeStrategyNotDraft, "a retired version cannot be activated; create the next version instead")
		}
		if lockVersion > 0 && lockVersion != d.LockVersion {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
				"the derivation strategy changed since it was read; reload and retry")
		}
		tables := map[string]domain.LookupTable{}
		rows, err := tx.Query(ctx, `select `+lookupColumns+` from lookup_table t where t.tenant_id = $1 and t.ledger_id = $2`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		for rows.Next() {
			t, err := scanLookup(rows)
			if err != nil {
				rows.Close()
				return err
			}
			tables[t.ID] = t
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if details := domain.ValidateStrategy(d, ledger.DimensionAttributes, tables, true); len(details) > 0 {
			return strategyViolation(details)
		}
		if _, err := tx.Exec(ctx, `update derivation_strategy set status = 'retired', lock_version = lock_version + 1, updated_at = now()
			where tenant_id = $1 and ledger_id = $2 and status = 'active'`, p.TenantID, ledgerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update derivation_strategy set status = 'active', activated_by = $4, activated_at = now(),
				lock_version = lock_version + 1, updated_at = now()
			where tenant_id = $1 and ledger_id = $2 and version = $3`, p.TenantID, ledgerID, version, p.UserID); err != nil {
			return err
		}
		out, err = scanStrategy(tx.QueryRow(ctx, `select `+strategyColumns+` from derivation_strategy where tenant_id = $1 and ledger_id = $2 and version = $3`,
			p.TenantID, ledgerID, version))
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, "derivation_strategy", out.ID, ledgerevents.RulesChanged, p.TenantID, ledgerevents.RulesChangedData{
			LedgerID: ledgerID, Kind: "derivation_strategy", ID: out.ID, Version: out.Version, Actor: p.UserID})
	})
	return out, err
}

// ---- posting stage -------------------------------------------------------------------

// applyDerivation runs the derivation stage of the posting pipeline (FR-11.22):
// the ledger's active strategy fills dimensions on every line before structure
// validation. It returns the lines with their derived dimensions and the trace
// per line index. A ledger without an active strategy passes through unchanged.
func applyDerivation(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, accounts map[string]domain.Account,
	date time.Time, lines []domain.LineInput) ([]domain.LineInput, map[int][]domain.DerivationTrace, error) {
	d, err := scanStrategy(tx.QueryRow(ctx, `select `+strategyColumns+` from derivation_strategy
		where tenant_id = $1 and ledger_id = $2 and status = 'active'`, tenantID, ledger.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return lines, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var tableIDs []string
	for _, st := range d.Steps {
		if st.Kind == domain.StepTableLookup {
			tableIDs = append(tableIDs, st.LookupTableID)
		}
	}
	rows := map[string][]domain.LookupRow{}
	if len(tableIDs) > 0 {
		rs, err := tx.Query(ctx, `select lookup_table_id::text, source_values, target_value, valid_from::text, valid_to::text from lookup_table_row
			where tenant_id = $1 and lookup_table_id = any($2::uuid[]) and valid_from <= $3::text::date and (valid_to is null or valid_to >= $3::text::date)`,
			tenantID, tableIDs, date.Format(time.DateOnly))
		if err != nil {
			return nil, nil, err
		}
		defer rs.Close()
		for rs.Next() {
			var id string
			var r domain.LookupRow
			if err := rs.Scan(&id, &r.Source, &r.Target, &r.ValidFrom, &r.ValidTo); err != nil {
				return nil, nil, err
			}
			rows[id] = append(rows[id], r)
		}
		if err := rs.Err(); err != nil {
			return nil, nil, err
		}
	}
	out := make([]domain.LineInput, len(lines))
	traces := map[int][]domain.DerivationTrace{}
	for i, in := range lines {
		acct := accounts[in.Account]
		if acct.Number == "" {
			acct = domain.Account{Number: in.Account}
		}
		dims, trace := domain.Derive(d.Steps, rows, date, acct, in.Dimensions)
		in.Dimensions = dims
		if len(trace) > 0 {
			in.DerivedBy = domain.StepIDs(trace)
			traces[i] = trace
		}
		out[i] = in
	}
	return out, traces, nil
}
