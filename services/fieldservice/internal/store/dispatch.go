package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// Problem codes of technicians and dispatch. The assignment hard constraints use the
// SVC-DSP codes of domain/dispatch.go.
const (
	CodeTechnicianDuplicate = "SVC-TECH-001"
	CodeTechnicianUnknown   = "SVC-TECH-002"
	CodeTechnicianBusy      = "SVC-TECH-003"
	CodeTechnicianEntity    = "SVC-TECH-004"
	CodeTechnicianBreaks    = "SVC-TECH-005"
	CodeDispatchEmpty       = "SVC-DSP-009"
	CodeDispatchRunUnknown  = "SVC-DSP-010"
)

// busyStatuses are the work-order statuses whose assignment holds the technician's time.
const busyStatuses = `status <> 'CANCELED'`

// Technician is a field technician: where, when and with which skills they can work
// (PRD 8.45.4). Skills are free codes until wave 42 owns them.
type Technician struct {
	ID               string   `json:"id"`
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	WorkerID         *string  `json:"workerId,omitempty"`
	LegalEntityID    string   `json:"legalEntityId"`
	TerritoryIDs     []string `json:"territoryIds"`
	Skills           []string `json:"skills"`
	TimeZone         string   `json:"timeZone"`
	ShiftDays        []int    `json:"shiftDays"`
	ShiftStartMinute int      `json:"shiftStartMinute"`
	ShiftEndMinute   int      `json:"shiftEndMinute"`
	HomeLatitude     *float64 `json:"homeLatitude,omitempty"`
	HomeLongitude    *float64 `json:"homeLongitude,omitempty"`
	TruckLocationID  *string  `json:"truckLocationId,omitempty"`
	// TruckSiteID and TruckWarehouseID, with the location, address the truck's stock in
	// inventory (BR-45.7); both or neither.
	TruckSiteID      *string `json:"truckSiteId,omitempty"`
	TruckWarehouseID *string `json:"truckWarehouseId,omitempty"`
	MaxDailyMinutes  int     `json:"maxDailyMinutes"`
	OvertimeAllowed  bool    `json:"overtimeAllowed"`
	Active           bool    `json:"active"`
	Version          int     `json:"version"`
}

func (t Technician) dispatchable() domain.Technician {
	return domain.Technician{ID: t.ID, LegalEntityID: t.LegalEntityID, TerritoryIDs: t.TerritoryIDs, Skills: t.Skills, TimeZone: t.TimeZone,
		ShiftDays: t.ShiftDays, ShiftStart: t.ShiftStartMinute, ShiftEnd: t.ShiftEndMinute, MaxDailyMinutes: t.MaxDailyMinutes,
		OvertimeAllowed: t.OvertimeAllowed, Active: t.Active}
}

const technicianColumns = `id::text, code, name, worker_id::text, legal_entity_id::text, territory_ids::text[], skills, time_zone, shift_days,
	shift_start_minute, shift_end_minute, home_lat::float8, home_lon::float8, truck_location_id::text, truck_site_id::text, truck_warehouse_id::text, max_daily_minutes, overtime_allowed, active, version`

func scanTechnician(row pgx.Row) (Technician, error) {
	var t Technician
	err := row.Scan(&t.ID, &t.Code, &t.Name, &t.WorkerID, &t.LegalEntityID, &t.TerritoryIDs, &t.Skills, &t.TimeZone, &t.ShiftDays,
		&t.ShiftStartMinute, &t.ShiftEndMinute, &t.HomeLatitude, &t.HomeLongitude, &t.TruckLocationID, &t.TruckSiteID, &t.TruckWarehouseID, &t.MaxDailyMinutes, &t.OvertimeAllowed, &t.Active, &t.Version)
	if t.Skills == nil {
		t.Skills = []string{}
	}
	return t, err
}

