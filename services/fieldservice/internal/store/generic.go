package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// Problem codes of generic case management (FR-45.12). Each is listed in the service README.
const (
	CodeCategoryDuplicate = "CASE-CAT-001"
	CodeCategoryUnknown   = "CASE-CAT-003"
	CodeProcessDuplicate  = "CASE-PRC-002"
	CodeProcessUnknown    = "CASE-PRC-003"
	CodeGenericUnknown    = "CASE-GEN-001"
)

// maxCaseSetup caps the process and category lists, which are setup data.
const maxCaseSetup = 500

// Case log kinds.
const (
	logCreated     = "CREATED"
	logStage       = "STAGE"
	logStatus      = "STATUS"
	logAssociation = "ASSOCIATION"
	logReopened    = "REOPENED"
)

// Category is a node of the case category tree (FR-45.12).
type Category struct {
	ID               string   `json:"id"`
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	CategoryType     string   `json:"categoryType"`
	ParentID         *string  `json:"parentId,omitempty"`
	ProcessID        *string  `json:"processId,omitempty"`
	SLAProfileID     *string  `json:"slaProfileId,omitempty"`
	DefaultPriority  int      `json:"defaultPriority"`
	ReopenWindowDays int      `json:"reopenWindowDays"`
	AccessDuty       string   `json:"accessDuty"`
	ResolutionCodes  []string `json:"resolutionCodes"`
}

// Process is the ordered set of stages the cases of a category move through (BR-45.9).
type Process struct {
	ID     string         `json:"id"`
	Code   string         `json:"code"`
	Name   string         `json:"name"`
	Stages []domain.Stage `json:"stages"`
}

// CaseAssociation links a generic case to any registry record (FR-45.12).
type CaseAssociation struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	Role       string `json:"role"`
}

// CaseLogEntry is one line of a case's log.
type CaseLogEntry struct {
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
	Kind  string    `json:"kind"`
	Text  string    `json:"text"`
}

