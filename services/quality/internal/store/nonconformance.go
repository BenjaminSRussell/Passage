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
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	qevents "github.com/BenjaminSRussell/passage/services/quality/internal/events"
)

// NCRFilter narrows the nonconformance list (section 6.33.14, NCR and CAPA status).
type NCRFilter struct {
	Status         string
	Source         string
	Severity       string
	QualityOrderID string
	Limit          int
}

const ncrColumns = `id, source, item_id, dims, qty::text, uom, problem_type, severity, description,
	coalesce(quality_order_id::text, ''), coalesce(vendor_id::text, ''), coalesce(customer_id::text, ''),
	coalesce(production_order_id::text, ''), coalesce(quarantine_order_id::text, ''),
	status, status_reason, created_by, created_at, updated_at, version`

func scanNCR(row pgx.Row) (domain.Nonconformance, error) {
	var n domain.Nonconformance
	var dims []byte
	var qty string
	if err := row.Scan(&n.ID, &n.Source, &n.ItemID, &dims, &qty, &n.UOM, &n.ProblemType, &n.Severity, &n.Description,
		&n.QualityOrderID, &n.VendorID, &n.CustomerID, &n.ProductionOrderID, &n.QuarantineOrderID,
		&n.Status, &n.StatusReason, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt, &n.Version); err != nil {
		return n, err
	}
	if err := json.Unmarshal(dims, &n.Dims); err != nil {
		return n, err
	}
	n.Qty = decimal.RequireFromString(qty)
	return n, nil
}

const correctionColumns = `id, status, current_step, steps, started_by, started_at, effectiveness_days, effectiveness_due_at,
	coalesce(effectiveness_result, ''), effectiveness_note, effectiveness_by, effectiveness_at, reopened, closed_at`

func scanCorrection(row pgx.Row) (domain.Correction, error) {
	var c domain.Correction
	var steps []byte
	if err := row.Scan(&c.ID, &c.Status, &c.CurrentStep, &steps, &c.StartedBy, &c.StartedAt, &c.EffectivenessDays,
		&c.EffectivenessDueAt, &c.EffectivenessResult, &c.EffectivenessNote, &c.EffectivenessBy, &c.EffectivenessAt,
		&c.Reopened, &c.ClosedAt); err != nil {
		return c, err
	}
	return c, json.Unmarshal(steps, &c.Steps)
}

