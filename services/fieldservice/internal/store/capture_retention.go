package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/objectstore"
)

// MaxCaptureBatch bounds one offload or purge call.
const MaxCaptureBatch = 200

// CaptureRetentionResult counts what one pass of the capture retention job did.
type CaptureRetentionResult struct {
	Offloaded int `json:"offloaded"`
	Purged    int `json:"purged"`
	Failed    int `json:"failed"`
}

// CaptureLegalEntities lists the legal entities of the caller's tenant that still hold capture bytes, so the retention job can
// run under a principal scoped to each. The caller needs the all-scope duty to see them all.
func (s *Postgres) CaptureLegalEntities(ctx context.Context) ([]string, error) {
	tid := tenantOf(ctx)
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from service_wo_capture where tenant_id = $1 and purged_at is null order by 1`, tid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var le string
			if err := rows.Scan(&le); err != nil {
				return err
			}
			out = append(out, le)
		}
		return rows.Err()
	})
	return out, err
}

type captureBody struct {
	id, workOrderID, contentType, key string
}

// OffloadCaptures moves captures still held in Postgres to the object store, oldest first, at most limit of them: the bytes
// are written under the capture's key, then the row drops them and names the key. It does nothing without an object store.
// A crash between the two steps leaves the object and the row's bytes, and the next pass writes the same key again.
func (s *Postgres) OffloadCaptures(ctx context.Context, limit int) (int, error) {
	if s.objects == nil {
		return 0, nil
	}
	if limit < 1 || limit > MaxCaptureBatch {
		limit = MaxCaptureBatch
	}
	tid := tenantOf(ctx)
	moved := 0
	for moved < limit {
		var b captureBody
		var data []byte
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select id::text, work_order_id::text, content_type, data from service_wo_capture
				where tenant_id = $1 and purged_at is null and object_key is null and data is not null order by created_at, id limit 1`, tid).
				Scan(&b.id, &b.workOrderID, &b.contentType, &data)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return moved, nil
		}
		if err != nil {
			return moved, err
		}
		key := objectstore.Key(tid, b.workOrderID, b.id)
		if err := s.objects.Put(ctx, key, b.contentType, data); err != nil {
			return moved, fmt.Errorf("offload capture %s: %w", b.id, err)
		}
		err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update service_wo_capture set data = null, object_key = $3 where tenant_id = $1 and id = $2 and object_key is null and purged_at is null`, tid, b.id, key)
			return err
		})
		if err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// PurgeCaptures deletes the bytes of the captures of work orders that were completed, posted or cancelled more than
// retention before now, at most limit of them, oldest first. The object (or the Postgres bytes) goes and the row stays with
// purged_at set, so the record of the image and its hash remain. A retention of zero or less does nothing.
func (s *Postgres) PurgeCaptures(ctx context.Context, now time.Time, retention time.Duration, limit int) (CaptureRetentionResult, error) {
	var res CaptureRetentionResult
	if retention <= 0 {
		return res, nil
	}
	if limit < 1 || limit > MaxCaptureBatch {
		limit = MaxCaptureBatch
	}
	tid := tenantOf(ctx)
	var due []captureBody
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select c.id::text, c.work_order_id::text, c.content_type, coalesce(c.object_key, '')
			from service_wo_capture c join service_work_order w on w.tenant_id = c.tenant_id and w.id = c.work_order_id
			where c.tenant_id = $1 and c.purged_at is null and w.status in ('COMPLETED', 'POSTED', 'CANCELED')
			  and coalesce(w.completed_at, w.updated_at) <= $2 order by coalesce(w.completed_at, w.updated_at), c.id limit $3`, tid, now.Add(-retention), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b captureBody
			if err := rows.Scan(&b.id, &b.workOrderID, &b.contentType, &b.key); err != nil {
				return err
			}
			due = append(due, b)
		}
		return rows.Err()
	})
	if err != nil {
		return res, err
	}
	for _, b := range due {
		if b.key != "" {
			if s.objects == nil {
				res.Failed++
				continue
			}
			if err := s.objects.Delete(ctx, b.key); err != nil {
				slog.ErrorContext(ctx, "capture object not deleted", "captureId", b.id, "workOrderId", b.workOrderID, "err", err)
				res.Failed++
				continue
			}
		}
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update service_wo_capture set data = null, object_key = null, purged_at = $3 where tenant_id = $1 and id = $2 and purged_at is null`, tid, b.id, now)
			return err
		})
		if err != nil {
			slog.ErrorContext(ctx, "capture not marked purged", "captureId", b.id, "err", err)
			res.Failed++
			continue
		}
		res.Purged++
	}
	return res, nil
}
