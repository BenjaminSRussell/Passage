package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// CodeVersionConflict is returned when If-Match names a version the record no
// longer has (the platform-wide code, as in receivables and quality).
const CodeVersionConflict = "conflict_version"

// DGCheckLimit caps the DG checks returned for one load; the newest come first.
const DGCheckLimit = 20

const loadColumns = `l.id, l.mode, l.status, l.ship_from_site_id, l.ship_to_region, to_char(l.pickup_date, 'YYYY-MM-DD'),
	l.total_weight_kg::text, l.total_volume_m3::text, l.pallets, l.max_weight_kg::text, l.max_volume_m3::text, l.max_pallets,
	(select coalesce(jsonb_agg(s.shipment_id order by s.position), '[]'::jsonb) from load_shipment s
	  where s.tenant_id = l.tenant_id and s.load_id = l.id),
	l.version, l.created_at`

const dgCheckColumns = `id, load_id, regulation, result, adr_points::text, findings, shipping_papers, checked_by, checked_at`

const decisionColumns = `id, freight_bill_id, action, from_status, to_status, reason, decided_by, decided_at, request_hash`

// LoadFilter narrows the load list (loads by status, §6.34.14). Empty matches all.
type LoadFilter struct {
	Status string
}

// BuildLoads stores the loads of one rule-based build in one transaction. A
// shipment already on a load is refused with TMS-0116; the load_shipment key
// keeps that true under concurrent builds. A replay with the same key returns
// the loads of the first request.
func (s *Postgres) BuildLoads(ctx context.Context, loads []domain.Load, idem Idem) ([]domain.Load, error) {
	p, _ := tenant.FromContext(ctx)
	var out []domain.Load
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay := func() (bool, error) {
			if idem.Key == "" {
				return false, nil
			}
			var buildID string
			var hash *string
			err := tx.QueryRow(ctx, `select id, request_hash from load_build where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key).Scan(&buildID, &hash)
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			if deref(hash) != idem.Fingerprint {
				return false, reuse()
			}
			out, err = queryLoads(ctx, tx, `select `+loadColumns+` from load l where l.tenant_id = $1 and l.build_id = $2 order by l.build_position`, p.TenantID, buildID)
			return true, err
		}
		if done, err := replay(); done || err != nil {
			return err
		}
		var shipmentIDs []string
		for _, l := range loads {
			shipmentIDs = append(shipmentIDs, l.ShipmentIDs...)
		}
		var planned, onLoad string
		err := tx.QueryRow(ctx, `select shipment_id, load_id from load_shipment where tenant_id = $1 and shipment_id = any($2::uuid[])
			order by shipment_id limit 1`, p.TenantID, shipmentIDs).Scan(&planned, &onLoad)
		if err == nil {
			return alreadyPlanned(planned, onLoad)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		buildID := ids.New()
		tag, err := tx.Exec(ctx, `insert into load_build (tenant_id, id, idempotency_key, request_hash, created_by) values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, idempotency_key) do nothing`, p.TenantID, buildID, nullable(idem.Key), nullable(idem.Fingerprint), p.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// A concurrent request with the same key committed first.
			if done, err := replay(); done || err != nil {
				return err
			}
		}
		out = make([]domain.Load, 0, len(loads))
		for i, l := range loads {
			created, err := insertLoad(ctx, tx, p.TenantID, buildID, i+1, l)
			if err != nil {
				return err
			}
			out = append(out, created)
		}
		return nil
	})
	return out, err
}

func insertLoad(ctx context.Context, tx pgx.Tx, tenantID, buildID string, position int, l domain.Load) (domain.Load, error) {
	l.ID, l.Status, l.Version = ids.New(), domain.LoadPlanned, 1
	if err := tx.QueryRow(ctx, `insert into load (tenant_id, id, build_id, build_position, mode, status, ship_from_site_id, ship_to_region, pickup_date,
		total_weight_kg, total_volume_m3, pallets, max_weight_kg, max_volume_m3, max_pallets)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10::numeric, $11::numeric, $12, $13::numeric, $14::numeric, $15)
		returning created_at`,
		tenantID, l.ID, buildID, position, l.Mode, l.Status, l.ShipFromSiteID, l.ShipToRegion, l.PickupDate,
		l.TotalWeightKg.String(), l.TotalVolumeM3.String(), l.Pallets,
		l.Capacity.MaxWeightKg.String(), l.Capacity.MaxVolumeM3.String(), l.Capacity.MaxPallets).Scan(&l.CreatedAt); err != nil {
		return domain.Load{}, err
	}
	for i, shipment := range l.ShipmentIDs {
		_, err := tx.Exec(ctx, `insert into load_shipment (tenant_id, shipment_id, load_id, position) values ($1, $2, $3, $4)`,
			tenantID, shipment, l.ID, i+1)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			// A concurrent build planned the shipment after the check above.
			return domain.Load{}, alreadyPlanned(shipment, "")
		}
		if err != nil {
			return domain.Load{}, err
		}
	}
	return l, nil
}

// dgGate refuses a tender for a load whose latest DG check FAILed (AC-34.13,
// FR-34.15 with dg_check_mode BLOCK, the default). A load with no DG check, or
// whose latest check is PASS or WARN, may be tendered; a new passing check after
// the load is split lifts the block.
func dgGate(ctx context.Context, tx pgx.Tx, tenantID, loadID string) error {
	var result string
	err := tx.QueryRow(ctx, `select result from dg_check where tenant_id = $1 and load_id = $2
		order by checked_at desc, id desc limit 1`, tenantID, loadID).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if result == domain.DGFail {
		return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDGSegregation,
			"the load's latest dangerous-goods check failed; split the load or fix its DG data and check it again before tendering")
	}
	return nil
}

func alreadyPlanned(shipment, load string) error {
	detail := "shipment " + shipment + " is already on a load"
	if load != "" {
		detail = "shipment " + shipment + " is already on load " + load
	}
	return httpx.NewProblem(http.StatusConflict, domain.CodeShipmentPlanned, detail)
}

// ListLoads returns the newest loads first, at most ListLimit.
func (s *Postgres) ListLoads(ctx context.Context, f LoadFilter) ([]domain.Load, error) {
	p, _ := tenant.FromContext(ctx)
	var out []domain.Load
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = queryLoads(ctx, tx, `select `+loadColumns+` from load l where l.tenant_id = $1 and ($2::text = '' or l.status = $2)
			order by l.created_at desc, l.build_position, l.id limit $3`, p.TenantID, f.Status, ListLimit)
		return err
	})
	if out == nil {
		out = []domain.Load{}
	}
	return out, err
}

// GetLoad returns one load or TMS-0100.
func (s *Postgres) GetLoad(ctx context.Context, id string) (domain.Load, error) {
	p, _ := tenant.FromContext(ctx)
	var l domain.Load
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		l, err = scanLoad(tx.QueryRow(ctx, `select `+loadColumns+` from load l where l.tenant_id = $1 and l.id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Load{}, NotFound("load")
	}
	return l, err
}

