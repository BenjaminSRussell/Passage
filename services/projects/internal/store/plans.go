package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	projevents "github.com/BenjaminSRussell/passage/services/projects/internal/events"
)

// NewPlan is the request to add a plan version to a project. CopyFromVersionID
// names the version whose WBS is copied ("" for an empty WORKING plan).
type NewPlan struct {
	Kind              domain.PlanKind
	CopyFromVersionID string
}

// WBS is one plan version with its nodes (in WBS-code order) and dependencies.
type WBS struct {
	Plan  domain.PlanVersion
	Nodes []domain.WBSNode
	Links []domain.WBSLink
}

// ScheduleResult is the WBS after a scheduler run, with the project finish date
// (YYYY-MM-DD, empty for an empty plan) and the critical node ids in dependency order.
type ScheduleResult struct {
	WBS
	FinishDate   string
	CriticalPath []string
}

// planKeyConstraint is the default name of UNIQUE (tenant_id, idempotency_key) on plan_version.
const planKeyConstraint = "plan_version_tenant_id_idempotency_key_key"

const planColumns = `id::text, project_id, kind, sequence, copied_from_version_id::text, locked, version, created_at, updated_at`

func scanPlan(row pgx.Row) (domain.PlanVersion, error) {
	var pv domain.PlanVersion
	var kind string
	err := row.Scan(&pv.ID, &pv.ProjectID, &kind, &pv.Sequence, &pv.CopiedFromVersionID, &pv.Locked, &pv.Version, &pv.CreatedAt, &pv.UpdatedAt)
	pv.Kind = domain.PlanKind(kind)
	return pv, err
}

func notFound(what string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeNotFound, what+" not found")
}

// lockProject locks the project row (serializing plan-version creation per
// project) and returns its start date, or 404 when it is not visible.
func lockProject(ctx context.Context, tx pgx.Tx, tenantID, projectID string, forUpdate bool) (string, error) {
	q := `select start_date::text from project where tenant_id = $1 and id = $2`
	if forUpdate {
		q += ` for update`
	}
	var start string
	err := tx.QueryRow(ctx, q, tenantID, projectID).Scan(&start)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound("project")
	}
	return start, err
}

