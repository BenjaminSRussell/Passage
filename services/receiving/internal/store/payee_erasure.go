package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const ErasureBatch = 200

type PayeeCandidate struct {
	ID           string
	SubjectRef   string
	Country      string
	CreatedAt    time.Time
	LastActivity time.Time
}

type PayeeCursor struct {
	CreatedAt time.Time
	ID        string
}

type ErasePayeesInput struct {
	LegalEntityID string
	SubjectRef    string
	ErasureID     string
	Now           time.Time
}

type ErasePayeesResult struct {
	Erased   int `json:"erased"`
	Retained int `json:"retained"`
	Held     int `json:"held"`
}

type PayeeErasureStore interface {
	PayeeCandidates(ctx context.Context, legalEntityID, subjectRef string, after PayeeCursor, limit int) ([]PayeeCandidate, error)
	MarkPayeesErased(ctx context.Context, legalEntityID string, ids []string, erasureID string, now time.Time) (int, error)
}

func ErasePayees(ctx context.Context, s PayeeErasureStore, holds domain.LegalHolds, in ErasePayeesInput) (ErasePayeesResult, error) {
	var out ErasePayeesResult
	var cursor PayeeCursor
	for {
		page, err := s.PayeeCandidates(ctx, in.LegalEntityID, in.SubjectRef, cursor, ErasureBatch)
		if err != nil {
			return out, err
		}
		for _, c := range page {
			end, erasable := domain.PayeeRetentionEnd(c.Country, c.LastActivity)
			if !erasable || in.Now.Before(end) {
				out.Retained++
				continue
			}
			covering, err := holds.Covering(ctx, domain.HoldQuery{LegalEntityID: in.LegalEntityID, SubjectRef: c.SubjectRef, DataClass: domain.PayeeDataClass, RecordRef: c.ID})
			if err != nil {
				return out, fmt.Errorf("legal hold check for payee %s: %w", c.ID, err)
			}
			if len(covering) > 0 {
				out.Held++
				continue
			}
			n, err := s.MarkPayeesErased(ctx, in.LegalEntityID, []string{c.ID}, in.ErasureID, in.Now)
			out.Erased += n
			if err != nil {
				return out, err
			}
		}
		if len(page) < ErasureBatch {
			return out, nil
		}
		last := page[len(page)-1]
		cursor = PayeeCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
}

func (s *Postgres) PayeeCandidates(ctx context.Context, legalEntityID, subjectRef string, after PayeeCursor, limit int) ([]PayeeCandidate, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	afterID := after.ID
	if afterID == "" {
		afterID = ids.Nil
	}
	var out []PayeeCandidate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.id::text, coalesce(p.subject_ref, ''), p.country, p.created_at, greatest(p.created_at, coalesce(max(i.created_at), p.created_at))
			FROM receiving_one_time_payee p
			LEFT JOIN receiving_vendor_invoice i ON i.tenant_id = p.tenant_id AND i.one_time_payee_id = p.id
			WHERE p.tenant_id = $1 AND p.legal_entity_id = $2 AND p.erased_at IS NULL AND ($3 = '' OR p.subject_ref = $3)
				AND (p.created_at, p.id) > ($5, $6::uuid)
			GROUP BY p.tenant_id, p.id
			ORDER BY p.created_at, p.id
			LIMIT $4`, tenantID, legalEntityID, subjectRef, limit, after.CreatedAt, afterID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c PayeeCandidate
			if err := rows.Scan(&c.ID, &c.SubjectRef, &c.Country, &c.CreatedAt, &c.LastActivity); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) MarkPayeesErased(ctx context.Context, legalEntityID string, ids []string, erasureID string, now time.Time) (int, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	var erasure *string
	if erasureID != "" {
		erasure = &erasureID
	}
	var n int
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE receiving_one_time_payee
			SET name_sealed = '\x'::bytea, address_sealed = '\x'::bytea, tax_id_sealed = NULL, email_sealed = NULL, bank_sealed = NULL, erased_at = $4, erasure_id = $5
			WHERE tenant_id = $1 AND legal_entity_id = $2 AND id = ANY($3::uuid[]) AND erased_at IS NULL`, tenantID, legalEntityID, ids, now, erasure)
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}
