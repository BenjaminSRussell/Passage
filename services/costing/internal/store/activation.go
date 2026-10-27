package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

const (
	CodeCostNotFound       = "CST-COST-001"
	CodeCopySourceEmpty    = "CST-COPY-002"
	CodeCopySameVersion    = "CST-COPY-003"
	CodeCopyIncompatible   = "CST-COPY-004"
	CodeConflictVersion    = "conflict_version"
	CodeActivationConflict = "CST-ACT-008"
	// MaxPageSize bounds every list read.
	MaxPageSize  = 200
	eventSource  = "/passage/costing"
	costAggType  = "item_cost"
	activateLock = "costing.activate"
)

// OnHandQty is the physical on-hand of one product x site at activation
// (BR-20.7) and, where it has no active standard yet, its current book value.
type OnHandQty struct {
	Qty       decimal.Decimal
	BookValue *decimal.Decimal
}

// OnHand maps OnHandKey(product, site) to its on-hand.
type OnHand map[string]OnHandQty

// OnHandKey is the map key for a product x site in an OnHand map.
func OnHandKey(productID, siteID string) string { return productID + "|" + siteID }

// ActivationResult is the outcome of an activation or a preview of one. Plan is
// the revaluation the activation posts (or would post, or did post when
// AlreadyActive replays a version activated earlier).
type ActivationResult struct {
	Version       CostingVersion
	AlreadyActive bool
	Plan          domain.ActivationPlan
}

// CostLookup is the standard cost of one product x site in force on a date
// (consumed by waves 21 and 27).
type CostLookup struct {
	ProductID     string    `json:"productId"`
	SiteID        string    `json:"siteId"`
	UnitCost      string    `json:"unitCost"`
	Currency      string    `json:"currency"`
	VersionID     string    `json:"versionId"`
	VersionCode   string    `json:"versionCode"`
	EffectiveFrom string    `json:"effectiveFrom"`
	ActivatedAt   time.Time `json:"activatedAt"`
}

// VersionFilter narrows ListVersions; empty fields do not filter.
type VersionFilter struct {
	LegalEntityID string
	Status        string
	Type          string
}

// VersionPage is one page of versions ordered by code then id. Next is the
// cursor for the following page, empty on the last one.
type VersionPage struct {
	Items []CostingVersion
	Next  string
}

// ItemCostRow is one item cost as the version screen shows it (FR-20.5
// "compare to active"): its own cost and status, the standard cost currently
// active for the product x site in another version, and what activation
// recorded once the row was activated.
type ItemCostRow struct {
	ProductID         string     `json:"productId"`
	SiteID            string     `json:"siteId"`
	UnitCost          string     `json:"unitCost"`
	Status            string     `json:"status"`
	Source            string     `json:"source"`
	ActiveCost        *string    `json:"activeCost,omitempty"`
	PreviousUnitCost  *string    `json:"previousUnitCost,omitempty"`
	BookValue         *string    `json:"bookValue,omitempty"`
	OnHandQty         *string    `json:"onHandQty,omitempty"`
	RevaluationAmount *string    `json:"revaluationAmount,omitempty"`
	ActivatedAt       *time.Time `json:"activatedAt,omitempty"`
}

// ItemCostPage is one page of item costs ordered by site then product.
type ItemCostPage struct {
	Items []ItemCostRow
	Next  string
}

// costChanged is the payload of dev.passage.costing.cost.changed.v1 (3.20.5).
// oldCost is absent when the product x site had no active standard.
type costChanged struct {
	ProductID         string  `json:"productId"`
	SiteID            string  `json:"siteId"`
	VersionID         string  `json:"versionId"`
	VersionCode       string  `json:"versionCode"`
	Currency          string  `json:"currency"`
	OldCost           *string `json:"oldCost,omitempty"`
	NewCost           string  `json:"newCost"`
	EffectiveFrom     string  `json:"effectiveFrom"`
	OnHandQty         string  `json:"onHandQty"`
	RevaluationAmount string  `json:"revaluationAmount"`
}

func versionNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "costing version not found")
}

