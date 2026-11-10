package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/optimizer"
)

const (
	CodeOptimizationUnknown = "SVC-DSP-014"
	CodeOptimizationState   = "SVC-DSP-015"
	CodeOptimizationEmpty   = "SVC-DSP-016"
)

const (
	OptPending   = "PENDING"
	OptRunning   = "RUNNING"
	OptSucceeded = "SUCCEEDED"
	OptAccepted  = "ACCEPTED"
	OptParked    = "PARKED"

	MaxOptimizationAttempts = 5
	DefaultOptimizerSeconds = 20

	maxOptimizerTechnicians = 100
	maxOptimizerOrders      = 200
	maxHeldOrders           = 300
	defaultCasePriority     = 4
	maxMinute               = 100_000
	maxTravelMinutes        = 1440
)

type Skipped struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type Rejection struct {
	WorkOrderID string `json:"workOrderId"`
	Code        string `json:"code"`
	Detail      string `json:"detail"`
}

type Applied struct {
	WorkOrderID  string    `json:"workOrderId"`
	TechnicianID string    `json:"technicianId"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
}

type Outcome struct {
	Applied  []Applied   `json:"applied"`
	Rejected []Rejection `json:"rejected"`
}

type Optimization struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	TerritoryID   string            `json:"territoryId"`
	Date          string            `json:"date"`
	TimeZone      string            `json:"timeZone"`
	Status        string            `json:"status"`
	Technicians   int               `json:"technicians"`
	WorkOrders    int               `json:"workOrders"`
	Pinned        int               `json:"pinned"`
	Skipped       []Skipped         `json:"skipped"`
	RemoteRunID   *string           `json:"remoteRunId,omitempty"`
	Result        *optimizer.Result `json:"result,omitempty"`
	Outcome       *Outcome          `json:"outcome,omitempty"`
	AttemptCount  int               `json:"attemptCount"`
	Generation    int               `json:"-"`
	UpdatedAt     time.Time         `json:"-"`
	NextAttemptAt time.Time         `json:"nextAttemptAt"`
	LastError     *string           `json:"lastError,omitempty"`
	RequestedBy   string            `json:"requestedBy"`
	AcceptedBy    *string           `json:"acceptedBy,omitempty"`
	AcceptedAt    *time.Time        `json:"acceptedAt,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
}

const optimizationColumns = `id::text, legal_entity_id::text, territory_id::text, run_date::text, time_zone, status,
	jsonb_array_length(request->'technicians'), jsonb_array_length(request->'workOrders'), jsonb_array_length(request->'pinned'),
	skipped, remote_run_id, result, outcome, attempt_count, retry_generation, updated_at, next_attempt_at, last_error, requested_by, accepted_by, accepted_at, created_at`

func scanOptimization(row pgx.Row) (Optimization, error) {
	var o Optimization
	var orders int
	var skipped, result, outcome []byte
	if err := row.Scan(&o.ID, &o.LegalEntityID, &o.TerritoryID, &o.Date, &o.TimeZone, &o.Status, &o.Technicians, &orders, &o.Pinned, &skipped,
		&o.RemoteRunID, &result, &outcome, &o.AttemptCount, &o.Generation, &o.UpdatedAt, &o.NextAttemptAt, &o.LastError, &o.RequestedBy, &o.AcceptedBy, &o.AcceptedAt, &o.CreatedAt); err != nil {
		return o, err
	}
	o.WorkOrders = orders - o.Pinned
	if err := json.Unmarshal(skipped, &o.Skipped); err != nil {
		return o, err
	}
	if result != nil {
		o.Result = &optimizer.Result{}
		if err := json.Unmarshal(result, o.Result); err != nil {
			return o, err
		}
	}
	if outcome != nil {
		o.Outcome = &Outcome{}
		return o, json.Unmarshal(outcome, o.Outcome)
	}
	return o, nil
}

