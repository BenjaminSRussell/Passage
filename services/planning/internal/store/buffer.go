package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
	// CodeProfileExists is returned when a buffer profile code is already in use.
	CodeProfileExists = "PLAN-PROFILE-EXISTS"
	// CodeProfileNotFound is returned for an unknown buffer profile, on read
	// (404) and when an item coverage or a recalculation names one (422).
	CodeProfileNotFound = "PLAN-PROFILE-NOT-FOUND"

	profileCodeConstraint        = "buffer_profile_code_key"
	profileIdempotencyConstraint = "buffer_profile_idempotency_key"
)

// BufferProfile is a stored buffer profile (FR-22.16). Version is its ETag.
type BufferProfile struct {
	ID      string
	Version int
	Spec    domain.BufferProfileSpec
}

// BufferProfileCreate is one create command; IdempotencyKey and InputHash are
// empty when the caller sent no Idempotency-Key.
type BufferProfileCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.BufferProfileSpec
}

// DdmrpBuffer is the stored result of one buffer recalculation (FR-22.16), one
// row per item, site and warehouse scope. ProfileCode is read from the profile
// for display; CalculatedAt is an RFC 3339 UTC timestamp.
type DdmrpBuffer struct {
	ID           string
	ItemID       string
	SiteID       string
	WarehouseID  string // empty for the site scope
	ProfileID    string
	ProfileCode  string
	ADU          decimal.Decimal
	DLTDays      decimal.Decimal
	DAF          decimal.Decimal
	Status       domain.BufferStatus
	CalculatedAt string
}

// BufferQuery filters the buffer status inquiry (4.22.14): site, zone and a
// priority ceiling (priority ≤ x); empty fields do not filter.
type BufferQuery struct {
	SiteID      string
	Zone        string
	MaxPriority *decimal.Decimal
	Limit       int
}

const (
	profileSelect = `select id::text, version, code, item_type, lead_time_factor::text, variability_factor::text,
	order_cycle_days, min_order_qty::text, spike_threshold_pct::text, spike_horizon_days, adu_window_past_days,
	adu_window_future_days from buffer_profile`
	calculatedAtText = `to_char(b.calculated_at at time zone 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`
	bufferSelect     = `select b.id::text, b.item_id::text, b.site_id::text, coalesce(b.warehouse_id::text, ''),
	b.profile_id::text, p.code, b.adu::text, b.dlt_days::text, b.daf::text, b.red_base::text, b.red_safety::text,
	b.yellow::text, b.green::text, b.top_of_red::text, b.top_of_yellow::text, b.top_of_green::text,
	b.qualified_demand::text, b.net_flow::text, b.planned_qty::text, b.priority::text, b.zone, ` + calculatedAtText + `
	from ddmrp_buffer b join buffer_profile p on p.tenant_id = b.tenant_id and p.id = b.profile_id`
)

func profileNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeProfileNotFound, "buffer profile not found")
}

// profileWriteError maps the constraint violations of a create or update.
func profileWriteError(err error, code string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case profileCodeConstraint:
			return httpx.NewProblem(http.StatusConflict, CodeProfileExists, "a buffer profile with code "+code+" already exists")
		case profileIdempotencyConstraint:
			// A concurrent create with the same key committed first.
			return idempotencyReuse()
		}
	}
	return err
}

// ListBufferProfiles returns the tenant's buffer profiles ordered by code.
func (s *Postgres) ListBufferProfiles(ctx context.Context, limit int) ([]BufferProfile, error) {
	p, _ := tenant.FromContext(ctx)
	out := []BufferProfile{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, profileSelect+` where tenant_id = $1 order by code limit $2`, p.TenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			bp, err := scanProfile(rows)
			if err != nil {
				return err
			}
			out = append(out, bp)
		}
		return rows.Err()
	})
	return out, err
}