func getTechnician(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Technician, error) {
	q := `select ` + technicianColumns + ` from service_technician where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	t, err := scanTechnician(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, httpx.NewProblem(http.StatusNotFound, CodeTechnicianUnknown, "technician not found")
	}
	return t, err
}

// CreateTechnician stores a technician. A replay with the same Idempotency-Key returns
// the technician created earlier.
func (s *Postgres) CreateTechnician(ctx context.Context, in Technician, key, requestHash string) (Technician, bool, error) {
	tid := tenantOf(ctx)
	var out Technician
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_technician", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getTechnician(ctx, tx, tid, id, false)
			return err
		}
		id = ids.New()
		if in.Skills == nil {
			in.Skills = []string{}
		}
		if _, err := tx.Exec(ctx, `insert into service_technician (tenant_id, id, code, name, worker_id, legal_entity_id, territory_ids, skills, time_zone,
			shift_days, shift_start_minute, shift_end_minute, home_lat, home_lon, truck_location_id, truck_site_id, truck_warehouse_id, max_daily_minutes, overtime_allowed, active,
			idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7::uuid[], $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)`,
			tid, id, in.Code, in.Name, in.WorkerID, in.LegalEntityID, in.TerritoryIDs, in.Skills, in.TimeZone, in.ShiftDays, in.ShiftStartMinute,
			in.ShiftEndMinute, in.HomeLatitude, in.HomeLongitude, in.TruckLocationID, in.TruckSiteID, in.TruckWarehouseID, in.MaxDailyMinutes, in.OvertimeAllowed, in.Active,
			nullable(key), requestHash); err != nil {
			return err
		}
		out, err = getTechnician(ctx, tx, tid, id, false)
		return err
	})
	if isUnique(err) {
		return Technician{}, false, httpx.NewProblem(http.StatusConflict, CodeTechnicianDuplicate, "a technician with this code already exists")
	}
	return out, replayed, err
}

// GetTechnician loads one technician.
func (s *Postgres) GetTechnician(ctx context.Context, id string) (Technician, error) {
	var t Technician
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		t, err = getTechnician(ctx, tx, tenantOf(ctx), id, false)
		return err
	})
	return t, err
}

// ListTechnicians returns technicians newest first, filtered by a territory they cover
// and by whether they are active ("true", "false" or "" for both).
func (s *Postgres) ListTechnicians(ctx context.Context, f ListFilter) (Page[Technician], error) {
	return listPage(ctx, s, f.Limit, func(t Technician) string { return t.ID }, scanTechnician,
		`select `+technicianColumns+` from service_technician x
		where x.tenant_id = $1 and ($2 = '' or $2::uuid = any(x.territory_ids)) and ($3 = '' or x.active = ($3 = 'true'))
			and `+keyset("x", "service_technician", 4)+`
		order by x.created_at desc, x.id desc limit $5`,
		tenantOf(ctx), f.Territory, f.Active, f.Before)
}

// UpdateTechnician replaces a technician's editable fields at the version the caller
// read. The code and the legal entity do not change. A technician with work orders
// still ahead of them cannot be deactivated (SVC-TECH-003), and a change that would put
// a SCHEDULED visit in breach of BR-45.3 is refused (SVC-TECH-005).
func (s *Postgres) UpdateTechnician(ctx context.Context, id string, version int, in Technician) (Technician, error) {
	tid := tenantOf(ctx)
	var out Technician
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := getTechnician(ctx, tx, tid, id, true)
		if err != nil {
			return err
		}
		if current.Version != version {
			return versionConflict(current.Version)
		}
		if in.Skills == nil {
			in.Skills = []string{}
		}
		if in.LegalEntityID != current.LegalEntityID {
			return unprocessable(CodeTechnicianEntity, "the legal entity of a technician does not change")
		}
		if current.Active && !in.Active {
			var open int
			if err := tx.QueryRow(ctx, `select count(*) from service_work_order where tenant_id = $1 and technician_id = $2
				and status in ('SCHEDULED', 'DISPATCHED', 'TRAVELING', 'IN_PROGRESS', 'ON_HOLD')`, tid, id).Scan(&open); err != nil {
				return err
			}
			if open > 0 {
				return unprocessable(CodeTechnicianBusy, fmt.Sprintf("the technician still has %d work orders ahead; unassign them first", open))
			}
		}
		next := in
		next.ID, next.LegalEntityID = current.ID, current.LegalEntityID
		if err := recheckScheduled(ctx, tx, tid, next.dispatchable()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update service_technician set name = $3, worker_id = $4, territory_ids = $5::uuid[], skills = $6, time_zone = $7,
			shift_days = $8, shift_start_minute = $9, shift_end_minute = $10, home_lat = $11, home_lon = $12, truck_location_id = $13,
			truck_site_id = $17, truck_warehouse_id = $18, max_daily_minutes = $14, overtime_allowed = $15, active = $16, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`,
			tid, id, in.Name, in.WorkerID, in.TerritoryIDs, in.Skills, in.TimeZone, in.ShiftDays, in.ShiftStartMinute, in.ShiftEndMinute,
			in.HomeLatitude, in.HomeLongitude, in.TruckLocationID, in.MaxDailyMinutes, in.OvertimeAllowed, in.Active, in.TruckSiteID, in.TruckWarehouseID); err != nil {
			return err
		}
		out, err = getTechnician(ctx, tx, tid, id, false)
		return err
	})
	return out, err
}

