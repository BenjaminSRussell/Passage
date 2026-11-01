package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

// --- incoming certificates (FR-33.15, BR-33.13) ----------------------------

const certColumns = `id, quality_order_id, cert_type, document_ref, certificate_values, in_spec, deviations, waiver, reason,
	coalesce(nonconformance_id::text, ''), recorded_by, recorded_at`

func scanCert(row pgx.Row) (domain.CertRecord, error) {
	var c domain.CertRecord
	var values, deviations []byte
	if err := row.Scan(&c.ID, &c.OrderID, &c.Type, &c.DocumentRef, &values, &c.InSpec, &deviations, &c.Waiver, &c.Reason,
		&c.NonconformanceID, &c.RecordedBy, &c.RecordedAt); err != nil {
		return c, err
	}
	if err := json.Unmarshal(values, &c.Values); err != nil {
		return c, err
	}
	return c, json.Unmarshal(deviations, &c.Deviations)
}

// RecordCertificate appends a certificate receipt or a waiver to an order. The
// order is locked first, so the record and a concurrent usage decision serialise:
// a closed or cancelled order takes no record, and a waiver needs an order that
// requires a certificate. ncr, when given (an out-of-specification receipt), is
// written with its nonconformance.created event in the same transaction and
// linked from the record. A replayed key returns the stored record with
// inserted false; the key used on another order is refused.
func (s *Postgres) RecordCertificate(ctx context.Context, rec domain.CertRecord, ncr *domain.Nonconformance, key string) (domain.CertRecord, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if rec.Values == nil {
		rec.Values = map[string]domain.CertValue{}
	}
	if rec.Deviations == nil {
		rec.Deviations = []string{}
	}
	values, err := json.Marshal(rec.Values)
	if err != nil {
		return domain.CertRecord{}, false, err
	}
	deviations, err := json.Marshal(rec.Deviations)
	if err != nil {
		return domain.CertRecord{}, false, err
	}
	var out domain.CertRecord
	var inserted bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanCert(tx.QueryRow(ctx, `select `+certColumns+` from quality_certificate where `+ownTenant+` and idempotency_key = $1`, key))
			if err == nil {
				if replay.OrderID != rec.OrderID {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeKeyReuse, "Idempotency-Key was already used for a certificate on another order")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		order, err := readOrder(ctx, tx, rec.OrderID, true)
		if err != nil {
			return err
		}
		if err := domain.CheckCertificateOrder(order, rec.Waiver); err != nil {
			return err
		}
		if ncr != nil {
			saved, err := insertNCR(ctx, tx, p.TenantID, *ncr, "")
			if err != nil {
				return err
			}
			rec.NonconformanceID = saved.ID
			if err := s.holdForNCR(ctx, tx, saved); err != nil {
				return err
			}
		}
		c, err := scanCert(tx.QueryRow(ctx, `insert into quality_certificate (tenant_id, id, quality_order_id, cert_type, document_ref,
			certificate_values, in_spec, deviations, waiver, reason, nonconformance_id, recorded_by, recorded_at, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) returning `+certColumns,
			p.TenantID, rec.ID, rec.OrderID, rec.Type, rec.DocumentRef, string(values), rec.InSpec, string(deviations), rec.Waiver,
			rec.Reason, nullable(rec.NonconformanceID), rec.RecordedBy, rec.RecordedAt, keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeCertSampleRace, "a certificate with this Idempotency-Key is being recorded; retry")
		}
		if err != nil {
			return err
		}
		out, inserted = c, true
		return nil
	})
	if inserted {
		s.FlushHolds(ctx)
	}
	return out, inserted, err
}

// certificatesTx returns the certificate records of an order, oldest first.
func certificatesTx(ctx context.Context, tx pgx.Tx, orderID string) ([]domain.CertRecord, error) {
	out := []domain.CertRecord{}
	rows, err := tx.Query(ctx, `select `+certColumns+` from quality_certificate where `+ownTenant+` and quality_order_id = $1
		order by recorded_at, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListCertificates returns every receipt and waiver recorded against an order, oldest first.
func (s *Postgres) ListCertificates(ctx context.Context, orderID string) ([]domain.CertRecord, error) {
	var out []domain.CertRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = certificatesTx(ctx, tx, orderID)
		return err
	})
	return out, err
}
