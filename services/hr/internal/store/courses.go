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
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// Course problem codes raised by the store (FR-42.14). None is in the 8.42.18
// catalog; the service README lists them. The seat, state and outcome codes
// are domain.CodeCourse*.
const (
	CodeCourseNotFound       = "HCM-CRS-NOT-FOUND"
	CodeCourseDuplicate      = "HCM-CRS-DUPLICATE"
	CodeSessionNotFound      = "HCM-CRS-SESSION-NOT-FOUND"
	CodeRegistrationNotFound = "HCM-CRS-REG-NOT-FOUND"
	CodeNotOwnCourse         = "HCM-CRS-NOT-OWN"

	// maxCourseRows bounds the sessions of a course and the registrations of
	// a session returned in one read.
	maxCourseRows = 500
)

// CourseSkillOutcome is a skill a course trains. A PASS raises the worker's
// rating to Rating when that is higher (BR-42.9).
type CourseSkillOutcome struct {
	SkillID   string `json:"skillId"`
	SkillCode string `json:"skillCode"`
	SkillName string `json:"skillName"`
	Rating    int    `json:"rating"`
}

// Course is a catalog course with the skills and certificate a PASS yields.
// ValidityMonths 0 issues a certificate that does not expire.
type Course struct {
	ID                      string               `json:"id"`
	Code                    string               `json:"code"`
	Name                    string               `json:"name"`
	CourseGroup             *string              `json:"courseGroup,omitempty"`
	SkillOutcomes           []CourseSkillOutcome `json:"skillOutcomes"`
	CertificationCode       *string              `json:"certificationCode,omitempty"`
	CertificationName       *string              `json:"certificationName,omitempty"`
	ValidityMonths          int                  `json:"validityMonths"`
	RequiresManagerApproval bool                 `json:"requiresManagerApproval"`
	CreatedAt               time.Time            `json:"createdAt"`
}

// CoursePage is one keyset page of the catalog ordered by code.
type CoursePage struct {
	Items     []Course
	NextAfter string
}

// Session is one scheduled run of a course. Confirmed, Waitlisted, Requested
// and SeatsLeft are counted at read time. CostPerAttendee is a decimal string
// in Currency.
type Session struct {
	ID                 string    `json:"id"`
	CourseID           string    `json:"courseId"`
	CourseCode         string    `json:"courseCode"`
	CourseName         string    `json:"courseName"`
	StartsAt           time.Time `json:"startsAt"`
	EndsAt             time.Time `json:"endsAt"`
	Location           *string   `json:"location,omitempty"`
	VirtualURL         *string   `json:"virtualUrl,omitempty"`
	InstructorWorkerID *string   `json:"instructorWorkerId,omitempty"`
	InstructorPartyID  *string   `json:"instructorPartyId,omitempty"`
	Capacity           int       `json:"capacity"`
	WaitlistCapacity   int       `json:"waitlistCapacity"`
	CostPerAttendee    *string   `json:"costPerAttendee,omitempty"`
	Currency           *string   `json:"currency,omitempty"`
	Confirmed          int       `json:"confirmed"`
	Waitlisted         int       `json:"waitlisted"`
	Requested          int       `json:"requested"`
	SeatsLeft          int       `json:"seatsLeft"`
	CreatedAt          time.Time `json:"createdAt"`
}