// recheckScheduled runs the BR-45.3 hard constraints again for every SCHEDULED visit of
// a technician against the technician as it is about to be saved, so an edit of the
// territories, skills, shift or daily maximum cannot leave a visit that publishing would
// dispatch in breach of them. Every broken visit is reported (SVC-TECH-005); the
// dispatcher moves or unassigns them first. DISPATCHED visits are already with the
// technician and are not rechecked.
func recheckScheduled(ctx context.Context, tx pgx.Tx, tenantID string, t domain.Technician) error {
	rows, err := tx.Query(ctx, `select id::text, number, legal_entity_id::text, territory_id::text, required_skills, window_start, window_end,
		assigned_start, assigned_end from service_work_order
		where tenant_id = $1 and technician_id = $2 and status = 'SCHEDULED' order by assigned_start, id`, tenantID, t.ID)
	if err != nil {
		return err
	}
	type held struct {
		number     string
		job        domain.Job
		start, end time.Time
	}
	var visits []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.job.ID, &h.number, &h.job.LegalEntityID, &h.job.TerritoryID, &h.job.RequiredSkills, &h.job.WindowStart,
			&h.job.WindowEnd, &h.start, &h.end); err != nil {
			rows.Close()
			return err
		}
		h.job.DurationMinutes = int(h.end.Sub(h.start) / time.Minute)
		visits = append(visits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	broken := map[string]string{}
	var details []string
	for _, h := range visits {
		others, err := bookings(ctx, tx, tenantID, t.ID, h.job.ID, h.start, h.end)
		if err != nil {
			return err
		}
		vs := domain.CheckAssignment(t, h.job, h.start, others)
		if len(vs) == 0 {
			continue
		}
		var lines []string
		for _, v := range vs {
			lines = append(lines, v.Code+" "+v.Detail)
		}
		broken[h.number] = strings.Join(lines, "; ")
		details = append(details, h.number)
	}
	if len(details) == 0 {
		return nil
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeTechnicianBreaks,
		fmt.Sprintf("the change breaks the dispatch rules for scheduled work orders %s; move or unassign them first", strings.Join(details, ", ")))
	p.Errors = broken
	return p
}

// Scheduling is what a dispatcher needs to know about a visit before it is placed.
type Scheduling struct {
	RequiredSkills     []string
	WindowStart        *time.Time
	WindowEnd          *time.Time
	Address            string
	Latitude           *float64
	Longitude          *float64
	EstDurationMinutes int
}

// UpdateWorkOrderScheduling sets the skills, window, address, position and estimate of a
// work order that is not scheduled yet; a scheduled order is unassigned first so the
// assignment checks never run against data that changed afterwards.
func (s *Postgres) UpdateWorkOrderScheduling(ctx context.Context, id string, version int, sc Scheduling) (WorkOrder, error) {
	tid := tenantOf(ctx)
	var out WorkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return versionConflict(current.Version)
		}
		if current.Status != string(domain.WOUnscheduled) {
			return unprocessable(CodeWOInvalid, fmt.Sprintf("scheduling data of a %s work order does not change; unassign it first", current.Status))
		}
		if _, err := tx.Exec(ctx, `update service_work_order set required_skills = $3, window_start = $4, window_end = $5, address = $6,
			geo_lat = $7, geo_lon = $8, est_duration_minutes = $9, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`,
			tid, id, sc.RequiredSkills, sc.WindowStart, sc.WindowEnd, sc.Address, sc.Latitude, sc.Longitude, sc.EstDurationMinutes); err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, tid, id)
		return err
	})
	return out, err
}