// ListVersions returns versions visible to the caller (tenant and legal-entity
// scope apply through RLS), ordered by code then id. after is "code|id".
func (s *Postgres) ListVersions(ctx context.Context, f VersionFilter, after string, limit int) (VersionPage, error) {
	p, _ := tenant.FromContext(ctx)
	afterCode, afterID, _ := strings.Cut(after, "|")
	var page VersionPage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+versionColumns+`,
				(select count(*) from item_cost ic where ic.tenant_id = v.tenant_id and ic.version_id = v.id)
			from costing_version v
			where v.tenant_id = $1
			  and ($2 = '' or v.legal_entity_id::text = $2)
			  and ($3 = '' or v.status = $3)
			  and ($4 = '' or v.type = $4)
			  and (not $5::boolean or (v.code, v.id) > ($6::text, $7::uuid))
			order by v.code, v.id
			limit $8`, p.TenantID, f.LegalEntityID, f.Status, f.Type, after != "", afterCode, nullUUID(afterID), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int
			v, err := scanVersion(rows, &n)
			if err != nil {
				return err
			}
			v.ItemCount = n
			page.Items = append(page.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		return VersionPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items, page.Next = page.Items[:limit], last.Code+"|"+last.ID
	}
	if page.Items == nil {
		page.Items = []CostingVersion{}
	}
	return page, nil
}

// GetVersion returns one version with its item count.
func (s *Postgres) GetVersion(ctx context.Context, id string) (CostingVersion, error) {
	p, _ := tenant.FromContext(ctx)
	var v CostingVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		v, err = getVersion(ctx, tx, p.TenantID, id, "")
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*) from item_cost where tenant_id = $1 and version_id = $2`, p.TenantID, id).Scan(&v.ItemCount)
	})
	return v, err
}

// getVersion reads one version inside tx; lock is "" or a locking clause.
func getVersion(ctx context.Context, tx pgx.Tx, tenantID, id, lock string) (CostingVersion, error) {
	v, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2 `+lock, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CostingVersion{}, versionNotFound()
	}
	return v, err
}