// Registration is one worker's place in a session. Result and Score are set
// only on ATTENDED; CompletedOn (the session end date) on ATTENDED and
// NO_SHOW.
type Registration struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"sessionId"`
	WorkerID        string    `json:"workerId"`
	PersonnelNumber string    `json:"personnelNumber"`
	LegalName       string    `json:"legalName"`
	Status          string    `json:"status"`
	Result          *string   `json:"result,omitempty"`
	Score           *string   `json:"score,omitempty"`
	CompletedOn     *string   `json:"completedOn,omitempty"`
	QueuedAt        time.Time `json:"queuedAt"`
}

// Outcome is one attendance line of a session completion. Score is a decimal
// string or "".
type Outcome struct {
	WorkerID string
	Status   string
	Result   string
	Score    string
}

const courseColumns = `id::text, code, name, course_group, certification_code, certification_name,
	validity_months, requires_manager_approval, created_at`

const sessionSelect = `select s.id::text, s.course_id::text, c.code, c.name, s.starts_at, s.ends_at, s.location_text, s.virtual_url,
	s.instructor_worker_id::text, s.instructor_party_id::text, s.capacity, s.waitlist_capacity, s.cost_per_attendee::text, s.currency, s.created_at,
	(select count(*) filter (where r.status = 'CONFIRMED')::int from hr_course_registration r where r.tenant_id = s.tenant_id and r.session_id = s.id),
	(select count(*) filter (where r.status = 'WAITLISTED')::int from hr_course_registration r where r.tenant_id = s.tenant_id and r.session_id = s.id),
	(select count(*) filter (where r.status = 'REQUESTED')::int from hr_course_registration r where r.tenant_id = s.tenant_id and r.session_id = s.id)
	from hr_course_session s join hr_course c on c.tenant_id = s.tenant_id and c.id = s.course_id`

const registrationSelect = `select r.id::text, r.session_id::text, r.worker_id::text, w.personnel_number, w.legal_name, r.status, r.result,
	r.score::text, to_char(r.completed_on, 'YYYY-MM-DD'), r.queued_at
	from hr_course_registration r join worker w on w.tenant_id = r.tenant_id and w.id = r.worker_id`

func crsScanCourse(row pgx.Row) (Course, error) {
	var c Course
	err := row.Scan(&c.ID, &c.Code, &c.Name, &c.CourseGroup, &c.CertificationCode, &c.CertificationName,
		&c.ValidityMonths, &c.RequiresManagerApproval, &c.CreatedAt)
	c.CreatedAt = utc(c.CreatedAt)
	c.SkillOutcomes = []CourseSkillOutcome{}
	return c, err
}

func crsScanSession(row pgx.Row) (Session, error) {
	var s Session
	var currency *string
	err := row.Scan(&s.ID, &s.CourseID, &s.CourseCode, &s.CourseName, &s.StartsAt, &s.EndsAt, &s.Location, &s.VirtualURL,
		&s.InstructorWorkerID, &s.InstructorPartyID, &s.Capacity, &s.WaitlistCapacity, &s.CostPerAttendee, &currency, &s.CreatedAt,
		&s.Confirmed, &s.Waitlisted, &s.Requested)
	s.Currency = currency
	if s.CostPerAttendee != nil && currency != nil {
		if d, derr := decimal.NewFromString(*s.CostPerAttendee); derr == nil {
			v := d.StringFixed(money.MinorUnits(*currency))
			s.CostPerAttendee = &v
		}
	}
	s.StartsAt, s.EndsAt, s.CreatedAt = utc(s.StartsAt), utc(s.EndsAt), utc(s.CreatedAt)
	s.SeatsLeft = max(0, s.Capacity-s.Confirmed)
	return s, err
}

func crsScanRegistration(row pgx.Row) (Registration, error) {
	var r Registration
	err := row.Scan(&r.ID, &r.SessionID, &r.WorkerID, &r.PersonnelNumber, &r.LegalName, &r.Status, &r.Result, &r.Score, &r.CompletedOn, &r.QueuedAt)
	r.QueuedAt = utc(r.QueuedAt)
	return r, err
}

func (r Registration) recorded() domain.Recorded {
	return domain.Recorded{Status: r.Status, Result: deref(r.Result), Score: deref(r.Score)}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// crsOutcomes reads a course's skill outcomes ordered by skill code.
func crsOutcomes(ctx context.Context, tx pgx.Tx, tenantID, courseID string) ([]CourseSkillOutcome, error) {
	rows, err := tx.Query(ctx, `select k.id::text, k.code, k.name, o.rating
		from hr_course_skill_outcome o join hr_skill k on k.tenant_id = o.tenant_id and k.id = o.skill_id
		where o.tenant_id = $1 and o.course_id = $2 order by k.code`, tenantID, courseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CourseSkillOutcome{}
	for rows.Next() {
		var o CourseSkillOutcome
		if err := rows.Scan(&o.SkillID, &o.SkillCode, &o.SkillName, &o.Rating); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// crsAttachOutcomes reads the skill outcomes of a page of courses in one query.
func crsAttachOutcomes(ctx context.Context, tx pgx.Tx, tenantID string, courses []Course) error {
	if len(courses) == 0 {
		return nil
	}
	idx := make(map[string]int, len(courses))
	courseIDs := make([]string, len(courses))
	for i, c := range courses {
		idx[c.ID] = i
		courseIDs[i] = c.ID
	}
	rows, err := tx.Query(ctx, `select o.course_id::text, k.id::text, k.code, k.name, o.rating
		from hr_course_skill_outcome o join hr_skill k on k.tenant_id = o.tenant_id and k.id = o.skill_id
		where o.tenant_id = $1 and o.course_id = any($2::uuid[]) order by k.code`, tenantID, courseIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var courseID string
		var o CourseSkillOutcome
		if err := rows.Scan(&courseID, &o.SkillID, &o.SkillCode, &o.SkillName, &o.Rating); err != nil {
			return err
		}
		i := idx[courseID]
		courses[i].SkillOutcomes = append(courses[i].SkillOutcomes, o)
	}
	return rows.Err()
}

// crsCourse reads a course with its outcomes inside a tenant transaction.
func crsCourse(ctx context.Context, tx pgx.Tx, tenantID, where string, arg any) (Course, error) {
	c, err := crsScanCourse(tx.QueryRow(ctx, `select `+courseColumns+` from hr_course where tenant_id = $1 and `+where, tenantID, arg))
	if err != nil {
		return Course{}, err
	}
	c.SkillOutcomes, err = crsOutcomes(ctx, tx, tenantID, c.ID)
	return c, err
}

func crsCourseByID(ctx context.Context, tx pgx.Tx, tenantID, id string) (Course, error) {
	c, err := crsCourse(ctx, tx, tenantID, "id = $2", id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Course{}, problem(http.StatusNotFound, CodeCourseNotFound, "course not found")
	}
	return c, err
}

func crsSession(ctx context.Context, tx pgx.Tx, tenantID, id string) (Session, error) {
	s, err := crsScanSession(tx.QueryRow(ctx, sessionSelect+` where s.tenant_id = $1 and s.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, problem(http.StatusNotFound, CodeSessionNotFound, "session not found")
	}
	return s, err
}

