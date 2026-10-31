package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

const (
	CodeQuestionnaireNotFound  = "HCM-QST-NOT-FOUND"
	CodeQuestionnaireDuplicate = "HCM-QST-DUPLICATE"
	CodeScheduleNotFound       = "HCM-QST-SCHEDULE-NOT-FOUND"
	CodeQuestionnaireNotOpen   = "HCM-QST-NOT-OPEN"
	CodeQuestionnaireNotOwn    = "HCM-QST-NOT-OWN"
	CodeNotInvited             = "HCM-QST-NOT-INVITED"
	CodeAlreadyAnswered        = "HCM-QST-ANSWERED"
	CodeResultsOpen            = "HCM-QST-NOT-CLOSED"

	maxSessions = 10000
)

type Questionnaire struct {
	ID        string            `json:"id"`
	Code      string            `json:"code"`
	Name      string            `json:"name"`
	Anonymous bool              `json:"anonymous"`
	Questions []domain.Question `json:"questions"`
	CreatedAt time.Time         `json:"createdAt"`
}

type QuestionnaireSchedule struct {
	ID              string    `json:"id"`
	QuestionnaireID string    `json:"questionnaireId"`
	Population      string    `json:"population"`
	OpensAt         time.Time `json:"opensAt"`
	ClosesAt        time.Time `json:"closesAt"`
	InvitedCount    int       `json:"invitedCount"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"createdAt"`
}

// ScheduleView is a schedule with the questionnaire a respondent fills in.
type ScheduleView struct {
	QuestionnaireSchedule
	Questionnaire Questionnaire `json:"questionnaire"`
}

func scheduleStatus(opens, closes, now time.Time) string {
	switch {
	case now.Before(opens):
		return "PLANNED"
	case now.Before(closes):
		return "OPEN"
	default:
		return "CLOSED"
	}
}

func scanQuestionnaire(row pgx.Row) (Questionnaire, error) {
	var q Questionnaire
	var raw []byte
	if err := row.Scan(&q.ID, &q.Code, &q.Name, &q.Anonymous, &raw, &q.CreatedAt); err != nil {
		return q, err
	}
	q.CreatedAt = utc(q.CreatedAt)
	return q, json.Unmarshal(raw, &q.Questions)
}

const questionnaireColumns = `id::text, code, name, anonymous, questions, created_at`

const scheduleColumns = `id::text, questionnaire_id::text, population, opens_at, closes_at, invited_count, created_at`

func (s *Postgres) scanSchedule(row pgx.Row) (QuestionnaireSchedule, error) {
	var sc QuestionnaireSchedule
	err := row.Scan(&sc.ID, &sc.QuestionnaireID, &sc.Population, &sc.OpensAt, &sc.ClosesAt, &sc.InvitedCount, &sc.CreatedAt)
	sc.OpensAt, sc.ClosesAt, sc.CreatedAt = utc(sc.OpensAt), utc(sc.ClosesAt), utc(sc.CreatedAt)
	sc.Status = scheduleStatus(sc.OpensAt, sc.ClosesAt, s.now())
	return sc, err
}

