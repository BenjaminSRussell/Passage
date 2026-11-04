package store

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
)

const (
	// CodeAllocationKeyExists is 409 when an allocation key code is already in use.
	CodeAllocationKeyExists = "PLAN-ALLOCATION-KEY-EXISTS"
	// CodeAllocationKeyNotFound is 404 for an unknown allocation key code.
	CodeAllocationKeyNotFound = "PLAN-ALLOCATION-KEY-NOT-FOUND"
	// CodeAllocationRange is 422 when a converted quantity exceeds the largest storable quantity.
	CodeAllocationRange = "PLAN-ALLOCATION-RANGE"

	allocationKeyCodeConstraint        = "forecast_allocation_key_code_key"
	allocationKeyIdempotencyConstraint = "forecast_allocation_key_idempotency_key"
)

// AllocationKey is a stored allocation key (FR-22.8). Version is its ETag. A list
// leaves Spec.Lines empty and reports the count in LineCount.
type AllocationKey struct {
	ID        string
	Version   int
	Spec      domain.AllocationKeySpec
	LineCount int
}

// AllocationKeyCreate is one create command; IdempotencyKey and InputHash are empty
// when the caller sent no Idempotency-Key.
type AllocationKeyCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.AllocationKeySpec
}

// AllocationPeriod is one aggregate quantity of the key's forecast unit for a period.
type AllocationPeriod struct {
	Start, End string
	Qty        decimal.Decimal
}

// AllocateRequest splits the aggregate quantities of Periods across the items of a
// key into the cells of one model at one site.
type AllocateRequest struct {
	ModelCode       string
	KeyCode         string
	LegalEntityID   string
	ExpectedVersion int
	SiteID          string
	Periods         []AllocationPeriod
}

// AllocateOutcome is the model after the allocation and what was written.
type AllocateOutcome struct {
	Model        ForecastModel
	Key          AllocationKey
	Items        int
	CellsWritten int
}

func allocationKeyNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeAllocationKeyNotFound, "allocation key not found")
}

const allocationKeySelect = `select k.id::text, k.version, k.code, k.name, k.forecast_uom,
	(select count(*) from forecast_allocation_key_line l where l.tenant_id = k.tenant_id and l.key_id = k.id)
	from forecast_allocation_key k`

func scanAllocationKey(row pgx.Row) (AllocationKey, error) {
	var k AllocationKey
	err := row.Scan(&k.ID, &k.Version, &k.Spec.Code, &k.Spec.Name, &k.Spec.ForecastUom, &k.LineCount)
	return k, err
}

