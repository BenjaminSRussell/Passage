package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
)

// ReadRecord is one row of the PHI read-access log.
type ReadRecord struct {
	ID            string    `json:"id"`
	Seq           int64     `json:"seq"`
	Entity        string    `json:"entity"`
	RecordID      string    `json:"recordId"`
	Fields        []string  `json:"fields"`
	Purpose       string    `json:"purpose"`
	UserID        string    `json:"userId"`
	SourceService string    `json:"sourceService"`
	TraceID       string    `json:"traceId"`
	OccurredAt    time.Time `json:"occurredAt"`
	PrevHash      string    `json:"prevHash"`
	Hash          string    `json:"hash"`
}

// ReadFilter narrows the read log. From is inclusive, To exclusive; AfterSeq resumes after a cursor; a Limit of
// zero means no limit (export).
type ReadFilter struct {
	Entity, RecordID, UserID string
	From, To                 *time.Time
	AfterSeq                 int64
	Limit                    int
}

// ReadChain is the verification result of a tenant's whole read-log chain.
type ReadChain struct {
	Verified bool   `json:"verified"`
	Entries  int64  `json:"entries"`
	Head     string `json:"head"`
	// BrokenAt is the 0-based position of the first row that does not link; absent when the chain verifies.
	BrokenAt *int64 `json:"brokenAt,omitempty"`
}

func chainOf(s domain.ChainStatus) ReadChain {
	c := ReadChain{Verified: s.Verified, Entries: s.Entries, Head: s.Head}
	if !s.Verified {
		at := s.BrokenAt
		c.BrokenAt = &at
	}
	return c
}

const readColumns = `id, seq, entity, record_id, fields, purpose, user_id, source_service, trace_id, occurred_at, prev_hash, hash`

func scanRead(rows pgx.Rows) (ReadRecord, error) {
	var r ReadRecord
	err := rows.Scan(&r.ID, &r.Seq, &r.Entity, &r.RecordID, &r.Fields, &r.Purpose, &r.UserID, &r.SourceService, &r.TraceID, &r.OccurredAt, &r.PrevHash, &r.Hash)
	return r, err
}

func (r ReadRecord) entry() domain.ReadEntry {
	return domain.ReadEntry{
		Read: domain.Read{
			Entity: r.Entity, RecordID: r.RecordID, Fields: r.Fields, Purpose: r.Purpose, UserID: r.UserID,
			Source: r.SourceService, TraceID: r.TraceID, OccurredAt: r.OccurredAt,
		},
		PrevHash: r.PrevHash, Hash: r.Hash,
	}
}

// readWhere builds the filter clause after "tenant_id = $1"; seqCap, when above zero, bounds the rows to a verified head.
func readWhere(f ReadFilter, seqCap int64) (string, []any) {
	args := []any{}
	clause := ""
	add := func(format string, v any) {
		args = append(args, v)
		clause += " and " + format + "$" + strconv.Itoa(len(args)+1)
	}
	if f.Entity != "" {
		add("entity = ", f.Entity)
	}
	if f.RecordID != "" {
		add("record_id = ", f.RecordID)
	}
	if f.UserID != "" {
		add("user_id = ", f.UserID)
	}
	if f.From != nil {
		add("occurred_at >= ", *f.From)
	}
	if f.To != nil {
		add("occurred_at < ", *f.To)
	}
	if f.AfterSeq > 0 {
		add("seq > ", f.AfterSeq)
	}
	if seqCap > 0 {
		add("seq <= ", seqCap)
	}
	return clause, args
}

// RecordRead appends a read to the tenant's chain. Appends are serialised per tenant so two concurrent reads
// cannot both link to the same previous hash.
func (s *Postgres) RecordRead(ctx context.Context, read domain.Read) (ReadRecord, error) {
	p, _ := tenant.FromContext(ctx)
	read = read.Normalize()
	var out ReadRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('phi_read:' || $1::text, 0))`, p.TenantID); err != nil {
			return err
		}
		var prev string
		err := tx.QueryRow(ctx, `select hash from phi_read where tenant_id=$1 order by seq desc limit 1`, p.TenantID).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			prev, err = domain.Genesis, nil
		}
		if err != nil {
			return err
		}
		entry, err := domain.LinkRead(prev, read, time.Now())
		if err != nil {
			return err
		}
		id := ids.New()
		var seq int64
		if err := tx.QueryRow(ctx, `insert into phi_read
			(tenant_id, id, entity, record_id, fields, purpose, user_id, source_service, trace_id, occurred_at, prev_hash, hash)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) returning seq`,
			p.TenantID, id, entry.Entity, entry.RecordID, entry.Fields, entry.Purpose, entry.UserID, entry.Source, entry.TraceID, entry.OccurredAt, entry.PrevHash, entry.Hash).Scan(&seq); err != nil {
			return err
		}
		out = ReadRecord{
			ID: id, Seq: seq, Entity: entry.Entity, RecordID: entry.RecordID, Fields: entry.Fields, Purpose: entry.Purpose,
			UserID: entry.UserID, SourceService: entry.Source, TraceID: entry.TraceID, OccurredAt: entry.OccurredAt,
			PrevHash: entry.PrevHash, Hash: entry.Hash,
		}
		return nil
	})
	return out, err
}

// ListReads returns read-log rows in chain order. A positive Limit returns at most Limit rows.
func (s *Postgres) ListReads(ctx context.Context, f ReadFilter) ([]ReadRecord, error) {
	p, _ := tenant.FromContext(ctx)
	out := []ReadRecord{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return queryReads(ctx, tx, p.TenantID, f, 0, func(r ReadRecord) error {
			out = append(out, r)
			return nil
		})
	})
	return out, err
}

func queryReads(ctx context.Context, tx pgx.Tx, tenantID string, f ReadFilter, seqCap int64, each func(ReadRecord) error) error {
	where, args := readWhere(f, seqCap)
	query := `select ` + readColumns + ` from phi_read where tenant_id = $1` + where + ` order by seq`
	if f.Limit > 0 {
		query += ` limit ` + strconv.Itoa(f.Limit)
	}
	rows, err := tx.Query(ctx, query, append([]any{tenantID}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRead(rows)
		if err != nil {
			return err
		}
		if err := each(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// verifyReads walks the tenant's whole chain from Genesis and returns its status and the highest sequence number seen.
func verifyReads(ctx context.Context, tx pgx.Tx, tenantID string) (ReadChain, int64, error) {
	walker := domain.NewChainWalker()
	var last int64
	err := queryReads(ctx, tx, tenantID, ReadFilter{}, 0, func(r ReadRecord) error {
		walker.Next(r.entry())
		last = r.Seq
		return nil
	})
	return chainOf(walker.Status()), last, err
}

// VerifyReads reports whether the tenant's read-log chain is intact.
func (s *Postgres) VerifyReads(ctx context.Context) (ReadChain, error) {
	p, _ := tenant.FromContext(ctx)
	var chain ReadChain
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		chain, _, err = verifyReads(ctx, tx, p.TenantID)
		return err
	})
	return chain, err
}

// ExportReads verifies the tenant's chain, hands the result to begin (which writes the response headers), then streams
// the filtered rows to each. Rows appended after the verification are left out, so every exported row is covered by it.
func (s *Postgres) ExportReads(ctx context.Context, f ReadFilter, begin func(ReadChain) error, each func(ReadRecord) error) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		chain, last, err := verifyReads(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		if err := begin(chain); err != nil {
			return err
		}
		if last == 0 {
			return nil
		}
		return queryReads(ctx, tx, p.TenantID, f, last, each)
	})
}
