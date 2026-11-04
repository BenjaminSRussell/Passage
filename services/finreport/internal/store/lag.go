package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// LagRow reports how far the read model has advanced for one ledger (FR-15.1).
type LagRow struct {
	LedgerID        string    `json:"ledgerId"`
	PostingSequence int64     `json:"postingSequence"`
	UpdatedAt       time.Time `json:"updatedAt"`
	EntriesApplied  int64     `json:"entriesApplied"`
	// EntriesPending counts entries that arrived ahead of the mark and wait for
	// an earlier one; a count that stays above zero means an entry is missing.
	EntriesPending int64 `json:"entriesPending"`
	// LagSeconds is the age of the oldest entry waiting for an earlier one: the
	// time the read model has been unable to reflect something the ledger has
	// already posted. Zero when nothing waits. It is a lower bound: entries the
	// consumer has not received (consumer stopped or behind on its partitions,
	// event lost with nothing after it) do not count, because the service has
	// no read of the ledger's own high-water mark (NOT-IMPLEMENTED.md).
	LagSeconds int64 `json:"lagSeconds"`
	// HWMLedger is the highest posting sequence seen for the ledger, applied or
	// waiting; PostingSequence is the one applied.
	HWMLedger     int64  `json:"hwmLedger"`
	LegalEntityID string `json:"-"`
}

// lagSQL lists a row per ledger the read model knows (applied or waiting). The
// first argument narrows it to one ledger when not empty; it is compared as a
// uuid, so an upper-case or braced ledger id (both pass the API's UUID check)
// cannot slip past the lag gate.
const lagSQL = `select l.ledger_id::text, coalesce(h.posting_sequence, 0),
		coalesce(h.updated_at, (select min(w.received_at) from reporting_entry_pending w where w.ledger_id = l.ledger_id)),
		(select count(*) from reporting_entry_applied a where a.ledger_id = l.ledger_id),
		(select count(*) from reporting_entry_pending w where w.ledger_id = l.ledger_id),
		coalesce(floor(extract(epoch from now() - (select min(w.received_at) from reporting_entry_pending w where w.ledger_id = l.ledger_id))), 0)::bigint,
		greatest(coalesce(h.posting_sequence, 0), coalesce((select max(w.posting_sequence) from reporting_entry_pending w where w.ledger_id = l.ledger_id), 0)),
		coalesce(h.legal_entity_id::text, (select w.legal_entity_id::text from reporting_entry_pending w where w.ledger_id = l.ledger_id limit 1), '')
	from (select ledger_id from reporting_hwm union select ledger_id from reporting_entry_pending) l
	left join reporting_hwm h on h.ledger_id = l.ledger_id
	where (nullif($1, '') is null or l.ledger_id = nullif($1, '')::uuid)
	order by l.ledger_id`

