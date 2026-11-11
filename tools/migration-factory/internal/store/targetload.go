package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

var (
	// ErrLoadBusy is returned when another load of the run holds the lease.
	ErrLoadBusy = errors.New("the run is being loaded by another request")
	// ErrNoTarget is returned when the run was not started with a target.
	ErrNoTarget = errors.New("the run has no target load")
	// ErrLoadSuperseded is returned when a later run of the same import source and legal entity has already been
	// loaded: loading this older snapshot would write it back over the newer one.
	ErrLoadSuperseded = errors.New("a later run of this import source has already been loaded")
	// ErrImportSourceTaken is returned when another legacy company already loads under the import source: their
	// legacy keys would share one key space in receivables and its totals would mix both companies.
	ErrImportSourceTaken = errors.New("the import source is already used by another legacy company")
)

// lockImportSource serializes the staging and the claim of loads of one import source in the tenant, so the checks
// below see every other load of it.
func lockImportSource(ctx context.Context, tx pgx.Tx, importSource string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('target|' || current_setting('passage.tenant_id', true) || '|' || $1, 0))`, importSource); err != nil {
		return fmt.Errorf("lock import source: %w", err)
	}
	return nil
}

// TargetStage is the load a run asks for: its target legal entity and import source, and the accepted
// records built into receivables rows (or held back, with the reason).
type TargetStage struct {
	LegalEntityID string
	ImportSource  string
	Rows          []domain.StagedRow
	// Payables is true when the run's AP lines are staged for the payables load (domain.TargetConfig.Payables).
	Payables bool
}

const loadColumns = `run_id::text, target, legal_entity_id::text, import_source, status, staged, created, updated, unchanged, skipped,
	rejected, not_available, pending, attempts, last_error, started_by, started_at, finished_at, version, created_at`

func scanLoad(row pgx.Row) (domain.TargetLoad, error) {
	var l domain.TargetLoad
	err := row.Scan(&l.RunID, &l.Target, &l.LegalEntityID, &l.ImportSource, &l.Status, &l.Staged, &l.Created, &l.Updated, &l.Unchanged, &l.Skipped,
		&l.Rejected, &l.NotAvailable, &l.Pending, &l.Attempts, &l.LastError, &l.StartedBy, &l.StartedAt, &l.FinishedAt, &l.Version, &l.CreatedAt)
	return l, err
}

// stageTarget stores the load header and its rows with the run, in the run's transaction. An import source that
// another legacy (system, company) already loads under is refused (ErrImportSourceTaken).
func stageTarget(ctx context.Context, tx pgx.Tx, runID string, st *TargetStage) error {
	if err := lockImportSource(ctx, tx, st.ImportSource); err != nil {
		return err
	}
	var other string
	err := tx.QueryRow(ctx, `select o.source_system || ' ' || o.source_company from target_load l
		join pack_run o on o.tenant_id = l.tenant_id and o.run_id = l.run_id
		join pack_run r on r.tenant_id = l.tenant_id and r.run_id = $1
		where l.tenant_id = current_setting('passage.tenant_id', true)::uuid and l.import_source = $2
		  and (o.source_system, o.source_company) <> (r.source_system, r.source_company)
		limit 1`, runID, st.ImportSource).Scan(&other)
	switch {
	case err == nil:
		return fmt.Errorf("%w: %s is loaded from %s; name another importSource for this company", ErrImportSourceTaken, st.ImportSource, other)
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check import source: %w", err)
	}
	pending, rejected, na := 0, 0, 0
	for _, r := range st.Rows {
		if st.Payables && r.Ledger == domain.LedgerAP {
			continue // counted by payables_load
		}
		switch r.Status {
		case domain.RowPending:
			pending++
		case domain.RowRejected:
			rejected++
		case domain.RowNotAvailable:
			na++
		}
	}
	if _, err := tx.Exec(ctx, `insert into target_load (tenant_id, run_id, target, legal_entity_id, import_source, status, staged, rejected, not_available, pending)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		runID, domain.TargetReceivables, st.LegalEntityID, st.ImportSource, domain.LoadStaged, len(st.Rows)-apStaged(st), rejected, na, pending); err != nil {
		return fmt.Errorf("insert target_load: %w", err)
	}
	if na > 0 {
		if _, err := tx.Exec(ctx, `update pack_run set payables_staged = $2 where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, countLedger(st.Rows, domain.LedgerAP)); err != nil {
			return fmt.Errorf("update pack_run payables_staged: %w", err)
		}
	}
	if st.Payables {
		if err := stagePayables(ctx, tx, runID, st); err != nil {
			return err
		}
	}
	if err := setReceivablesLoad(ctx, tx, runID, domain.LoadStaged); err != nil {
		return err
	}
	n := len(st.Rows)
	if n == 0 {
		return nil
	}
	seqs, legacy, ledgers, hashes := make([]int32, n), make([]string, n), make([]string, n), make([]string, n)
	payloads, statuses, codes, details := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, r := range st.Rows {
		seqs[i], legacy[i], ledgers[i], hashes[i], statuses[i], codes[i], details[i] = int32(r.Seq), r.LegacyID, r.Ledger, r.Hash, r.Status, r.Code, r.Detail
		payload := []byte("{}")
		var v any
		switch {
		case r.Row != nil:
			v = r.Row
		case r.AP != nil:
			v = r.AP
		}
		if v != nil {
			var err error
			if payload, err = json.Marshal(v); err != nil {
				return err
			}
		}
		payloads[i] = string(payload)
	}
	if _, err := tx.Exec(ctx, `insert into target_row (tenant_id, run_id, legal_entity_id, seq, legacy_id, ledger, row_hash, payload, status, code, detail)
		select current_setting('passage.tenant_id', true)::uuid, $1, $10, s, l, g, h, p::jsonb, st, c, d
		from unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[]) as t(s, l, g, h, p, st, c, d)`,
		runID, seqs, legacy, ledgers, hashes, payloads, statuses, codes, details, st.LegalEntityID); err != nil {
		return fmt.Errorf("insert target_row: %w", err)
	}
	return nil
}

// GetTargetLoad returns the load of a run, or ErrNoTarget when the run was started without one.
func (s *Postgres) GetTargetLoad(ctx context.Context, runID string) (domain.TargetLoad, error) {
	var l domain.TargetLoad
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		l, err = scanLoad(tx.QueryRow(ctx, `select `+loadColumns+` from target_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := tx.QueryRow(ctx, `select exists (select 1 from pack_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1)`, runID).Scan(&exists); qerr != nil {
				return qerr
			}
			if !exists {
				return ErrNotFound
			}
			return ErrNoTarget
		}
		return err
	})
	return l, err
}

