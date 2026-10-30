package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// RunSpec is a statement run request after validation at the API edge.
type RunSpec struct {
	Code              string
	Version           int // 0 means the latest published version
	LedgerID          string
	LegalEntityID     string
	CurrencyType      string
	PeriodYear        int
	PeriodNo          int
	IncludeAdjustment bool
	Calendar          []domain.Period
	RequestedBy       string
	IdempotencyKey    string
	RequestHash       string
}

// Run is a stored statement run (FR-15.6). Replayed is true when the run was
// returned for a repeated Idempotency-Key instead of being evaluated again.
type Run struct {
	ID                string           `json:"id"`
	DefinitionCode    string           `json:"definitionCode"`
	DefinitionVersion int              `json:"definitionVersion"`
	LedgerID          string           `json:"ledgerId"`
	CurrencyType      string           `json:"currencyType"`
	PeriodYear        int              `json:"periodYear"`
	PeriodNo          int              `json:"periodNo"`
	Status            string           `json:"status"`
	HWMByLedger       map[string]int64 `json:"hwmByLedger"`
	ResultHash        string           `json:"resultHash"`
	CellCount         int              `json:"cellCount"`
	RequestedBy       string           `json:"requestedBy"`
	StartedAt         time.Time        `json:"startedAt"`
	DurationMS        int              `json:"durationMs"`
	// Checks are the warnings recorded with the run, such as REPORT-0015 for
	// PL-FUNC lines without a functional area (FR-15.14). Never nil.
	Checks   []domain.Check `json:"checks"`
	Replayed bool           `json:"-"`
}

