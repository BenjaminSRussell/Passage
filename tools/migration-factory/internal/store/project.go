package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// ProjectWIPRun is a validated project_wip extract ready to load (FR-49.12). Run carries the
// identity, source, cutoff, input hash, counts and quarantine; its status is VALIDATED. Projects
// lists the extract's projects in request order and Legacy their legacy category totals.
type ProjectWIPRun struct {
	Run       domain.PackRun
	Accepted  []domain.OpeningLine
	Issues    []domain.LineIssue
	Projects  []string
	Legacy    map[string][]domain.LegacyCategoryTotal
	StartedBy string
}

// RunProjectWIP loads the accepted opening lines that legacy_xref does not hold yet, each with
// its xref row, reconciles every project of the extract against its legacy totals and stores the
// run with its totals, quarantine and the pack.reconciled event, in one transaction. Runs of
// one (system, company) serialize on the same advisory lock as the open_items pack. Opening
// lines never post to the GL (BR-49.11): nothing here calls a posting API.
func (s *Postgres) RunProjectWIP(ctx context.Context, idem Idem, in ProjectWIPRun) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.PackRun, error) {
			return runProjectWIP(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

type openingLineJSON struct {
	LegacyID        string            `json:"legacyId"`
	ProjectID       string            `json:"projectId"`
	Category        string            `json:"category"`
	TransactionDate string            `json:"transactionDate"`
	Qty             string            `json:"qty"`
	Amount          string            `json:"amount"`
	SalesAmount     string            `json:"salesAmount"`
	Currency        string            `json:"currency"`
	WorkerID        string            `json:"workerId,omitempty"`
	ActivityID      string            `json:"activityId,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
	BillingStatus   string            `json:"billingStatus"`
	WIPCost         string            `json:"wipCost"`
	WIPSales        string            `json:"wipSales"`
}

func lineJSON(l domain.OpeningLine) openingLineJSON {
	date := ""
	if !l.TransactionDate.IsZero() {
		date = l.TransactionDate.Format("2006-01-02")
	}
	return openingLineJSON{LegacyID: l.LegacyID, ProjectID: l.ProjectID, Category: string(l.Category), TransactionDate: date,
		Qty: l.Qty.String(), Amount: l.CostAmount.String(), SalesAmount: l.SalesAmount.String(), Currency: l.Currency,
		WorkerID: l.WorkerID, ActivityID: l.ActivityID, Dimensions: l.Dimensions, BillingStatus: string(l.BillingStatus),
		WIPCost: l.WIPCost.String(), WIPSales: l.WIPSales.String()}
}

func runProjectWIP(ctx context.Context, tx pgx.Tx, in ProjectWIPRun) (domain.PackRun, error) {
	run := in.Run
	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableProjectWIP); err != nil {
		return domain.PackRun{}, fmt.Errorf("lock legacy_xref: %w", err)
	}
	idsIn := make([]string, len(in.Accepted))
	for i, l := range in.Accepted {
		idsIn[i] = l.LegacyID
	}
	present := map[string]struct{}{}
	rows, err := tx.Query(ctx, `select source_id from legacy_xref
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and source_table = $3 and source_id = any($4)`,
		sys, company, domain.TableProjectWIP, idsIn)
	if err != nil {
		return domain.PackRun{}, fmt.Errorf("read legacy_xref: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.PackRun{}, err
		}
		present[id] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.PackRun{}, err
	}
	var toLoad []domain.OpeningLine
	for _, l := range in.Accepted {
		if _, ok := present[l.LegacyID]; !ok {
			toLoad = append(toLoad, l)
		}
	}

	held, err := linesOfProjects(ctx, tx, sys, company, in.Projects)
	if err != nil {
		return domain.PackRun{}, err
	}
	held = append(held, toLoad...)
	if err := domain.ReconcileProjects(&run, len(in.Accepted), len(toLoad), in.Projects, held, in.Legacy); err != nil {
		return domain.PackRun{}, err
	}
	if err := storeNewRun(ctx, tx, &run, in.StartedBy); err != nil {
		return domain.PackRun{}, err
	}
	batch := &pgx.Batch{}
	for _, l := range toLoad {
		xrefID, lineID := ids.New(), ids.New()
		dims, err := json.Marshal(nonNilMap(l.Dimensions))
		if err != nil {
			return domain.PackRun{}, err
		}
		batch.Queue(`insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
			passage_entity, passage_id, run_id, amount, currency)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'project_opening_line', $6, $7, $8, $9)`,
			xrefID, sys, company, domain.TableProjectWIP, l.LegacyID, lineID, run.RunID, l.CostAmount.String(), l.Currency)
		batch.Queue(`insert into project_opening_line (tenant_id, line_id, run_id, legacy_xref_id, source_system, source_company, legacy_id,
			project_id, category, transaction_date, qty, cost_amount, sales_amount, currency, worker_id, activity_id, dimensions_json, billing_status,
			wip_cost, wip_sales)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
			lineID, run.RunID, xrefID, sys, company, l.LegacyID, l.ProjectID, string(l.Category), l.TransactionDate, l.Qty.String(),
			l.CostAmount.String(), l.SalesAmount.String(), l.Currency, l.WorkerID, l.ActivityID, dims, string(l.BillingStatus),
			l.WIPCost.String(), l.WIPSales.String())
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return domain.PackRun{}, fmt.Errorf("insert opening lines: %w", err)
		}
	}
	if err := insertTotals(ctx, tx, run.RunID, run.Totals, 0); err != nil {
		return domain.PackRun{}, err
	}
	run.Quarantined = nil
	for i, q := range in.Issues {
		if err := insertQuarantine(ctx, tx, run.RunID, i+1, q.Line.LegacyID, q.Rule, q.Reason, lineJSON(q.Line)); err != nil {
			return domain.PackRun{}, err
		}
		run.Quarantined = append(run.Quarantined, domain.Quarantined{Seq: i + 1, Status: domain.QuarantineOpen, Rule: q.Rule, Reason: q.Reason,
			Record: domain.Record{LegacyID: q.Line.LegacyID, Currency: q.Line.Currency, Amount: q.Line.CostAmount}})
	}
	if err := enqueueReconciled(ctx, tx, run); err != nil {
		return domain.PackRun{}, err
	}
	return run, nil
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