func crsRegistration(ctx context.Context, tx pgx.Tx, tenantID, where string, args ...any) (Registration, error) {
	return crsScanRegistration(tx.QueryRow(ctx, registrationSelect+` where r.tenant_id = $1 and `+where, append([]any{tenantID}, args...)...))
}

func crsRegistrationByID(ctx context.Context, tx pgx.Tx, tenantID, id string) (Registration, error) {
	r, err := crsRegistration(ctx, tx, tenantID, "r.id = $2", id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Registration{}, problem(http.StatusNotFound, CodeRegistrationNotFound, "registration not found")
	}
	return r, err
}

// sessionLock is what seat allocation needs from a locked session row.
type sessionLock struct {
	courseID         string
	startsAt, endsAt time.Time
	capacity         int
	waitlistCapacity int
	approval         bool
}

// crsLockSession locks the session row FOR UPDATE, so registrations,
// approvals, cancellations and completions in one session run one at a time
// and seat counts stay exact (BR-42.9: seats in registration order).
func crsLockSession(ctx context.Context, tx pgx.Tx, tenantID, sessionID string) (sessionLock, error) {
	var l sessionLock
	err := tx.QueryRow(ctx, `select s.course_id::text, s.starts_at, s.ends_at, s.capacity, s.waitlist_capacity, c.requires_manager_approval
		from hr_course_session s join hr_course c on c.tenant_id = s.tenant_id and c.id = s.course_id
		where s.tenant_id = $1 and s.id = $2 for update of s`, tenantID, sessionID).
		Scan(&l.courseID, &l.startsAt, &l.endsAt, &l.capacity, &l.waitlistCapacity, &l.approval)
	if errors.Is(err, pgx.ErrNoRows) {
		return sessionLock{}, problem(http.StatusNotFound, CodeSessionNotFound, "session not found")
	}
	return l, err
}

// crsLockRegistration locks the session first, then reads the registration
// under that lock, so its status cannot change between the read and the write.
func crsLockRegistration(ctx context.Context, tx pgx.Tx, tenantID, id string) (Registration, sessionLock, error) {
	var sessionID string
	err := tx.QueryRow(ctx, `select session_id::text from hr_course_registration where tenant_id = $1 and id = $2`, tenantID, id).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Registration{}, sessionLock{}, problem(http.StatusNotFound, CodeRegistrationNotFound, "registration not found")
	}
	if err != nil {
		return Registration{}, sessionLock{}, err
	}
	lock, err := crsLockSession(ctx, tx, tenantID, sessionID)
	if err != nil {
		return Registration{}, sessionLock{}, err
	}
	reg, err := crsRegistrationByID(ctx, tx, tenantID, id)
	return reg, lock, err
}

