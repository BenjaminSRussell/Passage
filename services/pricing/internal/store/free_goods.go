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

const freeGoodsColumns = `id::text, legal_entity_id::text, customer_kind, customer_key, product_kind, product_id, unit, kind, calc_rule,
	every_quantity::text, free_quantity::text, min_quantity::text, valid_from, valid_to, withdrawn, version`

func scanFreeGoods(row pgx.Row) (domain.FreeGoodsDef, error) {
	var r domain.FreeGoodsDef
	var ck, pk, every, free, min string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &ck, &r.CustomerKey, &pk, &r.ProductID, &r.Unit, &r.Kind, &r.Calc,
		&every, &free, &min, &r.ValidFrom, &r.ValidTo, &r.Withdrawn, &r.Version); err != nil {
		return domain.FreeGoodsDef{}, err
	}
	r.CustomerKind = domain.CustomerKind(ck)
	r.ProductKind = domain.ProductKind(pk)
	r.Every = decimal.RequireFromString(every)
	r.Free = decimal.RequireFromString(free)
	r.MinQty = decimal.RequireFromString(min)
	return r, nil
}

func freeGoodsNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeFreeGoodsRuleNotFound, "no free goods rule "+id)
}

func sameFreeGoods(a, b domain.FreeGoodsDef) bool {
	return a.LegalEntityID == b.LegalEntityID && a.CustomerKind == b.CustomerKind && a.CustomerKey == b.CustomerKey &&
		a.ProductKind == b.ProductKind && a.ProductID == b.ProductID && a.Unit == b.Unit && a.Kind == b.Kind && a.Calc == b.Calc && a.Every.Equal(b.Every) &&
		a.Free.Equal(b.Free) && a.MinQty.Equal(b.MinQty) && dayStr(a.ValidFrom) == dayStr(b.ValidFrom) && dayStr(a.ValidTo) == dayStr(b.ValidTo)
}

func freeGoodsEventData(r domain.FreeGoodsDef) pevents.FreeGoodsRuleData {
	return pevents.FreeGoodsRuleData{RuleID: r.ID, LegalEntityID: r.LegalEntityID, CustomerKind: string(r.CustomerKind),
		CustomerKey: r.CustomerKey, ProductKind: string(r.ProductKind), ProductID: r.ProductID, Unit: r.Unit, Kind: r.Kind, CalcRule: r.Calc,
		EveryQuantity: r.Every.String(), FreeQuantity: r.Free.String(), MinQuantity: r.MinQty.String(),
		ValidFrom: dayStr(r.ValidFrom), ValidTo: dayStr(r.ValidTo), Withdrawn: r.Withdrawn, Version: r.Version}
}

