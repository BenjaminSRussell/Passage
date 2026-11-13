package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// ErrNoOpeningLoad means the run has not been loaded: it has no opening_load row yet.
var ErrNoOpeningLoad = errors.New("the run has no opening load")

// ErrNotLoadable means the run is not an onhand or bank_balances run that reconciled its staged balances.
var ErrNotLoadable = errors.New("the run cannot be loaded into a target")

const openingColumns = `run_id::text, target, legal_entity_id::text, status, staged, loaded, rejected, pending, attempts, last_error, started_by, started_at, finished_at, version, created_at`

func scanOpeningLoad(row pgx.Row) (domain.OpeningLoad, error) {
	var l domain.OpeningLoad
	err := row.Scan(&l.RunID, &l.Target, &l.LegalEntityID, &l.Status, &l.Staged, &l.Loaded, &l.Rejected, &l.Pending, &l.Attempts, &l.LastError, &l.StartedBy, &l.StartedAt, &l.FinishedAt, &l.Version, &l.CreatedAt)
	return l, err
}

// GetOpeningLoad returns the run's load or ErrNoOpeningLoad.
func (s *Postgres) GetOpeningLoad(ctx context.Context, runID string) (domain.OpeningLoad, error) {
	var l domain.OpeningLoad
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		l, err = scanOpeningLoad(tx.QueryRow(ctx, `select `+openingColumns+` from opening_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOpeningLoad
		}
		return err
	})
	return l, err
}

// PendingOpening is a staged balance the target does not hold yet; one of Onhand and Bank is set.
type PendingOpening struct {
	BalanceID string
	Onhand    *domain.OnhandLine
	Bank      *domain.BankBalanceLine
}

// OpeningRun names what the load needs of the run.
type OpeningRun struct {
	Pack    domain.Pack
	System  string
	Company string
}

func stagedQuery(pack domain.Pack) (string, error) {
	switch pack {
	case domain.PackOnhand:
		return `select o.balance_id::text, o.legacy_id, o.item, o.site, o.warehouse, o.location, o.batch, o.serial, o.unit, o.currency, o.quantity::text, o.value::text from onhand_opening_balance o`, nil
	case domain.PackBankBal:
		return `select o.balance_id::text, o.legacy_id, o.bank_account, o.currency, o.balance::text from bank_opening_balance o`, nil
	}
	return "", fmt.Errorf("%w: pack %s has no target", ErrNotLoadable, pack)
}

// ClaimOpeningLoad starts a pass over the run's staged balances: it creates or reopens the run's opening_load, refuses it
// while another pass holds the lease or a run of another legal entity owns it, and returns the staged balances of the
// run that the target does not hold yet, in legacy id order.
func (s *Postgres) ClaimOpeningLoad(ctx context.Context, runID, legalEntityID, startedBy string, lease time.Duration) (domain.OpeningLoad, OpeningRun, []PlannedOpening, []PendingOpening, error) {
	var load domain.OpeningLoad
	var run OpeningRun
	var pending []PendingOpening
	var planned []PlannedOpening
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var pack, status string
		err := tx.QueryRow(ctx, `select pack, source_system, source_company, status from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID).
			Scan(&pack, &run.System, &run.Company, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		run.Pack = domain.Pack(pack)
		target := domain.TargetOfPack(run.Pack)
		if target == "" {
			return fmt.Errorf("%w: pack %s has no target; only onhand and bank_balances runs are loaded", ErrNotLoadable, pack)
		}
		if domain.RunStatus(status) != domain.StatusReconciled {
			return fmt.Errorf("%w: run %s is %s; only a RECONCILED run is loaded", ErrNotLoadable, runID, status)
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('opening|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`, pack, run.System, run.Company); err != nil {
			return fmt.Errorf("lock opening load: %w", err)
		}
		query, err := stagedQuery(run.Pack)
		if err != nil {
			return err
		}
		var staged int
		if err := tx.QueryRow(ctx, `select count(*) from (`+query+` where o.tenant_id = current_setting('passage.tenant_id', true)::uuid and o.run_id = $1) x`, runID).Scan(&staged); err != nil {
			return err
		}
		var leaseUntil *time.Time
		var held string
		var heldStatus string
		err = tx.QueryRow(ctx, `select status, lease_until, legal_entity_id::text from opening_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID).Scan(&heldStatus, &leaseUntil, &held)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `insert into opening_load (tenant_id, run_id, target, legal_entity_id, status, staged) values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, 'STAGED', $4)`,
				runID, target, legalEntityID, staged); err != nil {
				return scopeErr(err)
			}
		case err != nil:
			return err
		default:
			if held != legalEntityID {
				return fmt.Errorf("%w: run %s is loaded into legal entity %s", domain.ErrInvalid, runID, held)
			}
			if heldStatus == domain.LoadLoading && leaseUntil != nil && leaseUntil.After(time.Now()) {
				return ErrLoadBusy
			}
		}
		var busy bool
		if err := tx.QueryRow(ctx, `select coalesce(bool_or(o.status = 'LOADING' and o.lease_until > now()), false) from opening_load o join pack_run p on p.tenant_id = o.tenant_id and p.run_id = o.run_id
			where o.tenant_id = current_setting('passage.tenant_id', true)::uuid and o.run_id <> $1 and p.pack = $2 and p.source_system = $3 and p.source_company = $4`, runID, pack, run.System, run.Company).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ErrLoadBusy
		}
		rows, err := tx.Query(ctx, query+` where o.tenant_id = current_setting('passage.tenant_id', true)::uuid and o.run_id = $1
			and not exists (select 1 from opening_load_row l where l.tenant_id = o.tenant_id and l.balance_id = o.balance_id and l.status = 'LOADED')
			and not exists (select 1 from opening_load_batch_row br join opening_load_batch b on b.tenant_id = br.tenant_id and b.run_id = br.run_id and b.batch_key = br.batch_key
				where br.tenant_id = o.tenant_id and br.balance_id = o.balance_id and b.status = 'PLANNED') order by o.legacy_id`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			p, err := scanPendingOpening(rows, run.Pack)
			if err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		planned, err = readPlanned(ctx, tx, runID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update opening_load set status = 'LOADING', staged = $2, lease_until = now() + $3::int * interval '1 second', started_by = $4, started_at = now(), finished_at = null,
				attempts = attempts + 1, last_error = '', version = version + 1
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, staged, int32(lease.Seconds()), startedBy); err != nil {
			return err
		}
		load, err = scanOpeningLoad(tx.QueryRow(ctx, `select `+openingColumns+` from opening_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, run, planned, pending, err
}

// PlannedOpening is a batch that was planned and not yet known to be taken: it is sent again exactly as it was.
type PlannedOpening struct {
	Key         string
	Currency    string
	PostingDate string
	Payload     json.RawMessage
	BalanceIDs  []string
}

func readPlanned(ctx context.Context, tx pgx.Tx, runID string) ([]PlannedOpening, error) {
	rows, err := tx.Query(ctx, `select batch_key, currency, posting_date, payload from opening_load_batch
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and status = 'PLANNED' order by batch_key`, runID)
	if err != nil {
		return nil, err
	}
	var out []PlannedOpening
	for rows.Next() {
		var p PlannedOpening
		if err := rows.Scan(&p.Key, &p.Currency, &p.PostingDate, &p.Payload); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ids, err := tx.Query(ctx, `select balance_id::text from opening_load_batch_row where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and batch_key = $2 order by seq`, runID, out[i].Key)
		if err != nil {
			return nil, err
		}
		for ids.Next() {
			var id string
			if err := ids.Scan(&id); err != nil {
				ids.Close()
				return nil, err
			}
			out[i].BalanceIDs = append(out[i].BalanceIDs, id)
		}
		ids.Close()
		if err := ids.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// PlanOpeningBatch stores a batch as PLANNED before it is sent, with its items exactly as sent. Planning the same key again is a no-op.
func (s *Postgres) PlanOpeningBatch(ctx context.Context, runID, legalEntityID string, lease time.Duration, p PlannedOpening) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendOpeningLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into opening_load_batch (tenant_id, run_id, batch_key, legal_entity_id, item_count, status, currency, posting_date, payload)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, 'PLANNED', $5, $6, $7) on conflict do nothing`,
			runID, p.Key, legalEntityID, len(p.BalanceIDs), p.Currency, p.PostingDate, []byte(p.Payload))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		for i, id := range p.BalanceIDs {
			if _, err := tx.Exec(ctx, `insert into opening_load_batch_row (tenant_id, run_id, batch_key, seq, balance_id) values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4)`, runID, p.Key, i+1, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropOpeningPlan marks a planned batch DROPPED: the target refused it whole and stored nothing, so its rows are formed into new batches.
func (s *Postgres) DropOpeningPlan(ctx context.Context, runID, key string) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update opening_load_batch set status = 'DROPPED' where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and batch_key = $2 and status = 'PLANNED'`, runID, key)
		return err
	})
}