// crsSeat allocates a place in a locked session or refuses with HCM-CRS-001.
func crsSeat(ctx context.Context, tx pgx.Tx, tenantID, sessionID string, l sessionLock) (string, error) {
	var confirmed, waitlisted int
	if err := tx.QueryRow(ctx, `select count(*) filter (where status = 'CONFIRMED')::int, count(*) filter (where status = 'WAITLISTED')::int
		from hr_course_registration where tenant_id = $1 and session_id = $2`, tenantID, sessionID).Scan(&confirmed, &waitlisted); err != nil {
		return "", err
	}
	status, ok := domain.Seat(confirmed, waitlisted, l.capacity, l.waitlistCapacity)
	if !ok {
		return "", problem(http.StatusUnprocessableEntity, domain.CodeCourseFull, "the session and its waitlist are full; pick another session")
	}
	return status, nil
}

// crsCheckOwn refuses an employee-only caller acting on another worker's
// registration (hr.learning.self is own data, 8.42.17).
func crsCheckOwn(ownOnly bool, callerUserID string, workerUser *string) error {
	if !ownOnly {
		return nil
	}
	if workerUser == nil || callerUserID == "" || *workerUser != callerUserID {
		return problem(http.StatusForbidden, CodeNotOwnCourse, "you can only register yourself and cancel your own registrations")
	}
	return nil
}

func sameCourse(stored, in Course) bool {
	if stored.Code != in.Code || stored.Name != in.Name || !sameStringPtr(stored.CourseGroup, in.CourseGroup) ||
		!sameStringPtr(stored.CertificationCode, in.CertificationCode) || !sameStringPtr(stored.CertificationName, in.CertificationName) ||
		stored.ValidityMonths != in.ValidityMonths || stored.RequiresManagerApproval != in.RequiresManagerApproval ||
		len(stored.SkillOutcomes) != len(in.SkillOutcomes) {
		return false
	}
	want := map[string]int{}
	for _, o := range in.SkillOutcomes {
		want[o.SkillCode] = o.Rating
	}
	for _, o := range stored.SkillOutcomes {
		if r, ok := want[o.SkillCode]; !ok || r != o.Rating {
			return false
		}
	}
	return true
}