// GetBufferProfile returns one buffer profile by id; an unknown id is 404.
func (s *Postgres) GetBufferProfile(ctx context.Context, id string) (BufferProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var bp BufferProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		bp, err = scanProfile(tx.QueryRow(ctx, profileSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return BufferProfile{}, profileNotFound()
	}
	return bp, err
}

// CreateBufferProfile stores a validated spec at version 1. A code already in
// use is 409 PLAN-PROFILE-EXISTS. With an Idempotency-Key, a retry of the same
// body returns the profile the first call created (replayed true) and another
// body under the same key is 422.
func (s *Postgres) CreateBufferProfile(ctx context.Context, req BufferProfileCreate) (BufferProfile, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out BufferProfile
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored string
			err := tx.QueryRow(ctx, `select input_hash from buffer_profile where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&stored)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				bp, err := scanProfile(tx.QueryRow(ctx, profileSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
				out, replayed = bp, true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		sp := req.Spec
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into buffer_profile (tenant_id, id, code, item_type, lead_time_factor, variability_factor,
			order_cycle_days, min_order_qty, spike_threshold_pct, spike_horizon_days, adu_window_past_days, adu_window_future_days,
			idempotency_key, input_hash)
			values ($1, $2, $3, $4, $5::numeric, $6::numeric, $7, $8::numeric, $9::numeric, $10, $11, $12, $13, $14)`,
			p.TenantID, id, sp.Code, sp.ItemType, sp.LeadTimeFactor.String(), sp.VariabilityFactor.String(), sp.OrderCycleDays,
			sp.MinOrderQty.String(), sp.SpikeThresholdPct.String(), sp.SpikeHorizonDays, sp.PastDays, sp.FutureDays, key, hash); err != nil {
			return err
		}
		var err error
		out, err = scanProfile(tx.QueryRow(ctx, profileSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if err != nil {
		return BufferProfile{}, false, profileWriteError(err, req.Spec.Code)
	}
	return out, replayed, nil
}

// UpdateBufferProfile replaces the settings of profile id under If-Match:
// expectedVersion must be its current version (412 otherwise) and the version
// is bumped. Stored buffers keep their zones until they are recalculated.
func (s *Postgres) UpdateBufferProfile(ctx context.Context, id string, expectedVersion int, spec domain.BufferProfileSpec) (BufferProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var out BufferProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `select version from buffer_profile where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&version)
		if errors.Is(err, pgx.ErrNoRows) {
			return profileNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("buffer profile", version)
		}
		if _, err := tx.Exec(ctx, `update buffer_profile set code = $3, item_type = $4, lead_time_factor = $5::numeric,
			variability_factor = $6::numeric, order_cycle_days = $7, min_order_qty = $8::numeric, spike_threshold_pct = $9::numeric,
			spike_horizon_days = $10, adu_window_past_days = $11, adu_window_future_days = $12, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`,
			p.TenantID, id, spec.Code, spec.ItemType, spec.LeadTimeFactor.String(), spec.VariabilityFactor.String(), spec.OrderCycleDays,
			spec.MinOrderQty.String(), spec.SpikeThresholdPct.String(), spec.SpikeHorizonDays, spec.PastDays, spec.FutureDays); err != nil {
			return err
		}
		out, err = scanProfile(tx.QueryRow(ctx, profileSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if err != nil {
		return BufferProfile{}, profileWriteError(err, spec.Code)
	}
	return out, nil
}

// UpsertDdmrpBuffer stores the recalculated buffer of its item, site and
// warehouse scope, replacing the previous result of that scope in one
// statement (the nightly recalculation of 4.22.16 upserts per item and site).
// A profile the tenant does not have is 422 PLAN-PROFILE-NOT-FOUND.
func (s *Postgres) UpsertDdmrpBuffer(ctx context.Context, b DdmrpBuffer) (DdmrpBuffer, error) {
	p, _ := tenant.FromContext(ctx)
	z, st := b.Status.Zones, b.Status
	var out DdmrpBuffer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `insert into ddmrp_buffer (tenant_id, id, item_id, site_id, warehouse_id, profile_id,
			adu, dlt_days, daf, red_base, red_safety, yellow, green, top_of_red, top_of_yellow, top_of_green,
			qualified_demand, net_flow, planned_qty, priority, zone)
			values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric,
			$13::numeric, $14::numeric, $15::numeric, $16::numeric, $17::numeric, $18::numeric, $19::numeric, $20::numeric, $21)
			on conflict (tenant_id, item_id, site_id, coalesce(warehouse_id, '00000000-0000-0000-0000-000000000000'::uuid))
			do update set profile_id = excluded.profile_id, adu = excluded.adu, dlt_days = excluded.dlt_days, daf = excluded.daf,
				red_base = excluded.red_base, red_safety = excluded.red_safety, yellow = excluded.yellow, green = excluded.green,
				top_of_red = excluded.top_of_red, top_of_yellow = excluded.top_of_yellow, top_of_green = excluded.top_of_green,
				qualified_demand = excluded.qualified_demand, net_flow = excluded.net_flow, planned_qty = excluded.planned_qty,
				priority = excluded.priority, zone = excluded.zone, calculated_at = now()
			returning id::text`,
			p.TenantID, ids.New(), b.ItemID, b.SiteID, optional(b.WarehouseID), b.ProfileID,
			b.ADU.String(), b.DLTDays.String(), b.DAF.String(), z.RedBase.String(), z.RedSafety.String(), z.Yellow.String(),
			z.Green.String(), z.TopOfRed.String(), z.TopOfYellow.String(), z.TopOfGreen.String(), st.QualifiedDemand.String(),
			st.NetFlow.String(), st.PlannedQty.String(), st.Priority.String(), st.Zone).Scan(&id); err != nil {
			return err
		}
		var err error
		out, err = scanBuffer(tx.QueryRow(ctx, bufferSelect+` where b.tenant_id = $1 and b.id = $2`, p.TenantID, id))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return DdmrpBuffer{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeProfileNotFound, "buffer profile "+b.ProfileID+" does not exist")
	}
	return out, err
}

// ListDdmrpBuffers is the buffer status inquiry (4.22.14): the stored buffers,
// optionally of one site, one zone and at or below a priority, most urgent
// (lowest priority) first.
func (s *Postgres) ListDdmrpBuffers(ctx context.Context, q BufferQuery) ([]DdmrpBuffer, error) {
	p, _ := tenant.FromContext(ctx)
	var maxPriority *string
	if q.MaxPriority != nil {
		v := q.MaxPriority.String()
		maxPriority = &v
	}
	out := []DdmrpBuffer{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, bufferSelect+` where b.tenant_id = $1 and ($2::uuid is null or b.site_id = $2)
			and ($3 = '' or b.zone = $3) and ($4::numeric is null or b.priority <= $4::numeric)
			order by b.priority, b.item_id, b.id limit $5`, p.TenantID, optional(q.SiteID), q.Zone, maxPriority, q.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBuffer(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

func scanProfile(row pgx.Row) (BufferProfile, error) {
	var bp BufferProfile
	s := &bp.Spec
	var ltf, vf, moq, spike string
	if err := row.Scan(&bp.ID, &bp.Version, &s.Code, &s.ItemType, &ltf, &vf, &s.OrderCycleDays, &moq, &spike,
		&s.SpikeHorizonDays, &s.PastDays, &s.FutureDays); err != nil {
		return BufferProfile{}, err
	}
	ds, err := parseDecimals(ltf, vf, moq, spike)
	if err != nil {
		return BufferProfile{}, fmt.Errorf("buffer profile %s: %w", bp.ID, err)
	}
	s.LeadTimeFactor, s.VariabilityFactor, s.MinOrderQty, s.SpikeThresholdPct = ds[0], ds[1], ds[2], ds[3]
	return bp, nil
}

func scanBuffer(row pgx.Row) (DdmrpBuffer, error) {
	var b DdmrpBuffer
	var adu, dlt, daf, rb, rs, y, g, tor, toy, tog, qd, nf, pq, pr string
	if err := row.Scan(&b.ID, &b.ItemID, &b.SiteID, &b.WarehouseID, &b.ProfileID, &b.ProfileCode, &adu, &dlt, &daf,
		&rb, &rs, &y, &g, &tor, &toy, &tog, &qd, &nf, &pq, &pr, &b.Status.Zone, &b.CalculatedAt); err != nil {
		return DdmrpBuffer{}, err
	}
	ds, err := parseDecimals(adu, dlt, daf, rb, rs, y, g, tor, toy, tog, qd, nf, pq, pr)
	if err != nil {
		return DdmrpBuffer{}, fmt.Errorf("ddmrp buffer %s: %w", b.ID, err)
	}
	b.ADU, b.DLTDays, b.DAF = ds[0], ds[1], ds[2]
	b.Status.Zones = domain.BufferZones{RedBase: ds[3], RedSafety: ds[4], Yellow: ds[5], Green: ds[6],
		TopOfRed: ds[7], TopOfYellow: ds[8], TopOfGreen: ds[9]}
	b.Status.QualifiedDemand, b.Status.NetFlow, b.Status.PlannedQty, b.Status.Priority = ds[10], ds[11], ds[12], ds[13]
	b.Status.AdjustedADU = b.ADU.Mul(b.DAF)
	return b, nil
}

// parseDecimals converts stored numeric text to decimals, in order.
func parseDecimals(vals ...string) ([]decimal.Decimal, error) {
	out := make([]decimal.Decimal, len(vals))
	for i, v := range vals {
		d, err := decimal.NewFromString(v)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}
