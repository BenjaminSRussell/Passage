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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// Benefit problem codes. HCM-BEN-001/002 are the 8.42.18 catalog (raised by
// the domain); the rest are this service's and listed in the README.
const (
	CodeBenefitPlanNotFound       = "HCM-BEN-PLAN-NOT-FOUND"
	CodeBenefitPlanDuplicate      = "HCM-BEN-PLAN-DUPLICATE"
	CodeBenefitEnrollmentNotFound = "HCM-BEN-ENR-NOT-FOUND"
	CodeBenefitLifeEventNotFound  = "HCM-BEN-LE-NOT-FOUND"
	CodeBenefitOptionUnknown      = "HCM-BEN-003"
	CodeBenefitAlreadyEnrolled    = "HCM-BEN-004"
	CodeBenefitEnded              = "HCM-BEN-ENDED"
	CodeNotOwnBenefits            = "HCM-BEN-NOT-OWN"

	enrollmentOverlap = "benefit_enrollment_no_overlap"
)

// Enrollment statuses derived from the validity range as of the store clock.
const (
	EnrollmentPending = "PENDING"
	EnrollmentActive  = "ACTIVE"
	EnrollmentEnded   = "ENDED"
)

// cobraPlanTypes are the group health plans whose end by termination is a
// COBRA qualifying event (8.42.4); whether COBRA applies by country is the
// wave-48 US pack's decision.
var cobraPlanTypes = map[string]bool{"MEDICAL": true, "DENTAL": true, "VISION": true, "FSA": true}

// BenefitOption is one coverage level of a plan. Rates are decimal strings in
// the plan currency.
type BenefitOption struct {
	Code          string `json:"code"`
	CoverageLevel string `json:"coverageLevel"`
	EmployeeRate  string `json:"employeeRate"`
	EmployerRate  string `json:"employerRate"`
	RateBasis     string `json:"rateBasis"`
}

// BenefitPlan is a plan as stored (FR-42.8). The open enrollment window is
// EnrollmentStart..EnrollmentEnd, both inclusive.
type BenefitPlan struct {
	ID                   string          `json:"id"`
	Code                 string          `json:"code"`
	PlanType             string          `json:"planType"`
	ProviderPartyID      *string         `json:"providerPartyId,omitempty"`
	Currency             string          `json:"currency"`
	EligibilityRule      string          `json:"eligibilityRule"`
	EnrollmentStart      string          `json:"enrollmentStart"`
	EnrollmentEnd        string          `json:"enrollmentEnd"`
	WaitingDays          int             `json:"waitingDays"`
	PreTax               bool            `json:"preTax"`
	PayrollDeductionCode *string         `json:"payrollDeductionCode,omitempty"`
	Options              []BenefitOption `json:"options"`
	CreatedAt            time.Time       `json:"createdAt"`
}

const benPlanColumns = `id::text, code, plan_type, provider_party_id::text, currency, eligibility_rule,
	to_char(enrollment_start, 'YYYY-MM-DD'), to_char(enrollment_end, 'YYYY-MM-DD'), waiting_days, pre_tax,
	payroll_deduction_code, created_at`

func benScanPlan(row pgx.Row) (BenefitPlan, error) {
	var p BenefitPlan
	err := row.Scan(&p.ID, &p.Code, &p.PlanType, &p.ProviderPartyID, &p.Currency, &p.EligibilityRule,
		&p.EnrollmentStart, &p.EnrollmentEnd, &p.WaitingDays, &p.PreTax, &p.PayrollDeductionCode, &p.CreatedAt)
	p.CreatedAt = utc(p.CreatedAt)
	p.Options = []BenefitOption{}
	return p, err
}

