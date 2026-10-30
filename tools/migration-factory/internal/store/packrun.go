package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// OpenItemsRun is a validated open_items extract ready to load. Run carries the identity,
// source, cutoff, input hash, record counts and quarantine; its status is VALIDATED.
type OpenItemsRun struct {
	Run       domain.PackRun
	Records   []domain.Record
	Accepted  []domain.Record
	StartedBy string
}

// RunOpenItems loads the accepted records that legacy_xref does not hold yet, reconciles the
// run and stores it with its control totals, quarantine and the pack.reconciled event, all in
// one transaction. Runs of the same (system, company) serialize on an advisory lock, so two
// concurrent runs of one extract cannot both load a record.
func (s *Postgres) RunOpenItems(ctx context.Context, idem Idem, in OpenItemsRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runOpenItems(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

func runOpenItems(ctx context.Context, tx pgx.Tx, in OpenItemsRun) (domain.PackRun, error) {
	run := in.Run
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableOpenItems); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	idsIn := make([]string, len(in.Accepted))
	for i, r := range in.Accepted {
		idsIn[i] = r.LegacyID
	}
	present := map[string]decimal.Decimal{}
	rows, err := tx.Query(ctx, `select source_id, amount::text from legacy_xref
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and source_table = $3 and source_id = any($4)`, sys, company, domain.TableOpenItems, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read legacy_xref: %w", err)
	}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		amt, err := decimal.NewFromString(raw)
		if err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		present[id] = amt
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	existing := make(map[string]struct{}, len(present))
	for id := range present {
		existing[domain.XrefKey(run.SourceSystem, company, domain.TableOpenItems, id)] = struct{}{}
	}
	plan := domain.PlanLoad(in.Accepted, run.SourceSystem, company, domain.TableOpenItems, existing)
	for _, r := range plan.ToLoad {
		present[r.LegacyID] = r.Amount
	}
	if err := domain.ReconcileOpenItems(&run, in.Records, in.Accepted, len(plan.ToLoad), present); err != nil {
		return domain.PackRun{}, err
	}

	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	if n := len(plan.ToLoad); n > 0 {
		xrefIDs, passageIDs, sourceIDs := make([]string, n), make([]string, n), make([]string, n)
		amounts, currencies := make([]string, n), make([]string, n)
		for i, r := range plan.ToLoad {
			xrefIDs[i], passageIDs[i], sourceIDs[i] = ids.New(), ids.New(), r.LegacyID
			amounts[i], currencies[i] = r.Amount.String(), r.Currency
		}
		tag, err := tx.Exec(ctx, `insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			select current_setting('passage.tenant_id', true)::uuid, x, $1, $2, $3, s, 'open_item', p, $4, a, c
			from unnest($5::uuid[], $6::text[], $7::uuid[], $8::numeric[], $9::text[]) as t(x, s, p, a, c)`,
			sys, company, domain.TableOpenItems, run.RunID, xrefIDs, sourceIDs, passageIDs, amounts, currencies)
		if err != nil {
			return domain.PackRun{}, fmt.Errorf("insert legacy_xref: %w", err)
		}
		if int(tag.RowsAffected()) != n {
			return domain.PackRun{}, fmt.Errorf("insert legacy_xref: %d of %d rows", tag.RowsAffected(), n)
		}
	}
	if err := insertTotals(ctx, tx, run.RunID, run.Totals, 0); err != nil {
		return domain.PackRun{}, err
	}
	for i := range run.Quarantined {
		q := &run.Quarantined[i]
		q.Seq, q.Status = i+1, domain.QuarantineOpen
		if err := insertQuarantine(ctx, tx, run.RunID, q.Seq, q.Record.LegacyID, q.Rule, q.Reason, recordJSON(q.Record)); err != nil {
			return domain.PackRun{}, err
		}
	}
	if err := enqueueReconciled(ctx, tx, run); err != nil {
		return domain.PackRun{}, err
	}
	return run, nil
}

// storeNewRun stores a new pack run at version 1 and sets its created_at.
func storeNewRun(ctx context.Context, tx pgx.Tx, run *domain.PackRun, startedBy string) error {
	parties := run.Parties
	if parties == nil {
		parties = []string{}
	}
	var openItemsRun *string
	if run.OpenItemsRunID != "" {
		openItemsRun = &run.OpenItemsRunID
	}
	run.Version = 1
	if err := tx.QueryRow(ctx, `insert into pack_run (tenant_id, run_id, pack, source_system, source_company, cutoff_ts, input_hash, status,
		records_in, records_loaded, records_quarantined, already_present, started_by, parties, open_items_run_id)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) returning created_at`,
		run.RunID, string(run.Pack), string(run.SourceSystem), run.SourceCompany, run.CutoffTS, run.InputHash, string(run.Status),
		run.RecordsIn, run.RecordsLoaded, run.RecordsQuarantined, run.AlreadyPresent, startedBy, parties, openItemsRun).Scan(&run.CreatedAt); err != nil {
		return fmt.Errorf("insert pack_run: %w", err)
	}
	return nil
}

// insertTotals stores control totals with seq numbers after fromSeq.
func insertTotals(ctx context.Context, tx pgx.Tx, runID string, totals []domain.ControlTotal, fromSeq int) error {
	for i, t := range totals {
		key, err := json.Marshal(t.Key)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into control_total (tenant_id, total_id, run_id, seq, name, key_json, source_value, target_value,
			delta, tolerance, tolerance_reason, status)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			ids.New(), runID, fromSeq+i+1, t.Name, key, t.Source.String(), t.Target.String(), t.Delta.String(), t.Tolerance.Amount.String(), t.Tolerance.Reason, string(t.Status)); err != nil {
			return fmt.Errorf("insert control_total: %w", err)
		}
	}
	return nil
}