// GenericCase is a case in a non-service category. SERVICE and CUSTOMER_SERVICE
// cases stay in service_case with the entitlement and SLA engine.
type GenericCase struct {
	ID               string            `json:"id"`
	Number           string            `json:"number"`
	CategoryID       string            `json:"categoryId"`
	CategoryCode     string            `json:"categoryCode"`
	CategoryType     string            `json:"categoryType"`
	LegalEntityID    string            `json:"legalEntityId"`
	CustomerPartyID  *string           `json:"customerPartyId,omitempty"`
	OwnerID          string            `json:"ownerId"`
	Subject          string            `json:"subject"`
	Description      string            `json:"description"`
	Priority         int               `json:"priority"`
	StageCode        string            `json:"stageCode"`
	Status           string            `json:"status"`
	ResolutionCode   *string           `json:"resolutionCode,omitempty"`
	ResolutionText   string            `json:"resolutionText"`
	SLAProfileID     *string           `json:"slaProfileId,omitempty"`
	SLAResponseDue   *time.Time        `json:"slaResponseDue,omitempty"`
	SLAResolutionDue *time.Time        `json:"slaResolutionDue,omitempty"`
	OpenedAt         time.Time         `json:"openedAt"`
	ResolvedAt       *time.Time        `json:"resolvedAt,omitempty"`
	ClosedAt         *time.Time        `json:"closedAt,omitempty"`
	ReopenedFrom     *string           `json:"reopenedFrom,omitempty"`
	Version          int               `json:"version"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	Associations     []CaseAssociation `json:"associations"`
	Log              []CaseLogEntry    `json:"log"`

	// accessDuty and reopenWindow come from the category; they drive BR-45.10 and BR-45.9.
	accessDuty   string
	reopenWindow int
}

// OpenGenericInput is a case to open. Priority 0 takes the category default.
type OpenGenericInput struct {
	CategoryID      string
	LegalEntityID   string
	CustomerPartyID *string
	Subject         string
	Description     string
	Priority        int
	Associations    []CaseAssociation
	OpenedAt        time.Time
}

// CaseMove is a stage, status or reopen request on one case at a version.
type CaseMove struct {
	ID             string
	Version        int
	ToStage        string
	ToStatus       string
	ResolutionCode string
	ResolutionText string
	Reason         string
	At             time.Time
}

// ReopenResult is the case that is OPEN after a reopen and whether it is a new
// case linked by reopenedFrom.
type ReopenResult struct {
	Case    GenericCase `json:"case"`
	Created bool        `json:"created"`
}

// GenericFilter narrows the case list. Empty fields do not filter; EntityType and
// EntityID together keep the cases associated with that record.
type GenericFilter struct {
	CategoryID string
	StageCode  string
	Status     string
	OwnerID    string
	EntityType string
	EntityID   string
	Cursor     string
	Limit      int
}

// caller is the principal of ctx: its user id and duties decide BR-45.10.
func caller(ctx context.Context) tenant.Principal {
	p, _ := tenant.FromContext(ctx)
	return p
}

// mayAccess applies BR-45.10 to a case for the caller of ctx.
func mayAccess(ctx context.Context, accessDuty, ownerID string) bool {
	p := caller(ctx)
	return domain.CanAccess(accessDuty, p.HasDuty(accessDuty), p.UserID != "" && p.UserID == ownerID)
}

func forbidCategory(code string) error {
	return httpx.NewProblem(http.StatusForbidden, domain.CodeCaseSecurity, "you do not have access to cases in category "+code)
}

// CreateProcess stores a process definition; the API validates the stages first.
func (s *Postgres) CreateProcess(ctx context.Context, in Process, key, requestHash string) (Process, bool, error) {
	tid := tenantOf(ctx)
	var out Process
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_case_process", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getProcess(ctx, tx, tid, id)
			return err
		}
		stages, err := json.Marshal(in.Stages)
		if err != nil {
			return err
		}
		out = in
		out.ID = ids.New()
		_, err = tx.Exec(ctx, `insert into service_case_process (tenant_id, id, code, name, stages, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7)`, tid, out.ID, in.Code, in.Name, stages, nullable(key), requestHash)
		return err
	})
	if isUnique(err) {
		return Process{}, false, httpx.NewProblem(http.StatusConflict, CodeProcessDuplicate, "a case process with this code already exists")
	}
	return out, replayed, err
}

// ListProcesses returns every case process of the tenant ordered by code.
func (s *Postgres) ListProcesses(ctx context.Context) ([]Process, error) {
	tid := tenantOf(ctx)
	out := []Process{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, code, name, stages from service_case_process where tenant_id = $1 order by code limit $2`,
			tid, maxCaseSetup)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Process, error) { return scanProcess(r) })
		return err
	})
	return out, err
}

func scanProcess(r pgx.Row) (Process, error) {
	var p Process
	var stages []byte
	if err := r.Scan(&p.ID, &p.Code, &p.Name, &stages); err != nil {
		return p, err
	}
	return p, json.Unmarshal(stages, &p.Stages)
}

