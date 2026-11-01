package store

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Problem codes of document splitting maintenance (FR-11.23).
const (
	CodeSplittingNotFound  = "LEDGER_SPLITTING_RULE_NOT_FOUND"
	CodeSplittingDuplicate = "LEDGER_SPLITTING_RULE_DUPLICATE"
)

const splittingColumns = `id::text, ledger_id::text, source_type, splitting_dimensions, coalesce(zero_balance_clearing_account, ''),
	default_splitting_value, active, version, created_at, updated_at`

func scanSplitting(row pgx.Row) (domain.SplittingRule, error) {
	var r domain.SplittingRule
	var def []byte
	err := row.Scan(&r.ID, &r.LedgerID, &r.SourceType, &r.SplittingDimensions, &r.ZeroBalanceClearingAccount, &def, &r.Active,
		&r.Version, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return r, err
	}
	r.DefaultSplittingValue = map[string]string{}
	return r, json.Unmarshal(def, &r.DefaultSplittingValue)
}

// ListSplittingRules returns the splitting rules of a ledger by source type.
func (s *Postgres) ListSplittingRules(ctx context.Context, ledgerID string) ([]domain.SplittingRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.SplittingRule{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+splittingColumns+` from splitting_rule where tenant_id = $1 and ledger_id = $2 order by source_type`,
			p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanSplitting(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetSplittingRule returns one rule or 404 LEDGER_SPLITTING_RULE_NOT_FOUND.
func (s *Postgres) GetSplittingRule(ctx context.Context, ledgerID, id string) (domain.SplittingRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.SplittingRule{}, err
	}
	var out domain.SplittingRule
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = scanSplitting(tx.QueryRow(ctx, `select `+splittingColumns+` from splitting_rule where tenant_id = $1 and ledger_id = $2 and id = $3`,
			p.TenantID, ledgerID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeSplittingNotFound, "splitting rule not found")
		}
		return err
	})
	return out, err
}

