package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// BlockingInput is a request to block quantity of one product at unrestricted
// dimensions (FR-16.8). The idempotency key makes a retry return the first
// blocking.
type BlockingInput struct {
	IdempotencyKey string
	LegalEntityID  string
	ProductID      string
	Dims           domain.Dimensions
	Qty            decimal.Decimal
	Unit           string
	Target         string
	Reason         string
	Note           string
	PhysicalDate   time.Time
	// ExpectedReleaseDate is YYYY-MM-DD or empty; it cannot be before PhysicalDate.
	ExpectedReleaseDate string
}

// BlockingFilter selects blockings for the inquiry. Empty fields do not filter.
type BlockingFilter struct {
	State     string
	ProductID string
	Limit     int
}

// CreateBlocking blocks quantity in one transaction (FR-16.8, BR-16.10). It
// locks the source and blocked projection rows in ascending order (BR-16.4),
// requires the quantity to be available physical stock at the source
// (INV-BLK-003), and moves it as a valued issue / receipt pair: SOLD at the
// source at the running average and PURCHASED at the blocked dimensions for
// the same value. Physical quantity and value summed over the product do not
// change, avail_physical drops by the quantity, and both legs post to the
// inventory account, so there is no GL entry. A repeated key returns the
// stored blocking with replayed=true, or idempotency_key_reuse when the body
// differs.
func (s *Postgres) CreateBlocking(ctx context.Context, in BlockingInput) (domain.Blocking, bool, error) {
	if err := domain.CheckExpectedRelease(in.PhysicalDate.Format(dateLayout), in.ExpectedReleaseDate); err != nil {
		return domain.Blocking{}, false, domainProblem(err)
	}
	req := domain.Blocking{LegalEntityID: in.LegalEntityID, ProductID: in.ProductID, Dims: in.Dims, Qty: in.Qty, Unit: in.Unit, Target: in.Target, Reason: in.Reason}
	if err := domain.ValidateBlockRequest(req, s.rules.MaxScale); err != nil {
		return domain.Blocking{}, false, domainProblem(err)
	}
	src, _ := in.Dims.Normalize()
	tp, _ := tenant.FromContext(ctx)
	var out domain.Blocking
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "blocking", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from inventory_blocking where tenant_id = $1 and idempotency_key = $2`,
			tp.TenantID, in.IdempotencyKey).Scan(&existing)
		if err == nil {
			replayed = true
			if out, err = loadBlocking(ctx, tx, existing, false); err != nil {
				return err
			}
			if out.LegalEntityID != in.LegalEntityID || out.ProductID != in.ProductID || !out.Qty.Equal(in.Qty) || out.Target != in.Target ||
				out.Reason != in.Reason || out.Unit != in.Unit || out.ExpectedReleaseDate != in.ExpectedReleaseDate || out.Dims.Canonical() != src.Canonical() {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := ids.New()
		from := &row{product: in.ProductID, dims: src, hash: src.Hash()}
		blocked := domain.BlockedDims(src, in.Target)
		to := &row{product: in.ProductID, dims: blocked, hash: blocked.Hash()}
		doc := document{legalEntityID: in.LegalEntityID, date: in.PhysicalDate, sourceType: sourceBlocking, referenceID: id}
		if err := moveStock(ctx, tx, tp.TenantID, doc, from, to, in.Qty, in.Unit, func(avail decimal.Decimal) error {
			return domain.CheckBlockable(in.Qty, avail)
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into inventory_blocking (tenant_id, legal_entity_id, id, idempotency_key, product_id, dimension_set_hash,
			blocked_dimension_set_hash, qty, unit, target_status, reason, note, state, physical_date, created_by, expected_release_date)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'ACTIVE', $13, $14, nullif($15, '')::date)`,
			tp.TenantID, in.LegalEntityID, id, in.IdempotencyKey, in.ProductID, from.hash, to.hash, in.Qty.String(), in.Unit, in.Target,
			in.Reason, optionalText(in.Note), in.PhysicalDate.Format(dateLayout), optionalText(tp.UserID), in.ExpectedReleaseDate); err != nil {
			return scopeProblem(err)
		}
		if out, err = loadBlocking(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "blocking", id, invevents.BlockingCreated, tp.TenantID, blockingData(out))
	})
	return out, replayed, err
}

