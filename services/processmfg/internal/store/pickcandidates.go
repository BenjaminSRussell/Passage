package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
)

// OnHand is the quantity of one stored batch that is available to pick. The
// batch register keeps no quantity (that is the wave-16 inventory ledger), so
// the caller passes it in.
type OnHand struct {
	BatchID string
	Qty     decimal.Decimal
}

// PickCandidate is a stored batch ready for the picking rules: the domain batch
// (disposition, expiry and current attribute values read from the register, the
// quantity from the caller) and its batch number for display.
type PickCandidate struct {
	Batch       domain.Batch
	BatchNumber string
}

// PickCandidates reads the batches named in onHand, in the order given, for a
// picking proposal of itemID (FR-24.3, FR-24.5, FR-24.6). It returns each
// batch with its stored disposition, expiry date and current attribute values,
// and the name of the item's potency attribute ("" when it defines none). A
// batch that does not exist in the caller's scope is PMF-0108; one of another
// item is COM-0001. The batches and their current values are read with one
// query each, whatever the number of batches.
func (s *Postgres) PickCandidates(ctx context.Context, itemID string, onHand []OnHand) ([]PickCandidate, string, error) {
	out := make([]PickCandidate, 0, len(onHand))
	potency := ""
	batchIDs := make([]string, len(onHand))
	for i, oh := range onHand {
		batchIDs[i] = oh.BatchID
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select coalesce(max(name), '') from batch_attribute_def where item_id = $1 and role = 'potency'`, itemID).Scan(&potency); err != nil {
			return err
		}
		type stored struct {
			item, number, disposition string
			expiry                    *time.Time
		}
		found := make(map[string]stored, len(onHand))
		rows, err := tx.Query(ctx, `select id::text, item_id::text, batch_number, disposition, expiry_date from batch_record where id = any($1::uuid[])`, batchIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var b stored
			if err := rows.Scan(&id, &b.item, &b.number, &b.disposition, &b.expiry); err != nil {
				rows.Close()
				return err
			}
			found[id] = b
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		values := make(map[string]map[string]domain.Value, len(onHand))
		rows, err = tx.Query(ctx, `select distinct on (batch_id, name) batch_id::text, name, num_value, text_value, date_value
			from batch_attribute_history where batch_id = any($1::uuid[]) order by batch_id, name, seq desc`, batchIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, name string
			var v domain.Value
			if err := rows.Scan(&id, &name, &v.Num, &v.Text, &v.Date); err != nil {
				rows.Close()
				return err
			}
			if values[id] == nil {
				values[id] = map[string]domain.Value{}
			}
			values[id][name] = v
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, oh := range onHand {
			b, ok := found[oh.BatchID]
			if !ok {
				return notFound(CodeBatchNotFound, "batch", oh.BatchID)
			}
			if b.item != itemID {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, fmt.Sprintf("batch %s is not a batch of item %s", b.number, itemID))
			}
			vals := values[oh.BatchID]
			if vals == nil {
				vals = map[string]domain.Value{}
			}
			// The register's expiry date is the one the batch carries (FR-24.6).
			if b.expiry != nil {
				vals[domain.ExpiryAttribute] = domain.Value{Date: b.expiry}
			}
			out = append(out, PickCandidate{BatchNumber: b.number, Batch: domain.Batch{
				ID: oh.BatchID, ItemID: itemID, Disposition: b.disposition, Qty: oh.Qty, Values: vals}})
		}
		return nil
	})
	return out, potency, err
}

// RegisteredBatches returns those of batchIDs (UUIDs) that the batch register
// holds in the caller's scope, so the caller-supplied picking proposal can
// refuse them: their disposition and expiry are on record and must not be
// restated by the caller.
func (s *Postgres) RegisteredBatches(ctx context.Context, batchIDs []string) ([]string, error) {
	out := []string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from batch_record where id = any($1::uuid[]) order by id`, batchIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}
