package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// MaxLagSeconds is the lag gate (reporting.max_lag_seconds): a run on a
	// ledger whose read model is further behind is refused with REPORT-0004
	// unless AllowStale is set, in which case the run records the warning.
	MaxLagSeconds int
	AllowStale    bool
	// Hierarchy, when set, replaces the definition's rows by the nodes of a
	// published hierarchy (FR-15.4).
	Hierarchy *HierarchyRun
	// Tree, when set, limits the run to the balances of one unit of a reporting
	// tree, rolled up (FR-15.4).
	Tree *TreeRun
	// Consolidation, when set (two to MaxConsolidationLedgers ledgers), makes
	// the run a consolidated-by-entity run (FR-15.5): LedgerID is the first of
	// them and the definition's columns are expanded per ledger. It is
	// refused with a tree, a hierarchy, a cash flow definition, CALC columns
	// and any currency type but rpt.
	Consolidation []domain.LedgerColumn
}

// TreeRun names the reporting tree and unit a run is limited to. An empty Unit
// is the whole tree (the roll-up of every unit).
type TreeRun struct {
	Code string
	Unit string
}

// RunTree is the tree, unit and tree revision a stored run used.
type RunTree struct {
	Code     string `json:"code"`
	Unit     string `json:"unit"`
	Revision int    `json:"revision"`
}

// HierarchyRun names the hierarchy a run takes its rows from. Version 0 is the
// latest published version. AccountFrom and AccountTo narrow a dimension
// hierarchy to accounts; NormalBalance (debit by default) is the presentation.
type HierarchyRun struct {
	Code          string
	Version       int
	NormalBalance string
	AccountFrom   string
	AccountTo     string
}