// TargetRowFilter narrows ListTargetRows. After is the seq of the last row of the previous page.
type TargetRowFilter struct {
	Status string
	After  int
	Limit  int
}

// ListTargetRows returns the staged rows of a run by seq.
func (s *Postgres) ListTargetRows(ctx context.Context, runID string, f TargetRowFilter) ([]domain.TargetRowView, error) {
	out := []domain.TargetRowView{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select seq, legacy_id, ledger, status, outcome, coalesce(passage_id::text, ''), code, detail,
				coalesce(payload->>'openAmount', ''), coalesce(payload->>'currency', ''), coalesce(payload->>'side', ''), coalesce(payload->>'customerId', '')
			from target_row where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1
			  and ($2 = '' or status = $2) and seq > $3 order by seq limit $4`, runID, f.Status, f.After, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v domain.TargetRowView
			if err := rows.Scan(&v.Seq, &v.LegacyID, &v.Ledger, &v.Status, &v.Outcome, &v.PassageID, &v.Code, &v.Detail, &v.Amount, &v.Currency, &v.Side, &v.CustomerID); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// PendingRow is a staged row still to send.
type PendingRow struct {
	Seq      int
	LegacyID string
	Hash     string
	Row      domain.ReceivablesRow
}

// ClaimTargetLoad takes the run's load lease and returns the rows still to send. Rows equal to what the
// target already accepted under the same import source are marked SKIPPED first: a rerun of an unchanged
// snapshot sends nothing. A load whose lease has not run out, or a live load of another run of the same import
// source and legal entity, is ErrLoadBusy: the reconciliation reads receivables' totals for the whole import
// source, so a second load writing to it at the same time would make them differ from what either run recorded.
// A run older than one already loaded under the same import source and legal entity is ErrLoadSuperseded.
func (s *Postgres) ClaimTargetLoad(ctx context.Context, runID, startedBy string, lease time.Duration) (domain.TargetLoad, []PendingRow, error) {
	var load domain.TargetLoad
	var pending []PendingRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		var leaseUntil *time.Time
		load, err = scanLoad(tx.QueryRow(ctx, `select `+loadColumns+` from target_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoTarget
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select lease_until from target_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&leaseUntil); err != nil {
			return err
		}
		if load.Status == domain.LoadLoading && leaseUntil != nil && leaseUntil.After(time.Now()) {
			return ErrLoadBusy
		}
		// A run a cutover rollback moved to ROLLED_BACK (BR-49.1) is not loaded into the target any more.
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
			from target_load l join target_load o on o.tenant_id = l.tenant_id and o.import_source = l.import_source
			  and o.legal_entity_id = l.legal_entity_id and o.run_id <> l.run_id
			where l.tenant_id = current_setting('passage.tenant_id', true)::uuid and l.run_id = $1`, runID).Scan(&busy, &superseded); err != nil {
			return fmt.Errorf("check other loads: %w", err)
		}
		if busy {
			return ErrLoadBusy
		}
		if superseded {
			return ErrLoadSuperseded
		}
		if _, err := tx.Exec(ctx, `update target_row r set status = 'SKIPPED', outcome = 'UNCHANGED', passage_id = l.passage_id, updated_at = now()
			from target_loaded l
			where r.tenant_id = current_setting('passage.tenant_id', true)::uuid and r.run_id = $1 and r.status = 'PENDING' and r.ledger = 'AR'
			  and l.tenant_id = r.tenant_id and l.import_source = $2 and l.legacy_id = r.legacy_id and l.row_hash = r.row_hash`, runID, load.ImportSource); err != nil {
			return fmt.Errorf("skip rows the target already holds: %w", err)
		}
		rows, err := tx.Query(ctx, `select seq, legacy_id, row_hash, payload from target_row
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and status = 'PENDING' and ledger = 'AR' order by seq`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p PendingRow
			var raw []byte
			if err := rows.Scan(&p.Seq, &p.LegacyID, &p.Hash, &raw); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(raw, &p.Row); err != nil {
				rows.Close()
				return fmt.Errorf("staged row %q: %w", p.LegacyID, err)
			}
			pending = append(pending, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update target_load set status = $2, lease_until = now() + $3::int * interval '1 second', started_by = $4, started_at = now(), finished_at = null,
				attempts = attempts + 1, last_error = '', version = version + 1
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
			runID, domain.LoadLoading, int32(lease.Seconds()), startedBy); err != nil {
			return err
		}
		if err := setReceivablesLoad(ctx, tx, runID, domain.LoadLoading); err != nil {
			return err
		}
		load, err = scanLoad(tx.QueryRow(ctx, `select `+loadColumns+` from target_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, pending, err
}

