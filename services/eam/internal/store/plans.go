package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
)

// PlanLineInput is one maintenance plan line (FR-44.4). Optional fields are
// pointers so that zero values are not mistaken for omissions.
type PlanLineInput struct {
	AssetID           string     `json:"assetId"`
	Kind              string     `json:"kind"`
	IntervalValue     *int       `json:"intervalValue,omitempty"`
	IntervalUnit      *string    `json:"intervalUnit,omitempty"`
	CounterID         *string    `json:"counterId,omitempty"`
	CounterInterval   *float64   `json:"counterInterval,omitempty"`
	JobType           string     `json:"jobType"`
	EarlyTolerancePct *int       `json:"earlyTolerancePct,omitempty"`
	HorizonDays       *int       `json:"horizonDays,omitempty"`
	StartAt           *time.Time `json:"startAt,omitempty"`
	// LastCompletedAt and LastCompletedReading carry the last completion over
	// from a migrated plan (8.44.11 "last completed from WO history").
	LastCompletedAt      *time.Time `json:"lastCompletedAt,omitempty"`
	LastCompletedReading *float64   `json:"lastCompletedReading,omitempty"`
}

// PlanInput is a plan header with its lines.
type PlanInput struct {
	Code     string          `json:"code"`
	Name     string          `json:"name"`
	Currency string          `json:"currency"`
	Lines    []PlanLineInput `json:"lines"`
}

// Plan is the stored plan with its line ids.
type Plan struct {
	ID       string   `json:"id"`
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Currency string   `json:"currency"`
	LineIDs  []string `json:"lineIds"`
}

// ScheduleResult summarises one scheduler run (EAM-PLN-002).
type ScheduleResult struct {
	Evaluated int `json:"evaluated"`
	Generated int `json:"generated"`
	Skipped   int `json:"skipped"`
}

// validateLine checks a line against the plan line rules before it is stored.
func validateLine(in PlanLineInput, start, now time.Time) error {
	if in.AssetID == "" || in.JobType == "" {
		return problem(422, CodeLineInvalid, "plan line needs an asset and a job type")
	}
	kind := domain.Kind(in.Kind)
	needsTime := kind == domain.KindTime || kind == domain.KindTimeOrCounter || kind == domain.KindTimeAndCounter
	needsCounter := kind == domain.KindCounter || kind == domain.KindTimeOrCounter || kind == domain.KindTimeAndCounter
	if !needsTime && !needsCounter {
		return problem(422, CodeLineInvalid, "unknown plan line kind "+in.Kind)
	}
	if needsTime && (in.IntervalValue == nil || in.IntervalUnit == nil) {
		return problem(422, CodeLineInvalid, "time-based lines need intervalValue and intervalUnit")
	}
	if needsCounter && (in.CounterID == nil || in.CounterInterval == nil) {
		return problem(422, CodeLineInvalid, "counter-based lines need counterId and counterInterval")
	}
	if in.EarlyTolerancePct != nil && (*in.EarlyTolerancePct < 0 || *in.EarlyTolerancePct > 100) {
		return problem(422, CodeLineInvalid, "earlyTolerancePct must be 0-100")
	}
	if in.HorizonDays != nil && (*in.HorizonDays < 1 || *in.HorizonDays > 730) {
		return problem(422, CodeLineInvalid, "horizonDays must be 1-730")
	}
	if in.LastCompletedReading != nil && (!needsCounter || *in.LastCompletedReading < 0) {
		return problem(422, CodeLineInvalid, "lastCompletedReading needs a counter-based line and must not be negative")
	}
	if in.LastCompletedAt != nil && in.LastCompletedAt.After(now) {
		return problem(422, CodeLineInvalid, "lastCompletedAt must not be in the future")
	}
	_, err := domain.Compute(lineToDomain(in, start, nil, 0), domain.CounterState{}, start)
	if err != nil {
		return problem(422, CodeLineInvalid, err.Error())
	}
	return nil
}

func deref[T any](p *T, zero T) T {
	if p == nil {
		return zero
	}
	return *p
}

