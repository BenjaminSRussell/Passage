package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

type IssueClaimInput struct {
	IdempotencyKey string
	LegalEntityID  string
	ProductID      string
	Warehouse      string
	Batch          string
	Kind           string
	Qty            decimal.Decimal
	// IssueRef names the document line the movement issues units for (see
	// platform/issueclaim: a sales order line, a production order item).
	// Claims that share a ref are successive steps of the same units and
	// count once; empty means the claim counts on its own.
	IssueRef string
}

// ClaimIssue records that a service is about to move qty of a product out of
// a warehouse (a pick, a shipment or a production consumption) and refuses
// the claim when it would reach stock a hold still keeps. The on-hand rows of
// the product are locked first, then the issue scope of the warehouse, so the
// held quantity read is the one a concurrent hold placement has committed and
// the claims and inventory issues read are those an earlier claim or posting
// has committed. Claims of every kind stand in, together, for the issues
// inventory has not booked: claims that share a ref (a pick and the ship of
// what was picked) count as the largest per-kind total of the ref, and claims
// with no ref count in full, each less what inventory has booked for them since
// (ISSUED, see settleIssue). With any quantity held, what is left free
// after the claim may not be below zero (domain.CheckIssueClaim), so the ship
// of picked units is refused while a hold placed after the pick covers them.
// Restricted (BLOCKED, QUARANTINE) rows are not free stock. The rule holds
// over the whole warehouse and, for a claim that names a batch, within that
// batch too. A repeated key is a replay.
func (s *Postgres) ClaimIssue(ctx context.Context, in IssueClaimInput) (replayed bool, err error) {
	if strings.HasPrefix(in.IdempotencyKey, issuedKeyPrefix) {
		return false, problem(http.StatusUnprocessableEntity, domain.CodeInvalid, "Idempotency-Key must not start with "+issuedKeyPrefix)
	}
	tp, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "issue-claim", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		prior, found, err := loadIssueClaim(ctx, tx, tp.TenantID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if prior.LegalEntityID != in.LegalEntityID || prior.ProductID != in.ProductID || prior.Warehouse != in.Warehouse ||
				prior.Batch != in.Batch || prior.Kind != in.Kind || !prior.Qty.Equal(in.Qty) || prior.IssueRef != in.IssueRef {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			replayed = true
			return nil
		}
		rows, err := lockProductRows(ctx, tx, tp.TenantID, in.LegalEntityID, in.ProductID)
		if err != nil {
			return err
		}
		sc := issueScope{product: in.ProductID, warehouse: in.Warehouse}
		if err := lockIssueScope(ctx, tx, tp.TenantID, in.LegalEntityID, sc); err != nil {
			return err
		}
		// The warehouse is always checked: a claim that names a batch with no
		// hold of its own must still not reach held stock of another batch
		// (more than its batch has, or what batchless claims already took).
		// A batch claim is checked within its batch as well.
		batches := []string{""}
		if in.Batch != "" {
			batches = append(batches, in.Batch)
		}
		// Inserted first, so the outstanding quantity read counts the claim
		// under its ref; a refusal rolls it back.
		if _, err := tx.Exec(ctx, `insert into inventory_issue_claim (tenant_id, legal_entity_id, idempotency_key, product_id, warehouse, batch, kind, qty, issue_ref)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			tp.TenantID, in.LegalEntityID, in.IdempotencyKey, in.ProductID, in.Warehouse, in.Batch, in.Kind, in.Qty.String(), in.IssueRef); err != nil {
			return scopeProblem(err)
		}
		for _, b := range batches {
			after, err := outstandingClaims(ctx, tx, tp.TenantID, in.LegalEntityID, sc, b)
			if err != nil {
				return err
			}
			physical, held := scopeStock(rows, in.Warehouse, b)
			if err := domain.CheckIssueClaim(physical, held, after); err != nil {
				return domainProblem(err)
			}
		}
		return nil
	})
	return replayed, err
}

// ReleaseIssueClaim withdraws a claim whose movement did not commit. An
// unknown key is already released; what inventory booked is not a claim.
func (s *Postgres) ReleaseIssueClaim(ctx context.Context, key string) error {
	tp, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "issue-claim", tp.TenantID, key); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `delete from inventory_issue_claim where tenant_id = $1 and idempotency_key = $2 and kind <> $3`,
			tp.TenantID, key, domain.IssueBooked)
		return err
	})
}

// IssueRoom is the hold rule's view of one product in one warehouse (and
// batch, when set): unrestricted physical and held stock, and the claimed
// quantity inventory has not booked as an issue.
type IssueRoom struct {
	Physical, Held, Outstanding decimal.Decimal
}

// Free is what may still move while a hold exists.
func (r IssueRoom) Free() decimal.Decimal { return r.Physical.Sub(r.Held).Sub(r.Outstanding) }

// GetIssueRoom reads the issue room of a product in a warehouse and optional
// batch, as ClaimIssue weighs it.
func (s *Postgres) GetIssueRoom(ctx context.Context, legalEntityID, productID, warehouse, batch string) (IssueRoom, error) {
	tp, _ := tenant.FromContext(ctx)
	var out IssueRoom
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := productRows(ctx, tx, tp.TenantID, legalEntityID, productID, "")
		if err != nil {
			return err
		}
		out.Physical, out.Held = scopeStock(rows, warehouse, batch)
		out.Outstanding, err = outstandingClaims(ctx, tx, tp.TenantID, legalEntityID, issueScope{product: productID, warehouse: warehouse}, batch)
		return err
	})
	return out, err
}

func loadIssueClaim(ctx context.Context, tx pgx.Tx, tenantID, key string) (IssueClaimInput, bool, error) {
	in := IssueClaimInput{IdempotencyKey: key}
	var qty string
	err := tx.QueryRow(ctx, `select legal_entity_id::text, product_id::text, warehouse, batch, kind, qty::text, issue_ref
		from inventory_issue_claim where tenant_id = $1 and idempotency_key = $2`, tenantID, key).
		Scan(&in.LegalEntityID, &in.ProductID, &in.Warehouse, &in.Batch, &in.Kind, &qty, &in.IssueRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return in, false, nil
	}
	if err != nil {
		return in, false, fmt.Errorf("load issue claim: %w", err)
	}
	in.Qty = decimal.RequireFromString(qty)
	return in, true, nil
}

// issueScope is the product and warehouse claims and holds are weighed in.
type issueScope struct{ product, warehouse string }

// lockIssueScope serialises the claims and the inventory issues of one
// product in one warehouse. Every caller has taken the row locks it needs
// before and takes no row lock after, so the lock never closes a cycle; a
// caller with several scopes takes them in issueScope order.
func lockIssueScope(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, sc issueScope) error {
	return lockKey(ctx, tx, "issue-scope", tenantID, legalEntityID+"|"+sc.product+"|"+sc.warehouse)
}

// scopeStock sums physical and held stock over the unrestricted rows of a
// warehouse, or of one batch in it.
func scopeStock(rows map[string]*row, warehouse, batch string) (physical, held decimal.Decimal) {
	for _, r := range rows {
		if r.dims[domain.DimWarehouse] != warehouse || (batch != "" && r.dims[domain.DimBatch] != batch) ||
			domain.IsRestricted(r.dims[domain.DimStatus]) {
			continue
		}
		physical, held = physical.Add(r.oh.PhysicalInvent()), held.Add(r.oh.HeldPhysical)
	}
	return physical, held
}

// issuedKeyPrefix starts the key of every settlement row; ClaimIssue refuses a
// caller key that starts with it, so a claim can never occupy a settlement's key.
const issuedKeyPrefix = "issued:"

// groupOpenSQL is, per ref group, what its claims stand for less what inventory
// booked for it, never below zero: the largest per-kind total of a ref, or the
// full total of the claims without a ref. claimFilter narrows the claims and
// issuedFilter the settlements (both start with " and ", or are empty).
func groupOpenSQL(claimFilter, issuedFilter string) string {
	return `greatest(0, case when issue_ref = '' then coalesce(sum(qty) filter (where kind <> 'ISSUED'` + claimFilter + `), 0)
		else greatest(sum(qty) filter (where kind = 'PICK'` + claimFilter + `), sum(qty) filter (where kind = 'SHIP'` + claimFilter + `),
			sum(qty) filter (where kind = 'CONSUME'` + claimFilter + `), 0) end
		- coalesce(sum(qty) filter (where kind = 'ISSUED'` + issuedFilter + `), 0))`
}

// outstandingClaims is the claimed quantity of a scope that inventory has not
// booked as an issue: the claims without a ref in full, plus per ref the
// largest per-kind total, each less what was booked for it. Within a batch, claims of the
// batch and batchless claims count (a batchless claim may have taken any
// batch), and a ref counts no more than it does over the whole warehouse, so
// a ref settled by an issue of another batch does not linger in this one.
func outstandingClaims(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, sc issueScope, batch string) (decimal.Decimal, error) {
	var out string
	err := tx.QueryRow(ctx, `with c as (
			select issue_ref, kind, batch, qty, ($5::text = '' or batch = '' or batch = $5::text) as in_batch from inventory_issue_claim
			where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and warehouse = $4),
		r as (
			select `+groupOpenSQL("", "")+` as w, `+groupOpenSQL(" and in_batch", " and ($5::text = '' or batch = $5::text)")+` as b
			from c group by issue_ref)
		select coalesce(sum(least(w, b)), 0)::text from r`,
		tenantID, legalEntityID, sc.product, sc.warehouse, batch).Scan(&out)
	if err != nil {
		return decimal.Zero, fmt.Errorf("outstanding issue claims: %w", err)
	}
	return decimal.RequireFromString(out), nil
}

// issueSettlement names the document line an inventory issue is booked for
// (the postings consumer's DELIVERED booking of a sales order line, ref as
// platform/issueclaim forms it; empty for a journal) and the issue's key: the
// issue settles the claims that stood in for it until it was booked (settleIssue).
type issueSettlement struct {
	ref, key string
}

// guardClaimedStock extends the hold rule to inventory's own issues (journal
// postings other than counts, transactions:apply and status steps, transfer
// shipments, blockings). moved are the rows a posting changed, already saved,
// with r.before the state read under their locks. For each product and
// warehouse whose unrestricted physical stock fell, it takes the issue scope
// lock, books what settle settles (no more than the ref still had
// outstanding, so an issue never settles claims taken after it) and refuses
// with QUALITY_HOLD when the fall, net of what it settled, would leave less
// than the held quantity once the outstanding claims are counted
// (domain.CheckIssueRoom), over the warehouse and within each batch that fell.
// The rows of the product it did not move are read unlocked: claims and issues
// of the scope are serialised by the scope lock, receipts only add free stock,
// and a hold placed meanwhile is checked against the row it holds.
func guardClaimedStock(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, moved []*row, settle *issueSettlement) error {
	falls := map[issueScope]map[string]decimal.Decimal{}
	issued := map[issueScope]map[string]decimal.Decimal{}
	for _, r := range moved {
		wh := r.dims[domain.DimWarehouse]
		if wh == "" || domain.IsRestricted(r.dims[domain.DimStatus]) {
			continue
		}
		d := r.oh.PhysicalInvent().Sub(r.before.PhysicalInvent())
		if d.IsZero() {
			continue
		}
		sc := issueScope{product: r.product, warehouse: wh}
		if falls[sc] == nil {
			falls[sc] = map[string]decimal.Decimal{}
		}
		falls[sc][""] = falls[sc][""].Add(d)
		if b := r.dims[domain.DimBatch]; b != "" {
			falls[sc][b] = falls[sc][b].Add(d)
		}
		if settle != nil && d.IsNegative() {
			if issued[sc] == nil {
				issued[sc] = map[string]decimal.Decimal{}
			}
			issued[sc][r.dims[domain.DimBatch]] = issued[sc][r.dims[domain.DimBatch]].Sub(d)
		}
	}
	scopes := make([]issueScope, 0, len(falls))
	for sc, m := range falls {
		for _, d := range m {
			if d.IsNegative() {
				scopes = append(scopes, sc)
				break
			}
		}
	}
	slices.SortFunc(scopes, func(a, b issueScope) int {
		if c := strings.Compare(a.product, b.product); c != 0 {
			return c
		}
		return strings.Compare(a.warehouse, b.warehouse)
	})
	for _, sc := range scopes {
		if err := lockIssueScope(ctx, tx, tenantID, legalEntityID, sc); err != nil {
			return err
		}
	}
	for _, sc := range scopes {
		var batches []string
		for b, d := range falls[sc] {
			if d.IsNegative() {
				batches = append(batches, b)
			}
		}
		slices.Sort(batches)
		before := make([]decimal.Decimal, len(batches))
		var err error
		for i, b := range batches {
			if before[i], err = outstandingClaims(ctx, tx, tenantID, legalEntityID, sc, b); err != nil {
				return err
			}
		}
		for _, b := range slices.Sorted(maps.Keys(issued[sc])) {
			if err := settleIssue(ctx, tx, tenantID, legalEntityID, sc, b, issued[sc][b], *settle); err != nil {
				return err
			}
		}
		after := make([]decimal.Decimal, len(batches))
		claimed := false
		for i, b := range batches {
			if after[i], err = outstandingClaims(ctx, tx, tenantID, legalEntityID, sc, b); err != nil {
				return err
			}
			claimed = claimed || before[i].IsPositive() || after[i].IsPositive()
		}
		if !claimed {
			// Without claims the rule is the row's own (CheckNegative keeps
			// every row at or above its held quantity), already applied.
			continue
		}
		rows, err := productRows(ctx, tx, tenantID, legalEntityID, sc.product, "")
		if err != nil {
			return err
		}
		for i, b := range batches {
			physical, held := scopeStock(rows, sc.warehouse, b)
			if err := domain.CheckIssueRoom(physical.Sub(falls[sc][b]).Sub(held).Sub(before[i]), physical, held, after[i]); err != nil {
				return domainProblem(err)
			}
		}
	}
	return nil
}

// settleIssue books what an issue of qty (from one batch, or none) settles of
// the claims of a scope, each group capped by what it has outstanding over the
// warehouse, so stock issued without a claim never absorbs a later claim. The
// issue settles the group of its own ref, and the picks that carry no ref (no
// caller sets one on a warehouse pick): both stood in for the units it moved.
// Ships and consumptions always carry their ref, so a claim of those kinds
// without one keeps reserving its units until released. An issue that names no
// ref (a journal) settles on, in the order of the refs, the groups that hold a
// CONSUME claim, up to qty: consumption reaches inventory as a journal that
// cannot name the production item.
func settleIssue(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, sc issueScope, batch string, qty decimal.Decimal, settle issueSettlement) error {
	groups, err := tx.Query(ctx, `select issue_ref, `+groupOpenSQL("", "")+`::text, bool_or(kind = 'CONSUME'),
		greatest(0, coalesce(sum(qty) filter (where kind = 'PICK'), 0) - coalesce(sum(qty) filter (where kind = 'ISSUED'), 0))::text from inventory_issue_claim
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and warehouse = $4 group by issue_ref order by issue_ref collate "C"`,
		tenantID, legalEntityID, sc.product, sc.warehouse)
	if err != nil {
		return fmt.Errorf("outstanding claims of the scope: %w", err)
	}
	type group struct {
		ref     string
		open    decimal.Decimal
		consume bool
		picks   decimal.Decimal
	}
	var open []group
	for groups.Next() {
		var g group
		var raw, picks string
		if err := groups.Scan(&g.ref, &raw, &g.consume, &picks); err != nil {
			groups.Close()
			return fmt.Errorf("outstanding claims of the scope: %w", err)
		}
		g.open, g.picks = decimal.RequireFromString(raw), decimal.RequireFromString(picks)
		open = append(open, g)
	}
	groups.Close()
	if err := groups.Err(); err != nil {
		return fmt.Errorf("outstanding claims of the scope: %w", err)
	}
	rest := qty
	for _, g := range open {
		var settled decimal.Decimal
		switch {
		case g.ref == "":
			settled = decimal.Min(qty, g.picks)
		case g.ref == settle.ref:
			settled = decimal.Min(qty, g.open)
		case settle.ref == "" && g.consume:
			settled = decimal.Min(rest, g.open)
			rest = rest.Sub(settled)
		}
		if !settled.IsPositive() {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into inventory_issue_claim (tenant_id, legal_entity_id, idempotency_key, product_id, warehouse, batch, kind, qty, issue_ref)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict do nothing`,
			tenantID, legalEntityID, issuedKeyPrefix+settle.key+"|"+batch+"|"+g.ref, sc.product, sc.warehouse, batch, domain.IssueBooked, settled.String(), g.ref); err != nil {
			return scopeProblem(err)
		}
	}
	return nil
}
