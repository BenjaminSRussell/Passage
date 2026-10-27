package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// Position is a budgeted seat with an FTE (FR-42.3).
type Position struct {
	ID             string     `json:"id"`
	PositionNumber string     `json:"positionNumber"`
	JobCode        string     `json:"jobCode"`
	DepartmentID   string     `json:"departmentId"`
	LegalEntityID  string     `json:"legalEntityId"`
	FTE            string     `json:"fte"`
	AllowOverfill  bool       `json:"allowOverfill"`
	ValidFrom      time.Time  `json:"validFrom"`
	ValidTo        *time.Time `json:"validTo,omitempty"`
	ReportsToID    *string    `json:"reportsToPositionId,omitempty"`
}

// PositionAssignment holds a worker on a position for a validity range.
type PositionAssignment struct {
	ID           string     `json:"id"`
	PositionID   string     `json:"positionId"`
	WorkerID     string     `json:"workerId"`
	EmploymentID string     `json:"employmentId"`
	FTEShare     string     `json:"fteShare"`
	IsPrimary    bool       `json:"isPrimary"`
	ValidFrom    time.Time  `json:"validFrom"`
	ValidTo      *time.Time `json:"validTo,omitempty"`
}

// AsOfView answers "who is this worker, reporting to whom, on this date".
type AsOfView struct {
	WorkerID        string       `json:"workerId"`
	Date            string       `json:"date"`
	Employments     []Employment `json:"employments"`
	PositionID      *string      `json:"positionId"`
	PositionNumber  *string      `json:"positionNumber"`
	FTEShare        *string      `json:"fteShare"`
	ManagerWorkerID *string      `json:"managerWorkerId"`
}

// PositionDetail is a position with every assignment on it, oldest first.
type PositionDetail struct {
	Position
	Assignments []PositionAssignment `json:"assignments"`
}

// PositionPage is one keyset page of positions ordered by position number.
type PositionPage struct {
	Items     []Position
	NextAfter string
}

// positionSelect reads a position with its latest reports-to edge (slice 1
// writes one edge per position, at creation).
const positionSelect = `select p.id::text, p.position_number, p.job_code, p.department_id::text, p.legal_entity_id::text, p.fte::text,
	p.allow_overfill, p.valid_from, p.valid_to, h.parent_position_id::text
	from hr_position p
	left join lateral (select parent_position_id from position_hierarchy ph
		where ph.tenant_id = p.tenant_id and ph.child_position_id = p.id order by ph.valid_from desc limit 1) h on true`

func scanPosition(row pgx.Row) (Position, error) {
	var p Position
	err := row.Scan(&p.ID, &p.PositionNumber, &p.JobCode, &p.DepartmentID, &p.LegalEntityID, &p.FTE, &p.AllowOverfill, &p.ValidFrom, &p.ValidTo, &p.ReportsToID)
	p.ValidFrom, p.ValidTo = utc(p.ValidFrom), utcPtr(p.ValidTo)
	return p, err
}