func getOptimization(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Optimization, error) {
	q := `select ` + optimizationColumns + ` from service_dispatch_optimization where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	o, err := scanOptimization(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return o, httpx.NewProblem(http.StatusNotFound, CodeOptimizationUnknown, "dispatch optimization not found")
	}
	return o, err
}

type OptimizeInput struct {
	LegalEntityID string
	TerritoryID   string
	Date          string
	TimeZone      string
	TimeLimit     int
}

type plannedOrder struct {
	WorkOrder
	priority int
	due      *time.Time
}

type stop struct {
	order    optimizer.WorkOrder
	assigned optimizer.Assignment
	lat, lon float64
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func buildRequest(date, zone string, limitSeconds int, techs []Technician, open, held []plannedOrder) (optimizer.Request, []Skipped, error) {
	from, to, err := domain.DayBounds(date, zone)
	if err != nil {
		return optimizer.Request{}, nil, err
	}
	y, mo, d := from.Date()
	minute := func(t time.Time) int { return int(math.Floor(t.Sub(from).Minutes())) }
	wall := func(m int) time.Time { return time.Date(y, mo, d, 0, m, 0, 0, from.Location()) }
	weekday := int(from.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	dayEnd := minute(to)
	window := func(start, end *time.Time) (*int, *int, bool) {
		if start == nil || end == nil {
			return nil, nil, true
		}
		s, e := max(0, minute(*start)), min(maxMinute, minute(*end))
		if e <= 0 || s >= dayEnd {
			return nil, nil, false
		}
		return &s, &e, true
	}

	skipped := []Skipped{}
	type candidate struct {
		tech     optimizer.Technician
		lat, lon float64
		route    []stop
	}
	byID := map[string]*candidate{}
	var order []string
	for _, t := range techs {
		switch {
		case t.TimeZone != zone:
			skipped = append(skipped, Skipped{"TECHNICIAN", t.ID, "TIME_ZONE"})
		case !slices.Contains(t.ShiftDays, weekday):
			skipped = append(skipped, Skipped{"TECHNICIAN", t.ID, "OFF_DAY"})
		case t.HomeLatitude == nil || t.HomeLongitude == nil:
			skipped = append(skipped, Skipped{"TECHNICIAN", t.ID, "NO_HOME"})
		default:
			overtime := 0
			if t.OvertimeAllowed {
				overtime = domain.OvertimeMinutes
			}
			byID[t.ID] = &candidate{lat: *t.HomeLatitude, lon: *t.HomeLongitude, tech: optimizer.Technician{ID: t.ID, Skills: nonNil(t.Skills),
				Territories: t.TerritoryIDs, ShiftStart: minute(wall(t.ShiftStartMinute)), ShiftEnd: minute(wall(t.ShiftEndMinute + overtime)),
				MaxDailyMinutes: t.MaxDailyMinutes, OvertimeMinutes: overtime}}
			order = append(order, t.ID)
		}
	}

	stale := map[string][]string{}
	for _, h := range held {
		c := byID[*h.TechnicianID]
		if c == nil {
			continue
		}
		start, end := minute(*h.AssignedStart), minute(*h.AssignedEnd)
		s := stop{lat: c.lat, lon: c.lon, assigned: optimizer.Assignment{WorkOrderID: h.ID, TechnicianID: c.tech.ID, Start: start, End: end, Sequence: len(c.route) + 1}}
		if h.Latitude != nil && h.Longitude != nil {
			s.lat, s.lon = *h.Latitude, *h.Longitude
		}
		s.order = optimizer.WorkOrder{ID: h.ID, Territory: h.TerritoryID, Skills: nonNil(h.RequiredSkills), Duration: max(1, end-start), Priority: h.priority}
		s.order.WindowStart, s.order.WindowEnd, _ = window(h.WindowStart, h.WindowEnd)
		c.route = append(c.route, s)
		if holdsNoMore(c.tech, s) {
			stale[c.tech.ID] = append(stale[c.tech.ID], h.ID)
		}
	}
	for _, id := range order {
		c := byID[id]
		switch {
		case len(stale[id]) > 0:
			skipped = append(skipped, Skipped{"TECHNICIAN", id, "HELD_VISIT_STALE"})
			for _, orderID := range stale[id] {
				skipped = append(skipped, Skipped{"WORK_ORDER", orderID, "HELD_VISIT_STALE"})
			}
			delete(byID, id)
		case !reachable(c.tech, c.lat, c.lon, c.route):
			skipped = append(skipped, Skipped{"TECHNICIAN", id, "HELD_ROUTE_UNREACHABLE"})
			delete(byID, id)
		}
	}

	live := make([]*candidate, 0, len(order))
	for _, id := range order {
		if c := byID[id]; c != nil {
			live = append(live, c)
		}
	}
	var planned []stop
	for _, o := range open {
		ws, we, inDay := window(o.WindowStart, o.WindowEnd)
		switch {
		case o.Latitude == nil || o.Longitude == nil:
			skipped = append(skipped, Skipped{"WORK_ORDER", o.ID, "NO_LOCATION"})
			continue
		case !inDay:
			skipped = append(skipped, Skipped{"WORK_ORDER", o.ID, "WINDOW_OUTSIDE_DAY"})
			continue
		case len(live) > 0 && !slices.ContainsFunc(live, func(c *candidate) bool { return withinReach(c.lat, c.lon, *o.Latitude, *o.Longitude) }):
			skipped = append(skipped, Skipped{"WORK_ORDER", o.ID, "TOO_FAR"})
			continue
		}
		wo := optimizer.WorkOrder{ID: o.ID, Territory: o.TerritoryID, Skills: nonNil(o.RequiredSkills), Duration: o.EstDurationMinutes,
			Priority: o.priority, WindowStart: ws, WindowEnd: we}
		if o.due != nil {
			due := min(maxMinute, max(0, int(math.Ceil(o.due.Sub(from).Minutes()))))
			wo.Due = &due
		}
		planned = append(planned, stop{order: wo, lat: *o.Latitude, lon: *o.Longitude})
	}
	for _, c := range live {
		if len(c.route) == 0 && !slices.ContainsFunc(planned, func(s stop) bool { return withinReach(c.lat, c.lon, s.lat, s.lon) }) {
			skipped = append(skipped, Skipped{"TECHNICIAN", c.tech.ID, "TOO_FAR"})
			delete(byID, c.tech.ID)
		}
	}

	req := optimizer.Request{BufferMinutes: domain.BufferMinutes, TimeLimitSeconds: limitSeconds, Technicians: []optimizer.Technician{},
		WorkOrders: []optimizer.WorkOrder{}, Pinned: []optimizer.Assignment{}}
	type point struct{ lat, lon float64 }
	var points []point
	place := func(lat, lon float64) int {
		points = append(points, point{lat, lon})
		return len(points) - 1
	}
	for _, id := range order {
		c := byID[id]
		if c == nil {
			continue
		}
		c.tech.Home = place(c.lat, c.lon)
		req.Technicians = append(req.Technicians, c.tech)
		for _, s := range c.route {
			s.order.Location = place(s.lat, s.lon)
			req.WorkOrders = append(req.WorkOrders, s.order)
			req.Pinned = append(req.Pinned, s.assigned)
		}
	}
	for _, s := range planned {
		s.order.Location = place(s.lat, s.lon)
		req.WorkOrders = append(req.WorkOrders, s.order)
	}
	req.Travel = make([][]int, len(points))
	for i, a := range points {
		req.Travel[i] = make([]int, len(points))
		for j, b := range points {
			if i != j {
				req.Travel[i][j] = min(maxTravelMinutes, domain.TravelMinutes(a.lat, a.lon, b.lat, b.lon))
			}
		}
	}
	return req, skipped, nil
}

func withinReach(aLat, aLon, bLat, bLon float64) bool {
	return domain.TravelMinutes(aLat, aLon, bLat, bLon) <= maxTravelMinutes
}

func holdsNoMore(t optimizer.Technician, s stop) bool {
	o, a := s.order, s.assigned
	return !isSubset(o.Skills, t.Skills) || !slices.Contains(t.Territories, o.Territory) || a.Start < t.ShiftStart || a.End > t.ShiftEnd ||
		(o.WindowStart != nil && a.Start < *o.WindowStart) || (o.WindowEnd != nil && a.End > *o.WindowEnd)
}

func isSubset(need, have []string) bool {
	return !slices.ContainsFunc(need, func(skill string) bool { return !slices.Contains(have, skill) })
}

func reachable(t optimizer.Technician, homeLat, homeLon float64, route []stop) bool {
	prevEnd, lat, lon, load := t.ShiftStart, homeLat, homeLon, 0
	for _, s := range route {
		in := domain.TravelMinutes(lat, lon, s.lat, s.lon)
		if in > maxTravelMinutes || s.assigned.Start < prevEnd+in+domain.BufferMinutes {
			return false
		}
		load += s.order.Duration + in
		prevEnd, lat, lon = s.assigned.End, s.lat, s.lon
	}
	if len(route) > 0 {
		back := domain.TravelMinutes(lat, lon, homeLat, homeLon)
		if back > maxTravelMinutes {
			return false
		}
		load += back
	}
	return load <= t.MaxDailyMinutes+t.OvertimeMinutes
}

func optimizationInputs(ctx context.Context, tx pgx.Tx, tid string, in OptimizeInput, from, to time.Time) ([]Technician, []plannedOrder, []plannedOrder, []Skipped, error) {
	var left []Skipped
	rows, err := tx.Query(ctx, `select `+technicianColumns+` from service_technician
		where tenant_id = $1 and legal_entity_id = $2 and active and $3::uuid = any(territory_ids) order by code limit $4`,
		tid, in.LegalEntityID, in.TerritoryID, maxOptimizerTechnicians+1)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var techs []Technician
	for rows.Next() {
		t, err := scanTechnician(rows)
		if err != nil {
			rows.Close()
			return nil, nil, nil, nil, err
		}
		techs = append(techs, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, nil, err
	}
	if len(techs) > maxOptimizerTechnicians {
		left = append(left, Skipped{"TECHNICIAN", techs[maxOptimizerTechnicians].ID, "OVER_LIMIT"})
		techs = techs[:maxOptimizerTechnicians]
	}
	techIDs := make([]string, len(techs))
	for i, t := range techs {
		techIDs[i] = t.ID
	}
	open, err := orderRows(ctx, tx, `select `+workOrderColumns+` from service_work_order where tenant_id = $1 and legal_entity_id = $2
		and territory_id = $3 and status = 'UNSCHEDULED' order by created_at, id limit $4`, tid, in.LegalEntityID, in.TerritoryID, maxOptimizerOrders+1)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(open) > maxOptimizerOrders {
		left = append(left, Skipped{"WORK_ORDER", open[maxOptimizerOrders].ID, "OVER_LIMIT"})
		open = open[:maxOptimizerOrders]
	}
	held, err := orderRows(ctx, tx, `select `+workOrderColumns+` from service_work_order where tenant_id = $1 and legal_entity_id = $2
		and technician_id = any($3::uuid[]) and `+busyStatuses+` and assigned_start < $5 and assigned_end > $4
		order by technician_id::text, assigned_start, id limit $6`,
		tid, in.LegalEntityID, techIDs, from, to, maxHeldOrders+1)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(held) > maxHeldOrders {
		cut := *held[maxHeldOrders].TechnicianID
		for _, t := range techs {
			if t.ID >= cut {
				left = append(left, Skipped{"TECHNICIAN", t.ID, "OVER_LIMIT"})
			}
		}
		techs = slices.DeleteFunc(techs, func(t Technician) bool { return t.ID >= cut })
		held = slices.DeleteFunc(held[:maxHeldOrders], func(h plannedOrder) bool { return *h.TechnicianID >= cut })
	}
	if err := withCases(ctx, tx, tid, open); err != nil {
		return nil, nil, nil, nil, err
	}
	return techs, open, held, left, nil
}

func orderRows(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]plannedOrder, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []plannedOrder
	for rows.Next() {
		wo, err := scanWorkOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, plannedOrder{WorkOrder: wo, priority: defaultCasePriority})
	}
	return out, rows.Err()
}

func withCases(ctx context.Context, tx pgx.Tx, tid string, orders []plannedOrder) error {
	var caseIDs []string
	for _, o := range orders {
		if o.CaseID != nil {
			caseIDs = append(caseIDs, *o.CaseID)
		}
	}
	if len(caseIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `select id::text, priority, sla_resolution_due from service_case where tenant_id = $1 and id = any($2::uuid[])`, tid, caseIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	type caseInfo struct {
		priority int
		due      time.Time
	}
	info := map[string]caseInfo{}
	for rows.Next() {
		var id string
		var c caseInfo
		if err := rows.Scan(&id, &c.priority, &c.due); err != nil {
			return err
		}
		info[id] = c
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, o := range orders {
		if o.CaseID == nil {
			continue
		}
		if c, ok := info[*o.CaseID]; ok {
			due := c.due
			orders[i].priority, orders[i].due = c.priority, &due
		}
	}
	return nil
}

func (s *Postgres) RequestOptimization(ctx context.Context, in OptimizeInput, now time.Time, key, requestHash string) (Optimization, bool, error) {
	from, to, err := domain.DayBounds(in.Date, in.TimeZone)
	if err != nil {
		return Optimization{}, false, unprocessable(CodeDispatchEmpty, err.Error())
	}
	tid := tenantOf(ctx)
	var out Optimization
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_dispatch_optimization", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getOptimization(ctx, tx, tid, id, false)
			return err
		}
		techs, open, held, left, err := optimizationInputs(ctx, tx, tid, in, from, to)
		if err != nil {
			return err
		}
		req, skipped, err := buildRequest(in.Date, in.TimeZone, in.TimeLimit, techs, open, held)
		if err != nil {
			return err
		}
		if len(req.WorkOrders) == len(req.Pinned) || len(req.Technicians) == 0 {
			return unprocessable(CodeOptimizationEmpty, fmt.Sprintf("nothing to optimize: %d technicians and %d work orders can be planned", len(req.Technicians), len(req.WorkOrders)-len(req.Pinned)))
		}
		skippedJSON, err := json.Marshal(append(skipped, left...))
		if err != nil {
			return err
		}
		requestJSON, err := json.Marshal(req)
		if err != nil {
			return err
		}
		id = ids.New()
		if _, err := tx.Exec(ctx, `insert into service_dispatch_optimization (tenant_id, id, legal_entity_id, territory_id, run_date, time_zone, request, skipped,
			next_attempt_at, requested_by, idempotency_key, request_hash) values ($1, $2, $3, $4, $5::date, $6, $7, $8, $9, $10, $11, $12)`,
			tid, id, in.LegalEntityID, in.TerritoryID, in.Date, in.TimeZone, requestJSON, skippedJSON, now, actorOf(ctx), nullable(key), requestHash); err != nil {
			return err
		}
		out, err = getOptimization(ctx, tx, tid, id, false)
		return err
	})
	return out, replayed, err
}

func (s *Postgres) GetOptimization(ctx context.Context, id string) (Optimization, error) {
	var o Optimization
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		o, err = getOptimization(ctx, tx, tenantOf(ctx), id, false)
		return err
	})
	return o, err
}

func (s *Postgres) OptimizationRequest(ctx context.Context, id string) (optimizer.Request, error) {
	var raw []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select request from service_dispatch_optimization where tenant_id = $1 and id = $2`, tenantOf(ctx), id).Scan(&raw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return optimizer.Request{}, httpx.NewProblem(http.StatusNotFound, CodeOptimizationUnknown, "dispatch optimization not found")
	}
	var req optimizer.Request
	if err != nil {
		return req, err
	}
	return req, json.Unmarshal(raw, &req)
}