// sameBenefitPlan is the replay check: a stored plan answers a retried key
// only if the retry asks for the same plan and options.
func sameBenefitPlan(a, b BenefitPlan) bool {
	if a.Code != b.Code || a.PlanType != b.PlanType || benStr(a.ProviderPartyID) != benStr(b.ProviderPartyID) ||
		a.Currency != b.Currency || a.EligibilityRule != b.EligibilityRule || a.EnrollmentStart != b.EnrollmentStart ||
		a.EnrollmentEnd != b.EnrollmentEnd || a.WaitingDays != b.WaitingDays || a.PreTax != b.PreTax ||
		benStr(a.PayrollDeductionCode) != benStr(b.PayrollDeductionCode) || len(a.Options) != len(b.Options) {
		return false
	}
	byCode := map[string]BenefitOption{}
	for _, o := range a.Options {
		byCode[o.Code] = o
	}
	for _, o := range b.Options {
		x, ok := byCode[o.Code]
		if !ok || x.CoverageLevel != o.CoverageLevel || x.RateBasis != o.RateBasis ||
			!sameDecimal(x.EmployeeRate, o.EmployeeRate) || !sameDecimal(x.EmployerRate, o.EmployerRate) {
			return false
		}
	}
	return true
}

// benLoadOptions fills the options of the given plans with one query.
func benLoadOptions(ctx context.Context, tx pgx.Tx, tenantID string, plans []BenefitPlan) error {
	if len(plans) == 0 {
		return nil
	}
	idx := map[string]int{}
	planIDs := make([]string, len(plans))
	for i, p := range plans {
		idx[p.ID] = i
		planIDs[i] = p.ID
	}
	rows, err := tx.Query(ctx, `select plan_id::text, code, coverage_level, employee_rate::text, employer_rate::text, rate_basis
		from benefit_plan_option where tenant_id = $1 and plan_id = any($2::uuid[]) order by plan_id, code`, tenantID, planIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var planID string
		var o BenefitOption
		if err := rows.Scan(&planID, &o.Code, &o.CoverageLevel, &o.EmployeeRate, &o.EmployerRate, &o.RateBasis); err != nil {
			return err
		}
		o.EmployeeRate, o.EmployerRate = trimRate(o.EmployeeRate), trimRate(o.EmployerRate)
		i := idx[planID]
		plans[i].Options = append(plans[i].Options, o)
	}
	return rows.Err()
}

// trimRate drops the numeric(20,6) padding: 12.500000 reads back as 12.5.
func trimRate(s string) string {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return s
	}
	return d.String()
}

// benLoadPlan reads a plan and its options inside tx. A malformed or unknown id is a 404.
func benLoadPlan(ctx context.Context, tx pgx.Tx, tenantID, id string) (BenefitPlan, error) {
	if !ids.Valid(id) {
		return BenefitPlan{}, problem(http.StatusNotFound, CodeBenefitPlanNotFound, "benefit plan not found")
	}
	p, err := benScanPlan(tx.QueryRow(ctx, `select `+benPlanColumns+` from benefit_plan where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return BenefitPlan{}, problem(http.StatusNotFound, CodeBenefitPlanNotFound, "benefit plan not found")
	}
	if err != nil {
		return BenefitPlan{}, err
	}
	plans := []BenefitPlan{p}
	err = benLoadOptions(ctx, tx, tenantID, plans)
	return plans[0], err
}

// CreateBenefitPlan stores a plan and its options. A replayed key with the
// same plan returns the stored plan with inserted false; with a different plan
// it is refused.
func (s *Postgres) CreateBenefitPlan(ctx context.Context, in BenefitPlan, idempotencyKey string) (BenefitPlan, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out BenefitPlan
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var foundID string
			err := tx.QueryRow(ctx, `select id::text from benefit_plan where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&foundID)
			if err == nil {
				out, err = benLoadPlan(ctx, tx, p.TenantID, foundID)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		planID := ids.New()
		if _, err := tx.Exec(ctx, `insert into benefit_plan (tenant_id, id, code, plan_type, provider_party_id, currency,
				eligibility_rule, enrollment_start, enrollment_end, waiting_days, pre_tax, payroll_deduction_code, idempotency_key)
			values ($1, $2, $3, $4, $5::uuid, $6, $7, $8::date, $9::date, $10, $11, $12, $13)`,
			p.TenantID, planID, in.Code, in.PlanType, in.ProviderPartyID, in.Currency, in.EligibilityRule,
			in.EnrollmentStart, in.EnrollmentEnd, in.WaitingDays, in.PreTax, in.PayrollDeductionCode, key); err != nil {
			return err
		}
		for _, o := range in.Options {
			if _, err := tx.Exec(ctx, `insert into benefit_plan_option (tenant_id, plan_id, code, coverage_level,
					employee_rate, employer_rate, rate_basis)
				values ($1, $2, $3, $4, $5::numeric, $6::numeric, $7)`,
				p.TenantID, planID, o.Code, o.CoverageLevel, o.EmployeeRate, o.EmployerRate, o.RateBasis); err != nil {
				return err
			}
		}
		loaded, err := benLoadPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return err
		}
		out, inserted = loaded, true
		return nil
	})
	if idempotencyRace(err) {
		return BenefitPlan{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return BenefitPlan{}, false, problem(http.StatusConflict, CodeBenefitPlanDuplicate, "a benefit plan with this code already exists")
	}
	if err == nil && !inserted && !sameBenefitPlan(out, in) {
		return BenefitPlan{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// GetBenefitPlan reads one plan with its options.
func (s *Postgres) GetBenefitPlan(ctx context.Context, id string) (BenefitPlan, error) {
	p := principal(ctx)
	var out BenefitPlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = benLoadPlan(ctx, tx, p.TenantID, id)
		return err
	})
	return out, err
}

// BenefitPlanPage is one keyset page of plans ordered by code.
type BenefitPlanPage struct {
	Items     []BenefitPlan
	NextAfter string
}

// ListBenefitPlans pages the tenant's plans by code, each with its options.
func (s *Postgres) ListBenefitPlans(ctx context.Context, after string, limit int) (BenefitPlanPage, error) {
	p := principal(ctx)
	page := BenefitPlanPage{Items: []BenefitPlan{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+benPlanColumns+` from benefit_plan where tenant_id = $1 and code > $2 order by code limit $3`,
			p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			plan, err := benScanPlan(rows)
			if err != nil {
				rows.Close()
				return err
			}
			page.Items = append(page.Items, plan)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextAfter = page.Items[limit-1].Code
		}
		return benLoadOptions(ctx, tx, p.TenantID, page.Items)
	})
	return page, err
}

