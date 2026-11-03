package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// Release is a request to correct and release one quarantined record of a run (FR-49.11).
// Parties are master-data ids known now, checked together with the run's snapshot.
// ExpectedVersion 0 skips the optimistic version check.
type Release struct {
	RunID string
	Seq   int
	Fix   domain.Correction
	// BankFix and OnhandFix correct a bank_balances or onhand line; each applies to its pack only.
	BankFix   domain.BankCorrection
	OnhandFix domain.OnhandCorrection
	// Parties are the master-data ids known now: parties for open_items, bank accounts for
	// bank_balances, items for onhand.
	Parties         []string
	ExpectedVersion int
	By              string
}

// ReleaseQuarantined loads one corrected quarantined record under the run's row lock: it writes
// the record's legacy_xref row, marks the quarantine row RELEASED with the corrected record,
// updates the run's counts, totals and version, and writes pack.reconciled when the release
// completes the reconciliation. A record already released returns the run unchanged with
// replayed=true (idempotent by record).
func (s *Postgres) ReleaseQuarantined(ctx context.Context, idem Idem, rq Release) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		var already bool
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.PackRun, error) {
			run, done, err := release(ctx, tx, rq)
			already = done
			return run, err
		})
		replayed = replayed || already
		return err
	})
	return out, replayed, err
}

