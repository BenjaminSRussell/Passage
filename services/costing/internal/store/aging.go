package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Aging problem codes (FR-20.17). CST-AGE-001 is the PRD's opening-balance flag.
const (
	CodeBucketSetDuplicate = "CST-AGE-002"
	CodeBucketSetNotFound  = "CST-AGE-003"
	CodeSnapshotNotFound   = "CST-AGE-004"
	snapshotAggType        = "aging_snapshot"
	bucketSetColumns       = "id::text, legal_entity_id::text, name, bounds, created_at"
)

// AgingSnapshotEventType is the stored-snapshot event. The PRD's
// dev.passage.costing.aging.snapshot.created.v1 has one segment too many for
// the kernel type pattern (<context>.<entity>.<verb>), so the entity is the
// single token "agingsnapshot".
var AgingSnapshotEventType = events.Type("costing", "agingsnapshot", "created", 1)

// BucketSet is a saved aging bucket set of a legal entity (FR-20.17, AX
// "Inventory aging report setup"). Bounds are ascending upper bounds in days;
// the last bucket is open-ended.
type BucketSet struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	Name          string    `json:"name"`
	Bounds        []int     `json:"bounds"`
	CreatedAt     time.Time `json:"createdAt"`
}

// SnapshotBucket is one bucket of a stored aging snapshot; quantities and
// values are decimal strings.
type SnapshotBucket struct {
	Label string `json:"label"`
	Qty   string `json:"qty"`
	Value string `json:"value"`
}

// SnapshotTypeOut is the aging of one valuation type of a split product.
type SnapshotTypeOut struct {
	ValuationType  string           `json:"valuationType"`
	Buckets        []SnapshotBucket `json:"buckets"`
	TotalQty       string           `json:"totalQty"`
	TotalValue     string           `json:"totalValue"`
	RemainingValue string           `json:"remainingValue"`
}

// SnapshotLineOut is the aging of one product x site line of a snapshot.
type SnapshotLineOut struct {
	ProductID       string            `json:"productId"`
	SiteID          string            `json:"siteId"`
	Buckets         []SnapshotBucket  `json:"buckets"`
	TotalQty        string            `json:"totalQty"`
	TotalValue      string            `json:"totalValue"`
	RemainingValue  string            `json:"remainingValue"`
	TiesToRemaining bool              `json:"tiesToRemaining"`
	ValuationTypes  []SnapshotTypeOut `json:"valuationTypes,omitempty"`
}

// AgingSnapshot is the stored aging of a legal entity as of a date: month-end
// evidence (3.20.15) that is written once and never edited. A list returns it
// without Lines; LineCount still says how many it holds.
type AgingSnapshot struct {
	ID                string            `json:"id"`
	LegalEntityID     string            `json:"legalEntityId"`
	ValuationLedgerID string            `json:"valuationLedgerId,omitempty"`
	AsOf              string            `json:"asOf"`
	Method            string            `json:"method"`
	Currency          string            `json:"currency"`
	BucketSetID       string            `json:"bucketSetId,omitempty"`
	Buckets           []SnapshotBucket  `json:"buckets"`
	TotalQty          string            `json:"totalQty"`
	TotalValue        string            `json:"totalValue"`
	RemainingValue    string            `json:"remainingValue"`
	TiesToRemaining   bool              `json:"tiesToRemaining"`
	LineCount         int               `json:"lineCount"`
	Lines             []SnapshotLineOut `json:"lines,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
}

// SnapshotPage is one page of snapshot summaries; Next is "<asOf>|<id>".
type SnapshotPage struct {
	Items []AgingSnapshot
	Next  string
}

// agingSnapshotCreated is the event payload (3.20.5: snapshot id, entity,
// as-of, valuation ledger, totals per bucket).
type agingSnapshotCreated struct {
	SnapshotID        string           `json:"snapshotId"`
	LegalEntityID     string           `json:"legalEntityId"`
	ValuationLedgerID string           `json:"valuationLedgerId,omitempty"`
	AsOf              string           `json:"asOf"`
	Method            string           `json:"method"`
	Currency          string           `json:"currency"`
	Buckets           []SnapshotBucket `json:"buckets"`
	TotalQty          string           `json:"totalQty"`
	TotalValue        string           `json:"totalValue"`
}

func scanBucketSet(row pgx.Row) (BucketSet, error) {
	var b BucketSet
	var raw []byte
	if err := row.Scan(&b.ID, &b.LegalEntityID, &b.Name, &raw, &b.CreatedAt); err != nil {
		return BucketSet{}, err
	}
	err := json.Unmarshal(raw, &b.Bounds)
	return b, err
}

// CreateBucketSet saves a bucket set; a name already used in the legal entity is refused.
func (s *Postgres) CreateBucketSet(ctx context.Context, in BucketSet) (BucketSet, error) {
	p, _ := tenant.FromContext(ctx)
	bounds, err := json.Marshal(in.Bounds)
	if err != nil {
		return BucketSet{}, err
	}
	var out BucketSet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanBucketSet(tx.QueryRow(ctx, `insert into aging_bucket_set (tenant_id, id, legal_entity_id, name, bounds)
			values ($1, $2, $3, $4, $5::jsonb)
			returning `+bucketSetColumns, p.TenantID, ids.New(), in.LegalEntityID, in.Name, string(bounds)))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return BucketSet{}, httpx.NewProblem(http.StatusConflict, CodeBucketSetDuplicate, "an aging bucket set with this name already exists in the legal entity")
	}
	if err != nil {
		return BucketSet{}, err
	}
	return out, nil
}

// ListBucketSets returns the saved bucket sets in the caller's scope,
// optionally of one legal entity, by name.
func (s *Postgres) ListBucketSets(ctx context.Context, legalEntityID string) ([]BucketSet, error) {
	p, _ := tenant.FromContext(ctx)
	out := []BucketSet{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+bucketSetColumns+` from aging_bucket_set
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			order by name, id`, p.TenantID, legalEntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBucketSet(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// GetBucketSet returns one saved bucket set.
func (s *Postgres) GetBucketSet(ctx context.Context, id string) (BucketSet, error) {
	p, _ := tenant.FromContext(ctx)
	var out BucketSet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanBucketSet(tx.QueryRow(ctx, `select `+bucketSetColumns+` from aging_bucket_set where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeBucketSetNotFound, "aging bucket set not found")
		}
		return err
	})
	return out, err
}