func allocationLines(ctx context.Context, tx pgx.Tx, tenantID, keyID string) ([]domain.AllocationLine, error) {
	rows, err := tx.Query(ctx, `select item_id::text, share::text, uom_factor::text from forecast_allocation_key_line
		where tenant_id = $1 and key_id = $2::uuid order by item_id`, tenantID, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AllocationLine
	for rows.Next() {
		var l domain.AllocationLine
		var share, factor string
		if err := rows.Scan(&l.ItemID, &share, &factor); err != nil {
			return nil, err
		}
		if l.Share, err = decimal.NewFromString(share); err != nil {
			return nil, err
		}
		if l.UomFactor, err = decimal.NewFromString(factor); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListAllocationKeys returns the tenant's allocation keys ordered by code, without lines.
func (s *Postgres) ListAllocationKeys(ctx context.Context, limit int) ([]AllocationKey, error) {
	p, _ := tenant.FromContext(ctx)
	out := []AllocationKey{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, allocationKeySelect+` where k.tenant_id = $1 order by k.code limit $2`, p.TenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			k, err := scanAllocationKey(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

// GetAllocationKey returns one allocation key with its lines ordered by item.
func (s *Postgres) GetAllocationKey(ctx context.Context, code string) (AllocationKey, error) {
	p, _ := tenant.FromContext(ctx)
	var k AllocationKey
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		k, err = scanAllocationKey(tx.QueryRow(ctx, allocationKeySelect+` where k.tenant_id = $1 and k.code = $2`, p.TenantID, code))
		if err != nil {
			return err
		}
		k.Spec.Lines, err = allocationLines(ctx, tx, p.TenantID, k.ID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return AllocationKey{}, allocationKeyNotFound()
	}
	return k, err
}

func insertAllocationLines(ctx context.Context, tx pgx.Tx, tenantID, keyID string, lines []domain.AllocationLine) error {
	batch := &pgx.Batch{}
	for _, l := range lines {
		batch.Queue(`insert into forecast_allocation_key_line (tenant_id, id, key_id, item_id, share, uom_factor)
			values ($1, $2, $3::uuid, $4::uuid, $5::numeric, $6::numeric)`, tenantID, ids.New(), keyID, l.ItemID, l.Share.String(), l.UomFactor.String())
	}
	return tx.SendBatch(ctx, batch).Close()
}

func sortedLines(lines []domain.AllocationLine) []domain.AllocationLine {
	out := append([]domain.AllocationLine(nil), lines...)
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
}

// CreateAllocationKey stores a validated key at version 1. A code already in use is
// 409 PLAN-ALLOCATION-KEY-EXISTS. With an Idempotency-Key, a retry of the same body
// returns the key created by the first call (replayed true) and another body under
// the same key is 422.
func (s *Postgres) CreateAllocationKey(ctx context.Context, req AllocationKeyCreate) (AllocationKey, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out AllocationKey
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored string
			err := tx.QueryRow(ctx, `select input_hash from forecast_allocation_key where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&stored)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				k, err := scanAllocationKey(tx.QueryRow(ctx, allocationKeySelect+` where k.tenant_id = $1 and k.idempotency_key = $2`, p.TenantID, *key))
				if err != nil {
					return err
				}
				if k.Spec.Lines, err = allocationLines(ctx, tx, p.TenantID, k.ID); err != nil {
					return err
				}
				out, replayed = k, true
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into forecast_allocation_key (tenant_id, id, code, name, forecast_uom, idempotency_key, input_hash, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, id, req.Spec.Code, req.Spec.Name, req.Spec.ForecastUom, key, hash, nullableText(p.UserID)); err != nil {
			return err
		}
		spec := req.Spec
		spec.Lines = sortedLines(spec.Lines)
		if err := insertAllocationLines(ctx, tx, p.TenantID, id, spec.Lines); err != nil {
			return err
		}
		out = AllocationKey{ID: id, Version: 1, Spec: spec, LineCount: len(spec.Lines)}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case allocationKeyCodeConstraint:
			return AllocationKey{}, false, httpx.NewProblem(http.StatusConflict, CodeAllocationKeyExists, "an allocation key with code "+req.Spec.Code+" already exists")
		case allocationKeyIdempotencyConstraint:
			// A concurrent create with the same key committed first.
			return AllocationKey{}, false, idempotencyReuse()
		}
	}
	return out, replayed, err
}

// UpdateAllocationKey replaces the name, unit and lines of the key spec.Code under
// If-Match: expectedVersion must be its current version (412 otherwise), and the
// version is bumped.
func (s *Postgres) UpdateAllocationKey(ctx context.Context, expectedVersion int, spec domain.AllocationKeySpec) (AllocationKey, error) {
	p, _ := tenant.FromContext(ctx)
	var out AllocationKey
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var version int
		err := tx.QueryRow(ctx, `select id::text, version from forecast_allocation_key where tenant_id = $1 and code = $2 for update`, p.TenantID, spec.Code).
			Scan(&id, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return allocationKeyNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("allocation key", version)
		}
		if _, err := tx.Exec(ctx, `update forecast_allocation_key set name = $3, forecast_uom = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid`, p.TenantID, id, spec.Name, spec.ForecastUom); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from forecast_allocation_key_line where tenant_id = $1 and key_id = $2::uuid`, p.TenantID, id); err != nil {
			return err
		}
		spec.Lines = sortedLines(spec.Lines)
		if err := insertAllocationLines(ctx, tx, p.TenantID, id, spec.Lines); err != nil {
			return err
		}
		out = AllocationKey{ID: id, Version: version + 1, Spec: spec, LineCount: len(spec.Lines)}
		return nil
	})
	return out, err
}

// AllocateForecast splits the aggregate quantity of each period across the items of
// the key and writes the shares as manual cells of the model at the site (FR-22.8,
// BR-22.10), under If-Match on the model (0 creates it). A cell replaces the stored
// cell of the same item, site and period start but keeps its sales quantity. The
// reduced quantities of the touched series are recomputed, the model version
// increases and forecast.allocated.v1 is written, all in one transaction. A
// submitted or published model is locked (409); an unknown key is 404 and nothing is
// created; a converted quantity that does not fit is 422 PLAN-ALLOCATION-RANGE.
func (s *Postgres) AllocateForecast(ctx context.Context, req AllocateRequest) (AllocateOutcome, error) {
	p, _ := tenant.FromContext(ctx)
	var out AllocateOutcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		key, err := scanAllocationKey(tx.QueryRow(ctx, allocationKeySelect+` where k.tenant_id = $1 and k.code = $2 for share of k`, p.TenantID, req.KeyCode))
		if errors.Is(err, pgx.ErrNoRows) {
			return allocationKeyNotFound()
		}
		if err != nil {
			return err
		}
		if key.Spec.Lines, err = allocationLines(ctx, tx, p.TenantID, key.ID); err != nil {
			return err
		}
		var cells []ForecastCellInput
		for _, per := range req.Periods {
			parts, ok := domain.AllocateForecast(per.Qty, key.Spec.Lines)
			if !ok {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeAllocationRange,
					"the allocation of "+per.Start+" exceeds the largest storable quantity once converted to the inventory unit; nothing was written")
			}
			for _, part := range parts {
				cells = append(cells, ForecastCellInput{ItemID: part.ItemID, SiteID: req.SiteID, PeriodStart: per.Start, PeriodEnd: per.End, Source: "manual", Qty: part.InventoryQty})
			}
		}
		m, err := lockForecastModel(ctx, tx, p.TenantID, req.ModelCode, req.LegalEntityID, req.ExpectedVersion)
		if err != nil {
			return err
		}
		if !domain.ConsensusWritable(m.Status) {
			return httpx.NewProblem(http.StatusConflict, CodeForecastLocked, "forecast model "+m.Code+" is "+m.Status+"; its cells are locked")
		}
		if err := tx.QueryRow(ctx, `update forecast_model set version = version + 1 where tenant_id = $1 and id = $2::uuid returning version`,
			p.TenantID, m.id).Scan(&m.Version); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, c := range cells {
			batch.Queue(`insert into forecast_line (tenant_id, id, model_id, item_id, site_id, period_start, period_end, qty, source, sales_qty, reduced_qty, updated_by)
				values ($1, $2, $3::uuid, $4::uuid, $5::uuid, $6::date, $7::date, $8::numeric, 'manual', 0, $8::numeric, $9)
				on conflict (tenant_id, model_id, item_id, site_id, period_start) do update set period_end = excluded.period_end,
				qty = excluded.qty, source = 'manual', reduced_qty = excluded.reduced_qty, updated_by = excluded.updated_by, updated_at = now()`,
				p.TenantID, ids.New(), m.id, c.ItemID, req.SiteID, c.PeriodStart, c.PeriodEnd, c.Qty.String(), nullableText(p.UserID))
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		for _, l := range key.Spec.Lines {
			if err := reduceSeries(ctx, tx, p.TenantID, m, l.ItemID, req.SiteID); err != nil {
				return err
			}
		}
		from, to := req.Periods[0].Start, req.Periods[0].End
		for _, per := range req.Periods {
			if per.Start < from {
				from = per.Start
			}
			if per.End > to {
				to = per.End
			}
		}
		evt, err := events.New(planevents.ForecastAllocated, eventSource, m.id, p.TenantID, planevents.ForecastAllocatedData{
			ModelID: m.id, ModelCode: m.Code, Version: m.Version, KeyID: key.ID, KeyCode: key.Spec.Code, KeyVersion: key.Version,
			SiteID: req.SiteID, Items: len(key.Spec.Lines), Periods: len(req.Periods), CellsWritten: len(cells), PeriodFrom: from, PeriodTo: to, AllocatedBy: p.UserID,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "forecast-model", m.id, evt); err != nil {
			return err
		}
		out = AllocateOutcome{Model: m, Key: key, Items: len(key.Spec.Lines), CellsWritten: len(cells)}
		return nil
	})
	return out, err
}