// ListItemCosts returns a page of a version's item costs, each with the
// standard cost active for its product x site in another version. after is
// "siteId|productId".
func (s *Postgres) ListItemCosts(ctx context.Context, versionID, after string, limit int) (ItemCostPage, error) {
	p, _ := tenant.FromContext(ctx)
	afterSite, afterProduct, _ := strings.Cut(after, "|")
	var page ItemCostPage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getVersion(ctx, tx, p.TenantID, versionID, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select ic.product_id::text, ic.site_id::text, ic.unit_cost::text, ic.status, ic.source,
				act.unit_cost::text, ic.previous_unit_cost::text, ic.book_value::text, ic.on_hand_qty::text,
				ic.revaluation_amount::text, ic.activated_at
			from item_cost ic
			left join item_cost act on act.tenant_id = ic.tenant_id and act.product_id = ic.product_id
				and act.site_id = ic.site_id and act.status = 'ACTIVE' and act.version_id <> ic.version_id
			where ic.tenant_id = $1 and ic.version_id = $2
			  and (not $3::boolean or (ic.site_id, ic.product_id) > ($4::uuid, $5::uuid))
			order by ic.site_id, ic.product_id
			limit $6`, p.TenantID, versionID, after != "", nullUUID(afterSite), nullUUID(afterProduct), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ItemCostRow
			if err := rows.Scan(&r.ProductID, &r.SiteID, &r.UnitCost, &r.Status, &r.Source, &r.ActiveCost, &r.PreviousUnitCost,
				&r.BookValue, &r.OnHandQty, &r.RevaluationAmount, &r.ActivatedAt); err != nil {
				return err
			}
			r.UnitCost = normalize(r.UnitCost)
			for _, f := range []*string{r.ActiveCost, r.PreviousUnitCost, r.BookValue, r.OnHandQty, r.RevaluationAmount} {
				if f != nil {
					*f = normalize(*f)
				}
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	if err != nil {
		return ItemCostPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items, page.Next = page.Items[:limit], last.SiteID+"|"+last.ProductID
	}
	if page.Items == nil {
		page.Items = []ItemCostRow{}
	}
	return page, nil
}

func nullUUID(s string) string {
	if s == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return s
}

// normalize drops the trailing zeros numeric(…, 6) text carries ("10.500000" → "10.5").
func normalize(s string) string {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return s
	}
	return d.String()
}

// CopyItemCosts copies every item cost of sourceID into the DRAFT targetID,
// moving each cost by percent (FR-20.5 copy with percent change). Source and
// target must be different versions of the same legal entity and currency. A
// repeated copy replaces the pending costs, so it is idempotent; it bumps the
// target's optimistic version like an import.
func (s *Postgres) CopyItemCosts(ctx context.Context, targetID, sourceID string, percent decimal.Decimal) (int, error) {
	p, _ := tenant.FromContext(ctx)
	if targetID == sourceID {
		return 0, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCopySameVersion, "copy from another costing version, not from the target itself")
	}
	n := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		target, err := getVersion(ctx, tx, p.TenantID, targetID, "for update")
		if err != nil {
			return err
		}
		if target.Status != "DRAFT" {
			return httpx.NewProblem(http.StatusConflict, CodeVersionNotDraft, "item costs can only be copied into a DRAFT version")
		}
		source, err := getVersion(ctx, tx, p.TenantID, sourceID, "")
		var prob *httpx.Problem
		if errors.As(err, &prob) && prob.Code == CodeVersionNotFound {
			return httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "source costing version not found")
		}
		if err != nil {
			return err
		}
		if source.LegalEntityID != target.LegalEntityID || source.Currency != target.Currency {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCopyIncompatible,
				"the source version must belong to the same legal entity and use the same currency as the target")
		}
		rows, err := tx.Query(ctx, `select product_id::text, site_id::text, unit_cost::text from item_cost
			where tenant_id = $1 and version_id = $2 order by site_id, product_id`, p.TenantID, sourceID)
		if err != nil {
			return err
		}
		var rowIDs, products, sites, costs []string
		for rows.Next() {
			var product, site, raw string
			if err := rows.Scan(&product, &site, &raw); err != nil {
				rows.Close()
				return err
			}
			cost, err := decimal.NewFromString(raw)
			if err != nil {
				rows.Close()
				return err
			}
			moved, err := domain.CopyCost(cost, percent)
			if err != nil {
				rows.Close()
				return err
			}
			rowIDs, products, sites, costs = append(rowIDs, ids.New()), append(products, product), append(sites, site), append(costs, moved.String())
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(products) == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeCopySourceEmpty, "source costing version has no item costs")
		}
		if _, err := tx.Exec(ctx, `insert into item_cost (tenant_id, id, legal_entity_id, version_id, product_id, site_id, unit_cost, effective_from, source)
			select v.tenant_id, i.id, v.legal_entity_id, v.id, i.product_id, i.site_id, i.unit_cost, v.effective_from, 'COPY'
			from costing_version v
			cross join unnest($3::uuid[], $4::uuid[], $5::uuid[], $6::numeric[]) as i(id, product_id, site_id, unit_cost)
			where v.tenant_id = $1 and v.id = $2
			on conflict (tenant_id, version_id, product_id, site_id) do update set unit_cost = excluded.unit_cost, source = 'COPY', updated_at = now()`,
			p.TenantID, targetID, rowIDs, products, sites, costs); err != nil {
			return err
		}
		n = len(products)
		_, err = tx.Exec(ctx, `update costing_version set version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, targetID)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// PreviewActivation computes the revaluation that activating versionID would
// post, without changing any row (FR-20.5: mandatory preview before activation).
// For an ACTIVE version it returns the revaluation recorded at activation.
func (s *Postgres) PreviewActivation(ctx context.Context, versionID string, onHand OnHand) (ActivationResult, error) {
	return s.activate(ctx, versionID, 0, onHand, false)
}

// ActivateVersion activates a DRAFT STANDARD version in one transaction: the
// previous ACTIVE cost of each product x site becomes SUPERSEDED, the PENDING
// costs become ACTIVE with the revaluation of on-hand recorded on each row, the
// version becomes ACTIVE, and one cost.changed event per product x site goes to
// the outbox. All of it commits or none of it does. Activations in one legal
// entity are serialised by an advisory lock, so two versions never read the
// same "previous" cost. Activating an ACTIVE version replays its recorded
// result without writing (idempotent per version id).
func (s *Postgres) ActivateVersion(ctx context.Context, versionID string, expectedVersion int, onHand OnHand) (ActivationResult, error) {
	return s.activate(ctx, versionID, expectedVersion, onHand, true)
}

func (s *Postgres) activate(ctx context.Context, versionID string, expectedVersion int, onHand OnHand, commit bool) (ActivationResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ActivationResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		lock := ""
		if commit {
			lock = "for update"
		}
		v, err := getVersion(ctx, tx, p.TenantID, versionID, lock)
		if err != nil {
			return err
		}
		out.Version = v
		if v.Status == "ACTIVE" {
			out.AlreadyActive = true
			out.Plan, err = loadActivated(ctx, tx, p.TenantID, versionID)
			return err
		}
		if v.Status != "DRAFT" {
			return httpx.NewProblem(http.StatusConflict, CodeVersionNotDraft, "only a DRAFT version can be activated")
		}
		if err := domain.CheckActivatable(domain.VersionState{Type: v.Type, EffectiveFrom: v.EffectiveFrom, Blocked: v.BlockActivation},
			time.Now().UTC().Format(time.DateOnly)); err != nil {
			return err
		}
		if commit {
			if v.Version != expectedVersion {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeConflictVersion,
					"costing version changed since it was read; preview the activation again")
			}
			if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
				activateLock+"|"+p.TenantID+"|"+v.LegalEntityID); err != nil {
				return err
			}
		}
		pending, err := loadPending(ctx, tx, p.TenantID, versionID, onHand)
		if err != nil {
			return err
		}
		plan, err := domain.PlanActivation(false, money.MinorUnits(v.Currency), pending)
		if err != nil {
			return err
		}
		out.Plan = plan
		if !commit {
			return nil
		}
		activatedAt, err := commitActivation(ctx, tx, p.TenantID, v, plan)
		if err != nil {
			return err
		}
		out.Version.Status, out.Version.Version, out.Version.ActivatedAt = "ACTIVE", v.Version+1, &activatedAt
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ActivationResult{}, httpx.NewProblem(http.StatusConflict, CodeActivationConflict,
			"another activation changed these product costs at the same time; preview again and retry")
	}
	if err != nil {
		return ActivationResult{}, err
	}
	return out, nil
}