// CreateCourse adds a catalog course (FR-42.14). Every skill outcome must
// name a catalog skill and sit on its scale. A replayed key with the same
// course returns the stored one with inserted false; with a different course
// it is refused. A second course with the same code is HCM-CRS-DUPLICATE.
func (s *Postgres) CreateCourse(ctx context.Context, in Course, idempotencyKey string) (Course, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Course
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := crsCourse(ctx, tx, p.TenantID, "idempotency_key = $2", *key)
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		skills := make([]Skill, len(in.SkillOutcomes))
		for i, o := range in.SkillOutcomes {
			k, err := sklByCode(ctx, tx, p.TenantID, o.SkillCode)
			if err != nil {
				return err
			}
			if err := domain.ValidateRating(o.Rating, k.ScaleMax); err != nil {
				return sklProblem(err)
			}
			skills[i] = k
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_course (tenant_id, id, code, name, course_group, certification_code, certification_name,
			validity_months, requires_manager_approval, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.TenantID, id, in.Code, in.Name, in.CourseGroup, in.CertificationCode, in.CertificationName,
			in.ValidityMonths, in.RequiresManagerApproval, key); err != nil {
			return err
		}
		for i, o := range in.SkillOutcomes {
			if _, err := tx.Exec(ctx, `insert into hr_course_skill_outcome (tenant_id, course_id, skill_id, rating) values ($1, $2, $3, $4)`,
				p.TenantID, id, skills[i].ID, o.Rating); err != nil {
				return err
			}
		}
		var err error
		out, err = crsCourseByID(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if idempotencyRace(err) {
		return Course{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Course{}, false, problem(http.StatusConflict, CodeCourseDuplicate, "a course with code "+in.Code+" already exists")
	}
	if err == nil && !inserted && !sameCourse(out, in) {
		return Course{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// ListCourses pages the catalog by code. The list carries each course's
// skill outcomes.
func (s *Postgres) ListCourses(ctx context.Context, after string, limit int) (CoursePage, error) {
	p := principal(ctx)
	page := CoursePage{Items: []Course{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+courseColumns+` from hr_course where tenant_id = $1 and code > $2 order by code limit $3`,
			p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			c, err := crsScanCourse(rows)
			if err != nil {
				rows.Close()
				return err
			}
			page.Items = append(page.Items, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return crsAttachOutcomes(ctx, tx, p.TenantID, page.Items)
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].Code
	}
	return page, err
}

// GetCourse reads one catalog course.
func (s *Postgres) GetCourse(ctx context.Context, id string) (Course, error) {
	p := principal(ctx)
	var out Course
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = crsCourseByID(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// sameDecimalPtr compares two optional decimal strings by value ("12.5" and
// "12.50" are the same amount).
func sameDecimalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	x, errA := decimal.NewFromString(*a)
	y, errB := decimal.NewFromString(*b)
	return errA == nil && errB == nil && x.Equal(y)
}

func sameSession(stored, in Session) bool {
	return stored.CourseID == in.CourseID && stored.StartsAt.Equal(in.StartsAt) && stored.EndsAt.Equal(in.EndsAt) &&
		sameStringPtr(stored.Location, in.Location) && sameStringPtr(stored.VirtualURL, in.VirtualURL) &&
		sameStringPtr(stored.InstructorWorkerID, in.InstructorWorkerID) && sameStringPtr(stored.InstructorPartyID, in.InstructorPartyID) &&
		stored.Capacity == in.Capacity && stored.WaitlistCapacity == in.WaitlistCapacity &&
		sameDecimalPtr(stored.CostPerAttendee, in.CostPerAttendee) && sameStringPtr(stored.Currency, in.Currency)
}

// CreateSession schedules a session of a course. The API validates the
// window, the seating and the cost; the store checks the course and the
// internal instructor exist. A replayed key with the same session returns it
// with inserted false; with a different one it is refused.
func (s *Postgres) CreateSession(ctx context.Context, in Session, idempotencyKey string) (Session, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Session
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var id string
			err := tx.QueryRow(ctx, `select id::text from hr_course_session where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&id)
			if err == nil {
				out, err = crsSession(ctx, tx, p.TenantID, id)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if _, err := crsCourseByID(ctx, tx, p.TenantID, in.CourseID); err != nil {
			return err
		}
		if in.InstructorWorkerID != nil {
			if err := sklRequireWorker(ctx, tx, p.TenantID, *in.InstructorWorkerID); err != nil {
				return problem(http.StatusUnprocessableEntity, CodeWorkerNotFound, "the instructor is not a worker in this tenant")
			}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_course_session (tenant_id, id, course_id, starts_at, ends_at, location_text, virtual_url,
			instructor_worker_id, instructor_party_id, capacity, waitlist_capacity, cost_per_attendee, currency, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13, $14)`,
			p.TenantID, id, in.CourseID, in.StartsAt, in.EndsAt, in.Location, in.VirtualURL, in.InstructorWorkerID, in.InstructorPartyID,
			in.Capacity, in.WaitlistCapacity, in.CostPerAttendee, in.Currency, key); err != nil {
			return err
		}
		var err error
		out, err = crsSession(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if idempotencyRace(err) {
		return Session{}, false, idempotencyInFlight()
	}
	if err == nil && !inserted && !sameSession(out, in) {
		return Session{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// ListSessions returns a course's sessions ordered by start, at most
// maxCourseRows.
func (s *Postgres) ListSessions(ctx context.Context, courseID string) ([]Session, error) {
	p := principal(ctx)
	out := []Session{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := crsCourseByID(ctx, tx, p.TenantID, courseID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sessionSelect+` where s.tenant_id = $1 and s.course_id = $2 order by s.starts_at, s.id limit $3`,
			p.TenantID, courseID, maxCourseRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sess, err := crsScanSession(rows)
			if err != nil {
				return err
			}
			out = append(out, sess)
		}
		return rows.Err()
	})
	return out, err
}

// GetSession reads a session with its seat counts.
func (s *Postgres) GetSession(ctx context.Context, id string) (Session, error) {
	p := principal(ctx)
	var out Session
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = crsSession(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// ListRegistrations returns a session's registrations in registration order
// (the attendance list, 8.42.15), at most maxCourseRows.
func (s *Postgres) ListRegistrations(ctx context.Context, sessionID string) ([]Registration, error) {
	p := principal(ctx)
	out := []Registration{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := crsSession(ctx, tx, p.TenantID, sessionID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, registrationSelect+` where r.tenant_id = $1 and r.session_id = $2 order by r.queued_at, r.id limit $3`,
			p.TenantID, sessionID, maxCourseRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := crsScanRegistration(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// RegisterWorker places a worker in a session (BR-42.9). A worker already
// registered and not canceled gets the existing registration with inserted
// false (one registration per worker and session). A course that needs
// manager approval registers as REQUESTED with the HCM-CRS-002 warning and
// takes a seat on approval; otherwise the seat is allocated now, or
// HCM-CRS-001 when the session and its waitlist are full. Registration closes
// when the session ends. An ownOnly caller may register only themselves.
func (s *Postgres) RegisterWorker(ctx context.Context, sessionID, workerID string, ownOnly bool) (Registration, []domain.RuleWarning, bool, error) {
	p := principal(ctx)
	var out Registration
	warnings := []domain.RuleWarning{}
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		lock, err := crsLockSession(ctx, tx, p.TenantID, sessionID)
		if err != nil {
			return err
		}
		user, err := absWorkerUser(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if err := crsCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		existing, err := crsRegistration(ctx, tx, p.TenantID, "r.session_id = $2 and r.worker_id = $3", sessionID, workerID)
		found := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if found && existing.Status != domain.RegCanceled {
			out = existing
			if existing.Status == domain.RegRequested {
				warnings = append(warnings, approvalWarning())
			}
			return nil
		}
		if !s.now().Before(lock.endsAt) {
			return problem(http.StatusConflict, domain.CodeCourseState, "the session has ended; registration is closed")
		}
		status := domain.RegRequested
		if lock.approval {
			warnings = append(warnings, approvalWarning())
		} else if status, err = crsSeat(ctx, tx, p.TenantID, sessionID, lock); err != nil {
			return err
		}
		id := ids.New()
		if found {
			id = existing.ID
			_, err = tx.Exec(ctx, `update hr_course_registration set status = $3, result = null, score = null, completed_on = null,
				queued_at = clock_timestamp(), updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, status)
		} else {
			_, err = tx.Exec(ctx, `insert into hr_course_registration (tenant_id, id, session_id, worker_id, status) values ($1, $2, $3, $4, $5)`,
				p.TenantID, id, sessionID, workerID, status)
		}
		if err != nil {
			return err
		}
		out, err = crsRegistrationByID(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	if pgCode(err, uniqueViolation) {
		// A concurrent first registration of the same worker won the race.
		return Registration{}, nil, false, idempotencyInFlight()
	}
	return out, warnings, inserted, err
}

func approvalWarning() domain.RuleWarning {
	return domain.RuleWarning{Code: domain.CodeCourseApproval, Detail: "this course needs manager approval; the registration takes a seat once approved"}
}

// ApproveRegistration seats a REQUESTED registration once a manager approves
// it: CONFIRMED, WAITLISTED, or HCM-CRS-001 when full. An approver cannot
// approve their own registration. Approving again is HCM-CRS-STATE.
func (s *Postgres) ApproveRegistration(ctx context.Context, id string) (Registration, error) {
	p := principal(ctx)
	var out Registration
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		reg, lock, err := crsLockRegistration(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if reg.Status != domain.RegRequested {
			return problem(http.StatusConflict, domain.CodeCourseState, "the registration is "+reg.Status+", not awaiting approval")
		}
		user, err := absWorkerUser(ctx, tx, p.TenantID, reg.WorkerID)
		if err != nil {
			return err
		}
		if err := domain.CheckNotSelfApproval(p.UserID, user); err != nil {
			return crsProblem(err, http.StatusForbidden)
		}
		status, err := crsSeat(ctx, tx, p.TenantID, reg.SessionID, lock)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update hr_course_registration set status = $3, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, status); err != nil {
			return err
		}
		out, err = crsRegistrationByID(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// CancelRegistration cancels a registration (a manager declining a REQUESTED
// one cancels it too). Canceling a CONFIRMED seat promotes the oldest
// WAITLISTED registration (BR-42.9). Canceling again returns it unchanged
// with changed false; a recorded outcome cannot be canceled. An ownOnly
// caller may cancel only their own registration.
func (s *Postgres) CancelRegistration(ctx context.Context, id string, ownOnly bool) (Registration, bool, error) {
	p := principal(ctx)
	var out Registration
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		reg, _, err := crsLockRegistration(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		user, err := absWorkerUser(ctx, tx, p.TenantID, reg.WorkerID)
		if err != nil {
			return err
		}
		if err := crsCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		noop, err := domain.CheckCancel(reg.Status)
		if err != nil {
			return crsProblem(err, http.StatusConflict)
		}
		if noop {
			out = reg
			return nil
		}
		if _, err := tx.Exec(ctx, `update hr_course_registration set status = 'CANCELED', updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id); err != nil {
			return err
		}
		if reg.Status == domain.RegConfirmed {
			if _, err := tx.Exec(ctx, `update hr_course_registration set status = 'CONFIRMED', updated_at = now()
				where tenant_id = $1 and id = (select id from hr_course_registration
					where tenant_id = $1 and session_id = $2 and status = 'WAITLISTED' order by queued_at, id limit 1)`,
				p.TenantID, reg.SessionID); err != nil {
				return err
			}
		}
		out, err = crsRegistrationByID(ctx, tx, p.TenantID, id)
		changed = err == nil
		return err
	})
	return out, changed, err
}

// sklProblemStatus maps a domain error to a problem with the given status.
func crsProblem(err error, status int) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return problem(status, de.Code, de.Detail)
	}
	return err
}

// CompletionResult is a session completion: every line's registration and
// how many lines changed something.
type CompletionResult struct {
	Items   []Registration `json:"items"`
	Changed int            `json:"changed"`
	Passed  int            `json:"passed"`
}

// CompleteSession records attendance and results (FR-42.14) once the session
// has started. Each line names a registered worker; see domain.CompletionStep
// for the allowed changes, and the same outcome again changes nothing. A PASS
// writes the course's skill outcomes (rating = max(existing, course rating))
// and issues its certificate valid from the session end date for the
// course's validity months, then publishes course.completed, all in this
// transaction (BR-42.9). Every line is applied or none is.
func (s *Postgres) CompleteSession(ctx context.Context, sessionID string, outcomes []Outcome) (CompletionResult, error) {
	p := principal(ctx)
	res := CompletionResult{Items: []Registration{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		lock, err := crsLockSession(ctx, tx, p.TenantID, sessionID)
		if err != nil {
			return err
		}
		if s.now().Before(lock.startsAt) {
			return problem(http.StatusConflict, domain.CodeCourseState, "the session has not started; attendance is recorded from its start")
		}
		course, err := crsCourseByID(ctx, tx, p.TenantID, lock.courseID)
		if err != nil {
			return err
		}
		completedOn := lock.endsAt.UTC().Format(absDateLayout)
		for _, o := range outcomes {
			reg, err := crsRegistration(ctx, tx, p.TenantID, "r.session_id = $2 and r.worker_id = $3", sessionID, o.WorkerID)
			if errors.Is(err, pgx.ErrNoRows) {
				return problem(http.StatusNotFound, CodeRegistrationNotFound, "worker "+o.WorkerID+" is not registered for this session")
			}
			if err != nil {
				return err
			}
			next := domain.Recorded{Status: o.Status, Result: o.Result, Score: o.Score}
			noop, err := domain.CompletionStep(reg.recorded(), next)
			if err != nil {
				return crsProblem(err, http.StatusConflict)
			}
			if noop {
				res.Items = append(res.Items, reg)
				continue
			}
			if _, err := tx.Exec(ctx, `update hr_course_registration set status = $3, result = nullif($4, ''), score = nullif($5, '')::numeric,
				completed_on = $6::date, updated_at = now() where tenant_id = $1 and id = $2`,
				p.TenantID, reg.ID, o.Status, o.Result, o.Score, completedOn); err != nil {
				return err
			}
			if reg, err = crsRegistrationByID(ctx, tx, p.TenantID, reg.ID); err != nil {
				return err
			}
			res.Items = append(res.Items, reg)
			res.Changed++
			if o.Status != domain.RegAttended || o.Result != domain.ResultPass {
				continue
			}
			res.Passed++
			awards, certID, err := crsAward(ctx, tx, p.TenantID, course, reg.WorkerID, reg.ID, lock.endsAt)
			if err != nil {
				return err
			}
			if err := enqueue(ctx, tx, "courseregistration", reg.ID, hrevents.CourseCompleted, p.TenantID, hrevents.CourseCompletedData{
				RegistrationID: reg.ID, WorkerID: reg.WorkerID, CourseID: course.ID, SessionID: sessionID, CompletedOn: completedOn,
				Skills: awards, CertificationID: certID,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return CompletionResult{}, err
	}
	return res, nil
}

// crsAward writes a PASS for one worker under the worker row lock (the lock
// RateWorkerSkill takes): each skill outcome raises the rating to the course
// rating when higher and publishes workerskill.changed when it does; the
// course certificate is issued once per registration.
func crsAward(ctx context.Context, tx pgx.Tx, tenantID string, course Course, workerID, registrationID string, endsAt time.Time) ([]hrevents.CourseSkillAward, *string, error) {
	if _, err := absLockWorker(ctx, tx, tenantID, workerID); err != nil {
		return nil, nil, err
	}
	completedOn := endsAt.UTC().Format(absDateLayout)
	awards := []hrevents.CourseSkillAward{}
	for _, o := range course.SkillOutcomes {
		var prevRating *int
		var prevDay string
		err := tx.QueryRow(ctx, `select rating, to_char(assessed_on, 'YYYY-MM-DD') from worker_skill where tenant_id = $1 and worker_id = $2 and skill_id = $3`,
			tenantID, workerID, o.SkillID).Scan(&prevRating, &prevDay)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		existing := 0
		if prevRating != nil {
			existing = *prevRating
		}
		rating := domain.AwardRating(existing, o.Rating)
		awards = append(awards, hrevents.CourseSkillAward{SkillID: o.SkillID, SkillCode: o.SkillCode, Rating: rating})
		if prevRating != nil && rating == existing {
			// The worker already holds the rating; a course never lowers it
			// and leaves the assessment date alone.
			continue
		}
		// A newer assessment keeps its date; the stale guard of
		// RateWorkerSkill compares dates.
		day := completedOn
		if prevDay > day {
			day = prevDay
		}
		if _, err := tx.Exec(ctx, `insert into worker_skill (tenant_id, worker_id, skill_id, rating, assessed_on)
			values ($1, $2, $3, $4, $5::date)
			on conflict (tenant_id, worker_id, skill_id) do update
			set rating = excluded.rating, assessed_on = excluded.assessed_on, updated_at = now()`,
			tenantID, workerID, o.SkillID, rating, day); err != nil {
			return nil, nil, err
		}
		if err := enqueue(ctx, tx, "worker", workerID, hrevents.WorkerSkillChanged, tenantID, hrevents.WorkerSkillChangedData{
			WorkerID: workerID, SkillID: o.SkillID, SkillCode: o.SkillCode, Rating: rating, PreviousRating: prevRating, AssessedOn: day,
		}); err != nil {
			return nil, nil, err
		}
	}
	if course.CertificationCode == nil {
		return awards, nil, nil
	}
	var expires *string
	if exp := domain.CertificationExpiry(endsAt.UTC(), course.ValidityMonths); exp != nil {
		e := exp.Format(absDateLayout)
		expires = &e
	}
	// One certificate per registration: the key makes a retried completion
	// find the certificate it issued. A certificate of the same code issued to
	// the worker on the same day by hand is reused, not duplicated.
	key := "course-registration:" + registrationID
	var certID string
	err := tx.QueryRow(ctx, `insert into hr_certification (tenant_id, id, worker_id, code, name, issued_on, expires_on, idempotency_key)
		values ($1, $2, $3, $4, $5, $6::date, $7::date, $8)
		on conflict do nothing returning id::text`,
		tenantID, ids.New(), workerID, *course.CertificationCode, *course.CertificationName, completedOn, expires, key).Scan(&certID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `select id::text from hr_certification where tenant_id = $1
			and (idempotency_key = $2 or (worker_id = $3 and code = $4 and issued_on = $5::date))
			order by (idempotency_key = $2) desc nulls last limit 1`,
			tenantID, key, workerID, *course.CertificationCode, completedOn).Scan(&certID)
		if err != nil {
			return nil, nil, err
		}
		return awards, &certID, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := enqueue(ctx, tx, "certification", certID, hrevents.CertificationIssued, tenantID, hrevents.CertificationIssuedData{
		ID: certID, WorkerID: workerID, Code: *course.CertificationCode, IssuedOn: completedOn, ExpiresOn: expires,
	}); err != nil {
		return nil, nil, err
	}
	return awards, &certID, nil
}