// benCheckOwn refuses an employee-only caller acting on another worker's
// benefits (EssBenefits is own data, 8.42.17).
func benCheckOwn(ownOnly bool, callerUserID string, workerUser *string) error {
	if !ownOnly {
		return nil
	}
	if workerUser == nil || callerUserID == "" || *workerUser != callerUserID {
		return problem(http.StatusForbidden, CodeNotOwnBenefits, "you can only see and change your own benefits")
	}
	return nil
}

// LifeEvent is a recorded qualifying event of one worker. WindowEnd is the
// last day an enrollment may take effect because of it (8.42.5).
type LifeEvent struct {
	ID         string    `json:"id"`
	WorkerID   string    `json:"workerId"`
	Kind       string    `json:"kind"`
	EventDate  string    `json:"eventDate"`
	WindowEnd  string    `json:"windowEnd"`
	RecordedBy *string   `json:"recordedBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

const lifeEventColumns = `id::text, worker_id::text, kind, to_char(event_date, 'YYYY-MM-DD'), event_date, recorded_by, created_at`

func benScanLifeEvent(row pgx.Row) (LifeEvent, error) {
	var e LifeEvent
	var date time.Time
	err := row.Scan(&e.ID, &e.WorkerID, &e.Kind, &e.EventDate, &date, &e.RecordedBy, &e.CreatedAt)
	e.WindowEnd = date.AddDate(0, 0, domain.DefaultLifeEventWindowDays).Format(absDateLayout)
	e.CreatedAt = utc(e.CreatedAt)
	return e, err
}

// RecordLifeEvent stores a qualifying event that opens the life-event
// enrollment window. An employee-only caller (ownOnly) records only their own.
// A replayed key with the same event returns it with inserted false.
func (s *Postgres) RecordLifeEvent(ctx context.Context, workerID, kind string, eventDate time.Time, idempotencyKey string, ownOnly bool) (LifeEvent, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out LifeEvent
	inserted := false
	if !ids.Valid(workerID) {
		return LifeEvent{}, false, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		user, err := absWorkerUser(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if err := benCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		if key != nil {
			found, err := benScanLifeEvent(tx.QueryRow(ctx, `select `+lifeEventColumns+` from benefit_life_event where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = found
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		row, err := benScanLifeEvent(tx.QueryRow(ctx, `insert into benefit_life_event (tenant_id, id, worker_id, kind, event_date, recorded_by, idempotency_key)
			values ($1, $2, $3, $4, $5::date, nullif($6, ''), $7)
			returning `+lifeEventColumns,
			p.TenantID, ids.New(), workerID, kind, eventDate, p.UserID, key))
		if err != nil {
			return err
		}
		out, inserted = row, true
		return nil
	})
	if idempotencyRace(err) {
		return LifeEvent{}, false, idempotencyInFlight()
	}
	if err == nil && !inserted && (out.WorkerID != workerID || out.Kind != kind || out.EventDate != eventDate.Format(absDateLayout)) {
		return LifeEvent{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// BenefitEnrollment is one worker's enrollment in one option, [ValidFrom,
// ValidTo), with the option's rates for the cost preview and payroll.
type BenefitEnrollment struct {
	ID                   string     `json:"id"`
	WorkerID             string     `json:"workerId"`
	PlanID               string     `json:"planId"`
	PlanCode             string     `json:"planCode"`
	PlanType             string     `json:"planType"`
	OptionCode           string     `json:"optionCode"`
	CoverageLevel        string     `json:"coverageLevel"`
	ValidFrom            time.Time  `json:"validFrom"`
	ValidTo              *time.Time `json:"validTo,omitempty"`
	Status               string     `json:"status"`
	LifeEventID          *string    `json:"lifeEventId,omitempty"`
	DependentCount       int        `json:"dependentCount"`
	EndReason            *string    `json:"endReason,omitempty"`
	CobraQualifyingEvent bool       `json:"cobraQualifyingEvent"`
	EmployeeRate         string     `json:"employeeRate"`
	EmployerRate         string     `json:"employerRate"`
	Currency             string     `json:"currency"`
	RateBasis            string     `json:"rateBasis"`
	PreTax               bool       `json:"preTax"`
	PayrollDeductionCode *string    `json:"payrollDeductionCode,omitempty"`
}

const benEnrollmentSelect = `select e.id::text, e.worker_id::text, e.plan_id::text, p.code, p.plan_type, e.option_code,
		o.coverage_level, e.valid_from, e.valid_to, e.life_event_id::text, e.dependent_count, e.end_reason,
		e.cobra_qualifying_event, o.employee_rate::text, o.employer_rate::text, p.currency, o.rate_basis,
		p.pre_tax, p.payroll_deduction_code
	from benefit_enrollment e
	join benefit_plan p on p.tenant_id = e.tenant_id and p.id = e.plan_id
	join benefit_plan_option o on o.tenant_id = e.tenant_id and o.plan_id = e.plan_id and o.code = e.option_code`

func (s *Postgres) benScanEnrollment(row pgx.Row) (BenefitEnrollment, error) {
	var e BenefitEnrollment
	err := row.Scan(&e.ID, &e.WorkerID, &e.PlanID, &e.PlanCode, &e.PlanType, &e.OptionCode, &e.CoverageLevel,
		&e.ValidFrom, &e.ValidTo, &e.LifeEventID, &e.DependentCount, &e.EndReason, &e.CobraQualifyingEvent,
		&e.EmployeeRate, &e.EmployerRate, &e.Currency, &e.RateBasis, &e.PreTax, &e.PayrollDeductionCode)
	e.ValidFrom, e.ValidTo = utc(e.ValidFrom), utcPtr(e.ValidTo)
	e.EmployeeRate, e.EmployerRate = trimRate(e.EmployeeRate), trimRate(e.EmployerRate)
	now := s.now()
	switch {
	case now.Before(e.ValidFrom):
		e.Status = EnrollmentPending
	case e.ValidTo != nil && !now.Before(*e.ValidTo):
		e.Status = EnrollmentEnded
	default:
		e.Status = EnrollmentActive
	}
	return e, err
}

func (s *Postgres) benLoadEnrollment(ctx context.Context, tx pgx.Tx, tenantID, id string) (BenefitEnrollment, error) {
	if !ids.Valid(id) {
		return BenefitEnrollment{}, problem(http.StatusNotFound, CodeBenefitEnrollmentNotFound, "benefit enrollment not found")
	}
	e, err := s.benScanEnrollment(tx.QueryRow(ctx, benEnrollmentSelect+` where e.tenant_id = $1 and e.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return BenefitEnrollment{}, problem(http.StatusNotFound, CodeBenefitEnrollmentNotFound, "benefit enrollment not found")
	}
	return e, err
}

// BenefitEnrollmentInput asks to enroll a worker in one option from
// EffectiveDate (a UTC day).
type BenefitEnrollmentInput struct {
	WorkerID       string
	PlanID         string
	OptionCode     string
	EffectiveDate  time.Time
	LifeEventID    string
	DependentCount int
}

func sameEnrollment(e BenefitEnrollment, in BenefitEnrollmentInput) bool {
	return e.WorkerID == in.WorkerID && e.PlanID == in.PlanID && e.OptionCode == in.OptionCode &&
		e.ValidFrom.Equal(domain.AsOfDay(in.EffectiveDate)) && benStr(e.LifeEventID) == in.LifeEventID &&
		e.DependentCount == in.DependentCount
}

// benEmployment is one PRIMARY employment of a worker as of a date, with the
// facts BR-42.6 evaluates.
type benEmployment struct {
	WorkerID        string
	PersonnelNumber string
	LegalName       string
	EmploymentID    string
	Facts           domain.EligibilityFacts
}

// benWorkforce reads the PRIMARY employments active on asOf, for one worker
// when workerID is set or for the whole tenant otherwise. FTE is the sum of
// the employment's position assignments on that day (nil when it has none).
// The read runs under the caller's legal-entity scope (RLS on employment).
func benWorkforce(ctx context.Context, tx pgx.Tx, tenantID string, asOf time.Time, workerID *string) ([]benEmployment, error) {
	rows, err := tx.Query(ctx, `select w.id::text, w.personnel_number, w.legal_name, e.id::text, e.employment_type,
			e.legal_entity_id::text, e.hire_date,
			(select sum(pa.fte_share)::text from position_assignment pa
				where pa.tenant_id = e.tenant_id and pa.employment_id = e.id and pa.validity @> $2::timestamptz)
		from employment e
		join worker w on w.tenant_id = e.tenant_id and w.id = e.worker_id
		where e.tenant_id = $1 and e.assignment_kind = 'PRIMARY' and e.validity @> $2::timestamptz
		  and ($3::uuid is null or e.worker_id = $3::uuid)
		order by w.personnel_number, e.legal_entity_id`, tenantID, asOf, workerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []benEmployment
	for rows.Next() {
		var e benEmployment
		var hire time.Time
		var fte *string
		if err := rows.Scan(&e.WorkerID, &e.PersonnelNumber, &e.LegalName, &e.EmploymentID, &e.Facts.EmploymentType,
			&e.Facts.LegalEntityID, &hire, &fte); err != nil {
			return nil, err
		}
		e.Facts.ServiceDays = domain.ServiceDays(hire, asOf)
		if fte != nil {
			d, err := decimal.NewFromString(*fte)
			if err != nil {
				return nil, err
			}
			e.Facts.FTE = &d
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// benEvaluateWorker applies BR-42.6 to a worker's PRIMARY employments: the
// worker is eligible when one of them is (a worker holds at most one PRIMARY
// employment per legal entity, BR-42.14). The answer names the employment
// that decided it, or the first one's reason when none qualifies.
func benEvaluateWorker(rule domain.Rule, waitingDays int, emps []benEmployment) (domain.Eligibility, *benEmployment) {
	if len(emps) == 0 {
		return domain.Eligibility{Reason: "no PRIMARY employment on the effective date"}, nil
	}
	first := domain.Evaluate(rule, waitingDays, emps[0].Facts)
	if first.Eligible {
		return first, &emps[0]
	}
	for i := 1; i < len(emps); i++ {
		if r := domain.Evaluate(rule, waitingDays, emps[i].Facts); r.Eligible {
			return r, &emps[i]
		}
	}
	return first, &emps[0]
}

// CreateBenefitEnrollment enrolls a worker (FR-42.8). The effective date must
// fall inside the open window or a life event's window (HCM-BEN-001), the
// worker must pass the waiting period and the plan rule as of that date
// (HCM-BEN-002), and must hold no overlapping enrollment in the plan
// (HCM-BEN-004). The enrollment and its outbox event commit together. An
// employee-only caller (ownOnly) enrolls only themselves. A replayed key with
// the same request returns the stored enrollment with inserted false.
func (s *Postgres) CreateBenefitEnrollment(ctx context.Context, in BenefitEnrollmentInput, idempotencyKey string, ownOnly bool) (BenefitEnrollment, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	eff := domain.AsOfDay(in.EffectiveDate)
	var out BenefitEnrollment
	inserted := false
	if !ids.Valid(in.WorkerID) {
		return BenefitEnrollment{}, false, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The worker lock serialises enrollments of one worker, so the
		// eligibility read and the insert see the same employments.
		user, err := absLockWorker(ctx, tx, p.TenantID, in.WorkerID)
		if err != nil {
			return err
		}
		if err := benCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		if key != nil {
			var foundID string
			err := tx.QueryRow(ctx, `select id::text from benefit_enrollment where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&foundID)
			if err == nil {
				out, err = s.benLoadEnrollment(ctx, tx, p.TenantID, foundID)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		plan, err := benLoadPlan(ctx, tx, p.TenantID, in.PlanID)
		if err != nil {
			return err
		}
		offered := false
		for _, o := range plan.Options {
			offered = offered || o.Code == in.OptionCode
		}
		if !offered {
			return problem(http.StatusUnprocessableEntity, CodeBenefitOptionUnknown, "option "+in.OptionCode+" is not offered by plan "+plan.Code)
		}
		var lifeEvent *time.Time
		if in.LifeEventID != "" {
			var date time.Time
			err := tx.QueryRow(ctx, `select event_date from benefit_life_event where tenant_id = $1 and worker_id = $2 and id = $3`,
				p.TenantID, in.WorkerID, in.LifeEventID).Scan(&date)
			if errors.Is(err, pgx.ErrNoRows) {
				return problem(http.StatusUnprocessableEntity, CodeBenefitLifeEventNotFound, "life event not found for this worker")
			}
			if err != nil {
				return err
			}
			lifeEvent = &date
		}
		start, err := time.Parse(absDateLayout, plan.EnrollmentStart)
		if err != nil {
			return err
		}
		end, err := time.Parse(absDateLayout, plan.EnrollmentEnd)
		if err != nil {
			return err
		}
		if err := domain.CheckEnrollmentWindow(eff, start, end, lifeEvent); err != nil {
			return absProblem(err)
		}
		rule, err := domain.CompileRule(plan.EligibilityRule)
		if err != nil {
			return err
		}
		emps, err := benWorkforce(ctx, tx, p.TenantID, eff, &in.WorkerID)
		if err != nil {
			return err
		}
		if res, _ := benEvaluateWorker(rule, plan.WaitingDays, emps); !res.Eligible {
			return problem(http.StatusUnprocessableEntity, domain.CodeBenefitIneligible, "Worker not eligible for plan "+plan.Code+": "+res.Reason)
		}
		id := ids.New()
		var lifeEventID *string
		if in.LifeEventID != "" {
			lifeEventID = &in.LifeEventID
		}
		if _, err := tx.Exec(ctx, `insert into benefit_enrollment (tenant_id, id, worker_id, plan_id, option_code, valid_from,
				life_event_id, dependent_count, enrolled_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7::uuid, $8, nullif($9, ''), $10)`,
			p.TenantID, id, in.WorkerID, in.PlanID, in.OptionCode, eff, lifeEventID, in.DependentCount, p.UserID, key); err != nil {
			return err
		}
		loaded, err := s.benLoadEnrollment(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "benefit_enrollment", id, hrevents.BenefitEnrollmentChanged, p.TenantID,
			benEnrollmentEvent(loaded, hrevents.BenefitStart)); err != nil {
			return err
		}
		out, inserted = loaded, true
		return nil
	})
	if idempotencyRace(err) {
		return BenefitEnrollment{}, false, idempotencyInFlight()
	}
	if pgConstraint(err, enrollmentOverlap) {
		return BenefitEnrollment{}, false, problem(http.StatusConflict, CodeBenefitAlreadyEnrolled, "the worker already has an enrollment in this plan that overlaps the effective date; end it first")
	}
	if err == nil && !inserted && !sameEnrollment(out, in) {
		return BenefitEnrollment{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// EndBenefitEnrollment closes an open enrollment after lastDay, its last
// covered day. A TERMINATION keeps coverage to the end of that month (BR-42.6)
// and marks a COBRA qualifying event on health plans. Ending it again with the
// same date and reason returns it unchanged with changed false; an enrollment
// already ended otherwise is HCM-BEN-ENDED. The END event commits with it.
func (s *Postgres) EndBenefitEnrollment(ctx context.Context, id string, lastDay time.Time, reason string) (BenefitEnrollment, bool, error) {
	p := principal(ctx)
	var out BenefitEnrollment
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, changed, err = s.endEnrollmentTx(ctx, tx, p, id, lastDay, reason)
		return err
	})
	return out, changed, err
}

func (s *Postgres) endEnrollmentTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, lastDay time.Time, reason string) (BenefitEnrollment, bool, error) {
	if !ids.Valid(id) {
		return BenefitEnrollment{}, false, problem(http.StatusNotFound, CodeBenefitEnrollmentNotFound, "benefit enrollment not found")
	}
	if _, err := tx.Exec(ctx, `select 1 from benefit_enrollment where tenant_id = $1 and id = $2 for update`, p.TenantID, id); err != nil {
		return BenefitEnrollment{}, false, err
	}
	cur, err := s.benLoadEnrollment(ctx, tx, p.TenantID, id)
	if err != nil {
		return BenefitEnrollment{}, false, err
	}
	validTo := domain.EnrollmentValidTo(lastDay, reason)
	if cur.ValidTo != nil {
		if cur.ValidTo.Equal(validTo) && benStr(cur.EndReason) == reason {
			return cur, false, nil
		}
		return BenefitEnrollment{}, false, problem(http.StatusConflict, CodeBenefitEnded, "the enrollment has already ended on "+cur.ValidTo.Format(absDateLayout))
	}
	if domain.AsOfDay(lastDay).Before(cur.ValidFrom) || !validTo.After(cur.ValidFrom) {
		return BenefitEnrollment{}, false, problem(http.StatusUnprocessableEntity, domain.CodeBenefitDateOrder, "the last covered day must be on or after the enrollment start "+cur.ValidFrom.Format(absDateLayout))
	}
	cobra := reason == domain.EndTermination && cobraPlanTypes[cur.PlanType]
	if _, err := tx.Exec(ctx, `update benefit_enrollment set valid_to = $3, end_reason = $4, cobra_qualifying_event = $5,
			ended_by = nullif($6, '')
		where tenant_id = $1 and id = $2`, p.TenantID, id, validTo, reason, cobra, p.UserID); err != nil {
		return BenefitEnrollment{}, false, err
	}
	loaded, err := s.benLoadEnrollment(ctx, tx, p.TenantID, id)
	if err != nil {
		return BenefitEnrollment{}, false, err
	}
	if err := enqueue(ctx, tx, "benefit_enrollment", id, hrevents.BenefitEnrollmentChanged, p.TenantID,
		benEnrollmentEvent(loaded, hrevents.BenefitEnd)); err != nil {
		return BenefitEnrollment{}, false, err
	}
	return loaded, true, nil
}

// WorkerBenefits is a worker's enrollments (oldest first) and life events
// (newest first).
type WorkerBenefits struct {
	WorkerID    string              `json:"workerId"`
	Enrollments []BenefitEnrollment `json:"enrollments"`
	LifeEvents  []LifeEvent         `json:"lifeEvents"`
}

// maxWorkerBenefitRows bounds each list on the worker's benefits read.
const maxWorkerBenefitRows = 200

// ListWorkerBenefits reads a worker's enrollments and life events. An
// employee-only caller (ownOnly) reads only their own.
func (s *Postgres) ListWorkerBenefits(ctx context.Context, workerID string, ownOnly bool) (WorkerBenefits, error) {
	p := principal(ctx)
	out := WorkerBenefits{WorkerID: workerID, Enrollments: []BenefitEnrollment{}, LifeEvents: []LifeEvent{}}
	if !ids.Valid(workerID) {
		return out, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		user, err := absWorkerUser(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if err := benCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, benEnrollmentSelect+` where e.tenant_id = $1 and e.worker_id = $2
			order by e.valid_from, p.code limit $3`, p.TenantID, workerID, maxWorkerBenefitRows)
		if err != nil {
			return err
		}
		for rows.Next() {
			e, err := s.benScanEnrollment(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out.Enrollments = append(out.Enrollments, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select `+lifeEventColumns+` from benefit_life_event where tenant_id = $1 and worker_id = $2
			order by event_date desc, created_at desc limit $3`, p.TenantID, workerID, maxWorkerBenefitRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := benScanLifeEvent(rows)
			if err != nil {
				return err
			}
			out.LifeEvents = append(out.LifeEvents, e)
		}
		return rows.Err()
	})
	return out, err
}

// BenefitSimulationRow is the eligibility answer for one worker.
type BenefitSimulationRow struct {
	WorkerID        string  `json:"workerId"`
	PersonnelNumber string  `json:"personnelNumber"`
	LegalName       string  `json:"legalName"`
	EmploymentID    *string `json:"employmentId,omitempty"`
	LegalEntityID   *string `json:"legalEntityId,omitempty"`
	EmploymentType  *string `json:"employmentType,omitempty"`
	ServiceDays     *int64  `json:"serviceDays,omitempty"`
	FTE             *string `json:"fte,omitempty"`
	Eligible        bool    `json:"eligible"`
	Reason          string  `json:"reason"`
}

// BenefitSimulation is the eligibility of the workforce for one plan.
type BenefitSimulation struct {
	PlanID   string                 `json:"planId"`
	PlanCode string                 `json:"planCode"`
	Rule     string                 `json:"rule"`
	AsOf     string                 `json:"asOf"`
	Total    int                    `json:"total"`
	Eligible int                    `json:"eligible"`
	Rows     []BenefitSimulationRow `json:"rows"`
}

// SimulateBenefitEligibility evaluates a plan for every worker with a PRIMARY
// employment on asOf, exactly as enrollment would (waiting period, then rule;
// BR-42.6), and writes nothing. Rows are ordered by personnel number.
func (s *Postgres) SimulateBenefitEligibility(ctx context.Context, planID string, asOf time.Time) (BenefitSimulation, error) {
	p := principal(ctx)
	asOf = domain.AsOfDay(asOf)
	var out BenefitSimulation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		plan, err := benLoadPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return err
		}
		rule, err := domain.CompileRule(plan.EligibilityRule)
		if err != nil {
			return err
		}
		emps, err := benWorkforce(ctx, tx, p.TenantID, asOf, nil)
		if err != nil {
			return err
		}
		out = BenefitSimulation{PlanID: plan.ID, PlanCode: plan.Code, Rule: plan.EligibilityRule, AsOf: asOf.Format(absDateLayout), Rows: []BenefitSimulationRow{}}
		for i := 0; i < len(emps); {
			j := i
			for j < len(emps) && emps[j].WorkerID == emps[i].WorkerID {
				j++
			}
			res, by := benEvaluateWorker(rule, plan.WaitingDays, emps[i:j])
			row := BenefitSimulationRow{WorkerID: by.WorkerID, PersonnelNumber: by.PersonnelNumber, LegalName: by.LegalName,
				EmploymentID: &by.EmploymentID, LegalEntityID: &by.Facts.LegalEntityID, EmploymentType: &by.Facts.EmploymentType,
				ServiceDays: &by.Facts.ServiceDays, Eligible: res.Eligible, Reason: res.Reason}
			if by.Facts.FTE != nil {
				f := by.Facts.FTE.String()
				row.FTE = &f
			}
			if res.Eligible {
				out.Eligible++
			}
			out.Rows = append(out.Rows, row)
			i = j
		}
		out.Total = len(out.Rows)
		return nil
	})
	return out, err
}

func benEnrollmentEvent(e BenefitEnrollment, action string) hrevents.BenefitEnrollmentChangedData {
	d := hrevents.BenefitEnrollmentChangedData{
		ID: e.ID, WorkerID: e.WorkerID, PlanID: e.PlanID, PlanCode: e.PlanCode, OptionCode: e.OptionCode, Action: action,
		ValidFrom: e.ValidFrom.Format(time.RFC3339), EndReason: e.EndReason, EmployeeRate: e.EmployeeRate,
		EmployerRate: e.EmployerRate, Currency: e.Currency, RateBasis: e.RateBasis, PreTax: e.PreTax,
		PayrollDeductionCode: e.PayrollDeductionCode,
	}
	if e.ValidTo != nil {
		to := e.ValidTo.Format(time.RFC3339)
		d.ValidTo = &to
	}
	return d
}

func benStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
