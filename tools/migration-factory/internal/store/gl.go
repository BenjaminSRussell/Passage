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

// GLBalancesRun is a validated gl_balances extract ready to load (FR-49.1). Run carries the
// identity, source, cutoff, input hash, counts and quarantine; its status is VALIDATED. Balances
// is the whole extract and Accepted the balances that passed the suite. OpenItemsRunID, when set,
// names the open_items run of the same system and company whose control accounts the GL balances
// must agree with (BR-49.2).
type GLBalancesRun struct {
	Run            domain.PackRun
	Balances       []domain.GLBalance
	Accepted       []domain.GLBalance
	OpenItemsRunID string
	StartedBy      string
}

// RunGLBalances loads the accepted balances that legacy_xref does not hold yet, each with its
// legacy_xref row and its gl_opening_balance row, reconciles the run (and the control accounts
// of the open_items run it names) and stores it with its totals, quarantine and the
// pack.reconciled event, in one transaction. Runs of one (system, company) serialize on an
// advisory lock. Nothing posts to the general ledger here: the opening balances are what its
// opening journal will post.
func (s *Postgres) RunGLBalances(ctx context.Context, idem Idem, in GLBalancesRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runGLBalances(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

func runGLBalances(ctx context.Context, tx pgx.Tx, in GLBalancesRun) (domain.PackRun, error) {
	run := in.Run
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableGLBalances); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	var openItems []domain.ControlTotal
	if in.OpenItemsRunID != "" {
		oi, err := getRun(ctx, tx, in.OpenItemsRunID, false)
		if errors.Is(err, ErrNotFound) {
			return domain.PackRun{}, fmt.Errorf("%w: openItemsRunId %s is not a pack run of this tenant", domain.ErrInvalid, in.OpenItemsRunID)
		}
		if err != nil {
			return domain.PackRun{}, err
		}
		if err := checkOpenItemsRun(oi, run); err != nil {
			return domain.PackRun{}, err
		}
		openItems = oi.Totals
		run.OpenItemsRunID = oi.RunID
	}

	idsIn := make([]string, len(in.Accepted))
	for i, g := range in.Accepted {
		idsIn[i] = g.LegacyID
	}
	held := map[string]domain.GLBalance{}
	rows, err := tx.Query(ctx, `select legacy_id, account, dimensions, currency, balance::text from gl_opening_balance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and legacy_id = any($3)`,
		sys, company, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read gl_opening_balance: %w", err)
	}
	for rows.Next() {
		var g domain.GLBalance
		var raw string
		if err := rows.Scan(&g.LegacyID, &g.Account, &g.Dimensions, &g.Currency, &raw); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		if g.Balance, err = decimal.NewFromString(raw); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		held[g.LegacyID] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	var toLoad []domain.GLBalance
	for _, g := range in.Accepted {
		if _, ok := held[g.LegacyID]; !ok {
			toLoad = append(toLoad, g)
		}
	}
	if err := checkNaturalKeys(ctx, tx, sys, company, toLoad); err != nil {
		return domain.PackRun{}, err
	}
	for _, g := range toLoad {
		held[g.LegacyID] = g
	}
	if err := domain.ReconcileGL(&run, in.Balances, in.Accepted, len(toLoad), held, openItems); err != nil {
		return domain.PackRun{}, err
	}
	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	batch := &pgx.Batch{}
	for _, g := range toLoad {
		xrefID, balanceID := ids.New(), ids.New()
		batch.Queue(`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'gl_opening_balance', $6, $7, $8, $9)`,
			xrefID, sys, company, domain.TableGLBalances, g.LegacyID, balanceID, run.RunID, g.Balance.String(), g.Currency)
		batch.Queue(`insert into gl_opening_balance (tenant_id, balance_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
			account, dimensions, currency, balance)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			balanceID, run.RunID, xrefID, sys, company, g.LegacyID, g.Account, g.Dimensions, g.Currency, g.Balance.String())
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return domain.PackRun{}, fmt.Errorf("insert gl opening balances: %w", err)
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

// checkOpenItemsRun accepts an open_items run of the same source system and company that has
// loaded its records (LOADED or RECONCILED) as the BR-49.2 counterpart of a gl_balances run.
func checkOpenItemsRun(oi, gl domain.PackRun) error {
	switch {
	case oi.Pack != domain.PackOpenItems:
		return fmt.Errorf("%w: openItemsRunId %s is a %s run, not open_items", domain.ErrInvalid, oi.RunID, oi.Pack)
	case oi.SourceSystem != gl.SourceSystem || oi.SourceCompany != gl.SourceCompany:
		return fmt.Errorf("%w: open_items run %s is for %s %s, this extract for %s %s", domain.ErrInvalid, oi.RunID,
			oi.SourceSystem, oi.SourceCompany, gl.SourceSystem, gl.SourceCompany)
	case oi.Status != domain.StatusLoaded && oi.Status != domain.StatusReconciled:
		return fmt.Errorf("%w: open_items run %s is %s; it must have loaded its open items", domain.ErrInvalid, oi.RunID, oi.Status)
	}
	return nil
}

// checkNaturalKeys refuses balances whose (account, dimensions, currency) Passage already holds
// for the company under another legacy id: loading them would put the opening balance in twice
// (FR-49.10). One conflict rejects the run before anything is written.
func checkNaturalKeys(ctx context.Context, tx pgx.Tx, sys, company string, toLoad []domain.GLBalance) error {
	if len(toLoad) == 0 {
		return nil
	}
	accounts, dims, currencies := make([]string, len(toLoad)), make([]string, len(toLoad)), make([]string, len(toLoad))
	for i, g := range toLoad {
		accounts[i], dims[i], currencies[i] = g.Account, g.Dimensions, g.Currency
	}
	var account, dim, currency, legacyID, runID string
	err := tx.QueryRow(ctx, `select b.account, b.dimensions, b.currency, b.legacy_id, b.run_id::text
		from gl_opening_balance b
		join unnest($3::text[], $4::text[], $5::text[]) as k(account, dimensions, currency)
		  on b.account = k.account and b.dimensions = k.dimensions and b.currency = k.currency
		where b.tenant_id = current_setting('passage.tenant_id', true)::uuid and b.source_system = $1 and b.source_company = $2
		order by b.account, b.dimensions, b.currency limit 1`,
		sys, company, accounts, dims, currencies).Scan(&account, &dim, &currency, &legacyID, &runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check gl_opening_balance keys: %w", err)
	}
	return fmt.Errorf("%w: account %s dimensions %q currency %s already has an opening balance, loaded as legacy id %s by run %s; re-extract with the same legacy ids",
		domain.ErrInvalid, account, dim, currency, legacyID, runID)
}
