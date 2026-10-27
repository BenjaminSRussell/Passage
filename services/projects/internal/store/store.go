// Package store persists projects, project groups, posting profiles, plan
// versions and WBS nodes, change orders and the risk register in Postgres. Every call runs in a transaction
// scoped to the caller's tenant (RLS), and every state change writes its event
// to the outbox in that same transaction.
package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	projevents "github.com/BenjaminSRussell/passage/services/projects/internal/events"
)

// MaxPageSize caps list pages.
const MaxPageSize = 100

// Problem codes raised by the store (see the service README).
const (
	CodeNotFound         = "PROJ-NOT-FOUND"
	CodeDuplicate        = "PROJ-CREATE-001"
	CodeParentUnknown    = "PROJ-PARENT-001"
	CodeVersionConflict  = "conflict_version"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	CodeValidation       = "COM-0001"
	CodeGroupUnknown     = "PROJ-GROUP-001"
	CodePrecondition     = "COM-0428"

	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
	projectGroupFK      = "project_project_group_fk"
	eventSource         = "projects"
)

// Filter narrows a project list.
type Filter struct {
	Stage       string
	ProjectType string
}

// Page is one page of projects; NextID is set when more remain.
type Page struct {
	Items  []domain.Project
	NextID string
}

// Store is the persistence contract the API depends on.
type Store interface {
	CreateProject(ctx context.Context, in domain.Project, idempotencyKey string) (domain.Project, bool, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
	ListProjects(ctx context.Context, f Filter, afterID string, limit int) (Page, error)
	TransitionProject(ctx context.Context, id string, version int, to domain.Stage) (domain.Project, error)

	// Setup (FR-41.1, FR-41.5). expected is the If-Match version, 0 when absent;
	// created reports an insert.
	UpsertProjectGroup(ctx context.Context, g domain.ProjectGroup, expected int) (out domain.ProjectGroup, created bool, err error)
	ListProjectGroups(ctx context.Context) ([]domain.ProjectGroup, error)
	UpsertPostingProfile(ctx context.Context, pr domain.PostingProfile, expected int) (out domain.PostingProfile, created bool, err error)
	ListPostingProfiles(ctx context.Context) ([]domain.PostingProfile, error)

	// Plans and WBS (FR-41.2).
	CreatePlanVersion(ctx context.Context, projectID string, in NewPlan, idempotencyKey string) (domain.PlanVersion, bool, error)
	ListPlanVersions(ctx context.Context, projectID string) ([]domain.PlanVersion, error)
	LoadWBS(ctx context.Context, projectID, planID string) (WBS, error)
	ReplaceWBS(ctx context.Context, projectID, planID string, expected int, nodes []domain.WBSNodeInput, deps []domain.WBSDependencyInput) (WBS, error)
	SchedulePlan(ctx context.Context, projectID, planID string) (ScheduleResult, error)

	// Change orders and claims (FR-41.18, BR-41.13). approvalRef, when not nil,
	// records customer approval evidence with a submit or approve.
	CreateChangeOrder(ctx context.Context, in domain.ChangeOrder, idempotencyKey string) (domain.ChangeOrder, bool, error)
	GetChangeOrder(ctx context.Context, id string) (domain.ChangeOrder, error)
	ListChangeOrders(ctx context.Context, f ChangeOrderFilter) ([]domain.ChangeOrder, error)
	DecideChangeOrder(ctx context.Context, id string, expected int, to domain.ChangeOrderStatus, approvalRef *string) (domain.ChangeOrder, error)
	ApplyChangeOrder(ctx context.Context, id string, expected int) (AppliedChangeOrder, error)

	// Risk and issue register (FR-41.20).
	CreateRisk(ctx context.Context, in domain.RiskIssue, idempotencyKey string) (domain.RiskIssue, bool, error)
	ListRisks(ctx context.Context, f RiskFilter) ([]domain.RiskIssue, error)
	UpdateRisk(ctx context.Context, expected int, in domain.RiskIssue) (domain.RiskIssue, error)

	Ping(ctx context.Context) error
}

// Postgres is the pgx-backed Store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps a pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const projectColumns = `id, legal_entity_id, parent_project_id, project_group_id, project_type, customer_party_id, contract_id,
	currency, contract_value::text, stage, start_date::text, end_date::text, version, created_at, updated_at`

func scanProject(row pgx.Row) (domain.Project, error) {
	var p domain.Project
	var ptype, stage, contract string
	err := row.Scan(&p.ID, &p.LegalEntityID, &p.ParentProjectID, &p.ProjectGroupID, &ptype, &p.CustomerPartyID, &p.ContractID,
		&p.Currency, &contract, &stage, &p.StartDate, &p.EndDate, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	p.ProjectType = domain.ProjectType(ptype)
	p.Stage = domain.Stage(stage)
	if err == nil {
		p.ContractValue, err = amount(contract, p.Currency)
	}
	return p, err
}

// SameRequest reports whether a replayed Idempotency-Key carries the same
// request as the stored project (after FR-41.1 inheritance on both sides and
// with the contract value rounded to the currency).
func SameRequest(stored, in domain.Project) bool {
	return stored.ID == in.ID && stored.LegalEntityID == in.LegalEntityID && eqPtr(stored.ParentProjectID, in.ParentProjectID) &&
		stored.ProjectGroupID == in.ProjectGroupID && stored.ProjectType == in.ProjectType && eqPtr(stored.CustomerPartyID, in.CustomerPartyID) &&
		eqPtr(stored.ContractID, in.ContractID) && stored.Currency == in.Currency && stored.ContractValue == in.ContractValue &&
		stored.StartDate == in.StartDate && eqPtr(stored.EndDate, in.EndDate)
}

// amount formats a stored numeric as a decimal string in the currency's minor unit.
func amount(stored, currency string) (string, error) {
	d, err := decimal.NewFromString(stored)
	if err != nil {
		return "", err
	}
	return d.StringFixed(money.MinorUnits(currency)), nil
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func idempotencyReuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

func enqueue(ctx context.Context, tx pgx.Tx, p tenant.Principal, aggregateID, eventType string, data any) error {
	return enqueueFor(ctx, tx, p, "project", aggregateID, eventType, data)
}

// enqueueFor writes one CloudEvent for an aggregate of the given type to the outbox.
func enqueueFor(ctx context.Context, tx pgx.Tx, p tenant.Principal, aggregateType, aggregateID, eventType string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, p.TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = p.UserID
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// applyParent loads the parent through RLS (so a parent outside the caller's
// legal-entity scope counts as unknown) and applies FR-41.1 inheritance; the
// inherited project must then be complete.
func applyParent(ctx context.Context, tx pgx.Tx, tenantID string, in domain.Project) (domain.Project, error) {
	if in.ParentProjectID == nil {
		return in, nil
	}
	parent, err := scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from project where tenant_id = $1 and id = $2`, tenantID, *in.ParentProjectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return in, parentUnknown()
	}
	if err != nil {
		return in, err
	}
	if in, err = domain.InheritFromParent(in, parent); err != nil {
		return in, err
	}
	if fields := domain.ValidateProject(in); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, CodeValidation, "the sub-project is incomplete after inheriting from its parent")
		p.Errors = fields
		return in, p
	}
	return in, nil
}

