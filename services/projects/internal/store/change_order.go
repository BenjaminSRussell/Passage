package store

import (
	"context"
	"errors"
	"fmt"
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

// ChangeOrderFilter narrows a change order list (§8.41.14 change order log).
// ProjectID is required; Type and Status are optional.
type ChangeOrderFilter struct {
	ProjectID string
	Type      domain.ChangeOrderType
	Status    domain.ChangeOrderStatus
}

// AppliedChangeOrder is the result of applying a change order: the APPLIED
// change order, its effect and the BASELINE plan version it created.
type AppliedChangeOrder struct {
	ChangeOrder domain.ChangeOrder
	Effect      domain.ApplyResult
	Baseline    domain.PlanVersion
	Project     domain.Project
}

// coKeyConstraint is the default name of UNIQUE (tenant_id, idempotency_key).
const coKeyConstraint = "project_change_order_tenant_id_idempotency_key_key"

// coColumns reads a change order with its project currency and line totals.
const coColumns = `c.id::text, c.project_id, c.number, c.co_type, c.title, c.description, c.status, c.customer_approval_ref,
	c.vendor_party_id::text, c.created_by, c.decided_by, c.applied_by, c.applied_baseline_version_id::text, c.applied_at,
	c.version, c.created_at, c.updated_at, p.currency,
	coalesce((select sum(l.cost_impact) from project_change_order_line l where l.tenant_id = c.tenant_id and l.change_order_id = c.id), 0)::text,
	coalesce((select sum(l.revenue_impact) from project_change_order_line l where l.tenant_id = c.tenant_id and l.change_order_id = c.id), 0)::text
	from project_change_order c join project p on p.tenant_id = c.tenant_id and p.id = c.project_id`

func scanCO(row pgx.Row) (domain.ChangeOrder, string, error) {
	var co domain.ChangeOrder
	var ctype, status, currency, cost, revenue string
	err := row.Scan(&co.ID, &co.ProjectID, &co.Number, &ctype, &co.Title, &co.Description, &status, &co.CustomerApprovalRef,
		&co.VendorPartyID, &co.CreatedBy, &co.DecidedBy, &co.AppliedBy, &co.AppliedBaselineVersionID, &co.AppliedAt,
		&co.Version, &co.CreatedAt, &co.UpdatedAt, &currency, &cost, &revenue)
	if err != nil {
		return co, "", err
	}
	co.Type = domain.ChangeOrderType(ctype)
	co.Status = domain.ChangeOrderStatus(status)
	co.Lines = []domain.ChangeOrderLine{}
	if co.CostImpact, err = amount(cost, currency); err != nil {
		return co, "", err
	}
	co.RevenueImpact, err = amount(revenue, currency)
	return co, currency, err
}

// readCO reads one change order with its lines (locking the header row when
// forUpdate). A change order of a project outside the caller's scope is 404.
func readCO(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (domain.ChangeOrder, error) {
	q := `select ` + coColumns + ` where c.tenant_id = $1 and c.id = $2::uuid`
	if forUpdate {
		q += ` for update of c`
	}
	co, currency, err := scanCO(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return co, notFound("change order")
	}
	if err != nil {
		return co, err
	}
	return withLines(ctx, tx, tenantID, co, currency)
}

// withLines loads the impact lines of a change order in line order.
func withLines(ctx context.Context, tx pgx.Tx, tenantID string, co domain.ChangeOrder, currency string) (domain.ChangeOrder, error) {
	rows, err := tx.Query(ctx, `select line_no, wbs_node_id::text, wbs_code, cost_impact::text, revenue_impact::text,
		effort_hours_delta::text, schedule_days_delta from project_change_order_line
		where tenant_id = $1 and change_order_id = $2::uuid order by line_no`, tenantID, co.ID)
	if err != nil {
		return co, err
	}
	co.Lines, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ChangeOrderLine, error) {
		var l domain.ChangeOrderLine
		if err := r.Scan(&l.LineNo, &l.WBSNodeID, &l.WBSCode, &l.CostImpact, &l.RevenueImpact, &l.EffortHoursDelta, &l.ScheduleDaysDelta); err != nil {
			return l, err
		}
		var err error
		if l.CostImpact, err = amount(l.CostImpact, currency); err != nil {
			return l, err
		}
		l.RevenueImpact, err = amount(l.RevenueImpact, currency)
		return l, err
	})
	return co, err
}

