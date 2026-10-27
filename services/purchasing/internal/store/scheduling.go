package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Scheduling agreement problem codes raised by the store (404s).
const (
	CodeSchedulingAgreementNotFound = "PRC-SA-NOT-FOUND"
	CodeSAReleaseNotFound           = "PRC-SA-REL-NOT-FOUND"
	schedulingAggregate             = "scheduling_agreement"
)

// SchedulingAgreementDetail is a scheduling agreement with its schedule and
// timestamps; Agreement.Version is the ETag.
type SchedulingAgreementDetail struct {
	Agreement domain.SchedulingAgreement
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SchedulingAgreementFilter narrows the release monitor; empty fields do not filter.
type SchedulingAgreementFilter struct {
	VendorID  string
	ProductID string
}

// SAMonitorRow is one agreement in the "Monitor scheduling agreement releases"
// inquiry (FR-17.15, 3.17.14): scheduled, received and open cumulative
// quantity, the last release and when the next one is due.
type SAMonitorRow struct {
	ID                string
	Number            string
	LegalEntityID     string
	VendorID          string
	ProductID         string
	SiteID            string
	Unit              string
	ValidFrom         domain.Day
	ValidTo           *domain.Day
	ReleaseFrequency  string
	Scheduled         decimal.Decimal
	Received          decimal.Decimal
	Open              decimal.Decimal
	ReleaseCount      int
	LastReleaseNumber string
	LastReleaseKind   string
	LastRunDate       *domain.Day
	NextReleaseDue    *domain.Day
	Version           int
}

// CreateSchedulingAgreement stores a new agreement with its schedule and a
// fresh SA- number. A replayed Idempotency-Key returns the first agreement.
func (s *Postgres) CreateSchedulingAgreement(ctx context.Context, in domain.SchedulingAgreement, idem Idem) (SchedulingAgreementDetail, error) {
	if err := in.Validate(); err != nil {
		return SchedulingAgreementDetail{}, err
	}
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out SchedulingAgreementDetail
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadSchedulingAgreement(ctx, tx, existing, false)
			return err
		}
		var seq int64
		if err := tx.QueryRow(ctx, `select nextval('purchase_scheduling_agreement_number_seq')`).Scan(&seq); err != nil {
			return err
		}
		in.Number = fmt.Sprintf("SA-%08d", seq)
		if _, err := tx.Exec(ctx, `insert into purchase_scheduling_agreement
			(tenant_id, id, number, legal_entity_id, vendor_id, product_id, site_id, currency, unit, unit_price, target_qty,
			 valid_from, valid_to, frc_horizon_days, jit_horizon_days, firm_zone_days, tradeoff_zone_days, release_frequency)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
			p.TenantID, in.ID, in.Number, in.LegalEntityID, in.VendorID, in.ProductID, in.SiteID, in.Currency, in.Unit, in.UnitPrice,
			in.TargetQty, in.ValidFrom.Time, dayPtr(in.ValidTo), in.Profile.FRCHorizonDays, in.Profile.JITHorizonDays,
			in.Profile.FirmDays, in.Profile.TradeoffDays, in.Profile.ReleaseFrequency); err != nil {
			return err
		}
		for i, l := range in.Lines {
			l.LineNo = i + 1
			if err := upsertScheduleLine(ctx, tx, in.ID, l); err != nil {
				return err
			}
		}
		out, err = loadSchedulingAgreement(ctx, tx, in.ID, false)
		return err
	})
	return out, err
}

// GetSchedulingAgreement returns one agreement with its schedule.
func (s *Postgres) GetSchedulingAgreement(ctx context.Context, id string) (SchedulingAgreementDetail, error) {
	var out SchedulingAgreementDetail
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadSchedulingAgreement(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListSchedulingAgreements is the release monitor (3.17.14), newest first.
func (s *Postgres) ListSchedulingAgreements(ctx context.Context, f SchedulingAgreementFilter, after string, limit int) (Page[SAMonitorRow], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []SAMonitorRow
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.id, a.number, a.legal_entity_id, a.vendor_id, a.product_id, a.site_id, a.unit,
			a.valid_from, a.valid_to, a.release_frequency, a.frc_horizon_days, a.jit_horizon_days, a.firm_zone_days, a.tradeoff_zone_days, a.version,
			coalesce(sl.scheduled, 0), coalesce(sl.received, 0), coalesce(r.n, 0), lr.number, lr.kind, lr.run_date
			from purchase_scheduling_agreement a
			left join lateral (select sum(l.qty) as scheduled, sum(l.received_qty) as received from purchase_sa_schedule_line l
				where l.tenant_id = a.tenant_id and l.agreement_id = a.id) sl on true
			left join lateral (select count(*) as n from purchase_sa_release x
				where x.tenant_id = a.tenant_id and x.agreement_id = a.id) r on true
			left join lateral (select x.number, x.kind, x.run_date from purchase_sa_release x
				where x.tenant_id = a.tenant_id and x.agreement_id = a.id order by x.release_no desc limit 1) lr on true
			where a.tenant_id = $1 and ($2 = '' or a.vendor_id::text = $2) and ($3 = '' or a.product_id::text = $3)
			and ($4::uuid is null or a.id < $4::uuid)
			order by a.id desc limit $5`, p.TenantID, f.VendorID, f.ProductID, nullable(after), limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (SAMonitorRow, error) {
			var m SAMonitorRow
			var from time.Time
			var to, lastRun *time.Time
			var lastNumber, lastKind *string
			var profile domain.ReleaseProfile
			if err := row.Scan(&m.ID, &m.Number, &m.LegalEntityID, &m.VendorID, &m.ProductID, &m.SiteID, &m.Unit, &from, &to,
				&profile.ReleaseFrequency, &profile.FRCHorizonDays, &profile.JITHorizonDays, &profile.FirmDays, &profile.TradeoffDays, &m.Version,
				&m.Scheduled, &m.Received, &m.ReleaseCount, &lastNumber, &lastKind, &lastRun); err != nil {
				return m, err
			}
			m.ValidFrom, m.ValidTo, m.LastRunDate = day(from), dayOrNil(to), dayOrNil(lastRun)
			m.ReleaseFrequency = profile.ReleaseFrequency
			if lastNumber != nil {
				m.LastReleaseNumber, m.LastReleaseKind = *lastNumber, *lastKind
			}
			m.Open = m.Scheduled.Sub(m.Received)
			m.NextReleaseDue = profile.NextReleaseDue(m.ValidFrom, m.ValidTo, m.LastRunDate)
			return m, nil
		})
		return err
	})
	if err != nil {
		return Page[SAMonitorRow]{}, err
	}
	if items == nil {
		items = []SAMonitorRow{}
	}
	return pageOf(items, limit, func(m SAMonitorRow) string { return m.ID }), nil
}