// ReleaseBlocking ends an active blocking with a disposition (FR-16.8). RELEASE
// moves the quantity back to unrestricted stock as a valued pair (SOLD at the
// blocked dimensions, PURCHASED at the source). SCRAP writes the stock off
// instead (scrapBlocking). The version must match (If-Match). A released
// blocking is returned unchanged, so a retry is harmless.
func (s *Postgres) ReleaseBlocking(ctx context.Context, id string, version int, disposition string, date time.Time, p PostParams) (domain.Blocking, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Blocking
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := loadBlocking(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if b.State == domain.BlockingReleased {
			out = b
			return nil
		}
		if b.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "blocking changed since it was read; reread and retry")
		}
		resolved, err := domain.ResolveRelease(b, disposition)
		if err != nil {
			return domainProblem(err)
		}
		scrapJournal := ""
		if resolved == domain.DispositionScrap {
			if scrapJournal, err = s.scrapBlocking(ctx, tx, tp.TenantID, b, date, p); err != nil {
				return err
			}
		} else {
			blocked := domain.BlockedDims(b.Dims, b.Target)
			from := &row{product: b.ProductID, dims: blocked, hash: blocked.Hash()}
			to := &row{product: b.ProductID, dims: b.Dims, hash: b.Dims.Hash()}
			doc := document{legalEntityID: b.LegalEntityID, date: date, sourceType: sourceBlocking, referenceID: b.ID}
			if err := moveStock(ctx, tx, tp.TenantID, doc, from, to, b.Qty, b.Unit, nil); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update inventory_blocking set state = 'RELEASED', disposition = $3, released_at = now(), released_by = $4,
			scrap_journal_id = nullif($5, '')::uuid, version = version + 1 where tenant_id = $1 and id = $2`,
			tp.TenantID, id, resolved, optionalText(tp.UserID), scrapJournal); err != nil {
			return err
		}
		if out, err = loadBlocking(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "blocking", id, invevents.BlockingReleased, tp.TenantID, blockingData(out))
	})
	return out, err
}

// scrapBlocking writes a blocking's quantity off (disposition SCRAP, FR-16.8):
// it creates and posts, in the caller's transaction, an ADJUSTMENT journal
// ("blocking-scrap:<id>", dated date) with one issue line at the blocked
// dimensions. The issue is valued at the running average of the blocked row,
// so the GL entry credits the inventory account and debits the adjustment
// account by exactly the value the blocking carried, and the inventory-to-
// ledger reconciliation sees it like any posted journal. It returns the
// journal id.
//
// The negative policy is always forbid, whatever the service default: like
// release and escalation (moveStock), a scrap only writes off stock the
// blocked row still has; it never drives the row negative or values a
// shortfall at a fallback cost.
func (s *Postgres) scrapBlocking(ctx context.Context, tx pgx.Tx, tenantID string, b domain.Blocking, date time.Time, p PostParams) (string, error) {
	p.Negative = domain.NegativePolicy{}
	blocked := domain.BlockedDims(b.Dims, b.Target)
	var currency *string
	err := tx.QueryRow(ctx, `select currency::text from on_hand where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and dimension_set_hash = $4`,
		tenantID, b.LegalEntityID, b.ProductID, blocked.Hash()).Scan(&currency)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if currency == nil {
		return "", problem(http.StatusUnprocessableEntity, domain.CodeNegativePhysical,
			fmt.Sprintf("no valued stock of product %s at %s to scrap", b.ProductID, describeDims(blocked)))
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into inventory_journal (tenant_id, legal_entity_id, id, journal_type, status, posting_date, currency, idempotency_key)
		values ($1, $2, $3, 'ADJUSTMENT', 'DRAFT', $4, $5, $6)`, tenantID, b.LegalEntityID, id, date.Format(dateLayout), *currency,
		"blocking-scrap:"+b.ID); err != nil {
		return "", scopeProblem(err)
	}
	hash, raw, err := upsertDimensionSet(ctx, tx, tenantID, blocked)
	if err != nil {
		return "", err
	}
	qty := b.Qty.Neg()
	if _, err := tx.Exec(ctx, `insert into inventory_journal_line (tenant_id, journal_id, id, line_no, product_id, dimension_set_hash, dims, qty, entered_qty, entered_unit, cost_price)
		values ($1, $2, $3, 1, $4, $5, $6, $7, $8, $9, 0)`, tenantID, id, ids.New(), b.ProductID, hash, raw, qty.String(), qty.String(), b.Unit); err != nil {
		return "", err
	}
	j, err := loadJournal(ctx, tx, id, true)
	if err != nil {
		return "", err
	}
	return id, s.postLoaded(ctx, tx, tenantID, j, p, "")
}