// CreateQuestionnaire stores a validated design (FR-42.15). The design is
// immutable; a changed questionnaire is a new code.
func (s *Postgres) CreateQuestionnaire(ctx context.Context, in Questionnaire, idempotencyKey string) (Questionnaire, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	raw, err := json.Marshal(in.Questions)
	if err != nil {
		return Questionnaire{}, false, err
	}
	var out Questionnaire
	inserted := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanQuestionnaire(tx.QueryRow(ctx, `select `+questionnaireColumns+` from hr_questionnaire where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, err = scanQuestionnaire(tx.QueryRow(ctx, `insert into hr_questionnaire (tenant_id, id, code, name, anonymous, questions, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7) returning `+questionnaireColumns, p.TenantID, ids.New(), in.Code, in.Name, in.Anonymous, raw, key))
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return Questionnaire{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Questionnaire{}, false, problem(http.StatusConflict, CodeQuestionnaireDuplicate, "a questionnaire with this code already exists")
	}
	if err == nil && !inserted && (out.Code != in.Code || out.Name != in.Name || out.Anonymous != in.Anonymous || len(out.Questions) != len(in.Questions)) {
		return Questionnaire{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// ListQuestionnaires returns up to 200 questionnaires by code.
func (s *Postgres) ListQuestionnaires(ctx context.Context) ([]Questionnaire, error) {
	p := principal(ctx)
	out := []Questionnaire{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+questionnaireColumns+` from hr_questionnaire where tenant_id = $1 order by code limit 200`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			q, err := scanQuestionnaire(rows)
			if err != nil {
				return err
			}
			out = append(out, q)
		}
		return rows.Err()
	})
	return out, err
}

// GetQuestionnaire returns one questionnaire.
func (s *Postgres) GetQuestionnaire(ctx context.Context, id string) (Questionnaire, error) {
	p := principal(ctx)
	var q Questionnaire
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		q, err = scanQuestionnaire(tx.QueryRow(ctx, `select `+questionnaireColumns+` from hr_questionnaire where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Questionnaire{}, problem(http.StatusNotFound, CodeQuestionnaireNotFound, "questionnaire not found")
	}
	return q, err
}

// populationRule compiles the schedule's CEL population; empty means everyone.
func populationRule(src string) (domain.Rule, error) {
	if src == "" {
		src = "true"
	}
	return domain.CompileRule(src)
}

// invitedWorkers is the distinct workers with a PRIMARY employment on asOf for
// whom the rule holds. Workers are counted once even with several employments.
func invitedWorkers(emps []benEmployment, rule domain.Rule) map[string]bool {
	out := map[string]bool{}
	for _, e := range emps {
		if !out[e.WorkerID] && domain.Evaluate(rule, 0, e.Facts).Eligible {
			out[e.WorkerID] = true
		}
	}
	return out
}

// CreateSchedule targets a questionnaire at a population. The population is
// evaluated now to snapshot the invited count; it is evaluated again when each
// respondent answers, so the list of invitees is never stored (BR-42.10).
func (s *Postgres) CreateSchedule(ctx context.Context, questionnaireID, population string, opens, closes time.Time, idempotencyKey string) (QuestionnaireSchedule, bool, error) {
	p := principal(ctx)
	rule, err := populationRule(population)
	if err != nil {
		return QuestionnaireSchedule{}, false, problem(http.StatusBadRequest, "COM-0001", "population rule: "+err.Error())
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out QuestionnaireSchedule
	inserted := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := s.scanSchedule(tx.QueryRow(ctx, `select `+scheduleColumns+` from hr_questionnaire_schedule where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from hr_questionnaire where tenant_id = $1 and id = $2)`, p.TenantID, questionnaireID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeQuestionnaireNotFound, "questionnaire not found")
		}
		emps, err := benWorkforce(ctx, tx, p.TenantID, s.now(), nil)
		if err != nil {
			return err
		}
		invited := len(invitedWorkers(emps, rule))
		var anonymous bool
		if err := tx.QueryRow(ctx, `select anonymous from hr_questionnaire where tenant_id = $1 and id = $2`, p.TenantID, questionnaireID).Scan(&anonymous); err != nil {
			return err
		}
		if anonymous && invited < domain.AnonymityThreshold {
			return problem(http.StatusUnprocessableEntity, domain.CodeQuestionnaireThreshold, "an anonymous questionnaire needs a population of at least 5 workers")
		}
		out, err = s.scanSchedule(tx.QueryRow(ctx, `insert into hr_questionnaire_schedule (tenant_id, id, questionnaire_id, population, opens_at, closes_at, invited_count, idempotency_key, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+scheduleColumns,
			p.TenantID, ids.New(), questionnaireID, population, opens, closes, invited, key, p.UserID))
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return QuestionnaireSchedule{}, false, idempotencyInFlight()
	}
	if err == nil && !inserted && (out.QuestionnaireID != questionnaireID || out.Population != population) {
		return QuestionnaireSchedule{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) loadScheduleView(ctx context.Context, tx pgx.Tx, tenantID, id string) (ScheduleView, error) {
	var v ScheduleView
	sc, err := s.scanSchedule(tx.QueryRow(ctx, `select `+scheduleColumns+` from hr_questionnaire_schedule where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, problem(http.StatusNotFound, CodeScheduleNotFound, "questionnaire schedule not found")
	}
	if err != nil {
		return v, err
	}
	v.QuestionnaireSchedule = sc
	v.Questionnaire, err = scanQuestionnaire(tx.QueryRow(ctx, `select `+questionnaireColumns+` from hr_questionnaire where tenant_id = $1 and id = $2`, tenantID, sc.QuestionnaireID))
	return v, err
}

// GetSchedule returns a schedule with its questionnaire (the form to fill in).
func (s *Postgres) GetSchedule(ctx context.Context, id string) (ScheduleView, error) {
	p := principal(ctx)
	var v ScheduleView
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		v, err = s.loadScheduleView(ctx, tx, p.TenantID, id)
		return err
	})
	return v, err
}

// ListOpenSchedules returns the schedules open now, soonest to close first.
func (s *Postgres) ListOpenSchedules(ctx context.Context) ([]QuestionnaireSchedule, error) {
	p := principal(ctx)
	out := []QuestionnaireSchedule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+scheduleColumns+` from hr_questionnaire_schedule where tenant_id = $1 and opens_at <= $2 and closes_at > $2
			order by closes_at, id limit 200`, p.TenantID, s.now())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sc, err := s.scanSchedule(rows)
			if err != nil {
				return err
			}
			out = append(out, sc)
		}
		return rows.Err()
	})
	return out, err
}