func scanPendingOpening(rows pgx.Rows, pack domain.Pack) (PendingOpening, error) {
	var p PendingOpening
	switch pack {
	case domain.PackOnhand:
		var o domain.OnhandLine
		var qty, val string
		if err := rows.Scan(&p.BalanceID, &o.LegacyID, &o.Item, &o.Site, &o.Warehouse, &o.Location, &o.Batch, &o.Serial, &o.Unit, &o.Currency, &qty, &val); err != nil {
			return p, err
		}
		var err error
		if o.Quantity, err = decimal.NewFromString(qty); err != nil {
			return p, err
		}
		if o.Value, err = decimal.NewFromString(val); err != nil {
			return p, err
		}
		p.Onhand = &o
	default:
		var b domain.BankBalanceLine
		var bal string
		if err := rows.Scan(&p.BalanceID, &b.LegacyID, &b.BankAccount, &b.Currency, &bal); err != nil {
			return p, err
		}
		var err error
		if b.Balance, err = decimal.NewFromString(bal); err != nil {
			return p, err
		}
		p.Bank = &b
	}
	return p, nil
}

// scopeErr maps a row-level security refusal to the out-of-scope error.
func scopeErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "row-level security") {
		return fmt.Errorf("%w: the legal entity is outside your scope", domain.ErrInvalid)
	}
	return err
}