// ReplaceSchedule applies changes to the release schedule judged at asOf
// (BR-17.13): firm-zone reductions need a reason (PRC-SA-001), trade-off
// reductions come back as warnings, and no line drops below its receipts
// (PRC-SA-002). ifMatch is the agreement version the caller read; a stale one
// is 409 PRC-VERSION-CONFLICT. The write bumps the version.
func (s *Postgres) ReplaceSchedule(ctx context.Context, id string, ifMatch int, asOf domain.Day, changes []domain.ScheduleChange) (SchedulingAgreementDetail, []string, error) {
	p, _ := tenant.FromContext(ctx)
	var out SchedulingAgreementDetail
	var warnings []string
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadSchedulingAgreement(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if cur.Agreement.Version != ifMatch {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict,
				fmt.Sprintf("scheduling agreement is at version %d, not %d; reload and retry", cur.Agreement.Version, ifMatch))
		}
		next, warns, err := domain.ApplySchedule(cur.Agreement, changes, asOf)
		if err != nil {
			return err
		}
		keep := make([]int32, 0, len(next))
		for _, l := range next {
			keep = append(keep, int32(l.LineNo))
		}
		if _, err := tx.Exec(ctx, `delete from purchase_sa_schedule_line where tenant_id = $1 and agreement_id = $2 and line_no <> all($3)`,
			p.TenantID, id, keep); err != nil {
			return err
		}
		for _, l := range next {
			if err := upsertScheduleLine(ctx, tx, id, l); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update purchase_scheduling_agreement set version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		warnings = warns
		out, err = loadSchedulingAgreement(ctx, tx, id, false)
		return err
	})
	if warnings == nil {
		warnings = []string{}
	}
	return out, warnings, err
}

