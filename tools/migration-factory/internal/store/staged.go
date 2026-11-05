package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// StagedRun is a validated extract of a staged pack (wip_prod, fixed_assets, payroll_ytd) ready to
// load. Lines is the whole extract and Accepted the lines that passed the pack's suite.
type StagedRun struct {
	Spec      domain.StagedSpec
	Run       domain.PackRun
	Lines     []domain.StagedLine
	Accepted  []domain.StagedLine
	StartedBy string
}

// RunStaged loads the accepted lines Passage does not hold yet, each with its legacy_xref row and
// its staged_opening_line row, reconciles the run and stores it with its totals, quarantine and the
// pack.reconciled event, in one transaction. Runs of one (system, company, pack) serialize on an
// advisory lock. Nothing posts to the owning service.
func (s *Postgres) RunStaged(ctx context.Context, idem Idem, in StagedRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runStaged(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

func runStaged(ctx context.Context, tx pgx.Tx, in StagedRun) (domain.PackRun, error) {
	run, spec := in.Run, in.Spec
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, spec.Table); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	idsIn := make([]string, len(in.Accepted))
	for i, l := range in.Accepted {
		idsIn[i] = l.LegacyID
	}
	held := map[string]domain.StagedLine{}
	rows, err := tx.Query(ctx, `select legacy_id, natural_key, currency, grouping, measures from staged_opening_line
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and pack = $1 and source_system = $2 and source_company = $3 and legacy_id = any($4)`,
		string(spec.Pack), sys, company, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read staged_opening_line: %w", err)
	}
	for rows.Next() {
		var l domain.StagedLine
		var grouping, measures []byte
		if err := rows.Scan(&l.LegacyID, &l.NaturalKey, &l.Currency, &grouping, &measures); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		if l.Group, l.Measures, err = decodeStaged(grouping, measures); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		held[l.LegacyID] = l
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	var toLoad []domain.StagedLine
	for _, l := range in.Accepted {
		if _, ok := held[l.LegacyID]; !ok {
			toLoad = append(toLoad, l)
		}
	}
	if err := checkStagedKeys(ctx, tx, spec.Pack, sys, company, toLoad); err != nil {
		return domain.PackRun{}, err
	}
	for _, l := range toLoad {
		held[l.LegacyID] = l
	}
	if run.Tolerances, err = loadTolerances(ctx, tx); err != nil {
		return domain.PackRun{}, err
	}
	if err := domain.ReconcileStaged(&run, spec, in.Lines, in.Accepted, len(toLoad), held); err != nil {
		return domain.PackRun{}, err
	}
	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	batch := &pgx.Batch{}
	for _, l := range toLoad {
		xrefID, lineID := ids.New(), ids.New()
		grouping, measures, err := encodeStaged(l)
		if err != nil {
			return domain.PackRun{}, err
		}
		amount := decimal.Zero
		if len(spec.Measures) > 0 {
			amount = l.Measures[spec.Measures[len(spec.Measures)-1]]
			if spec.Pack == domain.PackFixedAssets {
				amount = l.Measures["nbv"]
			}
		}
		batch.Queue(`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'staged_opening_line', $6, $7, $8, $9)`,
			xrefID, sys, company, spec.Table, l.LegacyID, lineID, run.RunID, amount.Round(6).String(), l.Currency)
		batch.Queue(`insert into staged_opening_line (tenant_id, line_id, run_id, legacy_xref_id, pack, source_system, source_company,
			legacy_id, natural_key, currency, grouping, measures)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			lineID, run.RunID, xrefID, string(spec.Pack), sys, company, l.LegacyID, l.NaturalKey, l.Currency, grouping, measures)
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return domain.PackRun{}, fmt.Errorf("insert staged opening lines: %w", err)
		}
	}
	if err := insertTotals(ctx, tx, run.RunID, run.Totals, 0); err != nil {
		return domain.PackRun{}, err
	}
	for i := range run.Quarantined {
		q := &run.Quarantined[i]
		q.Seq, q.Status = i+1, domain.QuarantineOpen
		if err := insertQuarantineLine(ctx, tx, run.RunID, q.Seq, q.Record.LegacyID, q.Rule, q.Reason, recordJSON(q.Record), q.Line); err != nil {
			return domain.PackRun{}, err
		}
	}
	if err := enqueueReconciled(ctx, tx, run); err != nil {
		return domain.PackRun{}, err
	}
	return run, nil
}

func encodeStaged(l domain.StagedLine) ([]byte, []byte, error) {
	grouping, err := json.Marshal(l.Group)
	if err != nil {
		return nil, nil, err
	}
	m := make(map[string]string, len(l.Measures))
	for k, v := range l.Measures {
		m[k] = v.String()
	}
	measures, err := json.Marshal(m)
	return grouping, measures, err
}

func decodeStaged(grouping, measures []byte) (map[string]string, map[string]decimal.Decimal, error) {
	var g map[string]string
	if err := json.Unmarshal(grouping, &g); err != nil {
		return nil, nil, fmt.Errorf("staged_opening_line grouping: %w", err)
	}
	var raw map[string]string
	if err := json.Unmarshal(measures, &raw); err != nil {
		return nil, nil, fmt.Errorf("staged_opening_line measures: %w", err)
	}
	m := make(map[string]decimal.Decimal, len(raw))
	for k, v := range raw {
		d, err := decimal.NewFromString(v)
		if err != nil {
			return nil, nil, fmt.Errorf("staged_opening_line measure %s %q: %w", k, v, err)
		}
		m[k] = d
	}
	return g, m, nil
}

// checkStagedKeys refuses a line whose natural key Passage already holds for the pack and company
// under another legacy id: loading it would open the order, asset book or worker year twice
// (FR-49.10). One conflict rejects the run before anything is written.
func checkStagedKeys(ctx context.Context, tx pgx.Tx, pack domain.Pack, sys, company string, toLoad []domain.StagedLine) error {
	if len(toLoad) == 0 {
		return nil
	}
	keys := make([]string, len(toLoad))
	for i, l := range toLoad {
		keys[i] = l.NaturalKey
	}
	var key, legacyID, runID string
	err := tx.QueryRow(ctx, `select natural_key, legacy_id, run_id::text from staged_opening_line
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and pack = $1 and source_system = $2 and source_company = $3 and natural_key = any($4)
		order by natural_key limit 1`, string(pack), sys, company, keys).Scan(&key, &legacyID, &runID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check staged_opening_line keys: %w", err)
	}
	return fmt.Errorf("%w: %s %s is already staged, loaded as legacy id %s by run %s; re-extract with the same legacy ids",
		domain.ErrInvalid, pack, key, legacyID, runID)
}