func lagTx(ctx context.Context, tx pgx.Tx, ledgerID string) ([]LagRow, error) {
	rows, err := tx.Query(ctx, lagSQL, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LagRow{}
	for rows.Next() {
		var l LagRow
		if err := rows.Scan(&l.LedgerID, &l.PostingSequence, &l.UpdatedAt, &l.EntriesApplied, &l.EntriesPending, &l.LagSeconds, &l.HWMLedger, &l.LegalEntityID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Lag returns the reporting high-water mark and lag per ledger (FR-15.1).
func (s *Postgres) Lag(ctx context.Context) ([]LagRow, error) {
	var out []LagRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = lagTx(ctx, tx, "")
		return err
	})
	return out, err
}

// staleCheckTx applies the lag gate to a run on ledgerID: a read model more than
// maxLag seconds behind refuses the run with 409 REPORT-0004 unless allowStale,
// which lets it complete with the warning recorded (FR-15.1, PRD §2.15.18).
// It runs in the run's transaction just before the balances are read. Under
// read committed the read is a later snapshot, which can only hold more of the
// ledger: an entry still waiting then was waiting here (and counted), or began
// waiting in between and is younger than that gap, so the gate errs by at most
// the milliseconds between the two statements.
func staleCheckTx(ctx context.Context, tx pgx.Tx, ledgerID string, maxLag int, allowStale bool) ([]domain.Check, error) {
	rows, err := lagTx(ctx, tx, ledgerID)
	if err != nil || len(rows) == 0 || rows[0].LagSeconds <= int64(maxLag) {
		return nil, err
	}
	detail := fmt.Sprintf("Read model is %d s behind ledger %s (limit %d s); %d journal entries wait for an earlier one.",
		rows[0].LagSeconds, ledgerID, maxLag, rows[0].EntriesPending)
	if !allowStale {
		return nil, problem(409, domain.CodeStale, detail+" Wait for it to catch up, or run with allowStale to accept stale results.")
	}
	return []domain.Check{{Code: domain.CodeStale, Severity: domain.SeverityWarning, Detail: detail + " The results are flagged as stale."}}, nil
}

// LagAlert is one lag.exceeded event raised by CheckLag.
type LagAlert struct {
	LedgerID     string `json:"ledgerId"`
	LagSeconds   int64  `json:"lagSeconds"`
	HWMLedger    int64  `json:"hwmLedger"`
	HWMReporting int64  `json:"hwmReporting"`
}

// LagCheck is the outcome of one CheckLag call.
type LagCheck struct {
	// Checked is the number of ledgers examined; Exceeded how many are above the
	// threshold now. Raised lists the alerts written by this call.
	Checked  int        `json:"checked"`
	Exceeded int        `json:"exceeded"`
	Raised   []LagAlert `json:"raised"`
}

// lagCheckLock serialises CheckLag per tenant (transaction-scoped advisory
// lock), so two overlapping checks cannot both open or alert the same episode
// nor deadlock over each other's episode rows.
const lagCheckLock = `select pg_advisory_xact_lock(hashtextextended('finreport.lag-check:' || current_setting('passage.tenant_id', true), 0))`

// CheckLag raises dev.passage.reporting.lag.exceeded.v1 once per episode for
// each ledger whose lag stayed above maxLag for sustain seconds (PRD §2.15.5:
// "lag > threshold for 60 s"). The first sighting opens an episode row; the
// event goes to the outbox in the same transaction as the row's alerted_at; a
// ledger back under the threshold closes its episode, so a later outage alerts
// again. The lag has been above maxLag for at least lag - maxLag seconds (the
// same entry has been waiting all along), so a check that first sees a ledger
// already that far behind alerts at once rather than a whole interval later.
// `finreport lag-check` (a CronJob, every minute) calls it per tenant.
func (s *Postgres) CheckLag(ctx context.Context, maxLag, sustain int) (LagCheck, error) {
	res := LagCheck{Raised: []LagAlert{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lagCheckLock); err != nil {
			return err
		}
		rows, err := lagTx(ctx, tx, "")
		if err != nil {
			return err
		}
		res.Checked = len(rows)
		over := []string{}
		for _, l := range rows {
			if l.LagSeconds <= int64(maxLag) {
				continue
			}
			over = append(over, l.LedgerID)
			res.Exceeded++
			// The lock already serialises checks; ON CONFLICT keeps the insert
			// safe without it, and the select below locks whichever row exists.
			if _, err := tx.Exec(ctx, `insert into reporting_lag_alert(tenant_id, ledger_id, first_exceeded_at, legal_entity_id)
				values($1,$2::uuid,now(),nullif($3,'')::uuid) on conflict (tenant_id, ledger_id) do nothing`,
				tenantID(ctx), l.LedgerID, l.LegalEntityID); err != nil {
				return err
			}
			var sustained, alerted bool
			if err := tx.QueryRow(ctx, `select now() - first_exceeded_at >= make_interval(secs => $2), alerted_at is not null
				from reporting_lag_alert where ledger_id=$1::uuid for update`, l.LedgerID, sustain).Scan(&sustained, &alerted); err != nil {
				return err
			}
			if alerted || !(sustained || l.LagSeconds-int64(maxLag) >= int64(sustain)) {
				continue
			}
			if _, err := tx.Exec(ctx, `update reporting_lag_alert set alerted_at=now() where ledger_id=$1::uuid`, l.LedgerID); err != nil {
				return err
			}
			a := LagAlert{LedgerID: l.LedgerID, LagSeconds: l.LagSeconds, HWMLedger: l.HWMLedger, HWMReporting: l.PostingSequence}
			if err := enqueue(ctx, tx, "lag", l.LedgerID, events.LagExceeded, tenantID(ctx), events.LagExceededData{
				LedgerID: a.LedgerID, LegalEntityID: l.LegalEntityID, LagSeconds: a.LagSeconds, HWMLedger: a.HWMLedger, HWMReporting: a.HWMReporting,
			}); err != nil {
				return err
			}
			res.Raised = append(res.Raised, a)
		}
		_, err = tx.Exec(ctx, `delete from reporting_lag_alert where ledger_id <> all($1::uuid[])`, over)
		return err
	})
	return res, err
}