// LagRow reports how far the read model has advanced for one ledger (FR-15.1).
type LagRow struct {
	LedgerID        string    `json:"ledgerId"`
	PostingSequence int64     `json:"postingSequence"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// CreateRun evaluates a published definition over the reporting read model and
// stores the run and its cells in one transaction. A repeated Idempotency-Key
// with the same request returns the stored run; a different request is refused
// (idempotency_key_reuse).
func (s *Postgres) CreateRun(ctx context.Context, spec RunSpec) (Run, error) {
	var run Run
	started := time.Now()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if spec.IdempotencyKey != "" {
			var id, hash string
			err := tx.QueryRow(ctx, `select id::text, request_hash from statement_run where idempotency_key=$1`, spec.IdempotencyKey).Scan(&id, &hash)
			if err == nil {
				if hash != spec.RequestHash {
					return problem(422, CodeIdempotencyReuse, "Idempotency-Key was used for a different request")
				}
				run, err = getRunTx(ctx, tx, id)
				run.Replayed = true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		def, _, err := loadDefinitionTx(ctx, tx, spec.Code, spec.Version)
		if err != nil {
			return err
		}
		cal, err := domain.NewCalendar(spec.Calendar)
		if err != nil {
			return problem(422, domain.CodePeriodNotFound, err.Error())
		}
		at, ok := cal.Index(spec.PeriodYear, spec.PeriodNo)
		if !ok {
			return problem(422, domain.CodePeriodNotFound, fmt.Sprintf("period %04d-%02d does not exist in ledger %s", spec.PeriodYear, spec.PeriodNo, spec.LedgerID))
		}
		entity, err := requireLedgerEntityTx(ctx, tx, spec.LedgerID, spec.LegalEntityID)
		if err != nil {
			return err
		}
		balances, hwm, err := loadBalancesTx(ctx, tx, spec.LedgerID, def.CurrencyTypes(spec.CurrencyType), cal)
		if err != nil {
			return err
		}
		in := domain.Input{Definition: def, LedgerID: spec.LedgerID, CurrencyType: spec.CurrencyType,
			IncludeAdjustmentPeriods: spec.IncludeAdjustment, Calendar: cal, At: at, Balances: balances}
		res, err := domain.Evaluate(in)
		if err != nil {
			return err
		}
		for _, c := range res.Cells {
			if err := c.CheckDrill(); err != nil {
				return fmt.Errorf("cell %s/%s: %w", c.Row, c.Column, err)
			}
		}
		// A cash flow that does not reconcile fails the run (BR-15.5); warnings
		// are stored with it (FR-15.14).
		if err := domain.CheckCashFlow(in, res); err != nil {
			return err
		}
		checks := domain.RunChecks(def, res)
		checksJSON, err := json.Marshal(checks)
		if err != nil {
			return err
		}
		hwmJSON, err := json.Marshal(hwm)
		if err != nil {
			return err
		}
		hash, err := hex.DecodeString(res.Hash)
		if err != nil {
			return err
		}
		run = Run{
			ID: ids.New(), DefinitionCode: def.Code, DefinitionVersion: def.Version, LedgerID: spec.LedgerID,
			CurrencyType: spec.CurrencyType, PeriodYear: spec.PeriodYear, PeriodNo: spec.PeriodNo, Status: "completed",
			HWMByLedger: hwm, ResultHash: res.Hash, CellCount: len(res.Cells), RequestedBy: spec.RequestedBy, StartedAt: started,
			Checks: checks,
		}
		_, err = tx.Exec(ctx, `insert into statement_run(tenant_id, id, definition_code, definition_version, ledger_id, currency_type,
			period_year, period_no, include_adjustment, status, hwm, result_hash, cell_count, requested_by, request_hash, idempotency_key,
			started_at, completed_at, duration_ms, checks, legal_entity_id)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,'completed',$10,$11,$12,$13,$14,$15,$16,now(),$17,$18,nullif($19,'')::uuid)`,
			tenantID(ctx), run.ID, def.Code, def.Version, spec.LedgerID, spec.CurrencyType, spec.PeriodYear, spec.PeriodNo,
			spec.IncludeAdjustment, hwmJSON, hash, len(res.Cells), spec.RequestedBy, spec.RequestHash, nullable(spec.IdempotencyKey),
			started, int(time.Since(started).Milliseconds()), checksJSON, entity)
		if err != nil {
			return err
		}
		for i, c := range res.Cells {
			keys, err := json.Marshal(c.Keys)
			if err != nil {
				return err
			}
			value := "0"
			if !c.Blank {
				value = c.Value.StringFixed(6)
			}
			if _, err := tx.Exec(ctx, `insert into statement_cell(tenant_id, run_id, row_code, column_code, value, blank, computed, note, balance_keys, line_count, position)
				values($1,$2,$3,$4,$5::numeric,$6,$7,$8,$9,$10,$11)`,
				tenantID(ctx), run.ID, c.Row, c.Column, value, c.Blank, c.Computed, c.Note, keys, len(c.Keys), i); err != nil {
				return err
			}
		}
		run.DurationMS = int(time.Since(started).Milliseconds())
		if _, err := tx.Exec(ctx, `update statement_run set duration_ms=$2 where id=$1`, run.ID, run.DurationMS); err != nil {
			return err
		}
		period := fmt.Sprintf("%04d-%02d", spec.PeriodYear, spec.PeriodNo)
		if err := enqueue(ctx, tx, "statement", run.ID, events.StatementGenerated, tenantID(ctx), events.StatementGeneratedData{
			RunID: run.ID, DefinitionCode: def.Code, DefinitionVersion: def.Version, LedgerID: spec.LedgerID, Period: period,
			CurrencyType: spec.CurrencyType, HWMByLedger: hwm, ResultHash: res.Hash, Outputs: []events.OutputRef{},
			Parameters: map[string]string{"period": period},
		}); err != nil {
			return err
		}
		return recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditStatementRun, LedgerID: spec.LedgerID, LegalEntityID: entity, Period: period,
			ActorID: spec.RequestedBy, ObjectRef: "run:" + run.ID, Details: map[string]any{
				"definition": def.Code, "version": def.Version, "currencyType": spec.CurrencyType,
				"hwm": hwm[spec.LedgerID], "resultHash": res.Hash, "cells": len(res.Cells),
			}})
	})
	return run, err
}

// GetRun returns a stored run.
func (s *Postgres) GetRun(ctx context.Context, id string) (Run, error) {
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = getRunTx(ctx, tx, id)
		return err
	})
	return run, err
}

// MaxRunPage is the largest page ListRuns returns.
const MaxRunPage = 200

// ListRuns returns the newest runs first, at most limit (1..MaxRunPage), and
// only runs of definition when it is not empty (report library, FR-15.8).
func (s *Postgres) ListRuns(ctx context.Context, definition string, limit int) ([]Run, error) {
	if limit < 1 || limit > MaxRunPage {
		limit = MaxRunPage
	}
	out := []Run{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from statement_run
			where ($1 = '' or definition_code = $1) order by started_at desc, id limit $2`, definition, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}

// ListCells returns every cell of a run in evaluation order.
func (s *Postgres) ListCells(ctx context.Context, runID string) ([]domain.Cell, error) {
	var out []domain.Cell
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = listCellsTx(ctx, tx, runID)
		return err
	})
	return out, err
}

func listCellsTx(ctx context.Context, tx pgx.Tx, runID string) ([]domain.Cell, error) {
	rows, err := tx.Query(ctx, `select row_code, column_code, value::text, blank, computed, note, balance_keys::text
		from statement_cell where run_id=$1 order by position`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Cell
	for rows.Next() {
		c, err := scanCell(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCell returns one cell of a run (drill level 1 input, BR-15.4).
func (s *Postgres) GetCell(ctx context.Context, runID, row, column string) (domain.Cell, error) {
	var cell domain.Cell
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		cell, err = scanCell(tx.QueryRow(ctx, `select row_code, column_code, value::text, blank, computed, note, balance_keys::text
			from statement_cell where run_id=$1 and row_code=$2 and column_code=$3`, runID, row, column))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeCellNotFound, fmt.Sprintf("cell %s/%s does not exist in run %s", row, column, runID))
		}
		return err
	})
	return cell, err
}