// RunSchedulingRelease snapshots the schedule into the next numbered release
// (FR-17.15, SAP ME84) and writes sarelease.created.v1 in the same
// transaction. A run with the same kind and run date on an unchanged schedule
// (same agreement version) returns the release already written with created =
// false, as does a replayed Idempotency-Key, so a repeated job creates nothing
// new (3.17.16). reset records a cumulative reset (BR-17.13).
func (s *Postgres) RunSchedulingRelease(ctx context.Context, id, kind string, runDate domain.Day, reset bool, idem Idem) (domain.SARelease, bool, error) {
	if err := domain.ValidReleaseKind(kind); err != nil {
		return domain.SARelease{}, false, err
	}
	p, _ := tenant.FromContext(ctx)
	var out domain.SARelease
	created := false
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadSchedulingAgreement(ctx, tx, id, true)
		if err != nil {
			return err
		}
		sa := cur.Agreement
		var sameRun string
		err = tx.QueryRow(ctx, `select id from purchase_sa_release where tenant_id = $1 and agreement_id = $2 and kind = $3
			and run_date = $4 and agreement_version = $5 order by release_no desc limit 1`,
			p.TenantID, id, kind, runDate.Time, sa.Version).Scan(&sameRun)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		releaseID := sameRun
		if releaseID == "" {
			releaseID = ids.New()
		}
		existing, replay, err := claimKey(ctx, tx, idem, releaseID)
		if err != nil {
			return err
		}
		if replay || sameRun != "" {
			if replay {
				releaseID = existing
			}
			out, err = loadSARelease(ctx, tx, releaseID)
			return err
		}
		var previous decimal.Decimal
		var lastNo int
		if err := tx.QueryRow(ctx, `select coalesce((select cumulative_released from purchase_sa_release
				where tenant_id = $1 and agreement_id = $2 order by release_no desc limit 1), 0),
			coalesce((select max(release_no) from purchase_sa_release where tenant_id = $1 and agreement_id = $2), 0)`,
			p.TenantID, id).Scan(&previous, &lastNo); err != nil {
			return err
		}
		rel, err := domain.BuildRelease(sa, kind, runDate, lastNo+1, previous, reset)
		if err != nil {
			return err
		}
		rel.ID = releaseID
		if err := insertSARelease(ctx, tx, rel); err != nil {
			return err
		}
		out, created = rel, true
		return enqueue(ctx, tx, schedulingAggregate, id, purchasingevents.SAReleaseCreated, saReleaseEvent(sa, rel))
	})
	return out, created, err
}

// ListSchedulingReleases returns the release headers of an agreement in
// release order; 404 when the agreement is not visible.
func (s *Postgres) ListSchedulingReleases(ctx context.Context, agreementID string) ([]domain.SARelease, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.SARelease{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkSAVisible(ctx, tx, agreementID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+saReleaseColumns+` from purchase_sa_release
			where tenant_id = $1 and agreement_id = $2 order by release_no`, p.TenantID, agreementID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SARelease, error) {
			return scanSARelease(row)
		})
		return err
	})
	return out, err
}

// GetSchedulingRelease returns the release documentation (SAP ME86) of one
// release number: the header with every schedule row and its cumulative figures.
func (s *Postgres) GetSchedulingRelease(ctx context.Context, agreementID string, releaseNo int) (domain.SARelease, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.SARelease
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := checkSAVisible(ctx, tx, agreementID); err != nil {
			return err
		}
		var rid string
		if err := tx.QueryRow(ctx, `select id from purchase_sa_release where tenant_id = $1 and agreement_id = $2 and release_no = $3`,
			p.TenantID, agreementID, releaseNo).Scan(&rid); err != nil {
			return notFound(err, CodeSAReleaseNotFound, fmt.Sprintf("scheduling agreement has no release %d", releaseNo))
		}
		var err error
		out, err = loadSARelease(ctx, tx, rid)
		return err
	})
	return out, err
}