// OpeningBatchSent is one accepted batch and the staged balances in it.
type OpeningBatchSent struct {
	BatchKey   string
	Replayed   bool
	BalanceIDs []string
}

// RecordOpeningBatch stores an accepted batch: its balances are LOADED. It also extends the lease.
func (s *Postgres) RecordOpeningBatch(ctx context.Context, runID, legalEntityID string, lease time.Duration, b OpeningBatchSent) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendOpeningLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into opening_load_batch (tenant_id, run_id, batch_key, legal_entity_id, item_count, replayed, status)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'ACCEPTED')
			on conflict (tenant_id, run_id, batch_key) do update set status = 'ACCEPTED', replayed = excluded.replayed`, runID, b.BatchKey, legalEntityID, len(b.BalanceIDs), b.Replayed); err != nil {
			return err
		}
		for _, id := range b.BalanceIDs {
			if _, err := tx.Exec(ctx, `insert into opening_load_row (tenant_id, run_id, balance_id, legal_entity_id, status, batch_key)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, 'LOADED', $4)
				on conflict (tenant_id, balance_id) do update set status = 'LOADED', batch_key = excluded.batch_key, reject_code = '', reject_detail = '', updated_at = now()`,
				runID, id, legalEntityID, b.BatchKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// OpeningReject is a staged balance the factory or the target refused, with the reason.
type OpeningReject struct {
	BalanceID string
	Code      string
	Detail    string
}

// RejectOpeningRows marks balances REJECTED; a later load retries them (their mapping may have been added).
func (s *Postgres) RejectOpeningRows(ctx context.Context, runID, legalEntityID string, lease time.Duration, rejects []OpeningReject) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendOpeningLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		for _, r := range rejects {
			if _, err := tx.Exec(ctx, `insert into opening_load_row (tenant_id, run_id, balance_id, legal_entity_id, status, reject_code, reject_detail)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, 'REJECTED', $4, $5)
				on conflict (tenant_id, balance_id) do update set status = 'REJECTED', reject_code = excluded.reject_code, reject_detail = excluded.reject_detail, updated_at = now()
				where opening_load_row.status <> 'LOADED'`, runID, r.BalanceID, legalEntityID, r.Code, r.Detail); err != nil {
				return err
			}
		}
		return nil
	})
}

func extendOpeningLease(ctx context.Context, tx pgx.Tx, runID string, lease time.Duration) error {
	tag, err := tx.Exec(ctx, `update opening_load set lease_until = now() + $2::int * interval '1 second'
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and status = 'LOADING'`, runID, int32(lease.Seconds()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLoadBusy
	}
	return nil
}

// OpeningRejects lists the run's rejected balances with their legacy ids (first limit).
type OpeningRejectRow struct {
	LegacyID string
	Code     string
	Detail   string
}

// ListOpeningRejects returns the run's REJECTED balances in legacy id order.
func (s *Postgres) ListOpeningRejects(ctx context.Context, runID string, limit int) ([]OpeningRejectRow, error) {
	out := []OpeningRejectRow{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select coalesce(o.legacy_id, b.legacy_id), l.reject_code, l.reject_detail from opening_load_row l
			left join onhand_opening_balance o on o.tenant_id = l.tenant_id and o.balance_id = l.balance_id
			left join bank_opening_balance b on b.tenant_id = l.tenant_id and b.balance_id = l.balance_id
			where l.tenant_id = current_setting('passage.tenant_id', true)::uuid and l.run_id = $1 and l.status = 'REJECTED' order by 1 limit $2`, runID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r OpeningRejectRow
			if err := rows.Scan(&r.LegacyID, &r.Code, &r.Detail); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// OpeningFinish is how a pass ended: Fatal stops the load (the target refused the factory), Unavailable leaves the rest
