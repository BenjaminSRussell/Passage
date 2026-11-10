package identity

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const (
	dutyMaintainSetup = "identity.setup.maintain"

	stepTodo    = "todo"
	stepDone    = "done"
	stepSkipped = "skipped"

	maxWorkspaceConfigBytes = 256 << 10
)

// setupStep is one stop in the onboarding wizard, in the order a person sets a company up in: what the company
// does, then what finance needs, then what operations needs, then people and data.
type setupStep struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

var setupSteps = []setupStep{
	{"features", "What your company does", "Pick the industry template, then switch single features on or off."},
	{"company", "Company and legal entity", "Name your company and its legal entity. The country sets the defaults."},
	{"fiscal-calendar", "Fiscal calendar", "Define the fiscal year and its periods."},
	{"chart-of-accounts", "Chart of accounts", "Start from a template for your industry and country."},
	{"currencies", "Currencies", "Choose the currencies you trade in and their exchange rates."},
	{"tax", "Tax", "Set up tax codes and rates."},
	{"sites-warehouses", "Sites and warehouses", "Add the places where you make, store and ship goods."},
	{"number-sequences", "Number sequences", "Check how orders, invoices and other documents are numbered."},
	{"users-roles", "Users and roles", "Invite people and give them the workspace they need."},
	{"import", "Import your first records", "Bring in vendors, customers and products from a spreadsheet."},
}

type TenantSetupAPI struct {
	pool *pgxpool.Pool
}

func NewTenantSetupAPI(pool *pgxpool.Pool) *TenantSetupAPI {
	return &TenantSetupAPI{pool: pool}
}

func (a *TenantSetupAPI) Register(r chi.Router) {
	r.Get("/v1/tenant/setup", a.get)
	r.With(requireAnyDuty(dutyMaintainSetup)).Put("/v1/tenant/setup/steps/{step}", a.putStep)
	r.Get("/v1/tenant/workspace-config", a.getWorkspaceConfig)
	r.With(requireAnyDuty(dutyMaintainSetup)).Put("/v1/tenant/workspace-config", a.putWorkspaceConfig)
}

type setupStepState struct {
	setupStep
	Status    string     `json:"status"`
	ChangedBy string     `json:"changedBy,omitempty"`
	ChangedAt *time.Time `json:"changedAt,omitempty"`
}

type setupResponse struct {
	Steps    []setupStepState `json:"steps"`
	Total    int              `json:"total"`
	Finished int              `json:"finished"`
	Complete bool             `json:"complete"`
}

type storedStep struct {
	status    string
	changedBy string
	changedAt time.Time
}

func loadSetup(r *http.Request, tx pgx.Tx, tenantID string) (setupResponse, error) {
	rows, err := tx.Query(r.Context(), `select step_key, status, updated_by, updated_at from identity_tenant_setup_steps where tenant_id = $1`, tenantID)
	if err != nil {
		return setupResponse{}, err
	}
	defer rows.Close()
	stored := map[string]storedStep{}
	for rows.Next() {
		var key string
		var s storedStep
		if err := rows.Scan(&key, &s.status, &s.changedBy, &s.changedAt); err != nil {
			return setupResponse{}, err
		}
		stored[key] = s
	}
	if err := rows.Err(); err != nil {
		return setupResponse{}, err
	}
	body := setupResponse{Total: len(setupSteps)}
	for _, step := range setupSteps {
		state := setupStepState{setupStep: step, Status: stepTodo}
		if s, ok := stored[step.Key]; ok {
			at := s.changedAt
			state.Status, state.ChangedBy, state.ChangedAt = s.status, s.changedBy, &at
			body.Finished++
		}
		body.Steps = append(body.Steps, state)
	}
	body.Complete = body.Finished == body.Total
	return body, nil
}

