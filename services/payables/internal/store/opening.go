package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var ErrOpeningKeyConflict = errors.New("batch key already imported with different content")

// OpeningReferenceError names the item references an earlier batch already imported, with that batch's key.
type OpeningReferenceError struct{ Taken map[string]string }

func (e *OpeningReferenceError) Error() string {
	return "item references already imported in another batch"
}

type OpeningItem struct {
	Reference     string
	VendorID      string
	InvoiceNumber string
	InvoiceDate   string
	DueDate       string
	Currency      string
	AmountMinor   int64
}

type OpeningImport struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	ImportedBy    string
	ControlTotals map[string]int64
	Items         []OpeningItem
}

// OpeningBatch is an imported batch with the counts and totals read back from its stored items.
type OpeningBatch struct {
	ID            string           `json:"id"`
	LegalEntityID string           `json:"legalEntityId"`
	BatchKey      string           `json:"batchKey"`
	ItemCount     int              `json:"itemCount"`
	Totals        map[string]int64 `json:"totalsMinor"`
	ImportedBy    string           `json:"importedBy"`
	ImportedAt    time.Time        `json:"importedAt"`
}

func toMinor(amount decimal.Decimal, currency string) int64 {
	return amount.Shift(money.MinorUnits(currency)).IntPart()
}

// ImportOpeningBatch stores a cutover batch of open vendor items without touching the ledger. The same
// key with the same content is a replay (false); the same key with other content is
// ErrOpeningKeyConflict; an item reference taken by another batch is an *OpeningReferenceError.
func (s *Postgres) ImportOpeningBatch(ctx context.Context, in OpeningImport) (OpeningBatch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out OpeningBatch
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('ap_opening_batch:' || $1 || ':' || $2 || ':' || $3, 0))`, p.TenantID, in.LegalEntityID, in.BatchKey); err != nil {
			return err
		}
		var id, hash string
		err := tx.QueryRow(ctx, `select id::text, content_hash from ap_opening_batch where legal_entity_id = $1::uuid and batch_key = $2`, in.LegalEntityID, in.BatchKey).Scan(&id, &hash)
		if err == nil {
			if hash != in.ContentHash {
				return ErrOpeningKeyConflict
			}
			batches, err := openingBatches(ctx, tx, in.LegalEntityID, in.BatchKey, 1)
			if err != nil || len(batches) == 0 {
				return errors.Join(err, pgx.ErrNoRows)
			}
			out = batches[0]
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := rejectTakenReferences(ctx, tx, in); err != nil {
			return err
		}
		totals, _ := json.Marshal(in.ControlTotals)
		id = ids.New()
		if _, err := tx.Exec(ctx, `insert into ap_opening_batch (tenant_id, id, legal_entity_id, batch_key, content_hash, item_count, control_totals, imported_by)
			values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8)`, p.TenantID, id, in.LegalEntityID, in.BatchKey, in.ContentHash, len(in.Items), totals, in.ImportedBy); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, it := range in.Items {
			amount := decimal.New(it.AmountMinor, -money.MinorUnits(it.Currency)).String()
			batch.Queue(`insert into ap_open_item (tenant_id, id, legal_entity_id, vendor_id, source_invoice_id, invoice_number, invoice_date, due_date, due_date_source,
				currency, amount, open_amount, status, opening_batch_id, opening_reference)
				values ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7::date, $8::date, $9, $10, $11::numeric, $11::numeric, 'OPEN', $12::uuid, $13)`,
				p.TenantID, ids.New(), in.LegalEntityID, it.VendorID, ids.New(), it.InvoiceNumber, it.InvoiceDate, it.DueDate, DueDateEvent, it.Currency, amount, id, it.Reference)
		}
		res := tx.SendBatch(ctx, batch)
		for range in.Items {
			if _, err := res.Exec(); err != nil {
				_ = res.Close()
				return err
			}
		}
		if err := res.Close(); err != nil {
			return err
		}
		created = true
		batches, err := openingBatches(ctx, tx, in.LegalEntityID, in.BatchKey, 1)
		if err != nil || len(batches) == 0 {
			return errors.Join(err, pgx.ErrNoRows)
		}
		out = batches[0]
		return nil
	})
	return out, created, err
}

func rejectTakenReferences(ctx context.Context, tx pgx.Tx, in OpeningImport) error {
	refs := make([]string, len(in.Items))
	for i, it := range in.Items {
		refs[i] = it.Reference
	}
	rows, err := tx.Query(ctx, `select i.opening_reference, b.batch_key from ap_open_item i join ap_opening_batch b on b.tenant_id = i.tenant_id and b.id = i.opening_batch_id
		where i.legal_entity_id = $1::uuid and i.opening_reference = any($2)`, in.LegalEntityID, refs)
	if err != nil {
		return err
	}
	defer rows.Close()
	taken := map[string]string{}
	for rows.Next() {
		var ref, key string
		if err := rows.Scan(&ref, &key); err != nil {
			return err
		}
		taken[ref] = key
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(taken) > 0 {
		return &OpeningReferenceError{Taken: taken}
	}
	return nil
}

// ListOpeningBatches returns a legal entity's imported batches, newest first, optionally one batch key.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int) ([]OpeningBatch, error) {
	var out []OpeningBatch
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = openingBatches(ctx, tx, legalEntityID, batchKey, min(max(limit, 1), maxLimit))
		return err
	})
	return out, err
}

func openingBatches(ctx context.Context, tx pgx.Tx, legalEntityID, batchKey string, limit int) ([]OpeningBatch, error) {
	rows, err := tx.Query(ctx, `select b.id::text, b.legal_entity_id::text, b.batch_key, b.imported_by, b.imported_at,
			coalesce(i.currency, ''), count(i.id), coalesce(sum(i.amount), 0)::text
		from (select * from ap_opening_batch where legal_entity_id = $1::uuid and ($2 = '' or batch_key = $2) order by imported_at desc, id limit $3) b
		left join ap_open_item i on i.tenant_id = b.tenant_id and i.opening_batch_id = b.id
		group by b.id, b.legal_entity_id, b.batch_key, b.imported_by, b.imported_at, i.currency
		order by b.imported_at desc, b.id, i.currency`, legalEntityID, batchKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpeningBatch{}
	for rows.Next() {
		var b OpeningBatch
		var currency, sum string
		var n int
		if err := rows.Scan(&b.ID, &b.LegalEntityID, &b.BatchKey, &b.ImportedBy, &b.ImportedAt, &currency, &n, &sum); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != b.ID {
			b.Totals = map[string]int64{}
			out = append(out, b)
		}
		cur := &out[len(out)-1]
		cur.ItemCount += n
		if currency != "" {
			amount, err := decimal.NewFromString(sum)
			if err != nil {
				return nil, err
			}
			cur.Totals[currency] = toMinor(amount, currency)
		}
	}
	return out, rows.Err()
}