// pending, ActualErr says the target's totals could not be read, else Actual is what the target holds.
type OpeningFinish struct {
	Fatal       string
	Unavailable string
	ActualErr   string
	Actual      domain.OpeningTotals
	// OnHand is what the inventory ledger's on-hand inquiry reports per product after an onhand load (H-S9-2); nil skips the
	// read-back.
	OnHand map[string]decimal.Decimal
}

// FinishOpeningLoad ends a pass: it recounts the rows, compares the batches the target holds with what the factory loaded
// for the pack of the company, stores the comparison as control totals and the status, and writes
// dev.passage.migration.pack.loaded.v1.
func (s *Postgres) FinishOpeningLoad(ctx context.Context, runID string, fin OpeningFinish) (domain.OpeningLoad, []domain.ControlTotal, error) {
	var load domain.OpeningLoad
	var totals []domain.ControlTotal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		load, err = scanOpeningLoad(tx.QueryRow(ctx, `select `+openingColumns+` from opening_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOpeningLoad
		}
		if err != nil {
			return err
		}
		var pack, system, company string
		if err := tx.QueryRow(ctx, `select pack, source_system, source_company from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&pack, &system, &company); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) filter (where status = 'LOADED'), count(*) filter (where status = 'REJECTED') from opening_load_row
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&load.Loaded, &load.Rejected); err != nil {
			return err
		}
		load.Pending = max(load.Staged-load.Loaded-load.Rejected, 0)
		load.LastError = ""
		switch {
		case fin.Fatal != "":
			load.Status, load.LastError = domain.LoadFailed, fin.Fatal
		case load.Pending > 0:
			load.Status, load.LastError = domain.LoadPartial, fin.Unavailable
		case fin.ActualErr != "":
			load.Status, load.LastError = domain.LoadUnreconciled, fin.ActualErr
		default:
			expected, err := expectedOpeningTotals(ctx, tx, domain.Pack(pack), system, company, load.LegalEntityID)
			if err != nil {
				return err
			}
			if load.Target == domain.TargetInventory {
				totals, err = domain.ReconcileStock(load.LegalEntityID, expected, fin.Actual)
				if err == nil && fin.OnHand != nil {
					var loaded map[string]decimal.Decimal
					if loaded, err = openingOnHandExpected(ctx, tx, load.LegalEntityID); err != nil {
						return err
					}
					var readBack []domain.ControlTotal
					if readBack, err = domain.ReconcileOnHand(load.LegalEntityID, loaded, fin.OnHand); err == nil {
						totals = append(totals, readBack...)
					}
				}
			} else {
				totals, err = domain.ReconcileBank(load.LegalEntityID, expected, fin.Actual)
			}
			if err != nil {
				return err
			}
			if err := upsertTargetTotals(ctx, tx, runID, totals); err != nil {
				return err
			}
			load.Status = domain.LoadReconciled
			if _, mismatch := domain.CountTotals(totals); mismatch > 0 {
				load.Status = domain.LoadMismatch
			}
		}
		if _, err := tx.Exec(ctx, `update opening_load set status = $2, loaded = $3, rejected = $4, pending = $5, last_error = $6, lease_until = null, finished_at = now(), version = version + 1
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, load.Status, load.Loaded, load.Rejected, load.Pending, load.LastError); err != nil {
			return err
		}
		match, mismatch := domain.CountTotals(totals)
		if err := enqueue(ctx, tx, mfevents.PackLoaded, "pack_run", runID, mfevents.PackLoadedData{
			RunID: runID, Pack: pack, Target: load.Target, ImportSource: pack + "/" + company, LegalEntityID: load.LegalEntityID, Status: load.Status,
			Created: load.Loaded, Rejected: load.Rejected, Pending: load.Pending, TotalsMatch: match, TotalsMismatch: mismatch,
		}); err != nil {
			return fmt.Errorf("enqueue pack.loaded: %w", err)
		}
		load, err = scanOpeningLoad(tx.QueryRow(ctx, `select `+openingColumns+` from opening_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, totals, err
}