// SameChangeOrder reports whether a replayed Idempotency-Key carries the same
// request as the stored change order (lines rounded to the currency on both sides).
func SameChangeOrder(stored, in domain.ChangeOrder) bool {
	if stored.ProjectID != in.ProjectID || stored.Type != in.Type || stored.Title != in.Title || stored.Description != in.Description ||
		!eqPtr(stored.CustomerApprovalRef, in.CustomerApprovalRef) || !eqPtr(stored.VendorPartyID, in.VendorPartyID) ||
		len(stored.Lines) != len(in.Lines) {
		return false
	}
	for i, a := range stored.Lines {
		b := in.Lines[i]
		if !eqPtr(a.WBSNodeID, b.WBSNodeID) || a.CostImpact != b.CostImpact || a.RevenueImpact != b.RevenueImpact ||
			a.EffortHoursDelta != b.EffortHoursDelta || a.ScheduleDaysDelta != b.ScheduleDaysDelta {
			return false
		}
	}
	return true
}

// CreateChangeOrder records a DRAFT change order with its impact lines. The
// project row is locked, so the number (CO-0001, CO-0002, ...) is gap-free per
// project and a concurrent request with the same key waits and then replays.
// Amounts are rounded half-even to the project currency (BR-41.5). A line's
// wbsNodeId must name a node of one of the project's plan versions; its WBS
// code is stored with it. A replayed Idempotency-Key returns the stored change
// order with created=false; a different body under the key is idempotency_key_reuse.
func (s *Postgres) CreateChangeOrder(ctx context.Context, in domain.ChangeOrder, idempotencyKey string) (domain.ChangeOrder, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ChangeOrder
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var currency string
		err := tx.QueryRow(ctx, `select currency from project where tenant_id = $1 and id = $2 for update`, p.TenantID, in.ProjectID).Scan(&currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("project")
		}
		if err != nil {
			return err
		}
		if in.Lines, err = domain.RoundLines(in.Lines, currency); err != nil {
			return err
		}
		stored, cur, err := scanCO(tx.QueryRow(ctx, `select `+coColumns+` where c.tenant_id = $1 and c.idempotency_key = $2`, p.TenantID, idempotencyKey))
		switch {
		case err == nil:
			if stored, err = withLines(ctx, tx, p.TenantID, stored, cur); err != nil {
				return err
			}
			if !SameChangeOrder(stored, in) {
				return idempotencyReuse()
			}
			out = stored
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		bad := map[string]string{}
		for i, l := range in.Lines {
			if l.WBSNodeID == nil {
				continue
			}
			var code string
			err := tx.QueryRow(ctx, `select n.wbs_code from wbs_node n join plan_version v on v.tenant_id = n.tenant_id and v.id = n.plan_version_id
				where n.tenant_id = $1 and n.id = $2::uuid and v.project_id = $3`, p.TenantID, *l.WBSNodeID, in.ProjectID).Scan(&code)
			if errors.Is(err, pgx.ErrNoRows) {
				bad[fmt.Sprintf("lines[%d].wbsNodeId", i)] = "is not a WBS node of this project"
				continue
			}
			if err != nil {
				return err
			}
			in.Lines[i].WBSCode = &code
		}
		if len(bad) > 0 {
			prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "an impact line names an unknown WBS node")
			prob.Errors = bad
			return prob
		}
		var id string
		err = tx.QueryRow(ctx, `insert into project_change_order (tenant_id, id, project_id, number, co_type, title, description,
				customer_approval_ref, vendor_party_id, created_by, idempotency_key)
			values ($1, $2, $3,
				'CO-' || lpad((select count(*) + 1 from project_change_order where tenant_id = $1 and project_id = $3)::text, 4, '0'),
				$4, $5, $6, $7, $8::text::uuid, $9, $10)
			returning id::text`,
			p.TenantID, ids.New(), in.ProjectID, string(in.Type), in.Title, in.Description,
			in.CustomerApprovalRef, in.VendorPartyID, p.UserID, idempotencyKey).Scan(&id)
		if err != nil {
			return err
		}
		b := &pgx.Batch{}
		for _, l := range in.Lines {
			b.Queue(`insert into project_change_order_line (tenant_id, change_order_id, line_no, wbs_node_id, wbs_code,
				cost_impact, revenue_impact, effort_hours_delta, schedule_days_delta)
				values ($1, $2::uuid, $3, $4::text::uuid, $5, $6::text::numeric, $7::text::numeric, $8::text::numeric, $9)`,
				p.TenantID, id, l.LineNo, l.WBSNodeID, l.WBSCode, l.CostImpact, l.RevenueImpact, l.EffortHoursDelta, l.ScheduleDaysDelta)
		}
		if err := runBatch(ctx, tx, b); err != nil {
			return err
		}
		created = true
		out, err = readCO(ctx, tx, p.TenantID, id, false)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == coKeyConstraint {
		// A request for another project took the key concurrently.
		return domain.ChangeOrder{}, false, idempotencyReuse()
	}
	return out, created, err
}