func duplicate() error {
	return httpx.NewProblem(http.StatusConflict, CodeDuplicate, "a project with this id already exists")
}

func parentUnknown() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeParentUnknown, "parent project does not exist")
}

// CreateProject inserts a project and its ProjectCreated event. A sub-project
// inherits group, customer and contract from its parent (FR-41.1). A replay of
// a known Idempotency-Key returns the stored project with created=false, also
// when two requests with the same key race; a different body under the same key
// is refused with idempotency_key_reuse.
func (s *Postgres) CreateProject(ctx context.Context, in domain.Project, idempotencyKey string) (domain.Project, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.Project
	created := false
	var err error
	if in.ContractValue, err = domain.RoundContractValue(in.ContractValue, in.Currency); err != nil {
		return domain.Project{}, false, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if in, err = applyParent(ctx, tx, p.TenantID, in); err != nil {
			return err
		}
		replay := func() error {
			stored, err := scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from project where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err != nil {
				return err
			}
			if !SameRequest(stored, in) {
				return idempotencyReuse()
			}
			out = stored
			return nil
		}
		if key != nil {
			if err := replay(); !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// "on conflict do nothing" with no target makes every unique index an
		// arbiter, so a concurrent request with the same key (which conflicts on
		// the key and usually the id too) waits for the other transaction and
		// inserts nothing. The key lookup then tells a replay from an id taken
		// by a different request.
		out, err = scanProject(tx.QueryRow(ctx, `insert into project (tenant_id, id, legal_entity_id, parent_project_id, project_group_id, project_type,
			customer_party_id, contract_id, currency, contract_value, start_date, end_date, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			on conflict do nothing
			returning `+projectColumns,
			p.TenantID, in.ID, in.LegalEntityID, in.ParentProjectID, in.ProjectGroupID, string(in.ProjectType),
			in.CustomerPartyID, in.ContractID, in.Currency, in.ContractValue, in.StartDate, in.EndDate, key))
		if errors.Is(err, pgx.ErrNoRows) {
			if key != nil {
				if err := replay(); !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			return duplicate()
		}
		if err != nil {
			return err
		}
		created = true
		return enqueue(ctx, tx, p, out.ID, projevents.ProjectCreated, projevents.ProjectCreatedData{
			ID: out.ID, LegalEntityID: out.LegalEntityID, ParentProjectID: out.ParentProjectID, ProjectType: string(out.ProjectType),
			Stage: string(out.Stage), Currency: out.Currency, Version: out.Version,
		})
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == projectGroupFK:
		return domain.Project{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeGroupUnknown,
			"project group "+in.ProjectGroupID+" does not exist")
	case errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation:
		return domain.Project{}, false, parentUnknown()
	case err != nil:
		return domain.Project{}, false, err
	}
	return out, created, nil
}

// GetProject returns one project of the caller's tenant.
func (s *Postgres) GetProject(ctx context.Context, id string) (domain.Project, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from project where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, httpx.NewProblem(http.StatusNotFound, CodeNotFound, "project not found")
	}
	return out, err
}

// ListProjects returns one page ordered by id, after afterID.
func (s *Postgres) ListProjects(ctx context.Context, f Filter, afterID string, limit int) (Page, error) {
	p, _ := tenant.FromContext(ctx)
	page := Page{Items: []domain.Project{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+projectColumns+` from project
			where tenant_id = $1 and id > $2 and ($3::text = '' or stage = $3) and ($4::text = '' or project_type = $4)
			order by id limit $5`, p.TenantID, afterID, f.Stage, f.ProjectType, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanProject(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, err
}

// TransitionProject moves a project to a new stage when version matches
// (optimistic concurrency). It returns conflict_version (412) on a stale version,
// PROJ-STAGE-001 on a forbidden move, and writes ProjectUpdated to the outbox.
func (s *Postgres) TransitionProject(ctx context.Context, id string, version int, to domain.Stage) (domain.Project, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stage string
		var current int
		err := tx.QueryRow(ctx, `select stage, version from project where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&stage, &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeNotFound, "project not found")
		}
		if err != nil {
			return err
		}
		if current != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the project changed since it was read; re-read and retry")
		}
		if err := domain.Transition(domain.Stage(stage), to); err != nil {
			return err
		}
		out, err = scanProject(tx.QueryRow(ctx, `update project set stage = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+projectColumns, p.TenantID, id, string(to)))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, p, out.ID, projevents.ProjectUpdated, projevents.ProjectUpdatedData{
			ID: out.ID, ChangedFields: []string{"stage"}, Stage: string(out.Stage), Version: out.Version,
		})
	})
	return out, err
}