// ncrTx reads one NCR with its correction; lock takes a row lock on the NCR.
func ncrTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.Nonconformance, error) {
	q := `select ` + ncrColumns + ` from quality_nonconformance where ` + ownTenant + ` and id = $1`
	if lock {
		q += ` for update`
	}
	n, err := scanNCR(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, notFound(domain.CodeNCRMissing, "nonconformance not found")
	}
	if err != nil {
		return n, err
	}
	c, err := scanCorrection(tx.QueryRow(ctx, `select `+correctionColumns+` from quality_correction where `+ownTenant+` and nonconformance_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	n.Correction = &c
	return n, nil
}

// insertNCR writes an NCR and its nonconformance.created event in tx. The usage
// decision calls it with an empty key; the create command passes its key.
func insertNCR(ctx context.Context, tx pgx.Tx, tenantID string, n domain.Nonconformance, key string) (domain.Nonconformance, error) {
	if n.ID == "" {
		n.ID = ids.New()
	}
	if n.Dims == nil {
		n.Dims = map[string]string{}
	}
	dims, err := json.Marshal(n.Dims)
	if err != nil {
		return n, err
	}
	out, err := scanNCR(tx.QueryRow(ctx, `insert into quality_nonconformance (tenant_id, id, source, item_id, dims, qty, uom, problem_type,
		severity, description, quality_order_id, vendor_id, customer_id, production_order_id, quarantine_order_id, status, created_by,
		created_at, updated_at, version, idempotency_key)
		values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18, 1, $19)
		returning `+ncrColumns,
		tenantID, n.ID, n.Source, n.ItemID, string(dims), n.Qty.String(), n.UOM, n.ProblemType, n.Severity, n.Description,
		nullable(n.QualityOrderID), nullable(n.VendorID), nullable(n.CustomerID), nullable(n.ProductionOrderID), nullable(n.QuarantineOrderID),
		domain.NCRNew, n.CreatedBy, n.CreatedAt, keyArg(key)))
	if err != nil {
		return out, err
	}
	return out, enqueue(ctx, tx, "nonconformance", out.ID, qevents.NonconformanceCreated, tenantID, qevents.NonconformanceCreatedData{
		NCRID: out.ID, Source: out.Source, Severity: out.Severity, ProblemType: out.ProblemType, ItemID: out.ItemID,
		VendorID: out.VendorID, CustomerID: out.CustomerID, Qty: out.Qty.String(), UOM: out.UOM, QualityOrderID: out.QualityOrderID,
	})
}

// CreateNonconformance stores an NCR in NEW and writes nonconformance.created in
// the same transaction. A replayed key returns the stored NCR with inserted false.
func (s *Postgres) CreateNonconformance(ctx context.Context, in domain.Nonconformance, key string) (domain.Nonconformance, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Nonconformance
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			var id string
			err := tx.QueryRow(ctx, `select id from quality_nonconformance where `+ownTenant+` and idempotency_key = $1`, key).Scan(&id)
			if err == nil {
				out, err = ncrTx(ctx, tx, id, false)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		n, err := insertNCR(ctx, tx, p.TenantID, in, key)
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeNCRRace, "a nonconformance with this Idempotency-Key is being created; retry")
		}
		if pgCode(err) == fkViolation {
			return notFound(domain.CodeOrderMissing, "quality order not found")
		}
		if err != nil {
			return err
		}
		out, inserted = n, true
		return nil
	})
	return out, inserted, err
}

// GetNonconformance reads one NCR with its correction.
func (s *Postgres) GetNonconformance(ctx context.Context, id string) (domain.Nonconformance, error) {
	var out domain.Nonconformance
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = ncrTx(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListNonconformances lists NCRs newest first; the correction is not loaded.
func (s *Postgres) ListNonconformances(ctx context.Context, f NCRFilter) ([]domain.Nonconformance, error) {
	out := []domain.Nonconformance{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+ncrColumns+` from quality_nonconformance where `+ownTenant+`
			and ($1 = '' or status = $1) and ($2 = '' or source = $2) and ($3 = '' or severity = $3)
			and ($4 = '' or quality_order_id = nullif($4, '')::uuid)
			order by created_at desc, id limit $5`, f.Status, f.Source, f.Severity, f.QualityOrderID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanNCR(rows)
			if err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateNonconformance applies change to the NCR at expectedVersion under a row
// lock, then writes its status and correction and moves the version. key, when
// set, is stored on a correction the change creates: a replay of that key returns
// the NCR as it is now, with applied false. Closing the correction writes
// capa.closed in the same transaction.
func (s *Postgres) UpdateNonconformance(ctx context.Context, id string, expectedVersion int, key string,
	change func(domain.Nonconformance) (domain.Nonconformance, error)) (domain.Nonconformance, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Nonconformance
	var applied bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			var ncrID string
			err := tx.QueryRow(ctx, `select nonconformance_id from quality_correction where `+ownTenant+` and idempotency_key = $1`, key).Scan(&ncrID)
			if err == nil {
				if ncrID != id {
					return httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency-Key was already used on another nonconformance")
				}
				out, err = ncrTx(ctx, tx, id, false)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		cur, err := ncrTx(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Version != expectedVersion {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("current version is %d, not %d", cur.Version, expectedVersion))
		}
		next, err := change(cur)
		if err != nil {
			return err
		}
		updated, err := scanNCR(tx.QueryRow(ctx, `update quality_nonconformance set status = $3, status_reason = $4, updated_at = $5,
			version = version + 1 where `+ownTenant+` and id = $1 and version = $2 returning `+ncrColumns,
			id, expectedVersion, next.Status, next.StatusReason, next.UpdatedAt))
		if err != nil {
			return err
		}
		if c := next.Correction; c != nil {
			saved, err := saveCorrection(ctx, tx, p.TenantID, id, *c, cur.Correction == nil, key)
			if err != nil {
				return err
			}
			updated.Correction = &saved
			wasClosed := cur.Correction != nil && cur.Correction.Status == domain.CAPAClosed
			if saved.Status == domain.CAPAClosed && !wasClosed {
				closedAt := ""
				if saved.ClosedAt != nil {
					closedAt = saved.ClosedAt.UTC().Format(time.RFC3339)
				}
				if err := enqueue(ctx, tx, "nonconformance", id, qevents.CAPAClosed, p.TenantID, qevents.CAPAClosedData{
					NCRID: id, CorrectionID: saved.ID, EffectivenessResult: saved.EffectivenessResult, Reopened: saved.Reopened, ClosedAt: closedAt,
				}); err != nil {
					return err
				}
			}
		}
		out, applied = updated, true
		return nil
	})
	return out, applied, err
}

// saveCorrection inserts a new correction or updates the existing one.
func saveCorrection(ctx context.Context, tx pgx.Tx, tenantID, ncrID string, c domain.Correction, create bool, key string) (domain.Correction, error) {
	steps, err := json.Marshal(c.Steps)
	if err != nil {
		return c, err
	}
	var result any
	if c.EffectivenessResult != "" {
		result = c.EffectivenessResult
	}
	if create {
		if c.ID == "" {
			c.ID = ids.New()
		}
		saved, err := scanCorrection(tx.QueryRow(ctx, `insert into quality_correction (tenant_id, id, nonconformance_id, status, current_step,
			steps, started_by, started_at, effectiveness_days, effectiveness_due_at, effectiveness_result, effectiveness_note,
			effectiveness_by, effectiveness_at, reopened, closed_at, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) returning `+correctionColumns,
			tenantID, c.ID, ncrID, c.Status, c.CurrentStep, string(steps), c.StartedBy, c.StartedAt, c.EffectivenessDays,
			c.EffectivenessDueAt, result, c.EffectivenessNote, c.EffectivenessBy, c.EffectivenessAt, c.Reopened, c.ClosedAt, keyArg(key)))
		if isUnique(err) {
			return saved, httpx.NewProblem(http.StatusConflict, domain.CodeNCRRace, "a correction with this Idempotency-Key is being started; retry")
		}
		return saved, err
	}
	return scanCorrection(tx.QueryRow(ctx, `update quality_correction set status = $3, current_step = $4, steps = $5,
		effectiveness_due_at = $6, effectiveness_result = $7, effectiveness_note = $8, effectiveness_by = $9, effectiveness_at = $10,
		reopened = $11, closed_at = $12
		where `+ownTenant+` and nonconformance_id = $1 and id = $2 returning `+correctionColumns,
		ncrID, c.ID, c.Status, c.CurrentStep, string(steps), c.EffectivenessDueAt, result, c.EffectivenessNote,
		c.EffectivenessBy, c.EffectivenessAt, c.Reopened, c.ClosedAt))
}
