package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// CodeActivityNotFound is returned when an activity is not found.
const CodeActivityNotFound = "SVC-ACT-001"

// Association ties an activity to any record by registry name (FR-45.14).
type Association struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
}

// Activity is a task, appointment or note owned by one worker (FR-45.14).
type Activity struct {
	ID              string        `json:"id"`
	Kind            string        `json:"kind"`
	Subject         string        `json:"subject"`
	OwnerWorkerID   string        `json:"ownerWorkerId"`
	DueAt           *time.Time    `json:"dueAt,omitempty"`
	StartAt         *time.Time    `json:"startAt,omitempty"`
	EndAt           *time.Time    `json:"endAt,omitempty"`
	Status          string        `json:"status"`
	ReminderMinutes *int          `json:"reminderMinutes,omitempty"`
	Associations    []Association `json:"associations"`
	CompletedAt     *time.Time    `json:"completedAt,omitempty"`
	CreatedAt       time.Time     `json:"createdAt"`
	// Version is the optimistic-concurrency version; it is the ETag of the activity.
	Version int `json:"version"`
}

// Schedule is the set of dates an activity carries; a reschedule replaces all three.
type Schedule struct {
	DueAt   *time.Time
	StartAt *time.Time
	EndAt   *time.Time
}

const activityColumns = `a.id::text, a.kind, a.subject, a.owner_worker_id::text, a.due_at, a.start_at, a.end_at, a.status,
	a.reminder_minutes, a.associations, a.completed_at, a.created_at, a.version`

func scanActivity(row pgx.Row) (Activity, error) {
	var a Activity
	var assoc []byte
	if err := row.Scan(&a.ID, &a.Kind, &a.Subject, &a.OwnerWorkerID, &a.DueAt, &a.StartAt, &a.EndAt, &a.Status,
		&a.ReminderMinutes, &assoc, &a.CompletedAt, &a.CreatedAt, &a.Version); err != nil {
		return a, err
	}
	a.Associations = []Association{}
	return a, json.Unmarshal(assoc, &a.Associations)
}

// CreateActivity stores a new OPEN activity. A replay with the same
// Idempotency-Key returns the stored activity.
func (s *Postgres) CreateActivity(ctx context.Context, in Activity, key, requestHash string) (Activity, bool, error) {
	tid := tenantOf(ctx)
	var out Activity
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_activity", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getActivity(ctx, tx, tid, id, false)
			return err
		}
		if in.Associations == nil {
			in.Associations = []Association{}
		}
		assoc, err := json.Marshal(in.Associations)
		if err != nil {
			return err
		}
		newID := ids.New()
		if _, err := tx.Exec(ctx, `insert into service_activity (tenant_id, id, kind, subject, owner_worker_id, due_at, start_at, end_at,
			status, reminder_minutes, associations, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, 'OPEN', $9, $10, $11, $12)`,
			tid, newID, in.Kind, in.Subject, in.OwnerWorkerID, in.DueAt, in.StartAt, in.EndAt,
			in.ReminderMinutes, assoc, nullable(key), requestHash); err != nil {
			return err
		}
		out, err = getActivity(ctx, tx, tid, newID, false)
		return err
	})
	return out, replayed, err
}

// CompleteActivity moves an OPEN activity to DONE and stamps completed_at
// (AC-45.15). Completing a DONE activity changes nothing.
func (s *Postgres) CompleteActivity(ctx context.Context, id string, version int) (Activity, error) {
	return s.moveActivity(ctx, id, version, domain.CompleteActivity, `completed_at = now()`)
}

// CancelActivity moves an OPEN activity to CANCELED. Canceling a CANCELED activity changes nothing.
func (s *Postgres) CancelActivity(ctx context.Context, id string, version int) (Activity, error) {
	return s.moveActivity(ctx, id, version, domain.CancelActivity, `completed_at = null`)
}

func (s *Postgres) moveActivity(ctx context.Context, id string, version int, move func(string) (string, error), set string) (Activity, error) {
	tid := tenantOf(ctx)
	var out Activity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getActivity(ctx, tx, tid, id, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		next, err := move(cur.Status)
		if err != nil {
			return ruleProblem(err)
		}
		if next == cur.Status {
			out = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `update service_activity set status = $3, `+set+`, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tid, id, next); err != nil {
			return err
		}
		out, err = getActivity(ctx, tx, tid, id, false)
		return err
	})
	return out, err
}

// RescheduleActivity replaces the due, start and end of an OPEN activity (PRD
// 8.45.6 "My activities: reschedule").
func (s *Postgres) RescheduleActivity(ctx context.Context, id string, version int, when Schedule) (Activity, error) {
	tid := tenantOf(ctx)
	var out Activity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getActivity(ctx, tx, tid, id, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		if err := domain.CanReschedule(cur.Status); err != nil {
			return ruleProblem(err)
		}
		if _, err := tx.Exec(ctx, `update service_activity set due_at = $3, start_at = $4, end_at = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, tid, id, when.DueAt, when.StartAt, when.EndAt); err != nil {
			return err
		}
		out, err = getActivity(ctx, tx, tid, id, false)
		return err
	})
	return out, err
}

// GetActivity loads one activity; a missing one is a 404.
func (s *Postgres) GetActivity(ctx context.Context, id string) (Activity, error) {
	tid := tenantOf(ctx)
	var a Activity
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		a, err = getActivity(ctx, tx, tid, id, false)
		return err
	})
	return a, err
}

// ActivityFilter selects activities by owner ("My activities") or by the record
// they are associated with; empty strings match everything.
type ActivityFilter struct {
	OwnerWorkerID string
	Status        string
	EntityType    string
	EntityID      string
	Before        string
	Limit         int
}

// ListActivities returns activities earliest due first (undated last), then
// oldest first. The cursor is the id of the last activity of the previous page.
func (s *Postgres) ListActivities(ctx context.Context, f ActivityFilter) (Page[Activity], error) {
	// '[]' is contained in every associations array, so it matches everything.
	assoc := "[]"
	if f.EntityType != "" {
		b, err := json.Marshal([]Association{{EntityType: f.EntityType, EntityID: f.EntityID}})
		if err != nil {
			return Page[Activity]{}, err
		}
		assoc = string(b)
	}
	return listPage(ctx, s, f.Limit, func(a Activity) string { return a.ID }, scanActivity,
		`select `+activityColumns+` from service_activity a
		where a.tenant_id = $1 and ($2 = '' or a.owner_worker_id::text = $2) and ($3 = '' or a.status = $3)
			and a.associations @> $4::jsonb
			and ($5 = '' or (coalesce(a.due_at, 'infinity'::timestamptz), a.created_at, a.id) >
				(select coalesce(c.due_at, 'infinity'::timestamptz), c.created_at, c.id from service_activity c where c.tenant_id = $1 and c.id::text = $5))
		order by coalesce(a.due_at, 'infinity'::timestamptz), a.created_at, a.id limit $6`,
		tenantOf(ctx), f.OwnerWorkerID, f.Status, assoc, f.Before)
}

func getActivity(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Activity, error) {
	q := `select ` + activityColumns + ` from service_activity a where a.tenant_id = $1 and a.id = $2`
	if lock {
		q += ` for update`
	}
	a, err := scanActivity(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, httpx.NewProblem(http.StatusNotFound, CodeActivityNotFound, "activity not found")
	}
	return a, err
}

// ruleProblem converts a domain rule violation into a 422 problem with its code.
func ruleProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return unprocessable(de.Code, de.Detail)
	}
	return err
}
