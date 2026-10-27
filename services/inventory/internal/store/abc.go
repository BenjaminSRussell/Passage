package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// AbcRunInput is the input of RunABC.
type AbcRunInput struct {
	IdempotencyKey string
	LegalEntityID  string
	Criterion      string
	Thresholds     domain.AbcThresholds
	ZeroClass      string
}

// RunABC classifies the legal entity's product x site stock by on-hand posted
// value (criterion VALUE, FR-16.19) and stores the run. An idempotency key replays
// the stored run.
func (s *Postgres) RunABC(ctx context.Context, in AbcRunInput) (domain.AbcRun, bool, error) {
	if err := in.Thresholds.Validate(); err != nil {
		return domain.AbcRun{}, false, domainProblem(err)
	}
	if in.Criterion != domain.AbcCriterionValue {
		return domain.AbcRun{}, false, problem(http.StatusBadRequest, domain.CodeInvalid, "criterion must be VALUE in this release")
	}
	zero := in.ZeroClass
	if zero == "" {
		zero = domain.ABCC
	}
	if zero != domain.ABCC && zero != domain.ABCN {
		return domain.AbcRun{}, false, problem(http.StatusBadRequest, domain.CodeInvalid, "zero class must be C or N")
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.AbcRun
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "abc", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from abc_run where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, in.IdempotencyKey).Scan(&existing)
		if err == nil {
			replayed = true
			if out, err = loadABCRun(ctx, tx, existing); err != nil {
				return err
			}
			if out.LegalEntityID != in.LegalEntityID || out.Criterion != in.Criterion || out.ZeroClass != zero || !out.Thresholds.A.Equal(in.Thresholds.A) ||
				!out.Thresholds.B.Equal(in.Thresholds.B) || !out.Thresholds.C.Equal(in.Thresholds.C) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// One currency per legal entity is assumed (the accounting currency); a
		// product x site valued in two currencies is refused rather than summed.
		rows, err := tx.Query(ctx, `select oh.product_id::text, coalesce(d.dims->>'site', ''), coalesce(sum(oh.posted_value), 0)::text,
			count(distinct oh.currency)
			from on_hand oh
			join dimension_set d on d.tenant_id = oh.tenant_id and d.hash = oh.dimension_set_hash
			where oh.tenant_id = $1 and oh.legal_entity_id = $2
			group by 1, 2 order by 1, 2`, tp.TenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		var items []domain.AbcItem
		for rows.Next() {
			var it domain.AbcItem
			var value string
			var currencies int
			if err := rows.Scan(&it.ProductID, &it.Site, &value, &currencies); err != nil {
				rows.Close()
				return err
			}
			if currencies > 1 {
				rows.Close()
				return problem(http.StatusUnprocessableEntity, CodeCurrencyMismatch,
					fmt.Sprintf("product %s at site %s is valued in more than one currency", it.ProductID, it.Site))
			}
			it.Value = decimal.RequireFromString(value)
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		ranks := domain.ClassifyABC(items, in.Thresholds, zero)
		runID := ids.New()
		if _, err := tx.Exec(ctx, `insert into abc_run (tenant_id, legal_entity_id, id, idempotency_key, criterion, threshold_a, threshold_b,
			threshold_c, zero_class) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, tp.TenantID, in.LegalEntityID, runID, in.IdempotencyKey,
			in.Criterion, in.Thresholds.A.String(), in.Thresholds.B.String(), in.Thresholds.C.String(), zero); err != nil {
			return scopeProblem(err)
		}
		for _, r := range ranks {
			if _, err := tx.Exec(ctx, `insert into abc_classification (tenant_id, run_id, product_id, site, criterion_value, cum_pct, class)
				values ($1, $2, $3, $4, $5, $6, $7)`, tp.TenantID, runID, r.ProductID, r.Site, r.Value.String(), r.CumPct.String(), r.Class); err != nil {
				return err
			}
		}
		out, err = loadABCRun(ctx, tx, runID)
		return err
	})
	return out, replayed, err
}

// GetABCRun returns a stored classification run with its items.
func (s *Postgres) GetABCRun(ctx context.Context, id string) (domain.AbcRun, error) {
	var out domain.AbcRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadABCRun(ctx, tx, id)
		return err
	})
	return out, err
}

func loadABCRun(ctx context.Context, tx pgx.Tx, id string) (domain.AbcRun, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.AbcRun
	var a, b, c string
	err := tx.QueryRow(ctx, `select `+abcRunColumns+` from abc_run where tenant_id = $1 and id = $2`, tp.TenantID, id).
		Scan(&out.ID, &out.LegalEntityID, &out.Criterion, &a, &b, &c, &out.ZeroClass, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, problem(http.StatusNotFound, domain.CodeABCNotFound, "ABC run not found")
	}
	if err != nil {
		return out, err
	}
	out.Thresholds = domain.AbcThresholds{A: decimal.RequireFromString(a), B: decimal.RequireFromString(b), C: decimal.RequireFromString(c)}
	rows, err := tx.Query(ctx, `select product_id::text, site, criterion_value::text, cum_pct::text, class
		from abc_classification where tenant_id = $1 and run_id = $2 order by criterion_value desc, product_id, site`, tp.TenantID, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.AbcRank
		var value, cum string
		if err := rows.Scan(&r.ProductID, &r.Site, &value, &cum, &r.Class); err != nil {
			return out, err
		}
		r.Value = decimal.RequireFromString(value)
		r.CumPct = decimal.RequireFromString(cum)
		out.Items = append(out.Items, r)
	}
	return out, rows.Err()
}

const abcRunColumns = `id::text, legal_entity_id::text, criterion, threshold_a::text, threshold_b::text, threshold_c::text, zero_class, created_at`

// AbcRunSummary is a stored run without its items: the number of items per class.
type AbcRunSummary struct {
	Run    domain.AbcRun
	Counts map[string]int
}

// ListABCRuns returns the latest classification runs, newest first (at most 100).
func (s *Postgres) ListABCRuns(ctx context.Context) ([]AbcRunSummary, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []AbcRunSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id::text, r.legal_entity_id::text, r.criterion, r.threshold_a::text, r.threshold_b::text,
			r.threshold_c::text, r.zero_class, r.created_at,
			count(c.*) filter (where c.class = 'A'), count(c.*) filter (where c.class = 'B'),
			count(c.*) filter (where c.class = 'C'), count(c.*) filter (where c.class = 'N')
			from abc_run r left join abc_classification c on c.tenant_id = r.tenant_id and c.run_id = r.id
			where r.tenant_id = $1
			group by r.tenant_id, r.id order by r.created_at desc, r.id desc limit 100`, tp.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.AbcRun
			var a, b, c string
			var na, nb, nc, nn int
			if err := rows.Scan(&r.ID, &r.LegalEntityID, &r.Criterion, &a, &b, &c, &r.ZeroClass, &r.CreatedAt, &na, &nb, &nc, &nn); err != nil {
				return err
			}
			r.Thresholds = domain.AbcThresholds{A: decimal.RequireFromString(a), B: decimal.RequireFromString(b), C: decimal.RequireFromString(c)}
			out = append(out, AbcRunSummary{Run: r, Counts: map[string]int{domain.ABCA: na, domain.ABCB: nb, domain.ABCC: nc, domain.ABCN: nn}})
		}
		return rows.Err()
	})
	return out, err
}