// AssignInput places a work order on a technician at a start time; the end follows from
// the order's estimated duration.
type AssignInput struct {
	TechnicianID string
	Start        time.Time
	Pinned       bool
}

func actorOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.UserID
}

func violationProblem(vs []domain.Violation) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, vs[0].Code, vs[0].Detail)
	p.Errors = map[string]string{}
	var details []string
	for _, v := range vs {
		if prev, ok := p.Errors[v.Code]; ok {
			p.Errors[v.Code] = prev + "; " + v.Detail
		} else {
			p.Errors[v.Code] = v.Detail
		}
		details = append(details, v.Detail)
	}
	p.Detail = strings.Join(details, "; ")
	return p
}

// bookings lists the other time the technician holds around a start, wide enough to
// cover the local day of any time zone.
func bookings(ctx context.Context, tx pgx.Tx, tenantID, technicianID, exceptID string, from, to time.Time) ([]domain.Slot, error) {
	rows, err := tx.Query(ctx, `select assigned_start, assigned_end from service_work_order
		where tenant_id = $1 and technician_id = $2 and id <> $3 and `+busyStatuses+` and assigned_start < $5 and assigned_end > $4`,
		tenantID, technicianID, exceptID, from.Add(-26*time.Hour), to.Add(26*time.Hour))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Slot
	for rows.Next() {
		var sl domain.Slot
		if err := rows.Scan(&sl.Start, &sl.End); err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// AssignWorkOrder gives an UNSCHEDULED or SCHEDULED work order to a technician at a
// start time and makes it SCHEDULED (FR-45.7, BR-45.3). The technician's row is locked
// so two dispatchers cannot book the same time; every hard constraint that fails is
// returned together (SVC-DSP-003 to 008). Dispatched orders are not moved.
func (s *Postgres) AssignWorkOrder(ctx context.Context, id string, version int, in AssignInput, now time.Time) (WorkOrder, error) {
	var out WorkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = assignTx(ctx, tx, id, version, in, now)
		return err
	})
	return out, err
}

// assignTx is AssignWorkOrder inside the caller's transaction; a version below 1 skips
// the version check (the optimizer accept applies a plan to the orders as they are).
func assignTx(ctx context.Context, tx pgx.Tx, id string, version int, in AssignInput, now time.Time) (WorkOrder, error) {
	tid := tenantOf(ctx)
	wo, err := lockWorkOrder(ctx, tx, tid, id)
	if err != nil {
		return WorkOrder{}, err
	}
	if version > 0 && wo.Version != version {
		return WorkOrder{}, versionConflict(wo.Version)
	}
	if wo.Status != string(domain.WOUnscheduled) && wo.Status != string(domain.WOScheduled) {
		return WorkOrder{}, unprocessable(CodeWOInvalid, fmt.Sprintf("a %s work order cannot be assigned", wo.Status))
	}
	tech, err := getTechnician(ctx, tx, tid, in.TechnicianID, true)
	if err != nil {
		return WorkOrder{}, err
	}
	end := in.Start.Add(time.Duration(wo.EstDurationMinutes) * time.Minute)
	others, err := bookings(ctx, tx, tid, tech.ID, id, in.Start, end)
	if err != nil {
		return WorkOrder{}, err
	}
	job := domain.Job{ID: id, LegalEntityID: wo.LegalEntityID, TerritoryID: wo.TerritoryID, RequiredSkills: wo.RequiredSkills,
		WindowStart: wo.WindowStart, WindowEnd: wo.WindowEnd, DurationMinutes: wo.EstDurationMinutes}
	if vs := domain.CheckAssignment(tech.dispatchable(), job, in.Start, others); len(vs) > 0 {
		return WorkOrder{}, violationProblem(vs)
	}
	if _, err := tx.Exec(ctx, `update service_work_order set technician_id = $3, assigned_start = $4, assigned_end = $5, assignment_pinned = $6,
		status = 'SCHEDULED', version = version + 1, updated_at = $7 where tenant_id = $1 and id = $2`,
		tid, id, tech.ID, in.Start, end, in.Pinned, now); err != nil {
		return WorkOrder{}, err
	}
	out, err := getWorkOrder(ctx, tx, tid, id)
	if err != nil {
		return WorkOrder{}, err
	}
	start, endText := in.Start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
	var previous *string
	if wo.TechnicianID != nil && *wo.TechnicianID != tech.ID {
		previous = wo.TechnicianID
	}
	if err := notifyRoute(ctx, tx, tid, &tech.ID, previous); err != nil {
		return WorkOrder{}, err
	}
	return out, enqueue(ctx, tx, "workorder", id, svcevents.AssignmentChanged, tid, svcevents.AssignmentChangedData{
		WorkOrderID: id, Number: wo.Number, LegalEntityID: wo.LegalEntityID, TerritoryID: wo.TerritoryID, Action: "ASSIGNED",
		TechnicianID: &tech.ID, PreviousTechnicianID: previous, Start: &start, End: &endText, Pinned: in.Pinned,
		At: now.UTC().Format(time.RFC3339), Actor: actorOf(ctx),
	})
}

