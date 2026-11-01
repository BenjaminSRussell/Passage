package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

const advancedColumns = `id::text, ledger_id::text, structure_id::text, name, priority, accounts, dimensions, segments, constraints,
	lock_version, created_at, updated_at`

func scanAdvancedRule(row pgx.Row) (domain.AdvancedRule, error) {
	var r domain.AdvancedRule
	var dims, cons []byte
	err := row.Scan(&r.ID, &r.LedgerID, &r.StructureID, &r.Name, &r.Priority, &r.Accounts, &dims, &r.Segments, &cons,
		&r.LockVersion, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(dims, &r.Dimensions); err != nil {
		return r, err
	}
	if err := json.Unmarshal(cons, &r.Constraints); err != nil {
		return r, err
	}
	if r.Accounts == nil {
		r.Accounts = []string{}
	}
	if r.Dimensions == nil {
		r.Dimensions = map[string]string{}
	}
	if r.Constraints == nil {
		r.Constraints = []domain.Constraint{}
	}
	return r, nil
}

func advancedRuleNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeAdvancedRuleNotFound, "advanced rule not found")
}

// queryAdvancedRules returns the ledger's advanced rules by structure, priority and name.
// A non-empty structureID restricts the result to one structure version.
func queryAdvancedRules(ctx context.Context, tx pgx.Tx, tenantID, ledgerID, structureID string) ([]domain.AdvancedRule, error) {
	return scanAdvancedRules(tx.Query(ctx, `select `+advancedColumns+` from advanced_rule
		where tenant_id = $1 and ledger_id = $2 and ($3 = '' or structure_id = nullif($3, '')::uuid)
		order by structure_id, priority, name`, tenantID, ledgerID, structureID))
}

