package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CodeOpeningKeyConflict is a batch key sent again with other content.
const CodeOpeningKeyConflict = "INV-OPB-002"

// OpeningBatch is one opening-stock batch of the migration factory: an OPENING_BALANCE journal that the import
// created, approved and posted. Value is the sum of quantity times cost price, unrounded (twelve decimals).
type OpeningBatch struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	JournalID     string
	Currency      string
	ItemCount     int
	Qty           decimal.Decimal
	Value         decimal.Decimal
}

// FindOpeningBatch returns the stored batch under the key, or false.
func (s *Postgres) FindOpeningBatch(ctx context.Context, key string) (OpeningBatch, bool, error) {
	var b OpeningBatch
	var found bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var q, v string
		err := tx.QueryRow(ctx, `select legal_entity_id::text, batch_key, content_hash, journal_id::text, currency, item_count, qty_total::text, value_total::text
			from inventory_opening_batch where tenant_id = current_setting('passage.tenant_id', true)::uuid and batch_key = $1`, key).
			Scan(&b.LegalEntityID, &b.BatchKey, &b.ContentHash, &b.JournalID, &b.Currency, &b.ItemCount, &q, &v)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if b.Qty, err = decimal.NewFromString(q); err != nil {
			return err
		}
		b.Value, err = decimal.NewFromString(v)
		return err
	})
	return b, found, err
}

// RecordOpeningBatch stores the posted batch. A second record of the same key is kept as it is: the first one
// wins, and a different content hash under the key is refused.
func (s *Postgres) RecordOpeningBatch(ctx context.Context, b OpeningBatch) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into inventory_opening_batch (tenant_id, legal_entity_id, batch_key, content_hash, journal_id, currency, item_count, qty_total, value_total)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict (tenant_id, batch_key) do nothing`,
			p.TenantID, b.LegalEntityID, b.BatchKey, b.ContentHash, b.JournalID, b.Currency, b.ItemCount, b.Qty.String(), b.Value.String()); err != nil {
			return scopeProblem(err)
		}
		var hash string
		if err := tx.QueryRow(ctx, `select content_hash from inventory_opening_batch where tenant_id = $1 and batch_key = $2`, p.TenantID, b.BatchKey).Scan(&hash); err != nil {
			return fmt.Errorf("read opening batch: %w", err)
		}
		if hash != b.ContentHash {
			return problem(http.StatusUnprocessableEntity, CodeOpeningKeyConflict, "the batch key was already used with other content")
		}
		return nil
	})
}

// ListOpeningBatches returns the legal entity's batches after the key, in key order.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, afterKey string, limit int) ([]OpeningBatch, error) {
	out := []OpeningBatch{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select legal_entity_id::text, batch_key, content_hash, journal_id::text, currency, item_count, qty_total::text, value_total::text
			from inventory_opening_batch where tenant_id = current_setting('passage.tenant_id', true)::uuid and legal_entity_id = $1 and batch_key > $2
			order by batch_key limit $3`, legalEntityID, afterKey, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b OpeningBatch
			var q, v string
			if err := rows.Scan(&b.LegalEntityID, &b.BatchKey, &b.ContentHash, &b.JournalID, &b.Currency, &b.ItemCount, &q, &v); err != nil {
				return err
			}
			if b.Qty, err = decimal.NewFromString(q); err != nil {
				return err
			}
			if b.Value, err = decimal.NewFromString(v); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
