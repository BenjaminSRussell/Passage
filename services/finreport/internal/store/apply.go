package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// ApplyEntry folds one posted ledger entry into the read model (FR-15.1) and
// returns how many entries it applied. The ledger's posting sequence is
// gap-free per ledger and the read model reflects exactly the entries up to its
// high-water mark (BR-15.6), so:
//   - an entry at or below the mark is a replay: nothing changes (0);
//   - the entry right after the mark is applied (its lines to
//     reporting_journal_line, its amounts into reporting_balance), then every
//     waiting entry that now follows on, and the mark advances past them, all
//     in one transaction;
//   - a later entry waits in reporting_entry_pending (0) until the entries
//     before it arrive. The ledger publishes one ledger's entries through
//     several Kafka partitions, so arriving out of order is normal and must
//     neither fail nor be skipped.
//
// The entry must name the ledger's legal entity, and the caller must be in its
// scope: the first entry of a ledger records it, later ones must match.
func (s *Postgres) ApplyEntry(ctx context.Context, e domain.PostedEntry) (int, error) {
	if _, _, err := e.Build(); err != nil {
		return 0, err
	}
	for field, id := range map[string]*string{"ledger id": &e.LedgerID, "entry id": &e.EntryID, "legal entity id": &e.LegalEntityID} {
		canonical, ok := ids.Canonical(*id)
		if !ok {
			return 0, problem(422, domain.CodeLineInvalid, field+" is not a uuid")
		}
		*id = canonical
	}
	if e.ReversalOfEntryID != "" {
		canonical, ok := ids.Canonical(e.ReversalOfEntryID)
		if !ok {
			return 0, problem(422, domain.CodeLineInvalid, "reversed entry id is not a uuid")
		}
		e.ReversalOfEntryID = canonical
	}
	if p, _ := tenant.FromContext(ctx); !tenant.InLegalEntityScope(p, e.LegalEntityID) {
		return 0, problem(422, CodeLedgerEntity, "the caller may not write the reporting data of legal entity "+e.LegalEntityID)
	}
	applied := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		applied = 0
		if _, err := tx.Exec(ctx, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence, legal_entity_id)
			values($1, $2::uuid, 0, $3::uuid) on conflict do nothing`, tenantID(ctx), e.LedgerID, e.LegalEntityID); err != nil {
			return err
		}
		var hwm int64
		var owner *string
		err := tx.QueryRow(ctx, `select posting_sequence, legal_entity_id::text from reporting_hwm where ledger_id=$1::uuid for update`, e.LedgerID).Scan(&hwm, &owner)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The mark exists (the insert found it) but lies outside the caller's scope.
			return problem(422, CodeLedgerEntity, "ledger "+e.LedgerID+" holds reporting data of another legal entity")
		case err != nil:
			return err
		case owner == nil || *owner != e.LegalEntityID:
			return problem(422, CodeLedgerEntity, "legal entity "+e.LegalEntityID+" does not own the reporting data of ledger "+e.LedgerID)
		}
		switch {
		case e.PostingSequence <= hwm:
			return nil // replay
		case e.PostingSequence > hwm+1:
			raw, err := json.Marshal(e)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into reporting_entry_pending(tenant_id, ledger_id, posting_sequence, entry_id, entry, legal_entity_id)
				values($1, $2::uuid, $3, $4::uuid, $5::jsonb, $6::uuid) on conflict do nothing`,
				tenantID(ctx), e.LedgerID, e.PostingSequence, e.EntryID, string(raw), e.LegalEntityID)
			return err
		}
		next := e
		for {
			if err := applyEntryTx(ctx, tx, next); err != nil {
				return err
			}
			applied++
			hwm = next.PostingSequence
			var raw []byte
			err := tx.QueryRow(ctx, `delete from reporting_entry_pending where ledger_id=$1::uuid and posting_sequence=$2 returning entry`,
				e.LedgerID, hwm+1).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err != nil {
				return err
			}
			next = domain.PostedEntry{}
			if err := json.Unmarshal(raw, &next); err != nil {
				return fmt.Errorf("pending entry %d of ledger %s: %w", hwm+1, e.LedgerID, err)
			}
		}
		_, err = tx.Exec(ctx, `update reporting_hwm set posting_sequence=$2, updated_at=now() where ledger_id=$1::uuid`, e.LedgerID, hwm)
		return err
	})
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// applyEntryTx writes one entry's lines, balance deltas and applied row. A
// reversal of an entry applied as a closing entry is a closing entry too.
func applyEntryTx(ctx context.Context, tx pgx.Tx, e domain.PostedEntry) error {
	if e.ReversalOfEntryID != "" {
		err := tx.QueryRow(ctx, `select closing from reporting_entry_applied where ledger_id=$1::uuid and entry_id=$2::uuid`,
			e.LedgerID, e.ReversalOfEntryID).Scan(&e.ReversesClosing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	lines, deltas, err := e.Build()
	if err != nil {
		return err
	}
	for _, l := range lines {
		if err := insertLineTx(ctx, tx, l, e.LegalEntityID); err != nil {
			return err
		}
	}
	for _, d := range deltas {
		dims, err := json.Marshal(d.Dims)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into reporting_balance(tenant_id, ledger_id, period_year, period_no, quarter, adjustment,
				account, combination, dims, currency_type, net, closing_net, legal_entity_id)
			values($1,$2::uuid,$3,$4,$5,false,$6,$7,$8::jsonb,'acct',$9::numeric,$10::numeric,$11::uuid)
			on conflict (tenant_id, ledger_id, period_year, period_no, account, combination, currency_type)
			do update set net = reporting_balance.net + excluded.net, closing_net = reporting_balance.closing_net + excluded.closing_net`,
			tenantID(ctx), e.LedgerID, d.PeriodYear, d.PeriodNo, d.Quarter, d.Account, d.Combination, string(dims),
			d.Net.StringFixed(6), d.ClosingNet.StringFixed(6), e.LegalEntityID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `insert into reporting_entry_applied(tenant_id, ledger_id, posting_sequence, entry_id, line_count, closing, legal_entity_id)
		values($1,$2::uuid,$3,$4::uuid,$5,$6,$7::uuid)`,
		tenantID(ctx), e.LedgerID, e.PostingSequence, e.EntryID, len(lines), e.Closing(), e.LegalEntityID); err != nil {
		if isUniqueViolation(err) {
			return problem(422, domain.CodeLineInvalid, "entry "+e.EntryID+" is already applied under another posting sequence")
		}
		return err
	}
	return nil
}
