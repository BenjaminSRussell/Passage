package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
)

const voyageColumns = `id, reference, currency, status, legs, containers, folios, cost_lines, receipt_lines,
	cost_allocations, allocations, version, created_at, posted_at, request_hash, coalesce(legal_entity_id::text, '')`

// VoyageFilter narrows the voyage list. Empty fields match everything.
type VoyageFilter struct {
	Status string
}

// VoyageSummary is one row of the voyage list: the header without the documents.
type VoyageSummary struct {
	ID           string          `json:"id"`
	Reference    string          `json:"reference"`
	Currency     string          `json:"currency"`
	Status       string          `json:"status"`
	CostTotal    decimal.Decimal `json:"costTotal"`
	Containers   int             `json:"containers"`
	ReceiptLines int             `json:"receiptLines"`
	CreatedAt    time.Time       `json:"createdAt"`
	PostedAt     *time.Time      `json:"postedAt,omitempty"`
}

// CreateVoyage stores a new ESTIMATED voyage (FR-34.12). A repeat with the same
// Idempotency-Key and body returns the first voyage; another body is refused.
func (s *Postgres) CreateVoyage(ctx context.Context, in domain.Voyage, idem Idem) (domain.Voyage, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Voyage
	entity, err := httpx.OwnerLegalEntity(ctx, in.LegalEntityID)
	if err != nil {
		return out, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			v, hash, err := scanVoyage(tx.QueryRow(ctx, `select `+voyageColumns+` from voyage where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if hash != idem.Fingerprint {
				return false, reuse()
			}
			out = v
			return true, nil
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		docs, err := marshalDocs(in.Legs, in.Containers, in.Folios, in.CostLines, in.ReceiptLines)
		if err != nil {
			return err
		}
		out, _, err = scanVoyage(tx.QueryRow(ctx, `insert into voyage (tenant_id, id, reference, currency, status, legs, containers, folios,
			cost_lines, receipt_lines, cost_total, idempotency_key, request_hash, legal_entity_id)
			values ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb, $11::numeric, $12, $13, $14::uuid)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+voyageColumns,
			p.TenantID, ids.New(), in.Reference, in.Currency, domain.VoyageEstimated, docs[0], docs[1], docs[2], docs[3], docs[4],
			in.CostTotal().String(), nullable(idem.Key), nullable(idem.Fingerprint), entity))
		if errors.Is(err, pgx.ErrNoRows) {
			if done, err := replay(); done || err != nil {
				return err
			}
			return reuse()
		}
		return err
	})
	return out, err
}

// GetVoyage returns one voyage with its documents and, once posted, its allocations.
func (s *Postgres) GetVoyage(ctx context.Context, id string) (domain.Voyage, error) {
	p, _ := tenant.FromContext(ctx)
	var v domain.Voyage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v, _, err = scanVoyage(tx.QueryRow(ctx, `select `+voyageColumns+` from voyage where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Voyage{}, NotFound("landed-cost voyage")
	}
	return v, err
}

// ListVoyages returns the newest voyages first, without their documents.
func (s *Postgres) ListVoyages(ctx context.Context, f VoyageFilter) ([]VoyageSummary, error) {
	p, _ := tenant.FromContext(ctx)
	out := []VoyageSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, reference, currency, status, cost_total::text, jsonb_array_length(containers),
			jsonb_array_length(receipt_lines), created_at, posted_at
			from voyage where tenant_id = $1 and ($2::text = '' or status = $2)
			order by created_at desc, id limit $3`, p.TenantID, f.Status, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v VoyageSummary
			var total string
			if err := rows.Scan(&v.ID, &v.Reference, &v.Currency, &v.Status, &total, &v.Containers, &v.ReceiptLines, &v.CreatedAt, &v.PostedAt); err != nil {
				return err
			}
			v.CostTotal = decimal.RequireFromString(total)
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// AllocateVoyage allocates the voyage's cost lines to its receipt lines and posts
// the result under a row lock (FR-34.9, BR-34.6). The landedcost.posted event is
// written in the same transaction. Allocating a POSTED voyage returns it
// unchanged and writes nothing, so a retry is safe.
func (s *Postgres) AllocateVoyage(ctx context.Context, id string, now time.Time) (domain.Voyage, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Voyage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		v, _, err := scanVoyage(tx.QueryRow(ctx, `select `+voyageColumns+` from voyage where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("landed-cost voyage")
		}
		if err != nil {
			return err
		}
		if v.Status == domain.VoyagePosted {
			out = v
			return nil
		}
		posted, derr := v.Allocate()
		if derr != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, derr.Code, derr.Detail)
		}
		allocated := decimal.Zero
		for _, a := range posted.Allocations {
			allocated = allocated.Add(a.Amount)
		}
		if !allocated.Equal(v.CostTotal()) {
			// Allocate guarantees this; refuse to post rather than store drift.
			return fmt.Errorf("voyage %s: allocated %s, cost total %s", id, allocated, v.CostTotal())
		}
		docs, err := marshalDocs(posted.CostAllocations, posted.Allocations)
		if err != nil {
			return err
		}
		out, _, err = scanVoyage(tx.QueryRow(ctx, `update voyage set status = $3, cost_allocations = $4::jsonb, allocations = $5::jsonb,
			posted_at = $6, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 returning `+voyageColumns,
			p.TenantID, id, posted.Status, docs[0], docs[1], now))
		if err != nil {
			return err
		}
		data := tmsevents.LandedCostPostedPayload{VoyageID: id, Reference: out.Reference, Currency: out.Currency,
			Total: allocated.StringFixed(2), ReceiptLines: make([]tmsevents.ReceiptAllocation, 0, len(out.Allocations))}
		for _, a := range out.Allocations {
			data.ReceiptLines = append(data.ReceiptLines, tmsevents.ReceiptAllocation{Line: a.Line, Amount: a.Amount.StringFixed(2)})
		}
		evt, err := events.New(tmsevents.LandedCostPosted, eventSource, id, p.TenantID, data)
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "landedcost", id, evt)
	})
	return out, err
}

// marshalDocs encodes each value as JSON, writing nil slices as [].
func marshalDocs(values ...any) ([]string, error) {
	out := make([]string, len(values))
	for i, v := range values {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if string(b) == "null" {
			b = []byte("[]")
		}
		out[i] = string(b)
	}
	return out, nil
}

func scanVoyage(row pgx.Row) (domain.Voyage, string, error) {
	var v domain.Voyage
	var legs, containers, folios, costs, receipts, costAllocs, allocs []byte
	var hash *string
	if err := row.Scan(&v.ID, &v.Reference, &v.Currency, &v.Status, &legs, &containers, &folios, &costs, &receipts,
		&costAllocs, &allocs, &v.Version, &v.CreatedAt, &v.PostedAt, &hash, &v.LegalEntityID); err != nil {
		return domain.Voyage{}, "", err
	}
	for _, doc := range []struct {
		raw []byte
		dst any
	}{{legs, &v.Legs}, {containers, &v.Containers}, {folios, &v.Folios}, {costs, &v.CostLines}, {receipts, &v.ReceiptLines},
		{costAllocs, &v.CostAllocations}, {allocs, &v.Allocations}} {
		if err := json.Unmarshal(doc.raw, doc.dst); err != nil {
			return domain.Voyage{}, "", err
		}
	}
	v.CreatedAt = v.CreatedAt.UTC()
	if v.PostedAt != nil {
		t := v.PostedAt.UTC()
		v.PostedAt = &t
	}
	return v, deref(hash), nil
}
