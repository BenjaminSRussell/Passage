package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeGoalNotFound   = "HCM-GOAL-NOT-FOUND"
	CodeReviewNotFound = "HCM-REV-NOT-FOUND"
	CodeReviewState    = "HCM-REV-STATE"
	CodeReviewSelf     = "HCM-REV-SELF"
	CodeGoalState      = "HCM-GOAL-STATE"
	CodeGoalSelf       = "HCM-GOAL-SELF"

	maxPerformanceRows = 500
)

// Goal is a worker's goal for a period of work (FR-42.11).
type Goal struct {
	ID            string    `json:"id"`
	WorkerID      string    `json:"workerId"`
	LegalEntityID *string   `json:"legalEntityId,omitempty"`
	Title         string    `json:"title"`
	Description   *string   `json:"description,omitempty"`
	DueOn         *string   `json:"dueOn,omitempty"`
	Status        string    `json:"status"`
	ProgressPct   int       `json:"progressPercent"`
	CreatedBy     string    `json:"createdBy"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Review is the two-step review of one worker for one period: draft, then the
// worker's self-assessment (self_submitted), then the manager's sign-off
// (signed_off). There is one template; no calibration or rating distribution.
type Review struct {
	ID               string     `json:"id"`
	WorkerID         string     `json:"workerId"`
	LegalEntityID    *string    `json:"legalEntityId,omitempty"`
	PeriodStart      string     `json:"periodStart"`
	PeriodEnd        string     `json:"periodEnd"`
	Status           string     `json:"status"`
	SelfAchievements *string    `json:"selfAchievements,omitempty"`
	SelfDevelopment  *string    `json:"selfDevelopment,omitempty"`
	SelfSubmittedAt  *time.Time `json:"selfSubmittedAt,omitempty"`
	ManagerComments  *string    `json:"managerComments,omitempty"`
	OverallRating    *int       `json:"overallRating,omitempty"`
	SignedOffBy      *string    `json:"signedOffBy,omitempty"`
	SignedOffAt      *time.Time `json:"signedOffAt,omitempty"`
	CreatedBy        string     `json:"createdBy"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

const goalColumns = `id::text, worker_id::text, legal_entity_id::text, title, description, to_char(due_on, 'YYYY-MM-DD'), status, progress_pct, created_by, created_at, updated_at`

const reviewColumns = `id::text, worker_id::text, legal_entity_id::text, to_char(period_start, 'YYYY-MM-DD'), to_char(period_end, 'YYYY-MM-DD'), status,
	self_achievements, self_development, self_submitted_at, manager_comments, overall_rating, signed_off_by, signed_off_at, created_by, created_at, updated_at`

func scanGoal(row pgx.Row) (Goal, error) {
	var g Goal
	err := row.Scan(&g.ID, &g.WorkerID, &g.LegalEntityID, &g.Title, &g.Description, &g.DueOn, &g.Status, &g.ProgressPct, &g.CreatedBy, &g.CreatedAt, &g.UpdatedAt)
	g.CreatedAt, g.UpdatedAt = utc(g.CreatedAt), utc(g.UpdatedAt)
	return g, err
}

func scanReview(row pgx.Row) (Review, error) {
	var r Review
	err := row.Scan(&r.ID, &r.WorkerID, &r.LegalEntityID, &r.PeriodStart, &r.PeriodEnd, &r.Status, &r.SelfAchievements, &r.SelfDevelopment,
		&r.SelfSubmittedAt, &r.ManagerComments, &r.OverallRating, &r.SignedOffBy, &r.SignedOffAt, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	r.CreatedAt, r.UpdatedAt = utc(r.CreatedAt), utc(r.UpdatedAt)
	r.SelfSubmittedAt, r.SignedOffAt = utcPtr(r.SelfSubmittedAt), utcPtr(r.SignedOffAt)
	return r, err
}

func reviewEvent(r Review) hrevents.ReviewChangedData {
	return hrevents.ReviewChangedData{ID: r.ID, WorkerID: r.WorkerID, Status: r.Status, PeriodStart: r.PeriodStart, PeriodEnd: r.PeriodEnd}
}

func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func performanceEntity(ctx context.Context, tx pgx.Tx, tenantID, workerID, on string, requested *string) (*string, error) {
	entity, err := workerEntity(ctx, tx, tenantID, workerID, on, requested)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, problem(http.StatusUnprocessableEntity, CodeEntityRequired, "the worker has no employment in a legal entity you can see")
	}
	return entity, nil
}

func requireWorker(ctx context.Context, tx pgx.Tx, tenantID, workerID string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, tenantID, workerID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return nil
}

func sameGoalRequest(stored, in Goal) bool {
	return stored.WorkerID == in.WorkerID && stored.Title == in.Title && samePtr(stored.Description, in.Description) &&
		samePtr(stored.DueOn, in.DueOn) && (in.LegalEntityID == nil || samePtr(stored.LegalEntityID, in.LegalEntityID))
}

// CreateGoal adds a goal for a worker in the legal entity of their employment
// today. A replayed Idempotency-Key returns the stored goal.
func (s *Postgres) CreateGoal(ctx context.Context, in Goal, idempotencyKey string) (Goal, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out Goal
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanGoal(tx.QueryRow(ctx, `select `+goalColumns+` from hr_goal where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := requireWorker(ctx, tx, p.TenantID, in.WorkerID); err != nil {
			return err
		}
		entity, err := performanceEntity(ctx, tx, p.TenantID, in.WorkerID, s.now().UTC().Format(absDateLayout), in.LegalEntityID)
		if err != nil {
			return err
		}
		out, err = scanGoal(tx.QueryRow(ctx, `insert into hr_goal (tenant_id, id, worker_id, legal_entity_id, title, description, due_on, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9) returning `+goalColumns,
			p.TenantID, ids.New(), in.WorkerID, entity, in.Title, in.Description, in.DueOn, p.UserID, key))
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return Goal{}, false, idempotencyInFlight()
	}
	if pgCode(err, rlsViolation) {
		return Goal{}, false, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	if err == nil && !inserted && !sameGoalRequest(out, in) {
		return Goal{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// ListWorkerGoals returns a worker's goals, newest first, at most 500.
func (s *Postgres) ListWorkerGoals(ctx context.Context, workerID string) ([]Goal, error) {
	p := principal(ctx)
	out := []Goal{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := requireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+goalColumns+` from hr_goal where tenant_id = $1 and worker_id = $2 order by created_at desc, id limit $3`,
			p.TenantID, workerID, maxPerformanceRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			g, err := scanGoal(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// GoalWorker is the worker a goal belongs to, for confining a manager to their team.
func (s *Postgres) GoalWorker(ctx context.Context, id string) (string, error) {
	return s.ownerOf(ctx, `select worker_id::text from hr_goal where tenant_id = $1 and id = $2`, id, CodeGoalNotFound, "goal not found")
}

// ReviewWorker is the worker a review belongs to.
func (s *Postgres) ReviewWorker(ctx context.Context, id string) (string, error) {
	return s.ownerOf(ctx, `select worker_id::text from hr_review where tenant_id = $1 and id = $2`, id, CodeReviewNotFound, "review not found")
}

func (s *Postgres) ownerOf(ctx context.Context, query, id, code, detail string) (string, error) {
	p := principal(ctx)
	var worker string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, p.TenantID, id).Scan(&worker)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problem(http.StatusNotFound, code, detail)
	}
	return worker, err
}

// UpdateGoal sets a goal's status and progress. Either may be nil to keep the
// stored value. Marking a goal ACHIEVED without a progress value sets 100.
func (s *Postgres) UpdateGoal(ctx context.Context, id string, status *string, progress *int) (Goal, error) {
	p := principal(ctx)
	var out Goal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := scanGoal(tx.QueryRow(ctx, `select `+goalColumns+` from hr_goal where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeGoalNotFound, "goal not found")
		}
		if err != nil {
			return err
		}
		newStatus, newProgress := current.Status, current.ProgressPct
		if status != nil {
			newStatus = *status
		}
		if progress != nil {
			newProgress = *progress
		} else if status != nil && *status == domain.GoalAchieved {
			newProgress = 100
		}
		if newStatus == current.Status && newProgress == current.ProgressPct {
			out = current
			return nil
		}
		if current.Status == domain.GoalCancelled {
			return problem(http.StatusConflict, CodeGoalState, "a cancelled goal cannot change")
		}
		if newStatus == domain.GoalAchieved && newProgress < 100 {
			return problem(http.StatusUnprocessableEntity, CodeGoalState, "an achieved goal must be at 100 percent")
		}
		out, err = scanGoal(tx.QueryRow(ctx, `update hr_goal set status = $3, progress_pct = $4, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+goalColumns, p.TenantID, id, newStatus, newProgress))
		return err
	})
	return out, err
}

// OpenReview starts the review of a worker for a period in draft. A review for
// the same worker and period that exists is returned with inserted false.
func (s *Postgres) OpenReview(ctx context.Context, workerID string, legalEntityID *string, start, end time.Time, idempotencyKey string) (Review, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out Review
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanReview(tx.QueryRow(ctx, `select `+reviewColumns+` from hr_review where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if err := requireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		entity, err := performanceEntity(ctx, tx, p.TenantID, workerID, end.Format(absDateLayout), legalEntityID)
		if err != nil {
			return err
		}
		out, err = scanReview(tx.QueryRow(ctx, `insert into hr_review (tenant_id, id, worker_id, legal_entity_id, period_start, period_end, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5::date, $6::date, $7, $8)
			on conflict (tenant_id, worker_id, period_start, period_end) do nothing returning `+reviewColumns,
			p.TenantID, ids.New(), workerID, entity, start.Format(absDateLayout), end.Format(absDateLayout), p.UserID, key))
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanReview(tx.QueryRow(ctx, `select `+reviewColumns+` from hr_review where tenant_id = $1 and worker_id = $2 and period_start = $3::date and period_end = $4::date`,
				p.TenantID, workerID, start.Format(absDateLayout), end.Format(absDateLayout)))
			return err
		}
		if err != nil {
			return err
		}
		inserted = true
		return enqueue(ctx, tx, "review", out.ID, hrevents.ReviewChanged, p.TenantID, reviewEvent(out))
	})
	if idempotencyRace(err) {
		return Review{}, false, idempotencyInFlight()
	}
	if pgCode(err, rlsViolation) {
		return Review{}, false, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	if err == nil && !inserted && key != nil && (out.WorkerID != workerID || out.PeriodStart != start.Format(absDateLayout) || out.PeriodEnd != end.Format(absDateLayout) ||
		(legalEntityID != nil && !samePtr(out.LegalEntityID, legalEntityID))) {
		return Review{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// GetReview returns one review.
func (s *Postgres) GetReview(ctx context.Context, id string) (Review, error) {
	p := principal(ctx)
	var out Review
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanReview(tx.QueryRow(ctx, `select `+reviewColumns+` from hr_review where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, problem(http.StatusNotFound, CodeReviewNotFound, "review not found")
	}
	return out, err
}

// ListWorkerReviews returns a worker's reviews, latest period first, at most 500.
func (s *Postgres) ListWorkerReviews(ctx context.Context, workerID string) ([]Review, error) {
	p := principal(ctx)
	out := []Review{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := requireWorker(ctx, tx, p.TenantID, workerID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+reviewColumns+` from hr_review where tenant_id = $1 and worker_id = $2 order by period_start desc, id limit $3`,
			p.TenantID, workerID, maxPerformanceRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReview(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func lockReview(ctx context.Context, tx pgx.Tx, tenantID, id string) (Review, error) {
	r, err := scanReview(tx.QueryRow(ctx, `select `+reviewColumns+` from hr_review where tenant_id = $1 and id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Review{}, problem(http.StatusNotFound, CodeReviewNotFound, "review not found")
	}
	return r, err
}

// SubmitSelfAssessment is step one: the reviewed worker writes their
// assessment and the review moves draft -> self_submitted. Only the reviewed
// worker (selfWorkerID) can; anyone else sees the review as not found. The same
// text again changes nothing; once submitted the text cannot change.
func (s *Postgres) SubmitSelfAssessment(ctx context.Context, id, selfWorkerID, achievements string, development *string) (Review, bool, error) {
	p := principal(ctx)
	var out Review
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockReview(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if cur.WorkerID != selfWorkerID {
			return problem(http.StatusNotFound, CodeReviewNotFound, "review not found")
		}
		if cur.Status != domain.ReviewDraft {
			if cur.Status == domain.ReviewSelfSubmitted && cur.SelfAchievements != nil && *cur.SelfAchievements == achievements && samePtr(cur.SelfDevelopment, development) {
				out = cur
				return nil
			}
			return problem(http.StatusConflict, CodeReviewState, "the self-assessment was already submitted and the review is "+cur.Status)
		}
		out, err = scanReview(tx.QueryRow(ctx, `update hr_review set status = $3, self_achievements = $4, self_development = $5, self_submitted_at = now(), updated_at = now()
			where tenant_id = $1 and id = $2 returning `+reviewColumns, p.TenantID, id, domain.ReviewSelfSubmitted, achievements, development))
		if err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "review", out.ID, hrevents.ReviewChanged, p.TenantID, reviewEvent(out))
	})
	return out, changed, err
}

// SignOffReview is step two: the manager signs off a self_submitted review. The
// reviewed worker cannot sign their own review (signerWorkerID is the signer's
// own worker, empty when the signer has none). The same sign-off again changes
// nothing; a draft review cannot be signed.
func (s *Postgres) SignOffReview(ctx context.Context, id, signerWorkerID string, comments *string, rating *int) (Review, bool, error) {
	p := principal(ctx)
	var out Review
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockReview(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if signerWorkerID != "" && cur.WorkerID == signerWorkerID {
			return problem(http.StatusForbidden, CodeReviewSelf, "you cannot sign off your own review")
		}
		switch cur.Status {
		case domain.ReviewSignedOff:
			if samePtr(cur.ManagerComments, comments) && samePtr(cur.OverallRating, rating) {
				out = cur
				return nil
			}
			return problem(http.StatusConflict, CodeReviewState, "the review is already signed off")
		case domain.ReviewDraft:
			return problem(http.StatusConflict, CodeReviewState, "the worker has not submitted their self-assessment")
		}
		out, err = scanReview(tx.QueryRow(ctx, `update hr_review set status = $3, manager_comments = $4, overall_rating = $5, signed_off_by = $6, signed_off_at = now(), updated_at = now()
			where tenant_id = $1 and id = $2 returning `+reviewColumns, p.TenantID, id, domain.ReviewSignedOff, comments, rating, p.UserID))
		if err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "review", out.ID, hrevents.ReviewChanged, p.TenantID, reviewEvent(out))
	})
	return out, changed, err
}
