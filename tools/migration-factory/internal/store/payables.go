package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// ErrNoPayablesLoad is returned when the run was not started with a payables load.
var ErrNoPayablesLoad = errors.New("the run has no payables load")

const payablesColumns = `run_id::text, legal_entity_id::text, import_source, status, staged, loaded, unchanged, skipped, rejected, pending,
	attempts, last_error, started_by, started_at, finished_at, version, created_at`

func scanPayablesLoad(row pgx.Row) (domain.PayablesLoad, error) {
	var l domain.PayablesLoad
	err := row.Scan(&l.RunID, &l.LegalEntityID, &l.ImportSource, &l.Status, &l.Staged, &l.Loaded, &l.Unchanged, &l.Skipped, &l.Rejected, &l.Pending,
		&l.Attempts, &l.LastError, &l.StartedBy, &l.StartedAt, &l.FinishedAt, &l.Version, &l.CreatedAt)
	return l, err
}

// stagePayables stores the AP side of a run's load header, in the run's transaction. The run keeps its
// payables_staged count (the go/no-go reads it) at every AP line until the load has reconciled.
func stagePayables(ctx context.Context, tx pgx.Tx, runID string, st *TargetStage) error {
	pending, rejected, total := 0, 0, 0
	for _, r := range st.Rows {
		if r.Ledger != domain.LedgerAP {
			continue
		}
		total++
		switch r.Status {
		case domain.RowPending:
			pending++
		case domain.RowRejected:
			rejected++
		}
	}
	if _, err := tx.Exec(ctx, `insert into payables_load (tenant_id, run_id, legal_entity_id, import_source, status, staged, rejected, pending)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7)`,
		runID, st.LegalEntityID, st.ImportSource, domain.LoadStaged, total, rejected, pending); err != nil {
		return fmt.Errorf("insert payables_load: %w", err)
	}
	if _, err := tx.Exec(ctx, `update pack_run set payables_staged = $2 where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, total); err != nil {
		return fmt.Errorf("update pack_run payables_staged: %w", err)
	}
	return nil
}

// GetPayablesLoad returns the AP load of a run, or ErrNoPayablesLoad when the run was started without one.
func (s *Postgres) GetPayablesLoad(ctx context.Context, runID string) (domain.PayablesLoad, error) {
	var l domain.PayablesLoad
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		l, err = scanPayablesLoad(tx.QueryRow(ctx, `select `+payablesColumns+` from payables_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := tx.QueryRow(ctx, `select exists (select 1 from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1)`, runID).Scan(&exists); qerr != nil {
				return qerr
			}
			if !exists {
				return ErrNotFound
			}
			return ErrNoPayablesLoad
		}
		return err
	})
	return l, err
}

// PendingPayable is a staged AP row still to send.
type PendingPayable struct {
	Seq      int
	LegacyID string
	Hash     string
	Row      domain.PayablesRow
}