func (s *Postgres) ListOptimizations(ctx context.Context, f ListFilter) (Page[Optimization], error) {
	return listPage(ctx, s, f.Limit, func(o Optimization) string { return o.ID }, scanOptimization,
		`select `+optimizationColumns+` from service_dispatch_optimization x
		where x.tenant_id = $1 and ($2 = '' or x.territory_id::text = $2) and ($3 = '' or x.status = $3) and `+keyset("x", "service_dispatch_optimization", 4)+`
		order by x.created_at desc, x.id desc limit $5`,
		tenantOf(ctx), f.Territory, f.Status, f.Before)
}

func (s *Postgres) updateOptimization(ctx context.Context, id, query string, args ...any) (Optimization, error) {
	var o Optimization
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, query, append([]any{tenantOf(ctx), id}, args...)...); err != nil {
			return err
		}
		var err error
		o, err = getOptimization(ctx, tx, tenantOf(ctx), id, false)
		return err
	})
	return o, err
}

func (s *Postgres) RecordOptimizationSubmitted(ctx context.Context, id string, attempt int, remoteRunID string, now time.Time) (Optimization, error) {
	return s.updateOptimization(ctx, id, `update service_dispatch_optimization set status = 'RUNNING', remote_run_id = $3, last_error = null, updated_at = $4
		where tenant_id = $1 and id = $2 and status = 'PENDING' and attempt_count = $5`, remoteRunID, now, attempt)
}