func lineToDomain(in PlanLineInput, start time.Time, lastAt *time.Time, lastReading float64) domain.PlanLine {
	return domain.PlanLine{
		Kind:                 domain.Kind(in.Kind),
		Interval:             domain.Interval{N: deref(in.IntervalValue, 0), Unit: deref(in.IntervalUnit, "")},
		CounterInterval:      deref(in.CounterInterval, 0),
		Start:                start,
		LastCompletedAt:      lastAt,
		LastCompletedReading: lastReading,
	}
}

// CreatePlan stores a plan and its lines, then schedules the new lines at once.
func (s *Postgres) CreatePlan(ctx context.Context, in PlanInput, key string) (Plan, error) {
	now := s.now()
	for _, l := range in.Lines {
		if err := validateLine(l, deref(l.StartAt, now), now); err != nil {
			return Plan{}, err
		}
	}
	var out Plan
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "maintenance_plan", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getPlan(ctx, tx, t, id)
			return gerr
		}
		if dup, err := taken(ctx, tx, "maintenance_plan", "tenant_id = $1 and code = $2", t, in.Code); err != nil || dup {
			if err != nil {
				return err
			}
			return problem(409, CodeDuplicate, "a plan with code "+in.Code+" already exists")
		}
		planID := ids.New()
		if _, err := tx.Exec(ctx, `insert into maintenance_plan (tenant_id, id, code, name, currency, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6)`, t, planID, in.Code, in.Name, in.Currency, optStr(key)); err != nil {
			return err
		}
		out = Plan{ID: planID, Code: in.Code, Name: in.Name, Currency: in.Currency}
		for _, l := range in.Lines {
			if _, err := getAsset(ctx, tx, t, l.AssetID, false); err != nil {
				return err
			}
			if l.CounterID != nil {
				var owner string
				err := tx.QueryRow(ctx, `select asset_id::text from counter where tenant_id = $1 and id = $2`, t, *l.CounterID).Scan(&owner)
				if err != nil {
					return notFound(err, "counter")
				}
				if owner != l.AssetID {
					return problem(422, CodeLineInvalid, "counter does not belong to the line's asset")
				}
			}
			lineID := ids.New()
			start := deref(l.StartAt, now)
			if _, err := tx.Exec(ctx, `insert into plan_line (tenant_id, id, plan_id, asset_id, line_type, interval_value,
			    interval_unit, counter_id, counter_interval, job_type, early_tolerance_pct, late_tolerance_pct,
			    horizon_days, start_at, last_completed_at, last_completed_reading)
			  values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11, 10, $12, $13, $14, $15::numeric)`,
				t, lineID, planID, l.AssetID, l.Kind, l.IntervalValue, l.IntervalUnit, l.CounterID,
				numPtr(l.CounterInterval), l.JobType, deref(l.EarlyTolerancePct, 10), deref(l.HorizonDays, 90), start,
				l.LastCompletedAt, numText(deref(l.LastCompletedReading, 0))); err != nil {
				return err
			}
			out.LineIDs = append(out.LineIDs, lineID)
		}
		_, err := scheduleLines(ctx, tx, t, "and pl.plan_id = $2", []any{planID}, now)
		return err
	})
	return out, err
}

func getPlan(ctx context.Context, tx pgx.Tx, t, id string) (Plan, error) {
	var p Plan
	err := tx.QueryRow(ctx, `select id::text, code, name, currency from maintenance_plan
	  where tenant_id = $1 and id = $2`, t, id).Scan(&p.ID, &p.Code, &p.Name, &p.Currency)
	if err != nil {
		return p, notFound(err, "plan")
	}
	rows, err := tx.Query(ctx, `select id::text from plan_line where tenant_id = $1 and plan_id = $2 order by id`, t, id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var lid string
		if err := rows.Scan(&lid); err != nil {
			return p, err
		}
		p.LineIDs = append(p.LineIDs, lid)
	}
	return p, rows.Err()
}