// GetChangeOrder returns one change order with its lines.
func (s *Postgres) GetChangeOrder(ctx context.Context, id string) (domain.ChangeOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ChangeOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = readCO(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ListChangeOrders returns the change orders of one project in number order,
// with their totals but without lines (GetChangeOrder returns them). A project
// that is not visible is 404.
func (s *Postgres) ListChangeOrders(ctx context.Context, f ChangeOrderFilter) ([]domain.ChangeOrder, error) {
	p, _ := tenant.FromContext(ctx)
	items := []domain.ChangeOrder{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, f.ProjectID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+coColumns+` where c.tenant_id = $1 and c.project_id = $2
			and ($3::text = '' or c.co_type = $3) and ($4::text = '' or c.status = $4) order by c.number`,
			p.TenantID, f.ProjectID, string(f.Type), string(f.Status))
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ChangeOrder, error) {
			co, _, err := scanCO(r)
			return co, err
		})
		return err
	})
	return items, err
}

// DecideChangeOrder submits, approves or rejects a change order when expected
// matches its version (412 otherwise). The creator cannot approve or reject
// (PROJ-CO-004); approving a customer claim needs the approval reference
// (PROJ-CO-002), which approvalRef may supply with a submit or approve.
func (s *Postgres) DecideChangeOrder(ctx context.Context, id string, expected int, to domain.ChangeOrderStatus, approvalRef *string) (domain.ChangeOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ChangeOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := readCO(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the change order changed since it was read; re-read and retry")
		}
		if approvalRef != nil && to != domain.COStatusRejected {
			cur.CustomerApprovalRef = approvalRef
		}
		if err := domain.CheckDecision(cur, to, p.UserID, domain.DefaultCustomerApprovalRequired); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update project_change_order set status = $3, customer_approval_ref = $4,
			decided_by = case when $3 in ('APPROVED', 'REJECTED') then $5 else decided_by end,
			version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, id, string(to), cur.CustomerApprovalRef, p.UserID); err != nil {
			return err
		}
		out, err = readCO(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ApplyChangeOrder applies an APPROVED change order atomically (BR-41.13), in
// one transaction holding the change order and project row locks:
//   - the project's contract value grows by Σ revenue impact (its version bumps);
//   - a new BASELINE plan version is created from the current baseline with each
//     line's effort and duration delta on the node of its WBS code, rescheduled
//     on the standard calendar; the old baseline is kept and marked superseded;
//   - the change order becomes APPLIED and immutable (PROJ-CO-001 on a second
//     apply, whatever version the caller sends);
//   - changeorder.applied (with the budget delta for wave-38 budget control)
//     and project.updated go to the outbox.
//
// A project without a current baseline is PROJ-CO-005; a line that cannot be
// applied is PROJ-CO-006. The applier must not be the creator (PROJ-CO-004).
func (s *Postgres) ApplyChangeOrder(ctx context.Context, id string, expected int) (AppliedChangeOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out AppliedChangeOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := readCO(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.Status == domain.COStatusApplied {
			return domain.CheckChangeOrderMove(cur, domain.COStatusApplied)
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the change order changed since it was read; re-read and retry")
		}
		if err := domain.CheckDecision(cur, domain.COStatusApplied, p.UserID, domain.DefaultCustomerApprovalRequired); err != nil {
			return err
		}
		var contract, currency, start string
		if err := tx.QueryRow(ctx, `select contract_value::text, currency, start_date::text from project
			where tenant_id = $1 and id = $2 for update`, p.TenantID, cur.ProjectID).Scan(&contract, &currency, &start); err != nil {
			return err
		}
		var baselineID string
		err = tx.QueryRow(ctx, `select id::text from plan_version where tenant_id = $1 and project_id = $2
			and kind = 'BASELINE' and superseded_by_version_id is null`, p.TenantID, cur.ProjectID).Scan(&baselineID)
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeNoBaseline, Detail: "Project " + cur.ProjectID + " has no BASELINE plan version; save the working plan as a baseline before applying a change order"}
		}
		if err != nil {
			return err
		}
		base, err := loadWBS(ctx, tx, p.TenantID, cur.ProjectID, baselineID, true)
		if err != nil {
			return err
		}
		effect, err := domain.ApplyChangeOrder(cur, contract, currency, base.Nodes)
		if err != nil {
			return err
		}
		nodes, links := copyWBS(effect.Nodes, base.Links)
		if len(nodes) > 0 {
			if _, err := domain.ScheduleWBS(domain.StandardCalendar(), start, nodes, links); err != nil {
				return err
			}
		}
		newID := ids.New()
		// The superseded-by foreign key is deferred, so the old baseline can step
		// aside before the new one exists (one current baseline per project).
		if _, err := tx.Exec(ctx, `update plan_version set superseded_by_version_id = $3::uuid, updated_at = now()
			where tenant_id = $1 and id = $2::uuid`, p.TenantID, baselineID, newID); err != nil {
			return err
		}
		out.Baseline, err = scanPlan(tx.QueryRow(ctx, `insert into plan_version (tenant_id, id, project_id, kind, sequence, copied_from_version_id, locked)
			values ($1, $2, $3, 'BASELINE', (select coalesce(max(sequence), 0) + 1 from plan_version where tenant_id = $1 and project_id = $3), $4::uuid, true)
			returning `+planColumns, p.TenantID, newID, cur.ProjectID, baselineID))
		if err != nil {
			return err
		}
		for i := range nodes {
			nodes[i].PlanVersionID = newID
		}
		if err := insertWBS(ctx, tx, p.TenantID, newID, nodes, links); err != nil {
			return err
		}
		out.Project, err = scanProject(tx.QueryRow(ctx, `update project set contract_value = $3::text::numeric, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+projectColumns, p.TenantID, cur.ProjectID, effect.ContractValue))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update project_change_order set status = 'APPLIED', applied_at = now(), applied_by = $3,
			applied_baseline_version_id = $4::uuid, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid`, p.TenantID, id, p.UserID, newID); err != nil {
			return err
		}
		if out.ChangeOrder, err = readCO(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		effect.Nodes = nil
		out.Effect = effect
		data := projevents.ChangeOrderAppliedData{
			ChangeOrderID: id, ChangeOrderNumber: cur.Number, ProjectID: cur.ProjectID, Type: string(cur.Type), Currency: currency,
			ContractValueDelta: effect.ContractValueDelta, BudgetDelta: effect.BudgetDelta, ContractValue: effect.ContractValue,
			NewBaselineVersionID: newID, VendorPartyID: deref(cur.VendorPartyID),
		}
		if err := enqueueFor(ctx, tx, p, "change_order", id, projevents.ChangeOrderApplied, data); err != nil {
			return err
		}
		return enqueue(ctx, tx, p, cur.ProjectID, projevents.ProjectUpdated, projevents.ProjectUpdatedData{
			ID: cur.ProjectID, ChangedFields: []string{"contractValue"}, Stage: string(out.Project.Stage), Version: out.Project.Version,
		})
	})
	return out, err
}
