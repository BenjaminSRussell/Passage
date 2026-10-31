package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyQuestionnaireAdmin = "HcmQuestionnaireAdmin"
	dutyEssQuestionnaire   = "EssQuestionnaire"
)

type questionnaireRequest struct {
	Code      string            `json:"code"`
	Name      string            `json:"name"`
	Anonymous bool              `json:"anonymous"`
	Questions []domain.Question `json:"questions"`
}

type scheduleRequest struct {
	QuestionnaireID string `json:"questionnaireId"`
	Population      string `json:"population"`
	OpensAt         string `json:"opensAt"`
	ClosesAt        string `json:"closesAt"`
}

type answerRequest struct {
	WorkerID string                     `json:"workerId"`
	Answers  map[string]json.RawMessage `json:"answers"`
}

func questionnaireRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeQuestionnaireNotFound, "questionnaire not found", next)
}

func scheduleRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeScheduleNotFound, "questionnaire schedule not found", next)
}

// createQuestionnaire is POST /v1/hr/questionnaires (HcmQuestionnaireAdmin).
func (a *API) createQuestionnaire(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req questionnaireRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	questions, fields := domain.NormalizeQuestions(req.Questions)
	if !templateCode.MatchString(req.Code) {
		fields["code"] = "must match [A-Z0-9][A-Z0-9_-]{0,19}"
	}
	if req.Name == "" || tooLong(req.Name, 120) {
		fields["name"] = "must be 1 to 120 characters"
	}
	if len(req.Questions) == 0 || len(req.Questions) > 100 {
		fields["questions"] = "needs 1 to 100 questions"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateQuestionnaire(r.Context(), store.Questionnaire{Code: req.Code, Name: req.Name, Anonymous: req.Anonymous, Questions: questions},
		r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create questionnaire", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

func (a *API) listQuestionnaires(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListQuestionnaires(r.Context())
	if err != nil {
		logFailure(r, "list questionnaires", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Questionnaire]{Items: items})
}

func (a *API) getQuestionnaire(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetQuestionnaire(r.Context(), id)
	if err != nil {
		logFailure(r, "get questionnaire", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// createSchedule is POST /v1/hr/questionnaires/schedules (HcmQuestionnaireAdmin).
func (a *API) createSchedule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req scheduleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.QuestionnaireID) {
		fields["questionnaireId"] = "must be a UUID"
	}
	if tooLong(req.Population, 500) {
		fields["population"] = "at most 500 characters"
	}
	opens, err := time.Parse(time.RFC3339, req.OpensAt)
	if err != nil {
		fields["opensAt"] = "must be an RFC 3339 timestamp"
	}
	closes, err := time.Parse(time.RFC3339, req.ClosesAt)
	if err != nil {
		fields["closesAt"] = "must be an RFC 3339 timestamp"
	} else if fields["opensAt"] == "" && !closes.After(opens) {
		fields["closesAt"] = "must be after opensAt"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateSchedule(r.Context(), req.QuestionnaireID, req.Population, opens, closes, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create questionnaire schedule", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

// openSchedules is GET /v1/hr/questionnaires/schedules/open (EssQuestionnaire).
func (a *API) openSchedules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListOpenSchedules(r.Context())
	if err != nil {
		logFailure(r, "list open questionnaire schedules", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.QuestionnaireSchedule]{Items: items})
}

func (a *API) getSchedule(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetSchedule(r.Context(), id)
	if err != nil {
		logFailure(r, "get questionnaire schedule", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// submitAnswers is POST /v1/hr/questionnaires/schedules/{id}/answers (EssQuestionnaire).
func (a *API) submitAnswers(w http.ResponseWriter, r *http.Request, id string) {
	var req answerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WorkerID) {
		validationFailed(w, r, map[string]string{"workerId": "must be a UUID"})
		return
	}
	if err := a.store.SubmitAnswers(r.Context(), id, req.WorkerID, req.Answers); err != nil {
		logFailure(r, "submit questionnaire answers", err)
		httpx.Fail(w, r, err)
		return
	}
	a.questionnaireAnswers.Add(1)
	httpx.JSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}

// scheduleResults is GET /v1/hr/questionnaires/schedules/{id}/results?by=department
// (HcmQuestionnaireAdmin).
func (a *API) scheduleResults(w http.ResponseWriter, r *http.Request, id string) {
	by := r.URL.Query().Get("by")
	if by != "" && by != "department" {
		validationFailed(w, r, map[string]string{"by": "must be department"})
		return
	}
	out, err := a.store.QuestionnaireResults(r.Context(), id, by == "department")
	if err != nil {
		logFailure(r, "questionnaire results", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