// getProcess loads a process that a command references; a missing one is a 422.
func getProcess(ctx context.Context, tx pgx.Tx, tenantID, id string) (Process, error) {
	p, err := scanProcess(tx.QueryRow(ctx, `select id::text, code, name, stages from service_case_process where tenant_id = $1 and id = $2`,
		tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, unprocessable(CodeProcessUnknown, "case process not found")
	}
	return p, err
}

// CreateCategory stores a category after checking that its parent, process and
// SLA profile exist.
func (s *Postgres) CreateCategory(ctx context.Context, in Category, key, requestHash string) (Category, bool, error) {
	tid := tenantOf(ctx)
	var out Category
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_case_category", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getCategory(ctx, tx, tid, id)
			return err
		}
		if in.ParentID != nil {
			if _, err := getCategory(ctx, tx, tid, *in.ParentID); err != nil {
				return err
			}
		}
		if in.ProcessID != nil {
			if _, err := getProcess(ctx, tx, tid, *in.ProcessID); err != nil {
				return err
			}
		}
		if in.SLAProfileID != nil {
			if _, err := getProfile(ctx, tx, tid, *in.SLAProfileID); err != nil {
				return err
			}
		}
		codes, err := json.Marshal(in.ResolutionCodes)
		if err != nil {
			return err
		}
		out = in
		out.ID = ids.New()
		_, err = tx.Exec(ctx, `insert into service_case_category (tenant_id, id, code, name, category_type, parent_id, process_id,
			sla_profile_id, default_priority, reopen_window_days, access_duty, resolution_codes, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			tid, out.ID, in.Code, in.Name, in.CategoryType, in.ParentID, in.ProcessID, in.SLAProfileID, in.DefaultPriority,
			in.ReopenWindowDays, in.AccessDuty, codes, nullable(key), requestHash)
		return err
	})
	if isUnique(err) {
		return Category{}, false, httpx.NewProblem(http.StatusConflict, CodeCategoryDuplicate, "a case category with this code already exists")
	}
	return out, replayed, err
}

// ListCategories returns every category of the tenant ordered by code.
func (s *Postgres) ListCategories(ctx context.Context) ([]Category, error) {
	tid := tenantOf(ctx)
	out := []Category{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+categoryColumns+` from service_case_category where tenant_id = $1 order by code limit $2`,
			tid, maxCaseSetup)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Category, error) { return scanCategory(r) })
		return err
	})
	return out, err
}

const categoryColumns = `id::text, code, name, category_type, parent_id::text, process_id::text, sla_profile_id::text,
	default_priority, reopen_window_days, access_duty, resolution_codes`

func scanCategory(r pgx.Row) (Category, error) {
	var c Category
	var codes []byte
	if err := r.Scan(&c.ID, &c.Code, &c.Name, &c.CategoryType, &c.ParentID, &c.ProcessID, &c.SLAProfileID,
		&c.DefaultPriority, &c.ReopenWindowDays, &c.AccessDuty, &codes); err != nil {
		return c, err
	}
	return c, json.Unmarshal(codes, &c.ResolutionCodes)
}

// getCategory loads a category that a command references; a missing one is a 422.
func getCategory(ctx context.Context, tx pgx.Tx, tenantID, id string) (Category, error) {
	c, err := scanCategory(tx.QueryRow(ctx, `select `+categoryColumns+` from service_case_category where tenant_id = $1 and id = $2`,
		tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, unprocessable(CodeCategoryUnknown, "case category not found")
	}
	return c, err
}

// firstStage is where a case of the category starts (BR-45.9); empty without a process.
func firstStage(ctx context.Context, tx pgx.Tx, tenantID string, cat Category) (string, error) {
	if cat.ProcessID == nil {
		return "", nil
	}
	p, err := getProcess(ctx, tx, tenantID, *cat.ProcessID)
	if err != nil || len(p.Stages) == 0 {
		return "", err
	}
	return p.Stages[0].Code, nil
}

// categoryClocks starts the response and resolution dues from the category's SLA
// profile (FR-45.12); a category without a profile has no clocks.
func categoryClocks(ctx context.Context, tx pgx.Tx, tenantID string, cat Category, priority int, opened time.Time) (*time.Time, *time.Time, error) {
	if cat.SLAProfileID == nil {
		return nil, nil, nil
	}
	profile, err := getProfile(ctx, tx, tenantID, *cat.SLAProfileID)
	if err != nil {
		return nil, nil, err
	}
	target, ok := profile.Target(priority)
	if !ok {
		return nil, nil, unprocessable(CodeNoTarget, "the category's SLA profile has no target for this priority")
	}
	cal, err := profile.Calendar()
	if err != nil {
		return nil, nil, unprocessable(CodeInvalidCalendar, "the category's SLA profile calendar is not valid")
	}
	clocks := domain.StartClocks(opened, cal, target)
	return &clocks.ResponseDue, &clocks.ResolutionDue, nil
}

// OpenGenericCase opens a case in a non-service category (FR-45.12). The caller
// becomes the owner and must hold the category's access duty when it has one
// (CaseCreate is category-scoped, BR-45.10). SERVICE and CUSTOMER_SERVICE open
// through /v1/service/cases (CASE-CAT-002); COLLECTIONS needs a customer
// (CASE-CUS-001). The case starts at the first stage of the category's process.
func (s *Postgres) OpenGenericCase(ctx context.Context, in OpenGenericInput, key, requestHash string) (GenericCase, bool, error) {
	tid := tenantOf(ctx)
	owner := caller(ctx).UserID
	var out GenericCase
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_generic_case", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getGenericCase(ctx, tx, tid, id, false)
			return err
		}
		cat, err := getCategory(ctx, tx, tid, in.CategoryID)
		if err != nil {
			return err
		}
		if cat.AccessDuty != "" && !caller(ctx).HasDuty(cat.AccessDuty) {
			return forbidCategory(cat.Code)
		}
		if domain.ServiceRouted(cat.CategoryType) {
			return unprocessable(domain.CodeCategoryRouted, "SERVICE and CUSTOMER_SERVICE cases open through /v1/service/cases")
		}
		if domain.RequiresCustomer(cat.CategoryType) && in.CustomerPartyID == nil {
			return unprocessable(domain.CodeCustomerRequired, "cases in a "+cat.CategoryType+" category require a customer party")
		}
		priority := in.Priority
		if priority == 0 {
			priority = cat.DefaultPriority
		}
		c := GenericCase{CategoryID: cat.ID, LegalEntityID: in.LegalEntityID, CustomerPartyID: in.CustomerPartyID, OwnerID: owner,
			Subject: in.Subject, Description: in.Description, Priority: priority, OpenedAt: in.OpenedAt}
		id, err = insertGenericCase(ctx, tx, tid, cat, c, in.Associations, "opened in category "+cat.Code, key, requestHash)
		if err != nil {
			return err
		}
		out, err = getGenericCase(ctx, tx, tid, id, false)
		return err
	})
	return out, replayed, err
}

// insertGenericCase allocates a CS number, starts the stage and the category's
// clocks and stores the case with its associations and a CREATED log line.
func insertGenericCase(ctx context.Context, tx pgx.Tx, tid string, cat Category, c GenericCase, assoc []CaseAssociation,
	logText, key, requestHash string) (string, error) {
	stage, err := firstStage(ctx, tx, tid, cat)
	if err != nil {
		return "", err
	}
	responseDue, resolutionDue, err := categoryClocks(ctx, tx, tid, cat, c.Priority, c.OpenedAt)
	if err != nil {
		return "", err
	}
	number, err := nextNumber(ctx, tx, tid, "case", "CS")
	if err != nil {
		return "", err
	}
	id := ids.New()
	_, err = tx.Exec(ctx, `insert into service_generic_case (tenant_id, id, number, category_id, legal_entity_id, customer_party_id,
		owner_id, subject, description, priority, stage_code, sla_profile_id, sla_response_due, sla_resolution_due, opened_at,
		reopened_from, idempotency_key, request_hash, created_at, updated_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $15, $15)`,
		tid, id, number, cat.ID, c.LegalEntityID, c.CustomerPartyID, c.OwnerID, c.Subject, c.Description, c.Priority, stage,
		cat.SLAProfileID, responseDue, resolutionDue, c.OpenedAt, c.ReopenedFrom, nullable(key), nullable(requestHash))
	if err != nil {
		return "", err
	}
	for _, a := range assoc {
		if err := upsertAssociation(ctx, tx, tid, id, a); err != nil {
			return "", err
		}
	}
	return id, addLog(ctx, tx, tid, id, c.OwnerID, logCreated, logText, c.OpenedAt)
}

// GetGenericCase loads one case with its associations and log; a caller outside
// the category's access gets CASE-SEC-001 (BR-45.10, AC-45.11).
func (s *Postgres) GetGenericCase(ctx context.Context, id string) (GenericCase, error) {
	tid := tenantOf(ctx)
	var c GenericCase
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = getGenericCase(ctx, tx, tid, id, false)
		if err == nil && !mayAccess(ctx, c.accessDuty, c.OwnerID) {
			err = forbidCategory(c.CategoryCode)
		}
		return err
	})
	return c, err
}

// ListGenericCases returns the cases the caller may read (BR-45.10), newest first,
// inside the caller's legal-entity scope. Cases of categories the caller has no
// access to are left out rather than failing the list.
func (s *Postgres) ListGenericCases(ctx context.Context, f GenericFilter) (Page[GenericCase], error) {
	tid := tenantOf(ctx)
	p := caller(ctx)
	duties := make([]string, 0, len(p.Duties))
	for d := range p.Duties {
		duties = append(duties, d)
	}
	sort.Strings(duties)
	return listPage(ctx, s, f.Limit, func(c GenericCase) string { return c.ID }, scanGenericCase,
		`select `+genericColumns+` from service_generic_case g join service_case_category k on k.tenant_id = g.tenant_id and k.id = g.category_id
		where g.tenant_id = $1
		  and (k.access_duty = '' or k.access_duty = any($2::text[]) or ($3 <> '' and g.owner_id = $3))
		  and ($4 = '' or g.category_id::text = $4) and ($5 = '' or g.status = $5) and ($6 = '' or g.stage_code = $6)
		  and ($7 = '' or g.owner_id = $7)
		  and ($8 = '' or exists (select 1 from service_case_association a where a.tenant_id = g.tenant_id and a.case_id = g.id
		       and a.entity_type = $8 and a.entity_id::text = $9))
		  and `+keyset("g", "service_generic_case", 10)+`
		order by g.created_at desc, g.id desc limit $11`,
		tid, duties, p.UserID, f.CategoryID, f.Status, f.StageCode, f.OwnerID, f.EntityType, f.EntityID, f.Cursor)
}

const genericColumns = `g.id::text, g.number, g.category_id::text, k.code, k.category_type, k.access_duty, k.reopen_window_days,
	g.legal_entity_id::text, g.customer_party_id::text, g.owner_id, g.subject, g.description, g.priority, g.stage_code, g.status,
	g.resolution_code, g.resolution_text, g.sla_profile_id::text, g.sla_response_due, g.sla_resolution_due, g.opened_at,
	g.resolved_at, g.closed_at, g.reopened_from::text, g.version, g.created_at, g.updated_at`

func scanGenericCase(r pgx.Row) (GenericCase, error) {
	var c GenericCase
	err := r.Scan(&c.ID, &c.Number, &c.CategoryID, &c.CategoryCode, &c.CategoryType, &c.accessDuty, &c.reopenWindow,
		&c.LegalEntityID, &c.CustomerPartyID, &c.OwnerID, &c.Subject, &c.Description, &c.Priority, &c.StageCode, &c.Status,
		&c.ResolutionCode, &c.ResolutionText, &c.SLAProfileID, &c.SLAResponseDue, &c.SLAResolutionDue, &c.OpenedAt,
		&c.ResolvedAt, &c.ClosedAt, &c.ReopenedFrom, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	c.Associations = []CaseAssociation{}
	c.Log = []CaseLogEntry{}
	return c, err
}

// getGenericCase loads a case with its associations and log; lock takes the case
// row FOR UPDATE so stage, status and association changes serialize on it.
func getGenericCase(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (GenericCase, error) {
	q := `select ` + genericColumns + ` from service_generic_case g join service_case_category k on k.tenant_id = g.tenant_id and k.id = g.category_id
		where g.tenant_id = $1 and g.id = $2`
	if lock {
		q += ` for update of g`
	}
	c, err := scanGenericCase(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NewProblem(http.StatusNotFound, CodeGenericUnknown, "case not found")
	}
	if err != nil {
		return c, err
	}
	rows, err := tx.Query(ctx, `select entity_type, entity_id::text, role from service_case_association
		where tenant_id = $1 and case_id = $2 order by created_at, entity_type, entity_id`, tenantID, id)
	if err != nil {
		return c, err
	}
	if c.Associations, err = pgx.CollectRows(rows, pgx.RowToStructByPos[CaseAssociation]); err != nil {
		return c, err
	}
	rows, err = tx.Query(ctx, `select at, actor, kind, text from service_case_log where tenant_id = $1 and case_id = $2 order by seq`,
		tenantID, id)
	if err != nil {
		return c, err
	}
	c.Log, err = pgx.CollectRows(rows, pgx.RowToStructByPos[CaseLogEntry])
	return c, err
}

// moveCase locks the case, checks the caller's access, answers a replay of the
// same Idempotency-Key with the stored answer, checks the version and runs apply.
// The answer is stored under the key in the same transaction.
func (s *Postgres) moveCase(ctx context.Context, id string, version int, key, requestHash string, out any,
	apply func(tx pgx.Tx, c GenericCase) (any, error)) (bool, error) {
	tid := tenantOf(ctx)
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := getGenericCase(ctx, tx, tid, id, true)
		if err != nil {
			return err
		}
		if !mayAccess(ctx, c.accessDuty, c.OwnerID) {
			return forbidCategory(c.CategoryCode)
		}
		if replayed, err = keyedAnswer(ctx, tx, tid, key, requestHash, out); err != nil || replayed {
			return err
		}
		if version != 0 && c.Version != version {
			return versionConflict(c.Version)
		}
		answer, err := apply(tx, c)
		if err != nil {
			return err
		}
		body, err := json.Marshal(answer)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(body, out); err != nil {
			return err
		}
		return storeAnswer(ctx, tx, tid, key, requestHash, answer)
	})
	return replayed, err
}

// AdvanceStage moves an OPEN case to the next stage once the current stage's
// required activities are DONE (CASE-STG-001), or back one stage with a reason
// (BR-45.9, AC-45.10). The move is logged and dev.passage.case.stage.changed.v1 is
// written to the outbox in the same transaction.
func (s *Postgres) AdvanceStage(ctx context.Context, m CaseMove, key, requestHash string) (GenericCase, bool, error) {
	tid := tenantOf(ctx)
	actor := caller(ctx).UserID
	var out GenericCase
	replayed, err := s.moveCase(ctx, m.ID, m.Version, key, requestHash, &out, func(tx pgx.Tx, c GenericCase) (any, error) {
		if c.Status != domain.GenericOpen {
			return nil, unprocessable(domain.CodeCaseState, "only an OPEN case can change stage")
		}
		cat, err := getCategory(ctx, tx, tid, c.CategoryID)
		if err != nil {
			return nil, err
		}
		if cat.ProcessID == nil {
			return nil, unprocessable(domain.CodeStageInvalid, "the case's category has no case process")
		}
		proc, err := getProcess(ctx, tx, tid, *cat.ProcessID)
		if err != nil {
			return nil, err
		}
		done, err := doneSubjects(ctx, tx, tid, c.ID)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckStage(proc.Stages, c.StageCode, m.ToStage, done, m.Reason); err != nil {
			return nil, ruleProblem(err)
		}
		if _, err := tx.Exec(ctx, `update service_generic_case set stage_code = $3, version = version + 1, updated_at = $4
			where tenant_id = $1 and id = $2`, tid, c.ID, m.ToStage, m.At); err != nil {
			return nil, err
		}
		text := fmt.Sprintf("stage %s to %s", c.StageCode, m.ToStage)
		if m.Reason != "" {
			text += ": " + m.Reason
		}
		if err := addLog(ctx, tx, tid, c.ID, actor, logStage, text, m.At); err != nil {
			return nil, err
		}
		if err := enqueue(ctx, tx, "case", c.ID, svcevents.CaseStageChanged, tid, svcevents.CaseStageChangedData{
			CaseID: c.ID, Number: c.Number, CategoryID: c.CategoryID, From: c.StageCode, To: m.ToStage,
			At: m.At.UTC().Format(time.RFC3339), Actor: actor, Reason: m.Reason,
		}); err != nil {
			return nil, err
		}
		return getGenericCase(ctx, tx, tid, c.ID, false)
	})
	return out, replayed, err
}

// doneSubjects returns the subjects of the DONE activities associated with the case.
func doneSubjects(ctx context.Context, tx pgx.Tx, tenantID, caseID string) (map[string]bool, error) {
	probe, err := json.Marshal([]Association{{EntityType: "case", EntityID: caseID}})
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select distinct subject from service_activity
		where tenant_id = $1 and status = 'DONE' and associations @> $2::jsonb`, tenantID, probe)
	if err != nil {
		return nil, err
	}
	subjects, err := pgx.CollectRows(rows, pgx.RowTo[string])
	done := map[string]bool{}
	for _, s := range subjects {
		done[s] = true
	}
	return done, err
}