func release(ctx context.Context, tx pgx.Tx, rq Release) (domain.PackRun, bool, error) {
	run, err := getRun(ctx, tx, rq.RunID, true)
	if err != nil {
		return domain.PackRun{}, false, err
	}
	idx := -1
	for i, q := range run.Quarantined {
		if q.Seq == rq.Seq {
			idx = i
		}
	}
	if idx < 0 {
		return domain.PackRun{}, false, fmt.Errorf("%w: run has no quarantined record %d", ErrNotFound, rq.Seq)
	}
	q := run.Quarantined[idx]
	if q.Status == domain.QuarantineReleased {
		return run, true, nil
	}
	if rq.ExpectedVersion != 0 && rq.ExpectedVersion != run.Version {
		return domain.PackRun{}, false, fmt.Errorf("%w: run is at version %d, request expected %d", ErrVersion, run.Version, rq.ExpectedVersion)
	}
	master := make(map[string]struct{}, len(run.Parties)+len(rq.Parties))
	for _, p := range append(append([]string{}, run.Parties...), rq.Parties...) {
		master[p] = struct{}{}
	}
	before := len(run.Totals)
	original := append([]domain.ControlTotal(nil), run.Totals...)
	var rec domain.Record
	switch run.Pack {
	case domain.PackOpenItems:
		if !rq.BankFix.IsZero() || !rq.OnhandFix.IsZero() {
			return domain.PackRun{}, false, fmt.Errorf("%w: an open_items record is corrected by party and account only", domain.ErrInvalid)
		}
		rec, err = releaseOpenItem(ctx, tx, &run, q, rq, master)
	case domain.PackBankBal:
		if !rq.Fix.IsZero() || !rq.OnhandFix.IsZero() {
			return domain.PackRun{}, false, fmt.Errorf("%w: a bank_balances line is corrected by bankAccount only", domain.ErrInvalid)
		}
		rec, err = releaseBank(ctx, tx, &run, q, rq, master)
	case domain.PackOnhand:
		if !rq.Fix.IsZero() || !rq.BankFix.IsZero() {
			return domain.PackRun{}, false, fmt.Errorf("%w: an onhand line is corrected by item, site, warehouse, location, batch and unit only", domain.ErrInvalid)
		}
		rec, err = releaseOnhand(ctx, tx, &run, q, rq, master)
	default:
		return domain.PackRun{}, false, fmt.Errorf("%w: pack %s has no record release; open_items, bank_balances and onhand do", domain.ErrUnknownPack, run.Pack)
	}
	if err != nil {
		return domain.PackRun{}, false, err
	}

	now := time.Now().UTC()
	released, err := json.Marshal(recordJSON(rec))
	if err != nil {
		return domain.PackRun{}, false, err
	}
	if _, err := tx.Exec(ctx, `update pack_quarantine set status = $3, released_record = $4, released_by = $5, released_at = $6
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and seq = $2`,
		run.RunID, q.Seq, domain.QuarantineReleased, released, rq.By, now); err != nil {
		return domain.PackRun{}, false, fmt.Errorf("update pack_quarantine: %w", err)
	}
	q.Status, q.Released, q.ReleasedBy, q.ReleasedAt = domain.QuarantineReleased, &rec, rq.By, &now
	run.Quarantined[idx] = q

	for i, t := range run.Totals[:before] {
		if t.Source.Equal(original[i].Source) && t.Target.Equal(original[i].Target) {
			continue
		}
		tag, err := tx.Exec(ctx, `update control_total set source_value = $3, target_value = $4, delta = $5, status = $6, computed_at = now()
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and seq = $2 and name = $7`,
			run.RunID, i+1, t.Source.String(), t.Target.String(), t.Delta.String(), string(t.Status), t.Name)
		if err != nil {
			return domain.PackRun{}, false, fmt.Errorf("update control_total: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return domain.PackRun{}, false, fmt.Errorf("update control_total %d of run %s: %d rows", i+1, run.RunID, tag.RowsAffected())
		}
	}
	if err := insertTotals(ctx, tx, run.RunID, run.Totals[before:], before); err != nil {
		return domain.PackRun{}, false, err
	}

	run.Version++
	if _, err := tx.Exec(ctx, `update pack_run set status = $2, records_loaded = $3, records_quarantined = $4, version = $5
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
		run.RunID, string(run.Status), run.RecordsLoaded, run.RecordsQuarantined, run.Version); err != nil {
		return domain.PackRun{}, false, fmt.Errorf("update pack_run: %w", err)
	}
	if run.Status == domain.StatusReconciled {
		if err := enqueueReconciled(ctx, tx, run); err != nil {
			return domain.PackRun{}, false, err
		}
	}
	return run, false, nil
}

// lockXref serializes the loads of one (system, company, source table), as a run does.
func lockXref(ctx context.Context, tx pgx.Tx, run domain.PackRun, table string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		string(run.SourceSystem), run.SourceCompany, table); err != nil {
		return fmt.Errorf("lock legacy_xref: %w", err)
	}
	return nil
}

// insertXref writes the line's legacy_xref row; a legacy id the cross-reference already holds
// cannot be released again (a line quarantined as a repeat of an id that loaded).
func insertXref(ctx context.Context, tx pgx.Tx, run domain.PackRun, table, entity, legacyID, xrefID, passageID, amount, currency string) error {
	tag, err := tx.Exec(ctx, `insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
		passage_entity, passage_id, run_id, amount, currency)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		on conflict do nothing`,
		xrefID, string(run.SourceSystem), run.SourceCompany, table, legacyID, entity, passageID, run.RunID, amount, currency)
	if err != nil {
		return fmt.Errorf("insert legacy_xref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: legacy id %s is already loaded by cross-reference; it cannot be released again", domain.ErrTransition, legacyID)
	}
	return nil
}

func releaseOpenItem(ctx context.Context, tx pgx.Tx, run *domain.PackRun, q domain.Quarantined, rq Release, parties map[string]struct{}) (domain.Record, error) {
	rec, err := domain.ReleaseOpenItem(run, q.Record, rq.Fix, parties)
	if err != nil {
		return domain.Record{}, err
	}
	if err := lockXref(ctx, tx, *run, domain.TableOpenItems); err != nil {
		return domain.Record{}, err
	}
	if err := insertXref(ctx, tx, *run, domain.OpenItemsTable(rec.Side()), "open_item", rec.LegacyID, ids.New(), ids.New(), rec.Amount.String(), rec.Currency); err != nil {
		return domain.Record{}, err
	}
	return rec, nil
}

// quarantinedLine reads the whole extracted line the quarantine row kept. A row quarantined before
// the line was kept (0017) has none: the run is re-extracted instead.
func quarantinedLine(ctx context.Context, tx pgx.Tx, runID string, seq int, into any) error {
	var raw []byte
	err := tx.QueryRow(ctx, `select line from pack_quarantine
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and seq = $2`, runID, seq).Scan(&raw)
	if err != nil {
		return fmt.Errorf("read pack_quarantine line: %w", err)
	}
	if raw == nil {
		return fmt.Errorf("%w: quarantined record %d was stored before its whole line was kept; re-extract and run the pack again", domain.ErrTransition, seq)
	}
	return json.Unmarshal(raw, into)
}

func releaseBank(ctx context.Context, tx pgx.Tx, run *domain.PackRun, q domain.Quarantined, rq Release, accounts map[string]struct{}) (domain.Record, error) {
	var orig domain.BankBalanceLine
	if err := quarantinedLine(ctx, tx, run.RunID, q.Seq, &orig); err != nil {
		return domain.Record{}, err
	}
	line, err := domain.ReleaseBankBalance(run, orig, rq.BankFix, accounts)
	if err != nil {
		return domain.Record{}, err
	}
	sys, company := string(run.SourceSystem), run.SourceCompany
	if err := lockXref(ctx, tx, *run, domain.TableBank); err != nil {
		return domain.Record{}, err
	}
	if err := checkBankAccounts(ctx, tx, sys, company, []domain.BankBalanceLine{line}); err != nil {
		return domain.Record{}, err
	}
	xrefID, balanceID := ids.New(), ids.New()
	if err := insertXref(ctx, tx, *run, domain.TableBank, "bank_opening_balance", line.LegacyID, xrefID, balanceID, line.Balance.String(), line.Currency); err != nil {
		return domain.Record{}, err
	}
	if _, err := tx.Exec(ctx, `insert into bank_opening_balance (tenant_id, balance_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
		bank_account, currency, balance)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		balanceID, run.RunID, xrefID, sys, company, line.LegacyID, line.BankAccount, line.Currency, line.Balance.String()); err != nil {
		return domain.Record{}, fmt.Errorf("insert bank opening balance: %w", err)
	}
	return line.Record(), nil
}

