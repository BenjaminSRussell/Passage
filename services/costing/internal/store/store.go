// Package store persists costing versions and item costs in the costing schema.
// Every query runs inside dbx.WithTenantTx, so row-level security applies.
package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeVersionCodeDuplicate = "CST-VER-CODE-DUPLICATE"
	CodeVersionNotFound      = "CST-VER-NOT-FOUND"
	CodeVersionNotDraft      = "CST-VER-NOT-DRAFT"
	CodeIdempotencyReuse     = "idempotency_key_reuse"
	uniqueViolation          = "23505"
)

// CostingVersion is a set of planned or standard costs (AX CostingVersion).
type CostingVersion struct {
	ID              string     `json:"id"`
	LegalEntityID   string     `json:"legalEntityId"`
	Code            string     `json:"code"`
	Type            string     `json:"type"`
	Status          string     `json:"status"`
	Currency        string     `json:"currency"`
	EffectiveFrom   string     `json:"effectiveFrom"`
	BlockActivation bool       `json:"blockActivation"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
	ActivatedAt     *time.Time `json:"activatedAt,omitempty"`
	// ItemCount is the number of item costs in the version; list and get fill it.
	ItemCount int `json:"itemCount"`
}

// ItemCost is one product x site cost in a version, unit cost as a decimal string.
type ItemCost struct {
	ProductID string `json:"productId"`
	SiteID    string `json:"siteId"`
	UnitCost  string `json:"unitCost"`
}

// Store is the persistence contract the API depends on.
type Store interface {
	// CreateCostingVersion reports created=false for an idempotent replay.
	CreateCostingVersion(ctx context.Context, in CostingVersion, idempotencyKey string) (v CostingVersion, created bool, err error)
	ImportItemCosts(ctx context.Context, versionID string, items []ItemCost) (int, error)
	ListVersions(ctx context.Context, f VersionFilter, after string, limit int) (VersionPage, error)
	GetVersion(ctx context.Context, id string) (CostingVersion, error)
	ListItemCosts(ctx context.Context, versionID, after string, limit int) (ItemCostPage, error)
	CopyItemCosts(ctx context.Context, targetID, sourceID string, percent decimal.Decimal) (int, error)
	PreviewActivation(ctx context.Context, versionID string, onHand OnHand) (ActivationResult, error)
	// ActivateVersion commits the activation when the version is still at
	// expectedVersion (If-Match); an ACTIVE version replays its stored result.
	ActivateVersion(ctx context.Context, versionID string, expectedVersion int, onHand OnHand) (ActivationResult, error)
	CurrentCost(ctx context.Context, productID, siteID, asOf string) (CostLookup, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pool.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a Postgres store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const versionColumns = "id, legal_entity_id, code, type, status, currency, effective_from::text, block_activation, version, created_at, activated_at"

func scanVersion(row pgx.Row, extra ...any) (CostingVersion, error) {
	var v CostingVersion
	err := row.Scan(append([]any{&v.ID, &v.LegalEntityID, &v.Code, &v.Type, &v.Status, &v.Currency, &v.EffectiveFrom,
		&v.BlockActivation, &v.Version, &v.CreatedAt, &v.ActivatedAt}, extra...)...)
	return v, err
}

// CreateCostingVersion inserts a DRAFT version. A repeated Idempotency-Key
// returns the stored version; the same key with a different body is refused.
func (s *Postgres) CreateCostingVersion(ctx context.Context, in CostingVersion, idempotencyKey string) (CostingVersion, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out CostingVersion
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		if err == nil {
			out = replay
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanVersion(tx.QueryRow(ctx, `insert into costing_version (tenant_id, id, legal_entity_id, code, type, currency, effective_from, block_activation, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9)
			on conflict (tenant_id, idempotency_key) do update set version = costing_version.version
			returning `+versionColumns+`, (xmax = 0)`,
			p.TenantID, ids.New(), in.LegalEntityID, in.Code, in.Type, in.Currency, in.EffectiveFrom, in.BlockActivation, idempotencyKey), &inserted)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return CostingVersion{}, false, httpx.NewProblem(http.StatusConflict, CodeVersionCodeDuplicate, "a costing version with this code already exists for the legal entity")
	}
	if err != nil {
		return CostingVersion{}, false, err
	}
	if !inserted && (out.Code != in.Code || out.Type != in.Type || out.Currency != in.Currency || out.EffectiveFrom != in.EffectiveFrom ||
		out.BlockActivation != in.BlockActivation || out.LegalEntityID != in.LegalEntityID) {
		return CostingVersion{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// ImportItemCosts writes pending item costs for a DRAFT version in one
// statement. Re-importing a product x site replaces its pending cost, so an
// import is idempotent. Every import bumps the version's optimistic version.
func (s *Postgres) ImportItemCosts(ctx context.Context, versionID string, items []ItemCost) (int, error) {
	p, _ := tenant.FromContext(ctx)
	rowIDs := make([]string, len(items))
	products := make([]string, len(items))
	sites := make([]string, len(items))
	costs := make([]string, len(items))
	for i, it := range items {
		rowIDs[i], products[i], sites[i], costs[i] = ids.New(), it.ProductID, it.SiteID, it.UnitCost
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `select status from costing_version where tenant_id = $1 and id = $2 for update`, p.TenantID, versionID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "costing version not found")
		}
		if err != nil {
			return err
		}
		if status != "DRAFT" {
			return httpx.NewProblem(http.StatusConflict, CodeVersionNotDraft, "item costs can only be imported into a DRAFT version")
		}
		if _, err := tx.Exec(ctx, `insert into item_cost (tenant_id, id, legal_entity_id, version_id, product_id, site_id, unit_cost, effective_from)
			select v.tenant_id, i.id, v.legal_entity_id, v.id, i.product_id, i.site_id, i.unit_cost, v.effective_from
			from costing_version v
			cross join unnest($3::uuid[], $4::uuid[], $5::uuid[], $6::numeric[]) as i(id, product_id, site_id, unit_cost)
			where v.tenant_id = $1 and v.id = $2
			on conflict (tenant_id, version_id, product_id, site_id) do update set unit_cost = excluded.unit_cost, source = 'IMPORT', updated_at = now()`,
			p.TenantID, versionID, rowIDs, products, sites, costs); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update costing_version set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, versionID)
		return err
	})
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

var _ Store = (*Postgres)(nil)