// OpeningOnHandExpected is the quantity per Passage product that the factory's accepted onhand batches put in the legal entity,
// from every run and company (the on-hand inquiry does not tell them apart).
func (s *Postgres) OpeningOnHandExpected(ctx context.Context, legalEntityID string) (map[string]decimal.Decimal, error) {
	var out map[string]decimal.Decimal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = openingOnHandExpected(ctx, tx, legalEntityID)
		return err
	})
	return out, err
}

func openingOnHandExpected(ctx context.Context, tx pgx.Tx, legalEntityID string) (map[string]decimal.Decimal, error) {
	rows, err := tx.Query(ctx, `select b.payload from opening_load_batch b join pack_run r on r.tenant_id = b.tenant_id and r.run_id = b.run_id
		where b.tenant_id = current_setting('passage.tenant_id', true)::uuid and b.legal_entity_id = $1 and b.status = 'ACCEPTED' and r.pack = $2 and b.payload is not null`,
		legalEntityID, string(domain.PackOnhand))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]decimal.Decimal{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var items []domain.StockItem
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("opening batch payload: %w", err)
		}
		for _, it := range items {
			q, err := decimal.NewFromString(it.Qty)
			if err != nil {
				return nil, fmt.Errorf("opening batch item %s: %w", it.Reference, err)
			}
			p := strings.ToLower(it.ProductID)
			out[p] = out[p].Add(q)
		}
	}
	return out, rows.Err()
}

// expectedOpeningTotals sums the balances of the company that the target holds as the factory loaded them, across every
// run of the pack (a balance is staged once per company).
func expectedOpeningTotals(ctx context.Context, tx pgx.Tx, pack domain.Pack, system, company, legalEntityID string) (domain.OpeningTotals, error) {
	out := domain.NewOpeningTotals()
	query, err := stagedQuery(pack)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, query+` join opening_load_row l on l.tenant_id = o.tenant_id and l.balance_id = o.balance_id and l.status = 'LOADED'
		where o.tenant_id = current_setting('passage.tenant_id', true)::uuid and o.source_system = $1 and o.source_company = $2 and l.legal_entity_id = $3`, system, company, legalEntityID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanPendingOpening(rows, pack)
		if err != nil {
			return out, err
		}
		out.Items++
		switch {
		case p.Onhand != nil:
			out.Qty[p.Onhand.Currency] = out.Qty[p.Onhand.Currency].Add(p.Onhand.Quantity)
			out.Value[p.Onhand.Currency] = out.Value[p.Onhand.Currency].Add(domain.StockValue(*p.Onhand))
		case p.Bank != nil:
			minor, ok := domain.ToMinor(p.Bank.Balance, p.Bank.Currency)
			if !ok {
				return out, fmt.Errorf("staged balance %s is not a whole number of minor units", p.Bank.LegacyID)
			}
			out.Minor[p.Bank.Currency] += minor
		}
	}
	return out, rows.Err()
}