type sessionDraft struct {
	respondent, department, entity *string
	day                            time.Time
	answers, scores                []byte
}

func insertSession(ctx context.Context, tx pgx.Tx, tenantID, id, scheduleID string, d sessionDraft) error {
	_, err := tx.Exec(ctx, `insert into hr_questionnaire_session (tenant_id, id, schedule_id, respondent_worker_id, department_id, legal_entity_id, completed_on, answers, scores)
		values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9)`, tenantID, id, scheduleID, d.respondent, d.department, d.entity, d.day, d.answers, d.scores)
	return err
}

// eligibleEntity is the legal entity of the first PRIMARY employment the
// population rule accepts.
func eligibleEntity(emps []benEmployment, rule domain.Rule) *string {
	for _, e := range emps {
		if domain.Evaluate(rule, 0, e.Facts).Eligible {
			le := e.Facts.LegalEntityID
			return &le
		}
	}
	return nil
}

// SubmitAnswers records one worker's answers (FR-42.15). The caller must be
// the worker; the schedule must be open (HCM-QST-001 after it closed); the
// worker must match the population now; a second answer is refused. Scores
// follow BR-42.10.
//
// On an anonymous questionnaire the response marker and the answer session are
// written in two separate transactions, and the session gets a random v4 id and
// only a date, no worker id: nothing in the session row (xmin, id order, time)
// links it to the marker. Answers are fully validated before the marker is
// written, so a failure between the two transactions needs a database outage;
// the worker then cannot answer again (no delete grant), which is the safe side.
func (s *Postgres) SubmitAnswers(ctx context.Context, scheduleID, workerID string, raw map[string]json.RawMessage) error {
	p := principal(ctx)
	var draft sessionDraft
	anonymous := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := s.loadScheduleView(ctx, tx, p.TenantID, scheduleID)
		if err != nil {
			return err
		}
		now := s.now()
		switch v.Status {
		case "PLANNED":
			return problem(http.StatusUnprocessableEntity, CodeQuestionnaireNotOpen, "the questionnaire opens on "+v.OpensAt.Format(time.RFC3339))
		case "CLOSED":
			return problem(http.StatusUnprocessableEntity, domain.CodeQuestionnaireClosed, "Questionnaire closed on "+v.ClosesAt.Format(time.RFC3339))
		}
		var user *string
		err = tx.QueryRow(ctx, `select user_id from worker where tenant_id = $1 and id = $2`, p.TenantID, workerID).Scan(&user)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		if err != nil {
			return err
		}
		if user == nil || p.UserID == "" || *user != p.UserID {
			return problem(http.StatusForbidden, CodeQuestionnaireNotOwn, "you can only answer for yourself")
		}
		rule, err := populationRule(v.Population)
		if err != nil {
			return err
		}
		emps, err := benWorkforce(ctx, tx, p.TenantID, now, &workerID)
		if err != nil {
			return err
		}
		if !invitedWorkers(emps, rule)[workerID] {
			return problem(http.StatusForbidden, CodeNotInvited, "you are not in this questionnaire's population")
		}
		answers, fields := domain.NormalizeAnswers(v.Questionnaire.Questions, raw)
		if len(fields) > 0 {
			pr := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
			pr.Errors = fields
			return pr
		}
		draft = sessionDraft{entity: eligibleEntity(emps, rule), day: now}
		if draft.answers, err = json.Marshal(answers); err != nil {
			return err
		}
		if draft.scores, err = json.Marshal(domain.Score(v.Questionnaire.Questions, answers)); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select p.department_id::text from position_assignment pa
			join hr_position p on p.tenant_id = pa.tenant_id and p.id = pa.position_id
			where pa.tenant_id = $1 and pa.worker_id = $2 and pa.is_primary and pa.validity @> $3::timestamptz`, p.TenantID, workerID, now).Scan(&draft.department); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into hr_questionnaire_responded (tenant_id, schedule_id, worker_id, legal_entity_id) values ($1, $2, $3, $4) on conflict do nothing`,
			p.TenantID, scheduleID, workerID, draft.entity)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusConflict, CodeAlreadyAnswered, "you have already answered this questionnaire")
		}
		anonymous = v.Questionnaire.Anonymous
		if anonymous {
			return nil
		}
		draft.respondent = &workerID
		return insertSession(ctx, tx, p.TenantID, ids.New(), scheduleID, draft)
	})
	if err != nil || !anonymous {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return insertSession(ctx, tx, p.TenantID, uuid.NewString(), scheduleID, draft)
	})
}

