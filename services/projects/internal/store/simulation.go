package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// Transfer is a simulation, the project's WORKING plan and the three-way diff
// between them (BR-41.14): a preview before the transfer, the outcome after it.
type Transfer struct {
	Simulation domain.PlanVersion
	Working    domain.PlanVersion
	Plan       domain.TransferPlan
}

// transferState loads a SIMULATION, its recorded common ancestor and the
// project's WORKING plan, and diffs them. lock takes the project and both plan
// rows FOR UPDATE. A version that is not a SIMULATION, or one copied before the
// ancestor was recorded, is PROJ-PLAN-001; a transferred (locked) simulation is
// PROJ-PLAN-002; a project without a WORKING plan is PROJ-PLAN-001.
func transferState(ctx context.Context, tx pgx.Tx, tenantID, projectID, simID string, lock bool, resolutions map[string]string) (Transfer, error) {
	if _, err := lockProject(ctx, tx, tenantID, projectID, lock); err != nil {
		return Transfer{}, err
	}
	sim, err := loadWBS(ctx, tx, tenantID, projectID, simID, lock)
	if err != nil {
		return Transfer{}, err
	}
	if sim.Plan.Kind != domain.KindSimulation {
		return Transfer{}, &domain.Error{Code: domain.CodePlanKind, Detail: "only a SIMULATION plan version can be transferred into WORKING"}
	}
	if sim.Plan.Locked {
		return Transfer{}, planLocked(sim.Plan)
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `select base_fingerprints from plan_version where tenant_id = $1 and id = $2::uuid`, tenantID, simID).Scan(&raw); err != nil {
		return Transfer{}, err
	}
	if raw == nil {
		return Transfer{}, &domain.Error{Code: domain.CodePlanKind,
			Detail: "this simulation was copied before transfers recorded a common ancestor; copy a new simulation to transfer"}
	}
	base := map[string]string{}
	if err := json.Unmarshal(raw, &base); err != nil {
		return Transfer{}, err
	}
	q := `select id::text from plan_version where tenant_id = $1 and project_id = $2 and kind = 'WORKING'`
	if lock {
		q += ` for update`
	}
	var workingID string
	err = tx.QueryRow(ctx, q, tenantID, projectID).Scan(&workingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, &domain.Error{Code: domain.CodePlanKind, Detail: "the project has no WORKING plan version to transfer into"}
	}
	if err != nil {
		return Transfer{}, err
	}
	working, err := loadWBS(ctx, tx, tenantID, projectID, workingID, false)
	if err != nil {
		return Transfer{}, err
	}
	plan := domain.DiffSimulation(base, domain.ContentByCode(working.Nodes, working.Links), domain.ContentByCode(sim.Nodes, sim.Links), resolutions)
	return Transfer{Simulation: sim.Plan, Working: working.Plan, Plan: plan}, nil
}

// PreviewTransfer returns the three-way diff of a SIMULATION against the
// WORKING plan without changing anything (FR-41.17 "transferred with a diff").
func (s *Postgres) PreviewTransfer(ctx context.Context, projectID, simID string) (Transfer, error) {
	p, _ := tenant.FromContext(ctx)
	var out Transfer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = transferState(ctx, tx, p.TenantID, projectID, simID, false, nil)
		return err
	})
	return out, err
}

// TransferSimulation applies a SIMULATION to the WORKING plan (BR-41.14). The
// project, the simulation and the WORKING plan are locked; expected is the
// simulation's If-Match version and workingVersion, when not 0, the WORKING
// version the caller resolved against (412 when either is stale). Unresolved
// conflicts, or a merge that is not a valid WBS, are PROJ-SIM-001 and nothing
// changes. Otherwise the WORKING plan's nodes and dependencies are replaced by
// the merge (scheduled dates are cleared, as on a WBS replacement), its version
// is bumped, and the simulation is locked with transferred_at set. A replay
// finds the simulation locked (PROJ-PLAN-002). No event is published (§8.41.5
// lists none for a transfer).
func (s *Postgres) TransferSimulation(ctx context.Context, projectID, simID string, expected, workingVersion int,
	resolutions map[string]string) (Transfer, error) {
	p, _ := tenant.FromContext(ctx)
	var out Transfer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := transferState(ctx, tx, p.TenantID, projectID, simID, true, resolutions)
		if err != nil {
			return err
		}
		if t.Simulation.Version != expected {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the simulation changed since it was read; re-read and retry")
		}
		if workingVersion != 0 && t.Working.Version != workingVersion {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the WORKING plan changed since the diff was read; review the diff again")
		}
		if err := domain.CheckTransfer(simID, t.Plan); err != nil {
			return err
		}
		workingID := t.Working.ID
		if _, err := tx.Exec(ctx, `delete from wbs_dependency where tenant_id = $1 and plan_version_id = $2::uuid`, p.TenantID, workingID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from wbs_node where tenant_id = $1 and plan_version_id = $2::uuid`, p.TenantID, workingID); err != nil {
			return err
		}
		nodes := append([]domain.WBSNodeInput(nil), t.Plan.Nodes...)
		domain.NormalizeWBS(nodes)
		newNodes, newLinks := fromInput(workingID, nodes, t.Plan.Dependencies)
		if err := insertWBS(ctx, tx, p.TenantID, workingID, newNodes, newLinks); err != nil {
			return err
		}
		if t.Working, err = scanPlan(tx.QueryRow(ctx, `update plan_version set version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2::uuid returning `+planColumns, p.TenantID, workingID)); err != nil {
			return err
		}
		if t.Simulation, err = scanPlan(tx.QueryRow(ctx, `update plan_version set locked = true, transferred_at = now(),
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2::uuid returning `+planColumns, p.TenantID, simID)); err != nil {
			return err
		}
		out = t
		return nil
	})
	return out, err
}