// UnassignWorkOrder takes a SCHEDULED work order off the schedule: it is UNSCHEDULED
// again and loses its technician and time, pinned or not (a pin protects the
// assignment from the optimizer, not from the dispatcher).
func (s *Postgres) UnassignWorkOrder(ctx context.Context, id string, version int, now time.Time) (WorkOrder, error) {
	tid := tenantOf(ctx)
	var out WorkOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		wo, err := lockWorkOrder(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		if wo.Version != version {
			return versionConflict(wo.Version)
		}
		if wo.Status != string(domain.WOScheduled) {
			return unprocessable(CodeWOInvalid, fmt.Sprintf("a %s work order cannot be taken off the schedule; only SCHEDULED ones can", wo.Status))
		}
		if _, err := tx.Exec(ctx, `update service_work_order set technician_id = null, assigned_start = null, assigned_end = null,
			assignment_pinned = false, status = 'UNSCHEDULED', version = version + 1, updated_at = $3 where tenant_id = $1 and id = $2`, tid, id, now); err != nil {
			return err
		}
		out, err = getWorkOrder(ctx, tx, tid, id)
		if err != nil || wo.TechnicianID == nil {
			return err
		}
		var start, end *string
		if wo.AssignedStart != nil && wo.AssignedEnd != nil {
			a, b := wo.AssignedStart.UTC().Format(time.RFC3339), wo.AssignedEnd.UTC().Format(time.RFC3339)
			start, end = &a, &b
		}
		if err := notifyRoute(ctx, tx, tid, wo.TechnicianID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "workorder", id, svcevents.AssignmentChanged, tid, svcevents.AssignmentChangedData{
			WorkOrderID: id, Number: wo.Number, LegalEntityID: wo.LegalEntityID, TerritoryID: wo.TerritoryID, Action: "UNASSIGNED",
			TechnicianID: wo.TechnicianID, Start: start, End: end, Pinned: wo.AssignmentPinned,
			At: now.UTC().Format(time.RFC3339), Actor: actorOf(ctx),
		})
	})
	return out, err
}

// BoardTechnician is one row of the dispatch board: a technician and what they hold on
// the day.
type BoardTechnician struct {
	Technician    Technician  `json:"technician"`
	Assignments   []WorkOrder `json:"assignments"`
	BookedMinutes int         `json:"bookedMinutes"`
}

// Board is the data of the dispatch board for one territory-day (FR-45.7): the active
// technicians who cover the territory with their assignments on the local day, and the
// territory's unscheduled work orders.
type Board struct {
	TerritoryID string            `json:"territoryId"`
	Date        string            `json:"date"`
	TimeZone    string            `json:"timeZone"`
	Technicians []BoardTechnician `json:"technicians"`
	Unscheduled []WorkOrder       `json:"unscheduled"`
}

// MaxBoardRows bounds the technicians and the unscheduled orders of one board.
const MaxBoardRows = 200

