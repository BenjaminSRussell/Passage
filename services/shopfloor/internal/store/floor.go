package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Catalog codes for machines and downtime (see README).
const (
	CodeMachineNotFound  = "SF-0108"
	CodeMachineExists    = "SF-0109"
	CodeDowntimeNotFound = "SF-0110"
)

// Machine maps an edge machine code to a resource (FR-25.9, FR-25.19 config).
type Machine struct {
	ID                string          `json:"id"`
	Code              string          `json:"machineId"`
	ResourceID        string          `json:"resourceId"`
	SiteID            *string         `json:"siteId"`
	IdealCycleSeconds decimal.Decimal `json:"idealCycleSeconds"`
	// IdleThresholdSeconds is how long RUNNING may go without a count before the
	// gap is IDLE (BR-25.6). Zero on registration means the default, 300.
	IdleThresholdSeconds int       `json:"idleThresholdSeconds"`
	LastSeq              int64     `json:"lastSeq"`
	CreatedAt            time.Time `json:"createdAt"`
}

// MachineEventIn is one edge event addressed to a machine code.
type MachineEventIn struct {
	MachineCode      string
	Event            domain.MachineEvent
	ReasonCode       *string
	JobID            *string
	MeasurementName  *string
	MeasurementValue *decimal.Decimal
	Unit             *string
}

// GapReport names a missing sequence run on one machine (SF-0008, a warning).
type GapReport struct {
	MachineID string `json:"machineId"`
	From      int64  `json:"from"`
	To        int64  `json:"to"`
}

// IngestResult summarizes one ingest batch.
type IngestResult struct {
	Accepted      int         `json:"accepted"`
	Dropped       int         `json:"dropped"`
	StaleDropped  int         `json:"staleDropped"`
	Gaps          []GapReport `json:"gaps"`
	AndonsRaised  int         `json:"andonsRaised"`
	DowntimesOpen int         `json:"downtimesOpen"`
}

// Downtime is one machine stop (FR-25.11).
type Downtime struct {
	ID          string     `json:"id"`
	MachineID   string     `json:"machineId"`
	ResourceID  string     `json:"resourceId"`
	StartedAt   time.Time  `json:"startedAt"`
	EndedAt     *time.Time `json:"endedAt"`
	ReasonCode  *string    `json:"reasonCode"`
	Category    string     `json:"category"`
	AndonRaised bool       `json:"andonRaised"`
}

// DowntimeFilter selects downtimes for the supervisor list.
type DowntimeFilter struct {
	OpenOnly   bool
	ResourceID string
	Limit      int
}

// Andon is an open alert (BR-25.10).
type Andon struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resourceId"`
	SiteID     *string   `json:"siteId"`
	Kind       string    `json:"kind"`
	Severity   string    `json:"severity"`
	DowntimeID *string   `json:"downtimeId"`
	StartedAt  time.Time `json:"startedAt"`
}

const machineColumns = `id::text, code, resource_id::text, site_id::text, ideal_cycle_s::text, idle_threshold_s, last_seq, created_at`

func scanMachine(row pgx.Row) (Machine, error) {
	var m Machine
	var ideal string
	if err := row.Scan(&m.ID, &m.Code, &m.ResourceID, &m.SiteID, &ideal, &m.IdleThresholdSeconds, &m.LastSeq, &m.CreatedAt); err != nil {
		return Machine{}, err
	}
	var err error
	m.IdealCycleSeconds, err = decimal.NewFromString(ideal)
	return m, err
}

