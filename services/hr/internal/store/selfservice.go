package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	CodeNoLinkedWorker  = "HCM-ESS-001"
	CodeAmbiguousWorker = "HCM-ESS-002"
	teamCTE             = `with recursive up(worker_id, pos, depth) as (
			select worker_id, position_id, 0 from position_assignment
			where tenant_id = $1 and is_primary and validity @> $2::timestamptz
			union all
			select u.worker_id, h.parent_position_id, u.depth + 1 from up u
			join position_hierarchy h on h.tenant_id = $1 and h.child_position_id = u.pos and h.validity @> $2::timestamptz
			where u.depth < $3),
		ranked as (
			select u.worker_id, m.worker_id as manager_id, u.depth, min(u.depth) over (partition by u.worker_id) as nearest
			from up u
			join position_assignment m on m.tenant_id = $1 and m.position_id = u.pos and m.is_primary and m.validity @> $2::timestamptz
			where u.depth > 0),
		team as (select worker_id, manager_id from ranked where depth = nearest and worker_id <> manager_id)`
)

// SelfWorkerID is the worker the signed-in user is linked to. The id comes
// from the caller's token, never from the request, so a self route cannot be
// pointed at another worker.
func (s *Postgres) SelfWorkerID(ctx context.Context) (string, error) {
	p := principal(ctx)
	if p.UserID == "" {
		return "", problem(http.StatusNotFound, CodeNoLinkedWorker, "no worker record is linked to your sign-in")
	}
	var found []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from worker where tenant_id = $1 and user_id = $2::uuid order by id limit 2`, p.TenantID, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			found = append(found, id)
		}
		return rows.Err()
	})
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", problem(http.StatusNotFound, CodeNoLinkedWorker, "no worker record is linked to your sign-in")
	case 1:
		return found[0], nil
	}
	return "", problem(http.StatusConflict, CodeAmbiguousWorker, "more than one worker record is linked to your sign-in")
}

// TeamPage is one keyset page of a manager's direct reports ordered by id.
type TeamPage struct {
	Items     []Worker
	NextAfter string
}

// Team lists the workers whose manager as-of at (BR-42.1, vacancies skipped
// up to the configured depth) is managerWorkerID.
func (s *Postgres) Team(ctx context.Context, managerWorkerID string, at time.Time, after string, limit int) (TeamPage, error) {
	p := principal(ctx)
	page := TeamPage{Items: []Worker{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, teamCTE+`
			select `+workerColumns+` from worker
			where tenant_id = $1 and id in (select worker_id from team where manager_id = $4::uuid) and id::text > $5
			order by id limit $6`, p.TenantID, at, s.depth, managerWorkerID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			w, err := scanWorker(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, w)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].ID
	}
	return page, err
}

// IsDirectReport answers whether managerWorkerID is the manager of workerID as-of at.
func (s *Postgres) IsDirectReport(ctx context.Context, managerWorkerID, workerID string, at time.Time) (bool, error) {
	p := principal(ctx)
	var ok bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, teamCTE+`
			select exists (select 1 from team where manager_id = $4::uuid and worker_id = $5::uuid)`,
			p.TenantID, at, s.depth, managerWorkerID, workerID).Scan(&ok)
	})
	return ok, err
}

// DirectReportsAmong returns the subset of workerIDs whose manager as-of at is managerWorkerID.
func (s *Postgres) DirectReportsAmong(ctx context.Context, managerWorkerID string, workerIDs []string, at time.Time) (map[string]bool, error) {
	p := principal(ctx)
	reports := map[string]bool{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := directReportsTx(ctx, tx, p.TenantID, managerWorkerID, workerIDs, at, s.depth)
		for _, id := range found {
			reports[id] = true
		}
		return err
	})
	return reports, err
}

func directReportsTx(ctx context.Context, tx pgx.Tx, tenantID, managerWorkerID string, workerIDs []string, at time.Time, depth int) ([]string, error) {
	if len(workerIDs) == 0 {
		return []string{}, nil
	}
	rows, err := tx.Query(ctx, teamCTE+`
		select worker_id::text from team where manager_id = $4::uuid and worker_id = any($5::uuid[])`,
		tenantID, at, depth, managerWorkerID, workerIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// TeamAbsenceRequests pages the absence requests of a manager's direct reports
// and the manager's own, oldest first, in the order of ListAbsenceRequests.
func (s *Postgres) TeamAbsenceRequests(ctx context.Context, managerWorkerID string, f RequestFilter, after string, limit int) (AbsenceRequestPage, error) {
	p := principal(ctx)
	page := AbsenceRequestPage{Items: []AbsenceRequest{}}
	afterDate, afterID, err := absCursor(after)
	if err != nil {
		return page, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, teamCTE+` `+absRequestSelect+`
			where r.tenant_id = $1 and ($7 = '' or r.status = $7) and ($8 = '' or r.worker_id::text = $8)
				and (r.worker_id = $4::uuid or r.worker_id in (select worker_id from team where manager_id = $4::uuid))
				and (r.date_from, r.id::text) > ($5::date, $6)
			order by r.date_from, r.id limit $9`, p.TenantID, s.now().UTC(), s.depth, managerWorkerID, afterDate, afterID, f.Status, f.WorkerID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := absScanRequest(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextAfter = last.From + "|" + last.ID
	}
	return page, err
}

func absCursor(after string) (string, string, error) {
	if after == "" {
		return "0001-01-01", "", nil
	}
	d, id, ok := strings.Cut(after, "|")
	if _, err := time.Parse(absDateLayout, d); !ok || err != nil || !ids.Valid(id) {
		return "", "", problem(http.StatusBadRequest, "COM-0001", "cursor must come from a previous page")
	}
	return d, id, nil
}

// RegistrationWorker is the worker a course registration belongs to.
func (s *Postgres) RegistrationWorker(ctx context.Context, registrationID string) (string, error) {
	p := principal(ctx)
	var workerID string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select worker_id::text from hr_course_registration where tenant_id = $1 and id = $2`, p.TenantID, registrationID).Scan(&workerID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problem(http.StatusNotFound, CodeRegistrationNotFound, "registration not found")
	}
	return workerID, err
}

// AbsenceRequestWorker is the worker an absence request belongs to.
func (s *Postgres) AbsenceRequestWorker(ctx context.Context, requestID string) (string, error) {
	p := principal(ctx)
	var workerID string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select worker_id::text from absence_request where tenant_id = $1 and id = $2`, p.TenantID, requestID).Scan(&workerID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problem(http.StatusNotFound, CodeAbsenceRequestNotFound, "absence request not found")
	}
	return workerID, err
}