// RecordDGCheck stores the result of a DG check on a load that exists.
func (s *Postgres) RecordDGCheck(ctx context.Context, in domain.DGCheck) (domain.DGCheck, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	in.CheckedBy = p.UserID
	findings, err := json.Marshal(in.Findings)
	if err != nil {
		return domain.DGCheck{}, err
	}
	papers, err := json.Marshal(in.ShippingPapers)
	if err != nil {
		return domain.DGCheck{}, err
	}
	var out domain.DGCheck
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from load where tenant_id = $1 and id = $2)`, p.TenantID, in.LoadID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return NotFound("load")
		}
		var err error
		out, err = scanDGCheck(tx.QueryRow(ctx, `insert into dg_check (tenant_id, id, load_id, regulation, result, adr_points, findings, shipping_papers, checked_by, checked_at)
			values ($1, $2, $3, $4, $5, $6::numeric, $7::jsonb, $8::jsonb, $9, $10) returning `+dgCheckColumns,
			p.TenantID, in.ID, in.LoadID, in.Regulation, in.Result, in.ADRPoints.String(), string(findings), string(papers), in.CheckedBy, in.CheckedAt))
		return err
	})
	return out, err
}

// ListDGChecks returns the newest DG checks of a load first.
func (s *Postgres) ListDGChecks(ctx context.Context, loadID string) ([]domain.DGCheck, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.DGCheck{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+dgCheckColumns+` from dg_check where tenant_id = $1 and load_id = $2
			order by checked_at desc, id desc limit $3`, p.TenantID, loadID, DGCheckLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanDGCheck(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// DecideFreightBill approves or disputes a bill (BR-34.4) under a row lock.
// version is the If-Match value and must be the bill's current version. The
// decision row and, for an approval, the dev.passage.tms.freightbill.approved.v1
// outbox row are written in the same transaction. A replay with the same
// Idempotency-Key returns the bill as it is now, without a second decision.
func (s *Postgres) DecideFreightBill(ctx context.Context, id, action, reason string, version int, idem Idem, now time.Time) (domain.FreightBill, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.FreightBill
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		lock := func() (domain.FreightBill, error) {
			b, _, err := scanFreightBill(tx.QueryRow(ctx, `select `+freightBillColumns+` from freight_bill where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return b, NotFound("freight bill")
			}
			return b, err
		}
		bill, err := lock()
		if err != nil {
			return err
		}
		if idem.Key != "" {
			d, hash, err := scanDecision(tx.QueryRow(ctx, `select `+decisionColumns+` from freight_bill_decision where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			switch {
			case err == nil && (hash != idem.Fingerprint || d.FreightBillID != id):
				return reuse()
			case err == nil:
				out = bill
				return nil
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		if bill.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the freight bill changed since it was read; reload it and decide again")
		}
		next, err := domain.Decide(action, bill.MatchStatus, reason)
		var rule *domain.Error
		if errors.As(err, &rule) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, rule.Code, rule.Detail)
		}
		if err != nil {
			return err
		}
		d := domain.Decision{ID: ids.New(), FreightBillID: id, Action: action, FromStatus: bill.MatchStatus, ToStatus: next,
			Reason: reason, DecidedBy: p.UserID, DecidedAt: now}
		if _, err := tx.Exec(ctx, `insert into freight_bill_decision (tenant_id, id, freight_bill_id, action, from_status, to_status, reason,
			decided_by, decided_at, idempotency_key, request_hash) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			p.TenantID, d.ID, id, d.Action, d.FromStatus, d.ToStatus, d.Reason, d.DecidedBy, d.DecidedAt, nullable(idem.Key), nullable(idem.Fingerprint)); err != nil {
			return err
		}
		out, _, err = scanFreightBill(tx.QueryRow(ctx, `update freight_bill set match_status = $3, version = version + 1, updated_at = $4
			where tenant_id = $1 and id = $2 returning `+freightBillColumns, p.TenantID, id, next, now))
		if err != nil || action != domain.ActionApprove {
			return err
		}
		return enqueueApproved(ctx, tx, p.TenantID, out, d)
	})
	return out, err
}