// RegisterMachine maps a machine code to a resource and stores the outcome
// under the Idempotency-Key. One machine per resource in this slice; a second
// registration of either the code or the resource is SF-0109.
func (s *Postgres) RegisterMachine(ctx context.Context, in Machine, idem Idem) (Machine, error) {
	p, _ := tenant.FromContext(ctx)
	if in.IdleThresholdSeconds == 0 {
		in.IdleThresholdSeconds = int(domain.DefaultIdleThreshold / time.Second)
	}
	var out Machine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanMachine(tx.QueryRow(ctx, `insert into machine (tenant_id, id, code, resource_id, site_id, ideal_cycle_s, idle_threshold_s)
			values ($1, $2, $3, $4, $5, $6, $7) returning `+machineColumns,
			p.TenantID, ids.New(), in.Code, in.ResourceID, in.SiteID, in.IdealCycleSeconds.String(), in.IdleThresholdSeconds))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return httpx.NewProblem(http.StatusConflict, CodeMachineExists, "machine code or resource is already registered")
		}
		if err != nil {
			return err
		}
		if idem.Key == "" {
			return nil
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// ListMachines returns registered machines ordered by code, at most limit rows.
func (s *Postgres) ListMachines(ctx context.Context, limit int) ([]Machine, error) {
	out := []Machine{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+machineColumns+` from machine order by code, id limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMachine(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// IngestMachineEvents stores accepted events per machine in one transaction.
// Each machine row is locked so sequence checks serialize. A DOWN report opens
// a downtime and any other state closes it; a downtime open longer than
// AndonAfter at now raises one andon per (resource, kind); an andon whose
// downtime ended at least AndonClearAfter before now is cleared. An unknown
// machine code rejects the whole batch (SF-0108).
func (s *Postgres) IngestMachineEvents(ctx context.Context, now time.Time, items []MachineEventIn) (IngestResult, error) {
	p, _ := tenant.FromContext(ctx)
	res := IngestResult{Gaps: []GapReport{}}
	groups := map[string][]MachineEventIn{}
	var order []string
	for _, it := range items {
		if _, seen := groups[it.MachineCode]; !seen {
			order = append(order, it.MachineCode)
		}
		groups[it.MachineCode] = append(groups[it.MachineCode], it)
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, code := range order {
			r, err := ingestMachine(ctx, tx, p.TenantID, now, code, groups[code])
			if err != nil {
				return err
			}
			res.Accepted += r.Accepted
			res.Dropped += r.Dropped
			res.StaleDropped += r.StaleDropped
			res.Gaps = append(res.Gaps, r.Gaps...)
			res.AndonsRaised += r.AndonsRaised
			res.DowntimesOpen += r.DowntimesOpen
		}
		return nil
	})
	if err != nil {
		return IngestResult{}, err
	}
	return res, nil
}

type machineRow struct {
	id         string
	resourceID string
	siteID     *string
	lastSeq    int64
}

type openDown struct {
	id    string
	start time.Time
}

func ingestMachine(ctx context.Context, tx pgx.Tx, tenantID string, now time.Time, code string, group []MachineEventIn) (IngestResult, error) {
	var m machineRow
	err := tx.QueryRow(ctx, `select id::text, resource_id::text, site_id::text, last_seq from machine
		where tenant_id = $1 and code = $2 for update`, tenantID, code).
		Scan(&m.id, &m.resourceID, &m.siteID, &m.lastSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return IngestResult{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeMachineNotFound,
			"machine "+code+" is not registered")
	}
	if err != nil {
		return IngestResult{}, err
	}

	evts := make([]domain.MachineEvent, len(group))
	for i, it := range group {
		evts[i] = it.Event
		evts[i].Index = i
	}
	c := domain.Classify(m.lastSeq, now, evts)
	res := IngestResult{Dropped: c.Dropped, StaleDropped: c.Stale}
	for _, g := range c.Gaps {
		res.Gaps = append(res.Gaps, GapReport{MachineID: code, From: g.From, To: g.To})
	}

	// A late event (seq at or below last_seq) is a duplicate only when its seq
	// is stored; otherwise it fills a gap and is kept, so a replayed gap or a
	// batch that commits after a later one loses nothing. It feeds counts and
	// OEE (computed from the events), not the downtime record, which follows
	// the newest state.
	for _, e := range c.Late {
		it := group[e.Index]
		tag, err := tx.Exec(ctx, `insert into machine_event (tenant_id, machine_id, seq, kind, ts, good_count, reject_count,
				state, reason_code, measurement_name, measurement_value, unit, job_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			on conflict (tenant_id, machine_id, seq) do nothing`,
			tenantID, m.id, e.Seq, e.Kind, e.At, e.GoodCount, e.RejectCount, nullable(e.State),
			it.ReasonCode, it.MeasurementName, measurementString(it.MeasurementValue), it.Unit, it.JobID)
		if err != nil {
			return IngestResult{}, err
		}
		if tag.RowsAffected() == 1 {
			res.Accepted++
		} else {
			res.Dropped++
		}
	}

	var open *openDown
	var d openDown
	err = tx.QueryRow(ctx, `select id::text, started_at from downtime_record
		where tenant_id = $1 and machine_id = $2 and ended_at is null`, tenantID, m.id).Scan(&d.id, &d.start)
	switch {
	case err == nil:
		open = &d
	case !errors.Is(err, pgx.ErrNoRows):
		return IngestResult{}, err
	}

	for _, e := range c.Accepted {
		it := group[e.Index]
		if _, err := tx.Exec(ctx, `insert into machine_event (tenant_id, machine_id, seq, kind, ts, good_count, reject_count,
				state, reason_code, measurement_name, measurement_value, unit, job_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			tenantID, m.id, e.Seq, e.Kind, e.At, e.GoodCount, e.RejectCount, nullable(e.State),
			it.ReasonCode, it.MeasurementName, measurementString(it.MeasurementValue), it.Unit, it.JobID); err != nil {
			return IngestResult{}, err
		}
		res.Accepted++
		switch domain.DowntimeStepFor(open != nil, e.State) {
		case domain.DowntimeStart:
			nd := openDown{id: ids.New(), start: e.At}
			if _, err := tx.Exec(ctx, `insert into downtime_record (tenant_id, id, machine_id, resource_id, started_at, reason_code)
				values ($1, $2, $3, $4, $5, $6)`, tenantID, nd.id, m.id, m.resourceID, e.At, it.ReasonCode); err != nil {
				return IngestResult{}, err
			}
			open = &nd
		case domain.DowntimeEnd:
			// An event stamped before the downtime start (device clock skew)
			// closes it at its start rather than breaking ended_at >= started_at.
			end := e.At
			if end.Before(open.start) {
				end = open.start
			}
			if _, err := tx.Exec(ctx, `update downtime_record set ended_at = $3 where tenant_id = $1 and id = $2`,
				tenantID, open.id, end); err != nil {
				return IngestResult{}, err
			}
			open = nil
		}
	}

	if len(c.Accepted) > 0 {
		last := c.Accepted[len(c.Accepted)-1].Seq
		if _, err := tx.Exec(ctx, `update machine set last_seq = $3 where tenant_id = $1 and id = $2`,
			tenantID, m.id, last); err != nil {
			return IngestResult{}, err
		}
	}
	if open != nil {
		res.DowntimesOpen = 1
		if domain.AndonDue(open.start, now) {
			raised, err := raiseAndon(ctx, tx, tenantID, m, open)
			if err != nil {
				return IngestResult{}, err
			}
			res.AndonsRaised += raised
		}
	}
	if err := clearAndon(ctx, tx, tenantID, m.resourceID, now); err != nil {
		return IngestResult{}, err
	}
	return res, nil
}

// raiseAndon opens one alert per (resource, kind) (BR-25.10) and emits
// dev.passage.shopfloor.andon.raised.v1 in the same transaction. While an alert
// for the resource is open a second raise is a no-op and returns 0.
func raiseAndon(ctx context.Context, tx pgx.Tx, tenantID string, m machineRow, d *openDown) (int, error) {
	alertID := ids.New()
	var got string
	err := tx.QueryRow(ctx, `insert into andon_alert (tenant_id, id, site_id, resource_id, kind, severity, downtime_id, started_at)
		values ($1, $2, $3, $4, 'downtime', 'warning', $5, $6)
		on conflict (tenant_id, resource_id, kind) where cleared_at is null do nothing
		returning id::text`, tenantID, alertID, m.siteID, m.resourceID, d.id, d.start).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `update downtime_record set andon_raised = true where tenant_id = $1 and id = $2`,
		tenantID, d.id); err != nil {
		return 0, err
	}
	if err := enqueue(ctx, tx, "andon", alertID, sfevents.AndonRaised, tenantID, sfevents.AndonRaisedData{
		AlertID: alertID, ResourceID: m.resourceID, Kind: "downtime", Severity: "warning",
		StartedAt: d.start.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return 0, err
	}
	return 1, nil
}

// clearAndon closes a resource's open downtime andon once its downtime ended at
// least AndonClearAfter before now and no downtime is open on the resource
// (BR-25.10). Clearing happens on the next ingest for the machine after the
// hold has elapsed; a scheduled sweep is a later slice.
func clearAndon(ctx context.Context, tx pgx.Tx, tenantID, resourceID string, now time.Time) error {
	_, err := tx.Exec(ctx, `update andon_alert a set cleared_at = greatest($3, a.started_at)
		where a.tenant_id = $1 and a.resource_id = $2 and a.kind = 'downtime' and a.cleared_at is null
		and not exists (select 1 from downtime_record o where o.tenant_id = $1 and o.resource_id = $2 and o.ended_at is null)
		and exists (select 1 from downtime_record d where d.tenant_id = $1 and d.id = a.downtime_id and d.ended_at <= $4)`,
		tenantID, resourceID, now, now.Add(-domain.AndonClearAfter))
	return err
}

const downtimeColumns = `id::text, machine_id::text, resource_id::text, started_at, ended_at, reason_code, category, andon_raised`

func scanDowntime(row pgx.Row) (Downtime, error) {
	var d Downtime
	err := row.Scan(&d.ID, &d.MachineID, &d.ResourceID, &d.StartedAt, &d.EndedAt, &d.ReasonCode, &d.Category, &d.AndonRaised)
	return d, err
}

// CodeDowntime sets the reason code (and optionally the category) of a downtime
// and stores the outcome under the Idempotency-Key. The andon stays open until
// its condition clears (AC-25.6). An unknown id is SF-0110.
func (s *Postgres) CodeDowntime(ctx context.Context, id, reason, category string, idem Idem) (Downtime, error) {
	p, _ := tenant.FromContext(ctx)
	var out Downtime
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanDowntime(tx.QueryRow(ctx, `update downtime_record
			set reason_code = $3, category = coalesce(nullif($4, ''), category)
			where tenant_id = $1 and id = $2 returning `+downtimeColumns, p.TenantID, id, reason, category))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeDowntimeNotFound, "downtime not found")
		}
		if err != nil {
			return err
		}
		if idem.Key == "" {
			return nil
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// ListDowntimes returns downtimes, newest first, at most f.Limit rows.
func (s *Postgres) ListDowntimes(ctx context.Context, f DowntimeFilter) ([]Downtime, error) {
	out := []Downtime{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+downtimeColumns+` from downtime_record
			where ($1 = false or ended_at is null) and ($2 = '' or resource_id::text = $2)
			order by started_at desc, id limit $3`, f.OpenOnly, f.ResourceID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDowntime(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// OpenAndon lists open alerts, oldest first, optionally for one site.
func (s *Postgres) OpenAndon(ctx context.Context, siteID string) ([]Andon, error) {
	out := []Andon{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, resource_id::text, site_id::text, kind, severity, downtime_id::text, started_at
			from andon_alert where cleared_at is null and ($1 = '' or site_id::text = $1)
			order by started_at, id`, siteID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Andon
			if err := rows.Scan(&a.ID, &a.ResourceID, &a.SiteID, &a.Kind, &a.Severity, &a.DowntimeID, &a.StartedAt); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// LoadOEEStreams reads, per resource with a machine, the machine's ideal cycle
// and idle threshold, its events in [from - look-back, to), the state in effect
// at the start of that history, the downtimes coded as planned that overlap the
// window and the first count or state event at or after to (the Horizon, zero
// when there is none yet). The look-back is the longest idle threshold of the
// machines read, so idle detection (BR-25.6) knows the last count before the
// window. Resources without a machine are absent.
func (s *Postgres) LoadOEEStreams(ctx context.Context, resourceIDs []string, from, to time.Time) (map[string]domain.OEEStream, error) {
	out := map[string]domain.OEEStream{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		resourceOf := map[string]string{}
		var machineIDs []string
		var lookBack time.Duration
		rows, err := tx.Query(ctx, `select resource_id::text, id::text, ideal_cycle_s::text, idle_threshold_s from machine
			where resource_id = any($1::uuid[])`, resourceIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var rid, mid, ideal string
			var idle int
			if err := rows.Scan(&rid, &mid, &ideal, &idle); err != nil {
				rows.Close()
				return err
			}
			d, err := decimal.NewFromString(ideal)
			if err != nil {
				rows.Close()
				return err
			}
			threshold := time.Duration(idle) * time.Second
			lookBack = max(lookBack, threshold)
			resourceOf[mid] = rid
			machineIDs = append(machineIDs, mid)
			out[rid] = domain.OEEStream{IdealCycleSeconds: d, IdleThreshold: threshold}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(machineIDs) == 0 {
			return nil
		}
		historyFrom := from.Add(-lookBack)
		for rid, st := range out {
			st.HistoryFrom = historyFrom
			out[rid] = st
		}

		// One indexed probe per machine on (tenant_id, machine_id, ts): the state in
		// effect at the start of the history and the first count or state event at
		// or after the window end.
		edges, err := tx.Query(ctx, `select m.id::text,
				(select e.state from machine_event e where e.machine_id = m.id and e.ts < $2 and e.state is not null
					order by e.ts desc, e.seq desc limit 1),
				(select e.ts from machine_event e where e.machine_id = m.id and e.ts >= $3 and (e.kind = 'count' or e.state is not null)
					order by e.ts, e.seq limit 1)
			from unnest($1::uuid[]) as m(id)`, machineIDs, historyFrom, to)
		if err != nil {
			return err
		}
		for edges.Next() {
			var mid string
			var state *string
			var horizon *time.Time
			if err := edges.Scan(&mid, &state, &horizon); err != nil {
				edges.Close()
				return err
			}
			st := out[resourceOf[mid]]
			if state != nil {
				st.Initial = *state
			}
			if horizon != nil {
				st.Horizon = *horizon
			}
			out[resourceOf[mid]] = st
		}
		edges.Close()
		if err := edges.Err(); err != nil {
			return err
		}

		planned, err := tx.Query(ctx, `select machine_id::text, started_at, coalesce(ended_at, $3) from downtime_record
			where machine_id = any($1::uuid[]) and category = 'planned' and started_at < $3 and (ended_at is null or ended_at > $2)
			order by machine_id, started_at`, machineIDs, from, to)
		if err != nil {
			return err
		}
		for planned.Next() {
			var mid string
			var iv domain.Interval
			if err := planned.Scan(&mid, &iv.Start, &iv.End); err != nil {
				planned.Close()
				return err
			}
			st := out[resourceOf[mid]]
			st.PlannedDown = append(st.PlannedDown, iv)
			out[resourceOf[mid]] = st
		}
		planned.Close()
		if err := planned.Err(); err != nil {
			return err
		}

		evts, err := tx.Query(ctx, `select machine_id::text, seq, kind, ts, good_count, reject_count, coalesce(state, '')
			from machine_event where machine_id = any($1::uuid[]) and ts >= $2 and ts < $3
			order by machine_id, ts, seq`, machineIDs, historyFrom, to)
		if err != nil {
			return err
		}
		defer evts.Close()
		for evts.Next() {
			var mid string
			var e domain.MachineEvent
			if err := evts.Scan(&mid, &e.Seq, &e.Kind, &e.At, &e.GoodCount, &e.RejectCount, &e.State); err != nil {
				return err
			}
			st := out[resourceOf[mid]]
			st.Events = append(st.Events, e)
			out[resourceOf[mid]] = st
		}
		return evts.Err()
	})
	return out, err
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func measurementString(d *decimal.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}
