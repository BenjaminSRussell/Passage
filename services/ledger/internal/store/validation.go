package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Problem codes of validation rule maintenance (FR-11.21).
const (
	CodeValidationRuleNotFound  = "LEDGER_VALIDATION_RULE_NOT_FOUND"
	CodeValidationRuleInvalid   = domain.CodeValidationRuleInvalid
	CodeValidationRuleDuplicate = "LEDGER_VALIDATION_RULE_NAME_DUPLICATE"
	CodeValidationRuleLimit     = "LEDGER_VALIDATION_RULE_LIMIT"
)

const validationColumns = `id::text, ledger_id::text, sequence, name, level, severity, prerequisite, checks, message, active,
	lock_version, created_at, updated_at`

func scanValidationRule(row pgx.Row) (domain.ValidationRule, error) {
	var r domain.ValidationRule
	var pre, checks []byte
	err := row.Scan(&r.ID, &r.LedgerID, &r.Sequence, &r.Name, &r.Level, &r.Severity, &pre, &checks, &r.Message, &r.Active,
		&r.LockVersion, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(pre, &r.Prerequisite); err != nil {
		return r, err
	}
	if r.Prerequisite == nil {
		r.Prerequisite = []domain.RuleCondition{}
	}
	return r, json.Unmarshal(checks, &r.Checks)
}

func validationRuleNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeValidationRuleNotFound, "validation rule not found")
}

// ListValidationRules returns the rules of a ledger in evaluation order.
func (s *Postgres) ListValidationRules(ctx context.Context, ledgerID string) ([]domain.ValidationRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.ValidationRule{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		var err error
		out, err = queryValidationRules(ctx, tx, p.TenantID, ledgerID, false)
		return err
	})
	return out, err
}

func queryValidationRules(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, activeOnly bool) ([]domain.ValidationRule, error) {
	rows, err := tx.Query(ctx, `select `+validationColumns+` from validation_rule
		where tenant_id = $1 and ledger_id = $2 and (not $3 or active) order by sequence, name`, tenantID, ledgerID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ValidationRule{}
	for rows.Next() {
		r, err := scanValidationRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetValidationRule returns one rule.
func (s *Postgres) GetValidationRule(ctx context.Context, ledgerID, id string) (domain.ValidationRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.ValidationRule{}, err
	}
	var out domain.ValidationRule
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = scanValidationRule(tx.QueryRow(ctx, `select `+validationColumns+` from validation_rule
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledgerID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return validationRuleNotFound()
		}
		return err
	})
	return out, err
}

// PutValidationRule creates or replaces a rule. A replace checks lockVersion when
// it is above zero (412 LEDGER_VERSION_CONFLICT). Both announce rules.changed
// (kind validation_rule) in the same transaction.
func (s *Postgres) PutValidationRule(ctx context.Context, ledgerID, id string, lockVersion int, in domain.ValidationRule) (domain.ValidationRule, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.ValidationRule{}, false, err
	}
	var out domain.ValidationRule
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		if problems := domain.ValidateRule(in, ledger.DimensionAttributes); len(problems) > 0 {
			var vs []domain.Violation
			for field, msg := range problems {
				vs = append(vs, domain.Violation{Code: CodeValidationRuleInvalid, Field: field, Message: msg})
			}
			sortViolations(vs)
			return &domain.RejectedError{Violations: vs}
		}
		pre, err := json.Marshal(nonNilConditions(in.Prerequisite))
		if err != nil {
			return err
		}
		checks, err := json.Marshal(in.Checks)
		if err != nil {
			return err
		}
		var current int
		err = tx.QueryRow(ctx, `select lock_version from validation_rule where tenant_id = $1 and ledger_id = $2 and id = $3 for update`,
			p.TenantID, ledgerID, id).Scan(&current)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			created = true
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from validation_rule where tenant_id = $1 and ledger_id = $2`, p.TenantID, ledgerID).Scan(&n); err != nil {
				return err
			}
			if n >= domain.MaxValidationRules {
				return httpx.NewProblem(http.StatusConflict, CodeValidationRuleLimit, "a ledger has at most 200 validation rules")
			}
			_, err = tx.Exec(ctx, `insert into validation_rule (tenant_id, id, legal_entity_id, ledger_id, sequence, name, level, severity,
					prerequisite, checks, message, active, created_by, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
				p.TenantID, id, ledger.LegalEntityID, ledgerID, in.Sequence, in.Name, in.Level, in.Severity, pre, checks, in.Message, in.Active, p.UserID)
		case err != nil:
			return err
		default:
			if lockVersion > 0 && lockVersion != current {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the validation rule changed since it was read; reload and retry")
			}
			_, err = tx.Exec(ctx, `update validation_rule set sequence = $4, name = $5, level = $6, severity = $7, prerequisite = $8,
					checks = $9, message = $10, active = $11, updated_by = $12, lock_version = lock_version + 1, updated_at = now()
				where tenant_id = $1 and ledger_id = $2 and id = $3`,
				p.TenantID, ledgerID, id, in.Sequence, in.Name, in.Level, in.Severity, pre, checks, in.Message, in.Active, p.UserID)
		}
		if err != nil {
			if isUnique(err) {
				return httpx.NewProblem(http.StatusConflict, CodeValidationRuleDuplicate, "a validation rule with this name already exists on the ledger")
			}
			return err
		}
		out, err = scanValidationRule(tx.QueryRow(ctx, `select `+validationColumns+` from validation_rule
			where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledgerID, id))
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, "validation_rule", id, ledgerevents.RulesChanged, p.TenantID, ledgerevents.RulesChangedData{
			LedgerID: ledgerID, Kind: "validation_rule", ID: id, Version: out.LockVersion, Actor: p.UserID})
	})
	return out, created, err
}

func nonNilConditions(cs []domain.RuleCondition) []domain.RuleCondition {
	if cs == nil {
		return []domain.RuleCondition{}
	}
	return cs
}

// applyValidationRules runs the validation rule stage of the posting pipeline
// (FR-11.21) over the derived and split lines (FR-11.22, FR-11.23), after structure
// validation; LINE_COUNT and TOTAL_DEBIT include the lines document splitting added.
func applyValidationRules(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, in PostInput,
	accounts map[string]domain.Account, lines []domain.LineInput) (domain.RuleOutcome, error) {
	rules, err := queryValidationRules(ctx, tx, tenantID, ledger.ID, true)
	if err != nil || len(rules) == 0 {
		return domain.RuleOutcome{}, err
	}
	return domain.EvaluateRules(rules, domain.RuleHeader{SourceSystem: in.Source.System, SourceType: in.Source.Type,
		PostingDate: in.PostingDate, DocumentDate: in.DocumentDate}, accounts, lines), nil
}
