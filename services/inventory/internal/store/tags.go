package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

const tagColumns = `t.id::text, t.seq, t.tag_number, t.plan_id::text, t.status, coalesce(t.product_id::text, ''),
	t.dims, t.qty::text, coalesce(t.counted_by, ''), coalesce(t.sheet_id::text, ''), t.version`

// GenerateTags creates count tags for a plan (FR-16.18). Numbers come from the
// legal entity's TAG- sequence, allocated under a transaction-scoped advisory
// lock, so a number is consumed once (BR-16.15). A repeated key returns the tags of the
// original range with replayed=true; the same key for another plan or count
// is idempotency_key_reuse.
func (s *Postgres) GenerateTags(ctx context.Context, planID, key string, count int) ([]domain.CountTag, bool, error) {
	if count < 1 || count > domain.MaxTagRange {
		return nil, false, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: fmt.Sprintf("count must be between 1 and %d", domain.MaxTagRange)})
	}
	tp, _ := tenant.FromContext(ctx)
	var out []domain.CountTag
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "count-tags", tp.TenantID, key); err != nil {
			return err
		}
		var rangeID, rangePlan string
		var first, last int64
		err := tx.QueryRow(ctx, `select id::text, plan_id::text, first_seq, last_seq from count_tag_range where tenant_id = $1 and idempotency_key = $2`,
			tp.TenantID, key).Scan(&rangeID, &rangePlan, &first, &last)
		if err == nil {
			if rangePlan != planID || last-first+1 != int64(count) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
			}
			replayed = true
			out, err = loadTags(ctx, tx, tp.TenantID, false, "t.range_id = $2", rangeID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		plan, err := loadCountPlan(ctx, tx, planID)
		if err != nil {
			return err
		}
		// The TAG- sequence runs per legal entity (like the wave 5 number
		// sequences per company); the caller can see every range of the plan's
		// legal entity, and the lock serializes allocations within it.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"inventory.tag-seq|"+tp.TenantID+"|"+plan.LegalEntityID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(max(last_seq), 0) from count_tag_range where tenant_id = $1 and legal_entity_id = $2`,
			tp.TenantID, plan.LegalEntityID).Scan(&last); err != nil {
			return err
		}
		rangeID = ids.New()
		if _, err := tx.Exec(ctx, `insert into count_tag_range (tenant_id, legal_entity_id, id, plan_id, idempotency_key, first_seq, last_seq)
			values ($1, $2, $3, $4, $5, $6, $7)`,
			tp.TenantID, plan.LegalEntityID, rangeID, planID, key, last+1, last+int64(count)); err != nil {
			return scopeProblem(err)
		}
		if _, err := tx.Exec(ctx, `insert into count_tag (tenant_id, legal_entity_id, id, range_id, plan_id, seq, tag_number, status)
			select $1, $2, gen_random_uuid(), $3, $4, n, 'TAG-' || lpad(n::text, 6, '0'), 'OPEN' from generate_series($5::bigint, $6::bigint) n`,
			tp.TenantID, plan.LegalEntityID, rangeID, planID, last+1, last+int64(count)); err != nil {
			return err
		}
		out, err = loadTags(ctx, tx, tp.TenantID, false, "t.range_id = $2", rangeID)
		return err
	})
	return out, replayed, err
}

// ListTags returns the tags of a plan in number order, optionally of one
// status (the missing and void tags are listed before a transfer, FR-16.18).
func (s *Postgres) ListTags(ctx context.Context, planID string, status domain.TagStatus) ([]domain.CountTag, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []domain.CountTag{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadCountPlan(ctx, tx, planID); err != nil {
			return err
		}
		tags, err := loadTags(ctx, tx, tp.TenantID, false, "t.plan_id = $2 and ($3 = '' or t.status = $3)", planID, string(status))
		if err != nil {
			return err
		}
		out = append(out, tags...)
		return nil
	})
	return out, err
}