func insertQuarantine(ctx context.Context, tx pgx.Tx, runID string, seq int, legacyID, rule, reason string, record any) error {
	rec, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into pack_quarantine (tenant_id, run_id, seq, legacy_id, rule, reason, record)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6)`,
		runID, seq, legacyID, rule, reason, rec); err != nil {
		return fmt.Errorf("insert pack_quarantine: %w", err)
	}
	return nil
}

// enqueueReconciled writes dev.passage.migration.pack.reconciled.v1 for the run's current state.
func enqueueReconciled(ctx context.Context, tx pgx.Tx, run domain.PackRun) error {
	match, mismatch := domain.CountTotals(run.Totals)
	if err := enqueue(ctx, tx, mfevents.PackReconciled, "pack_run", run.RunID, mfevents.PackReconciledData{
		RunID: run.RunID, Pack: string(run.Pack), SourceSystem: string(run.SourceSystem), SourceCompany: run.SourceCompany, Status: string(run.Status),
		TotalsMatch: match, TotalsMismatch: mismatch, Quarantined: run.RecordsQuarantined, RecordsLoaded: run.RecordsLoaded,
	}); err != nil {
		return fmt.Errorf("enqueue pack.reconciled: %w", err)
	}
	return nil
}

type quarantinedRecord struct {
	LegacyID   string `json:"legacyId"`
	Party      string `json:"party"`
	Account    string `json:"account"`
	Dimensions string `json:"dimensions,omitempty"`
	Currency   string `json:"currency"`
	Amount     string `json:"amount"`
}

func recordJSON(r domain.Record) quarantinedRecord {
	return quarantinedRecord{LegacyID: r.LegacyID, Party: r.Party, Account: r.Account, Dimensions: r.Dimensions, Currency: r.Currency, Amount: r.Amount.String()}
}

const runColumns = `run_id::text, pack, source_system, source_company, cutoff_ts, input_hash, status, records_in, records_loaded,
	records_quarantined, already_present, parties, coalesce(open_items_run_id::text, ''), version, created_at`

func scanRun(row pgx.Row) (domain.PackRun, error) {
	var r domain.PackRun
	var pack, sys, status string
	if err := row.Scan(&r.RunID, &pack, &sys, &r.SourceCompany, &r.CutoffTS, &r.InputHash, &status, &r.RecordsIn, &r.RecordsLoaded,
		&r.RecordsQuarantined, &r.AlreadyPresent, &r.Parties, &r.OpenItemsRunID, &r.Version, &r.CreatedAt); err != nil {
		return domain.PackRun{}, err
	}
	r.Pack, r.SourceSystem, r.Status = domain.Pack(pack), domain.SourceSystem(sys), domain.RunStatus(status)
	return r, nil
}

// GetRun returns a run with its control totals and quarantine.
func (s *Postgres) GetRun(ctx context.Context, id string) (domain.PackRun, error) {
	var run domain.PackRun
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		run, err = getRun(ctx, tx, id, false)
		return err
	})
	return run, err
}

// getRun reads a run with its totals and quarantine; forUpdate takes the run's row lock first.
func getRun(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domain.PackRun, error) {
	q := `select ` + runColumns + ` from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`
	if forUpdate {
		q += ` for update`
	}
	run, err := scanRun(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PackRun{}, ErrNotFound
	}
	if err != nil {
		return domain.PackRun{}, err
	}
	rows, err := tx.Query(ctx, `select name, key_json, source_value::text, target_value::text, delta::text, tolerance::text, tolerance_reason, status
		from control_total where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 order by seq`, id)
	if err != nil {
		return domain.PackRun{}, err
	}
	for rows.Next() {
		var t domain.ControlTotal
		var key []byte
		var status, src, tgt, delta, tol string
		if err := rows.Scan(&t.Name, &key, &src, &tgt, &delta, &tol, &t.Tolerance.Reason, &status); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		t.Source, t.Target, t.Delta, t.Tolerance.Amount = decimal.RequireFromString(src), decimal.RequireFromString(tgt), decimal.RequireFromString(delta), decimal.RequireFromString(tol)
		if err := json.Unmarshal(key, &t.Key); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		t.Status = domain.ControlStatus(status)
		run.Totals = append(run.Totals, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	rows, err = tx.Query(ctx, `select seq, rule, reason, record, status, released_record, released_by, released_at
		from pack_quarantine where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 order by seq`, id)
	if err != nil {
		return domain.PackRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var q domain.Quarantined
		var raw, released []byte
		if err := rows.Scan(&q.Seq, &q.Rule, &q.Reason, &raw, &q.Status, &released, &q.ReleasedBy, &q.ReleasedAt); err != nil {
			return domain.PackRun{}, err
		}
		rec, err := parseRecordJSON(raw)
		if err != nil {
			return domain.PackRun{}, err
		}
		q.Record = rec
		if released != nil {
			rel, err := parseRecordJSON(released)
			if err != nil {
				return domain.PackRun{}, err
			}
			q.Released = &rel
		}
		run.Quarantined = append(run.Quarantined, q)
	}
	return run, rows.Err()
}

func parseRecordJSON(raw []byte) (domain.Record, error) {
	var rec quarantinedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return domain.Record{}, err
	}
	amt := decimal.Zero
	if rec.Amount != "" {
		var err error
		if amt, err = decimal.NewFromString(rec.Amount); err != nil {
			return domain.Record{}, fmt.Errorf("quarantined record %q amount: %w", rec.LegacyID, err)
		}
	}
	return domain.Record{LegacyID: rec.LegacyID, Party: rec.Party, Account: rec.Account, Dimensions: rec.Dimensions, Currency: rec.Currency, Amount: amt}, nil
}

// RunFilter narrows ListRuns. After is the run id of the last row of the previous page.
type RunFilter struct {
	Pack   string
	Status string
	After  string
	Limit  int
}

// ListRuns returns runs newest first, without totals or quarantine.
func (s *Postgres) ListRuns(ctx context.Context, f RunFilter) ([]domain.PackRun, error) {
	out := []domain.PackRun{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var after *time.Time
		var afterID *string
		if f.After != "" {
			var at time.Time
			err := tx.QueryRow(ctx, `select created_at from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, f.After).Scan(&at)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			after, afterID = &at, &f.After
		}
		rows, err := tx.Query(ctx, `select `+runColumns+` from pack_run
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and ($1 = '' or pack = $1) and ($2 = '' or status = $2)
			  and ($3::timestamptz is null or (created_at, run_id) < ($3, $4::uuid))
			order by created_at desc, run_id desc limit $5`, f.Pack, f.Status, after, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