func releaseOnhand(ctx context.Context, tx pgx.Tx, run *domain.PackRun, q domain.Quarantined, rq Release, items map[string]struct{}) (domain.Record, error) {
	var orig domain.OnhandLine
	if err := quarantinedLine(ctx, tx, run.RunID, q.Seq, &orig); err != nil {
		return domain.Record{}, err
	}
	line, err := domain.ReleaseOnhand(run, orig, rq.OnhandFix, items)
	if err != nil {
		return domain.Record{}, err
	}
	sys, company := string(run.SourceSystem), run.SourceCompany
	if err := lockXref(ctx, tx, *run, domain.TableOnhand); err != nil {
		return domain.Record{}, err
	}
	if err := checkOnhandKeys(ctx, tx, sys, company, []domain.OnhandLine{line}); err != nil {
		return domain.Record{}, err
	}
	xrefID, balanceID := ids.New(), ids.New()
	if err := insertXref(ctx, tx, *run, domain.TableOnhand, "onhand_opening_balance", line.LegacyID, xrefID, balanceID, line.Value.String(), line.Currency); err != nil {
		return domain.Record{}, err
	}
	if _, err := tx.Exec(ctx, `insert into onhand_opening_balance (tenant_id, balance_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
		item, site, warehouse, location, batch, serial, unit, quantity, value, currency)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		balanceID, run.RunID, xrefID, sys, company, line.LegacyID, line.Item, line.Site, line.Warehouse, line.Location, line.Batch, line.Serial,
		line.Unit, line.Quantity.String(), line.Value.String(), line.Currency); err != nil {
		return domain.Record{}, fmt.Errorf("insert onhand opening balance: %w", err)
	}
	return line.Record(), nil
}
