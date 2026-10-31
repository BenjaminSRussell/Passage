package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// CodeChangeNotFound refuses a command on an order change record that does not exist.
const CodeChangeNotFound = "PRODUCTION_ORDER_CHANGE_NOT_FOUND"

// ---- OCM profiles (FR-21.16, PRD 4.21.13) ----

// OCMProfile is a stored OCM profile with its version for If-Match.
type OCMProfile struct {
	LegalEntityID string `json:"legalEntityId"`
	domain.OCMProfile
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// OCMProfileSave creates (IfMatch 0) or replaces the profile of a legal entity × item group × site.
type OCMProfileSave struct {
	LegalEntityID string
	Profile       domain.OCMProfile
	IfMatch       int
	Actor         string
}

const ocmProfileColumns = `legal_entity_id::text, item_group, site_key, mode, surcharge_fixed::text, surcharge_pct::text, version, updated_by, updated_at`

func scanOCMProfile(row pgx.Row) (OCMProfile, error) {
	var p OCMProfile
	var mode, fixed, pct string
	err := row.Scan(&p.LegalEntityID, &p.ItemGroup, &p.SiteID, &mode, &fixed, &pct, &p.Version, &p.UpdatedBy, &p.UpdatedAt)
	p.Mode, p.SurchargeFixed, p.SurchargePct = domain.OCMMode(mode), dec(fixed), dec(pct)
	return p, err
}

func loadOCMProfiles(ctx context.Context, q querier, tenantID, legalEntityID string) ([]OCMProfile, error) {
	rows, err := q.Query(ctx, `select `+ocmProfileColumns+` from production_ocm_profile
		where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by legal_entity_id, item_group, site_key`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OCMProfile{}
	for rows.Next() {
		p, err := scanOCMProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListOCMProfiles returns the OCM profiles of a legal entity.
func (s *Postgres) ListOCMProfiles(ctx context.Context, legalEntityID string) ([]OCMProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var out []OCMProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadOCMProfiles(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// SaveOCMProfile creates a profile (IfMatch 0) or replaces it under If-Match; created reports which.
func (s *Postgres) SaveOCMProfile(ctx context.Context, in OCMProfileSave) (OCMProfile, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out OCMProfile
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr := in.Profile
		var version int
		err := tx.QueryRow(ctx, `select version from production_ocm_profile
			where tenant_id = $1 and legal_entity_id = $2 and item_group = $3 and site_key = $4 for update`,
			p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID).Scan(&version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if in.IfMatch != 0 {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the OCM profile does not exist; create it without If-Match")
			}
			created = true
			_, err = tx.Exec(ctx, `insert into production_ocm_profile (tenant_id, legal_entity_id, item_group, site_key, mode, surcharge_fixed, surcharge_pct, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8)`, p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID, string(pr.Mode),
				pr.SurchargeFixed.String(), pr.SurchargePct.String(), in.Actor)
		case err != nil:
			return err
		case in.IfMatch == 0:
			return httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the profile version is required to replace it")
		case in.IfMatch != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the OCM profile was changed by another user; current version is %d", version))
		default:
			_, err = tx.Exec(ctx, `update production_ocm_profile set mode = $5, surcharge_fixed = $6, surcharge_pct = $7, updated_by = $8, updated_at = now(), version = version + 1
				where tenant_id = $1 and legal_entity_id = $2 and item_group = $3 and site_key = $4`, p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID,
				string(pr.Mode), pr.SurchargeFixed.String(), pr.SurchargePct.String(), in.Actor)
		}
		if err != nil {
			return err
		}
		out, err = scanOCMProfile(tx.QueryRow(ctx, `select `+ocmProfileColumns+` from production_ocm_profile
			where tenant_id = $1 and legal_entity_id = $2 and item_group = $3 and site_key = $4`, p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID))
		return err
	})
	return out, created, err
}

// ---- order change records ----

// OrderChange is one OrderChangeRecord (FR-21.16): an open order an ECO's BOM
// or route supersedes, with the delta and change cost and what became of it.
type OrderChange struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	OrderID       string `json:"orderId"`
	OrderNumber   string `json:"orderNumber"`
	ProductID     string `json:"productId"`
	SiteID        string `json:"siteId"`
	EcoID         string `json:"ecoId"`
	Status        string `json:"status"`
	ProfileMode   string `json:"profileMode"`
	// NeedsReview marks an order edited by hand (FR-21.18): never applied automatically.
	NeedsReview bool `json:"needsReview"`
	// ChangesBOM and ChangesRoute say which part of the order the ECO replaces.
	ChangesBOM   bool               `json:"changesBom"`
	ChangesRoute bool               `json:"changesRoute"`
	Delta        domain.ChangeDelta `json:"delta"`
	DeltaCount   int                `json:"deltaCount"`
	Currency     string             `json:"currency"`
	ChangeCost   decimal.Decimal    `json:"changeCost"`
	BeforeHash   *string            `json:"beforeHash"`
	AfterHash    *string            `json:"afterHash"`
	Note         string             `json:"note"`
	Version      int                `json:"version"`
	DetectedBy   string             `json:"detectedBy"`
	DetectedAt   time.Time          `json:"detectedAt"`
	ResolvedBy   *string            `json:"resolvedBy"`
	ResolvedAt   *time.Time         `json:"resolvedAt"`
}

const changeColumns = `c.id::text, c.legal_entity_id::text, c.order_id::text, o.order_number, o.product_id::text, o.site_id::text, c.eco_id, c.status,
	c.profile_mode, c.needs_review, c.new_bom is not null, c.new_operations is not null, c.lines_added, c.lines_removed, c.lines_changed,
	c.operations_added, c.operations_removed, c.operations_changed, c.currency::text, c.change_cost::text, c.before_hash, c.after_hash, c.note,
	c.version, c.detected_by, c.detected_at, c.resolved_by, c.resolved_at`

const changeFrom = ` from production_order_change c join production_order o on o.tenant_id = c.tenant_id and o.id = c.order_id `

func scanChange(row pgx.Row) (OrderChange, error) {
	var c OrderChange
	var cost string
	d := &c.Delta
	err := row.Scan(&c.ID, &c.LegalEntityID, &c.OrderID, &c.OrderNumber, &c.ProductID, &c.SiteID, &c.EcoID, &c.Status, &c.ProfileMode, &c.NeedsReview,
		&c.ChangesBOM, &c.ChangesRoute, &d.LinesAdded, &d.LinesRemoved, &d.LinesChanged, &d.OperationsAdded, &d.OperationsRemoved, &d.OperationsChanged,
		&c.Currency, &cost, &c.BeforeHash, &c.AfterHash, &c.Note, &c.Version, &c.DetectedBy, &c.DetectedAt, &c.ResolvedBy, &c.ResolvedAt)
	c.ChangeCost = dec(cost)
	c.DeltaCount = d.Total()
	return c, err
}

func changeNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeChangeNotFound, "order change not found")
}

func loadChange(ctx context.Context, q querier, tenantID, id string, lock bool) (OrderChange, error) {
	sql := `select ` + changeColumns + changeFrom + ` where c.tenant_id = $1 and c.id = $2`
	if lock {
		sql += ` for update of c`
	}
	c, err := scanChange(q.QueryRow(ctx, sql, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, changeNotFound()
	}
	return c, err
}

// GetOrderChange returns one change record.
func (s *Postgres) GetOrderChange(ctx context.Context, id string) (OrderChange, error) {
	p, _ := tenant.FromContext(ctx)
	var out OrderChange
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadChange(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ChangeFilter selects the OCM worklist (PRD 4.21.14). Cursor is the NextCursor of the previous page.
type ChangeFilter struct {
	Status      string
	SiteID      string
	OrderID     string
	EcoID       string
	NeedsReview *bool
	Limit       int
	Cursor      string
}

// ChangePage is a page of the worklist, newest first.
type ChangePage struct {
	Items      []OrderChange `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// ListOrderChanges serves the order change management worklist, newest first.
func (s *Postgres) ListOrderChanges(ctx context.Context, f ChangeFilter) (ChangePage, error) {
	p, _ := tenant.FromContext(ctx)
	page := ChangePage{Items: []OrderChange{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var afterAt time.Time
		var afterID string
		if f.Cursor != "" {
			at, id, ok := strings.Cut(f.Cursor, "_")
			t, perr := time.Parse(time.RFC3339Nano, at)
			if !ok || perr != nil || !ids.Valid(id) {
				return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor is not one this list returned")
			}
			afterAt, afterID = t, id
		}
		rows, err := tx.Query(ctx, `select `+changeColumns+changeFrom+`
			where c.tenant_id = $1 and ($2 = '' or c.status = $2) and ($3 = '' or o.site_id::text = $3) and ($4 = '' or c.order_id::text = $4)
			  and ($5 = '' or c.eco_id = $5) and ($6::boolean is null or c.needs_review = $6)
			  and ($7::timestamptz is null or (c.detected_at, c.id) < ($7, $8::uuid))
			order by c.detected_at desc, c.id desc limit $9`, p.TenantID, f.Status, f.SiteID, f.OrderID, f.EcoID, f.NeedsReview,
			nullTime(afterAt), nullUUID(afterID), f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanChange(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > f.Limit {
			page.Items = page.Items[:f.Limit]
			last := page.Items[f.Limit-1]
			page.NextCursor = last.DetectedAt.UTC().Format(time.RFC3339Nano) + "_" + last.ID
		}
		return nil
	})
	return page, err
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- detection ----

// ChangeDetect is one ECO notice (FR-21.16): the product whose BOM or route
// version the ECO superseded and the new snapshot. BOM or Operations nil leaves
// that part of the order alone. Site narrows the orders to one site.
type ChangeDetect struct {
	EcoID       string
	ProductID   string
	SiteID      string
	BOM         *[]domain.BOMComponent
	Operations  *[]domain.Operation
	Actor       string
	TraceID     string
	RequestHash string
}

// ChangeDetectResult says what a detection did: Created are the new records,
// Existing the orders already recorded for the ECO, and the skips the open
// orders that need nothing (profile none, or the order already matches).
type ChangeDetectResult struct {
	EcoID          string        `json:"ecoId"`
	Created        []OrderChange `json:"items"`
	Existing       int           `json:"existing"`
	SkippedNone    int           `json:"skippedByProfile"`
	SkippedNoDelta int           `json:"skippedNoDelta"`
}

// openStatuses are the order statuses that take a BOM or route change.
var openStatuses = []string{string(domain.Created), string(domain.Estimated), string(domain.Scheduled), string(domain.Released)}

// DetectOrderChanges marks the open orders of a product whose BOM or route the
// ECO supersedes (FR-21.16, ocm-detect). The open orders are locked (in
// order-number order, before any change record). Per order, in one transaction: the
// OCM profile of its item group × site is resolved (none skips the order), the
// new snapshot is exploded for the order's quantity and compared with its
// lines and operations (no difference skips it), and an OrderChangeRecord with
// the delta and the BR-21.12 change cost is written with
// orderchange.detected. An (order, ECO) pair is recorded once: repeating the
// notice is a no-op, and the same ECO with another snapshot is refused.
func (s *Postgres) DetectOrderChanges(ctx context.Context, in ChangeDetect) (ChangeDetectResult, error) {
	p, _ := tenant.FromContext(ctx)
	res := ChangeDetectResult{EcoID: in.EcoID, Created: []OrderChange{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		profiles, err := loadOCMProfiles(ctx, tx, p.TenantID, "")
		if err != nil {
			return err
		}
		byEntity := map[string][]domain.OCMProfile{}
		for _, pr := range profiles {
			byEntity[pr.LegalEntityID] = append(byEntity[pr.LegalEntityID], pr.OCMProfile)
		}
		var clash string
		err = tx.QueryRow(ctx, `select order_id::text from production_order_change where tenant_id = $1 and eco_id = $2 and request_hash <> $3 limit 1`,
			p.TenantID, in.EcoID, in.RequestHash).Scan(&clash)
		if err == nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "ecoId was already detected with a different BOM or route snapshot")
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, order_number, status, quantity::text, site_id::text, item_group, currency::text, order_bom_modified, version
			from production_order where tenant_id = $1 and product_id = $2 and status = any($3) and ($4 = '' or site_id::text = $4)
			order by order_number for update`, p.TenantID, in.ProductID, openStatuses, in.SiteID)
		if err != nil {
			return err
		}
		type candidate struct {
			id, entity, number, status, siteID, group, currency string
			qty                                                 decimal.Decimal
			modified                                            bool
			version                                             int
		}
		var open []candidate
		for rows.Next() {
			var c candidate
			var qty string
			if err := rows.Scan(&c.id, &c.entity, &c.number, &c.status, &qty, &c.siteID, &c.group, &c.currency, &c.modified, &c.version); err != nil {
				rows.Close()
				return err
			}
			c.qty = dec(qty)
			open = append(open, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range open {
			var seen bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from production_order_change where tenant_id = $1 and order_id = $2 and eco_id = $3)`,
				p.TenantID, c.id, in.EcoID).Scan(&seen); err != nil {
				return err
			}
			if seen {
				res.Existing++
				continue
			}
			// The ECO replaces what older pending changes of the order would bring:
			// they are dismissed, whatever this ECO does to the order.
			if err := supersedePending(ctx, tx, p.TenantID, c.id, c.version, in); err != nil {
				return err
			}
			profile := domain.ResolveOCMProfile(byEntity[c.entity], c.group, c.siteID)
			if profile.Mode == domain.OCMNone {
				res.SkippedNone++
				continue
			}
			lines, err := loadLines(ctx, tx, p.TenantID, c.id)
			if err != nil {
				return err
			}
			ops, err := loadOperations(ctx, tx, p.TenantID, c.id)
			if err != nil {
				return err
			}
			next, err := explodeSnapshot(in.BOM, in.Operations, c.qty)
			if err != nil {
				return err
			}
			_, delta := domain.DiffSnapshot(lines, ops, next.lines, next.ops, in.BOM != nil, in.Operations != nil)
			if delta.Total() == 0 {
				res.SkippedNoDelta++
				continue
			}
			cost := changeCostOf(lines, ops, next, profile, c.currency)
			id := ids.New()
			// A concurrent notice of the same ECO may have recorded the order first.
			tag, err := tx.Exec(ctx, `insert into production_order_change (tenant_id, id, legal_entity_id, order_id, eco_id, profile_mode, needs_review, new_bom,
					new_operations, request_hash, lines_added, lines_removed, lines_changed, operations_added, operations_removed, operations_changed,
					currency, change_cost, detected_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
				on conflict (tenant_id, order_id, eco_id) do nothing`, p.TenantID, id, c.entity, c.id, in.EcoID,
				string(profile.Mode), c.modified, snapshotJSON(in.BOM), snapshotJSON(in.Operations), in.RequestHash, delta.LinesAdded, delta.LinesRemoved,
				delta.LinesChanged, delta.OperationsAdded, delta.OperationsRemoved, delta.OperationsChanged, c.currency, cost.Total.String(), in.Actor)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				res.Existing++
				continue
			}
			rec, err := loadChange(ctx, tx, p.TenantID, id, false)
			if err != nil {
				return err
			}
			if err := enqueueChange(ctx, tx, prodevents.OrderChangeDetected, p.TenantID, rec, rec.DetectedAt, c.version, in.Actor); err != nil {
				return err
			}
			res.Created = append(res.Created, rec)
		}
		return nil
	})
	return res, err
}

// supersedePending dismisses the pending changes of an order that the ECO
// being detected covers (it replaces every part they replace), with
// orderchange.dismissed each: applying one later would undo the newer version.
func supersedePending(ctx context.Context, tx pgx.Tx, tenantID, orderID string, orderVersion int, in ChangeDetect) error {
	rows, err := tx.Query(ctx, `select id::text from production_order_change
		where tenant_id = $1 and order_id = $2 and status = 'pending' and eco_id <> $3 and ($4 or new_bom is null) and ($5 or new_operations is null)
		order by detected_at, id for update`, tenantID, orderID, in.EcoID, in.BOM != nil, in.Operations != nil)
	if err != nil {
		return err
	}
	var old []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		old = append(old, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range old {
		if _, err := tx.Exec(ctx, `update production_order_change set status = 'dismissed', note = $3, resolved_by = $4, resolved_at = now(), version = version + 1
			where tenant_id = $1 and id = $2`, tenantID, id, "superseded by "+in.EcoID, in.Actor); err != nil {
			return err
		}
		c, err := loadChange(ctx, tx, tenantID, id, false)
		if err != nil {
			return err
		}
		if err := enqueueChange(ctx, tx, prodevents.OrderChangeDismissed, tenantID, c, *c.ResolvedAt, orderVersion, in.Actor); err != nil {
			return err
		}
	}
	return nil
}

// newSnapshot is an ECO's BOM and route exploded and planned for one order's
// quantity; a nil part leaves the order's own lines or operations alone.
type newSnapshot struct {
	lines []domain.OrderLine
	ops   []domain.Operation
}

func explodeSnapshot(bom *[]domain.BOMComponent, ops *[]domain.Operation, qty decimal.Decimal) (newSnapshot, error) {
	var out newSnapshot
	var err error
	if bom != nil {
		if out.lines, err = domain.Explode(*bom, qty); err != nil {
			return out, domainProblem(err)
		}
	}
	if ops != nil {
		if out.ops, err = domain.PlanOperations(*ops, qty); err != nil {
			return out, domainProblem(err)
		}
	}
	return out, nil
}

// changeCostOf prices a change from the planned cost of the order before and
// of the order with the snapshot's parts after (BR-21.12).
func changeCostOf(lines []domain.OrderLine, ops []domain.Operation, next newSnapshot, p domain.OCMProfile, currency string) domain.ChangeCost {
	afterLines, afterOps := lines, ops
	if next.lines != nil {
		afterLines = next.lines
	}
	if next.ops != nil {
		afterOps = next.ops
	}
	return domain.ComputeChangeCost(domain.Estimate(lines, ops), domain.Estimate(afterLines, afterOps), p, money.MinorUnits(currency))
}

func snapshotJSON[T any](v *T) []byte {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	return raw
}

func snapshotHash(lines []domain.OrderLine, ops []domain.Operation) string {
	type line struct {
		LineNo, ItemID, QtyPer, Series, Scrap, Required, Cost, Flushing string
		RoundUp                                                         bool
	}
	type op struct {
		OperNumber                                                  int
		Resource, Setup, Run, Scrap, Labor, Overhead, Sub, SubPrice string
	}
	var view struct {
		Lines []line
		Ops   []op
	}
	for _, l := range lines {
		view.Lines = append(view.Lines, line{l.LineNo, l.ItemID, l.QtyPer.String(), l.SeriesSize.String(), l.ScrapPct.String(), l.QtyRequired.String(),
			l.UnitCost.String(), string(l.Flushing), l.RoundUp})
	}
	for _, o := range ops {
		view.Ops = append(view.Ops, op{o.OperNumber, o.ResourceID, o.SetupH.String(), o.RunHPer.String(), o.ScrapPct.String(), o.LaborRate.String(),
			o.OverheadRate.String(), fmt.Sprint(o.Subcontract), o.SubcontractPrice.String()})
	}
	raw, _ := json.Marshal(view)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func enqueueChange(ctx context.Context, tx pgx.Tx, eventType, tenantID string, c OrderChange, at time.Time, orderVersion int, actor string) error {
	return enqueue(ctx, tx, c.OrderID, eventType, tenantID, actor, prodevents.OrderChangeData{
		ChangeID: c.ID, OrderID: c.OrderID, OrderNumber: c.OrderNumber, EcoID: c.EcoID, Status: c.Status, ProfileMode: c.ProfileMode,
		NeedsReview: c.NeedsReview, DeltaCount: c.DeltaCount, ChangeCost: c.ChangeCost.String(), Currency: c.Currency,
		OrderVersion: orderVersion, Actor: actor, OccurredAt: at.UTC().Format(time.RFC3339Nano)})
}

// ---- preview, apply, dismiss ----

// Refusal is why a change cannot be applied to the order as it stands now.
type Refusal struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// ChangePreview is the delta of applying a change to the order as it is now,
// with the planned cost before and after and the change cost (FR-21.16).
type ChangePreview struct {
	Change  OrderChange        `json:"change"`
	Delta   domain.ChangeDelta `json:"delta"`
	Cost    domain.ChangeCost  `json:"cost"`
	Edits   domain.OrderEdits  `json:"edits"`
	Blocked *Refusal           `json:"blocked"`
}

type changePlan struct {
	head    orderHead
	lines   []domain.OrderLine
	ops     []domain.Operation
	edits   domain.OrderEdits
	delta   domain.ChangeDelta
	cost    domain.ChangeCost
	blocked error
}

// planChange compares the order as it is now with the change's snapshot. An
// edit the order cannot take (a consumed line to remove) is blocked, not an error.
func planChange(ctx context.Context, tx pgx.Tx, tenantID string, c OrderChange, h orderHead) (changePlan, error) {
	plan := changePlan{head: h}
	var bom, route []byte
	if err := tx.QueryRow(ctx, `select new_bom, new_operations from production_order_change where tenant_id = $1 and id = $2`, tenantID, c.ID).Scan(&bom, &route); err != nil {
		return plan, err
	}
	var nextBOM *[]domain.BOMComponent
	var nextOps *[]domain.Operation
	if bom != nil {
		nextBOM = new([]domain.BOMComponent)
		if err := json.Unmarshal(bom, nextBOM); err != nil {
			return plan, err
		}
	}
	if route != nil {
		nextOps = new([]domain.Operation)
		if err := json.Unmarshal(route, nextOps); err != nil {
			return plan, err
		}
	}
	var err error
	if plan.lines, err = loadLines(ctx, tx, tenantID, h.id); err != nil {
		return plan, err
	}
	if plan.ops, err = loadOperations(ctx, tx, tenantID, h.id); err != nil {
		return plan, err
	}
	next, err := explodeSnapshot(nextBOM, nextOps, h.quantity)
	if err != nil {
		return plan, err
	}
	plan.edits, plan.delta = domain.DiffSnapshot(plan.lines, plan.ops, next.lines, next.ops, nextBOM != nil, nextOps != nil)
	profiles, err := loadOCMProfiles(ctx, tx, tenantID, h.legalEntityID)
	if err != nil {
		return plan, err
	}
	stored := make([]domain.OCMProfile, len(profiles))
	for i, pr := range profiles {
		stored[i] = pr.OCMProfile
	}
	plan.cost = changeCostOf(plan.lines, plan.ops, next, domain.ResolveOCMProfile(stored, h.itemGroup, h.siteID), h.currency)
	var later string
	err = tx.QueryRow(ctx, `select eco_id from production_order_change
		where tenant_id = $1 and order_id = $2 and id <> $3 and status in ('pending', 'applied') and detected_at > $4
		  and (($5 and new_bom is not null) or ($6 and new_operations is not null))
		order by detected_at limit 1`, tenantID, h.id, c.ID, c.DetectedAt, c.ChangesBOM, c.ChangesRoute).Scan(&later)
	switch {
	case err == nil:
		plan.blocked = &domain.Error{Code: domain.CodeChangeSuperseded,
			Detail: fmt.Sprintf("the later %s replaces the same BOM or route of this order; applying %s would undo it", later, c.EcoID)}
		return plan, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return plan, err
	}
	if plan.delta.Total() > 0 {
		inUse, err := journaledTargets(ctx, tx, tenantID, h.id)
		if err != nil {
			return plan, err
		}
		_, plan.blocked = domain.ApplyOrderEdits(plan.lines, plan.ops, h.quantity, plan.edits, inUse)
	}
	return plan, nil
}

func loadHead(ctx context.Context, tx pgx.Tx, tenantID, orderID string) (orderHead, error) {
	h := orderHead{id: orderID}
	var status, qty string
	err := tx.QueryRow(ctx, `select status, version, quantity::text, legal_entity_id::text, product_id::text, site_id::text, item_group, currency::text
		from production_order where tenant_id = $1 and id = $2`, tenantID, orderID).Scan(&status, &h.version, &qty, &h.legalEntityID, &h.productID, &h.siteID, &h.itemGroup, &h.currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, notFound()
	}
	h.status, h.quantity = domain.Status(status), dec(qty)
	return h, err
}

// lockChange locks an order change and its order, the order row first: every
// path that writes change records (a status command, an order edit, detection)
// holds the order before its changes, so taking them in the other order could
// deadlock.
func lockChange(ctx context.Context, tx pgx.Tx, tenantID, id string) (OrderChange, orderHead, error) {
	c, err := loadChange(ctx, tx, tenantID, id, false)
	if err != nil {
		return c, orderHead{}, err
	}
	h, err := lockOrder(ctx, tx, tenantID, c.OrderID, 0)
	if err != nil {
		return c, h, err
	}
	c, err = loadChange(ctx, tx, tenantID, id, true)
	return c, h, err
}

// PreviewOrderChange shows what applying the change does to the order now,
// without writing anything.
func (s *Postgres) PreviewOrderChange(ctx context.Context, id string) (ChangePreview, error) {
	p, _ := tenant.FromContext(ctx)
	var out ChangePreview
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadChange(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		h, err := loadHead(ctx, tx, p.TenantID, c.OrderID)
		if err != nil {
			return err
		}
		plan, err := planChange(ctx, tx, p.TenantID, c, h)
		if err != nil {
			return err
		}
		out = ChangePreview{Change: c, Delta: plan.delta, Cost: plan.cost, Edits: plan.edits}
		if plan.blocked == nil {
			plan.blocked = domain.CheckChangeEligible(h.status)
		}
		var de *domain.Error
		if errors.As(plan.blocked, &de) {
			out.Blocked = &Refusal{Code: de.Code, Detail: de.Detail}
		}
		return nil
	})
	return out, err
}

// ChangeApply applies a pending change. IfMatch is the change record's version.
type ChangeApply struct {
	ChangeID       string
	IfMatch        int
	IdempotencyKey string
	ReleaseDuty    bool
	Actor          string
	TraceID        string
}

// ChangeApplyResult is the record and the order after the change; Replayed
// answers a stored Idempotency-Key.
type ChangeApplyResult struct {
	Change   OrderChange
	Order    Order
	Replayed bool
}

// ApplyOrderChange applies a pending change (FR-21.16 `:apply-change`) under the
// change's If-Match: under the order row lock it re-explodes the order against
// the new BOM and route through the order-edit rules (nothing consumed or
// journaled is removed or re-pointed), re-estimates the planned cost, and
// records the before and after snapshot hashes, the change cost and the actor,
// with order.bomedited and orderchange.applied, in one transaction. The order
// is not flagged as hand-edited: it matches the new version again.
func (s *Postgres) ApplyOrderChange(ctx context.Context, in ChangeApply) (ChangeApplyResult, error) {
	p, _ := tenant.FromContext(ctx)
	var res ChangeApplyResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, h, err := lockChange(ctx, tx, p.TenantID, in.ChangeID)
		if err != nil {
			return err
		}
		var keyed string
		err = tx.QueryRow(ctx, `select id::text from production_order_change where tenant_id = $1 and apply_idempotency_key = $2`, p.TenantID, in.IdempotencyKey).Scan(&keyed)
		switch {
		case err == nil && keyed != c.ID:
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another order change")
		case err == nil:
			res.Replayed = true
			res.Change = c
			res.Order, err = loadOrder(ctx, tx, p.TenantID, c.OrderID)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if c.Status != "pending" {
			return httpx.NewProblem(http.StatusConflict, domain.CodeChangeResolved, fmt.Sprintf("the change was already %s", c.Status))
		}
		if in.IfMatch != c.Version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the change was modified by another user; current version is %d", c.Version))
		}
		if err := domain.CheckChangeEligible(h.status); err != nil {
			return domainProblem(err)
		}
		plan, err := planChange(ctx, tx, p.TenantID, c, h)
		if err != nil {
			return err
		}
		if plan.delta.Total() == 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeChangeNoDelta, "the order already matches the new BOM and route; dismiss the change")
		}
		if plan.blocked != nil {
			return domainProblem(plan.blocked)
		}
		res.Change, res.Order, err = applyPlan(ctx, tx, p.TenantID, c, plan, in.ReleaseDuty, in.Actor, in.TraceID, &in.IdempotencyKey, "")
		return err
	})
	return res, err
}

// applyPlan performs a planned change inside the caller's transaction: the
// order edits (not flagged orderBomModified), the record's before and after
// snapshot hashes, cost and delta as applied, and orderchange.applied. key is
// the Idempotency-Key of a planner's :apply (nil when applied automatically);
// a non-empty note replaces the record's note.
func applyPlan(ctx context.Context, tx pgx.Tx, tenantID string, c OrderChange, plan changePlan, releaseDuty bool, actor, traceID string, key *string, note string) (OrderChange, Order, error) {
	before := snapshotHash(plan.lines, plan.ops)
	order, err := editOrderTx(ctx, tx, tenantID, OrderEdit{OrderID: c.OrderID, Edits: plan.edits, ReleaseDuty: releaseDuty, Actor: actor, TraceID: traceID}, false)
	if err != nil {
		return OrderChange{}, Order{}, err
	}
	after := snapshotHash(order.Lines, order.Operations)
	if _, err := tx.Exec(ctx, `update production_order_change set status = 'applied', before_hash = $3, after_hash = $4, change_cost = $5,
			lines_added = $6, lines_removed = $7, lines_changed = $8, operations_added = $9, operations_removed = $10, operations_changed = $11,
			resolved_by = $12, resolved_at = now(), apply_idempotency_key = $13, note = case when $14 = '' then note else $14 end, version = version + 1
		where tenant_id = $1 and id = $2`, tenantID, c.ID, before, after, plan.cost.Total.String(), plan.delta.LinesAdded, plan.delta.LinesRemoved,
		plan.delta.LinesChanged, plan.delta.OperationsAdded, plan.delta.OperationsRemoved, plan.delta.OperationsChanged, actor, key, note); err != nil {
		return OrderChange{}, Order{}, err
	}
	applied, err := loadChange(ctx, tx, tenantID, c.ID, false)
	if err != nil {
		return OrderChange{}, Order{}, err
	}
	return applied, order, enqueueChange(ctx, tx, prodevents.OrderChangeApplied, tenantID, applied, *applied.ResolvedAt, order.Version, actor)
}

// autoApplyChanges applies, oldest first, the pending changes of an order whose
// profile was automatic (FR-21.16 "apply at the next status transition"),
// called by Transition under the order lock before the command takes effect.
// An order edited by hand is never changed automatically (FR-21.18), and a
// change that cannot apply now (superseded, blocked by consumption, or no
// difference left) stays pending for the planner. The profile stands in for
// the planner, so a Released order needs no after-release duty here.
func autoApplyChanges(ctx context.Context, tx pgx.Tx, tenantID, orderID string, cmd domain.Command, actor, traceID string) error {
	var edited bool
	if err := tx.QueryRow(ctx, `select order_bom_modified from production_order where tenant_id = $1 and id = $2`, tenantID, orderID).Scan(&edited); err != nil {
		return err
	}
	if !domain.AutoApplies(domain.OCMAutomatic, edited, cmd) {
		return nil
	}
	rows, err := tx.Query(ctx, `select id::text from production_order_change
		where tenant_id = $1 and order_id = $2 and status = 'pending' and profile_mode = 'automatic' and not needs_review
		order by detected_at, id`, tenantID, orderID)
	if err != nil {
		return err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range pending {
		c, err := loadChange(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if c.Status != "pending" {
			continue
		}
		h, err := lockOrder(ctx, tx, tenantID, orderID, 0)
		if err != nil {
			return err
		}
		plan, err := planChange(ctx, tx, tenantID, c, h)
		if err != nil {
			return err
		}
		if plan.delta.Total() == 0 || plan.blocked != nil {
			continue
		}
		if _, _, err := applyPlan(ctx, tx, tenantID, c, plan, true, actor, traceID, nil, "applied automatically at :"+string(cmd)); err != nil {
			return err
		}
	}
	return nil
}

// flagPendingForReview marks the pending changes of an order edited by hand
// (FR-21.18) for manual review, so none of them applies automatically.
func flagPendingForReview(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	_, err := tx.Exec(ctx, `update production_order_change set needs_review = true, version = version + 1
		where tenant_id = $1 and order_id = $2 and status = 'pending' and not needs_review`, tenantID, orderID)
	return err
}

// ChangeDismiss sets a pending change aside with a note. IfMatch is the change record's version.
type ChangeDismiss struct {
	ChangeID string
	IfMatch  int
	Note     string
	Actor    string
	TraceID  string
}

// DismissOrderChange sets a pending change aside (the planner keeps the order
// on its current version) and emits orderchange.dismissed. A change already
// dismissed is returned unchanged; an applied one is refused.
func (s *Postgres) DismissOrderChange(ctx context.Context, in ChangeDismiss) (OrderChange, error) {
	p, _ := tenant.FromContext(ctx)
	var out OrderChange
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, h, err := lockChange(ctx, tx, p.TenantID, in.ChangeID)
		if err != nil {
			return err
		}
		switch {
		case c.Status == "dismissed":
			out = c
			return nil
		case c.Status != "pending":
			return httpx.NewProblem(http.StatusConflict, domain.CodeChangeResolved, fmt.Sprintf("the change was already %s", c.Status))
		case in.IfMatch != c.Version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the change was modified by another user; current version is %d", c.Version))
		}
		if _, err := tx.Exec(ctx, `update production_order_change set status = 'dismissed', note = $3, resolved_by = $4, resolved_at = now(), version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, c.ID, in.Note, in.Actor); err != nil {
			return err
		}
		if out, err = loadChange(ctx, tx, p.TenantID, c.ID, false); err != nil {
			return err
		}
		return enqueueChange(ctx, tx, prodevents.OrderChangeDismissed, p.TenantID, out, *out.ResolvedAt, h.version, in.Actor)
	})
	return out, err
}
