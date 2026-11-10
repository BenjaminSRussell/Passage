package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	TypeParty         = "party"
	TypeProduct       = "product"
	TypePurchaseOrder = "purchase_order"

	wordSimilarityThreshold = "0.4"
)

var errNoPrincipal = errors.New("search: no principal in context")

func LegalEntityScoped(recordType string) bool { return recordType == TypePurchaseOrder }

type Record struct {
	Type          string
	ID            string
	LegalEntityID string
	Number        string
	Title         string
	Subtitle      string
	Status        string
	Removed       bool
	At            time.Time
}

type Hit struct {
	Type          string
	ID            string
	LegalEntityID string
	Number        string
	Title         string
	Subtitle      string
	Status        string
}

type Query struct {
	Text  string
	Types []string
	Limit int
}

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Apply writes r unless the row already holds a newer event. Empty text fields
// keep what the row holds, so a status-only event (a cancellation) leaves the
// title alone. It reports whether the row changed.
func (s *Store) Apply(ctx context.Context, r Record) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errNoPrincipal
	}
	applied := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		args := []any{p.TenantID, r.Type, r.ID, r.Number, r.Title, r.Subtitle, r.Status, r.Removed, r.At}
		table, columns, values := "search_record_tenant", "", ""
		if LegalEntityScoped(r.Type) {
			table, columns, values = "search_record", "legal_entity_id, ", "$10, "
			args = append(args, r.LegalEntityID)
		}
		tag, err := tx.Exec(ctx, fmt.Sprintf(`insert into %[1]s as t (tenant_id, %[2]srecord_type, record_id, number, title, subtitle, status, removed, source_at)
			values ($1, %[3]s$2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, record_type, record_id) do update set
			  number = coalesce(nullif(excluded.number, ''), t.number),
			  title = coalesce(nullif(excluded.title, ''), t.title),
			  subtitle = coalesce(nullif(excluded.subtitle, ''), t.subtitle),
			  status = coalesce(nullif(excluded.status, ''), t.status),
			  removed = excluded.removed, source_at = excluded.source_at, indexed_at = now()
			where t.source_at <= excluded.source_at`, table, columns, values), args...)
		applied = err == nil && tag.RowsAffected() == 1
		return err
	})
	return applied, err
}

// Search returns the live records of the given types that match q within the
// caller's tenant and legal entities (row-level security): an exact number
// first, then a number prefix, then trigram similarity.
func (s *Store) Search(ctx context.Context, q Query) ([]Hit, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	escaped := likeEscaper.Replace(text)
	var hits []Hit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('pg_trgm.word_similarity_threshold', $1, true)`, wordSimilarityThreshold); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, searchSQL, p.TenantID, q.Types, text, escaped+"%", "%"+escaped+"%", q.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			if err := rows.Scan(&h.Type, &h.ID, &h.LegalEntityID, &h.Number, &h.Title, &h.Subtitle, &h.Status); err != nil {
				return err
			}
			hits = append(hits, h)
		}
		return rows.Err()
	})
	return hits, err
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

const searchSQL = `
with hits as (
  select record_type, record_id::text as record_id, legal_entity_id::text as legal_entity_id, number, title, subtitle, status, search_text
  from search_record
  where tenant_id = $1 and record_type = any($2) and not removed
    and (lower(number) like $4 or search_text like $5 or $3 <% search_text)
  union all
  select record_type, record_id::text, '', number, title, subtitle, status, search_text
  from search_record_tenant
  where tenant_id = $1 and record_type = any($2) and not removed
    and (lower(number) like $4 or search_text like $5 or $3 <% search_text)
)
select record_type, record_id, legal_entity_id, number, title, subtitle, status
from hits
order by (lower(number) = $3) desc, (lower(number) like $4) desc, word_similarity($3, search_text) desc, number, record_id
limit $6`