// TransitionGenericCase resolves (with a resolution code from the category's list,
// BR-45.9), closes or cancels a case.
func (s *Postgres) TransitionGenericCase(ctx context.Context, m CaseMove, key, requestHash string) (GenericCase, bool, error) {
	tid := tenantOf(ctx)
	actor := caller(ctx).UserID
	var out GenericCase
	replayed, err := s.moveCase(ctx, m.ID, m.Version, key, requestHash, &out, func(tx pgx.Tx, c GenericCase) (any, error) {
		cat, err := getCategory(ctx, tx, tid, c.CategoryID)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckTransition(c.Status, m.ToStatus, m.ResolutionCode, cat.ResolutionCodes); err != nil {
			return nil, ruleProblem(err)
		}
		// RESOLVED records the resolution; CLOSED and CANCELED end the case.
		set := `closed_at = $4`
		args := []any{tid, c.ID, m.ToStatus, m.At}
		if m.ToStatus == domain.GenericResolved {
			set = `resolution_code = $5, resolution_text = $6, resolved_at = $4`
			args = append(args, m.ResolutionCode, m.ResolutionText)
		}
		if _, err := tx.Exec(ctx, `update service_generic_case set status = $3, `+set+`, version = version + 1, updated_at = $4
			where tenant_id = $1 and id = $2`, args...); err != nil {
			return nil, err
		}
		text := c.Status + " to " + m.ToStatus
		if m.ResolutionCode != "" && m.ToStatus == domain.GenericResolved {
			text += " (" + m.ResolutionCode + ")"
		}
		if m.Reason != "" {
			text += ": " + m.Reason
		}
		if err := addLog(ctx, tx, tid, c.ID, actor, logStatus, text, m.At); err != nil {
			return nil, err
		}
		return getGenericCase(ctx, tx, tid, c.ID, false)
	})
	return out, replayed, err
}