// CreatePlanVersion adds a plan version to a project (FR-41.2, FR-41.17). The
// project row is locked, so sequence numbers and the one-WORKING / one-BASELINE
// rule hold under concurrency. A copy takes the source's nodes (with their
// scheduled dates) and dependencies under fresh ids. A replayed Idempotency-Key
// returns the stored version with created=false; a different body under the
// same key is idempotency_key_reuse.
func (s *Postgres) CreatePlanVersion(ctx context.Context, projectID string, in NewPlan, idempotencyKey string) (domain.PlanVersion, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.PlanVersion
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, projectID, true); err != nil {
			return err
		}
		replay, err := scanPlan(tx.QueryRow(ctx, `select `+planColumns+` from plan_version where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		switch {
		case err == nil:
			if replay.ProjectID != projectID || replay.Kind != in.Kind || deref(replay.CopiedFromVersionID) != in.CopyFromVersionID {
				return idempotencyReuse()
			}
			out = replay
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var source *WBS
		if in.CopyFromVersionID != "" {
			w, err := loadWBS(ctx, tx, p.TenantID, projectID, in.CopyFromVersionID, false)
			if err != nil {
				var prob *httpx.Problem
				if errors.As(err, &prob) && prob.Status == http.StatusNotFound {
					return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePlanKind, "copyFromVersionId is not a plan version of this project")
				}
				return err
			}
			source = &w
		}
		var kinds []domain.PlanKind
		rows, err := tx.Query(ctx, `select kind from plan_version where tenant_id = $1 and project_id = $2`, p.TenantID, projectID)
		if err != nil {
			return err
		}
		kinds, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.PlanKind, error) {
			var k string
			err := r.Scan(&k)
			return domain.PlanKind(k), err
		})
		if err != nil {
			return err
		}
		var sourcePlan *domain.PlanVersion
		if source != nil {
			sourcePlan = &source.Plan
		}
		locked, err := domain.CheckNewPlan(in.Kind, sourcePlan, kinds)
		if err != nil {
			return err
		}
		out, err = scanPlan(tx.QueryRow(ctx, `insert into plan_version (tenant_id, id, project_id, kind, sequence, copied_from_version_id, locked, idempotency_key)
			values ($1, $2, $3, $4, (select coalesce(max(sequence), 0) + 1 from plan_version where tenant_id = $1 and project_id = $3), $5::text::uuid, $6, $7)
			returning `+planColumns, p.TenantID, ids.New(), projectID, string(in.Kind), nullable(in.CopyFromVersionID), locked, idempotencyKey))
		if err != nil {
			return err
		}
		created = true
		if source == nil {
			return nil
		}
		nodes, links := copyWBS(source.Nodes, source.Links)
		return insertWBS(ctx, tx, p.TenantID, out.ID, nodes, links)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == planKeyConstraint {
		// A request for another project took the key concurrently.
		return domain.PlanVersion{}, false, idempotencyReuse()
	}
	return out, created, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// copyWBS gives every node a fresh id and remaps parents and dependencies.
func copyWBS(nodes []domain.WBSNode, links []domain.WBSLink) ([]domain.WBSNode, []domain.WBSLink) {
	idOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		idOf[n.ID] = ids.New()
	}
	outNodes := make([]domain.WBSNode, len(nodes))
	for i, n := range nodes {
		n.ID = idOf[n.ID]
		if n.ParentID != nil {
			pid := idOf[*n.ParentID]
			n.ParentID = &pid
		}
		outNodes[i] = n
	}
	outLinks := make([]domain.WBSLink, len(links))
	for i, l := range links {
		outLinks[i] = domain.WBSLink{PredecessorID: idOf[l.PredecessorID], SuccessorID: idOf[l.SuccessorID], Type: l.Type, LagDays: l.LagDays}
	}
	return outNodes, outLinks
}

// fromInput turns a validated WBS replacement into nodes and links with fresh ids.
func fromInput(planID string, nodes []domain.WBSNodeInput, deps []domain.WBSDependencyInput) ([]domain.WBSNode, []domain.WBSLink) {
	idOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		idOf[n.WBSCode] = ids.New()
	}
	outNodes := make([]domain.WBSNode, 0, len(nodes))
	for _, n := range nodes {
		var parent *string
		if n.ParentWBSCode != nil {
			pid := idOf[*n.ParentWBSCode]
			parent = &pid
		}
		outNodes = append(outNodes, domain.WBSNode{ID: idOf[n.WBSCode], PlanVersionID: planID, WBSCode: n.WBSCode, ParentID: parent,
			Name: n.Name, EffortHours: n.EffortHours, DurationDays: n.DurationDays, ConstraintType: n.ConstraintType,
			ConstraintDate: n.ConstraintDate, RoleID: n.RoleID, IsBillableMilestone: n.IsBillableMilestone})
	}
	outLinks := make([]domain.WBSLink, 0, len(deps))
	for _, d := range deps {
		outLinks = append(outLinks, domain.WBSLink{PredecessorID: idOf[d.PredecessorWBSCode], SuccessorID: idOf[d.SuccessorWBSCode], Type: d.Type, LagDays: d.LagDays})
	}
	return outNodes, outLinks
}

// insertWBS writes nodes and links of one plan version in a single batch. The
// parent foreign key is deferred, so node order does not matter.
func insertWBS(ctx context.Context, tx pgx.Tx, tenantID, planID string, nodes []domain.WBSNode, links []domain.WBSLink) error {
	b := &pgx.Batch{}
	for _, n := range nodes {
		b.Queue(`insert into wbs_node (tenant_id, id, plan_version_id, wbs_code, parent_id, name, effort_hours, duration_days,
			constraint_type, constraint_date, role_id, is_billable_milestone, early_start, early_finish, late_start, late_finish, slack_days, is_critical)
			values ($1, $2, $3, $4, $5::text::uuid, $6, $7::text::numeric, $8, $9, $10::text::date, $11::text::uuid, $12,
				$13::text::date, $14::text::date, $15::text::date, $16::text::date, $17, $18)`,
			tenantID, n.ID, planID, n.WBSCode, n.ParentID, n.Name, n.EffortHours, n.DurationDays, n.ConstraintType,
			n.ConstraintDate, n.RoleID, n.IsBillableMilestone, n.EarlyStart, n.EarlyFinish, n.LateStart, n.LateFinish, n.SlackDays, n.Critical)
	}
	for _, l := range links {
		b.Queue(`insert into wbs_dependency (tenant_id, plan_version_id, predecessor_id, successor_id, dep_type, lag_days)
			values ($1, $2, $3, $4, $5, $6)`, tenantID, planID, l.PredecessorID, l.SuccessorID, l.Type, l.LagDays)
	}
	return runBatch(ctx, tx, b)
}

func runBatch(ctx context.Context, tx pgx.Tx, b *pgx.Batch) error {
	if b.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, b)
	for i := 0; i < b.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

// loadWBS reads one plan version of a project (locking it when forUpdate) with
// its nodes and dependencies. A plan of another project, or one outside the
// caller's tenant or legal-entity scope, is 404.
func loadWBS(ctx context.Context, tx pgx.Tx, tenantID, projectID, planID string, forUpdate bool) (WBS, error) {
	q := `select ` + planColumns + ` from plan_version where tenant_id = $1 and project_id = $2 and id = $3::uuid`
	if forUpdate {
		q += ` for update`
	}
	pv, err := scanPlan(tx.QueryRow(ctx, q, tenantID, projectID, planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return WBS{}, notFound("plan version")
	}
	if err != nil {
		return WBS{}, err
	}
	w := WBS{Plan: pv, Nodes: []domain.WBSNode{}, Links: []domain.WBSLink{}}
	rows, err := tx.Query(ctx, `select id::text, wbs_code, parent_id::text, name, effort_hours::text, duration_days, constraint_type,
			constraint_date::text, role_id::text, is_billable_milestone, early_start::text, early_finish::text, late_start::text,
			late_finish::text, slack_days, is_critical
		from wbs_node where tenant_id = $1 and plan_version_id = $2::uuid
		order by string_to_array(wbs_code, '.')::int[]`, tenantID, planID)
	if err != nil {
		return WBS{}, err
	}
	w.Nodes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.WBSNode, error) {
		n := domain.WBSNode{PlanVersionID: planID}
		err := r.Scan(&n.ID, &n.WBSCode, &n.ParentID, &n.Name, &n.EffortHours, &n.DurationDays, &n.ConstraintType,
			&n.ConstraintDate, &n.RoleID, &n.IsBillableMilestone, &n.EarlyStart, &n.EarlyFinish, &n.LateStart, &n.LateFinish,
			&n.SlackDays, &n.Critical)
		return n, err
	})
	if err != nil {
		return WBS{}, err
	}
	rows, err = tx.Query(ctx, `select predecessor_id::text, successor_id::text, dep_type, lag_days
		from wbs_dependency where tenant_id = $1 and plan_version_id = $2::uuid order by predecessor_id, successor_id`, tenantID, planID)
	if err != nil {
		return WBS{}, err
	}
	w.Links, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.WBSLink, error) {
		var l domain.WBSLink
		err := r.Scan(&l.PredecessorID, &l.SuccessorID, &l.Type, &l.LagDays)
		return l, err
	})
	return w, err
}

func planLocked(pv domain.PlanVersion) error {
	return &domain.Error{Code: domain.CodePlanLocked, Detail: "plan version " + pv.ID + " (" + string(pv.Kind) + ") is locked and cannot change"}
}

// ReplaceWBS replaces every node and dependency of a plan version in one
// transaction and bumps its version. expected is the If-Match version (412 when
// stale); a locked plan version is PROJ-PLAN-002. The caller has normalized and
// validated the input with domain.ValidateWBS. Replacing clears scheduled dates.
func (s *Postgres) ReplaceWBS(ctx context.Context, projectID, planID string, expected int,
	nodes []domain.WBSNodeInput, deps []domain.WBSDependencyInput) (WBS, error) {
	p, _ := tenant.FromContext(ctx)
	var out WBS
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadPlanForUpdate(ctx, tx, p.TenantID, projectID, planID)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the plan version changed since it was read; re-read and retry")
		}
		if cur.Locked {
			return planLocked(cur)
		}
		if _, err := tx.Exec(ctx, `delete from wbs_dependency where tenant_id = $1 and plan_version_id = $2::uuid`, p.TenantID, planID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from wbs_node where tenant_id = $1 and plan_version_id = $2::uuid`, p.TenantID, planID); err != nil {
			return err
		}
		newNodes, newLinks := fromInput(planID, nodes, deps)
		if err := insertWBS(ctx, tx, p.TenantID, planID, newNodes, newLinks); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update plan_version set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, planID); err != nil {
			return err
		}
		out, err = loadWBS(ctx, tx, p.TenantID, projectID, planID, false)
		return err
	})
	return out, err
}