func (a *TenantSetupAPI) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body setupResponse
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) (err error) {
		body, err = loadSetup(r, tx, p.TenantID)
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

type putStepRequest struct {
	Status string `json:"status"`
}

func (a *TenantSetupAPI) putStep(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	if !dbx.TenantWide(p, dutyMaintainSetup) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "changing the setup of a tenant requires the tenant.administer duty and a request without a legal-entity scope"))
		return
	}
	key := chi.URLParam(r, "step")
	known := false
	for _, s := range setupSteps {
		known = known || s.Key == key
	}
	if !known {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "no such setup step"))
		return
	}
	var req putStepRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Status != stepTodo && req.Status != stepDone && req.Status != stepSkipped {
		invalidFields(w, r, "invalid setup step", map[string]string{"status": "must be todo, done or skipped"})
		return
	}
	var body setupResponse
	err := withAdminTx(r.Context(), a.pool, p, dutyMaintainSetup, func(tx pgx.Tx) error {
		if req.Status == stepTodo {
			if _, err := tx.Exec(r.Context(), `delete from identity_tenant_setup_steps where tenant_id = $1 and step_key = $2`, p.TenantID, key); err != nil {
				return err
			}
		} else if _, err := tx.Exec(r.Context(), `insert into identity_tenant_setup_steps (tenant_id, step_key, status, updated_by)
values ($1, $2, $3, $4)
on conflict (tenant_id, step_key) do update set status = excluded.status, updated_by = excluded.updated_by, updated_at = now()`, p.TenantID, key, req.Status, p.UserID); err != nil {
			return err
		}
		if err := record(r.Context(), tx, p.TenantID, p.UserID, "tenantsetup", p.TenantID, "updated", map[string]any{"step": key, "status": req.Status}); err != nil {
			return err
		}
		var err error
		body, err = loadSetup(r, tx, p.TenantID)
		return err
	})
	if err != nil {
		failWrite(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

type workspaceConfigBody struct {
	Config json.RawMessage `json:"config"`
}

func (a *TenantSetupAPI) getWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var raw []byte
	err := dbx.WithTenantTx(r.Context(), a.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `select workspace_config from identity_tenant_settings where tenant_id = $1`, p.TenantID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	body := workspaceConfigBody{Config: json.RawMessage("null")}
	if len(raw) > 0 {
		body.Config = raw
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *TenantSetupAPI) putWorkspaceConfig(w http.ResponseWriter, r *http.Request) {
	p, ok := actingPrincipal(w, r)
	if !ok {
		return
	}
	if !dbx.TenantWide(p, dutyMaintainSetup) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbidden, "changing the workspaces of a tenant requires the tenant.administer duty and a request without a legal-entity scope"))
		return
	}
	var req workspaceConfigBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var stored any
	if len(req.Config) > 0 && string(req.Config) != "null" {
		var obj map[string]json.RawMessage
		if len(req.Config) > maxWorkspaceConfigBytes || json.Unmarshal(req.Config, &obj) != nil {
			invalidFields(w, r, "invalid workspace configuration", map[string]string{"config": "must be a JSON object under 256 KB, or null to go back to the shipped workspaces"})
			return
		}
		fields := map[string]string{}
		for k := range obj {
			if k != "areas" && k != "workspaces" && k != "hide" {
				fields[k] = "is not a workspace setting; use areas, workspaces or hide"
			}
		}
		if len(fields) > 0 {
			invalidFields(w, r, "invalid workspace configuration", fields)
			return
		}
		stored = string(req.Config)
	}
	err := withAdminTx(r.Context(), a.pool, p, dutyMaintainSetup, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into identity_tenant_settings (tenant_id, workspace_config, updated_by)
values ($1, $2::jsonb, $3)
on conflict (tenant_id) do update set workspace_config = excluded.workspace_config, updated_by = excluded.updated_by, updated_at = now()`, p.TenantID, stored, p.UserID); err != nil {
			return err
		}
		return record(r.Context(), tx, p.TenantID, p.UserID, "tenantworkspaces", p.TenantID, "updated", map[string]any{"reset": stored == nil})
	})
	if err != nil {
		failWrite(w, r, err)
		return
	}
	if req.Config == nil {
		req.Config = json.RawMessage("null")
	}
	httpx.JSON(w, http.StatusOK, req)
}