// Lag returns the reporting high-water mark per ledger (FR-15.1).
func (s *Postgres) Lag(ctx context.Context) ([]LagRow, error) {
	var out []LagRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select ledger_id::text, posting_sequence, updated_at from reporting_hwm order by ledger_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LagRow
			if err := rows.Scan(&l.LedgerID, &l.PostingSequence, &l.UpdatedAt); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

func loadDefinitionTx(ctx context.Context, tx pgx.Tx, code string, version int) (domain.Definition, int, error) {
	var def domain.Definition
	var body []byte
	var err error
	if version > 0 {
		err = tx.QueryRow(ctx, `select body from report_definition where code=$1 and status='published' and version=$2`, code, version).Scan(&body)
	} else {
		err = tx.QueryRow(ctx, `select body from report_definition where code=$1 and status='published' order by version desc limit 1`, code).Scan(&body)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return def, 0, problem(422, CodeNotPublished, fmt.Sprintf("definition %s has no published version", code))
	}
	if err != nil {
		return def, 0, err
	}
	if err := json.Unmarshal(body, &def); err != nil {
		return def, 0, err
	}
	return def, def.Version, nil
}

// loadBalancesTx reads the read model for one ledger and the given currency
// types, together with the ledger's high-water mark, in a single statement so
// both come from the same snapshot (BR-15.6: the run reflects exactly the
// postings up to the HWM it records). Balances outside the calendar's years are
// not read; those inside the years but not in the calendar cannot be placed in a
// column and are skipped.
func loadBalancesTx(ctx context.Context, tx pgx.Tx, ledgerID string, currencies []string, cal domain.Calendar) ([]domain.Balance, map[string]int64, error) {
	ps := cal.Periods()
	if len(ps) == 0 {
		return nil, nil, problem(422, domain.CodePeriodNotFound, "calendar is empty")
	}
	rows, err := tx.Query(ctx, `with h as (select coalesce((select posting_sequence from reporting_hwm where ledger_id=$1), 0) as seq)
		select h.seq, b.period_year, b.period_no, b.account, b.combination, b.dims::text, b.net::text, b.closing_net::text, b.currency_type
		from h left join reporting_balance b
		  on b.ledger_id=$1 and b.currency_type = any($2::text[]) and b.period_year between $3 and $4`,
		ledgerID, currencies, ps[0].Year, ps[len(ps)-1].Year)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []domain.Balance
	var seq int64
	for rows.Next() {
		var year, no *int
		var account, combination, dims, net, closing, currency *string
		if err := rows.Scan(&seq, &year, &no, &account, &combination, &dims, &net, &closing, &currency); err != nil {
			return nil, nil, err
		}
		if year == nil {
			continue // no balances: the left join still carries the HWM
		}
		idx, ok := cal.Index(*year, *no)
		if !ok {
			continue
		}
		b := domain.Balance{LedgerID: ledgerID, Period: idx, Account: *account, Combination: *combination, CurrencyType: *currency}
		if err := json.Unmarshal([]byte(*dims), &b.Dims); err != nil {
			return nil, nil, err
		}
		if b.Net, err = decimal.NewFromString(*net); err != nil {
			return nil, nil, err
		}
		if b.ClosingNet, err = decimal.NewFromString(*closing); err != nil {
			return nil, nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return out, map[string]int64{ledgerID: seq}, nil
}

const runColumns = `id::text, definition_code, definition_version, ledger_id::text, currency_type, period_year, period_no,
	status, hwm, result_hash, cell_count, requested_by, started_at, duration_ms, checks`

func getRunTx(ctx context.Context, tx pgx.Tx, id string) (Run, error) {
	run, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from statement_run where id=$1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return run, problem(404, CodeRunNotFound, "statement run "+id+" does not exist")
	}
	return run, err
}

func scanRun(r rowScanner) (Run, error) {
	var run Run
	var hwm, hash, checks []byte
	var duration *int
	if err := r.Scan(&run.ID, &run.DefinitionCode, &run.DefinitionVersion, &run.LedgerID, &run.CurrencyType, &run.PeriodYear, &run.PeriodNo,
		&run.Status, &hwm, &hash, &run.CellCount, &run.RequestedBy, &run.StartedAt, &duration, &checks); err != nil {
		return run, err
	}
	run.Checks = []domain.Check{}
	if err := json.Unmarshal(checks, &run.Checks); err != nil {
		return run, err
	}
	if err := json.Unmarshal(hwm, &run.HWMByLedger); err != nil {
		return run, err
	}
	run.ResultHash = hex.EncodeToString(hash)
	if duration != nil {
		run.DurationMS = *duration
	}
	return run, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCell(r rowScanner) (domain.Cell, error) {
	var c domain.Cell
	var value, keys string
	if err := r.Scan(&c.Row, &c.Column, &value, &c.Blank, &c.Computed, &c.Note, &keys); err != nil {
		return c, err
	}
	var err error
	if c.Value, err = decimal.NewFromString(value); err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(keys), &c.Keys); err != nil {
		return c, err
	}
	if c.Keys == nil {
		c.Keys = []domain.Key{}
	}
	return c, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