// ListBillDecisions returns a bill's approvals and disputes, oldest first.
func (s *Postgres) ListBillDecisions(ctx context.Context, billID string) ([]domain.Decision, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Decision{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+decisionColumns+` from freight_bill_decision where tenant_id = $1 and freight_bill_id = $2
			order by decided_at, id`, p.TenantID, billID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, _, err := scanDecision(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func enqueueApproved(ctx context.Context, tx pgx.Tx, tenantID string, b domain.FreightBill, d domain.Decision) error {
	variance := decimal.Zero
	if b.VarianceTotal != nil {
		variance = *b.VarianceTotal
	}
	data := tmsevents.FreightBillApprovedPayload{
		FreightBillID:    b.ID,
		LoadID:           b.LoadID,
		CarrierID:        b.CarrierID,
		CarrierInvoiceID: deref(b.CarrierInvoiceID),
		Currency:         b.Currency,
		ExpectedTotal:    b.ExpectedTotal.StringFixed(2),
		InvoicedTotal:    b.ExpectedTotal.Add(variance).StringFixed(2),
		Variance:         variance.StringFixed(2),
		FromStatus:       d.FromStatus,
		Reason:           d.Reason,
		ApprovedBy:       d.DecidedBy,
		ApprovedAt:       d.DecidedAt.UTC().Format(time.RFC3339Nano),
	}
	evt, err := events.New(tmsevents.FreightBillApproved, eventSource, b.ID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "freightbill", b.ID, evt)
}

func queryLoads(ctx context.Context, q queryer, sql string, args ...any) ([]domain.Load, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Load
	for rows.Next() {
		l, err := scanLoad(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func scanLoad(row pgx.Row) (domain.Load, error) {
	var l domain.Load
	var weight, volume, maxWeight, maxVolume string
	var shipments []byte
	if err := row.Scan(&l.ID, &l.Mode, &l.Status, &l.ShipFromSiteID, &l.ShipToRegion, &l.PickupDate, &weight, &volume, &l.Pallets,
		&maxWeight, &maxVolume, &l.Capacity.MaxPallets, &shipments, &l.Version, &l.CreatedAt); err != nil {
		return domain.Load{}, err
	}
	l.TotalWeightKg = decimal.RequireFromString(weight)
	l.TotalVolumeM3 = decimal.RequireFromString(volume)
	l.Capacity.MaxWeightKg = decimal.RequireFromString(maxWeight)
	l.Capacity.MaxVolumeM3 = decimal.RequireFromString(maxVolume)
	if err := json.Unmarshal(shipments, &l.ShipmentIDs); err != nil {
		return domain.Load{}, err
	}
	return l, nil
}

func scanDGCheck(row pgx.Row) (domain.DGCheck, error) {
	var c domain.DGCheck
	var points string
	var findings, papers []byte
	if err := row.Scan(&c.ID, &c.LoadID, &c.Regulation, &c.Result, &points, &findings, &papers, &c.CheckedBy, &c.CheckedAt); err != nil {
		return domain.DGCheck{}, err
	}
	c.ADRPoints = decimal.RequireFromString(points)
	if err := json.Unmarshal(findings, &c.Findings); err != nil {
		return domain.DGCheck{}, err
	}
	if err := json.Unmarshal(papers, &c.ShippingPapers); err != nil {
		return domain.DGCheck{}, err
	}
	return c, nil
}

func scanDecision(row pgx.Row) (domain.Decision, string, error) {
	var d domain.Decision
	var hash *string
	if err := row.Scan(&d.ID, &d.FreightBillID, &d.Action, &d.FromStatus, &d.ToStatus, &d.Reason, &d.DecidedBy, &d.DecidedAt, &hash); err != nil {
		return domain.Decision{}, "", err
	}
	return d, deref(hash), nil
}
