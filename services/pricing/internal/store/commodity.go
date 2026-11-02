package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// CommodityRun is a stored, non-preview run of a price calculation (FR-26.21). JournalID is
// empty when no ACTIVE record changed price.
type CommodityRun struct {
	ID             string
	LegalEntityID  string
	CalculationID  string
	AsOf           time.Time
	IndexAsOf      time.Time
	IndexRevision  int
	IndexValue     decimal.Decimal
	Price          decimal.Decimal
	ChangedCount   int
	UnchangedCount int
	Stale          bool
	JournalID      string
	// RunBy is the user who ran it; that user cannot post the run's journal (PRC-0056).
	RunBy     string
	CreatedAt time.Time
}

const basisColumns = `id::text, legal_entity_id::text, code, name, currency, unit, version`

func scanBasis(row pgx.Row) (domain.CommodityBasis, error) {
	var b domain.CommodityBasis
	err := row.Scan(&b.ID, &b.LegalEntityID, &b.Code, &b.Name, &b.Currency, &b.Unit, &b.Version)
	return b, err
}

func basisNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeBasisNotFound, "no commodity cost basis type "+id)
}

func calcNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeCalculationNotFound, "no commodity price calculation "+id)
}

func isConstraint(err error, name string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == name
}

func inFlight() error {
	return httpx.NewProblem(http.StatusConflict, CodeIdempotencyUse, "a request with this Idempotency-Key is in flight; retry it")
}

// CreateCommodityBasis stores a cost basis type at version 1. A second basis with the same
// code in the legal entity is refused (409 PRC-0054). A repeated Idempotency-Key returns the
// original, or idempotency_key_reuse when the body differs.
func (s *Postgres) CreateCommodityBasis(ctx context.Context, in domain.CommodityBasis, idempotencyKey string) (domain.CommodityBasis, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.CommodityBasis
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|cbasis|"+in.LegalEntityID+"|"+in.Code); err != nil {
			return err
		}
		if key != nil {
			replay, err := scanBasis(tx.QueryRow(ctx, `select `+basisColumns+` from commodity_basis where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if replay.LegalEntityID != in.LegalEntityID || replay.Code != in.Code || replay.Name != in.Name || replay.Currency != in.Currency || replay.Unit != in.Unit {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var taken bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from commodity_basis where legal_entity_id = $1 and code = $2)`, in.LegalEntityID, in.Code).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return httpx.NewProblem(http.StatusConflict, domain.CodeBasisCodeTaken, "a cost basis type with code "+in.Code+" already exists in this legal entity")
		}
		created, err := scanBasis(tx.QueryRow(ctx, `insert into commodity_basis (tenant_id, id, legal_entity_id, code, name, currency, unit, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+basisColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.Code, in.Name, in.Currency, in.Unit, key))
		if isConstraint(err, "commodity_basis_tenant_id_idempotency_key_key") {
			return inFlight()
		}
		if err != nil {
			return err
		}
		out = created
		return enqueueEvent(ctx, tx, "commoditybasis", out.ID, pevents.CommodityBasisCreated, pevents.CommodityBasisData{
			BasisID: out.ID, LegalEntityID: out.LegalEntityID, Code: out.Code, Currency: out.Currency, Unit: out.Unit})
	})
	return out, err
}

// GetCommodityBasis reads one cost basis type (404 PRC-0050).
func (s *Postgres) GetCommodityBasis(ctx context.Context, id string) (domain.CommodityBasis, error) {
	var out domain.CommodityBasis
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanBasis(tx.QueryRow(ctx, `select `+basisColumns+` from commodity_basis where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return basisNotFound(id)
		}
		return err
	})
	return out, err
}