// ClaimPayablesLoad takes the AP load's lease and returns the rows still to send, with the same rules as
// ClaimTargetLoad: a live lease, or a live load of another run of the same import source and legal entity, is
// ErrLoadBusy; a run older than one already loaded is ErrLoadSuperseded; rows equal to what payables already
// accepted under the import source are marked SKIPPED first, so a rerun of an unchanged snapshot sends nothing.
func (s *Postgres) ClaimPayablesLoad(ctx context.Context, runID, startedBy string, lease time.Duration) (domain.PayablesLoad, []PendingPayable, error) {
	var load domain.PayablesLoad
	var pending []PendingPayable
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var leaseUntil *time.Time
		var err error
		load, err = scanPayablesLoad(tx.QueryRow(ctx, `select `+payablesColumns+` from payables_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoPayablesLoad
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select lease_until from payables_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&leaseUntil); err != nil {
			return err
		}
		if load.Status == domain.LoadLoading && leaseUntil != nil && leaseUntil.After(time.Now()) {
			return ErrLoadBusy
		}
		var runStatus string
		if err := tx.QueryRow(ctx, `select status from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&runStatus); err != nil {
			return err
		}
		if domain.RunStatus(runStatus) == domain.StatusRolledBack {
			return fmt.Errorf("%w: run %s was rolled back with its cutover plan and is not loaded", domain.ErrTransition, runID)
		}
		if err := lockImportSource(ctx, tx, load.ImportSource); err != nil {
			return err
		}
		var busy, superseded bool
		if err := tx.QueryRow(ctx, `select
				coalesce(bool_or(o.status = 'LOADING' and o.lease_until > now()), false),
				coalesce(bool_or(o.attempts > 0 and o.created_at > l.created_at), false)
			from payables_load l join payables_load o on o.tenant_id = l.tenant_id and o.import_source = l.import_source
			  and o.legal_entity_id = l.legal_entity_id and o.run_id <> l.run_id
			where l.tenant_id = current_setting('passage.tenant_id', true)::uuid and l.run_id = $1`, runID).Scan(&busy, &superseded); err != nil {
			return fmt.Errorf("check other payables loads: %w", err)
		}
		if busy {
			return ErrLoadBusy
		}
		if superseded {
			return ErrLoadSuperseded
		}
		if _, err := tx.Exec(ctx, `update target_row r set status = 'SKIPPED', outcome = 'UNCHANGED', passage_id = l.batch_id, updated_at = now()
			from payables_loaded l
			where r.tenant_id = current_setting('passage.tenant_id', true)::uuid and r.run_id = $1 and r.status = 'PENDING' and r.ledger = 'AP'
			  and l.tenant_id = r.tenant_id and l.import_source = $2 and l.reference = r.legacy_id and l.row_hash = r.row_hash`, runID, load.ImportSource); err != nil {
			return fmt.Errorf("skip rows payables already holds: %w", err)
		}
		rows, err := tx.Query(ctx, `select seq, legacy_id, row_hash, payload from target_row
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and status = 'PENDING' and ledger = 'AP' order by seq`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p PendingPayable
			var raw []byte
			if err := rows.Scan(&p.Seq, &p.LegacyID, &p.Hash, &raw); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(raw, &p.Row); err != nil {
				rows.Close()
				return fmt.Errorf("staged AP row %q: %w", p.LegacyID, err)
			}
			pending = append(pending, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update payables_load set status = $2, lease_until = now() + $3::int * interval '1 second', started_by = $4, started_at = now(), finished_at = null,
				attempts = attempts + 1, last_error = '', version = version + 1
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, domain.LoadLoading, int32(lease.Seconds()), startedBy); err != nil {
			return err
		}
		load, err = scanPayablesLoad(tx.QueryRow(ctx, `select `+payablesColumns+` from payables_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, pending, err
}

// PayablesBatchSent is one accepted opening batch: the key it was sent under, the id payables stored it as, whether
// payables already held it, and the rows in it.
type PayablesBatchSent struct {
	BatchKey string
	BatchID  string
	Replayed bool
	Rows     []PendingPayable
}

// RecordPayablesBatch stores an accepted batch: its rows are LOADED (UNCHANGED when payables already held the batch),
// payables_loaded remembers what payables holds per reference, and payables_batch the batch for the reconciliation.
// It also extends the lease, so a long load keeps it.
func (s *Postgres) RecordPayablesBatch(ctx context.Context, runID string, lease time.Duration, b PayablesBatchSent) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		var importSource, le string
		if err := tx.QueryRow(ctx, `select import_source, legal_entity_id::text from payables_load
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID).Scan(&importSource, &le); err != nil {
			return fmt.Errorf("read payables_load: %w", err)
		}
		n := len(b.Rows)
		seqs, refs, curs, hashes := make([]int32, n), make([]string, n), make([]string, n), make([]string, n)
		minor := make([]int64, n)
		totals := map[string]int64{}
		for i, r := range b.Rows {
			seqs[i], refs[i], curs[i], hashes[i], minor[i] = int32(r.Seq), r.LegacyID, r.Row.Currency, r.Hash, r.Row.AmountMinor
			totals[r.Row.Currency] += r.Row.AmountMinor
		}
		status, outcome := domain.RowLoaded, "CREATED"
		if b.Replayed {
			status, outcome = domain.RowUnchanged, "UNCHANGED"
		}
		if _, err := tx.Exec(ctx, `update target_row set status = $3, outcome = $4, passage_id = $5::uuid, code = '', detail = '', updated_at = now()
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and ledger = 'AP' and seq = any($2::int[])`,
			runID, seqs, status, outcome, b.BatchID); err != nil {
			return fmt.Errorf("update target_row: %w", err)
		}
		if _, err := tx.Exec(ctx, `insert into payables_loaded (tenant_id, import_source, reference, legal_entity_id, currency, amount_minor, row_hash, batch_key, batch_id, run_id)
			select current_setting('passage.tenant_id', true)::uuid, $1, r, $2, c, a, h, $3, $4::uuid, $5
			from unnest($6::text[], $7::text[], $8::bigint[], $9::text[]) as t(r, c, a, h)
			on conflict (tenant_id, import_source, reference) do update
			set currency = excluded.currency, amount_minor = excluded.amount_minor, row_hash = excluded.row_hash, batch_key = excluded.batch_key,
			    batch_id = excluded.batch_id, run_id = excluded.run_id, loaded_at = now()`,
			importSource, le, b.BatchKey, b.BatchID, runID, refs, curs, minor, hashes); err != nil {
			return fmt.Errorf("upsert payables_loaded: %w", err)
		}
		raw, err := json.Marshal(totals)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into payables_batch (tenant_id, run_id, batch_key, import_source, legal_entity_id, batch_id, item_count, totals_minor, replayed)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5::uuid, $6, $7::jsonb, $8)
			on conflict (tenant_id, run_id, batch_key) do nothing`, runID, b.BatchKey, importSource, le, b.BatchID, n, string(raw), b.Replayed); err != nil {
			return fmt.Errorf("insert payables_batch: %w", err)
		}
		_, err = tx.Exec(ctx, `update payables_load set lease_until = now() + $2::int * interval '1 second' where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
			runID, int32(lease.Seconds()))
		return err
	})
}

