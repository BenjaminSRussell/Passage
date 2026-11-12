package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
)

// Review states of the break-glass queue.
const (
	ReviewPending  = "pending"
	ReviewReviewed = "reviewed"
	ReviewAll      = "all"
)

// ReadReview is the second person's review of a break-glass read.
type ReadReview struct {
	Outcome    string    `json:"outcome"`
	Note       string    `json:"note,omitempty"`
	ReviewerID string    `json:"reviewerId"`
	ReviewedAt time.Time `json:"reviewedAt"`
}

// BreakGlassRead is a read recorded with the break-glass reason code and its review, absent while pending.
type BreakGlassRead struct {
	ReadRecord
	Review *ReadReview `json:"review,omitempty"`
}

// breakGlassWhere matches domain.IsBreakGlass in SQL (the partial index phi_read_break_glass covers the prefix).
const breakGlassWhere = `upper(btrim(r.purpose)) like 'BREAK-GLASS%' and (length(btrim(r.purpose)) = 11 or substr(upper(btrim(r.purpose)), 12, 1) in (':', ' '))`

const breakGlassSelect = `select r.id, r.seq, r.entity, r.record_id, r.fields, r.purpose, r.user_id, r.source_service, r.trace_id,
	r.occurred_at, r.prev_hash, r.hash, v.outcome, v.note, v.reviewer_id, v.reviewed_at
	from phi_read r left join phi_read_review v on v.tenant_id = r.tenant_id and v.read_id = r.id
	where r.tenant_id = $1 and ` + breakGlassWhere

func scanBreakGlass(row pgx.Row) (BreakGlassRead, error) {
	var (
		b                       BreakGlassRead
		outcome, note, reviewer *string
		reviewedAt              *time.Time
	)
	r := &b.ReadRecord
	if err := row.Scan(&r.ID, &r.Seq, &r.Entity, &r.RecordID, &r.Fields, &r.Purpose, &r.UserID, &r.SourceService, &r.TraceID,
		&r.OccurredAt, &r.PrevHash, &r.Hash, &outcome, &note, &reviewer, &reviewedAt); err != nil {
		return b, err
	}
	if outcome != nil {
		b.Review = &ReadReview{Outcome: *outcome, Note: deref(note), ReviewerID: deref(reviewer), ReviewedAt: *reviewedAt}
	}
	return b, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ListBreakGlass returns break-glass reads in chain order: status is pending (no review yet), reviewed or all. A
// positive limit returns at most limit rows after afterSeq.
func (s *Postgres) ListBreakGlass(ctx context.Context, status string, limit int, afterSeq int64) ([]BreakGlassRead, error) {
	p, _ := tenant.FromContext(ctx)
	query := breakGlassSelect + ` and r.seq > $2`
	switch status {
	case ReviewPending:
		query += ` and v.read_id is null`
	case ReviewReviewed:
		query += ` and v.read_id is not null`
	}
	query += ` order by r.seq`
	args := []any{p.TenantID, afterSeq}
	if limit > 0 {
		query += ` limit $3`
		args = append(args, limit)
	}
	out := []BreakGlassRead{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBreakGlass(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// ReviewRead records reviewerID's review of a break-glass read. It refuses (AUD-0005) a read that is not break-glass,
// one already reviewed and a review by the reader; an unknown read is AUD-0004.
func (s *Postgres) ReviewRead(ctx context.Context, readID, reviewerID, outcome, note string) (BreakGlassRead, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(readID) {
		return BreakGlassRead{}, httpx.NewProblem(http.StatusNotFound, domain.CodeReadNotFound, "no read with that id in this tenant's read log")
	}
	var out BreakGlassRead
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var purpose, reader string
		var reviewed bool
		err := tx.QueryRow(ctx, `select r.purpose, r.user_id, v.read_id is not null
			from phi_read r left join phi_read_review v on v.tenant_id = r.tenant_id and v.read_id = r.id
			where r.tenant_id = $1 and r.id = $2`, p.TenantID, readID).Scan(&purpose, &reader, &reviewed)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, domain.CodeReadNotFound, "no read with that id in this tenant's read log")
		}
		if err != nil {
			return err
		}
		if why := domain.ReviewRefusal(purpose, reader, reviewerID, reviewed); why != "" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeReviewRefused, why)
		}
		if _, err := tx.Exec(ctx, `insert into phi_read_review (tenant_id, read_id, outcome, note, reviewer_id) values ($1, $2, $3, $4, $5)`,
			p.TenantID, readID, outcome, strings.TrimSpace(note), reviewerID); err != nil {
			// passage_app may not lock phi_read rows (it holds SELECT and INSERT only), so two reviews racing for one
			// read meet at the primary key instead.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return httpx.NewProblem(http.StatusConflict, domain.CodeReviewRefused, "this read has already been reviewed")
			}
			return err
		}
		out, err = scanBreakGlass(tx.QueryRow(ctx, breakGlassSelect+` and r.id = $2`, p.TenantID, readID))
		return err
	})
	return out, err
}