// ReopenGenericCase applies the BR-45.9 reopen rule to a RESOLVED or CLOSED case:
// inside the category's reopen window the case itself returns to OPEN at its
// stage with its resolution cleared; after it a new OPEN case is created at the
// first stage, linked by reopenedFrom, with the original's associations.
func (s *Postgres) ReopenGenericCase(ctx context.Context, m CaseMove, key, requestHash string) (ReopenResult, bool, error) {
	tid := tenantOf(ctx)
	actor := caller(ctx).UserID
	var out ReopenResult
	replayed, err := s.moveCase(ctx, m.ID, m.Version, key, requestHash, &out, func(tx pgx.Tx, c GenericCase) (any, error) {
		inPlace, err := domain.ReopenInPlace(c.Status, c.ResolvedAt, m.At, c.reopenWindow)
		if err != nil {
			return nil, ruleProblem(err)
		}
		if inPlace {
			if _, err := tx.Exec(ctx, `update service_generic_case set status = 'OPEN', resolution_code = null, resolution_text = '',
				resolved_at = null, closed_at = null, version = version + 1, updated_at = $3 where tenant_id = $1 and id = $2`,
				tid, c.ID, m.At); err != nil {
				return nil, err
			}
			if err := addLog(ctx, tx, tid, c.ID, actor, logReopened, c.Status+" to OPEN: "+m.Reason, m.At); err != nil {
				return nil, err
			}
			reopened, err := getGenericCase(ctx, tx, tid, c.ID, false)
			return ReopenResult{Case: reopened}, err
		}
		cat, err := getCategory(ctx, tx, tid, c.CategoryID)
		if err != nil {
			return nil, err
		}
		next := GenericCase{LegalEntityID: c.LegalEntityID, CustomerPartyID: c.CustomerPartyID, OwnerID: c.OwnerID,
			Subject: c.Subject, Description: c.Description, Priority: c.Priority, OpenedAt: m.At, ReopenedFrom: &c.ID}
		id, err := insertGenericCase(ctx, tx, tid, cat, next, c.Associations, "reopened from "+c.Number+": "+m.Reason, "", "")
		if err != nil {
			return nil, err
		}
		created, err := getGenericCase(ctx, tx, tid, id, false)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `update service_generic_case set version = version + 1, updated_at = $3 where tenant_id = $1 and id = $2`,
			tid, c.ID, m.At); err != nil {
			return nil, err
		}
		if err := addLog(ctx, tx, tid, c.ID, actor, logReopened, "reopened as "+created.Number+": "+m.Reason, m.At); err != nil {
			return nil, err
		}
		return ReopenResult{Case: created, Created: true}, nil
	})
	return out, replayed, err
}