// CreateFreeGoodsRule stores a live rule at version 1. Under a transaction-scoped advisory
// lock on the rule key it refuses a live rule with the same customer, product and unit whose
// validity overlaps (409 PRC-0002), and announces the rule through the outbox in the same
// transaction. A repeated Idempotency-Key returns the original rule, or idempotency_key_reuse
// when the body differs.
func (s *Postgres) CreateFreeGoodsRule(ctx context.Context, in domain.FreeGoodsDef, idempotencyKey string) (domain.FreeGoodsDef, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out domain.FreeGoodsDef
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The lock comes first so a retry of the same request waits for the original and
		// replays it instead of tripping the overlap guard.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|fg|"+domain.FreeGoodsKey(in)); err != nil {
			return err
		}
		if key != nil {
			replay, err := scanFreeGoods(tx.QueryRow(ctx, `select `+freeGoodsColumns+` from free_goods_rule where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if !sameFreeGoods(replay, in) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		rows, err := tx.Query(ctx, `select `+freeGoodsColumns+` from free_goods_rule
			where legal_entity_id = $1 and customer_kind = $2 and customer_key = $3 and product_kind = $4 and product_id = $5 and unit = $6 and not withdrawn`,
			in.LegalEntityID, string(in.CustomerKind), in.CustomerKey, string(in.ProductKind), in.ProductID, in.Unit)
		if err != nil {
			return err
		}
		var live []domain.FreeGoodsDef
		for rows.Next() {
			r, err := scanFreeGoods(rows)
			if err != nil {
				rows.Close()
				return err
			}
			live = append(live, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if c, clash := domain.FreeGoodsOverlap(in, live); clash {
			return httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, fmt.Sprintf("overlapping free goods rule %s for the same customer, product and unit", c.ID))
		}
		created, err := scanFreeGoods(tx.QueryRow(ctx, `insert into free_goods_rule (tenant_id, id, legal_entity_id, customer_kind, customer_key,
			product_kind, product_id, unit, kind, calc_rule, every_quantity, free_quantity, min_quantity, valid_from, valid_to, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric, $12::numeric, $13::numeric, $14, $15, $16)
			returning `+freeGoodsColumns,
			p.TenantID, ids.New(), in.LegalEntityID, string(in.CustomerKind), in.CustomerKey, string(in.ProductKind), in.ProductID, in.Unit, in.Kind, in.Calc,
			in.Every.String(), in.Free.String(), in.MinQty.String(), dayStr(in.ValidFrom), dayStr(in.ValidTo), key))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "free_goods_rule_tenant_id_idempotency_key_key" {
			// A concurrent request with the same key and another rule key won the insert.
			return httpx.NewProblem(http.StatusConflict, CodeIdempotencyUse, "a request with this Idempotency-Key is in flight; retry it")
		}
		if err != nil {
			return err
		}
		out = created
		return enqueueEvent(ctx, tx, "freegoodsrule", out.ID, pevents.FreeGoodsRuleCreated, freeGoodsEventData(out))
	})
	return out, err
}

// GetFreeGoodsRule reads one rule. PRC-0047 when it does not exist or lies outside the
// caller's legal entities.
func (s *Postgres) GetFreeGoodsRule(ctx context.Context, id string) (domain.FreeGoodsDef, error) {
	var out domain.FreeGoodsDef
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanFreeGoods(tx.QueryRow(ctx, `select `+freeGoodsColumns+` from free_goods_rule where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return freeGoodsNotFound(id)
		}
		return err
	})
	return out, err
}

