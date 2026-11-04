package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// OnhandRun is a validated onhand extract ready to load (FR-49.1). Run carries the identity,
// source, cutoff, input hash, counts and quarantine; its status is VALIDATED. Lines is the whole
// extract and Accepted the lines that passed the suite. GLRunID and Account, when set, name the
// gl_balances run of the same system and company and the inventory GL account whose migrated
// balance must equal the loaded on-hand value (BR-49.3).
type OnhandRun struct {
	Run       domain.PackRun
	Lines     []domain.OnhandLine
	Accepted  []domain.OnhandLine
	GLRunID   string
	Account   string
	StartedBy string
}

// RunOnhand loads the accepted lines that Passage does not hold yet, each with its legacy_xref
// row and its onhand_opening_balance row, reconciles the run (and the inventory account of the
// gl_balances run it names) and stores it with its totals, quarantine and the pack.reconciled
// event, in one transaction. Runs of one (system, company) serialize on an advisory lock. Nothing
// posts to the inventory ledger here: the opening balances are what its opening journal will take.
func (s *Postgres) RunOnhand(ctx context.Context, idem Idem, in OnhandRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runOnhand(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

func runOnhand(ctx context.Context, tx pgx.Tx, in OnhandRun) (domain.PackRun, error) {
	run := in.Run
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableOnhand); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	var glTotals []domain.ControlTotal
	if in.GLRunID != "" {
		gl, err := getRun(ctx, tx, in.GLRunID, false)
		if errors.Is(err, ErrNotFound) {
			return domain.PackRun{}, fmt.Errorf("%w: glRunId %s is not a pack run of this tenant", domain.ErrInvalid, in.GLRunID)
		}
		if err != nil {
			return domain.PackRun{}, err
		}
		if err := checkGLRun(gl, run); err != nil {
			return domain.PackRun{}, err
		}
		glTotals = gl.Totals
		run.GLRunID = gl.RunID
	}

	idsIn := make([]string, len(in.Accepted))
	for i, o := range in.Accepted {
		idsIn[i] = o.LegacyID
	}
	held := map[string]domain.OnhandLine{}
	rows, err := tx.Query(ctx, `select legacy_id, item, site, warehouse, location, batch, serial, unit, currency, quantity::text, value::text
		from onhand_opening_balance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and legacy_id = any($3)`,
		sys, company, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read onhand_opening_balance: %w", err)
	}
	for rows.Next() {
		var o domain.OnhandLine
		var qty, val string
		if err := rows.Scan(&o.LegacyID, &o.Item, &o.Site, &o.Warehouse, &o.Location, &o.Batch, &o.Serial, &o.Unit, &o.Currency, &qty, &val); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		if o.Quantity, err = decimal.NewFromString(qty); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		if o.Value, err = decimal.NewFromString(val); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		held[o.LegacyID] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	var toLoad []domain.OnhandLine
	for _, o := range in.Accepted {
		if _, ok := held[o.LegacyID]; !ok {
			toLoad = append(toLoad, o)
		}
	}
	if err := checkOnhandKeys(ctx, tx, sys, company, toLoad); err != nil {
		return domain.PackRun{}, err
	}
	for _, o := range toLoad {
		held[o.LegacyID] = o
	}
	if run.Tolerances, err = loadTolerances(ctx, tx); err != nil {
		return domain.PackRun{}, err
	}
	if err := domain.ReconcileOnhand(&run, in.Lines, in.Accepted, len(toLoad), held, in.Account, glTotals); err != nil {
		return domain.PackRun{}, err
	}
	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	batch := &pgx.Batch{}
	for _, o := range toLoad {
		xrefID, balanceID := ids.New(), ids.New()
		batch.Queue(`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'onhand_opening_balance', $6, $7, $8, $9)`,
			xrefID, sys, company, domain.TableOnhand, o.LegacyID, balanceID, run.RunID, o.Value.String(), o.Currency)
		batch.Queue(`insert into onhand_opening_balance (tenant_id, balance_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
			item, site, warehouse, location, batch, serial, unit, quantity, value, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
			balanceID, run.RunID, xrefID, sys, company, o.LegacyID, o.Item, o.Site, o.Warehouse, o.Location, o.Batch, o.Serial, o.Unit,
			o.Quantity.String(), o.Value.String(), o.Currency)
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return domain.PackRun{}, fmt.Errorf("insert onhand opening balances: %w", err)
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

// checkGLRun accepts a gl_balances run of the same source system and company that has loaded its
// balances (LOADED or RECONCILED) as the BR-49.3 counterpart of an onhand run.
func checkGLRun(gl, oh domain.PackRun) error {
	switch {
	case gl.Pack != domain.PackGLBalances:
		return fmt.Errorf("%w: glRunId %s is a %s run, not gl_balances", domain.ErrInvalid, gl.RunID, gl.Pack)
	case gl.SourceSystem != oh.SourceSystem || gl.SourceCompany != oh.SourceCompany:
		return fmt.Errorf("%w: gl_balances run %s is for %s %s, this extract for %s %s", domain.ErrInvalid, gl.RunID,
			gl.SourceSystem, gl.SourceCompany, oh.SourceSystem, oh.SourceCompany)
	case gl.Status != domain.StatusLoaded && gl.Status != domain.StatusReconciled:
		return fmt.Errorf("%w: gl_balances run %s is %s; it must have loaded its balances", domain.ErrInvalid, gl.RunID, gl.Status)
	}
	return nil
}

// checkOnhandKeys refuses lines whose (item, site, warehouse, location, batch, serial) Passage
// already holds for the company under another legacy id: loading them would open the stock twice
// (FR-49.10), and a serial the company already holds anywhere (checkOnhandSerials). One conflict
// rejects the run before anything is written.
func checkOnhandKeys(ctx context.Context, tx pgx.Tx, sys, company string, toLoad []domain.OnhandLine) error {
	if len(toLoad) == 0 {
		return nil
	}
	n := len(toLoad)
	cols := make([][]string, 6)
	for i := range cols {
		cols[i] = make([]string, n)
	}
	for i, o := range toLoad {
		cols[0][i], cols[1][i], cols[2][i], cols[3][i], cols[4][i], cols[5][i] = o.Item, o.Site, o.Warehouse, o.Location, o.Batch, o.Serial
	}
	var item, site, warehouse, location, batch, serial, legacyID, runID string
	err := tx.QueryRow(ctx, `select b.item, b.site, b.warehouse, b.location, b.batch, b.serial, b.legacy_id, b.run_id::text
		from onhand_opening_balance b
		join unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[]) as k(item, site, warehouse, location, batch, serial)
		  on b.item = k.item and b.site = k.site and b.warehouse = k.warehouse and b.location = k.location and b.batch = k.batch and b.serial = k.serial
		where b.tenant_id = current_setting('passage.tenant_id', true)::uuid and b.source_system = $1 and b.source_company = $2
		order by b.item, b.site, b.warehouse, b.location, b.batch, b.serial limit 1`,
		sys, company, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5]).Scan(&item, &site, &warehouse, &location, &batch, &serial, &legacyID, &runID)
	switch {
	case err == nil:
		return fmt.Errorf("%w: item %s at %s/%s/%s batch %q serial %q already has opening stock, loaded as legacy id %s by run %s; re-extract with the same legacy ids",
			domain.ErrInvalid, item, site, warehouse, location, batch, serial, legacyID, runID)
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check onhand_opening_balance keys: %w", err)
	}
	return checkOnhandSerials(ctx, tx, sys, company, toLoad)
}

// checkOnhandSerials refuses a serial of an item that Passage already holds for the company under
// another legacy id, at any dimension: a serial is one unit in one place, and loading it again
// at another warehouse or location would open it twice.
func checkOnhandSerials(ctx context.Context, tx pgx.Tx, sys, company string, toLoad []domain.OnhandLine) error {
	var items, serials []string
	for _, o := range toLoad {
		if o.Serial != "" {
			items, serials = append(items, o.Item), append(serials, o.Serial)
		}
	}
	if len(items) == 0 {
		return nil
	}
	var item, serial, site, warehouse, location, legacyID, runID string
	err := tx.QueryRow(ctx, `select b.item, b.serial, b.site, b.warehouse, b.location, b.legacy_id, b.run_id::text
		from onhand_opening_balance b
		join unnest($3::text[], $4::text[]) as k(item, serial) on b.item = k.item and b.serial = k.serial
		where b.tenant_id = current_setting('passage.tenant_id', true)::uuid and b.source_system = $1 and b.source_company = $2
		order by b.item, b.serial limit 1`,
		sys, company, items, serials).Scan(&item, &serial, &site, &warehouse, &location, &legacyID, &runID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check onhand_opening_balance serials: %w", err)
	}
	return fmt.Errorf("%w: serial %q of item %s already has opening stock at %s/%s/%s, loaded as legacy id %s by run %s; a serial is opened once",
		domain.ErrInvalid, serial, item, site, warehouse, location, legacyID, runID)
}
