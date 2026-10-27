package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

// --- physical samples (FR-33.20) -------------------------------------------

// SampleFilter narrows the sample list. Status ACTIVE or DUE_FOR_DISPOSAL is
// judged against AsOf (a YYYY-MM-DD date).
type SampleFilter struct {
	QualityOrderID string
	Status         string
	AsOf           string
	Limit          int
}

const sampleColumns = `id, quality_order_id, kind, primary_ids, quantity::text, uom, container, storage_location, condition,
	coalesce(batch_expires_on::text, ''), retention_years, coalesce(retention_expires_on::text, ''), recorded_by, created_at`

func scanSample(row pgx.Row) (domain.Sample, error) {
	var s domain.Sample
	var primaries []byte
	var qty string
	if err := row.Scan(&s.ID, &s.QualityOrderID, &s.Kind, &primaries, &qty, &s.UOM, &s.Container, &s.StorageLocation, &s.Condition,
		&s.BatchExpiresOn, &s.RetentionYears, &s.RetentionExpiresOn, &s.RecordedBy, &s.CreatedAt); err != nil {
		return s, err
	}
	if err := json.Unmarshal(primaries, &s.PrimaryIDs); err != nil {
		return s, err
	}
	s.Quantity = decimal.RequireFromString(qty)
	return s, nil
}

// RegisterSample stores a physical sample of an order. Under the order's row
// lock it checks the order takes samples, that a POOLED sample's primaries are
// PRIMARY samples of the same order (QM-0181), and that samples drawn from the
// lot stay within the lot quantity (QM-0182). A replayed key returns the stored
// sample with inserted false; the key used on another order is refused. The
// returned status is not set; callers derive it (Sample.WithStatus).
func (s *Postgres) RegisterSample(ctx context.Context, in domain.Sample, key string) (domain.Sample, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if in.PrimaryIDs == nil {
		in.PrimaryIDs = []string{}
	}
	primaries, err := json.Marshal(in.PrimaryIDs)
	if err != nil {
		return domain.Sample{}, false, err
	}
	var out domain.Sample
	var inserted bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanSample(tx.QueryRow(ctx, `select `+sampleColumns+` from quality_sample where `+ownTenant+` and idempotency_key = $1`, key))
			if err == nil {
				if replay.QualityOrderID != in.QualityOrderID {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeyReuse, "Idempotency-Key was already used for a sample on another order")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		order, err := readOrder(ctx, tx, in.QualityOrderID, true)
		if err != nil {
			return err
		}
		if len(in.PrimaryIDs) > 0 {
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from quality_sample where `+ownTenant+` and quality_order_id = $1 and kind = 'PRIMARY'
				and id = any($2::uuid[])`, in.QualityOrderID, in.PrimaryIDs).Scan(&n); err != nil {
				return err
			}
			if n != len(in.PrimaryIDs) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeSamplePrimary,
					"every primary sample of a pooled sample must be a PRIMARY sample of the same quality order")
			}
		}
		var drawn string
		if err := tx.QueryRow(ctx, `select coalesce(sum(quantity), 0)::text from quality_sample where `+ownTenant+`
			and quality_order_id = $1 and kind <> 'POOLED'`, in.QualityOrderID).Scan(&drawn); err != nil {
			return err
		}
		if err := domain.CheckSampleOrder(order, in, decimal.RequireFromString(drawn)); err != nil {
			return err
		}
		smp, err := scanSample(tx.QueryRow(ctx, `insert into quality_sample (tenant_id, id, quality_order_id, kind, primary_ids, quantity, uom,
			container, storage_location, condition, batch_expires_on, retention_years, retention_expires_on, recorded_by, created_at, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10, nullif($11, '')::date, $12, nullif($13, '')::date, $14, $15, $16)
			returning `+sampleColumns,
			p.TenantID, in.ID, in.QualityOrderID, in.Kind, string(primaries), in.Quantity.String(), in.UOM, in.Container, in.StorageLocation,
			in.Condition, in.BatchExpiresOn, in.RetentionYears, in.RetentionExpiresOn, in.RecordedBy, in.CreatedAt, keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeCertSampleRace, "a sample with this Idempotency-Key is being registered; retry")
		}
		if err != nil {
			return err
		}
		out, inserted = smp, true
		return nil
	})
	return out, inserted, err
}

// ListSamples returns physical samples, newest first, optionally of one order and
// in one status as of f.AsOf. DUE_FOR_DISPOSAL means the retention expiry is on or
// before that date (AC-33.18).
func (s *Postgres) ListSamples(ctx context.Context, f SampleFilter) ([]domain.Sample, error) {
	if f.AsOf == "" {
		f.AsOf = time.Now().UTC().Format(domain.DateLayout)
	}
	out := []domain.Sample{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+sampleColumns+` from quality_sample
			where `+ownTenant+` and ($1 = '' or quality_order_id = nullif($1, '')::uuid)
			and ($2 = '' or ($2 = 'DUE_FOR_DISPOSAL') = (retention_expires_on is not null and retention_expires_on <= $3::date))
			order by created_at desc, id limit $4`, f.QualityOrderID, f.Status, f.AsOf, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			smp, err := scanSample(rows)
			if err != nil {
				return err
			}
			out = append(out, smp)
		}
		return rows.Err()
	})
	return out, err
}