func (s *Postgres) RecordOptimizationResult(ctx context.Context, id, remoteRunID string, result optimizer.Result, now time.Time) (Optimization, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Optimization{}, err
	}
	return s.updateOptimization(ctx, id, `update service_dispatch_optimization set status = 'SUCCEEDED', result = $3, last_error = null, updated_at = $4
		where tenant_id = $1 and id = $2 and status = 'RUNNING' and remote_run_id = $5`, raw, now, remoteRunID)
}

func (s *Postgres) RecordOptimizationFailure(ctx context.Context, id string, attempt int, reason string, permanent bool, now time.Time) (Optimization, error) {
	return s.updateOptimization(ctx, id, `update service_dispatch_optimization set attempt_count = attempt_count + 1, last_error = $3, remote_run_id = null,
			next_attempt_at = $4::timestamptz + make_interval(secs => least(3600, 60 * power(2, attempt_count))::double precision),
			status = case when $5 or attempt_count + 1 >= $6 then 'PARKED' else 'PENDING' end, updated_at = $4
		where tenant_id = $1 and id = $2 and status in ('PENDING', 'RUNNING') and attempt_count = $7`, reason, now, permanent, MaxOptimizationAttempts, attempt)
}

func (s *Postgres) RetryOptimization(ctx context.Context, id string, now time.Time) (Optimization, error) {
	var o Optimization
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := getOptimization(ctx, tx, tenantOf(ctx), id, true)
		if err != nil {
			return err
		}
		if current.Status != OptParked {
			return unprocessable(CodeOptimizationState, fmt.Sprintf("only a PARKED optimization is retried; this one is %s", current.Status))
		}
		if _, err := tx.Exec(ctx, `update service_dispatch_optimization set status = 'PENDING', attempt_count = 0, retry_generation = retry_generation + 1, next_attempt_at = $3, updated_at = $3
			where tenant_id = $1 and id = $2`, tenantOf(ctx), id, now); err != nil {
			return err
		}
		o, err = getOptimization(ctx, tx, tenantOf(ctx), id, false)
		return err
	})
	return o, err
}