// PutSplittingRule creates or replaces a splitting rule. A replace checks version when it is
// above zero (412 LEDGER_VERSION_CONFLICT). One rule exists per ledger and source type (409
// LEDGER_SPLITTING_RULE_DUPLICATE). The clearing account must exist in the ledger's chart and be
// postable. Announces rules.changed (kind splitting_rule).
func (s *Postgres) PutSplittingRule(ctx context.Context, ledgerID, id string, version int, in domain.SplittingRule) (domain.SplittingRule, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.SplittingRule{}, false, err
	}
	var out domain.SplittingRule
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false)
		if err != nil {
			return err
		}
		problems := domain.ValidateSplittingRule(in, ledger.DimensionAttributes)
		if acct := in.ZeroBalanceClearingAccount; acct != "" {
			found, err := loadAccounts(ctx, tx, p.TenantID, ledger.ChartCode, []string{acct})
			if err != nil {
				return err
			}
			a, ok := found[acct]
			switch {
			case !ok || !a.Postable:
				problems["zeroBalanceClearingAccount"] = "must be a postable account of the ledger's chart"
			case !balanceSheetTypes[a.Type]:
				problems["zeroBalanceClearingAccount"] = "must be a balance-sheet account (asset, liability or equity)"
			case a.ReconType != "" && a.ReconType != domain.ReconNone:
				problems["zeroBalanceClearingAccount"] = "must not be a reconciliation account; those accept subledger postings only"
			}
		}
		if len(problems) > 0 {
			var vs []domain.Violation
			for field, msg := range problems {
				vs = append(vs, domain.Violation{Code: domain.CodeSplittingInvalid, Field: field, Message: msg})
			}
			sortViolations(vs)
			return &domain.RejectedError{Violations: vs}
		}
		if in.DefaultSplittingValue == nil {
			in.DefaultSplittingValue = map[string]string{}
		}
		def, err := json.Marshal(in.DefaultSplittingValue)
		if err != nil {
			return err
		}
		clearing := &in.ZeroBalanceClearingAccount
		if in.ZeroBalanceClearingAccount == "" {
			clearing = nil
		}
		current, err := scanSplitting(tx.QueryRow(ctx, `select `+splittingColumns+` from splitting_rule
			where tenant_id = $1 and ledger_id = $2 and id = $3 for update`, p.TenantID, ledgerID, id))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			created = true
			_, err = tx.Exec(ctx, `insert into splitting_rule (tenant_id, id, legal_entity_id, ledger_id, source_type, splitting_dimensions,
				zero_balance_clearing_account, default_splitting_value, active, created_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
				p.TenantID, id, ledger.LegalEntityID, ledgerID, in.SourceType, in.SplittingDimensions, clearing, def, in.Active, p.UserID)
		case err != nil:
			return err
		default:
			if version > 0 && version != current.Version {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the splitting rule changed since it was read; reload and retry")
			}
			if sameSplitting(current, in) {
				// A repeated PUT of the same content changes nothing and announces nothing.
				out = current
				return nil
			}
			_, err = tx.Exec(ctx, `update splitting_rule set source_type = $4, splitting_dimensions = $5, zero_balance_clearing_account = $6,
				default_splitting_value = $7, active = $8, version = version + 1, updated_at = now()
				where tenant_id = $1 and ledger_id = $2 and id = $3`,
				p.TenantID, ledgerID, id, in.SourceType, in.SplittingDimensions, clearing, def, in.Active)
		}
		if err != nil {
			if isUnique(err) {
				return httpx.NewProblem(http.StatusConflict, CodeSplittingDuplicate, "a splitting rule for this source type already exists on the ledger")
			}
			return err
		}
		out, err = scanSplitting(tx.QueryRow(ctx, `select `+splittingColumns+` from splitting_rule where tenant_id = $1 and ledger_id = $2 and id = $3`,
			p.TenantID, ledgerID, id))
		if err != nil {
			return err
		}
		return enqueueFor(ctx, tx, "splitting_rule", id, ledgerevents.RulesChanged, p.TenantID, ledgerevents.RulesChangedData{
			LedgerID: ledgerID, Kind: "splitting_rule", ID: id, Version: out.Version, Actor: p.UserID})
	})
	return out, created, err
}

// balanceSheetTypes are the account types a zero-balance clearing account may have (PRD
// splitting.zero_balance_clearing_account_id: a balance-sheet account).
var balanceSheetTypes = map[string]bool{"asset": true, "liability": true, "equity": true}

// sameSplitting reports whether a replacement would leave the stored rule as it is.
func sameSplitting(cur, in domain.SplittingRule) bool {
	return cur.SourceType == in.SourceType && slices.Equal(cur.SplittingDimensions, in.SplittingDimensions) &&
		cur.ZeroBalanceClearingAccount == in.ZeroBalanceClearingAccount && maps.Equal(cur.DefaultSplittingValue, in.DefaultSplittingValue) &&
		cur.Active == in.Active
}

// applySplitting runs the document splitting stage (FR-11.23) after derivation: the active rule
// of the entry's source type, if any, fills the splitting dimensions of lines that lack them and
// adds the zero-balance clearing lines. The clearing account is loaded into accounts so the
// later stages know it. Violations are returned for the caller to append to the validation result.
func applySplitting(ctx context.Context, tx pgx.Tx, tenantID string, ledger Ledger, accounts map[string]domain.Account,
	sourceType string, lines []domain.LineInput) ([]domain.LineInput, []domain.SplitTrace, []domain.Violation, error) {
	rule, err := scanSplitting(tx.QueryRow(ctx, `select `+splittingColumns+` from splitting_rule
		where tenant_id = $1 and ledger_id = $2 and source_type = $3 and active`, tenantID, ledger.ID, sourceType))
	if errors.Is(err, pgx.ErrNoRows) {
		return lines, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	out, traces, viol := domain.ApplySplitting(rule, ledger.Currency, lines)
	if acct := rule.ZeroBalanceClearingAccount; acct != "" {
		if _, known := accounts[acct]; !known {
			found, err := loadAccounts(ctx, tx, tenantID, ledger.ChartCode, []string{acct})
			if err != nil {
				return nil, nil, nil, err
			}
			for k, v := range found {
				accounts[k] = v
			}
		}
	}
	return out, traces, viol, nil
}
