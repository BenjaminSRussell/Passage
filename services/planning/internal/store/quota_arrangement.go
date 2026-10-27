package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const (
	// CodeQuotaOverlap is returned when the validity of an arrangement overlaps
	// another arrangement of the same item and site (data model invariant).
	CodeQuotaOverlap = "PLAN-QUOTA-OVERLAP"
	// CodeQuotaNotFound is returned for an unknown quota arrangement id.
	CodeQuotaNotFound = "PLAN-QUOTA-NOT-FOUND"

	quotaIdempotencyConstraint = "quota_arrangement_idempotency_key"
)

// QuotaArrangement is a stored quota arrangement (FR-22.17). Version is its
// ETag; Spec.Sources carry their ids in the order they were given.
type QuotaArrangement struct {
	ID      string
	Version int
	Spec    domain.QuotaArrangementSpec
}

// QuotaCreate is one create command; IdempotencyKey and InputHash are empty
// when the caller sent no Idempotency-Key.
type QuotaCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.QuotaArrangementSpec
}

// QuotaQuery filters the arrangement list: item, site and a date the
// arrangement must be valid on; empty fields do not filter.
type QuotaQuery struct {
	ItemID  string
	SiteID  string
	ValidOn *time.Time
	Limit   int
}

const quotaSelect = `select id::text, version, item_id::text, site_id::text, valid_from, valid_to, split, min_split_qty::text
	from quota_arrangement`

func quotaNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeQuotaNotFound, "quota arrangement not found")
}

func quotaOverlap() error {
	return httpx.NewProblem(http.StatusConflict, CodeQuotaOverlap,
		"the validity period overlaps another quota arrangement of this item and site")
}

func scanQuota(row pgx.Row) (QuotaArrangement, error) {
	var a QuotaArrangement
	var minSplit string
	if err := row.Scan(&a.ID, &a.Version, &a.Spec.ItemID, &a.Spec.SiteID, &a.Spec.ValidFrom, &a.Spec.ValidTo,
		&a.Spec.Split, &minSplit); err != nil {
		return QuotaArrangement{}, err
	}
	d, err := decimal.NewFromString(minSplit)
	if err != nil {
		return QuotaArrangement{}, fmt.Errorf("quota arrangement %s: min_split_qty: %w", a.ID, err)
	}
	a.Spec.MinSplitQty = d
	return a, nil
}

// ListQuotaArrangements returns the tenant's arrangements, filtered by q,
// ordered by item, site and validity start, each with its sources.
func (s *Postgres) ListQuotaArrangements(ctx context.Context, q QuotaQuery) ([]QuotaArrangement, error) {
	p, _ := tenant.FromContext(ctx)
	out := []QuotaArrangement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, quotaSelect+` where tenant_id = $1 and ($2::uuid is null or item_id = $2)
			and ($3::uuid is null or site_id = $3) and ($4::date is null or $4::date between valid_from and valid_to)
			order by item_id, site_id, valid_from, id limit $5`,
			p.TenantID, optional(q.ItemID), optional(q.SiteID), q.ValidOn, q.Limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			a, err := scanQuota(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return loadQuotaSources(ctx, tx, p.TenantID, out)
	})
	return out, err
}

// GetQuotaArrangement returns one arrangement with its sources; an unknown id is 404.
func (s *Postgres) GetQuotaArrangement(ctx context.Context, id string) (QuotaArrangement, error) {
	p, _ := tenant.FromContext(ctx)
	var a QuotaArrangement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = readQuota(ctx, tx, p.TenantID, `id = $2`, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return QuotaArrangement{}, quotaNotFound()
	}
	return a, err
}

// CreateQuotaArrangement stores a validated arrangement and its sources at
// version 1. A validity period that overlaps another arrangement of the same
// item and site is 409 PLAN-QUOTA-OVERLAP; a transaction advisory lock on the
// item and site serialises concurrent writes so the check holds. With an
// Idempotency-Key, a retry of the same body returns the first result
// (replayed true) and another body under the same key is 422.
func (s *Postgres) CreateQuotaArrangement(ctx context.Context, req QuotaCreate) (QuotaArrangement, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out QuotaArrangement
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	spec := req.Spec
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored string
			err := tx.QueryRow(ctx, `select input_hash from quota_arrangement where tenant_id = $1 and idempotency_key = $2`,
				p.TenantID, *key).Scan(&stored)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				a, err := readQuota(ctx, tx, p.TenantID, `idempotency_key = $2`, *key)
				out, replayed = a, true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		id := ids.New()
		if err := lockQuotaScope(ctx, tx, p.TenantID, spec, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into quota_arrangement (tenant_id, id, item_id, site_id, valid_from, valid_to,
			split, min_split_qty, idempotency_key, input_hash) values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, $10)`,
			p.TenantID, id, spec.ItemID, spec.SiteID, spec.ValidFrom, spec.ValidTo, spec.Split, spec.MinSplitQty.String(),
			key, hash); err != nil {
			return err
		}
		if err := insertQuotaSources(ctx, tx, p.TenantID, id, spec.Sources); err != nil {
			return err
		}
		var err error
		out, err = readQuota(ctx, tx, p.TenantID, `id = $2`, id)
		return err
	})
	if err != nil {
		return QuotaArrangement{}, false, quotaWriteError(err)
	}
	return out, replayed, nil
}

// UpdateQuotaArrangement replaces arrangement id and all its sources under
// If-Match: expectedVersion must be its current version (412 otherwise) and
// the version is bumped. The overlap rule is checked as on create, ignoring
// the arrangement itself. Replaced sources get new ids.
func (s *Postgres) UpdateQuotaArrangement(ctx context.Context, id string, expectedVersion int, spec domain.QuotaArrangementSpec) (QuotaArrangement, error) {
	p, _ := tenant.FromContext(ctx)
	var out QuotaArrangement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `select version from quota_arrangement where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return quotaNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("quota arrangement", version)
		}
		if err := lockQuotaScope(ctx, tx, p.TenantID, spec, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update quota_arrangement set item_id = $3, site_id = $4, valid_from = $5, valid_to = $6,
			split = $7, min_split_qty = $8::numeric, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, spec.ItemID, spec.SiteID, spec.ValidFrom, spec.ValidTo, spec.Split, spec.MinSplitQty.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from quota_source where tenant_id = $1 and arrangement_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := insertQuotaSources(ctx, tx, p.TenantID, id, spec.Sources); err != nil {
			return err
		}
		out, err = readQuota(ctx, tx, p.TenantID, `id = $2`, id)
		return err
	})
	if err != nil {
		return QuotaArrangement{}, quotaWriteError(err)
	}
	return out, nil
}

func quotaWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == quotaIdempotencyConstraint {
		// A concurrent create with the same key committed first.
		return idempotencyReuse()
	}
	return err
}

// lockQuotaScope takes the transaction advisory lock of the item and site and
// refuses a validity period that overlaps another arrangement there (both
// ranges inclusive). self is the arrangement being written, never a clash.
func lockQuotaScope(ctx context.Context, tx pgx.Tx, tenantID string, spec domain.QuotaArrangementSpec, self string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"planning.quota:"+tenantID+":"+spec.ItemID+":"+spec.SiteID); err != nil {
		return err
	}
	var clash bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from quota_arrangement where tenant_id = $1 and item_id = $2
		and site_id = $3 and id <> $4 and valid_from <= $6 and valid_to >= $5)`,
		tenantID, spec.ItemID, spec.SiteID, self, spec.ValidFrom, spec.ValidTo).Scan(&clash); err != nil {
		return err
	}
	if clash {
		return quotaOverlap()
	}
	return nil
}

func insertQuotaSources(ctx context.Context, tx pgx.Tx, tenantID, arrangementID string, sources []domain.QuotaSourceSpec) error {
	for i, src := range sources {
		var maxQty *string
		if src.MaxQtyPerPeriod != nil {
			v := src.MaxQtyPerPeriod.String()
			maxQty = &v
		}
		if _, err := tx.Exec(ctx, `insert into quota_source (tenant_id, id, arrangement_id, line, source_type, vendor_id,
			production_version_id, from_warehouse_id, quota, base_qty, allocated_qty, max_qty_per_period, period_days, priority, blocked)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13, $14, $15)`,
			tenantID, ids.New(), arrangementID, i+1, src.Type, optional(src.VendorID), optional(src.ProductionVersionID),
			optional(src.FromWarehouseID), src.Quota.String(), src.BaseQty.String(), src.Allocated.String(), maxQty,
			src.PeriodDays, src.Priority, src.Blocked); err != nil {
			return err
		}
	}
	return nil
}

// readQuota reads one arrangement matched by where (with $1 the tenant and $2
// its argument) and its sources.
func readQuota(ctx context.Context, tx pgx.Tx, tenantID, where string, arg any) (QuotaArrangement, error) {
	a, err := scanQuota(tx.QueryRow(ctx, quotaSelect+` where tenant_id = $1 and `+where, tenantID, arg))
	if err != nil {
		return QuotaArrangement{}, err
	}
	list := []QuotaArrangement{a}
	if err := loadQuotaSources(ctx, tx, tenantID, list); err != nil {
		return QuotaArrangement{}, err
	}
	return list[0], nil
}

// loadQuotaSources fills the sources of the given arrangements in line order.
func loadQuotaSources(ctx context.Context, tx pgx.Tx, tenantID string, list []QuotaArrangement) error {
	if len(list) == 0 {
		return nil
	}
	index := make(map[string]int, len(list))
	arrangementIDs := make([]string, len(list))
	for i, a := range list {
		index[a.ID] = i
		arrangementIDs[i] = a.ID
		list[i].Spec.Sources = []domain.QuotaSourceSpec{}
	}
	rows, err := tx.Query(ctx, `select arrangement_id::text, id::text, source_type, coalesce(vendor_id::text, ''),
		coalesce(production_version_id::text, ''), coalesce(from_warehouse_id::text, ''), quota::text, base_qty::text,
		allocated_qty::text, max_qty_per_period::text, period_days, priority, blocked from quota_source
		where tenant_id = $1 and arrangement_id = any($2::uuid[]) order by arrangement_id, line`, tenantID, arrangementIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var arrangementID string
		var src domain.QuotaSourceSpec
		var quota, base, allocated string
		var maxQty *string
		if err := rows.Scan(&arrangementID, &src.ID, &src.Type, &src.VendorID, &src.ProductionVersionID,
			&src.FromWarehouseID, &quota, &base, &allocated, &maxQty, &src.PeriodDays, &src.Priority, &src.Blocked); err != nil {
			return err
		}
		ds, err := parseDecimals(quota, base, allocated)
		if err != nil {
			return fmt.Errorf("quota source %s: %w", src.ID, err)
		}
		src.Quota, src.BaseQty, src.Allocated = ds[0], ds[1], ds[2]
		if maxQty != nil {
			d, err := decimal.NewFromString(*maxQty)
			if err != nil {
				return fmt.Errorf("quota source %s: max_qty_per_period: %w", src.ID, err)
			}
			src.MaxQtyPerPeriod = &d
		}
		i := index[arrangementID]
		list[i].Spec.Sources = append(list[i].Spec.Sources, src)
	}
	return rows.Err()
}