// RunHierarchy is the hierarchy version a stored run used.
type RunHierarchy struct {
	Code    string `json:"code"`
	Version int    `json:"version"`
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
	Checks []domain.Check `json:"checks"`
	// Hierarchy is set when the run's rows are the nodes of a hierarchy; the
	// run's own definition (GET /runs/{id}/definition) then differs from the
	// published one.
	Hierarchy *RunHierarchy `json:"hierarchy,omitempty"`
	// Tree is set when the run is limited to a unit of a reporting tree; unit
	// is empty for the whole tree.
	Tree *RunTree `json:"tree,omitempty"`
	// Consolidation lists the ledgers of a consolidated-by-entity run in column
	// order; LedgerID is the first. Absent for a single-ledger run.
	Consolidation []domain.LedgerColumn `json:"consolidation,omitempty"`
	Replayed      bool                  `json:"-"`
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
		consolidated := len(spec.Consolidation) > 0
		if consolidated {
			if def, err = consolidateRun(def, spec); err != nil {
				return err
			}
		}
		ledgers := []string{spec.LedgerID}
		if consolidated {
			ledgers = ledgers[:0]
			for _, l := range spec.Consolidation {
				ledgers = append(ledgers, l.LedgerID)
			}
		}
		// Every ledger is read under the caller's legal-entity scope: a ledger the
		// caller cannot see is REPORT-0031, never a silently smaller consolidation.
		var entity string
		var entities []string
		var stale []domain.Check
		var balances []domain.Balance
		hwm := map[string]int64{}
		for i, ledger := range ledgers {
			asserted := ""
			if !consolidated {
				asserted = spec.LegalEntityID
			}
			e, err := requireLedgerEntityTx(ctx, tx, ledger, asserted)
			if err != nil {
				return err
			}
			if i == 0 {
				entity = e
			}
			entities = append(entities, e)
			st, err := staleCheckTx(ctx, tx, ledger, spec.MaxLagSeconds, spec.AllowStale)
			if err != nil {
				return err
			}
			stale = append(stale, st...)
			b, h, err := loadBalancesTx(ctx, tx, ledger, def.CurrencyTypes(spec.CurrencyType), cal)
			if err != nil {
				return err
			}
			balances = append(balances, b...)
			for k, v := range h {
				hwm[k] = v
			}
		}
		var tree *RunTree
		var extra []domain.Check
		if spec.Tree != nil {
			if def.Kind == domain.KindCashFlow {
				return problem(422, domain.CodeTreeRun, "a cash flow definition cannot be limited to a tree unit; its net income and cash reconcile over the whole ledger.")
			}
			if balances, tree, extra, err = limitRunToTreeTx(ctx, tx, spec.Tree, balances); err != nil {
				return err
			}
		}
		var hier *RunHierarchy
		if spec.Hierarchy != nil {
			// Appended, not assigned: a tree run over a hierarchy keeps REPORT-0041.
			var hierChecks []domain.Check
			if def, hier, hierChecks, err = expandRunHierarchyTx(ctx, tx, def, spec, balances); err != nil {
				return err
			}
			extra = append(extra, hierChecks...)
		}
		in := domain.Input{Definition: def, LedgerID: spec.LedgerID, CurrencyType: spec.CurrencyType,
			IncludeAdjustmentPeriods: spec.IncludeAdjustment, Calendar: cal, At: at, Balances: balances, Consolidated: consolidated}
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
		checks := append(domain.RunChecks(def, res), extra...)
		checks = append(checks, stale...)
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
		var hierCode, hierVersion, runDef any
		if hier != nil {
			body, err := json.Marshal(def)
			if err != nil {
				return err
			}
			hierCode, hierVersion, runDef = hier.Code, hier.Version, body
		}
		var treeCode, treeUnit, treeRev any
		if tree != nil {
			treeCode, treeUnit, treeRev = tree.Code, tree.Unit, tree.Revision
		}
		run = Run{
			Hierarchy: hier,
			ID:        ids.New(), DefinitionCode: def.Code, DefinitionVersion: def.Version, LedgerID: spec.LedgerID,
			CurrencyType: spec.CurrencyType, PeriodYear: spec.PeriodYear, PeriodNo: spec.PeriodNo, Status: "completed",
			HWMByLedger: hwm, ResultHash: res.Hash, CellCount: len(res.Cells), RequestedBy: spec.RequestedBy, StartedAt: started,
			Checks: checks, Tree: tree, Consolidation: spec.Consolidation,
		}
		var consolidationJSON any
		var entityIDs any
		if consolidated {
			if consolidationJSON, err = json.Marshal(spec.Consolidation); err != nil {
				return err
			}
			// A ledger without an assigned entity makes the run visible to
			// all-entity callers only (a NULL list matches no scoped caller).
			entityIDs = scopedEntities(entities)
		}
		_, err = tx.Exec(ctx, `insert into statement_run(tenant_id, id, definition_code, definition_version, ledger_id, currency_type,
			period_year, period_no, include_adjustment, status, hwm, result_hash, cell_count, requested_by, request_hash, idempotency_key,
			started_at, completed_at, duration_ms, checks, legal_entity_id, hierarchy_code, hierarchy_version, run_definition,
			tree_code, tree_unit, tree_revision, consolidation, legal_entity_ids)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,'completed',$10,$11,$12,$13,$14,$15,$16,now(),$17,$18,nullif($19,'')::uuid,$20,$21,$22,$23,$24,$25,$26,$27::uuid[])`,
			tenantID(ctx), run.ID, def.Code, def.Version, spec.LedgerID, spec.CurrencyType, spec.PeriodYear, spec.PeriodNo,
			spec.IncludeAdjustment, hwmJSON, hash, len(res.Cells), spec.RequestedBy, spec.RequestHash, nullable(spec.IdempotencyKey),
			started, int(time.Since(started).Milliseconds()), checksJSON, entityOf(entity, consolidated), hierCode, hierVersion, runDef, treeCode, treeUnit, treeRev, consolidationJSON, entityIDs)
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
			Parameters: consolidatedParameters(runParameters(period, hier, tree), spec.Consolidation),
		}); err != nil {
			return err
		}
		details := map[string]any{
			"definition": def.Code, "version": def.Version, "currencyType": spec.CurrencyType,
			"hwm": hwm[spec.LedgerID], "resultHash": res.Hash, "cells": len(res.Cells),
		}
		if hier != nil {
			details["hierarchy"] = hierarchyRef(hier)
		}
		if tree != nil {
			details["tree"] = treeRef(tree)
		}
		// One entry per ledger, each in its own entity's scope, so a caller sees
		// the entries of the entities it may see and no others.
		for i, ledger := range ledgers {
			own := map[string]any{}
			for k, v := range details {
				own[k] = v
			}
			own["hwm"] = hwm[ledger]
			if consolidated {
				// The entry names the consolidation it belongs to, not the other
				// ledgers, which may belong to entities outside this entry's scope.
				own["consolidatedLedgers"] = len(ledgers)
			}
			if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditStatementRun, LedgerID: ledger, LegalEntityID: entities[i], Period: period,
				ActorID: spec.RequestedBy, ObjectRef: "run:" + run.ID, Details: own}); err != nil {
				return err
			}
		}
		return nil
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

// MaxRunPage is the largest page ListRunsPage returns; RunDefaultPage is the
// size of a page when the caller does not ask for one.
const (
	MaxRunPage     = 200
	RunDefaultPage = 50
)

// RunQuery narrows the report library of runs. Empty fields do not filter; a
// period is both PeriodYear and PeriodNo (PeriodYear 0 means any period).
type RunQuery struct {
	Definition string
	LedgerID   string
	PeriodYear int
	PeriodNo   int
}

// RunPage is one keyset page of runs, newest first.
type RunPage struct {
	Items []Run
	Next  *domain.AuditCursor // started-at and id of the last item, when more follow
}