// RowOutcome is what the target answered for one sent row.
type RowOutcome struct {
	Pending    PendingRow
	Outcome    string // CREATED, UPDATED, UNCHANGED or REJECTED
	OpenItemID string
	Code       string
	Detail     string
}

// RecordTargetChunk stores the outcomes of one batch: the staged rows, what the target now holds for each accepted
// key, and the open-item id in legacy_xref (once). It also extends the lease, so a long load keeps it.
func (s *Postgres) RecordTargetChunk(ctx context.Context, runID string, lease time.Duration, outs []RowOutcome) error {
	if len(outs) == 0 {
		return nil
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		var importSource, le string
		if err := tx.QueryRow(ctx, `select import_source, legal_entity_id::text from target_load
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID).Scan(&importSource, &le); err != nil {
			return fmt.Errorf("read target_load: %w", err)
		}
		n := len(outs)
		seqs, statuses, outcomes, passage, codes, details := make([]int32, n), make([]string, n), make([]string, n), make([]*string, n), make([]string, n), make([]string, n)
		var aLegacy, aCur, aSide, aHash []string
		var aOpen, aPassage []string
		for i, o := range outs {
			seqs[i], outcomes[i], codes[i], details[i] = int32(o.Pending.Seq), o.Outcome, o.Code, o.Detail
			switch o.Outcome {
			case "CREATED", "UPDATED":
				statuses[i] = domain.RowLoaded
			case "UNCHANGED":
				statuses[i] = domain.RowUnchanged
			default:
				statuses[i] = domain.RowRejected
			}
			if statuses[i] != domain.RowRejected {
				id := o.OpenItemID
				passage[i] = &id
				row := o.Pending.Row
				aLegacy, aCur, aSide, aHash = append(aLegacy, o.Pending.LegacyID), append(aCur, row.Currency), append(aSide, row.Side), append(aHash, o.Pending.Hash)
				aOpen, aPassage = append(aOpen, row.OpenAmount.String()), append(aPassage, id)
			}
		}
		if _, err := tx.Exec(ctx, `update target_row r set status = t.st, outcome = t.o, passage_id = t.p::uuid, code = t.c, detail = t.d, updated_at = now()
			from unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) as t(s, st, o, p, c, d)
			where r.tenant_id = current_setting('passage.tenant_id', true)::uuid and r.run_id = $1 and r.seq = t.s`,
			runID, seqs, statuses, outcomes, passage, codes, details); err != nil {
			return fmt.Errorf("update target_row: %w", err)
		}
		if len(aLegacy) > 0 {
			if _, err := tx.Exec(ctx, `insert into target_loaded (tenant_id, import_source, legacy_id, legal_entity_id, currency, side, open_amount, row_hash, passage_id, run_id)
				select current_setting('passage.tenant_id', true)::uuid, $1, l, $2, c, sd, a::numeric, h, p::uuid, $3
				from unnest($4::text[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[]) as t(l, c, sd, a, h, p)
				on conflict (tenant_id, import_source, legacy_id) do update
				set currency = excluded.currency, side = excluded.side, open_amount = excluded.open_amount, row_hash = excluded.row_hash,
				    passage_id = excluded.passage_id, run_id = excluded.run_id, loaded_at = now()`,
				importSource, le, runID, aLegacy, aCur, aSide, aOpen, aHash, aPassage); err != nil {
				return fmt.Errorf("upsert target_loaded: %w", err)
			}
			if _, err := tx.Exec(ctx, `update legacy_xref x set passage_id = t.p::uuid, target_confirmed = true
				from pack_run p, unnest($2::text[], $3::text[]) as t(l, p)
				where p.tenant_id = x.tenant_id and p.run_id = $1 and x.tenant_id = current_setting('passage.tenant_id', true)::uuid
				  and x.source_system = p.source_system and x.source_company = p.source_company and x.source_table = $4
				  and x.source_id = t.l and not x.target_confirmed`, runID, aLegacy, aPassage, domain.TableOpenItemsReceivable); err != nil {
				return fmt.Errorf("confirm legacy_xref: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `update target_load set lease_until = now() + $2::int * interval '1 second' where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
			runID, int32(lease.Seconds()))
		return err
	})
}