// PayablesReject is a row payables refused, with its code and reason.
type PayablesReject struct {
	Seq    int
	Code   string
	Detail string
}

// RejectPayablesRows marks rows payables refused as REJECTED; the rest of their batch stays pending.
func (s *Postgres) RejectPayablesRows(ctx context.Context, runID string, lease time.Duration, rejects []PayablesReject) error {
	if len(rejects) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		seqs, codes, details := make([]int32, len(rejects)), make([]string, len(rejects)), make([]string, len(rejects))
		for i, r := range rejects {
			seqs[i], codes[i], details[i] = int32(r.Seq), r.Code, r.Detail
		}
		if _, err := tx.Exec(ctx, `update target_row r set status = 'REJECTED', outcome = 'REJECTED', code = t.c, detail = t.d, updated_at = now()
			from unnest($2::int[], $3::text[], $4::text[]) as t(s, c, d)
			where r.tenant_id = current_setting('passage.tenant_id', true)::uuid and r.run_id = $1 and r.ledger = 'AP' and r.seq = t.s and r.status = 'PENDING'`,
			runID, seqs, codes, details); err != nil {
			return fmt.Errorf("reject target_row: %w", err)
		}
		_, err := tx.Exec(ctx, `update payables_load set lease_until = now() + $2::int * interval '1 second' where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
			runID, int32(lease.Seconds()))
		return err
	})
}

// PayablesFinish is how one pass of the AP load ended.
type PayablesFinish struct {
	// Fatal is set when the load stopped because payables refused the factory or the request (status FAILED).
	Fatal string
	// Unavailable is set when payables stopped answering and rows are still pending (status PARTIAL).
	Unavailable string
	// ActualErr is set when payables' batches could not be read back (status UNRECONCILED).
	ActualErr string
	// Actual is what payables reports for the import source's batches, read after the last batch was sent.
	Actual domain.PayablesTotals
}

// FinishPayablesLoad ends a pass: it recounts the rows, compares the batches payables holds with what the factory
// sent, stores the comparison as control totals and the status, keeps the run's payables_staged count at every AP
// line until the load reconciled, and writes dev.passage.migration.pack.loaded.v1.
func (s *Postgres) FinishPayablesLoad(ctx context.Context, runID string, fin PayablesFinish) (domain.PayablesLoad, []domain.ControlTotal, error) {
	var load domain.PayablesLoad
	var totals []domain.ControlTotal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		load, err = scanPayablesLoad(tx.QueryRow(ctx, `select `+payablesColumns+` from payables_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if err != nil {
			return err
		}
		counts := map[string]int{}
		rows, err := tx.Query(ctx, `select status, outcome, count(*) from target_row where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and ledger = 'AP' group by status, outcome`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var st, outcome string
			var n int
			if err := rows.Scan(&st, &outcome, &n); err != nil {
				rows.Close()
				return err
			}
			counts[st] += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		load.Loaded, load.Unchanged, load.Skipped, load.Rejected, load.Pending = counts[domain.RowLoaded], counts[domain.RowUnchanged], counts[domain.RowSkipped], counts[domain.RowRejected], counts[domain.RowPending]
		load.LastError = ""
		switch {
		case fin.Fatal != "":
			load.Status, load.LastError = domain.LoadFailed, fin.Fatal
		case load.Pending > 0:
			load.Status, load.LastError = domain.LoadPartial, fin.Unavailable
		case fin.ActualErr != "":
			load.Status, load.LastError = domain.LoadUnreconciled, fin.ActualErr
		default:
			expected, err := expectedPayablesTotals(ctx, tx, load.ImportSource, load.LegalEntityID)
			if err != nil {
				return err
			}
			totals, err = domain.ReconcilePayables(load.ImportSource, load.LegalEntityID, expected, fin.Actual)
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
		if _, err := tx.Exec(ctx, `update payables_load set status = $2, loaded = $3, unchanged = $4, skipped = $5, rejected = $6, pending = $7, last_error = $8,
				lease_until = null, finished_at = now(), version = version + 1
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
			runID, load.Status, load.Loaded, load.Unchanged, load.Skipped, load.Rejected, load.Pending, load.LastError); err != nil {
			return err
		}
		// The go/no-go keeps open_items staged for AP until payables holds every line and its batches reconciled; a
		// rejected line stays staged, so only a waiver can excuse it.
		staged := load.Staged
		if load.Status == domain.LoadReconciled {
			staged = load.Rejected
		}
		if _, err := tx.Exec(ctx, `update pack_run set payables_staged = $2 where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, staged); err != nil {
			return fmt.Errorf("update pack_run payables_staged: %w", err)
		}
		match, mismatch := domain.CountTotals(totals)
		if err := enqueue(ctx, tx, mfevents.PackLoaded, "pack_run", runID, mfevents.PackLoadedData{
			RunID: runID, Pack: string(domain.PackOpenItems), Target: domain.TargetPayables, ImportSource: load.ImportSource, LegalEntityID: load.LegalEntityID, Status: load.Status,
			Created: load.Loaded, Unchanged: load.Unchanged, Skipped: load.Skipped, Rejected: load.Rejected, Pending: load.Pending, TotalsMatch: match, TotalsMismatch: mismatch,
		}); err != nil {
			return fmt.Errorf("enqueue pack.loaded: %w", err)
		}
		load, err = scanPayablesLoad(tx.QueryRow(ctx, `select `+payablesColumns+` from payables_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, totals, err
}

// expectedPayablesTotals sums the distinct batches the factory sent for the import source: a batch another run sent
// again (payables answered it as replayed) counts once, as payables holds it once.
func expectedPayablesTotals(ctx context.Context, tx pgx.Tx, importSource, le string) (domain.PayablesTotals, error) {
	out := domain.PayablesTotals{TotalMinor: map[string]int64{}}
	rows, err := tx.Query(ctx, `select distinct on (batch_key) item_count, totals_minor from payables_batch
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and import_source = $1 and legal_entity_id = $2 order by batch_key, sent_at`, importSource, le)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var raw []byte
		if err := rows.Scan(&n, &raw); err != nil {
			return out, err
		}
		var totals map[string]int64
		if err := json.Unmarshal(raw, &totals); err != nil {
			return out, err
		}
		out.Items += n
		for c, v := range totals {
			out.TotalMinor[c] += v
		}
	}
	return out, rows.Err()
}