// UseTag records a count on an OPEN tag (FR-16.18). version is the tag's
// If-Match value; the counter is the caller.
func (s *Postgres) UseTag(ctx context.Context, tagID string, version int, productID string, dims domain.Dimensions, qty decimal.Decimal) (domain.CountTag, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountTag
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := loadTagLocked(ctx, tx, tp.TenantID, tagID)
		if err != nil {
			return err
		}
		if t.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count tag changed since it was read; reread and retry")
		}
		plan, err := loadCountPlan(ctx, tx, t.PlanID)
		if err != nil {
			return err
		}
		next, err := domain.UseTag(t, plan.Warehouse, productID, dims, qty, s.rules.MaxScale)
		if err != nil {
			return domainProblem(err)
		}
		raw, err := json.Marshal(next.Dims)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update count_tag set status = 'USED', product_id = $3, dims = $4, qty = $5, counted_by = $6, counted_at = now(),
			version = version + 1 where tenant_id = $1 and id = $2`,
			tp.TenantID, tagID, productID, raw, qty.String(), optionalText(tp.UserID)); err != nil {
			return err
		}
		out, err = loadTagLocked(ctx, tx, tp.TenantID, tagID)
		return err
	})
	return out, err
}

// VoidTag voids a tag that has not been transferred. version is the If-Match value.
func (s *Postgres) VoidTag(ctx context.Context, tagID string, version int) (domain.CountTag, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountTag
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := loadTagLocked(ctx, tx, tp.TenantID, tagID)
		if err != nil {
			return err
		}
		if t.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count tag changed since it was read; reread and retry")
		}
		if _, err := domain.VoidTag(t); err != nil {
			return domainProblem(err)
		}
		if _, err := tx.Exec(ctx, `update count_tag set status = 'VOID', version = version + 1 where tenant_id = $1 and id = $2`, tp.TenantID, tagID); err != nil {
			return err
		}
		out, err = loadTagLocked(ctx, tx, tp.TenantID, tagID)
		return err
	})
	return out, err
}

// TransferTags moves the used tags of a plan into a new OPEN count sheet
// (FR-16.18, "transfer to counting journal"). Each product and dimension set
// becomes one line whose counted quantity is the sum of its tags and whose
// expected quantity is the book quantity now; the sheet is then posted like
// any count sheet, so the variance is measured against the book at posting
// and the tolerance approval applies. Open tags block the transfer
// (INV-TAG-002) unless markOpenMissing, which only a supervisor may use
// (canApprove, BR-16.15); they become MISSING. A repeated key returns the
// sheet already created (replayed=true).
func (s *Postgres) TransferTags(ctx context.Context, planID, key string, markOpenMissing, canApprove bool) (domain.CountSheet, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	sheetKey := "tags:" + key
	var out domain.CountSheet
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "count-sheet", tp.TenantID, sheetKey); err != nil {
			return err
		}
		var existing, existingPlan string
		err := tx.QueryRow(ctx, `select id::text, plan_id::text from count_sheet where tenant_id = $1 and idempotency_key = $2`, tp.TenantID, sheetKey).
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
		tags, err := loadTags(ctx, tx, tp.TenantID, true, "t.plan_id = $2 and t.sheet_id is null", planID)
		if err != nil {
			return err
		}
		transfer, err := domain.PlanTagTransfer(tags, markOpenMissing)
		if err != nil {
			return domainProblem(err)
		}
		if len(transfer.MissingIDs) > 0 && !canApprove {
			return problem(http.StatusForbidden, domain.CodeTagApproval,
				fmt.Sprintf("%d open tag(s) would be marked MISSING; a supervisor (inventory.count.approve) must confirm", len(transfer.MissingIDs)))
		}
		sheetID := ids.New()
		number := "CNT-" + strings.ToUpper(strings.ReplaceAll(sheetID, "-", "")[20:])
		if _, err := tx.Exec(ctx, `insert into count_sheet (tenant_id, legal_entity_id, id, plan_id, number, status, idempotency_key)
			values ($1, $2, $3, $4, $5, 'OPEN', $6)`, tp.TenantID, plan.LegalEntityID, sheetID, planID, number, sheetKey); err != nil {
			return scopeProblem(err)
		}
		var usedIDs []string
		for i, ln := range transfer.Lines {
			hash, _, err := upsertDimensionSet(ctx, tx, tp.TenantID, ln.Dims)
			if err != nil {
				return err
			}
			var expected string
			if err := tx.QueryRow(ctx, `select coalesce((select (o.posted_qty + o.received + o.registered - o.deducted - o.picked)::text
				from on_hand o where o.tenant_id = $1 and o.legal_entity_id = $2 and o.product_id = $3 and o.dimension_set_hash = $4), '0')`,
				tp.TenantID, plan.LegalEntityID, ln.ProductID, hash).Scan(&expected); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into count_sheet_line (tenant_id, id, sheet_id, line_no, product_id, dimension_set_hash,
				expected_qty, counted_qty, counted_by, counted_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())`,
				tp.TenantID, ids.New(), sheetID, i+1, ln.ProductID, hash, expected, ln.Qty.String(), optionalText(tp.UserID)); err != nil {
				return err
			}
			usedIDs = append(usedIDs, ln.TagIDs...)
		}
		if len(transfer.MissingIDs) > 0 {
			if _, err := tx.Exec(ctx, `update count_tag set status = 'MISSING', version = version + 1
				where tenant_id = $1 and id = any($2::uuid[])`, tp.TenantID, transfer.MissingIDs); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update count_tag set sheet_id = $3, version = version + 1
			where tenant_id = $1 and id = any($2::uuid[])`, tp.TenantID, usedIDs, sheetID); err != nil {
			return err
		}
		out, err = loadCountSheet(ctx, tx, sheetID, false)
		return err
	})
	return out, replayed, err
}

func loadTagLocked(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.CountTag, error) {
	tags, err := loadTags(ctx, tx, tenantID, true, "t.id = $2", id)
	if err != nil {
		return domain.CountTag{}, err
	}
	if len(tags) == 0 {
		return domain.CountTag{}, problem(http.StatusNotFound, domain.CodeTagNotFound, "count tag not found")
	}
	return tags[0], nil
}

// loadTags selects the tenant's tags matching where, in number order. where
// refers to its own arguments from $2 on ($1 is the tenant).
func loadTags(ctx context.Context, tx pgx.Tx, tenantID string, lock bool, where string, args ...any) ([]domain.CountTag, error) {
	q := `select ` + tagColumns + ` from count_tag t where t.tenant_id = $1 and (` + where + `) order by t.seq`
	if lock {
		q += " for update"
	}
	rows, err := tx.Query(ctx, q, append([]any{tenantID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CountTag
	for rows.Next() {
		var t domain.CountTag
		var status, qty string
		var dims []byte
		if err := rows.Scan(&t.ID, &t.Seq, &t.Number, &t.PlanID, &status, &t.ProductID, &dims, &qty, &t.CountedBy, &t.SheetID, &t.Version); err != nil {
			return nil, err
		}
		t.Status = domain.TagStatus(status)
		if err := json.Unmarshal(dims, &t.Dims); err != nil {
			return nil, err
		}
		t.Qty = decimal.RequireFromString(qty)
		out = append(out, t)
	}
	return out, rows.Err()
}
