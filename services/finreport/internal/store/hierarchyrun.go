package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// runDefinitionTx returns the definition a stored run was evaluated with: the
// expanded definition of a hierarchy run, else the exact published version
// (retired versions still resolve, BR-15.8).
func runDefinitionTx(ctx context.Context, tx pgx.Tx, run Run) (domain.Definition, error) {
	var def domain.Definition
	var body []byte
	if run.Hierarchy != nil {
		if err := tx.QueryRow(ctx, `select run_definition from statement_run where id=$1::uuid`, run.ID).Scan(&body); err != nil {
			return def, fmt.Errorf("expanded definition of run %s: %w", run.ID, err)
		}
	} else if err := tx.QueryRow(ctx, `select body from report_definition where code=$1 and version=$2`,
		run.DefinitionCode, run.DefinitionVersion).Scan(&body); err != nil {
		return def, fmt.Errorf("definition %s v%d of run %s: %w", run.DefinitionCode, run.DefinitionVersion, run.ID, err)
	}
	return def, json.Unmarshal(body, &def)
}

// RunDefinition returns the definition a run was evaluated with, so a client
// can label the rows of a hierarchy run (one row per hierarchy node).
func (s *Postgres) RunDefinition(ctx context.Context, runID string) (domain.Definition, error) {
	var def domain.Definition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := getRunTx(ctx, tx, runID)
		if err != nil {
			return err
		}
		def, err = runDefinitionTx(ctx, tx, run)
		return err
	})
	return def, err
}

// expandRunHierarchyTx replaces the rows of def by the nodes of the published
// hierarchy named in spec, over the members that have balances in the loaded
// read model, and returns the hierarchy version used and the run warnings.
func expandRunHierarchyTx(ctx context.Context, tx pgx.Tx, def domain.Definition, spec RunSpec, balances []domain.Balance) (domain.Definition, *RunHierarchy, []domain.Check, error) {
	want := spec.Hierarchy
	var body, raw []byte
	var err error
	if want.Version > 0 {
		err = tx.QueryRow(ctx, `select body, membership from report_hierarchy where code=$1 and status='published' and version=$2`, want.Code, want.Version).Scan(&body, &raw)
	} else {
		err = tx.QueryRow(ctx, `select body, membership from report_hierarchy where code=$1 and status='published' order by version desc limit 1`, want.Code).Scan(&body, &raw)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return def, nil, nil, problem(422, CodeHierarchyNotFound, fmt.Sprintf("hierarchy %s has no %s", want.Code, versionLabel(want.Version)))
	}
	if err != nil {
		return def, nil, nil, err
	}
	var h domain.Hierarchy
	var stored domain.Membership
	if err := json.Unmarshal(body, &h); err != nil {
		return def, nil, nil, err
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return def, nil, nil, err
	}
	opts := domain.HierarchyRunOptions{NormalBalance: want.NormalBalance, AccountFrom: want.AccountFrom, AccountTo: want.AccountTo}
	seen := observedMembers(h, balances, opts)
	rows, err := domain.ExpandHierarchy(def, h, stored, seen, opts)
	if err != nil {
		return def, nil, nil, asProblem(err)
	}
	var checks []domain.Check
	if c := domain.HierarchyRunCheck(h, h.Version, rows.Unassigned, seen.HasBlank); c != nil {
		checks = append(checks, *c)
	}
	return rows.Definition, &RunHierarchy{Code: h.Code, Version: h.Version}, checks, nil
}

// observedMembers lists the accounts, or the values of the dimension attribute,
// that the loaded balances hold. A dimension hierarchy with an account range
// looks only at balances inside the range.
func observedMembers(h domain.Hierarchy, balances []domain.Balance, opts domain.HierarchyRunOptions) domain.HierarchyObserved {
	set := map[string]bool{}
	out := domain.HierarchyObserved{}
	for _, b := range balances {
		member := b.Account
		if h.Subject == domain.SubjectDimension {
			if opts.AccountFrom != "" && (b.Account < opts.AccountFrom || b.Account > opts.AccountTo) {
				continue
			}
			if member = b.Dims[h.DimensionAttribute]; member == "" {
				out.HasBlank = true
				continue
			}
		}
		if !set[member] {
			set[member] = true
			out.Members = append(out.Members, member)
		}
	}
	return out
}

// hierarchyRef names the hierarchy version of a run as CODE@version, in the
// statement.generated parameters and the audit entry.
func hierarchyRef(h *RunHierarchy) string {
	return fmt.Sprintf("%s@%d", h.Code, h.Version)
}

func runParameters(period string, h *RunHierarchy) map[string]string {
	p := map[string]string{"period": period}
	if h != nil {
		p["hierarchy"] = hierarchyRef(h)
	}
	return p
}