func loadPlanForUpdate(ctx context.Context, tx pgx.Tx, tenantID, projectID, planID string) (domain.PlanVersion, error) {
	pv, err := scanPlan(tx.QueryRow(ctx, `select `+planColumns+` from plan_version
		where tenant_id = $1 and project_id = $2 and id = $3::uuid for update`, tenantID, projectID, planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return pv, notFound("plan version")
	}
	return pv, err
}

// SchedulePlan runs the CPM scheduler (FR-41.2) over a plan version from the
// project start date on the standard Monday-to-Friday calendar, stores each
// node's early and late dates, slack and critical flag, and writes
// dev.passage.projects.wbs.scheduled.v1 in the same transaction. The plan row is
// locked so a concurrent WBS replacement cannot interleave. The plan version's
// version is not bumped: the dates are derived from the WBS it versions. A
// locked plan version is PROJ-PLAN-002; an empty plan schedules nothing and
// emits no event.
func (s *Postgres) SchedulePlan(ctx context.Context, projectID, planID string) (ScheduleResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ScheduleResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		start, err := lockProject(ctx, tx, p.TenantID, projectID, false)
		if err != nil {
			return err
		}
		w, err := loadWBS(ctx, tx, p.TenantID, projectID, planID, true)
		if err != nil {
			return err
		}
		if w.Plan.Locked {
			return planLocked(w.Plan)
		}
		out.WBS = w
		if len(w.Nodes) == 0 {
			return nil
		}
		sched, err := domain.ScheduleWBS(domain.StandardCalendar(), start, w.Nodes, w.Links)
		if err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, n := range w.Nodes {
			b.Queue(`update wbs_node set early_start = $3::text::date, early_finish = $4::text::date, late_start = $5::text::date,
				late_finish = $6::text::date, slack_days = $7, is_critical = $8 where tenant_id = $1 and id = $2::uuid`,
				p.TenantID, n.ID, n.EarlyStart, n.EarlyFinish, n.LateStart, n.LateFinish, n.SlackDays, n.Critical)
		}
		if err := runBatch(ctx, tx, b); err != nil {
			return err
		}
		out.FinishDate = sched.FinishDate.Format("2006-01-02")
		out.CriticalPath = append([]string{}, sched.CriticalPath...)
		return enqueueFor(ctx, tx, p, "plan_version", planID, projevents.WBSScheduled, projevents.WBSScheduledData{
			ProjectID: projectID, PlanVersionID: planID, CriticalPath: out.CriticalPath, FinishDate: out.FinishDate,
		})
	})
	return out, err
}

// ListPlanVersions returns the plan versions of a project in sequence order; a
// project that is not visible is 404.
func (s *Postgres) ListPlanVersions(ctx context.Context, projectID string) ([]domain.PlanVersion, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.PlanVersion{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, projectID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+planColumns+` from plan_version
			where tenant_id = $1 and project_id = $2 order by sequence`, p.TenantID, projectID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.PlanVersion, error) { return scanPlan(r) })
		return err
	})
	return out, err
}

// LoadWBS returns one plan version of a project with its nodes and dependencies.
func (s *Postgres) LoadWBS(ctx context.Context, projectID, planID string) (WBS, error) {
	p, _ := tenant.FromContext(ctx)
	var out WBS
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadWBS(ctx, tx, p.TenantID, projectID, planID, false)
		return err
	})
	return out, err
}