// AddAssociation links a record to a case that is not CLOSED or CANCELED. A repeat
// of the same record replaces its role. The case version moves, since its
// representation changes, but the call needs no If-Match (PRD 8.45.5: key only).
func (s *Postgres) AddAssociation(ctx context.Context, caseID string, in CaseAssociation, at time.Time, key, requestHash string) (GenericCase, bool, error) {
	tid := tenantOf(ctx)
	actor := caller(ctx).UserID
	var out GenericCase
	replayed, err := s.moveCase(ctx, caseID, 0, key, requestHash, &out, func(tx pgx.Tx, c GenericCase) (any, error) {
		if c.Status == domain.GenericClosed || c.Status == domain.GenericCanceled {
			return nil, unprocessable(domain.CodeCaseState, "a "+c.Status+" case takes no new associations")
		}
		if err := upsertAssociation(ctx, tx, tid, c.ID, in); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `update service_generic_case set version = version + 1, updated_at = $3 where tenant_id = $1 and id = $2`,
			tid, c.ID, at); err != nil {
			return nil, err
		}
		if err := addLog(ctx, tx, tid, c.ID, actor, logAssociation, in.Role+" "+in.EntityType+" "+in.EntityID, at); err != nil {
			return nil, err
		}
		return getGenericCase(ctx, tx, tid, c.ID, false)
	})
	return out, replayed, err
}

func upsertAssociation(ctx context.Context, tx pgx.Tx, tenantID, caseID string, a CaseAssociation) error {
	_, err := tx.Exec(ctx, `insert into service_case_association (tenant_id, case_id, entity_type, entity_id, role)
		values ($1, $2, $3, $4, $5)
		on conflict (tenant_id, case_id, entity_type, entity_id) do update set role = excluded.role`,
		tenantID, caseID, a.EntityType, a.EntityID, a.Role)
	return err
}

func addLog(ctx context.Context, tx pgx.Tx, tenantID, caseID, actor, kind, text string, at time.Time) error {
	if n := []rune(text); len(n) > 1000 {
		text = string(n[:1000])
	}
	_, err := tx.Exec(ctx, `insert into service_case_log (tenant_id, id, case_id, at, actor, kind, text) values ($1, $2, $3, $4, $5, $6, $7)`,
		tenantID, ids.New(), caseID, at, actor, kind, text)
	return err
}