// CreatePosition stores a position and, when reportsTo is set, its reports-to
// edge effective from the position's own start date. The reports-to position
// must exist on that date. A new position has no reports so it cannot close a
// cycle; the BR-42.7 check (domain.WouldCycle) applies to hierarchy edits.
func (s *Postgres) CreatePosition(ctx context.Context, in Position, idempotencyKey string) (Position, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Position
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanPosition(tx.QueryRow(ctx, positionSelect+` where p.tenant_id = $1 and p.idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if in.ReportsToID != nil {
			var valid bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from hr_position where tenant_id = $1 and id = $2 and validity @> $3::timestamptz)`,
				p.TenantID, *in.ReportsToID, in.ValidFrom).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return problem(http.StatusUnprocessableEntity, CodePositionParent, "the reports-to position does not exist on the position's start date")
			}
		}
		var err error
		out, err = scanPosition(tx.QueryRow(ctx, `insert into hr_position (tenant_id, id, position_number, job_code, department_id, legal_entity_id, fte, allow_overfill, valid_from, valid_to, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::numeric, $8, $9, $10, $11)
			returning id::text, position_number, job_code, department_id::text, legal_entity_id::text, fte::text, allow_overfill, valid_from, valid_to, null::text`,
			p.TenantID, ids.New(), in.PositionNumber, in.JobCode, in.DepartmentID, in.LegalEntityID, in.FTE, in.AllowOverfill, in.ValidFrom, in.ValidTo, key))
		if err != nil {
			return err
		}
		inserted = true
		if in.ReportsToID != nil {
			if _, err := tx.Exec(ctx, `insert into position_hierarchy (tenant_id, id, child_position_id, parent_position_id, valid_from, valid_to)
				values ($1, $2, $3, $4, $5, $6)`, p.TenantID, ids.New(), out.ID, *in.ReportsToID, out.ValidFrom, out.ValidTo); err != nil {
				return err
			}
			out.ReportsToID = in.ReportsToID
		}
		return nil
	})
	if idempotencyRace(err) {
		return Position{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Position{}, false, problem(http.StatusConflict, CodePositionDuplicate, "a position with this number already exists")
	}
	if err == nil && key != nil && !inserted && (out.PositionNumber != in.PositionNumber || out.FTE != in.FTE ||
		out.LegalEntityID != in.LegalEntityID || !sameStringPtr(out.ReportsToID, in.ReportsToID)) {
		return Position{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// GetPosition returns a position with its assignments.
func (s *Postgres) GetPosition(ctx context.Context, id string) (PositionDetail, error) {
	p := principal(ctx)
	out := PositionDetail{Assignments: []PositionAssignment{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pos, err := scanPosition(tx.QueryRow(ctx, positionSelect+` where p.tenant_id = $1 and p.id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodePositionNotFound, "position not found")
		}
		if err != nil {
			return err
		}
		out.Position = pos
		rows, err := tx.Query(ctx, assignmentSelect+` where tenant_id = $1 and position_id = $2 order by valid_from, id`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				return err
			}
			out.Assignments = append(out.Assignments, a)
		}
		return rows.Err()
	})
	return out, err
}

// ListPositions pages positions by position number.
func (s *Postgres) ListPositions(ctx context.Context, after string, limit int) (PositionPage, error) {
	p := principal(ctx)
	page := PositionPage{Items: []Position{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, positionSelect+` where p.tenant_id = $1 and p.position_number > $2 order by p.position_number limit $3`, p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pos, err := scanPosition(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, pos)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].PositionNumber
	}
	return page, err
}

// CreateAssignment places a worker on a position. The assignment must lie
// inside both the position's and the employment's validity, and the
// employment must belong to the worker and to the position's legal entity.
// The FTE total over the overlapping assignments is checked under a row lock
// on the position (FR-42.3). The total is conservative: it sums every
// overlapping row, not the peak at each instant, so an instant-accurate check
// is a later slice.
func (s *Postgres) CreateAssignment(ctx context.Context, positionID string, in PositionAssignment, idempotencyKey string) (PositionAssignment, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out PositionAssignment
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanAssignment(tx.QueryRow(ctx, assignmentSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		span := `tstzrange($3::timestamptz, $4::timestamptz, '[)')`
		var fte, positionEntity string
		var overfill, positionCovers bool
		err := tx.QueryRow(ctx, `select fte::text, allow_overfill, legal_entity_id::text, validity @> `+span+` from hr_position
			where tenant_id = $1 and id = $2 for update`, p.TenantID, positionID, in.ValidFrom, in.ValidTo).Scan(&fte, &overfill, &positionEntity, &positionCovers)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodePositionNotFound, "position not found")
		}
		if err != nil {
			return err
		}
		var employmentWorker, employmentEntity string
		var employmentCovers bool
		err = tx.QueryRow(ctx, `select worker_id::text, legal_entity_id::text, validity @> `+span+` from employment where tenant_id = $1 and id = $2`,
			p.TenantID, in.EmploymentID, in.ValidFrom, in.ValidTo).Scan(&employmentWorker, &employmentEntity, &employmentCovers)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (employmentWorker != in.WorkerID || employmentEntity != positionEntity)) {
			return problem(http.StatusUnprocessableEntity, CodeAssignmentMismatch, "the employment does not belong to this worker in the position's legal entity")
		}
		if err != nil {
			return err
		}
		if !positionCovers || !employmentCovers {
			return problem(http.StatusUnprocessableEntity, CodeAssignmentRange, "the assignment dates must lie inside both the position's and the employment's validity")
		}
		var existing string
		if err := tx.QueryRow(ctx, `select coalesce(sum(fte_share), 0)::text from position_assignment
			where tenant_id = $1 and position_id = $2 and validity && `+span,
			p.TenantID, positionID, in.ValidFrom, in.ValidTo).Scan(&existing); err != nil {
			return err
		}
		if err := domain.CheckFTE(decimal.RequireFromString(fte), overfill, decimal.RequireFromString(existing), decimal.RequireFromString(in.FTEShare)); err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				return problem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			return err
		}
		out, err = scanAssignment(tx.QueryRow(ctx, `insert into position_assignment (tenant_id, id, position_id, worker_id, employment_id, fte_share, is_primary, valid_from, valid_to, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10)
			returning id::text, position_id::text, worker_id::text, employment_id::text, fte_share::text, is_primary, valid_from, valid_to`,
			p.TenantID, ids.New(), positionID, in.WorkerID, in.EmploymentID, in.FTEShare, in.IsPrimary, in.ValidFrom, in.ValidTo, key))
		if err != nil {
			return err
		}
		inserted = true
		var managerPosition *string
		err = tx.QueryRow(ctx, `select parent_position_id::text from position_hierarchy
			where tenant_id = $1 and child_position_id = $2 and validity @> $3::timestamptz`, p.TenantID, positionID, out.ValidFrom).Scan(&managerPosition)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return enqueue(ctx, tx, "positionassignment", out.ID, hrevents.PositionAssignmentChanged, p.TenantID, hrevents.PositionAssignmentChangedData{
			ID: out.ID, WorkerID: out.WorkerID, PositionID: out.PositionID, ManagerPositionID: managerPosition, FTEShare: out.FTEShare,
			IsPrimary: out.IsPrimary, ValidFrom: out.ValidFrom.UTC().Format(time.RFC3339), ValidTo: rfc3339Ptr(out.ValidTo),
		})
	})
	if idempotencyRace(err) {
		return PositionAssignment{}, false, idempotencyInFlight()
	}
	if pgConstraint(err, "position_assignment_primary_once") {
		return PositionAssignment{}, false, problem(http.StatusConflict, CodeAssignmentOverlap, "the worker already has a primary position on that date")
	}
	if err == nil && key != nil && !inserted && (out.PositionID != positionID || out.WorkerID != in.WorkerID || out.EmploymentID != in.EmploymentID ||
		out.FTEShare != in.FTEShare || out.IsPrimary != in.IsPrimary) {
		return PositionAssignment{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

const assignmentSelect = "select id::text, position_id::text, worker_id::text, employment_id::text, fte_share::text, is_primary, valid_from, valid_to from position_assignment"

func scanAssignment(row pgx.Row) (PositionAssignment, error) {
	var a PositionAssignment
	err := row.Scan(&a.ID, &a.PositionID, &a.WorkerID, &a.EmploymentID, &a.FTEShare, &a.IsPrimary, &a.ValidFrom, &a.ValidTo)
	a.ValidFrom, a.ValidTo = utc(a.ValidFrom), utcPtr(a.ValidTo)
	return a, err
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// WorkerAsOf returns the worker's employments, primary position and manager on
// a date. The manager walk (BR-42.1) runs over a bounded recursive chain of the
// reports-to ancestors, so it reads a handful of rows rather than the tenant.
func (s *Postgres) WorkerAsOf(ctx context.Context, workerID string, at time.Time, date string) (AsOfView, error) {
	p := principal(ctx)
	view := AsOfView{WorkerID: workerID, Date: date, Employments: []Employment{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, workerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		rows, err := tx.Query(ctx, `select `+employmentColumns+` from employment
			where tenant_id = $1 and worker_id = $2 and validity @> $3::timestamptz order by valid_from`, p.TenantID, workerID, at)
		if err != nil {
			return err
		}
		for rows.Next() {
			e, err := scanEmployment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			view.Employments = append(view.Employments, e.statusAt(at))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var primary string
		var pnum, share string
		err = tx.QueryRow(ctx, `select pa.position_id::text, p.position_number, pa.fte_share::text from position_assignment pa
			join hr_position p on p.tenant_id = pa.tenant_id and p.id = pa.position_id
			where pa.tenant_id = $1 and pa.worker_id = $2 and pa.is_primary and pa.validity @> $3::timestamptz`, p.TenantID, workerID, at).Scan(&primary, &pnum, &share)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		view.PositionID, view.PositionNumber, view.FTEShare = &primary, &pnum, &share
		manager, err := s.manager(ctx, tx, p.TenantID, workerID, primary, at)
		view.ManagerWorkerID = manager
		return err
	})
	return view, err
}

// manager loads the reports-to chain above the worker's position as-of at, the
// incumbents on it and the edges, then applies domain.Manager.
func (s *Postgres) manager(ctx context.Context, tx pgx.Tx, tenantID, workerID, position string, at time.Time) (*string, error) {
	chainRows, err := tx.Query(ctx, `with recursive chain(position_id, depth) as (
			select $2::uuid, 0
			union all
			select h.parent_position_id, c.depth + 1 from chain c
			join position_hierarchy h on h.tenant_id = $1 and h.child_position_id = c.position_id and h.validity @> $3::timestamptz
			where c.depth < $4)
		select position_id::text from chain`, tenantID, position, at, s.depth)
	if err != nil {
		return nil, err
	}
	var chain []string
	for chainRows.Next() {
		var id string
		if err := chainRows.Scan(&id); err != nil {
			chainRows.Close()
			return nil, err
		}
		chain = append(chain, id)
	}
	chainRows.Close()
	if err := chainRows.Err(); err != nil {
		return nil, err
	}
	asRows, err := tx.Query(ctx, `select worker_id::text, position_id::text, is_primary, valid_from, valid_to from position_assignment
		where tenant_id = $1 and is_primary and validity @> $2::timestamptz and position_id = any($3::uuid[])`, tenantID, at, chain)
	if err != nil {
		return nil, err
	}
	var assignments []domain.Assignment
	for asRows.Next() {
		var a domain.Assignment
		if err := asRows.Scan(&a.WorkerID, &a.PositionID, &a.Primary, &a.Validity.From, &a.Validity.To); err != nil {
			asRows.Close()
			return nil, err
		}
		assignments = append(assignments, a)
	}
	asRows.Close()
	if err := asRows.Err(); err != nil {
		return nil, err
	}
	edgeRows, err := tx.Query(ctx, `select child_position_id::text, parent_position_id::text, valid_from, valid_to from position_hierarchy
		where tenant_id = $1 and child_position_id = any($2::uuid[]) and validity @> $3::timestamptz`, tenantID, chain, at)
	if err != nil {
		return nil, err
	}
	var edges []domain.Edge
	for edgeRows.Next() {
		var e domain.Edge
		if err := edgeRows.Scan(&e.ChildPositionID, &e.ParentPositionID, &e.Validity.From, &e.Validity.To); err != nil {
			edgeRows.Close()
			return nil, err
		}
		edges = append(edges, e)
	}
	edgeRows.Close()
	if err := edgeRows.Err(); err != nil {
		return nil, err
	}
	mgr, ok := domain.Manager(workerID, at, assignments, edges, s.depth)
	if !ok {
		return nil, nil
	}
	return &mgr, nil
}
