package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// CountSheetFilter selects count sheet headers. Empty fields do not filter.
type CountSheetFilter struct {
	PlanID string
	Status string
	Limit  int
}

// CreateCountPlan stores a count plan (FR-16.9). A repeated key returns the
// stored plan with replayed=true, or idempotency_key_reuse when the body
// differs.
func (s *Postgres) CreateCountPlan(ctx context.Context, key string, in domain.CountPlan) (domain.CountPlan, bool, error) {
	if err := domain.ValidatePlan(in); err != nil {
		return domain.CountPlan{}, false, domainProblem(err)
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountPlan
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `insert into count_plan (tenant_id, legal_entity_id, id, name, warehouse, currency, unit, tolerance_pct,
			tolerance_value, blind, idempotency_key) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, idempotency_key) do update set name = count_plan.name
			returning id::text, (xmax = 0)`,
			tp.TenantID, in.LegalEntityID, ids.New(), strings.TrimSpace(in.Name), optionalText(in.Warehouse), in.Currency, in.Unit,
			in.TolerancePct.String(), in.ToleranceValue.String(), in.Blind, key).Scan(&id, &inserted); err != nil {
			return scopeProblem(err)
		}
		var err error
		out, err = loadCountPlan(ctx, tx, id)
		return err
	})
	if err != nil {
		return domain.CountPlan{}, false, err
	}
	if !inserted && (out.LegalEntityID != in.LegalEntityID || out.Name != strings.TrimSpace(in.Name) || out.Warehouse != in.Warehouse ||
		out.Currency != in.Currency || out.Unit != in.Unit || !out.TolerancePct.Equal(in.TolerancePct) ||
		!out.ToleranceValue.Equal(in.ToleranceValue) || out.Blind != in.Blind) {
		return domain.CountPlan{}, false, problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// ListCountPlans returns count plans, newest first (at most 200).
func (s *Postgres) ListCountPlans(ctx context.Context) ([]domain.CountPlan, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []domain.CountPlan{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+countPlanColumns+` from count_plan where tenant_id = $1 order by created_at desc, id desc limit 200`, tp.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanCountPlan(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// GenerateCountSheet creates an OPEN count sheet from a plan: one line per
// on-hand row of the plan's legal entity (and warehouse, when the plan names
// one) holding physical stock, expected at its physical quantity now
// (FR-16.9). The key makes a retry return the sheet it generated; the same
// key for another plan is idempotency_key_reuse.
func (s *Postgres) GenerateCountSheet(ctx context.Context, planID, key string) (domain.CountSheet, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "count-sheet", tp.TenantID, key); err != nil {
			return err
		}
		var existing, existingPlan string
		err := tx.QueryRow(ctx, `select id::text, plan_id::text from count_sheet where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, key).
			Scan(&existing, &existingPlan)
		if err == nil {
			if existingPlan != planID {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another count plan")
			}
			replayed = true
			out, err = loadCountSheet(ctx, tx, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		plan, err := loadCountPlan(ctx, tx, planID)
		if err != nil {
			return err
		}
		sheetID := ids.New()
		number := "CNT-" + strings.ToUpper(strings.ReplaceAll(sheetID, "-", "")[20:])
		if _, err := tx.Exec(ctx, `insert into count_sheet (tenant_id, legal_entity_id, id, plan_id, number, status, idempotency_key)
			values ($1, $2, $3, $4, $5, 'OPEN', $6)`, tp.TenantID, plan.LegalEntityID, sheetID, planID, number, key); err != nil {
			return scopeProblem(err)
		}
		if err := snapshotLines(ctx, tx, tp.TenantID, plan, sheetID); err != nil {
			return err
		}
		out, err = loadCountSheet(ctx, tx, sheetID, false)
		return err
	})
	return out, replayed, err
}

// snapshotLines writes one sheet line per on-hand row of the plan holding
// physical stock, in (product, dimension set) order.
func snapshotLines(ctx context.Context, tx pgx.Tx, tenantID string, plan domain.CountPlan, sheetID string) error {
	rows, err := tx.Query(ctx, `select o.product_id::text, o.dimension_set_hash, (o.posted_qty + o.received + o.registered - o.deducted - o.picked)::text
		from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
		where o.tenant_id = $1 and o.legal_entity_id = $2 and ($3 = '' or d.dims->>'warehouse' = $3)
		  and o.posted_qty + o.received + o.registered - o.deducted - o.picked <> 0
		order by o.product_id, o.dimension_set_hash`, tenantID, plan.LegalEntityID, plan.Warehouse)
	if err != nil {
		return err
	}
	var lineIDs, products, expected []string
	var hashes [][]byte
	for rows.Next() {
		var product, qty string
		var hash []byte
		if err := rows.Scan(&product, &hash, &qty); err != nil {
			rows.Close()
			return err
		}
		lineIDs, products, hashes, expected = append(lineIDs, ids.New()), append(products, product), append(hashes, hash), append(expected, qty)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(lineIDs) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `insert into count_sheet_line (tenant_id, id, sheet_id, line_no, product_id, dimension_set_hash, expected_qty)
		select $1, l.id, $2, l.n, l.product, l.hash, l.qty
		from unnest($3::uuid[], $4::uuid[], $5::bytea[], $6::numeric[]) with ordinality as l(id, product, hash, qty, n)`,
		tenantID, sheetID, lineIDs, products, hashes, expected)
	return err
}

// ListCountSheets returns sheet headers (no lines), newest first (at most 200).
func (s *Postgres) ListCountSheets(ctx context.Context, f CountSheetFilter) ([]domain.CountSheet, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	tp, _ := tenant.FromContext(ctx)
	out := []domain.CountSheet{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+countSheetColumns+` from count_sheet s join count_plan p on p.tenant_id = s.tenant_id and p.id = s.plan_id
			where s.tenant_id = $1 and ($2 = '' or s.plan_id::text = $2) and ($3 = '' or s.status = $3)
			order by s.created_at desc, s.id desc limit $4`, tp.TenantID, f.PlanID, f.Status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sheet, err := scanCountSheet(rows)
			if err != nil {
				return err
			}
			out = append(out, sheet)
		}
		return rows.Err()
	})
	return out, err
}

// GetCountSheet returns a sheet with its lines.
func (s *Postgres) GetCountSheet(ctx context.Context, id string) (domain.CountSheet, error) {
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadCountSheet(ctx, tx, id, false)
		return err
	})
	return out, err
}

// EnterCount records a counted quantity for one line of an OPEN sheet. The
// sheet version must match (If-Match) and moves on with each entry; a later
// entry for the same line replaces the earlier one.
func (s *Postgres) EnterCount(ctx context.Context, sheetID, lineID string, version int, counted decimal.Decimal) (domain.CountSheet, error) {
	if err := domain.ValidateCounted(counted, s.rules.MaxScale); err != nil {
		return domain.CountSheet{}, domainProblem(err)
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := loadCountSheet(ctx, tx, sheetID, true)
		if err != nil {
			return err
		}
		if err := domain.RequireOpen(sheet.Status); err != nil {
			return domainProblem(err)
		}
		if sheet.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count sheet changed since it was read; reread and retry")
		}
		tag, err := tx.Exec(ctx, `update count_sheet_line set counted_qty = $4, counted_by = $5, counted_at = now()
			where tenant_id = $1 and sheet_id = $2 and id = $3`, tp.TenantID, sheetID, lineID, counted.String(), optionalText(tp.UserID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, domain.CodeCountNotFound, "count line not found on this sheet")
		}
		if _, err := tx.Exec(ctx, `update count_sheet set version = version + 1 where tenant_id = $1 and id = $2`, tp.TenantID, sheetID); err != nil {
			return err
		}
		out, err = loadCountSheet(ctx, tx, sheetID, false)
		return err
	})
	return out, err
}

// PostCountSheet posts the counted variances of an OPEN sheet as one COUNT
// journal (FR-16.9, BR-16.8). The variance of each counted line is counted
// minus the book quantity now, read under the row lock, not the quantity
// expected at generation. A variance above the plan tolerance needs approval:
// without canApprove the post is refused with INV-CNT-001 and nothing is
// written. Uncounted lines are skipped; a sheet with no counted line is
// INV-CNT-005. The journal posts through the same path as any journal, so the
// negative policy, valuation and GL entry are the same. Posting a POSTED sheet
// returns it unchanged.
func (s *Postgres) PostCountSheet(ctx context.Context, sheetID string, version int, canApprove bool, p PostParams) (domain.CountSheet, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := loadCountSheet(ctx, tx, sheetID, true)
		if err != nil {
			return err
		}
		if sheet.Status == domain.CountPosted {
			out = sheet
			return nil
		}
		if sheet.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count sheet changed since it was read; reread and retry")
		}
		plan, err := loadCountPlan(ctx, tx, sheet.PlanID)
		if err != nil {
			return err
		}
		lines, variances, approval, err := countVariances(ctx, tx, tp.TenantID, sheet, plan)
		if err != nil {
			return err
		}
		if approval != "" && !canApprove {
			return problem(http.StatusForbidden, domain.CodeCountApproval, approval+"; supervisor approval (inventory.count.approve) is required")
		}
		var journalID *string
		if len(lines) > 0 {
			id, err := s.postCountJournal(ctx, tx, tp.TenantID, sheet, plan, lines, p)
			if err != nil {
				return err
			}
			journalID = &id
		}
		for lineID, v := range variances {
			if _, err := tx.Exec(ctx, `update count_sheet_line set posted_variance = $3 where tenant_id = $1 and id = $2`, tp.TenantID, lineID, v.String()); err != nil {
				return err
			}
		}
		var approvedBy *string
		if approval != "" {
			approvedBy = optionalText(tp.UserID)
		}
		if _, err := tx.Exec(ctx, `update count_sheet set status = 'POSTED', journal_id = $3, approved_by = $4, posted_by = $5, posted_at = now(),
			version = version + 1 where tenant_id = $1 and id = $2`, tp.TenantID, sheetID, journalID, approvedBy, optionalText(tp.UserID)); err != nil {
			return err
		}
		out, err = loadCountSheet(ctx, tx, sheetID, false)
		return err
	})
	return out, err
}

// countVariances locks the projection row of every counted line in lock-key
// order, computes the variance against the book quantity under the lock and
// returns the journal lines for the non-zero variances, the variance of every
// counted line, and a description of the first variance above tolerance
// (empty when none needs approval).
func countVariances(ctx context.Context, tx pgx.Tx, tenantID string, sheet domain.CountSheet, plan domain.CountPlan) ([]domain.JournalLine, map[string]decimal.Decimal, string, error) {
	type counted struct {
		line domain.CountLine
		row  *row
	}
	var items []counted
	for _, l := range sheet.Lines {
		if l.Counted == nil {
			continue
		}
		dims, err := l.Dims.Normalize()
		if err != nil {
			return nil, nil, "", domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
		}
		items = append(items, counted{line: l, row: &row{product: l.ProductID, dims: dims, hash: dims.Hash()}})
	}
	if len(items) == 0 {
		return nil, nil, "", problem(http.StatusUnprocessableEntity, domain.CodeCountNothing, "no line of the count sheet has been counted")
	}
	slices.SortFunc(items, func(a, b counted) int {
		return strings.Compare(rowKey(a.row.product, a.row.hash), rowKey(b.row.product, b.row.hash))
	})
	var lines []domain.JournalLine
	variances := map[string]decimal.Decimal{}
	approval := ""
	for _, it := range items {
		if err := lockRow(ctx, tx, tenantID, sheet.LegalEntityID, plan.Currency, it.row); err != nil {
			return nil, nil, "", err
		}
		book := it.row.oh.PhysicalInvent()
		variance := domain.Variance(book, *it.line.Counted)
		variances[it.line.ID] = variance
		if variance.IsZero() {
			continue
		}
		unitCost := decimal.Zero
		if it.row.oh.PostedQty.Sign() > 0 {
			unitCost = it.row.oh.PostedValue.Div(it.row.oh.PostedQty).Round(6)
		}
		if approval == "" && domain.ExceedsTolerance(variance, book, unitCost, plan.TolerancePct, plan.ToleranceValue) {
			approval = fmt.Sprintf("line %d variance %s against book %s exceeds the tolerance of %s%% or %s %s",
				it.line.LineNo, variance, book, plan.TolerancePct, plan.ToleranceValue, plan.Currency)
		}
		lines = append(lines, domain.JournalLine{LineNo: len(lines) + 1, ProductID: it.line.ProductID, Dims: it.row.dims,
			Qty: variance, EnteredQty: variance, EnteredUnit: plan.Unit, CostPrice: unitCost})
	}
	return lines, variances, approval, nil
}

// postCountJournal writes the COUNT journal of a sheet (keyed by the sheet, so
// a sheet has at most one) and posts it in the same transaction.
func (s *Postgres) postCountJournal(ctx context.Context, tx pgx.Tx, tenantID string, sheet domain.CountSheet, plan domain.CountPlan, lines []domain.JournalLine, p PostParams) (string, error) {
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into inventory_journal (tenant_id, legal_entity_id, id, journal_type, status, posting_date, currency, idempotency_key)
		values ($1, $2, $3, 'COUNT', 'DRAFT', $4, $5, $6)`, tenantID, sheet.LegalEntityID, id, time.Now().UTC().Format(dateLayout), plan.Currency,
		"count-sheet:"+sheet.ID); err != nil {
		return "", scopeProblem(err)
	}
	for _, l := range lines {
		hash, raw, err := upsertDimensionSet(ctx, tx, tenantID, l.Dims)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `insert into inventory_journal_line (tenant_id, journal_id, id, line_no, product_id, dimension_set_hash, dims, qty, entered_qty, entered_unit, cost_price)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, tenantID, id, ids.New(), l.LineNo, l.ProductID, hash, raw, l.Qty.String(),
			l.EnteredQty.String(), l.EnteredUnit, l.CostPrice.String()); err != nil {
			return "", err
		}
	}
	j, err := loadJournal(ctx, tx, id, true)
	if err != nil {
		return "", err
	}
	return id, s.postLoaded(ctx, tx, tenantID, j, p)
}

const countPlanColumns = `id::text, legal_entity_id::text, name, coalesce(warehouse, ''), currency::text, unit, tolerance_pct::text,
	tolerance_value::text, blind, created_at`

func scanCountPlan(row pgx.Row) (domain.CountPlan, error) {
	var p domain.CountPlan
	var pct, val string
	if err := row.Scan(&p.ID, &p.LegalEntityID, &p.Name, &p.Warehouse, &p.Currency, &p.Unit, &pct, &val, &p.Blind, &p.CreatedAt); err != nil {
		return p, err
	}
	p.TolerancePct, p.ToleranceValue = decimal.RequireFromString(pct), decimal.RequireFromString(val)
	return p, nil
}

func loadCountPlan(ctx context.Context, tx pgx.Tx, id string) (domain.CountPlan, error) {
	tp, _ := tenant.FromContext(ctx)
	p, err := scanCountPlan(tx.QueryRow(ctx, `select `+countPlanColumns+` from count_plan where tenant_id = $1 and id = $2`, tp.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, problem(http.StatusNotFound, domain.CodeCountNotFound, "count plan not found")
	}
	return p, err
}

const countSheetColumns = `s.id::text, s.legal_entity_id::text, s.plan_id::text, p.name, s.number, s.status, coalesce(s.approved_by, ''),
	coalesce(s.journal_id::text, ''), p.blind, s.version, s.created_at, s.posted_at,
	(select count(*) from count_sheet_line l where l.tenant_id = s.tenant_id and l.sheet_id = s.id),
	(select count(*) from count_sheet_line l where l.tenant_id = s.tenant_id and l.sheet_id = s.id and l.counted_qty is not null)`

func scanCountSheet(row pgx.Row) (domain.CountSheet, error) {
	var out domain.CountSheet
	err := row.Scan(&out.ID, &out.LegalEntityID, &out.PlanID, &out.PlanName, &out.Number, &out.Status, &out.ApprovedBy, &out.JournalID,
		&out.Blind, &out.Version, &out.CreatedAt, &out.PostedAt, &out.LineCount, &out.CountedCount)
	return out, err
}

// loadCountSheet reads a sheet and its lines in line order; forUpdate locks
// the header.
func loadCountSheet(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domain.CountSheet, error) {
	tp, _ := tenant.FromContext(ctx)
	q := `select ` + countSheetColumns + ` from count_sheet s join count_plan p on p.tenant_id = s.tenant_id and p.id = s.plan_id
		where s.tenant_id = $1 and s.id = $2`
	if forUpdate {
		q += ` for update of s`
	}
	out, err := scanCountSheet(tx.QueryRow(ctx, q, tp.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, problem(http.StatusNotFound, domain.CodeCountNotFound, "count sheet not found")
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `select l.id::text, l.line_no, l.product_id::text, d.dims, l.expected_qty::text, l.counted_qty::text,
		coalesce(l.counted_by, ''), l.counted_at, l.posted_variance::text from count_sheet_line l
		join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
		where l.tenant_id = $1 and l.sheet_id = $2 order by l.line_no`, tp.TenantID, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.CountLine
		var dims []byte
		var expected string
		var counted, posted *string
		if err := rows.Scan(&l.ID, &l.LineNo, &l.ProductID, &dims, &expected, &counted, &l.CountedBy, &l.CountedAt, &posted); err != nil {
			return out, err
		}
		if err := json.Unmarshal(dims, &l.Dims); err != nil {
			return out, err
		}
		l.Expected = decimal.RequireFromString(expected)
		if counted != nil {
			c := decimal.RequireFromString(*counted)
			l.Counted = &c
		}
		if posted != nil {
			v := decimal.RequireFromString(*posted)
			l.PostedVariance = &v
		}
		out.Lines = append(out.Lines, l)
	}
	return out, rows.Err()
}