// Board reads the dispatch board of a territory and local day.
func (s *Postgres) Board(ctx context.Context, territoryID, date, timeZone string) (Board, error) {
	from, to, err := domain.DayBounds(date, timeZone)
	if err != nil {
		return Board{}, unprocessable(CodeDispatchEmpty, err.Error())
	}
	tid := tenantOf(ctx)
	b := Board{TerritoryID: territoryID, Date: date, TimeZone: timeZone, Technicians: []BoardTechnician{}, Unscheduled: []WorkOrder{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+technicianColumns+` from service_technician
			where tenant_id = $1 and active and $2::uuid = any(territory_ids) order by code limit $3`, tid, territoryID, MaxBoardRows)
		if err != nil {
			return err
		}
		index := map[string]int{}
		for rows.Next() {
			t, err := scanTechnician(rows)
			if err != nil {
				rows.Close()
				return err
			}
			index[t.ID] = len(b.Technicians)
			b.Technicians = append(b.Technicians, BoardTechnician{Technician: t, Assignments: []WorkOrder{}})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(index) > 0 {
			techIDs := make([]string, 0, len(index))
			for techID := range index {
				techIDs = append(techIDs, techID)
			}
			assigned, err := tx.Query(ctx, `select `+workOrderColumns+` from service_work_order
				where tenant_id = $1 and technician_id = any($2::uuid[]) and `+busyStatuses+` and assigned_start >= $3 and assigned_start < $4
				order by assigned_start, id`, tid, techIDs, from, to)
			if err != nil {
				return err
			}
			for assigned.Next() {
				wo, err := scanWorkOrder(assigned)
				if err != nil {
					assigned.Close()
					return err
				}
				row := &b.Technicians[index[*wo.TechnicianID]]
				row.Assignments = append(row.Assignments, wo)
				row.BookedMinutes += wo.EstDurationMinutes
			}
			assigned.Close()
			if err := assigned.Err(); err != nil {
				return err
			}
		}
		open, err := tx.Query(ctx, `select `+workOrderColumns+` from service_work_order
			where tenant_id = $1 and territory_id = $2 and status = 'UNSCHEDULED' order by created_at, id limit $3`, tid, territoryID, MaxBoardRows)
		if err != nil {
			return err
		}
		defer open.Close()
		for open.Next() {
			wo, err := scanWorkOrder(open)
			if err != nil {
				return err
			}
			b.Unscheduled = append(b.Unscheduled, wo)
		}
		return open.Err()
	})
	return b, err
}

// DispatchRun is a published territory-day: the work orders that went from SCHEDULED to
// DISPATCHED together, with their route order (PRD 8.45.4 DispatchRun, without a solver).
type DispatchRun struct {
	ID              string                      `json:"id"`
	LegalEntityID   string                      `json:"legalEntityId"`
	TerritoryID     string                      `json:"territoryId"`
	Date            string                      `json:"date"`
	TimeZone        string                      `json:"timeZone"`
	Status          string                      `json:"status"`
	Assignments     []svcevents.DispatchedOrder `json:"assignments"`
	DispatchedCount int                         `json:"dispatchedCount"`
	PublishedBy     string                      `json:"publishedBy"`
	PublishedAt     time.Time                   `json:"publishedAt"`
}

// PublishInput names the territory-day to publish.
type PublishInput struct {
	LegalEntityID string
	TerritoryID   string
	Date          string
	TimeZone      string
}

const runColumns = `id::text, legal_entity_id::text, territory_id::text, run_date::text, time_zone, status, assignments, dispatched_count, published_by, published_at`

func scanRun(row pgx.Row) (DispatchRun, error) {
	var r DispatchRun
	var raw []byte
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.TerritoryID, &r.Date, &r.TimeZone, &r.Status, &raw, &r.DispatchedCount, &r.PublishedBy, &r.PublishedAt); err != nil {
		return r, err
	}
	return r, json.Unmarshal(raw, &r.Assignments)
}