// checkSAVisible is 404 when the agreement does not exist or its legal entity
// is outside the caller's scope; schedule and release rows carry no legal
// entity and are reached only through it.
func checkSAVisible(ctx context.Context, tx pgx.Tx, id string) error {
	p, _ := tenant.FromContext(ctx)
	var one int
	if err := tx.QueryRow(ctx, `select 1 from purchase_scheduling_agreement where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&one); err != nil {
		return notFound(err, CodeSchedulingAgreementNotFound, "scheduling agreement not found")
	}
	return nil
}

// loadSchedulingAgreement reads the header and schedule; lock takes a row lock
// on the header so schedule changes and release runs on one agreement serialize.
func loadSchedulingAgreement(ctx context.Context, tx pgx.Tx, id string, lock bool) (SchedulingAgreementDetail, error) {
	p, _ := tenant.FromContext(ctx)
	var out SchedulingAgreementDetail
	sa := &out.Agreement
	var from time.Time
	var to *time.Time
	q := `select id, number, legal_entity_id, vendor_id, product_id, site_id, currency, unit, unit_price, target_qty,
		valid_from, valid_to, frc_horizon_days, jit_horizon_days, firm_zone_days, tradeoff_zone_days, release_frequency,
		version, created_at, updated_at
		from purchase_scheduling_agreement where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	err := tx.QueryRow(ctx, q, p.TenantID, id).Scan(&sa.ID, &sa.Number, &sa.LegalEntityID, &sa.VendorID, &sa.ProductID, &sa.SiteID,
		&sa.Currency, &sa.Unit, &sa.UnitPrice, &sa.TargetQty, &from, &to, &sa.Profile.FRCHorizonDays, &sa.Profile.JITHorizonDays,
		&sa.Profile.FirmDays, &sa.Profile.TradeoffDays, &sa.Profile.ReleaseFrequency, &sa.Version, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return SchedulingAgreementDetail{}, notFound(err, CodeSchedulingAgreementNotFound, "scheduling agreement not found")
	}
	sa.ValidFrom, sa.ValidTo = day(from), dayOrNil(to)
	rows, err := tx.Query(ctx, `select line_no, delivery_date, qty, received_qty, coalesce(reduction_reason, '')
		from purchase_sa_schedule_line where tenant_id = $1 and agreement_id = $2 order by line_no`, p.TenantID, id)
	if err != nil {
		return SchedulingAgreementDetail{}, err
	}
	sa.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ScheduleLine, error) {
		var l domain.ScheduleLine
		var d time.Time
		err := row.Scan(&l.LineNo, &d, &l.Qty, &l.ReceivedQty, &l.ReductionReason)
		l.DeliveryDate = day(d)
		return l, err
	})
	return out, err
}

func upsertScheduleLine(ctx context.Context, tx pgx.Tx, agreementID string, l domain.ScheduleLine) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `insert into purchase_sa_schedule_line
		(tenant_id, agreement_id, line_no, delivery_date, qty, received_qty, reduction_reason)
		values ($1, $2, $3, $4, $5, $6, $7)
		on conflict (tenant_id, agreement_id, line_no) do update set
		  delivery_date = excluded.delivery_date, qty = excluded.qty, reduction_reason = excluded.reduction_reason`,
		p.TenantID, agreementID, l.LineNo, l.DeliveryDate.Time, l.Qty, l.ReceivedQty, nullable(l.ReductionReason))
	return err
}

const saReleaseColumns = `id, agreement_id, release_no, number, kind, run_date, agreement_version, cumulative_reset,
	cumulative_released, cumulative_received`

func scanSARelease(row pgx.Row) (domain.SARelease, error) {
	var r domain.SARelease
	var run time.Time
	err := row.Scan(&r.ID, &r.AgreementID, &r.ReleaseNo, &r.Number, &r.Kind, &run, &r.AgreementVersion, &r.CumulativeReset,
		&r.CumulativeReleased, &r.CumulativeReceived)
	r.RunDate = day(run)
	return r, err
}

