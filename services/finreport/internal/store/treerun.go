package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// treeRef names the tree revision of a run as CODE@revision, in the run
// parameters of statement.generated.
func treeRef(t *RunTree) string { return fmt.Sprintf("%s@%d", t.Code, t.Revision) }

// limitRunToTreeTx keeps the loaded balances that belong to the unit of the
// tree (rolled up over the units below it) and returns the tree revision used
// and the warnings of the run (FR-15.4).
func limitRunToTreeTx(ctx context.Context, tx pgx.Tx, want *TreeRun, balances []domain.Balance) ([]domain.Balance, *RunTree, []domain.Check, error) {
	var body []byte
	var rev int
	err := tx.QueryRow(ctx, `select body, revision from report_tree where code=$1`, want.Code).Scan(&body, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil, problem(422, domain.CodeTreeRun, fmt.Sprintf("tree %s does not exist", want.Code))
	}
	if err != nil {
		return nil, nil, nil, err
	}
	var t domain.Tree
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, nil, nil, err
	}
	kept, scope, err := domain.LimitToUnit(t, want.Unit, balances)
	if err != nil {
		return nil, nil, nil, asProblem(err)
	}
	var checks []domain.Check
	if c := domain.TreeRunCheck(t, want.Unit, scope); c != nil {
		checks = append(checks, *c)
	}
	return kept, &RunTree{Code: t.Code, Unit: want.Unit, Revision: rev}, checks, nil
}

// keepRevisionTx stores the body of a tree revision (FR-15.4) so that a run
// made on it can show its units after the tree changes. Other parts keep no
// history.
func keepRevisionTx(ctx context.Context, tx pgx.Tx, table partTable, code string, rev int, body []byte, updatedBy string) error {
	if table != treeTable {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into report_tree_revision(tenant_id, tree_code, revision, body, updated_by) values($1,$2,$3,$4,$5)`,
		tenantID(ctx), code, rev, body, updatedBy)
	return err
}

// keepCurrentRevisionTx stores the revision a tree replace is about to
// overwrite when it is not stored yet: a tree saved before migration 0016 whose
// backfill was skipped (a migration role under row-level security sees no
// rows). It runs under the replace's row lock, so no revision is ever lost.
func keepCurrentRevisionTx(ctx context.Context, tx pgx.Tx, table partTable, code string) error {
	if table != treeTable {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into report_tree_revision(tenant_id, tree_code, revision, body, updated_by, created_at)
		select tenant_id, code, revision, body, updated_by, updated_at from report_tree where code=$1
		on conflict do nothing`, code)
	return err
}

// TreeRevision is one stored revision of a reporting tree, without its body.
type TreeRevision struct {
	Revision  int       `json:"revision"`
	UpdatedBy string    `json:"updatedBy"`
	CreatedAt time.Time `json:"createdAt"`
}

// ListTreeRevisions returns the stored revisions of a tree, newest first. A
// tree that does not exist is REPORT-0037.
func (s *Postgres) ListTreeRevisions(ctx context.Context, code string) ([]TreeRevision, error) {
	out := []TreeRevision{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from report_tree where code=$1`, code).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return problem(404, domain.CodePartNotFound, fmt.Sprintf("tree %s does not exist", code))
		} else if err != nil {
			return err
		}
		// The current revision of a tree saved before migration 0016 and not
		// backfilled is read from the tree itself until its next save stores it.
		rows, err := tx.Query(ctx, `select revision, updated_by, created_at from report_tree_revision where tree_code=$1
			union all
			select t.revision, t.updated_by, t.updated_at from report_tree t where t.code=$1
				and not exists (select 1 from report_tree_revision r where r.tree_code=t.code and r.revision=t.revision)
			order by 1 desc`, code)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r TreeRevision
			if err := rows.Scan(&r.Revision, &r.UpdatedBy, &r.CreatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetTreeRevision returns the tree as it was at one revision.
func (s *Postgres) GetTreeRevision(ctx context.Context, code string, revision int) (domain.Tree, error) {
	var t domain.Tree
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, err = treeRevisionTx(ctx, tx, code, revision)
		return err
	})
	return t, err
}

func treeRevisionTx(ctx context.Context, tx pgx.Tx, code string, revision int) (domain.Tree, error) {
	var t domain.Tree
	var body []byte
	err := tx.QueryRow(ctx, `select body from report_tree_revision where tree_code=$1 and revision=$2
		union all
		select body from report_tree where code=$1 and revision=$2
		limit 1`, code, revision).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, problem(404, domain.CodePartNotFound, fmt.Sprintf("tree %s has no stored revision %d", code, revision))
	}
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(body, &t)
}

// RunTreeView is the tree a stored run was limited to, as it was when the run
// was made, with the unit of the run and whether the tree has changed since.
type RunTreeView struct {
	Code            string      `json:"code"`
	Unit            string      `json:"unit"`
	Revision        int         `json:"revision"`
	CurrentRevision int         `json:"currentRevision"`
	Tree            domain.Tree `json:"tree"`
}

// RunTree returns the tree revision a run used, limited to the run's unit and
// the units below it: that is what the run covered, and a statement viewer does
// not hold the maintainer duty that reads whole trees. A run that was not
// limited to a tree is REPORT-0037, and so is a revision the store did not keep
// (a run made on a tree revision that was replaced before migration 0016).
func (s *Postgres) RunTree(ctx context.Context, runID string) (RunTreeView, error) {
	var v RunTreeView
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := getRunTx(ctx, tx, runID)
		if err != nil {
			return err
		}
		if run.Tree == nil {
			return problem(404, domain.CodePartNotFound, "statement run "+runID+" was not limited to a reporting tree")
		}
		t, err := treeRevisionTx(ctx, tx, run.Tree.Code, run.Tree.Revision)
		if err != nil {
			return err
		}
		v = RunTreeView{Code: run.Tree.Code, Unit: run.Tree.Unit, Revision: run.Tree.Revision, CurrentRevision: run.Tree.Revision, Tree: t.Subtree(run.Tree.Unit)}
		err = tx.QueryRow(ctx, `select revision from report_tree where code=$1`, run.Tree.Code).Scan(&v.CurrentRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return v, err
}