// PlanLine is a stored plan line with its computed next due point (FR-44.4, BR-44.1).
type PlanLine struct {
	ID                   string     `json:"id"`
	AssetID              string     `json:"assetId"`
	Kind                 string     `json:"kind"`
	IntervalValue        *int       `json:"intervalValue,omitempty"`
	IntervalUnit         *string    `json:"intervalUnit,omitempty"`
	CounterID            *string    `json:"counterId,omitempty"`
	CounterInterval      *float64   `json:"counterInterval,omitempty"`
	JobType              string     `json:"jobType"`
	EarlyTolerancePct    int        `json:"earlyTolerancePct"`
	HorizonDays          int        `json:"horizonDays"`
	StartAt              time.Time  `json:"startAt"`
	LastCompletedAt      *time.Time `json:"lastCompletedAt,omitempty"`
	LastCompletedReading float64    `json:"lastCompletedReading"`
	NextDueAt            *time.Time `json:"nextDueAt,omitempty"`
	NextDueReading       *float64   `json:"nextDueReading,omitempty"`
	NextDueForecastAt    *time.Time `json:"nextDueForecastAt,omitempty"`
}

// PlanView is a plan with its lines (Maintenance plan editor, scheduling overview).
type PlanView struct {
	ID       string     `json:"id"`
	Code     string     `json:"code"`
	Name     string     `json:"name"`
	Currency string     `json:"currency"`
	Active   bool       `json:"active"`
	Lines    []PlanLine `json:"lines"`
}

// ListPlans returns a page of plans, newest first, each with its lines.
// With opt.AssetID only plans that have a line on that asset are returned.
func (s *Postgres) ListPlans(ctx context.Context, opt ListOptions) (Page[PlanView], error) {
	var out Page[PlanView]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select p.id::text, p.code, p.name, p.currency, p.active from maintenance_plan p
		  where p.tenant_id = $1 and ($2::uuid is null or p.id < $2)
		    and ($3::uuid is null or exists (select 1 from plan_line pl
		      where pl.tenant_id = p.tenant_id and pl.plan_id = p.id and pl.asset_id = $3))
		  order by p.id desc limit $4`, t, optStr(opt.Cursor), optStr(opt.AssetID), opt.Limit+1)
		if err != nil {
			return err
		}
		var plans []PlanView
		for rows.Next() {
			p := PlanView{Lines: []PlanLine{}}
			if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.Currency, &p.Active); err != nil {
				rows.Close()
				return err
			}
			plans = append(plans, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(plans, opt.Limit, func(p PlanView) string { return p.ID })
		for i := range out.Items {
			if out.Items[i].Lines, err = planLines(ctx, tx, t, out.Items[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func planLines(ctx context.Context, tx pgx.Tx, t, planID string) ([]PlanLine, error) {
	rows, err := tx.Query(ctx, `select id::text, asset_id::text, line_type, interval_value, interval_unit, counter_id::text,
	    counter_interval::float8, job_type, early_tolerance_pct, horizon_days, start_at, last_completed_at,
	    last_completed_reading::float8, next_due_at, next_due_reading::float8, next_due_forecast_at
	  from plan_line where tenant_id = $1 and plan_id = $2 order by id`, t, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []PlanLine{}
	for rows.Next() {
		var l PlanLine
		if err := rows.Scan(&l.ID, &l.AssetID, &l.Kind, &l.IntervalValue, &l.IntervalUnit, &l.CounterID,
			&l.CounterInterval, &l.JobType, &l.EarlyTolerancePct, &l.HorizonDays, &l.StartAt, &l.LastCompletedAt,
			&l.LastCompletedReading, &l.NextDueAt, &l.NextDueReading, &l.NextDueForecastAt); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

// SchedulePlans runs the scheduler over every active line (nightly sweep, FR-44.5).
func (s *Postgres) SchedulePlans(ctx context.Context) (ScheduleResult, error) {
	var res ScheduleResult
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		var err error
		res, err = scheduleLines(ctx, tx, t, "", nil, s.now())
		return err
	})
	return res, err
}

type lineRow struct {
	id, assetID, kind, jobType, currency, lifecycle string
	intervalValue                                   *int
	intervalUnit, counterID                         *string
	counterInterval                                 *float64
	counterLast, usage                              float64
	startAt                                         time.Time
	lastAt                                          *time.Time
	lastReading                                     float64
	early, horizon, criticality                     int
}

// scheduleLines recomputes next due for matching lines and generates work
// orders where BR-44.1 (d) holds and no open work order exists for the line.
// extra is an optional predicate on pl that uses $2 for its first argument.
func scheduleLines(ctx context.Context, tx pgx.Tx, t, extra string, args []any, now time.Time) (ScheduleResult, error) {
	var res ScheduleResult
	lines, err := loadLines(ctx, tx, t, extra, args)
	if err != nil {
		return res, err
	}
	for _, l := range lines {
		res.Evaluated++
		sched, err := domain.Compute(lineToDomain(PlanLineInput{Kind: l.kind,
			IntervalValue: l.intervalValue, IntervalUnit: l.intervalUnit, CounterInterval: l.counterInterval},
			l.startAt, l.lastAt, l.lastReading), domain.CounterState{Last: l.counterLast, AvgDailyUsage: l.usage}, now)
		if err != nil {
			return res, problem(422, CodeLineInvalid, err.Error())
		}
		if _, err := tx.Exec(ctx, `update plan_line set next_due_at = $3, next_due_reading = $4::numeric,
		    next_due_forecast_at = $5 where tenant_id = $1 and id = $2`,
			t, l.id, sched.NextDueAt, numPtr(sched.NextDueReading), sched.ForecastAt); err != nil {
			return res, err
		}
		if !domain.Generate(sched, l.early, l.horizon, now) {
			continue
		}
		if l.lifecycle != "ACTIVE" {
			res.Skipped++
			continue
		}
		var open bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from work_order where tenant_id = $1 and plan_line_id = $2
		    and status not in ('COMPLETED', 'CLOSED', 'CANCELED'))`, t, l.id).Scan(&open); err != nil {
			return res, err
		}
		if open {
			res.Skipped++
			continue
		}
		lineID := l.id
		woID, err := insertWorkOrder(ctx, tx, t, WorkOrder{AssetID: l.assetID, Type: "PREVENTIVE", Status: "CREATED",
			Priority: domain.DefaultPriority(l.criticality, false), Currency: l.currency, PlanLineID: &lineID,
			JobType: l.jobType, DueAt: sched.NextDueAt}, "")
		if err != nil {
			return res, err
		}
		if err := enqueue(ctx, tx, t, "workorder", woID, eamevents.WorkOrderCreated, eamevents.WorkOrderCreatedData{
			WorkOrderID: woID, AssetID: l.assetID, Type: "PREVENTIVE", PlanLineID: l.id}); err != nil {
			return res, err
		}
		res.Generated++
	}
	return res, nil
}