// SnapshotHash is the SHA-256 of what a snapshot computed, without its id and
// creation time: the same input always gives the same hash.
func SnapshotHash(in AgingSnapshot) (string, error) {
	in.ID, in.CreatedAt, in.LineCount = "", time.Time{}, 0
	raw, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// CreateAgingSnapshot stores a snapshot and writes AgingSnapshotEventType to
// the outbox in the same transaction. A repeated Idempotency-Key returns the
// stored snapshot with created=false and writes nothing; the same key with a
// snapshot that computed something else is refused.
func (s *Postgres) CreateAgingSnapshot(ctx context.Context, in AgingSnapshot, idempotencyKey string) (AgingSnapshot, bool, error) {
	p, _ := tenant.FromContext(ctx)
	hash, err := SnapshotHash(in)
	if err != nil {
		return AgingSnapshot{}, false, err
	}
	in.ID, in.CreatedAt, in.LineCount = ids.New(), time.Now().UTC(), len(in.Lines)
	body, err := json.Marshal(in)
	if err != nil {
		return AgingSnapshot{}, false, err
	}
	var out AgingSnapshot
	created := true
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `insert into aging_snapshot (tenant_id, id, legal_entity_id, valuation_ledger_id, as_of, method, currency,
				total_value, idempotency_key, body_hash, body, created_at)
			values ($1, $2, $3, nullif($4, '')::uuid, $5::date, $6, $7, $8::numeric, $9, $10, $11::jsonb, $12)
			on conflict (tenant_id, idempotency_key) do nothing
			returning body`,
			p.TenantID, in.ID, in.LegalEntityID, in.ValuationLedgerID, in.AsOf, in.Method, in.Currency, in.TotalValue,
			idempotencyKey, hash, string(body), in.CreatedAt).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			created = false
			var storedHash string
			if err := tx.QueryRow(ctx, `select body, body_hash from aging_snapshot where tenant_id = $1 and idempotency_key = $2`,
				p.TenantID, idempotencyKey).Scan(&raw, &storedHash); err != nil {
				return err
			}
			if storedHash != hash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return json.Unmarshal(raw, &out)
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		evt, err := events.New(AgingSnapshotEventType, eventSource, out.ID, p.TenantID, agingSnapshotCreated{SnapshotID: out.ID,
			LegalEntityID: out.LegalEntityID, ValuationLedgerID: out.ValuationLedgerID, AsOf: out.AsOf, Method: out.Method,
			Currency: out.Currency, Buckets: out.Buckets, TotalQty: out.TotalQty, TotalValue: out.TotalValue})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, snapshotAggType, out.LegalEntityID, evt)
	})
	if err != nil {
		return AgingSnapshot{}, false, err
	}
	return out, created, nil
}

// GetAgingSnapshot returns one stored snapshot with its lines.
func (s *Postgres) GetAgingSnapshot(ctx context.Context, id string) (AgingSnapshot, error) {
	p, _ := tenant.FromContext(ctx)
	var out AgingSnapshot
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `select body from aging_snapshot where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeSnapshotNotFound, "aging snapshot not found")
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	return out, err
}

// ListAgingSnapshots returns snapshot summaries (without lines) in the
// caller's scope, optionally of one legal entity, newest as-of date first.
// after is the Next of the previous page.
func (s *Postgres) ListAgingSnapshots(ctx context.Context, legalEntityID, after string, limit int) (SnapshotPage, error) {
	p, _ := tenant.FromContext(ctx)
	afterAsOf, afterID, _ := strings.Cut(after, "|")
	page := SnapshotPage{Items: []AgingSnapshot{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select body - 'lines' from aging_snapshot
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			  and (not $3::boolean or (as_of, id) < ($4::date, $5::uuid))
			order by as_of desc, id desc
			limit $6`, p.TenantID, legalEntityID, after != "", nullDate(afterAsOf), nullUUID(afterID), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var snap AgingSnapshot
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &snap); err != nil {
				return err
			}
			page.Items = append(page.Items, snap)
		}
		return rows.Err()
	})
	if err != nil {
		return SnapshotPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items, page.Next = page.Items[:limit], last.AsOf+"|"+last.ID
	}
	return page, nil
}

func nullDate(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