// loadPending reads the PENDING costs of a version with the ACTIVE cost each one
// replaces, attaching the on-hand used for the revaluation.
func loadPending(ctx context.Context, tx pgx.Tx, tenantID, versionID string, onHand OnHand) ([]domain.PendingCost, error) {
	rows, err := tx.Query(ctx, `select ic.product_id::text, ic.site_id::text, ic.unit_cost::text, prev.unit_cost::text
		from item_cost ic
		left join item_cost prev on prev.tenant_id = ic.tenant_id and prev.product_id = ic.product_id
			and prev.site_id = ic.site_id and prev.status = 'ACTIVE'
		where ic.tenant_id = $1 and ic.version_id = $2 and ic.status = 'PENDING'
		order by ic.site_id, ic.product_id`, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.PendingCost
	for rows.Next() {
		var productID, siteID, newCost string
		var prevCost *string
		if err := rows.Scan(&productID, &siteID, &newCost, &prevCost); err != nil {
			return nil, err
		}
		oh := onHand[OnHandKey(productID, siteID)]
		pc := domain.PendingCost{ProductID: productID, SiteID: siteID, OnHandQty: oh.Qty, BookValue: oh.BookValue}
		if pc.NewCost, err = decimal.NewFromString(newCost); err != nil {
			return nil, err
		}
		if prevCost != nil {
			pc.HasPrevious = true
			if pc.PreviousCost, err = decimal.NewFromString(*prevCost); err != nil {
				return nil, err
			}
		}
		out = append(out, pc)
	}
	return out, rows.Err()
}

// loadActivated rebuilds the plan an ACTIVE version recorded when it was activated.
func loadActivated(ctx context.Context, tx pgx.Tx, tenantID, versionID string) (domain.ActivationPlan, error) {
	rows, err := tx.Query(ctx, `select product_id::text, site_id::text, unit_cost::text, previous_unit_cost::text,
			coalesce(book_value, 0)::text, coalesce(on_hand_qty, 0)::text, coalesce(revaluation_amount, 0)::text
		from item_cost where tenant_id = $1 and version_id = $2 and activated_at is not null
		order by site_id, product_id`, tenantID, versionID)
	if err != nil {
		return domain.ActivationPlan{}, err
	}
	defer rows.Close()
	var lines []domain.ActivationLine
	for rows.Next() {
		var l domain.ActivationLine
		var cost, book, qty, reval string
		var prev *string
		if err := rows.Scan(&l.ProductID, &l.SiteID, &cost, &prev, &book, &qty, &reval); err != nil {
			return domain.ActivationPlan{}, err
		}
		l.NewCost, l.BookValue, l.OnHandQty, l.Revaluation = decimal.RequireFromString(cost), decimal.RequireFromString(book),
			decimal.RequireFromString(qty), decimal.RequireFromString(reval)
		if prev != nil {
			l.HasPrevious, l.PreviousCost = true, decimal.RequireFromString(*prev)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return domain.ActivationPlan{}, err
	}
	sites, total := domain.SiteTotals(lines)
	return domain.ActivationPlan{Lines: lines, Sites: sites, Total: total}, nil
}

// commitActivation writes the plan: supersede the old ACTIVE rows, activate the
// pending rows with what the revaluation used, emit cost.changed per product x
// site and mark the version ACTIVE. It returns the activation time.
func commitActivation(ctx context.Context, tx pgx.Tx, tenantID string, v CostingVersion, plan domain.ActivationPlan) (time.Time, error) {
	var activatedAt time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&activatedAt); err != nil {
		return time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `update item_cost prev set status = 'SUPERSEDED', updated_at = now()
		from item_cost ic
		where ic.tenant_id = $1 and ic.version_id = $2 and ic.status = 'PENDING'
		  and prev.tenant_id = ic.tenant_id and prev.product_id = ic.product_id and prev.site_id = ic.site_id
		  and prev.status = 'ACTIVE'`, tenantID, v.ID); err != nil {
		return time.Time{}, err
	}
	scale := money.MinorUnits(v.Currency)
	n := len(plan.Lines)
	products, sites, prevs, books, qtys, revals := make([]string, n), make([]string, n), make([]*string, n), make([]*string, n), make([]string, n), make([]string, n)
	for i, l := range plan.Lines {
		products[i], sites[i], qtys[i], revals[i] = l.ProductID, l.SiteID, l.OnHandQty.String(), l.Revaluation.StringFixed(scale)
		if l.HasPrevious {
			prev := l.PreviousCost.String()
			prevs[i] = &prev
		} else {
			book := l.BookValue.String()
			books[i] = &book
		}
	}
	tag, err := tx.Exec(ctx, `update item_cost ic set status = 'ACTIVE', previous_unit_cost = l.prev::numeric, book_value = l.book::numeric,
			on_hand_qty = l.qty::numeric, revaluation_amount = l.reval::numeric, activated_at = $9, updated_at = now()
		from unnest($3::uuid[], $4::uuid[], $5::text[], $6::text[], $7::text[], $8::text[]) as l(product_id, site_id, prev, book, qty, reval)
		where ic.tenant_id = $1 and ic.version_id = $2 and ic.status = 'PENDING'
		  and ic.product_id = l.product_id and ic.site_id = l.site_id`,
		tenantID, v.ID, products, sites, prevs, books, qtys, revals, activatedAt)
	if err != nil {
		return time.Time{}, err
	}
	if int(tag.RowsAffected()) != n {
		return time.Time{}, errors.New("activation: pending item costs changed during activation")
	}
	for _, l := range plan.Lines {
		payload := costChanged{ProductID: l.ProductID, SiteID: l.SiteID, VersionID: v.ID, VersionCode: v.Code, Currency: v.Currency,
			NewCost: l.NewCost.String(), EffectiveFrom: v.EffectiveFrom, OnHandQty: l.OnHandQty.String(),
			RevaluationAmount: l.Revaluation.StringFixed(scale)}
		if l.HasPrevious {
			old := l.PreviousCost.String()
			payload.OldCost = &old
		}
		evt, err := events.New(events.Type("costing", "cost", "changed", 1), eventSource, l.ProductID, tenantID, payload)
		if err != nil {
			return time.Time{}, err
		}
		if err := outbox.Enqueue(ctx, tx, costAggType, OnHandKey(l.ProductID, l.SiteID), evt); err != nil {
			return time.Time{}, err
		}
	}
	_, err = tx.Exec(ctx, `update costing_version set status = 'ACTIVE', activated_at = $3, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, tenantID, v.ID, activatedAt)
	return activatedAt, err
}

// CurrentCost returns the standard cost of a product x site in force at the end
// of asOf (YYYY-MM-DD, UTC): the cost activated last on or before that day,
// whether it is still ACTIVE or has since been SUPERSEDED. Activation never
// happens before a version's effective date, so the activation time is when the
// cost took effect.
func (s *Postgres) CurrentCost(ctx context.Context, productID, siteID, asOf string) (CostLookup, error) {
	p, _ := tenant.FromContext(ctx)
	var out CostLookup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select ic.product_id::text, ic.site_id::text, ic.unit_cost::text, v.currency, v.id::text, v.code,
				v.effective_from::text, ic.activated_at
			from item_cost ic
			join costing_version v on v.tenant_id = ic.tenant_id and v.id = ic.version_id
			where ic.tenant_id = $1 and ic.product_id = $2::uuid and ic.site_id = $3::uuid
			  and ic.status in ('ACTIVE', 'SUPERSEDED') and ic.activated_at is not null
			  and ic.activated_at < (($4::date + 1)::timestamp at time zone 'UTC')
			order by ic.activated_at desc
			limit 1`, p.TenantID, productID, siteID, asOf).Scan(&out.ProductID, &out.SiteID, &out.UnitCost, &out.Currency,
			&out.VersionID, &out.VersionCode, &out.EffectiveFrom, &out.ActivatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeCostNotFound, "no standard cost was active for this product and site on that date")
		}
		return err
	})
	if err != nil {
		return CostLookup{}, err
	}
	out.UnitCost = normalize(out.UnitCost)
	return out, nil
}