// ListCommodityBases returns the cost basis types of one legal entity (all the caller may
// see when empty) ordered by code.
func (s *Postgres) ListCommodityBases(ctx context.Context, legalEntityID string, limit int) ([]domain.CommodityBasis, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.CommodityBasis{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+basisColumns+` from commodity_basis
			where ($1::text = '' or legal_entity_id::text = $1) order by code, id limit $2`, legalEntityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBasis(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

const indexColumns = `basis_id::text, as_of, revision, value::text, source`

func scanIndex(row pgx.Row) (domain.IndexValue, error) {
	var v domain.IndexValue
	var val string
	if err := row.Scan(&v.BasisID, &v.AsOf, &v.Revision, &val, &v.Source); err != nil {
		return domain.IndexValue{}, err
	}
	v.Value = decimal.RequireFromString(val)
	return v, nil
}

// RecordIndexValue appends an index quote for a day. Quotes are never overwritten: a second
// quote for the same day is the next revision, and a run reads the highest one. The basis
// row is locked to number revisions. A repeated Idempotency-Key returns the original quote.
func (s *Postgres) RecordIndexValue(ctx context.Context, in domain.IndexValue, idempotencyKey string) (domain.IndexValue, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.IndexValue
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var le string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from commodity_basis where id = $1 for update`, in.BasisID).Scan(&le); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return basisNotFound(in.BasisID)
			}
			return err
		}
		if key != nil {
			replay, err := scanIndex(tx.QueryRow(ctx, `select `+indexColumns+` from commodity_index_value where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if replay.BasisID != in.BasisID || dayStr(replay.AsOf) != dayStr(in.AsOf) || !replay.Value.Equal(in.Value) || replay.Source != in.Source {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		created, err := scanIndex(tx.QueryRow(ctx, `insert into commodity_index_value (tenant_id, basis_id, as_of, revision, legal_entity_id, value, source, idempotency_key)
			values ($1, $2, $3::date, (select coalesce(max(revision), 0) + 1 from commodity_index_value where basis_id = $2 and as_of = $3::date),
			$4, $5::numeric, $6, $7) returning `+indexColumns,
			p.TenantID, in.BasisID, dayStr(in.AsOf), le, in.Value.String(), in.Source, key))
		if isConstraint(err, "commodity_index_value_tenant_id_idempotency_key_key") {
			return inFlight()
		}
		if err != nil {
			return err
		}
		out = created
		return enqueueEvent(ctx, tx, "commodityindex", out.BasisID, pevents.CommodityIndexRecorded, pevents.CommodityIndexRecordedData{
			BasisID: out.BasisID, LegalEntityID: le, AsOf: dayStr(out.AsOf), Revision: out.Revision, Value: out.Value.String(), Source: out.Source})
	})
	return out, err
}

// ListIndexValues returns every quote of a basis, newest day first and highest revision
// first within a day (the Commodity prices inquiry).
func (s *Postgres) ListIndexValues(ctx context.Context, basisID string, limit int) ([]domain.IndexValue, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.IndexValue{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from commodity_basis where id = $1)`, basisID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return basisNotFound(basisID)
		}
		rows, err := tx.Query(ctx, `select `+indexColumns+` from commodity_index_value where basis_id = $1
			order by as_of desc, revision desc limit $2`, basisID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanIndex(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

const calcColumns = `id::text, legal_entity_id::text, name, basis_id::text, factor::text, margin_kind, margin::text, rounding,
	max_index_age_days, customer_kind, customer_key, product_id, unit, currency, withdrawn, version`

func scanCalc(row pgx.Row) (domain.CommodityCalc, error) {
	var c domain.CommodityCalc
	var factor, margin, ck string
	if err := row.Scan(&c.ID, &c.LegalEntityID, &c.Name, &c.BasisID, &factor, &c.MarginKind, &margin, &c.Rounding,
		&c.MaxIndexAgeDays, &ck, &c.CustomerKey, &c.ProductID, &c.Unit, &c.Currency, &c.Withdrawn, &c.Version); err != nil {
		return domain.CommodityCalc{}, err
	}
	c.CustomerKind = domain.CustomerKind(ck)
	c.Factor, c.Margin = decimal.RequireFromString(factor), decimal.RequireFromString(margin)
	return c, nil
}

func sameCalc(a, b domain.CommodityCalc) bool {
	return a.LegalEntityID == b.LegalEntityID && a.Name == b.Name && a.BasisID == b.BasisID && a.Factor.Equal(b.Factor) &&
		a.MarginKind == b.MarginKind && a.Margin.Equal(b.Margin) && a.Rounding == b.Rounding && a.MaxIndexAgeDays == b.MaxIndexAgeDays &&
		a.CustomerKind == b.CustomerKind && a.CustomerKey == b.CustomerKey && a.ProductID == b.ProductID && a.Unit == b.Unit && a.Currency == b.Currency
}

func calcEventData(c domain.CommodityCalc) pevents.CommodityCalculationData {
	return pevents.CommodityCalculationData{CalculationID: c.ID, LegalEntityID: c.LegalEntityID, BasisID: c.BasisID, ProductID: c.ProductID,
		CustomerKind: string(c.CustomerKind), CustomerKey: c.CustomerKey, Unit: c.Unit, Currency: c.Currency, Factor: c.Factor.String(),
		MarginKind: c.MarginKind, Margin: c.Margin.String(), Withdrawn: c.Withdrawn, Version: c.Version}
}

// CreateCommodityCalc stores a price calculation at version 1. The basis must exist in the
// same legal entity (404 PRC-0050) and be quoted in the calculation's currency (422
// PRC-0055: there is no cross-currency conversion, so a factor would hide an exchange rate;
// the unit may differ, the factor converts it). A repeated Idempotency-Key returns the original.
func (s *Postgres) CreateCommodityCalc(ctx context.Context, in domain.CommodityCalc, idempotencyKey string) (domain.CommodityCalc, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.CommodityCalc
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|ccalc|"+idempotencyKey); err != nil {
				return err
			}
			replay, err := scanCalc(tx.QueryRow(ctx, `select `+calcColumns+` from commodity_calculation where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if !sameCalc(replay, in) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var basisCurrency string
		err := tx.QueryRow(ctx, `select currency from commodity_basis where id = $1 and legal_entity_id = $2`, in.BasisID, in.LegalEntityID).Scan(&basisCurrency)
		if errors.Is(err, pgx.ErrNoRows) {
			return basisNotFound(in.BasisID)
		}
		if err != nil {
			return err
		}
		if basisCurrency != in.Currency {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeCurrencyMismatch,
				"the cost basis is quoted in "+basisCurrency+"; a calculation prices in the same currency ("+in.Currency+" given)")
		}
		created, err := scanCalc(tx.QueryRow(ctx, `insert into commodity_calculation (tenant_id, id, legal_entity_id, name, basis_id, factor, margin_kind, margin,
			rounding, max_index_age_days, customer_kind, customer_key, product_id, unit, currency, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8::numeric, $9, $10, $11, $12, $13, $14, $15, $16) returning `+calcColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.Name, in.BasisID, in.Factor.String(), in.MarginKind, in.Margin.String(),
			in.Rounding, in.MaxIndexAgeDays, string(in.CustomerKind), in.CustomerKey, in.ProductID, in.Unit, in.Currency, key))
		if isConstraint(err, "commodity_calculation_tenant_id_idempotency_key_key") {
			return inFlight()
		}
		if err != nil {
			return err
		}
		out = created
		return enqueueEvent(ctx, tx, "commoditycalculation", out.ID, pevents.CommodityCalculationCreated, calcEventData(out))
	})
	return out, err
}

