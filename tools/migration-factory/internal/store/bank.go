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

// BankRun is a validated bank_balances extract ready to load (FR-49.1). Run carries the identity,
// source, cutoff, input hash, counts and quarantine; its status is VALIDATED. Lines is the whole
// extract and Accepted the lines that passed the suite.
type BankRun struct {
	Run       domain.PackRun
	Lines     []domain.BankBalanceLine
	Accepted  []domain.BankBalanceLine
	StartedBy string
}

// RunBankBalances loads the accepted lines that Passage does not hold yet, each with its
// legacy_xref row and its bank_opening_balance row, reconciles the run and stores it with its
// totals, quarantine and the pack.reconciled event, in one transaction. Runs of one (system,
// company) serialize on an advisory lock. Nothing posts to treasury here.
func (s *Postgres) RunBankBalances(ctx context.Context, idem Idem, in BankRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runBankBalances(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

func runBankBalances(ctx context.Context, tx pgx.Tx, in BankRun) (domain.PackRun, error) {
	run := in.Run
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableBank); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	idsIn := make([]string, len(in.Accepted))
	for i, b := range in.Accepted {
		idsIn[i] = b.LegacyID
	}
	held := map[string]domain.BankBalanceLine{}
	rows, err := tx.Query(ctx, `select legacy_id, bank_account, currency, balance::text from bank_opening_balance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and legacy_id = any($3)`,
		sys, company, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read bank_opening_balance: %w", err)
	}
	for rows.Next() {
		var b domain.BankBalanceLine
		var bal string
		if err := rows.Scan(&b.LegacyID, &b.BankAccount, &b.Currency, &bal); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		if b.Balance, err = decimal.NewFromString(bal); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		held[b.LegacyID] = b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	var toLoad []domain.BankBalanceLine
	for _, b := range in.Accepted {
		if _, ok := held[b.LegacyID]; !ok {
			toLoad = append(toLoad, b)
		}
	}
	if err := checkBankAccounts(ctx, tx, sys, company, toLoad); err != nil {
		return domain.PackRun{}, err
	}
	for _, b := range toLoad {
		held[b.LegacyID] = b
	}
	if err := domain.ReconcileBankBalances(&run, in.Lines, in.Accepted, len(toLoad), held); err != nil {
		return domain.PackRun{}, err
	}
	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	batch := &pgx.Batch{}
	for _, b := range toLoad {
		xrefID, balanceID := ids.New(), ids.New()
		batch.Queue(`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'bank_opening_balance', $6, $7, $8, $9)`,
			xrefID, sys, company, domain.TableBank, b.LegacyID, balanceID, run.RunID, b.Balance.String(), b.Currency)
		batch.Queue(`insert into bank_opening_balance (tenant_id, balance_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
			bank_account, currency, balance)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			balanceID, run.RunID, xrefID, sys, company, b.LegacyID, b.BankAccount, b.Currency, b.Balance.String())
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return domain.PackRun{}, fmt.Errorf("insert bank opening balances: %w", err)
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

// checkBankAccounts refuses a line whose bank account Passage already holds for the company under
// another legacy id: loading it would open the account twice (FR-49.10). One conflict rejects the
// run before anything is written.
func checkBankAccounts(ctx context.Context, tx pgx.Tx, sys, company string, toLoad []domain.BankBalanceLine) error {
	if len(toLoad) == 0 {
		return nil
	}
	accounts := make([]string, len(toLoad))
	for i, b := range toLoad {
		accounts[i] = b.BankAccount
	}
	var account, legacyID, runID string
	err := tx.QueryRow(ctx, `select bank_account, legacy_id, run_id::text from bank_opening_balance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and bank_account = any($3)
		order by bank_account limit 1`, sys, company, accounts).Scan(&account, &legacyID, &runID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check bank_opening_balance accounts: %w", err)
	}
	return fmt.Errorf("%w: bank account %s already has an opening balance, loaded as legacy id %s by run %s; re-extract with the same legacy ids",
		domain.ErrInvalid, account, legacyID, runID)
}