// ListFreeGoodsRules returns the rules of one legal entity (all the caller may see when
// empty), optionally for one product, latest start first. Withdrawn rules are included.
func (s *Postgres) ListFreeGoodsRules(ctx context.Context, legalEntityID, productID string, limit int) ([]domain.FreeGoodsDef, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.FreeGoodsDef{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+freeGoodsColumns+` from free_goods_rule
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or product_id = $2)
			order by valid_from desc, product_id, id limit $3`, legalEntityID, productID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanFreeGoods(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ActiveFreeGoodsRules returns the live rules of one legal entity keyed on one of the named
// keys (the products and the product groups of the document lines; the proposal tells the two
// kinds apart) that cover asOf, for the resolve proposal. Withdrawn rules and rules outside their
// validity are not returned.
func (s *Postgres) ActiveFreeGoodsRules(ctx context.Context, legalEntityID string, keys []string, asOf time.Time) ([]domain.FreeGoodsDef, error) {
	out := []domain.FreeGoodsDef{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+freeGoodsColumns+` from free_goods_rule
			where legal_entity_id = $1::uuid and product_id = any($2::text[]) and not withdrawn
			and valid_from <= $3::date and valid_to >= $3::date`, legalEntityID, keys, dayStr(asOf))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanFreeGoods(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// WithdrawFreeGoodsRule takes a rule out of resolve at the expected version (412
// conflict_version otherwise; 409 PRC-0031 when already withdrawn) and announces it through
// the outbox in the same transaction. A repeated Idempotency-Key replays the first response.
func (s *Postgres) WithdrawFreeGoodsRule(ctx context.Context, id string, version int, idempotencyKey string) (domain.FreeGoodsDef, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.FreeGoodsDef
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanFreeGoods(tx.QueryRow(ctx, `select `+freeGoodsColumns+` from free_goods_rule where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return freeGoodsNotFound(id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("freegoods-withdraw", id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "free goods rule version does not match")
		}
		if cur.Withdrawn {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "free goods rule is already withdrawn")
		}
		out, err = scanFreeGoods(tx.QueryRow(ctx, `update free_goods_rule set withdrawn = true, version = version + 1
			where id = $1 returning `+freeGoodsColumns, id))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "freegoodsrule", out.ID, pevents.FreeGoodsRuleWithdrawn, freeGoodsEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ReviseFreeGoodsRule changes the quantities, calculation rule and validity of a live rule at
// the expected version (412 conflict_version otherwise; 409 PRC-0031 when withdrawn or when
// nothing changes; 409 PRC-0002 when the new validity overlaps another live rule of the same
// customer, product and unit; 422 COM-0001 with field errors when a value is invalid or the
// revision would change what a day before today proposed, domain.FreeGoodsPastChanges). The
// next resolve uses the revised values; documents already priced keep theirs. It is announced
// through the outbox in the same transaction. A repeated Idempotency-Key replays the first
// response. The advisory lock is create's, so a create and a revise of the same key serialize.
func (s *Postgres) ReviseFreeGoodsRule(ctx context.Context, rev domain.FreeGoodsRevision, idempotencyKey string) (domain.FreeGoodsDef, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.FreeGoodsDef
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanFreeGoods(tx.QueryRow(ctx, `select `+freeGoodsColumns+` from free_goods_rule where id = $1 for update`, rev.ID))
		if errors.Is(err, pgx.ErrNoRows) {
			return freeGoodsNotFound(rev.ID)
		}
		if err != nil {
			return err
		}
		hash := commandHash("freegoods-revise", rev.ID, strconv.Itoa(rev.Version), rev.Calc, rev.Every.String(), rev.Free.String(),
			rev.MinQty.String(), dayStr(rev.ValidFrom), dayStr(rev.ValidTo))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != rev.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "free goods rule version does not match")
		}
		if cur.Withdrawn {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "free goods rule is withdrawn and cannot be revised")
		}
		next := rev.Apply(cur)
		var today time.Time
		if err := tx.QueryRow(ctx, `select current_date`).Scan(&today); err != nil {
			return err
		}
		fields := domain.ValidateFreeGoods(next)
		for k, v := range domain.FreeGoodsPastChanges(cur, next, today) {
			if _, ok := fields[k]; !ok {
				fields[k] = v
			}
		}
		if len(fields) > 0 {
			problem := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "free goods rule revision is invalid")
			problem.Errors = fields
			return problem
		}
		if sameFreeGoods(cur, next) {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "the revision changes nothing")
		}
		// Same lock as create, so a concurrent create or revise of the same key waits.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|fg|"+domain.FreeGoodsKey(cur)); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+freeGoodsColumns+` from free_goods_rule
			where legal_entity_id = $1 and customer_kind = $2 and customer_key = $3 and product_kind = $4 and product_id = $5 and unit = $6 and not withdrawn and id <> $7`,
			cur.LegalEntityID, string(cur.CustomerKind), cur.CustomerKey, string(cur.ProductKind), cur.ProductID, cur.Unit, cur.ID)
		if err != nil {
			return err
		}
		var live []domain.FreeGoodsDef
		for rows.Next() {
			r, err := scanFreeGoods(rows)
			if err != nil {
				rows.Close()
				return err
			}
			live = append(live, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if c, clash := domain.FreeGoodsOverlap(next, live); clash {
			return httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, fmt.Sprintf("overlapping free goods rule %s for the same customer, product and unit", c.ID))
		}
		out, err = scanFreeGoods(tx.QueryRow(ctx, `update free_goods_rule set calc_rule = $2, every_quantity = $3::numeric,
			free_quantity = $4::numeric, min_quantity = $5::numeric, valid_from = $6, valid_to = $7, version = version + 1
			where id = $1 returning `+freeGoodsColumns,
			cur.ID, next.Calc, next.Every.String(), next.Free.String(), next.MinQty.String(), dayStr(next.ValidFrom), dayStr(next.ValidTo)))
		if err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "freegoodsrule", out.ID, pevents.FreeGoodsRuleRevised, freeGoodsEventData(out)); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}