type GroupResult struct {
	DepartmentID *string      `json:"departmentId"`
	Stats        domain.Stats `json:"stats"`
}

type QuestionnaireResults struct {
	ScheduleID   string        `json:"scheduleId"`
	Anonymous    bool          `json:"anonymous"`
	Invited      int           `json:"invited"`
	Responded    int           `json:"responded"`
	ResponseRate string        `json:"responseRate"`
	Overall      domain.Stats  `json:"overall"`
	Suppressed   bool          `json:"suppressed"`
	Groups       []GroupResult `json:"groups,omitempty"`
}

type storedSession struct {
	department *string
	session    domain.Session
}

// QuestionnaireResults analyses a schedule (FR-42.15): response rate, statistics by
// question and result group, and with by="department" the same per department.
// On an anonymous questionnaire a breakdown with any group under 5 respondents
// is HCM-QST-002 (BR-42.10); the overall figures are always returned.
func (s *Postgres) QuestionnaireResults(ctx context.Context, scheduleID string, byDepartment bool) (QuestionnaireResults, error) {
	p := principal(ctx)
	var res QuestionnaireResults
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		v, err := s.loadScheduleView(ctx, tx, p.TenantID, scheduleID)
		if err != nil {
			return err
		}
		if v.Questionnaire.Anonymous && v.Status != "CLOSED" {
			return problem(http.StatusConflict, CodeResultsOpen, "results of an anonymous questionnaire are released after it closes on "+v.ClosesAt.Format(time.RFC3339))
		}
		rows, err := tx.Query(ctx, `select department_id::text, answers, scores from hr_questionnaire_session where tenant_id = $1 and schedule_id = $2 limit $3`,
			p.TenantID, scheduleID, maxSessions)
		if err != nil {
			return err
		}
		defer rows.Close()
		var stored []storedSession
		for rows.Next() {
			var st storedSession
			var a, sc []byte
			if err := rows.Scan(&st.department, &a, &sc); err != nil {
				return err
			}
			if err := json.Unmarshal(a, &st.session.Answers); err != nil {
				return err
			}
			if err := json.Unmarshal(sc, &st.session.Scores); err != nil {
				return err
			}
			stored = append(stored, st)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		qs := v.Questionnaire.Questions
		all := make([]domain.Session, len(stored))
		byDept := map[string][]domain.Session{}
		var order []string
		for i, st := range stored {
			all[i] = st.session
			k := ""
			if st.department != nil {
				k = *st.department
			}
			if _, seen := byDept[k]; !seen {
				order = append(order, k)
			}
			byDept[k] = append(byDept[k], st.session)
		}
		res = QuestionnaireResults{ScheduleID: scheduleID, Anonymous: v.Questionnaire.Anonymous, Invited: v.InvitedCount, Responded: len(stored),
			ResponseRate: "0.00", Overall: domain.Aggregate(qs, all)}
		if res.Anonymous && res.Responded < domain.AnonymityThreshold {
			res.Overall, res.Suppressed = domain.Stats{Respondents: len(stored), Questions: []domain.QuestionStat{}, ResultGroups: []domain.GroupStat{}}, true
		}
		if v.InvitedCount > 0 {
			res.ResponseRate = decimal.NewFromInt(int64(len(stored))).Div(decimal.NewFromInt(int64(v.InvitedCount))).Mul(decimal.NewFromInt(100)).Round(2).StringFixed(2)
		}
		if !byDepartment {
			return nil
		}
		sizes := make([]int, 0, len(order))
		for _, k := range order {
			sizes = append(sizes, len(byDept[k]))
		}
		if derr := domain.CheckBreakdown(res.Anonymous, sizes); derr != nil {
			return crsProblem(derr, http.StatusUnprocessableEntity)
		}
		for _, k := range order {
			g := GroupResult{Stats: domain.Aggregate(qs, byDept[k])}
			if k != "" {
				d := k
				g.DepartmentID = &d
			}
			res.Groups = append(res.Groups, g)
		}
		return nil
	})
	return res, err
}
