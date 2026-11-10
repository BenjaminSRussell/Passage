package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	icevents "github.com/BenjaminSRussell/passage/services/intercompany/internal/events"
)

func principalOf(ctx context.Context) (pr tenant.Principal, by string, err error) {
	pr, ok := tenant.FromContext(ctx)
	if !ok {
		return pr, "", errors.New("store: no principal in context")
	}
	by = pr.UserID
	if by == "" {
		by = pr.Service
	}
	if by == "" {
		return pr, "", errors.New("store: principal has neither user nor service")
	}
	return pr, by, nil
}

// HashOf returns the hash that tells a retry of a request from a different request under the same key.
func HashOf(parts ...any) (string, error) {
	raw, err := json.Marshal(parts)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type legJSON struct {
	Account string `json:"account"`
	Side    string `json:"side"`
	Amount  string `json:"amount"`
}

func encodeLegs(legs []domain.Leg) ([]byte, error) {
	out := make([]legJSON, 0, len(legs))
	for _, l := range legs {
		out = append(out, legJSON{Account: l.Account, Side: string(l.Side), Amount: l.Amount.Value.String()})
	}
	return json.Marshal(out)
}

func decodeLegs(entity, currency string, raw []byte) ([]domain.Leg, error) {
	var in []legJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := make([]domain.Leg, 0, len(in))
	for _, l := range in {
		v, err := decimal.NewFromString(l.Amount)
		if err != nil {
			return nil, fmt.Errorf("store: leg amount %q: %w", l.Amount, err)
		}
		out = append(out, domain.Leg{Entity: entity, Account: l.Account, Side: domain.Side(l.Side), Amount: money.Amount{Value: v, Currency: currency}})
	}
	return out, nil
}

const journalColumns = `id::text, pair_id::text, source_type, source_ref, posting_date, status, coalesce(failure_code, ''), coalesce(failure_detail, ''), created_by, created_at`

func scanJournal(row pgx.Row) (domain.JournalRecord, error) {
	var r domain.JournalRecord
	var status string
	err := row.Scan(&r.ID, &r.PairID, &r.Source, &r.SourceRef, &r.PostingDate, &status, &r.FailureCode, &r.FailureDetail, &r.CreatedBy, &r.CreatedAt)
	r.Status = domain.PostingStatus(status)
	return r, err
}

func loadEntries(ctx context.Context, tx pgx.Tx, tenantID, journalID string) ([]domain.PostedEntry, error) {
	rows, err := tx.Query(ctx, `select entity, legal_entity_id::text, ledger_id, currency, legs, coalesce(ledger_entry_id, ''), coalesce(reversal_entry_id, '')
		from ic_journal_entry where tenant_id = $1 and journal_id = $2::uuid order by seq`, tenantID, journalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PostedEntry
	for rows.Next() {
		var e domain.PostedEntry
		var legs []byte
		if err := rows.Scan(&e.Entity, &e.Target.LegalEntityID, &e.Target.LedgerID, &e.Currency, &legs, &e.LedgerEntryID, &e.ReversalEntryID); err != nil {
			return nil, err
		}
		if e.Legs, err = decodeLegs(e.Entity, e.Currency, legs); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func errPostingNotFound(id string) error {
	return &domain.Error{Code: domain.CodePostingNotFound, Detail: "no intercompany journal with id " + id}
}

// BeginJournal stores a journal as pending with one row per entity entry, or
// returns the journal already stored under the Idempotency-Key (existing=true).
// Reusing the key for another request fails with IC-0841.
func (s *Postgres) BeginJournal(ctx context.Context, rec domain.JournalRecord, key, hash string) (domain.JournalRecord, bool, error) {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.JournalRecord{}, false, err
	}
	var (
		out      domain.JournalRecord
		existing bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into ic_journal (tenant_id, id, pair_id, source_type, source_ref, idempotency_key, request_hash, posting_date, status, created_by)
			values ($1, $2, $3::uuid, $4, $5, $6, $7, $8, 'pending', $9) on conflict (tenant_id, idempotency_key) do nothing`,
			pr.TenantID, id, rec.PairID, rec.Source, rec.SourceRef, key, hash, rec.PostingDate, by)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var stored string
			var existingID string
			if err := tx.QueryRow(ctx, `select request_hash, id::text from ic_journal where tenant_id = $1 and idempotency_key = $2`, pr.TenantID, key).Scan(&stored, &existingID); err != nil {
				return err
			}
			if stored != hash {
				return &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "Idempotency-Key was already used with a different request"}
			}
			existing = true
			out, err = readJournal(ctx, tx, pr.TenantID, existingID)
			return err
		}
		for i, e := range rec.Entries {
			legs, err := encodeLegs(e.Legs)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into ic_journal_entry (tenant_id, journal_id, seq, entity, legal_entity_id, ledger_id, currency, legs)
				values ($1, $2, $3, $4, $5::uuid, $6, $7, $8)`, pr.TenantID, id, i, e.Entity, e.Target.LegalEntityID, e.Target.LedgerID, e.Currency, legs); err != nil {
				return err
			}
		}
		out, err = readJournal(ctx, tx, pr.TenantID, id)
		return err
	})
	if err != nil {
		return domain.JournalRecord{}, false, wrap("begin intercompany journal", err)
	}
	return out, existing, nil
}