// GetCommodityCalc reads one calculation (404 PRC-0051).
func (s *Postgres) GetCommodityCalc(ctx context.Context, id string) (domain.CommodityCalc, error) {
	var out domain.CommodityCalc
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanCalc(tx.QueryRow(ctx, `select `+calcColumns+` from commodity_calculation where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return calcNotFound(id)
		}
		return err
	})
	return out, err
}

// ListCommodityCalcs returns the calculations of one legal entity (all the caller may see
// when empty), optionally of one basis, ordered by name. Withdrawn ones are included.
func (s *Postgres) ListCommodityCalcs(ctx context.Context, legalEntityID, basisID string, limit int) ([]domain.CommodityCalc, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.CommodityCalc{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+calcColumns+` from commodity_calculation
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or basis_id::text = $2)
			order by name, id limit $3`, legalEntityID, basisID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCalc(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// WithdrawCommodityCalc retires a calculation at the expected version (412 conflict_version;
// 409 PRC-0031 when already withdrawn). A repeated Idempotency-Key replays the first response.
func (s *Postgres) WithdrawCommodityCalc(ctx context.Context, id string, version int, idempotencyKey string) (domain.CommodityCalc, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CommodityCalc
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanCalc(tx.QueryRow(ctx, `select `+calcColumns+` from commodity_calculation where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return calcNotFound(id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("ccalc-withdraw", id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "calculation version does not match")
		}
		if cur.Withdrawn {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "calculation is already withdrawn")
		}
		out, err = scanCalc(tx.QueryRow(ctx, `update commodity_calculation set withdrawn = true, version = version + 1
			where id = $1 returning `+calcColumns, id))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "commoditycalculation", out.ID, pevents.CommodityCalculationWithdrawn, calcEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// planCommodityRun reads the calculation, its latest index on or before asOf and the ACTIVE
// PRICE records on its key, and plans the run. Read-only.
func planCommodityRun(ctx context.Context, tx pgx.Tx, calcID string, asOf time.Time) (domain.CommodityCalc, domain.RunPlan, error) {
	calc, err := scanCalc(tx.QueryRow(ctx, `select `+calcColumns+` from commodity_calculation where id = $1`, calcID))
	if errors.Is(err, pgx.ErrNoRows) {
		return calc, domain.RunPlan{}, calcNotFound(calcID)
	}
	if err != nil {
		return calc, domain.RunPlan{}, err
	}
	if calc.Withdrawn {
		return calc, domain.RunPlan{}, httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "calculation is withdrawn")
	}
	idx, err := scanIndex(tx.QueryRow(ctx, `select `+indexColumns+` from commodity_index_value where basis_id = $1 and as_of <= $2::date
		order by as_of desc, revision desc limit 1`, calc.BasisID, dayStr(asOf)))
	if errors.Is(err, pgx.ErrNoRows) {
		return calc, domain.RunPlan{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoIndexValue,
			fmt.Sprintf("no index value for the cost basis on or before %s", dayStr(asOf)))
	}
	if err != nil {
		return calc, domain.RunPlan{}, err
	}
	rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
		where legal_entity_id = $1 and status = 'ACTIVE' and condition_type = 'PRICE' and customer_kind = $2 and customer_key = $3
		and product_kind = 'product' and product_key = $4 and unit = $5 and currency = $6 and from_quantity = 0
		and variant = '' and campaign_id is null and valid_from <= $7::date and valid_to >= $7::date order by id`,
		calc.LegalEntityID, string(calc.CustomerKind), calc.CustomerKey, calc.ProductID, calc.Unit, calc.Currency, dayStr(asOf))
	if err != nil {
		return calc, domain.RunPlan{}, err
	}
	var stored []domain.Record
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return calc, domain.RunPlan{}, err
		}
		stored = append(stored, e.Record)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return calc, domain.RunPlan{}, err
	}
	plan, perr := domain.PlanRun(calc, idx, asOf, stored)
	if perr != nil {
		return calc, domain.RunPlan{}, httpx.NewProblem(http.StatusUnprocessableEntity, perr.Code, perr.Detail)
	}
	return calc, plan, nil
}

// PreviewCommodityRun plans a run without writing anything: the index used, the price and
// the records that would change.
func (s *Postgres) PreviewCommodityRun(ctx context.Context, calcID string, asOf time.Time) (domain.CommodityCalc, domain.RunPlan, error) {
	var calc domain.CommodityCalc
	var plan domain.RunPlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		calc, plan, err = planCommodityRun(ctx, tx, calcID, asOf)
		return err
	})
	return calc, plan, err
}

const runColumns = `id::text, legal_entity_id::text, calculation_id::text, as_of, index_as_of, index_revision, index_value::text,
	price::text, changed_count, unchanged_count, stale, coalesce(journal_id::text, ''), run_by, created_at`

func scanRun(row pgx.Row) (CommodityRun, error) {
	var r CommodityRun
	var iv, price string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.CalculationID, &r.AsOf, &r.IndexAsOf, &r.IndexRevision, &iv, &price,
		&r.ChangedCount, &r.UnchangedCount, &r.Stale, &r.JournalID, &r.RunBy, &r.CreatedAt); err != nil {
		return CommodityRun{}, err
	}
	r.IndexValue, r.Price = decimal.RequireFromString(iv), decimal.RequireFromString(price)
	return r, nil
}

// RunCommodityCalc runs a price calculation: it reads the latest index on or before asOf,
// prices the key, and when ACTIVE records differ writes a price journal of UPDATE lines
// validated against the current records (VALIDATED when clean, otherwise OPEN with the line
// errors) and the run row in one transaction. The journal is posted by the normal journal
// post, so the run and the post are two separate acts (FR-26.9), and the user who ran it
// cannot post it (PRC-0056, see PostJournal). Runs of one calculation are serialized. The run
// row keeps the index day, revision and value it used, so a later correction of a past quote
// leaves the run reproducible. A repeated Idempotency-Key replays the first run.
func (s *Postgres) RunCommodityCalc(ctx context.Context, calcID string, asOf time.Time, idempotencyKey string) (CommodityRun, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out CommodityRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|crun|"+calcID); err != nil {
			return err
		}
		if key != nil {
			replay, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from commodity_run where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if replay.CalculationID != calcID || dayStr(replay.AsOf) != dayStr(asOf) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		calc, plan, err := planCommodityRun(ctx, tx, calcID, asOf)
		if err != nil {
			return err
		}
		runID := ids.New()
		journalID := ""
		if len(plan.Changes) > 0 {
			journalID = ids.New()
			j := domain.Journal{ID: journalID, LegalEntityID: calc.LegalEntityID, Lines: plan.JournalLines()}
			name := truncate(fmt.Sprintf("Commodity %s %s", calc.Name, dayStr(asOf)), 120)
			if _, err := tx.Exec(ctx, `insert into price_journal (tenant_id, id, legal_entity_id, name, status, smart_round, line_count, created_by)
				values ($1, $2, $3, $4, 'OPEN', false, $5, $6)`, p.TenantID, journalID, calc.LegalEntityID, name, len(j.Lines), p.UserID); err != nil {
				return err
			}
			clean, err := checkJournal(ctx, tx, &j)
			if err != nil {
				return err
			}
			errCount := 0
			for _, l := range j.Lines {
				if len(l.Errors) > 0 {
					errCount++
				}
			}
			status := domain.JournalOpen
			if clean {
				status = domain.JournalValidated
			}
			if err := writeLines(ctx, tx, p.TenantID, journalID, j.Lines); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update price_journal set status = $2, error_count = $3 where id = $1`, journalID, status, errCount); err != nil {
				return err
			}
		}
		var jid *string
		if journalID != "" {
			jid = &journalID
		}
		created, err := scanRun(tx.QueryRow(ctx, `insert into commodity_run (tenant_id, id, legal_entity_id, calculation_id, as_of, index_as_of, index_revision,
			index_value, price, changed_count, unchanged_count, stale, journal_id, run_by, idempotency_key)
			values ($1, $2, $3, $4, $5::date, $6::date, $7, $8::numeric, $9::numeric, $10, $11, $12, $13, $14, $15) returning `+runColumns,
			p.TenantID, runID, calc.LegalEntityID, calcID, dayStr(asOf), dayStr(plan.Index.AsOf), plan.Index.Revision, plan.Index.Value.String(),
			plan.Price.String(), len(plan.Changes), plan.Unchanged, plan.Stale, jid, p.UserID, key))
		if isConstraint(err, "commodity_run_tenant_id_idempotency_key_key") {
			return inFlight()
		}
		if err != nil {
			return err
		}
		out = created
		return enqueueEvent(ctx, tx, "commodityrun", out.ID, pevents.CommodityRunCompleted, pevents.CommodityRunCompletedData{
			RunID: out.ID, CalculationID: calcID, LegalEntityID: calc.LegalEntityID, AsOf: dayStr(asOf), IndexAsOf: dayStr(plan.Index.AsOf),
			IndexRevision: plan.Index.Revision, IndexValue: plan.Index.Value.String(), Price: plan.Price.String(),
			ChangedCount: out.ChangedCount, UnchangedCount: out.UnchangedCount, Stale: out.Stale, JournalID: out.JournalID})
	})
	return out, err
}

// ListCommodityRuns returns the runs of one calculation, newest first.
func (s *Postgres) ListCommodityRuns(ctx context.Context, calcID string, limit int) ([]CommodityRun, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []CommodityRun{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from commodity_calculation where id = $1)`, calcID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return calcNotFound(calcID)
		}
		rows, err := tx.Query(ctx, `select `+runColumns+` from commodity_run where calculation_id = $1 order by created_at desc, id limit $2`, calcID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
