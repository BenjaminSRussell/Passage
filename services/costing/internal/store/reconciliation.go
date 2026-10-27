package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CodeReconciliationNotFound: no such reconciliation run.
const (
	CodeReconciliationNotFound = "CST-REC-002"
	reconciliationAggType      = "reconciliation_run"
)

// ReconciliationDiffEventType is dev.passage.costing.reconciliation.diff.v1,
// one per differing group of a reconciliation run (FR-20.4).
var ReconciliationDiffEventType = events.Type("costing", "reconciliation", "diff", 1)

// ReconciliationRow is one group of a run: the inventory value, the ledger
// balance and their difference, as decimal strings.
type ReconciliationRow struct {
	Key       map[string]string `json:"key"`
	Inventory string            `json:"inventory"`
	Ledger    string            `json:"ledger"`
	Diff      string            `json:"diff"`
}

// ReconciliationRun is one stored tie-out of the inventory value report against
// the ledger balances of the inventory accounts (FR-20.4, costing.reconcile).
// Rows are absent from list results.
type ReconciliationRun struct {
	ID                  string              `json:"id"`
	LegalEntityID       string              `json:"legalEntityId"`
	AsOf                string              `json:"asOf"`
	ValuationLedgerID   string              `json:"valuationLedgerId,omitempty"`
	ValuationLedgerCode string              `json:"valuationLedgerCode,omitempty"`
	SiteID              string              `json:"siteId,omitempty"`
	Currency            string              `json:"currency"`
	GroupBy             []string            `json:"groupBy"`
	Differences         int                 `json:"differences"`
	TotalDiff           string              `json:"totalDiff"`
	Ties                bool                `json:"ties"`
	RowCount            int                 `json:"rowCount"`
	Rows                []ReconciliationRow `json:"rows,omitempty"`
	CreatedAt           time.Time           `json:"createdAt"`
}

// ReconciliationPage is one page of runs, newest as-of date first.
type ReconciliationPage struct {
	Items []ReconciliationRun
	Next  string
}

// reconciliationDiff is the payload of dev.passage.costing.reconciliation.diff.v1
// (3.20.5). The group's dimensions stand for the PRD's account and dimension
// until posting profiles resolve accounts; sample vouchers are not sent yet.
type reconciliationDiff struct {
	RunID             string            `json:"runId"`
	LegalEntityID     string            `json:"legalEntityId"`
	ValuationLedgerID string            `json:"valuationLedgerId,omitempty"`
	AsOf              string            `json:"asOf"`
	Currency          string            `json:"currency"`
	Dimensions        map[string]string `json:"dimensions"`
	InventoryValue    string            `json:"inventoryValue"`
	LedgerBalance     string            `json:"ledgerBalance"`
	Diff              string            `json:"diff"`
}

// CreateReconciliationRun stores a run and writes one reconciliation.diff
// event per differing row to the outbox in the same transaction. A repeated
// Idempotency-Key with the same request returns the stored run with
// created=false and writes nothing (even if the inventory moved since); with
// another request it is refused. requestHash identifies the request.
func (s *Postgres) CreateReconciliationRun(ctx context.Context, in ReconciliationRun, idempotencyKey, requestHash string) (ReconciliationRun, bool, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID, in.CreatedAt, in.RowCount = ids.New(), time.Now().UTC(), len(in.Rows)
	body, err := json.Marshal(in)
	if err != nil {
		return ReconciliationRun{}, false, err
	}
	var out ReconciliationRun
	created := true
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `insert into reconciliation_run (tenant_id, id, legal_entity_id, valuation_ledger_id, as_of, ties,
				differences, idempotency_key, request_hash, body, created_at)
			values ($1, $2, $3::uuid, nullif($4, '')::uuid, $5::date, $6, $7, $8, $9, $10::jsonb, $11)
			on conflict (tenant_id, idempotency_key) do nothing
			returning body`,
			p.TenantID, in.ID, in.LegalEntityID, in.ValuationLedgerID, in.AsOf, in.Ties, in.Differences, idempotencyKey, requestHash,
			string(body), in.CreatedAt).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			created = false
			var storedHash string
			if err := tx.QueryRow(ctx, `select body, request_hash from reconciliation_run where tenant_id = $1 and idempotency_key = $2`,
				p.TenantID, idempotencyKey).Scan(&raw, &storedHash); err != nil {
				return err
			}
			if storedHash != requestHash {
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
		for _, row := range in.Rows {
			if isZeroDecimal(row.Diff) {
				continue
			}
			evt, err := events.New(ReconciliationDiffEventType, eventSource, in.ID, p.TenantID, reconciliationDiff{RunID: in.ID,
				LegalEntityID: in.LegalEntityID, ValuationLedgerID: in.ValuationLedgerID, AsOf: in.AsOf, Currency: in.Currency,
				Dimensions: row.Key, InventoryValue: row.Inventory, LedgerBalance: row.Ledger, Diff: row.Diff})
			if err != nil {
				return err
			}
			if err := outbox.Enqueue(ctx, tx, reconciliationAggType, in.LegalEntityID, evt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ReconciliationRun{}, false, err
	}
	return out, created, nil
}

// isZeroDecimal reports whether a decimal string is zero; a malformed one is not.
func isZeroDecimal(s string) bool {
	d, err := decimal.NewFromString(s)
	return err == nil && d.IsZero()
}

// GetReconciliationRun returns one stored run with its rows.
func (s *Postgres) GetReconciliationRun(ctx context.Context, id string) (ReconciliationRun, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReconciliationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `select body from reconciliation_run where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeReconciliationNotFound, "reconciliation run not found")
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	return out, err
}

// ListReconciliationRuns returns run summaries (without rows) in the caller's
// scope, optionally of one legal entity, newest as-of date first. after is the
// Next of the previous page ("asOf|id").
func (s *Postgres) ListReconciliationRuns(ctx context.Context, legalEntityID, after string, limit int) (ReconciliationPage, error) {
	p, _ := tenant.FromContext(ctx)
	afterAsOf, afterID, _ := strings.Cut(after, "|")
	page := ReconciliationPage{Items: []ReconciliationRun{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select body - 'rows' from reconciliation_run
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
			var run ReconciliationRun
			if err := rows.Scan(&raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &run); err != nil {
				return err
			}
			page.Items = append(page.Items, run)
		}
		return rows.Err()
	})
	if err != nil {
		return ReconciliationPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items, page.Next = page.Items[:limit], last.AsOf+"|"+last.ID
	}
	return page, nil
}