// TargetFinish is how one pass of the load ended.
type TargetFinish struct {
	// Fatal is set when the load stopped because the target refused the factory (status FAILED).
	Fatal string
	// Unavailable is set when the target stopped answering and rows are still pending (status PARTIAL).
	Unavailable string
	// ActualErr is set when the target's totals could not be read (status UNRECONCILED).
	ActualErr string
	// Actual is the target's totals for the import source, read after the last row was sent.
	Actual []domain.ImportTotals
}

// FinishTargetLoad ends a pass: it recounts the rows, compares the target's totals with what the factory
// sent, stores the comparison as control totals and the status, and writes dev.passage.migration.pack.loaded.v1.
func (s *Postgres) FinishTargetLoad(ctx context.Context, runID string, fin TargetFinish) (domain.TargetLoad, []domain.ControlTotal, error) {
	var load domain.TargetLoad
	var totals []domain.ControlTotal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		load, err = scanLoad(tx.QueryRow(ctx, `select `+loadColumns+` from target_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if err != nil {
			return err
		}
		counts := map[string]int{}
		rows, err := tx.Query(ctx, `select status, outcome, count(*) from target_row where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1
			and (ledger = 'AR' or status = 'NOT_AVAILABLE') group by status, outcome`, runID)
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
			if st == domain.RowLoaded {
				counts[outcome] += n
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		load.Created, load.Updated, load.Unchanged = counts["CREATED"], counts["UPDATED"], counts[domain.RowUnchanged]
		load.Skipped, load.Rejected, load.NotAvailable, load.Pending = counts[domain.RowSkipped], counts[domain.RowRejected], counts[domain.RowNotAvailable], counts[domain.RowPending]
		load.LastError = ""
		switch {
		case fin.Fatal != "":
			load.Status, load.LastError = domain.LoadFailed, fin.Fatal
		case load.Pending > 0:
			load.Status, load.LastError = domain.LoadPartial, fin.Unavailable
		case fin.ActualErr != "":
			load.Status, load.LastError = domain.LoadUnreconciled, fin.ActualErr
		default:
			expected, err := expectedTotals(ctx, tx, load.ImportSource, load.LegalEntityID)
			if err != nil {
				return err
			}
			totals, err = domain.ReconcileTarget(load.ImportSource, load.LegalEntityID, expected, fin.Actual)
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
		if _, err := tx.Exec(ctx, `update target_load set status = $2, created = $3, updated = $4, unchanged = $5, skipped = $6, rejected = $7, not_available = $8,
				pending = $9, last_error = $10, lease_until = null, finished_at = now(), version = version + 1
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
			runID, load.Status, load.Created, load.Updated, load.Unchanged, load.Skipped, load.Rejected, load.NotAvailable, load.Pending, load.LastError); err != nil {
			return err
		}
		if err := setReceivablesLoad(ctx, tx, runID, load.Status); err != nil {
			return err
		}
		match, mismatch := domain.CountTotals(totals)
		if err := enqueue(ctx, tx, mfevents.PackLoaded, "pack_run", runID, mfevents.PackLoadedData{
			RunID: runID, Pack: string(domain.PackOpenItems), Target: load.Target, ImportSource: load.ImportSource, LegalEntityID: load.LegalEntityID, Status: load.Status,
			Created: load.Created, Updated: load.Updated, Unchanged: load.Unchanged, Skipped: load.Skipped, Rejected: load.Rejected, NotAvailable: load.NotAvailable,
			Pending: load.Pending, TotalsMatch: match, TotalsMismatch: mismatch,
		}); err != nil {
			return fmt.Errorf("enqueue pack.loaded: %w", err)
		}
		load, err = scanLoad(tx.QueryRow(ctx, `select `+loadColumns+` from target_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, totals, err
}

// expectedTotals sums what the target accepted for the import source, per currency, in receivables' conventions
// (credit negative).
func expectedTotals(ctx context.Context, tx pgx.Tx, importSource, le string) ([]domain.ImportTotals, error) {
	rows, err := tx.Query(ctx, `select currency, count(*), coalesce(sum(open_amount) filter (where side = 'DEBIT'), 0)::text,
			coalesce(-sum(open_amount) filter (where side = 'CREDIT'), 0)::text
		from target_loaded where tenant_id = current_setting('passage.tenant_id', true)::uuid and import_source = $1 and legal_entity_id = $2
		group by currency order by currency`, importSource, le)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ImportTotals
	for rows.Next() {
		var t domain.ImportTotals
		var debit, credit string
		if err := rows.Scan(&t.Currency, &t.ItemCount, &debit, &credit); err != nil {
			return nil, err
		}
		t.DebitOpen, t.CreditOpen = decimal.RequireFromString(debit), decimal.RequireFromString(credit)
		t.NetOpen = t.DebitOpen.Add(t.CreditOpen)
		out = append(out, t)
	}
	return out, rows.Err()
}

// upsertTargetTotals stores the target reconciliation as control totals of the run; a total of the same name and
// key from an earlier pass is replaced, so the run shows the latest comparison.
func upsertTargetTotals(ctx context.Context, tx pgx.Tx, runID string, totals []domain.ControlTotal) error {
	var maxSeq int
	if err := tx.QueryRow(ctx, `select coalesce(max(seq), 0) from control_total where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&maxSeq); err != nil {
		return err
	}
	for _, t := range totals {
		key, err := json.Marshal(t.Key)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update control_total set source_value = $4, target_value = $5, delta = $6, status = $7, computed_at = now()
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and name = $2 and key_json = $3::jsonb`,
			runID, t.Name, key, t.Source.String(), t.Target.String(), t.Delta.String(), string(t.Status))
		if err != nil {
			return fmt.Errorf("update control_total: %w", err)
		}
		if tag.RowsAffected() > 0 {
			continue
		}
		maxSeq++
		if _, err := tx.Exec(ctx, `insert into control_total (tenant_id, total_id, run_id, seq, name, key_json, source_value, target_value, delta, tolerance, tolerance_reason, status)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, 0, '', $9)`,
			ids.New(), runID, maxSeq, t.Name, key, t.Source.String(), t.Target.String(), t.Delta.String(), string(t.Status)); err != nil {
			return fmt.Errorf("insert control_total: %w", err)
		}
	}
	return nil
}

// countLedger counts the staged rows of one subledger that the target has not taken.
func countLedger(rows []domain.StagedRow, ledger string) int {
	n := 0
	for _, r := range rows {
		if r.Ledger == ledger && r.Status == domain.RowNotAvailable {
			n++
		}
	}
	return n
}

// setReceivablesLoad copies the load's status onto the run (pack_run.receivables_load). The run is scoped
// by tenant only while target_load is held to the caller's legal-entity scopes, and the go/no-go reads the
// status as a cutover manager of any scope: a narrow scope must not read "no load ran" and pass.
func setReceivablesLoad(ctx context.Context, tx pgx.Tx, runID, status string) error {
	if _, err := tx.Exec(ctx, `update pack_run set receivables_load = $2 where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`, runID, status); err != nil {
		return fmt.Errorf("update pack_run receivables_load: %w", err)
	}
	return nil
}

// apStaged counts the AP rows a payables load takes over from the receivables load's counts.
func apStaged(st *TargetStage) int {
	if !st.Payables {
		return 0
	}
	return countAP(st.Rows)
}

func countAP(rows []domain.StagedRow) int {
	n := 0
	for _, r := range rows {
		if r.Ledger == domain.LedgerAP {
			n++
		}
	}
	return n
}
