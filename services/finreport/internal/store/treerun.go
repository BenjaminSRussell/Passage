package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

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