func loadLines(ctx context.Context, tx pgx.Tx, t, extra string, args []any) ([]lineRow, error) {
	q := `select pl.id::text, pl.asset_id::text, pl.line_type, pl.interval_value, pl.interval_unit, pl.counter_id::text,
	    pl.counter_interval::float8, coalesce(c.last_reading, 0)::float8, coalesce(c.avg_daily_usage, 0)::float8,
	    pl.start_at, pl.last_completed_at, pl.last_completed_reading::float8, pl.early_tolerance_pct, pl.horizon_days,
	    pl.job_type, p.currency, a.criticality, a.lifecycle_state
	  from plan_line pl
	  join maintenance_plan p on p.tenant_id = pl.tenant_id and p.id = pl.plan_id
	  join asset a on a.tenant_id = pl.tenant_id and a.id = pl.asset_id
	  left join counter c on c.tenant_id = pl.tenant_id and c.id = pl.counter_id
	  where pl.tenant_id = $1 and p.active and pl.active ` + extra + ` order by pl.id
	  for update of pl`
	rows, err := tx.Query(ctx, q, append([]any{t}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lineRow
	for rows.Next() {
		var l lineRow
		if err := rows.Scan(&l.id, &l.assetID, &l.kind, &l.intervalValue, &l.intervalUnit, &l.counterID,
			&l.counterInterval, &l.counterLast, &l.usage, &l.startAt, &l.lastAt, &l.lastReading, &l.early,
			&l.horizon, &l.jobType, &l.currency, &l.criticality, &l.lifecycle); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
