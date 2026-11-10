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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
)

// read runs fn in a tenant transaction; RLS and the explicit tenant filter both apply.
func (s *Postgres) read(ctx context.Context, fn func(tx pgx.Tx, tenantID string) error) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, p.TenantID) })
}

// ListRules lists rule versions, newest version first per code. An empty
// legalEntityID lists every legal entity in the caller's scope.
func (s *Postgres) ListRules(ctx context.Context, legalEntityID string) ([]Rule, error) {
	out := []Rule{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+ruleColumns+` from budget_control_rule
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			order by code, version desc limit $3`, tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetRule returns one rule version or BUD-0001.
func (s *Postgres) GetRule(ctx context.Context, id string) (Rule, error) {
	var r Rule
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		r, err = loadRule(ctx, tx, tenantID, id)
		return err
	})
	return r, err
}

// ListRegisterEntries lists posted entries of a rule version, newest first.
func (s *Postgres) ListRegisterEntries(ctx context.Context, ruleVersionID string) ([]RegisterEntry, error) {
	out := []RegisterEntry{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		if _, err := loadRule(ctx, tx, tenantID, ruleVersionID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, rule_version_id::text, entry_type, status, total::text, posted_at, lines
			from budget_register_entry where tenant_id = $1 and rule_version_id = $2 order by posted_at desc, id limit $3`,
			tenantID, ruleVersionID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e RegisterEntry
			var total string
			var postedAt time.Time
			var lines []byte
			if err := rows.Scan(&e.ID, &e.RuleVersionID, &e.EntryType, &e.Status, &total, &postedAt, &lines); err != nil {
				return err
			}
			e.Total, e.PostedAt = parseDec(total), stamp(postedAt)
			if err := json.Unmarshal(lines, &e.Lines); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// Available lists the control combinations of a rule version with their
// available funds (FR-38.4 step 5). dim and span narrow the list when set.
func (s *Postgres) Available(ctx context.Context, ruleVersionID, dim, span string) ([]BalanceView, error) {
	out := []BalanceView{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		if _, err := loadRule(ctx, tx, tenantID, ruleVersionID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, rule_version_id::text, control_dimension_key, time_span_key, budget_total::text, actuals::text,
			encumbrances::text, pre_encumbrances::text, reservations::text, version from budget_control_balance
			where tenant_id = $1 and rule_version_id = $2 and ($3 = '' or control_dimension_key = $3) and ($4 = '' or time_span_key = $4)
			order by control_dimension_key, time_span_key limit $5`, tenantID, ruleVersionID, dim, span, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b BalanceView
			var budget, actuals, enc, pre, res string
			if err := rows.Scan(&b.ID, &b.RuleVersionID, &b.ControlDimensionKey, &b.TimeSpanKey, &budget, &actuals, &enc, &pre, &res, &b.Version); err != nil {
				return err
			}
			bal := domain.Balance{Budget: parseDec(budget), Actuals: parseDec(actuals), Encumbrances: parseDec(enc), PreEncumbrances: parseDec(pre), Reservations: parseDec(res)}
			b.BudgetTotal, b.Actuals, b.Encumbrances, b.PreEncumbrances, b.Reservations, b.Available = bal.Budget, bal.Actuals, bal.Encumbrances, bal.PreEncumbrances, bal.Reservations, bal.Available()
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

const encumbranceColumns = `e.id::text, b.rule_version_id::text, b.control_dimension_key, b.time_span_key, e.source_type, e.source_document_id::text,
	e.source_line_id::text, e.reserved::text, e.relieved::text, e.released::text, e.status, e.lines, e.created_at, e.stage`

func scanEncumbrance(row pgx.Row) (EncumbranceView, error) {
	var e EncumbranceView
	var reserved, relieved, released string
	var history []byte
	var created time.Time
	if err := row.Scan(&e.ID, &e.RuleVersionID, &e.ControlDimensionKey, &e.TimeSpanKey, &e.SourceType, &e.SourceDocumentID,
		&e.SourceLineID, &reserved, &relieved, &released, &e.Status, &history, &created, &e.Stage); err != nil {
		return e, err
	}
	e.Reserved, e.Relieved, e.Released, e.CreatedAt = parseDec(reserved), parseDec(relieved), parseDec(released), stamp(created)
	e.Remaining = domain.Release(e.Reserved, e.Relieved, e.Released)
	e.History = []historyEntry{}
	return e, json.Unmarshal(history, &e.History)
}

// ListEncumbrances lists reservations of a rule version, newest first. status
// narrows the list when set.
func (s *Postgres) ListEncumbrances(ctx context.Context, ruleVersionID, status string) ([]EncumbranceView, error) {
	out := []EncumbranceView{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		if _, err := loadRule(ctx, tx, tenantID, ruleVersionID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+encumbranceColumns+` from encumbrance e
			join budget_control_balance b on b.tenant_id = e.tenant_id and b.id = e.balance_id
			where e.tenant_id = $1 and b.rule_version_id = $2 and ($3 = '' or e.status = $3)
			order by e.created_at desc, e.id limit $4`, tenantID, ruleVersionID, status, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEncumbrance(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// GetEncumbrance returns one reservation with its history, or BUD-0002.
func (s *Postgres) GetEncumbrance(ctx context.Context, id string) (EncumbranceView, error) {
	var e EncumbranceView
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		e, err = scanEncumbrance(tx.QueryRow(ctx, `select `+encumbranceColumns+` from encumbrance e
			join budget_control_balance b on b.tenant_id = e.tenant_id and b.id = e.balance_id
			where e.tenant_id = $1 and e.id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeEncumbranceNotFound, "encumbrance not found")
		}
		return err
	})
	return e, err
}

const checkColumns = `id::text, rule_version_id::text, document_id::text, document_type, result, overridden, checked_at, lines`

func scanCheck(row pgx.Row) (CheckResponse, error) {
	var c CheckResponse
	var checked time.Time
	var lines []byte
	if err := row.Scan(&c.ID, &c.RuleVersionID, &c.DocumentID, &c.DocumentType, &c.Result, &c.Overridden, &checked, &lines); err != nil {
		return c, err
	}
	c.CheckedAt = stamp(checked)
	return c, json.Unmarshal(lines, &c.Lines)
}

// ListChecks lists recorded checks of a rule version, newest first.
// documentID narrows the list to one document's checks when set.
func (s *Postgres) ListChecks(ctx context.Context, ruleVersionID, documentID string) ([]CheckResponse, error) {
	out := []CheckResponse{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		if _, err := loadRule(ctx, tx, tenantID, ruleVersionID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+checkColumns+` from budget_check_result
			where tenant_id = $1 and rule_version_id = $2 and ($3 = '' or document_id::text = $3)
			order by checked_at desc, id limit $4`, tenantID, ruleVersionID, documentID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCheck(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetCheck returns one recorded check, or BUD-0004.
func (s *Postgres) GetCheck(ctx context.Context, id string) (CheckResponse, error) {
	var c CheckResponse
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		c, err = scanCheck(tx.QueryRow(ctx, `select `+checkColumns+` from budget_check_result where tenant_id = $1 and id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeCheckNotFound, "budget check not found")
		}
		return err
	})
	return c, err
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