func insertSARelease(ctx context.Context, tx pgx.Tx, rel domain.SARelease) error {
	p, _ := tenant.FromContext(ctx)
	if _, err := tx.Exec(ctx, `insert into purchase_sa_release
		(tenant_id, id, agreement_id, release_no, number, kind, run_date, agreement_version, cumulative_reset,
		 cumulative_released, cumulative_received)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		p.TenantID, rel.ID, rel.AgreementID, rel.ReleaseNo, rel.Number, rel.Kind, rel.RunDate.Time, rel.AgreementVersion,
		rel.CumulativeReset, rel.CumulativeReleased, rel.CumulativeReceived); err != nil {
		return err
	}
	for _, l := range rel.Lines {
		if _, err := tx.Exec(ctx, `insert into purchase_sa_release_line
			(tenant_id, release_id, line_no, schedule_line_no, delivery_date, zone, in_horizon, qty, cumulative_released, cumulative_received)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.TenantID, rel.ID, l.LineNo, l.ScheduleLineNo, l.DeliveryDate.Time, l.Zone, l.InHorizon, l.Qty,
			l.CumulativeReleased, l.CumulativeReceived); err != nil {
			return err
		}
	}
	return nil
}

// loadSARelease reads a release header and its lines by release id.
func loadSARelease(ctx context.Context, tx pgx.Tx, releaseID string) (domain.SARelease, error) {
	p, _ := tenant.FromContext(ctx)
	rel, err := scanSARelease(tx.QueryRow(ctx, `select `+saReleaseColumns+` from purchase_sa_release where tenant_id = $1 and id = $2`,
		p.TenantID, releaseID))
	if err != nil {
		return domain.SARelease{}, notFound(err, CodeSAReleaseNotFound, "release not found")
	}
	rows, err := tx.Query(ctx, `select line_no, schedule_line_no, delivery_date, zone, in_horizon, qty, cumulative_released, cumulative_received
		from purchase_sa_release_line where tenant_id = $1 and release_id = $2 order by line_no`, p.TenantID, releaseID)
	if err != nil {
		return domain.SARelease{}, err
	}
	rel.Lines, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.SAReleaseLine, error) {
		var l domain.SAReleaseLine
		var d time.Time
		err := row.Scan(&l.LineNo, &l.ScheduleLineNo, &d, &l.Zone, &l.InHorizon, &l.Qty, &l.CumulativeReleased, &l.CumulativeReceived)
		l.DeliveryDate = day(d)
		return l, err
	})
	return rel, err
}

// saReleaseEvent builds the sarelease.created.v1 payload. Quantities are decimal strings.
func saReleaseEvent(sa domain.SchedulingAgreement, rel domain.SARelease) purchasingevents.SAReleaseCreatedData {
	data := purchasingevents.SAReleaseCreatedData{
		AgreementID: sa.ID, AgreementNumber: sa.Number, LegalEntityID: sa.LegalEntityID,
		ReleaseNumber: rel.Number, ReleaseNo: rel.ReleaseNo, Kind: rel.Kind, RunDate: rel.RunDate.String(),
		VendorID: sa.VendorID, ProductID: sa.ProductID, SiteID: sa.SiteID, Unit: sa.Unit, CumulativeReset: rel.CumulativeReset,
		CumulativeReleased: rel.CumulativeReleased.String(), CumulativeReceived: rel.CumulativeReceived.String(),
		Lines: make([]purchasingevents.SAReleaseLine, 0, len(rel.Lines)),
	}
	for _, l := range rel.Lines {
		data.Lines = append(data.Lines, purchasingevents.SAReleaseLine{
			LineNo: l.LineNo, ScheduleLineNo: l.ScheduleLineNo, ProductID: sa.ProductID, DeliveryDate: l.DeliveryDate.String(),
			Qty: l.Qty.String(), CumulativeReleased: l.CumulativeReleased.String(), CumulativeReceived: l.CumulativeReceived.String(),
			Zone: l.Zone, InHorizon: l.InHorizon,
		})
	}
	return data
}