// ListRuns returns the first page of the newest runs for one definition code
// (empty for every definition), at most limit (1..MaxRunPage).
func (s *Postgres) ListRuns(ctx context.Context, definition string, limit int) ([]Run, error) {
	if limit < 1 || limit > MaxRunPage {
		limit = MaxRunPage
	}
	page, err := s.ListRunsPage(ctx, RunQuery{Definition: definition}, nil, limit)
	return page.Items, err
}

// ListRunsPage returns the report library of runs (FR-15.6, FR-15.8), newest
// first, filtered by definition, ledger and period and paged by keyset on
// (started at, id) so a run stored while a user pages through is never skipped
// or repeated. limit outside 1..MaxRunPage becomes RunDefaultPage.
func (s *Postgres) ListRunsPage(ctx context.Context, q RunQuery, after *domain.AuditCursor, limit int) (RunPage, error) {
	if limit < 1 || limit > MaxRunPage {
		limit = RunDefaultPage
	}
	page := RunPage{Items: []Run{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cursorAt *time.Time
		cursorID := ""
		if after != nil {
			t := after.OccurredAt
			cursorAt, cursorID = &t, after.ID
		}
		rows, err := tx.Query(ctx, `select `+runColumns+` from statement_run
			where ($1 = '' or definition_code = $1)
			  and ($2 = '' or ledger_id = $2::uuid)
			  and ($3 = 0 or (period_year = $3 and period_no = $4))
			  and ($5::timestamptz is null or (started_at, id) < ($5, nullif($6, '')::uuid))
			order by started_at desc, id desc limit $7`,
			q.Definition, q.LedgerID, q.PeriodYear, q.PeriodNo, cursorAt, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, run)
		}
		return rows.Err()
	})
	if err != nil {
		return RunPage{Items: []Run{}}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = &domain.AuditCursor{OccurredAt: last.StartedAt, ID: last.ID}
	}
	return page, nil
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
	status, hwm, result_hash, cell_count, requested_by, started_at, duration_ms, checks, hierarchy_code, hierarchy_version, tree_code, tree_unit, tree_revision, consolidation`

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
	var duration, hierVersion, treeRev *int
	var hierCode, treeCode, treeUnit *string
	var consolidation []byte
	if err := r.Scan(&run.ID, &run.DefinitionCode, &run.DefinitionVersion, &run.LedgerID, &run.CurrencyType, &run.PeriodYear, &run.PeriodNo,
		&run.Status, &hwm, &hash, &run.CellCount, &run.RequestedBy, &run.StartedAt, &duration, &checks, &hierCode, &hierVersion, &treeCode, &treeUnit, &treeRev, &consolidation); err != nil {
		return run, err
	}
	if len(consolidation) > 0 {
		if err := json.Unmarshal(consolidation, &run.Consolidation); err != nil {
			return run, err
		}
	}
	if hierCode != nil && hierVersion != nil {
		run.Hierarchy = &RunHierarchy{Code: *hierCode, Version: *hierVersion}
	}
	if treeCode != nil && treeUnit != nil && treeRev != nil {
		run.Tree = &RunTree{Code: *treeCode, Unit: *treeUnit, Revision: *treeRev}
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

// consolidateRun expands def for a consolidated-by-entity run and refuses the
// combinations the expansion cannot honour: only the reporting currency type
// sums across ledgers (there is no translation between ledger currencies), and
// a tree unit or a hierarchy replaces the rows or the balances per ledger.
func consolidateRun(def domain.Definition, spec RunSpec) (domain.Definition, error) {
	switch {
	case spec.Tree != nil || spec.Hierarchy != nil:
		return def, problem(422, domain.CodeConsolidation, "a consolidated run cannot also use a reporting tree or a hierarchy.")
	case spec.LedgerID != spec.Consolidation[0].LedgerID:
		return def, problem(422, domain.CodeConsolidation, "ledgerId must be the first consolidation ledger.")
	}
	for _, ct := range def.CurrencyTypes(spec.CurrencyType) {
		if ct != "rpt" {
			return def, problem(422, domain.CodeConsolidation, "ledgers are added as stored, without currency translation; a consolidated run needs the reporting currency type (rpt) in every column.")
		}
	}
	return domain.Consolidate(def, spec.Consolidation)
}

// consolidatedParameters adds the ledgers of a consolidated run to the event
// parameters (comma separated, in column order).
func consolidatedParameters(p map[string]string, c []domain.LedgerColumn) map[string]string {
	if len(c) > 0 {
		list := make([]string, len(c))
		for i, l := range c {
			list[i] = l.LedgerID
		}
		p["ledgers"] = strings.Join(list, ",")
	}
	return p
}

// entityOf is the legal_entity_id column of a run: empty for a consolidated run,
// whose entities are in legal_entity_ids.
func entityOf(entity string, consolidated bool) string {
	if consolidated {
		return ""
	}
	return entity
}

// scopedEntities lists the distinct entities of a consolidated run, or nil when
// a ledger has no assigned entity: the run is then visible to all-entity callers
// only, like the ledger itself.
func scopedEntities(entities []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entities {
		if e == "" {
			return nil
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}