const lineColumns = `legacy_id, project_id, category, transaction_date, qty::text, cost_amount::text, sales_amount::text, currency,
	worker_id, activity_id, dimensions_json, billing_status, wip_cost::text, wip_sales::text`

func scanLine(row pgx.Row) (domain.OpeningLine, error) {
	var l domain.OpeningLine
	var cat, bill, qty, cost, sales, wipCost, wipSales string
	var dims []byte
	if err := row.Scan(&l.LegacyID, &l.ProjectID, &cat, &l.TransactionDate, &qty, &cost, &sales, &l.Currency, &l.WorkerID, &l.ActivityID,
		&dims, &bill, &wipCost, &wipSales); err != nil {
		return domain.OpeningLine{}, err
	}
	l.Category, l.BillingStatus = domain.Category(cat), domain.BillingStatus(bill)
	l.Qty, l.CostAmount, l.SalesAmount = decimal.RequireFromString(qty), decimal.RequireFromString(cost), decimal.RequireFromString(sales)
	l.WIPCost, l.WIPSales = decimal.RequireFromString(wipCost), decimal.RequireFromString(wipSales)
	if err := json.Unmarshal(dims, &l.Dimensions); err != nil {
		return domain.OpeningLine{}, err
	}
	return l, nil
}

// linesOfProjects returns every opening line Passage holds for the projects in (system, company).
func linesOfProjects(ctx context.Context, tx pgx.Tx, sys, company string, projects []string) ([]domain.OpeningLine, error) {
	rows, err := tx.Query(ctx, `select `+lineColumns+` from project_opening_line
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and project_id = any($3)`,
		sys, company, projects)
	if err != nil {
		return nil, fmt.Errorf("read project_opening_line: %w", err)
	}
	defer rows.Close()
	var out []domain.OpeningLine
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ProjectOpening is one project's opening lines in a project_wip run's (system, company) and the
// run's reconciliation of that project (FR-49.12, BR-49.12).
type ProjectOpening struct {
	Run       domain.PackRun
	ProjectID string
	Status    string
	Totals    []domain.ControlTotal
	Lines     []domain.OpeningLine
	LineCount int
}

// OpeningFilter pages a project's opening lines by legacy id.
type OpeningFilter struct {
	RunID     string
	ProjectID string
	After     string
	Limit     int
}

// GetProjectOpening returns the project's totals from the run and a page of its opening lines,
// ordered by legacy id. It is ErrNotFound when the run is not project_wip or does not cover the
// project.
func (s *Postgres) GetProjectOpening(ctx context.Context, f OpeningFilter) (ProjectOpening, error) {
	var out ProjectOpening
	err := s.tx(ctx, func(tx pgx.Tx) error {
		run, err := getRun(ctx, tx, f.RunID, false)
		if err != nil {
			return err
		}
		totals := domain.TotalsOfProject(run.Totals, f.ProjectID)
		if run.Pack != domain.PackProjectWIP || len(totals) == 0 {
			return fmt.Errorf("%w: run %s does not cover project %s", ErrNotFound, f.RunID, f.ProjectID)
		}
		out = ProjectOpening{Run: run, ProjectID: f.ProjectID, Totals: totals, Status: domain.ProjectStatus(totals), Lines: []domain.OpeningLine{}}
		sys, company := string(run.SourceSystem), run.SourceCompany
		if err := tx.QueryRow(ctx, `select count(*) from project_opening_line
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and project_id = $3`,
			sys, company, f.ProjectID).Scan(&out.LineCount); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+lineColumns+` from project_opening_line
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2 and project_id = $3
			  and legacy_id > $4
			order by legacy_id limit $5`, sys, company, f.ProjectID, f.After, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLine(rows)
			if err != nil {
				return err
			}
			out.Lines = append(out.Lines, l)
		}
		return rows.Err()
	})
	return out, err
}
