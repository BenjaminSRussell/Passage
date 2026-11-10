package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	TypeParty           = "party"
	TypeProduct         = "product"
	TypePurchaseOrder   = "purchase_order"
	TypeReceipt         = "receipt"
	TypeVendorInvoice   = "vendor_invoice"
	TypeSalesOrder      = "sales_order"
	TypePackingSlip     = "packing_slip"
	TypeCustomerInvoice = "customer_invoice"
	TypeProductionOrder = "production_order"
	TypeWorker          = "worker"

	wordSimilarityThreshold = "0.4"
)

var errNoPrincipal = errors.New("search: no principal in context")

// LegalEntityScoped reports whether records of the type belong to one legal
// entity (every type except the tenant-wide party, product and worker).
func LegalEntityScoped(recordType string) bool {
	return recordType != TypeParty && recordType != TypeProduct && recordType != TypeWorker
}

type Record struct {
	Type          string
	ID            string
	LegalEntityID string
	Number        string
	Title         string
	Subtitle      string
	Status        string
	Removed       bool
	UpdateOnly    bool
	Version       int
	At            time.Time
}

const removedVersion = math.MaxInt32

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

// Apply writes r unless the row already holds a newer event, newer meaning a
// higher source version, then a later event time. Empty text fields keep what
// the row holds, so a status-only event leaves the title alone. A removal
// outranks every version, so it cannot be undone by a late event. UpdateOnly
// changes an existing row and never creates one. It reports whether the row
// changed.
func (s *Store) Apply(ctx context.Context, r Record) (bool, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errNoPrincipal
	}
	if r.Removed {
		r.Version = removedVersion
	}
	table := "search_record_tenant"
	if LegalEntityScoped(r.Type) {
		table = "search_record"
	}
	applied := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		var err error
		if r.UpdateOnly {
			tag, err = tx.Exec(ctx, fmt.Sprintf(`update %s as t set status = coalesce(nullif($4, ''), t.status), removed = $5,
				source_version = $7, source_at = $6, indexed_at = now()
				where tenant_id = $1 and record_type = $2 and record_id = $3 and (t.source_version, t.source_at) <= ($7, $6)`, table),
				p.TenantID, r.Type, r.ID, r.Status, r.Removed, r.At, r.Version)
		} else {
			args := []any{p.TenantID, r.Type, r.ID, r.Number, r.Title, r.Subtitle, r.Status, r.Removed, r.At, r.Version}
			columns, values := "", ""
			if LegalEntityScoped(r.Type) {
				columns, values = "legal_entity_id, ", "$11, "
				args = append(args, r.LegalEntityID)
			}
			tag, err = tx.Exec(ctx, fmt.Sprintf(`insert into %[1]s as t (tenant_id, %[2]srecord_type, record_id, number, title, subtitle, status, removed, source_at, source_version)
				values ($1, %[3]s$2, $3, $4, $5, $6, $7, $8, $9, $10)
				on conflict (tenant_id, record_type, record_id) do update set
				  number = coalesce(nullif(excluded.number, ''), t.number),
				  title = coalesce(nullif(excluded.title, ''), t.title),
				  subtitle = coalesce(nullif(excluded.subtitle, ''), t.subtitle),
				  status = coalesce(nullif(excluded.status, ''), t.status),
				  removed = excluded.removed, source_at = excluded.source_at, source_version = excluded.source_version, indexed_at = now()
				where (t.source_version, t.source_at) <= (excluded.source_version, excluded.source_at)`, table, columns, values), args...)
		}
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
	var hits []Hit
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('pg_trgm.word_similarity_threshold', $1, true)`, wordSimilarityThreshold); err != nil {
			return err
		}
		sql, arg := plan(text)
		rows, err := tx.Query(ctx, sql, p.TenantID, q.Types, text, arg, q.Limit)
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

const (
	minFuzzyRunes = 3
	prefixCeiling = "\U0010FFFF"
)

// plan picks the statement and its fourth argument. A short or number-shaped
// query is a number prefix: a range on the C-collated number_key, which the
// btree index serves even under row-level security (its operators are
// leakproof). Anything else is a name and also matches by trigram similarity;
// pg_trgm's operators are not leakproof, so that arm is filtered, not indexed.
func plan(text string) (sql string, arg string) {
	// A digit and no space looks like a number, unless it holds a word ("7-eleven"): that is a name and matches as one.
	numberShaped := strings.ContainsFunc(text, unicode.IsDigit) && !strings.ContainsFunc(text, unicode.IsSpace) && !hasWord(text)
	if utf8.RuneCountInString(text) < minFuzzyRunes || numberShaped {
		return prefixSQL, text + prefixCeiling
	}
	return fuzzySQL, "%" + likeEscaper.Replace(text) + "%"
}

// maxNumberLetters is the longest run of letters a number carries (a prefix such as "INV" or "PO"); a longer run is a word.
const maxNumberLetters = 3

func hasWord(text string) bool {
	run := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			if run++; run > maxNumberLetters {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

const (
	prefixWhere = `number_key collate "C" >= $3 and number_key collate "C" < $4`
	fuzzyWhere  = `(search_text like $4 or $3 <% search_text)`

	searchTemplate = `
with hits as (
  select record_type, record_id::text as record_id, legal_entity_id::text as legal_entity_id, number, title, subtitle, status, search_text, number_key
  from search_record
  where tenant_id = $1 and record_type = any($2) and not removed and %[1]s
  union all
  select record_type, record_id::text, '', number, title, subtitle, status, search_text, number_key
  from search_record_tenant
  where tenant_id = $1 and record_type = any($2) and not removed and %[1]s
)
select record_type, record_id, legal_entity_id, number, title, subtitle, status
from hits
order by (number_key = $3) desc, starts_with(number_key, $3) desc, word_similarity($3, search_text) desc, number, record_id
limit $5`
)

var (
	prefixSQL = fmt.Sprintf(searchTemplate, prefixWhere)
	fuzzySQL  = fmt.Sprintf(searchTemplate, fuzzyWhere)
)