// EscalateBlocking moves an active quarantine to blocked (the QUARANTINE to
// BLOCKED transition of FR-16.8): the quantity moves from the quarantine
// dimensions to the same dimensions with inventory_status BLOCKED as a valued
// issue / receipt pair, and the blocking keeps its identity with target BLOCKED
// and a higher version (If-Match). It emits blocking.escalated.
func (s *Postgres) EscalateBlocking(ctx context.Context, id string, version int, date time.Time) (domain.Blocking, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Blocking
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		b, err := loadBlocking(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if b.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "blocking changed since it was read; reread and retry")
		}
		if err := domain.CheckEscalate(b); err != nil {
			return domainProblem(err)
		}
		fromDims := domain.BlockedDims(b.Dims, b.Target)
		toDims := domain.BlockedDims(b.Dims, domain.StockBlocked)
		from := &row{product: b.ProductID, dims: fromDims, hash: fromDims.Hash()}
		to := &row{product: b.ProductID, dims: toDims, hash: toDims.Hash()}
		doc := document{legalEntityID: b.LegalEntityID, date: date, sourceType: sourceBlocking, referenceID: b.ID}
		if err := moveStock(ctx, tx, tp.TenantID, doc, from, to, b.Qty, b.Unit, nil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update inventory_blocking set target_status = 'BLOCKED', blocked_dimension_set_hash = $3, escalated_at = now(),
			escalated_by = $4, version = version + 1 where tenant_id = $1 and id = $2`, tp.TenantID, id, to.hash, optionalText(tp.UserID)); err != nil {
			return err
		}
		if out, err = loadBlocking(ctx, tx, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "blocking", id, invevents.BlockingEscalated, tp.TenantID, blockingData(out))
	})
	return out, err
}

// moveStock moves qty of one product from one dimension set to another of the
// same legal entity as a valued SOLD issue and PURCHASED receipt. Both rows are
// locked in rowKey order; check, when set, sees the source's available
// physical quantity before the move. The receipt takes the issue's value at
// the source's running average, so the two legs cancel on the inventory
// account and the value follows the stock.
func moveStock(ctx context.Context, tx pgx.Tx, tenantID string, doc document, from, to *row, qty decimal.Decimal, unit string, check func(avail decimal.Decimal) error) error {
	first, second := from, to
	if rowKey(to.product, to.hash) < rowKey(from.product, from.hash) {
		first, second = to, from
	}
	for _, r := range []*row{first, second} {
		if err := lockRow(ctx, tx, tenantID, doc.legalEntityID, "", r); err != nil {
			return err
		}
	}
	if err := checkCountFreeze(ctx, tx, tenantID, doc.legalEntityID, "", true, []*row{first, second}); err != nil {
		return err
	}
	if check != nil {
		if err := check(from.oh.AvailPhysical()); err != nil {
			return domainProblem(err)
		}
	}
	if from.currency == "" {
		return problem(http.StatusUnprocessableEntity, domain.CodeNegativePhysical,
			fmt.Sprintf("no valued stock of product %s at %s", from.product, describeDims(from.dims)))
	}
	if to.currency != "" && to.currency != from.currency {
		return problem(http.StatusUnprocessableEntity, CodeCurrencyMismatch,
			fmt.Sprintf("on-hand for product %s at %s is valued in %s, the source in %s", to.product, describeDims(to.dims), to.currency, from.currency))
	}
	doc.currency = from.currency
	value := from.oh.IssueValue(qty.Neg(), decimal.Zero, doc.currency)
	issue := domain.PostedLine{Status: domain.StatusSold, Value: value,
		Line: domain.JournalLine{ProductID: from.product, Dims: from.dims, Qty: qty.Neg(), EnteredQty: qty.Neg(), EnteredUnit: unit}}
	receipt := domain.PostedLine{Status: domain.StatusPurchased, Value: value.Neg(),
		Line: domain.JournalLine{ProductID: to.product, Dims: to.dims, Qty: qty, EnteredQty: qty, EnteredUnit: unit}}
	if err := from.oh.Post(issue.Status, issue.Line.Qty, issue.Value, 1); err != nil {
		return err
	}
	if err := to.oh.Post(receipt.Status, receipt.Line.Qty, receipt.Value, 1); err != nil {
		return err
	}
	if err := checkNegative(from, domain.NegativePolicy{}); err != nil {
		return err
	}
	to.currency = from.currency
	for _, leg := range []struct {
		r  *row
		pl domain.PostedLine
	}{{from, issue}, {to, receipt}} {
		if err := saveRow(ctx, tx, tenantID, doc.legalEntityID, leg.r, 1); err != nil {
			return err
		}
		if _, err := writeTransaction(ctx, tx, tenantID, doc, ids.New(), leg.r, leg.pl); err != nil {
			return err
		}
		if err := enqueueOnHand(ctx, tx, tenantID, doc.legalEntityID, leg.r, invevents.BucketPosted, leg.pl.Line.Qty); err != nil {
			return err
		}
	}
	return guardClaimedStock(ctx, tx, tenantID, doc.legalEntityID, []*row{from, to}, nil)
}

// ListBlockings returns blockings newest first, filtered by state and product.
func (s *Postgres) ListBlockings(ctx context.Context, f BlockingFilter) ([]domain.Blocking, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	tp, _ := tenant.FromContext(ctx)
	out := []domain.Blocking{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+blockingColumns+` from inventory_blocking b
			join dimension_set d on d.tenant_id = b.tenant_id and d.hash = b.dimension_set_hash
			where b.tenant_id = $1 and ($2 = '' or b.state = $2) and ($3 = '' or b.product_id::text = $3)
			order by b.created_at desc, b.id desc limit $4`, tp.TenantID, f.State, f.ProductID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBlocking(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

const blockingColumns = `b.id::text, b.legal_entity_id::text, b.product_id::text, d.dims, b.qty::text, b.unit, b.target_status, b.reason,
	coalesce(b.note, ''), b.state, coalesce(b.disposition, ''), to_char(b.physical_date, 'YYYY-MM-DD'), b.version,
	coalesce(to_char(b.expected_release_date, 'YYYY-MM-DD'), ''), coalesce(b.scrap_journal_id::text, '')`

func scanBlocking(row pgx.Row) (domain.Blocking, error) {
	var b domain.Blocking
	var dims []byte
	var qty string
	if err := row.Scan(&b.ID, &b.LegalEntityID, &b.ProductID, &dims, &qty, &b.Unit, &b.Target, &b.Reason, &b.Note, &b.State, &b.Disposition,
		&b.PhysicalDate, &b.Version, &b.ExpectedReleaseDate, &b.ScrapJournalID); err != nil {
		return b, err
	}
	if err := json.Unmarshal(dims, &b.Dims); err != nil {
		return b, err
	}
	b.Qty = decimal.RequireFromString(qty)
	return b, nil
}

// loadBlocking reads one blocking with its source dimensions; forUpdate locks it.
func loadBlocking(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domain.Blocking, error) {
	tp, _ := tenant.FromContext(ctx)
	q := `select ` + blockingColumns + ` from inventory_blocking b
		join dimension_set d on d.tenant_id = b.tenant_id and d.hash = b.dimension_set_hash
		where b.tenant_id = $1 and b.id = $2`
	if forUpdate {
		q += ` for update of b`
	}
	b, err := scanBlocking(tx.QueryRow(ctx, q, tp.TenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, problem(http.StatusNotFound, domain.CodeBlockingNotFound, "blocking not found")
	}
	return b, err
}

// blockingData is the event payload of both blocking events.
func blockingData(b domain.Blocking) invevents.BlockingData {
	return invevents.BlockingData{BlockingID: b.ID, LegalEntityID: b.LegalEntityID, ProductID: b.ProductID, DimensionSet: map[string]string(b.Dims),
		Qty: b.Qty.String(), TargetStatus: b.Target, Reason: b.Reason, State: b.State, Disposition: optionalText(b.Disposition),
		ExpectedReleaseDate: optionalText(b.ExpectedReleaseDate), ScrapJournalID: optionalText(b.ScrapJournalID)}
}

// optionalInt stores zero as NULL.
func optionalInt(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