func (s *Postgres) AcceptOptimization(ctx context.Context, id string, now time.Time) (Optimization, bool, error) {
	tid := tenantOf(ctx)
	var out Optimization
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := getOptimization(ctx, tx, tid, id, true)
		if err != nil {
			return err
		}
		if current.Status == OptAccepted {
			replayed, out = true, current
			return nil
		}
		if current.Status != OptSucceeded {
			return unprocessable(CodeOptimizationState, fmt.Sprintf("only a SUCCEEDED optimization is accepted; this one is %s", current.Status))
		}
		from, _, err := domain.DayBounds(current.Date, current.TimeZone)
		if err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `select request from service_dispatch_optimization where tenant_id = $1 and id = $2`, tid, id).Scan(&raw); err != nil {
			return err
		}
		var req optimizer.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
		held := map[string]bool{}
		for _, p := range req.Pinned {
			held[p.WorkOrderID] = true
		}
		plan := slices.DeleteFunc(slices.Clone(current.Result.Assignments), func(a optimizer.Assignment) bool { return held[a.WorkOrderID] })
		slices.SortFunc(plan, func(a, b optimizer.Assignment) int {
			if a.TechnicianID != b.TechnicianID {
				return strings.Compare(a.TechnicianID, b.TechnicianID)
			}
			return a.Start - b.Start
		})
		outcome := Outcome{Applied: []Applied{}, Rejected: []Rejection{}}
		for _, a := range plan {
			start := from.Add(time.Duration(a.Start) * time.Minute).UTC()
			wo, rejection, err := applyPlanned(ctx, tx, a, start, now)
			if err != nil {
				return err
			}
			if rejection != nil {
				outcome.Rejected = append(outcome.Rejected, *rejection)
				continue
			}
			outcome.Applied = append(outcome.Applied, Applied{WorkOrderID: wo.ID, TechnicianID: a.TechnicianID, Start: *wo.AssignedStart, End: *wo.AssignedEnd})
		}
		outcomeJSON, err := json.Marshal(outcome)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update service_dispatch_optimization set status = 'ACCEPTED', outcome = $3, accepted_by = $4, accepted_at = $5, updated_at = $5
			where tenant_id = $1 and id = $2`, tid, id, outcomeJSON, actorOf(ctx), now); err != nil {
			return err
		}
		out, err = getOptimization(ctx, tx, tid, id, false)
		return err
	})
	return out, replayed, err
}

func applyPlanned(ctx context.Context, tx pgx.Tx, a optimizer.Assignment, start, now time.Time) (WorkOrder, *Rejection, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return WorkOrder{}, nil, err
	}
	wo, err := lockWorkOrder(ctx, sp, tenantOf(ctx), a.WorkOrderID)
	if err == nil && wo.Status != string(domain.WOUnscheduled) {
		err = unprocessable(CodeWOInvalid, fmt.Sprintf("the work order is %s, not UNSCHEDULED, since the optimization was requested", wo.Status))
	}
	if err == nil {
		wo, err = assignTx(ctx, sp, a.WorkOrderID, 0, AssignInput{TechnicianID: a.TechnicianID, Start: start}, now)
	}
	if err == nil {
		return wo, nil, sp.Commit(ctx)
	}
	if rollback := sp.Rollback(ctx); rollback != nil {
		return WorkOrder{}, nil, rollback
	}
	var p *httpx.Problem
	if errors.As(err, &p) && p.Status < http.StatusInternalServerError {
		return WorkOrder{}, &Rejection{WorkOrderID: a.WorkOrderID, Code: p.Code, Detail: p.Detail}, nil
	}
	return WorkOrder{}, nil, err
}