func readJournal(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.JournalRecord, error) {
	r, err := scanJournal(tx.QueryRow(ctx, `select `+journalColumns+` from ic_journal where tenant_id = $1 and id = $2::uuid`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errPostingNotFound(id)
	}
	if err != nil {
		return r, err
	}
	r.Entries, err = loadEntries(ctx, tx, tenantID, id)
	return r, err
}

// RecordEntry notes the ledger entry an entity's side made, or the entry that
// reversed it. Recording the same id twice is harmless.
func (s *Postgres) RecordEntry(ctx context.Context, journalID, entity, ledgerEntryID string, reversal bool) error {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return err
	}
	column := "ledger_entry_id"
	if reversal {
		column = "reversal_entry_id"
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update ic_journal_entry set `+column+` = $4 where tenant_id = $1 and journal_id = $2::uuid and entity = $3`, pr.TenantID, journalID, entity, ledgerEntryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errPostingNotFound(journalID)
		}
		return nil
	})
	if err != nil {
		return wrap("record intercompany ledger entry", err)
	}
	return nil
}

// FinishJournal moves a pending journal to posted or compensated and publishes
// the matching event in the same transaction. A journal that is no longer
// pending is returned unchanged.
func (s *Postgres) FinishJournal(ctx context.Context, journalID string, status domain.PostingStatus, failureCode, failureDetail string) (domain.JournalRecord, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.JournalRecord{}, err
	}
	var out domain.JournalRecord
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update ic_journal set status = $3, failure_code = nullif($4, ''), failure_detail = nullif($5, ''), updated_at = now()
			where tenant_id = $1 and id = $2::uuid and status = 'pending'`, pr.TenantID, journalID, string(status), failureCode, failureDetail)
		if err != nil {
			return err
		}
		out, err = readJournal(ctx, tx, pr.TenantID, journalID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if status == domain.PostingPosted {
			return enqueue(ctx, tx, "ic_journal", out.ID, icevents.JournalPosted, pr.TenantID, journalEvent(out))
		}
		return enqueue(ctx, tx, "ic_journal", out.ID, icevents.JournalCompensated, pr.TenantID, journalEvent(out))
	})
	if err != nil {
		return domain.JournalRecord{}, wrap("finish intercompany journal", err)
	}
	return out, nil
}

func journalEvent(r domain.JournalRecord) icevents.JournalData {
	d := icevents.JournalData{ID: r.ID, PairID: r.PairID, Source: r.Source, SourceRef: r.SourceRef, PostingDate: r.PostingDate.Format("2006-01-02"),
		Status: string(r.Status), FailureCode: r.FailureCode}
	for _, e := range r.Entries {
		d.Entries = append(d.Entries, icevents.JournalEntryData{Entity: e.Entity, LegalEntityID: e.Target.LegalEntityID, LedgerEntryID: e.LedgerEntryID, ReversalEntryID: e.ReversalEntryID})
	}
	return d
}

// GetJournal returns one journal with its entries, or IC-0844.
func (s *Postgres) GetJournal(ctx context.Context, id string) (domain.JournalRecord, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.JournalRecord{}, err
	}
	var out domain.JournalRecord
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = readJournal(ctx, tx, pr.TenantID, id)
		return err
	})
	if err != nil {
		return domain.JournalRecord{}, wrap("get intercompany journal", err)
	}
	return out, nil
}

// JournalFilter narrows a journal listing; empty fields match everything.
type JournalFilter struct {
	Status string
	Source string
}

// JournalPage is one page of journals, newest first.
type JournalPage struct {
	Items []domain.JournalRecord
	Next  string
}

// ListJournals returns one page of journals with their entries, newest first, after the cursor (the id of the last journal of the previous page).
func (s *Postgres) ListJournals(ctx context.Context, f JournalFilter, after string, limit int) (JournalPage, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return JournalPage{}, err
	}
	page := JournalPage{Items: []domain.JournalRecord{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+journalColumns+` from ic_journal
			where tenant_id = $1
			  and ($2::text = '' or status = $2)
			  and ($3::text = '' or source_type = $3)
			  and ($4::text = '' or (created_at, id) < (select created_at, id from ic_journal where tenant_id = $1 and id = $4::uuid))
			order by created_at desc, id desc limit $5`, pr.TenantID, f.Status, f.Source, after, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			r, err := scanJournal(rows)
			if err != nil {
				rows.Close()
				return err
			}
			page.Items = append(page.Items, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range page.Items {
			if page.Items[i].Entries, err = loadEntries(ctx, tx, pr.TenantID, page.Items[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return JournalPage{}, wrap("list intercompany journals", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = page.Items[limit-1].ID
	}
	return page, nil
}