func scanAdvancedRules(rows pgx.Rows, err error) ([]domain.AdvancedRule, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.AdvancedRule{}
	for rows.Next() {
		r, err := scanAdvancedRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// attachAdvancedRules fills AdvancedRules of the given (active) structures so posting applies
// them. It runs once per posting, in one query over the rules of those versions only (the
// rules of drafts and retired versions are not read), and the lines are matched in memory.
func attachAdvancedRules(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, structs []domain.Structure) error {
	if len(structs) == 0 {
		return nil
	}
	at := make(map[string]int, len(structs))
	versions := make([]string, 0, len(structs))
	for i, st := range structs {
		at[st.ID] = i
		versions = append(versions, st.ID)
	}
	rules, err := scanAdvancedRules(tx.Query(ctx, `select `+advancedColumns+` from advanced_rule
		where tenant_id = $1 and ledger_id = $2 and structure_id = any($3::uuid[])
		order by structure_id, priority, name`, tenantID, ledgerID, versions))
	if err != nil {
		return err
	}
	for _, r := range rules {
		if i, ok := at[r.StructureID]; ok {
			structs[i].AdvancedRules = append(structs[i].AdvancedRules, r)
		}
	}
	return nil
}

// ListAdvancedRules returns the advanced rules of a ledger, optionally of one structure version.
func (s *Postgres) ListAdvancedRules(ctx context.Context, ledgerID, structureID string) ([]domain.AdvancedRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.AdvancedRule{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		var err error
		out, err = queryAdvancedRules(ctx, tx, p.TenantID, ledgerID, structureID)
		return err
	})
	return out, err
}

// GetAdvancedRule returns one advanced rule.
func (s *Postgres) GetAdvancedRule(ctx context.Context, ledgerID, id string) (domain.AdvancedRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.AdvancedRule{}, err
	}
	var out domain.AdvancedRule
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = scanAdvancedRule(tx.QueryRow(ctx, `select `+advancedColumns+` from advanced_rule
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledgerID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return advancedRuleNotFound()
		}
		return err
	})
	return out, err
}

// PutAdvancedRule creates or replaces an advanced rule of a structure version (FR-11.16). The
// version must be a draft (409 LEDGER_STRUCTURE_LOCKED otherwise), because postings validated
// under an active version are hashed with it (FR-11.4). A replace checks lockVersion when it
// is above zero (412 LEDGER_VERSION_CONFLICT) and cannot move the rule to another structure.
// A repeated PUT of the stored content changes nothing and announces nothing, also once the
// version is active, so a retried request is idempotent. A change announces rules.changed
// (kind advanced_rule) in the same transaction. The ledger row lock serialises the save with
// the structure's activation.
func (s *Postgres) PutAdvancedRule(ctx context.Context, ledgerID, id string, lockVersion int, in domain.AdvancedRule) (domain.AdvancedRule, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.AdvancedRule{}, false, err
	}
	if in.Accounts == nil {
		in.Accounts = []string{}
	}
	if in.Dimensions == nil {
		in.Dimensions = map[string]string{}
	}
	if in.Constraints == nil {
		in.Constraints = []domain.Constraint{}
	}
	var out domain.AdvancedRule
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		st, owner, found, err := findStructure(ctx, tx, p.TenantID, in.StructureID)
		if err != nil {
			return err
		}
		if !found || owner != ledgerID {
			return httpx.NewProblem(http.StatusNotFound, domain.CodeStructureNotFound, "account structure not found")
		}
		current, err := scanAdvancedRule(tx.QueryRow(ctx, `select `+advancedColumns+` from advanced_rule
			where tenant_id = $1 and ledger_id = $2 and id = $3 for update`, p.TenantID, ledgerID, id))
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if exists {
			if current.StructureID != in.StructureID {
				return &domain.RejectedError{Violations: []domain.Violation{{Code: domain.CodeAdvancedRuleInvalid, Field: "structureId",
					Message: "a rule cannot move to another structure version"}}}
			}
			if lockVersion > 0 && lockVersion != current.LockVersion {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the advanced rule changed since it was read; reload and retry")
			}
			if sameAdvancedRule(current, in) {
				out = current
				return nil
			}
		}
		if vs := domain.ValidateAdvancedRule(in, st, ledger.DimensionAttributes); len(vs) > 0 {
			sortViolations(vs)
			return &domain.RejectedError{Violations: vs}
		}
		if st.Status != domain.StructureDraft {
			return httpx.NewProblem(http.StatusConflict, domain.CodeStructureLocked,
				"advanced rules can change only while their account structure version is a draft; create a new version")
		}
		dims, err := json.Marshal(in.Dimensions)
		if err != nil {
			return err
		}
		cons, err := json.Marshal(in.Constraints)
		if err != nil {
			return err
		}
		if exists {
			_, err = tx.Exec(ctx, `update advanced_rule set name = $4, priority = $5, accounts = $6, dimensions = $7, segments = $8,
					constraints = $9, updated_by = $10, lock_version = lock_version + 1, updated_at = now()
				where tenant_id = $1 and ledger_id = $2 and id = $3`,
				p.TenantID, ledgerID, id, in.Name, in.Priority, in.Accounts, dims, in.Segments, cons, p.UserID)
		} else {
			created = true
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from advanced_rule where tenant_id = $1 and structure_id = $2`,
				p.TenantID, in.StructureID).Scan(&n); err != nil {
				return err
			}
			if n >= domain.MaxAdvancedRules {
				return httpx.NewProblem(http.StatusConflict, domain.CodeAdvancedRuleLimit,
					fmt.Sprintf("a structure version has at most %d advanced rules", domain.MaxAdvancedRules))
			}
			_, err = tx.Exec(ctx, `insert into advanced_rule (tenant_id, id, legal_entity_id, ledger_id, structure_id, name, priority,
					accounts, dimensions, segments, constraints, created_by, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)`,
				p.TenantID, id, ledger.LegalEntityID, ledgerID, in.StructureID, in.Name, in.Priority, in.Accounts, dims, in.Segments, cons, p.UserID)
		}
		if err != nil {
			if isUnique(err) {
				return httpx.NewProblem(http.StatusConflict, domain.CodeAdvancedRuleDuplicate,
					"an advanced rule with this name already exists on the structure version, or the id is taken")
			}
			return err
		}
		out, err = scanAdvancedRule(tx.QueryRow(ctx, `select `+advancedColumns+` from advanced_rule
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledgerID, id))
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, "advanced_rule", id, ledgerevents.RulesChanged, p.TenantID, ledgerevents.RulesChangedData{
			LedgerID: ledgerID, Kind: "advanced_rule", ID: id, Version: out.LockVersion, Actor: p.UserID})
	})
	return out, created, err
}

// sameAdvancedRule reports whether a replacement would leave the stored rule as it is.
func sameAdvancedRule(cur, in domain.AdvancedRule) bool {
	if cur.Name != in.Name || cur.Priority != in.Priority || !slices.Equal(cur.Accounts, in.Accounts) ||
		!maps.Equal(cur.Dimensions, in.Dimensions) || !slices.Equal(cur.Segments, in.Segments) || len(cur.Constraints) != len(in.Constraints) {
		return false
	}
	for i, c := range cur.Constraints {
		d := in.Constraints[i]
		if c.Segment != d.Segment || !slices.Equal(c.Allowed, d.Allowed) || !maps.Equal(c.Under, d.Under) {
			return false
		}
	}
	return true
}