// PublishDispatch dispatches every SCHEDULED work order with a technician that starts on
// the local day in the territory and legal entity (SCHEDULED to DISPATCHED, which stops
// the response clock of the order's case, BR-45.2), and records the run with its route
// order per technician. Nothing to dispatch is SVC-DSP-009. A replay with the same
// Idempotency-Key returns the run.
func (s *Postgres) PublishDispatch(ctx context.Context, in PublishInput, now time.Time, key, requestHash string) (DispatchRun, bool, error) {
	from, to, err := domain.DayBounds(in.Date, in.TimeZone)
	if err != nil {
		return DispatchRun{}, false, unprocessable(CodeDispatchEmpty, err.Error())
	}
	tid := tenantOf(ctx)
	var out DispatchRun
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_dispatch_run", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from service_dispatch_run where tenant_id = $1 and id = $2`, tid, id))
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, number, case_id::text, technician_id::text, assigned_start, assigned_end from service_work_order
			where tenant_id = $1 and legal_entity_id = $2 and territory_id = $3 and status = 'SCHEDULED' and technician_id is not null
				and assigned_start >= $4 and assigned_start < $5 order by technician_id, assigned_start, id for update`,
			tid, in.LegalEntityID, in.TerritoryID, from, to)
		if err != nil {
			return err
		}
		type held struct {
			order  svcevents.DispatchedOrder
			caseID *string
		}
		var orders []held
		sequence := map[string]int{}
		for rows.Next() {
			var h held
			var start, end time.Time
			if err := rows.Scan(&h.order.WorkOrderID, &h.order.Number, &h.caseID, &h.order.TechnicianID, &start, &end); err != nil {
				rows.Close()
				return err
			}
			sequence[h.order.TechnicianID]++
			h.order.Sequence = sequence[h.order.TechnicianID]
			h.order.Start, h.order.End = start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339)
			orders = append(orders, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(orders) == 0 {
			return unprocessable(CodeDispatchEmpty, "no SCHEDULED work order with a technician starts on that day in the territory")
		}
		assignments := make([]svcevents.DispatchedOrder, 0, len(orders))
		for _, h := range orders {
			if _, err := tx.Exec(ctx, `update service_work_order set status = 'DISPATCHED', version = version + 1, updated_at = $3
				where tenant_id = $1 and id = $2`, tid, h.order.WorkOrderID, now); err != nil {
				return err
			}
			if h.caseID != nil {
				if err := recordResponse(ctx, tx, tid, *h.caseID, now); err != nil {
					return err
				}
			}
			assignments = append(assignments, h.order)
			technician := h.order.TechnicianID
			if err := notifyRoute(ctx, tx, tid, &technician); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(assignments)
		if err != nil {
			return err
		}
		runID := ids.New()
		if _, err := tx.Exec(ctx, `insert into service_dispatch_run (tenant_id, id, legal_entity_id, territory_id, run_date, time_zone, assignments,
			dispatched_count, published_by, published_at, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5::date, $6, $7, $8, $9, $10, $11, $12)`,
			tid, runID, in.LegalEntityID, in.TerritoryID, in.Date, in.TimeZone, raw, len(assignments), actorOf(ctx), now, nullable(key), requestHash); err != nil {
			return err
		}
		out = DispatchRun{ID: runID, LegalEntityID: in.LegalEntityID, TerritoryID: in.TerritoryID, Date: in.Date, TimeZone: in.TimeZone, Status: "PUBLISHED",
			Assignments: assignments, DispatchedCount: len(assignments), PublishedBy: actorOf(ctx), PublishedAt: now}
		return enqueue(ctx, tx, "dispatchrun", runID, svcevents.DispatchPublished, tid, svcevents.DispatchPublishedData{
			RunID: runID, LegalEntityID: in.LegalEntityID, TerritoryID: in.TerritoryID, Date: in.Date, TimeZone: in.TimeZone,
			PublishedAt: now.UTC().Format(time.RFC3339), Actor: actorOf(ctx), Assignments: assignments,
		})
	})
	return out, replayed, err
}

// GetDispatchRun loads one published run.
func (s *Postgres) GetDispatchRun(ctx context.Context, id string) (DispatchRun, error) {
	var r DispatchRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		r, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from service_dispatch_run where tenant_id = $1 and id = $2`, tenantOf(ctx), id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeDispatchRunUnknown, "dispatch run not found")
		}
		return err
	})
	return r, err
}

// ListDispatchRuns returns published runs newest first, optionally of one territory.
func (s *Postgres) ListDispatchRuns(ctx context.Context, f ListFilter) (Page[DispatchRun], error) {
	return listPage(ctx, s, f.Limit, func(r DispatchRun) string { return r.ID }, scanRun,
		`select `+runColumns+` from service_dispatch_run x
		where x.tenant_id = $1 and ($2 = '' or x.territory_id::text = $2) and `+keyset("x", "service_dispatch_run", 3)+`
		order by x.created_at desc, x.id desc limit $4`,
		tenantOf(ctx), f.Territory, f.Before)
}
