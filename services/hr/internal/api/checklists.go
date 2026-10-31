package api

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	maxTemplateTasks = 50
	maxOffsetDays    = 365
)

var (
	templateCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	taskCode     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,29}$`)
	loanType     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
)

type loanRequest struct {
	LoanType        string  `json:"loanType"`
	ItemDescription string  `json:"itemDescription"`
	AssetID         *string `json:"assetId"`
	Serial          *string `json:"serial"`
	IssuedOn        string  `json:"issuedOn"`
	DueOn           *string `json:"dueOn"`
	ConditionOut    *string `json:"conditionOut"`
}

type loanCloseRequest struct {
	Status      string  `json:"status"`
	ClosedOn    string  `json:"closedOn"`
	ConditionIn *string `json:"conditionIn"`
}

type templateTaskRequest struct {
	Code             string  `json:"code"`
	Title            string  `json:"title"`
	OwnerRole        string  `json:"ownerRole"`
	DueOffsetDays    int     `json:"dueOffsetDays"`
	Instructions     *string `json:"instructions"`
	EvidenceRequired bool    `json:"evidenceRequired"`
}

type templateRequest struct {
	Code  string                `json:"code"`
	Kind  string                `json:"kind"`
	Name  string                `json:"name"`
	Tasks []templateTaskRequest `json:"tasks"`
}

type startChecklistRequest struct {
	TemplateID    string `json:"templateId"`
	EffectiveDate string `json:"effectiveDate"`
}

type finishTaskRequest struct {
	Status      string  `json:"status"`
	EvidenceRef *string `json:"evidenceRef"`
}

func optionalText(name string, v *string, max int, fields map[string]string) *string {
	if v == nil {
		return nil
	}
	if *v == "" || tooLong(*v, max) {
		fields[name] = "must be 1 to " + strconv.Itoa(max) + " characters"
	}
	return v
}

func validateLoan(workerID string, req loanRequest) (store.LoanedItem, map[string]string) {
	fields := map[string]string{}
	if !loanType.MatchString(req.LoanType) {
		fields["loanType"] = "must match [A-Z0-9][A-Z0-9_-]{0,19}"
	}
	if req.ItemDescription == "" || tooLong(req.ItemDescription, 200) {
		fields["itemDescription"] = "must be 1 to 200 characters"
	}
	if req.AssetID != nil && !ids.Valid(*req.AssetID) {
		fields["assetId"] = "must be a UUID"
	}
	if _, ok := parseDate(req.IssuedOn); !ok {
		fields["issuedOn"] = "must be a YYYY-MM-DD date"
	}
	if _, ok := optionalDate(req.DueOn); !ok {
		fields["dueOn"] = "must be a YYYY-MM-DD date"
	}
	in := store.LoanedItem{WorkerID: workerID, LoanType: req.LoanType, ItemDescription: req.ItemDescription, AssetID: req.AssetID, IssuedOn: req.IssuedOn, DueOn: req.DueOn}
	in.Serial = optionalText("serial", req.Serial, 80, fields)
	in.ConditionOut = optionalText("conditionOut", req.ConditionOut, 200, fields)
	return in, fields
}

// issueLoanedItem is POST /v1/hr/workers/{id}/loaned-items (HcmWorkerMaintain).
func (a *API) issueLoanedItem(w http.ResponseWriter, r *http.Request, workerID string) {
	var req loanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := validateLoan(workerID, req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.IssueLoanedItem(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "issue loaned item", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.loansIssued.Add(1)
	}
	httpx.JSON(w, status, out)
}

func (a *API) listWorkerLoans(w http.ResponseWriter, r *http.Request, workerID string) {
	items, err := a.store.ListWorkerLoans(r.Context(), workerID)
	if err != nil {
		logFailure(r, "list worker loans", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.LoanedItem]{Items: items})
}

// outstandingLoans is GET /v1/hr/loaned-items/outstanding?dueBy= (HcmWorkerInquire).
func (a *API) outstandingLoans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	dueBy := r.URL.Query().Get("dueBy")
	if _, ok := optionalDate(&dueBy); !ok {
		validationFailed(w, r, map[string]string{"dueBy": "must be a YYYY-MM-DD date"})
		return
	}
	items, err := a.store.ListOutstandingLoans(r.Context(), dueBy)
	if err != nil {
		logFailure(r, "list outstanding loans", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.LoanedItem]{Items: items})
}

func loanRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeLoanNotFound, "loaned item not found", next)
}

// closeLoanedItem is POST /v1/hr/loaned-items/{id}/close (HcmWorkerMaintain).
func (a *API) closeLoanedItem(w http.ResponseWriter, r *http.Request, id string) {
	var req loanCloseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Status != domain.LoanReturned && req.Status != domain.LoanLost {
		fields["status"] = "must be RETURNED or LOST"
	}
	if _, ok := parseDate(req.ClosedOn); !ok {
		fields["closedOn"] = "must be a YYYY-MM-DD date"
	}
	conditionIn := optionalText("conditionIn", req.ConditionIn, 200, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, changed, err := a.store.CloseLoanedItem(r.Context(), id, req.Status, req.ClosedOn, conditionIn)
	if err != nil {
		logFailure(r, "close loaned item", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.loansClosed.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func validateTemplate(req templateRequest) (store.ChecklistTemplate, map[string]string) {
	fields := map[string]string{}
	if !templateCode.MatchString(req.Code) {
		fields["code"] = "must match [A-Z0-9][A-Z0-9_-]{0,19}"
	}
	if !contains(domain.ChecklistKinds, req.Kind) {
		fields["kind"] = "must be ONBOARDING, OFFBOARDING or TRANSFER"
	}
	if req.Name == "" || tooLong(req.Name, 120) {
		fields["name"] = "must be 1 to 120 characters"
	}
	if len(req.Tasks) == 0 || len(req.Tasks) > maxTemplateTasks {
		fields["tasks"] = "needs 1 to 50 tasks"
	}
	tpl := store.ChecklistTemplate{Code: req.Code, Kind: req.Kind, Name: req.Name, Tasks: []store.TemplateTask{}}
	seen := map[string]bool{}
	for i, t := range req.Tasks {
		at := func(f string) string { return "tasks[" + strconv.Itoa(i) + "]." + f }
		if !taskCode.MatchString(t.Code) || seen[t.Code] || t.Code == domain.ReturnLoanedItemsCode {
			fields[at("code")] = "must be unique, match [A-Z0-9][A-Z0-9_-]{0,29} and not be the reserved " + domain.ReturnLoanedItemsCode
		}
		seen[t.Code] = true
		if t.Title == "" || tooLong(t.Title, 200) {
			fields[at("title")] = "must be 1 to 200 characters"
		}
		if !contains(domain.OwnerRoles, t.OwnerRole) {
			fields[at("ownerRole")] = "must be MANAGER, WORKER, HR_PARTNER or IT"
		}
		if t.DueOffsetDays < -maxOffsetDays || t.DueOffsetDays > maxOffsetDays {
			fields[at("dueOffsetDays")] = "must be -365 to 365"
		}
		if t.Instructions != nil && tooLong(*t.Instructions, 2000) {
			fields[at("instructions")] = "at most 2000 characters"
		}
		tpl.Tasks = append(tpl.Tasks, store.TemplateTask{Code: t.Code, Title: t.Title, OwnerRole: t.OwnerRole, DueOffsetDays: t.DueOffsetDays,
			Instructions: t.Instructions, EvidenceRequired: t.EvidenceRequired})
	}
	return tpl, fields
}

// createChecklistTemplate is POST /v1/hr/checklist-templates (HcmSetupAdmin).
func (a *API) createChecklistTemplate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req templateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	tpl, fields := validateTemplate(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateChecklistTemplate(r.Context(), tpl, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create checklist template", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

// listChecklistTemplates is GET /v1/hr/checklist-templates?kind= (HcmSetupAdmin).
func (a *API) listChecklistTemplates(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !contains(domain.ChecklistKinds, kind) {
		validationFailed(w, r, map[string]string{"kind": "must be ONBOARDING, OFFBOARDING or TRANSFER"})
		return
	}
	items, err := a.store.ListChecklistTemplates(r.Context(), kind)
	if err != nil {
		logFailure(r, "list checklist templates", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ChecklistTemplate]{Items: items})
}

// startChecklist is POST /v1/hr/workers/{id}/checklists (HcmActionMaintain).
func (a *API) startChecklist(w http.ResponseWriter, r *http.Request, workerID string) {
	var req startChecklistRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.TemplateID) {
		fields["templateId"] = "must be a UUID"
	}
	effective, ok := parseDate(req.EffectiveDate)
	if !ok {
		fields["effectiveDate"] = "must be a YYYY-MM-DD date"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.StartChecklist(r.Context(), workerID, req.TemplateID, effective)
	if err != nil {
		logFailure(r, "start checklist", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.checklistsStarted.Add(1)
	}
	httpx.JSON(w, status, out)
}

func (a *API) listWorkerChecklists(w http.ResponseWriter, r *http.Request, workerID string) {
	items, err := a.store.ListWorkerChecklists(r.Context(), workerID)
	if err != nil {
		logFailure(r, "list worker checklists", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Checklist]{Items: items})
}

func checklistRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeChecklistNotFound, "checklist not found", next)
}

func taskRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeTaskNotFound, "task not found", next)
}

func (a *API) getChecklist(w http.ResponseWriter, r *http.Request, id string) {
	out, err := a.store.GetChecklist(r.Context(), id)
	if err != nil {
		logFailure(r, "get checklist", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// completeChecklist is POST /v1/hr/checklists/{id}/complete (HcmActionMaintain).
func (a *API) completeChecklist(w http.ResponseWriter, r *http.Request, id string) {
	out, changed, err := a.store.CompleteChecklist(r.Context(), id)
	if err != nil {
		logFailure(r, "complete checklist", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.checklistsCompleted.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listOwnedTasks is GET /v1/hr/tasks?ownerWorkerId=&status= (HcmWorkerInquire).
func (a *API) listOwnedTasks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if !ids.Valid(q.Get("ownerWorkerId")) {
		fields["ownerWorkerId"] = "must be a UUID"
	}
	if s := q.Get("status"); s != "" && s != domain.TaskOpen && s != domain.TaskDone && s != domain.TaskSkipped {
		fields["status"] = "must be OPEN, DONE or SKIPPED"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	items, err := a.store.ListOwnedTasks(r.Context(), q.Get("ownerWorkerId"), q.Get("status"))
	if err != nil {
		logFailure(r, "list owned tasks", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WorkerTask]{Items: items})
}

// finishTask is POST /v1/hr/tasks/{id}/finish (HcmActionMaintain).
func (a *API) finishTask(w http.ResponseWriter, r *http.Request, id string) {
	var req finishTaskRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Status != domain.TaskDone && req.Status != domain.TaskSkipped {
		fields["status"] = "must be DONE or SKIPPED"
	}
	evidence := optionalText("evidenceRef", req.EvidenceRef, 500, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, changed, err := a.store.FinishTask(r.Context(), id, req.Status, evidence)
	if err != nil {
		logFailure(r, "finish task", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.tasksFinished.Add(1)
	}
	httpx.JSON(w, http.StatusOK, out)
}
