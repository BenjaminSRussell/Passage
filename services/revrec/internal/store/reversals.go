package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// MaxReversalRefusals is how many ledger refusals park a reversal posting until a caller retries it.
const MaxReversalRefusals = 5

// reversalBatch bounds the reversal postings one call sends, per source table.
const reversalBatch = 200

// reversalTables hold the recognized revenue to post back out (Dr 4000 / Cr 2400): credit notes and contract modifications. Both
// have the same posting columns; ids are UUIDs, so an update by id hits one row in one of them.
func reversalTables() [2]string { return [2]string{"credit_note_reversal", "modification_reversal"} }

// ReversalPosting is the ledger posting of the recognized revenue a credit note reversed (Dr 4000 / Cr 2400), one per
// credit_note_reversal row, dated the credit note's posting date. Every amount comes from the stored row.
type ReversalPosting struct {
	ID               string
	LegalEntityID    string
	LedgerID         string
	ContractID       string
	CreditNoteID     string
	CreditNoteNumber string
	// ModificationID is set (and CreditNoteID empty) for the revenue a contract modification took back (modification_reversal).
	ModificationID string
	Currency       string
	PostingDate    time.Time
	Amount         decimal.Decimal
	created        time.Time
}

// DueReversals returns the entity's reversals with recognized revenue to post that have no ledger entry, are not parked and
// whose next attempt is due, oldest first. A reversal is pinned to ledgerID at its first attempt (ledger_id); one pinned to
// another ledger is returned in otherLedger and never re-targeted. retryParked offers the parked ones again first.
func (s *Postgres) DueReversals(ctx context.Context, legalEntityID, ledgerID string, now time.Time, retryParked bool) (due, otherLedger []ReversalPosting, err error) {
	var all []ReversalPosting
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, table := range reversalTables() {
			if retryParked {
				if _, err := tx.Exec(ctx, `update `+table+` set gl_parked_code = null, gl_refusal_count = 0, gl_next_attempt_at = null
					where legal_entity_id = $1::uuid and gl_parked_code is not null and entry_id is null`, legalEntityID); err != nil {
					return err
				}
			}
			cols := `r.credit_note_id::text, r.credit_note_number, ''`
			if table == "modification_reversal" {
				cols = `'', '', r.modification_id::text`
			}
			rows, err := tx.Query(ctx, `with due as (
				  select id from `+table+`
				  where legal_entity_id = $1::uuid and revenue_reversed > 0 and entry_id is null and gl_parked_code is null
				    and (gl_next_attempt_at is null or gl_next_attempt_at <= $3)
				  order by posting_date, created_at, id limit $4 for update skip locked)
				update `+table+` r set ledger_id = coalesce(r.ledger_id, $2::uuid)
				from due where r.id = due.id
				returning r.id::text, r.ledger_id::text, r.contract_id::text, `+cols+`, r.currency::text,
				  r.posting_date, r.revenue_reversed::text, r.created_at`, legalEntityID, ledgerID, now, reversalBatch)
			if err != nil {
				return err
			}
			for rows.Next() {
				rp, amount := ReversalPosting{LegalEntityID: legalEntityID}, ""
				if err := rows.Scan(&rp.ID, &rp.LedgerID, &rp.ContractID, &rp.CreditNoteID, &rp.CreditNoteNumber, &rp.ModificationID, &rp.Currency, &rp.PostingDate, &amount, &rp.created); err != nil {
					rows.Close()
					return err
				}
				if rp.Amount, err = decimal.NewFromString(amount); err != nil {
					rows.Close()
					return err
				}
				all = append(all, rp)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	// UPDATE ... RETURNING does not keep the subquery's order.
	slices.SortStableFunc(all, func(a, b ReversalPosting) int {
		return cmp.Or(a.PostingDate.Compare(b.PostingDate), a.created.Compare(b.created), cmp.Compare(a.ID, b.ID))
	})
	for _, rp := range all {
		if rp.LedgerID == ledgerID {
			due = append(due, rp)
		} else {
			otherLedger = append(otherLedger, rp)
		}
	}
	return due, otherLedger, nil
}

// CompleteReversal keeps the ledger entry of a reversal posting; a second call changes nothing.
func (s *Postgres) CompleteReversal(ctx context.Context, id, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, table := range reversalTables() {
			if _, err := tx.Exec(ctx, `update `+table+` set entry_id = $2, gl_last_error = null, gl_next_attempt_at = null
				where id = $1::uuid and entry_id is null`, id, entryID); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecordReversalFailure backs a reversal posting off (5 minutes, doubling, at most a day) and records code. A refusal by the
// ledger (refused) counts toward MaxReversalRefusals and parks the row with code when it is reached; an outage without a
// verdict never parks.
func (s *Postgres) RecordReversalFailure(ctx context.Context, id, code string, now time.Time, refused bool) (parked bool, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, table := range reversalTables() {
			err := tx.QueryRow(ctx, `update `+table+` set gl_attempt_count = gl_attempt_count + 1, gl_last_error = $2,
				gl_refusal_count = gl_refusal_count + case when $4::boolean then 1 else 0 end,
				gl_next_attempt_at = $3::timestamptz + (5 * least(2 ^ gl_attempt_count, 288)) * interval '1 minute',
				gl_parked_code = case when $4::boolean and gl_refusal_count + 1 >= $5 then $2 end
				where id = $1::uuid and entry_id is null returning gl_parked_code is not null`, id, code, now, refused, MaxReversalRefusals).Scan(&parked)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return err
		}
		return nil
	})
	return parked, err
}
