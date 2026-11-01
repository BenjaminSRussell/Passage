package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const (
	MaxPageSize          = 100
	CodeIdempotencyReuse = "idempotency_key_reuse"
	CodeDuplicate        = "ENG-DUPLICATE"
	CodeEcoNotFound      = "ENG-ECO-NOTFOUND"
	CodeVersionNotFound  = "ENG-BOM-VERSION-NOTFOUND"
	CodeBomNotFound      = "ENG-BOM-NOTFOUND"
	CodeCalNotFound      = "ENG-CAL-NOTFOUND"
	CodeResNotFound      = "ENG-RES-NOTFOUND"
	eventSource          = "/passage/engineering/engineering"
	uniqueViolation      = "23505"
	aggregateBomVersion  = "bomversion"
	aggregateEco         = "eco"
	aggregateResource    = "resource"
	aggregateCalendar    = "calendar"

	CodeRouteNotFound     = "ENG-ROUTE-NOTFOUND"
	aggregateRouteVersion = "routeversion"
)

type Idem struct{ Key, Hash string }

type Snapshot map[string][]domain.BomVersion

func (s Snapshot) VersionsFor(product string) []domain.BomVersion { return s[product] }

func (s Snapshot) VersionsUsing(component string) []domain.BomVersion {
	var out []domain.BomVersion
	for _, versions := range s {
		for _, v := range versions {
			for _, l := range v.Lines {
				if l.ComponentProductID == component {
					out = append(out, v)
					break
				}
			}
		}
	}
	return out
}

type Page[T any] struct {
	Items  []T
	NextID string
}

type BomVersionPage = Page[domain.BomVersion]

func trimPage[T any](items []T, limit int, id func(T) string) Page[T] {
	if len(items) > limit {
		items = items[:limit]
		return Page[T]{Items: items, NextID: id(items[limit-1])}
	}
	return Page[T]{Items: items}
}

type EcoRecord struct {
	Eco      domain.Eco
	Versions []domain.BomVersion
}

type Store interface {
	CreateBomVersion(ctx context.Context, v domain.BomVersion, idem Idem, check func(Snapshot) error) (domain.BomVersion, error)
	ListBomVersions(ctx context.Context, f BomVersionFilter, afterID string, limit int) (BomVersionPage, error)
	GetBomVersion(ctx context.Context, id string) (domain.BomVersion, error)
	ActivateBomVersion(ctx context.Context, id string, check func(Snapshot, domain.BomVersion) error) (domain.BomVersion, error)
	CancelBomVersion(ctx context.Context, id string) (domain.BomVersion, error)
	UpdateBomDraft(ctx context.Context, v domain.BomVersion, expectedVersion int, idem Idem, check func(Snapshot) error) (domain.BomVersion, error)
	Catalog(ctx context.Context, roots []string, up bool) (Snapshot, error)
	CreateEco(ctx context.Context, e domain.Eco, idem Idem) (domain.Eco, error)
	GetEco(ctx context.Context, id string) (EcoRecord, error)
	ListEcos(ctx context.Context, state string, afterID string, limit int) (Page[domain.Eco], error)
	ReleaseEco(ctx context.Context, e domain.Eco, prev domain.EcoState, plan domain.ReleasePlan) error
	CreateCalendar(ctx context.Context, c Calendar, idem Idem) (Calendar, []string, error)
	GenerateCalendar(ctx context.Context, id string, from, to time.Time) (CalendarGeneration, error)
	ListCalendarDays(ctx context.Context, id string, from, to time.Time) ([]domain.CalendarDay, error)
	CreateResourceGroup(ctx context.Context, g domain.ResourceGroup, idem Idem) (domain.ResourceGroup, error)
	CreateCapability(ctx context.Context, c domain.Capability, idem Idem) (domain.Capability, error)
	CreateResource(ctx context.Context, r domain.Resource, idem Idem) (ResourceRecord, error)
	GetResource(ctx context.Context, id string) (ResourceRecord, error)
	ListResources(ctx context.Context, f ResourceFilter, afterID string, limit int) (Page[ResourceRecord], error)
	ListResourceGroups(ctx context.Context, siteID, afterID string, limit int) (Page[domain.ResourceGroup], error)
	ListCapabilities(ctx context.Context, afterID string, limit int) (Page[domain.Capability], error)
	GetCalendar(ctx context.Context, id string) (Calendar, error)
	ListCalendars(ctx context.Context, afterID string, limit int) (Page[Calendar], error)
	GetResourceGroup(ctx context.Context, id string) (domain.ResourceGroup, error)
	GetCapability(ctx context.Context, id string) (domain.Capability, error)
	CreateRouteVersion(ctx context.Context, v domain.RouteVersion, idem Idem) (domain.RouteVersion, error)
	GetRouteVersion(ctx context.Context, id string) (domain.RouteVersion, error)
	ActivateRouteVersion(ctx context.Context, id string) (domain.RouteVersion, error)
	CancelRouteVersion(ctx context.Context, id string) (domain.RouteVersion, error)
	ListRouteVersions(ctx context.Context, f RouteVersionFilter, afterID string, limit int) (Page[RouteVersionHeader], error)
	ActiveRouteVersion(ctx context.Context, productID, siteID string, asOf time.Time) (domain.RouteVersion, error)
	Ping(ctx context.Context) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const (
	versionColumns = `id::text, product_id, site_id, effective_from, effective_to, from_qty::text, to_qty::text, status, coalesce(copied_from_version_id::text, ''), version`
	lineColumns    = `bom_version_id::text, line_no, component_product_id, quantity::text, series::text, line_type, scrap_const::text, scrap_var_pct::text, round_up, effective_from, effective_to`
	activeStatuses = `v.status in ('active', 'expired')`
	reachDown      = `with recursive reach(product_id) as (
		select unnest($2::text[])
		union
		select l.component_product_id from reach r
			join bom_version v on v.tenant_id = $1 and v.product_id = r.product_id and ` + activeStatuses + `
			join bom_line l on l.tenant_id = v.tenant_id and l.bom_version_id = v.id
	) select product_id from reach`
	reachUp = `with recursive reach(product_id) as (
		select unnest($2::text[])
		union
		select v.product_id from reach r
			join bom_line l on l.tenant_id = $1 and l.component_product_id = r.product_id
			join bom_version v on v.tenant_id = l.tenant_id and v.id = l.bom_version_id and ` + activeStatuses + `
	) select product_id from reach`
)

func zeroToNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func decimalToNil(d *decimal.Decimal) any {
	if d == nil {
		return nil
	}
	return d.String()
}

func parseDecimal(s string, failure *error) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil && *failure == nil {
		*failure = err
	}
	return d
}

func parseOptional(s *string, failure *error) *decimal.Decimal {
	if s == nil {
		return nil
	}
	d := parseDecimal(*s, failure)
	return &d
}

func valueOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func queryVersions(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]domain.BomVersion, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.BomVersion
	for rows.Next() {
		var v domain.BomVersion
		var to *time.Time
		var fromQty, toQty *string
		if err := rows.Scan(&v.ID, &v.ProductID, &v.SiteID, &v.EffectiveFrom, &to, &fromQty, &toQty, &v.Status, &v.CopiedFrom, &v.Version); err != nil {
			return nil, err
		}
		var failure error
		v.EffectiveTo, v.FromQty, v.ToQty = valueOrZero(to), parseOptional(fromQty, &failure), parseOptional(toQty, &failure)
		if failure != nil {
			return nil, failure
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func loadVersions(ctx context.Context, tx pgx.Tx, tenantID, cond string, args ...any) ([]domain.BomVersion, error) {
	versions, err := queryVersions(ctx, tx, `select `+versionColumns+` from bom_version v where tenant_id = $1 and (`+cond+`) order by id`, append([]any{tenantID}, args...)...)
	if err != nil || len(versions) == 0 {
		return versions, err
	}
	position := make(map[string]int, len(versions))
	versionIDs := make([]string, len(versions))
	for i, v := range versions {
		position[v.ID], versionIDs[i] = i, v.ID
	}
	rows, err := tx.Query(ctx, `select `+lineColumns+` from bom_line where tenant_id = $1 and bom_version_id = any($2::uuid[]) order by bom_version_id, line_no`, tenantID, versionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var versionID, quantity, series, scrapConst, scrapVarPct string
		var l domain.BomLine
		var from, to *time.Time
		if err := rows.Scan(&versionID, &l.LineNo, &l.ComponentProductID, &quantity, &series, &l.LineType, &scrapConst, &scrapVarPct, &l.RoundUp, &from, &to); err != nil {
			return nil, err
		}
		var failure error
		l.Quantity, l.Series = parseDecimal(quantity, &failure), parseDecimal(series, &failure)
		l.ScrapConst, l.ScrapVarPct = parseDecimal(scrapConst, &failure), parseDecimal(scrapVarPct, &failure)
		l.EffectiveFrom, l.EffectiveTo = valueOrZero(from), valueOrZero(to)
		if failure != nil {
			return nil, failure
		}
		i := position[versionID]
		versions[i].Lines = append(versions[i].Lines, l)
	}
	return versions, rows.Err()
}

func loadSnapshot(ctx context.Context, tx pgx.Tx, tenantID string, roots []string, up bool) (Snapshot, error) {
	reach := reachDown
	if up {
		reach = reachUp
	}
	versions, err := loadVersions(ctx, tx, tenantID, `status in ('active', 'expired') and product_id in (`+reach+`)`, roots)
	snapshot := Snapshot{}
	for _, v := range versions {
		snapshot[v.ProductID] = append(snapshot[v.ProductID], v)
	}
	return snapshot, err
}

func (s *Postgres) Catalog(ctx context.Context, roots []string, up bool) (Snapshot, error) {
	p, _ := tenant.FromContext(ctx)
	var snapshot Snapshot
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		snapshot, err = loadSnapshot(ctx, tx, p.TenantID, roots, up)
		return err
	})
	return snapshot, err
}

func replay(ctx context.Context, tx pgx.Tx, tenantID string, idem Idem) (string, bool, error) {
	if idem.Key == "" {
		return "", false, nil
	}
	var hash, id string
	err := tx.QueryRow(ctx, `select request_hash, response_body->>'id' from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, idem.Key).Scan(&hash, &id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	case hash != idem.Hash:
		return "", false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return id, true, nil
}

func remember(ctx context.Context, tx pgx.Tx, tenantID string, idem Idem, resourceID string) error {
	if idem.Key == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, jsonb_build_object('id', $5::text))`,
		tenantID, idem.Key, idem.Hash, http.StatusCreated, resourceID)
	return err
}

func conflictOnDuplicate(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodeDuplicate, "a record with this number or Idempotency-Key already exists or is in flight")
	}
	return err
}

func lockProducts(ctx context.Context, tx pgx.Tx, tenantID string, products ...string) error {
	sorted := slices.Clone(products)
	slices.Sort(sorted)
	for _, product := range slices.Compact(sorted) {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+":"+product); err != nil {
			return err
		}
	}
	return nil
}

func ensureNoActiveOverlap(ctx context.Context, tx pgx.Tx, tenantID string, activated domain.BomVersion) error {
	active, err := queryVersions(ctx, tx, `select `+versionColumns+` from bom_version v where tenant_id = $1 and product_id = $2 and status = 'active'`, tenantID, activated.ProductID)
	if err != nil {
		return err
	}
	return domain.CheckNoOverlap(active, activated)
}

func versionRoots(v domain.BomVersion) []string {
	seen := map[string]bool{v.ProductID: true}
	roots := []string{v.ProductID}
	for _, l := range v.Lines {
		if !seen[l.ComponentProductID] {
			seen[l.ComponentProductID] = true
			roots = append(roots, l.ComponentProductID)
		}
	}
	return roots
}

func insertVersion(ctx context.Context, tx pgx.Tx, tenantID string, v domain.BomVersion) error {
	if _, err := tx.Exec(ctx, `insert into bom_version (tenant_id, id, product_id, site_id, effective_from, effective_to, from_qty, to_qty, status, copied_from_version_id)
		values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9, nullif($10, '')::uuid)`,
		tenantID, v.ID, v.ProductID, v.SiteID, v.EffectiveFrom, zeroToNil(v.EffectiveTo), decimalToNil(v.FromQty), decimalToNil(v.ToQty), v.Status, v.CopiedFrom); err != nil {
		return err
	}
	return insertLines(ctx, tx, tenantID, v)
}

func insertLines(ctx context.Context, tx pgx.Tx, tenantID string, v domain.BomVersion) error {
	batch := &pgx.Batch{}
	for _, l := range v.Lines {
		batch.Queue(`insert into bom_line (tenant_id, bom_version_id, line_no, component_product_id, quantity, series, line_type, scrap_const, scrap_var_pct, round_up, effective_from, effective_to)
			values ($1, $2, $3, $4, $5::numeric, $6::numeric, $7, $8::numeric, $9::numeric, $10, $11, $12)`,
			tenantID, v.ID, l.LineNo, l.ComponentProductID, l.Quantity.String(), l.Series.String(), l.LineType, l.ScrapConst.String(), l.ScrapVarPct.String(), l.RoundUp, zeroToNil(l.EffectiveFrom), zeroToNil(l.EffectiveTo))
	}
	results := tx.SendBatch(ctx, batch)
	for range v.Lines {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}

func (s *Postgres) CreateBomVersion(ctx context.Context, v domain.BomVersion, idem Idem, check func(Snapshot) error) (domain.BomVersion, error) {
	p, _ := tenant.FromContext(ctx)
	created := v
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			original, err := loadVersions(ctx, tx, p.TenantID, `id = $2::uuid`, id)
			if err != nil || len(original) == 0 {
				return errors.Join(err, fmt.Errorf("idempotent BOM version %s is missing", id))
			}
			created = original[0]
			return nil
		}
		if err := lockProducts(ctx, tx, p.TenantID, v.ProductID); err != nil {
			return err
		}
		snapshot, err := loadSnapshot(ctx, tx, p.TenantID, versionRoots(v), false)
		if err != nil {
			return err
		}
		if err := check(snapshot); err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, p.TenantID, v); err != nil {
			return err
		}
		created.Version = 1
		if v.Status == domain.StatusActive {
			if err := emit(ctx, tx, p, v.ID, engevents.BomVersionActivated, aggregateBomVersion, v.ID, v.ActivatedEvent("")); err != nil {
				return err
			}
		}
		return remember(ctx, tx, p.TenantID, idem, v.ID)
	})
	return created, conflictOnDuplicate(err)
}

// BomVersionFilter narrows the BOM version list; empty fields match everything.
// AsOf keeps the versions whose effective range covers that date.
type BomVersionFilter struct {
	ProductID, SiteID, Status string
	AsOf                      time.Time
}

func (s *Postgres) ListBomVersions(ctx context.Context, f BomVersionFilter, afterID string, limit int) (BomVersionPage, error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	query := `select ` + versionColumns + ` from bom_version v where v.tenant_id = $1 and v.id > $2::uuid`
	args := []any{p.TenantID, afterID}
	for _, c := range []struct {
		value  any
		clause string
		set    bool
	}{
		{f.ProductID, `v.product_id = $%d`, f.ProductID != ""},
		{f.SiteID, `v.site_id = $%d`, f.SiteID != ""},
		{f.Status, `v.status = $%d`, f.Status != ""},
		{f.AsOf, `v.effective_from <= $%[1]d and (v.effective_to is null or v.effective_to >= $%[1]d)`, !f.AsOf.IsZero()},
	} {
		if c.set {
			args = append(args, c.value)
			query += ` and ` + fmt.Sprintf(c.clause, len(args))
		}
	}
	query += fmt.Sprintf(` order by v.id limit $%d`, len(args)+1)
	var items []domain.BomVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		items, err = queryVersions(ctx, tx, query, append(args, limit+1)...)
		return err
	})
	return trimPage(items, limit, func(v domain.BomVersion) string { return v.ID }), err
}

func (s *Postgres) GetBomVersion(ctx context.Context, id string) (v domain.BomVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := loadVersions(ctx, tx, p.TenantID, `id = $2::uuid`, id)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return notFound(CodeBomNotFound, "BOM version not found")
		}
		v = found[0]
		return nil
	})
	return v, err
}

func getEco(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.Eco, error) {
	var e domain.Eco
	var releaseDate *time.Time
	err := tx.QueryRow(ctx, `select id::text, number, state, release_date, decided_by, created_by, decision_reason from eco where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&e.ID, &e.Number, &e.State, &releaseDate, &e.DecidedBy, &e.CreatedBy, &e.DecisionReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, httpx.NewProblem(http.StatusNotFound, CodeEcoNotFound, "ECO not found")
	}
	if err != nil {
		return e, err
	}
	e.ReleaseDate = valueOrZero(releaseDate)
	rows, err := tx.Query(ctx, `select old_version_id::text, new_version_id::text from eco_replacement where tenant_id = $1 and eco_id = $2::uuid order by new_version_id`, tenantID, id)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.Replacement
		if err := rows.Scan(&r.OldVersionID, &r.NewVersionID); err != nil {
			return e, err
		}
		e.Replaces = append(e.Replaces, r)
	}
	return e, rows.Err()
}

func emit(ctx context.Context, tx pgx.Tx, p tenant.Principal, correlationID, typ, aggregateType, aggregateID string, data any) error {
	evt, err := events.New(typ, eventSource, aggregateID, p.TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor, evt.CorrelationID = p.UserID, correlationID
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func (s *Postgres) CreateEco(ctx context.Context, e domain.Eco, idem Idem) (domain.Eco, error) {
	p, _ := tenant.FromContext(ctx)
	created := e
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			created, err = getEco(ctx, tx, p.TenantID, id)
			return err
		}
		versionIDs := make([]string, 0, 2*len(e.Replaces))
		distinct := map[string]bool{}
		for _, r := range e.Replaces {
			for _, id := range []string{r.OldVersionID, r.NewVersionID} {
				if !distinct[id] {
					distinct[id] = true
					versionIDs = append(versionIDs, id)
				}
			}
		}
		var known int
		var products []string
		if err := tx.QueryRow(ctx, `select count(*), coalesce(array_agg(distinct product_id), '{}') from bom_version where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, versionIDs).Scan(&known, &products); err != nil {
			return err
		}
		if known != len(versionIDs) {
			return httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "a replaced or replacing BOM version does not exist")
		}
		// The product lock serialises this with a draft edit, activation and cancel (BR-9.3): an edit that
		// passed its open-ECO check commits before the ECO sees the draft, and a later edit sees the ECO.
		if err := lockProducts(ctx, tx, p.TenantID, products...); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into eco (tenant_id, id, number, state, release_date, created_by) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, e.ID, e.Number, string(e.State), zeroToNil(e.ReleaseDate), e.CreatedBy); err != nil {
			return err
		}
		for _, r := range e.Replaces {
			if _, err := tx.Exec(ctx, `insert into eco_replacement (tenant_id, eco_id, old_version_id, new_version_id) values ($1, $2, $3, $4)`,
				p.TenantID, e.ID, r.OldVersionID, r.NewVersionID); err != nil {
				return err
			}
		}
		if err := emit(ctx, tx, p, e.ID, engevents.EcoSubmitted, aggregateEco, e.ID, e.EventData()); err != nil {
			return err
		}
		return remember(ctx, tx, p.TenantID, idem, e.ID)
	})
	return created, conflictOnDuplicate(err)
}

func (s *Postgres) GetEco(ctx context.Context, id string) (EcoRecord, error) {
	p, _ := tenant.FromContext(ctx)
	var rec EcoRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if rec.Eco, err = getEco(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		versionIDs := make([]string, 0, 2*len(rec.Eco.Replaces))
		for _, r := range rec.Eco.Replaces {
			versionIDs = append(versionIDs, r.OldVersionID, r.NewVersionID)
		}
		rec.Versions, err = loadVersions(ctx, tx, p.TenantID, `product_id in (select product_id from bom_version where tenant_id = $1 and id = any($2::uuid[]))`, versionIDs)
		return err
	})
	return rec, err
}

// ListEcos pages through engineering change orders by id, optionally for one
// state, each with its replacements.
func (s *Postgres) ListEcos(ctx context.Context, state, afterID string, limit int) (Page[domain.Eco], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []domain.Eco
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, number, state, release_date, decided_by, created_by, decision_reason from eco
			where tenant_id = $1 and id > $2::uuid and ($3 = '' or state = $3) order by id limit $4`, p.TenantID, afterID, state, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		byID := map[string]int{}
		var idList []string
		for rows.Next() {
			var e domain.Eco
			var releaseDate *time.Time
			if err := rows.Scan(&e.ID, &e.Number, &e.State, &releaseDate, &e.DecidedBy, &e.CreatedBy, &e.DecisionReason); err != nil {
				return err
			}
			e.ReleaseDate = valueOrZero(releaseDate)
			byID[e.ID] = len(items)
			idList = append(idList, e.ID)
			items = append(items, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if len(idList) == 0 {
			return nil
		}
		reps, err := tx.Query(ctx, `select eco_id::text, old_version_id::text, new_version_id::text from eco_replacement
			where tenant_id = $1 and eco_id = any($2::uuid[]) order by eco_id, new_version_id`, p.TenantID, idList)
		if err != nil {
			return err
		}
		defer reps.Close()
		for reps.Next() {
			var id string
			var r domain.Replacement
			if err := reps.Scan(&id, &r.OldVersionID, &r.NewVersionID); err != nil {
				return err
			}
			i := byID[id]
			items[i].Replaces = append(items[i].Replaces, r)
		}
		return reps.Err()
	})
	return trimPage(items, limit, func(e domain.Eco) string { return e.ID }), err
}

func transition(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	tag, err := tx.Exec(ctx, query, args...)
	if err == nil && tag.RowsAffected() != 1 {
		return httpx.NewProblem(http.StatusConflict, domain.CodeEcoState, "the ECO or its BOM versions changed concurrently, reload and retry")
	}
	return err
}

func (s *Postgres) ReleaseEco(ctx context.Context, e domain.Eco, prev domain.EcoState, plan domain.ReleasePlan) error {
	p, _ := tenant.FromContext(ctx)
	activatedProducts := make([]string, len(plan.Activate))
	for i, v := range plan.Activate {
		activatedProducts[i] = v.ProductID
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockProducts(ctx, tx, p.TenantID, activatedProducts...); err != nil {
			return err
		}
		if err := transition(ctx, tx, `update eco set state = $3, decided_by = $4, decision_reason = $6, version = version + 1 where tenant_id = $1 and id = $2::uuid and state = $5`,
			p.TenantID, e.ID, string(e.State), e.DecidedBy, string(prev), e.DecisionReason); err != nil {
			return err
		}
		for _, v := range plan.Expire {
			if err := transition(ctx, tx, `update bom_version set status = 'expired', effective_to = $3, version = version + 1 where tenant_id = $1 and id = $2::uuid and status = 'active'`,
				p.TenantID, v.ID, v.EffectiveTo); err != nil {
				return err
			}
		}
		for _, v := range plan.Activate {
			if err := transition(ctx, tx, `update bom_version set status = 'active', effective_from = $3, version = version + 1 where tenant_id = $1 and id = $2::uuid and status = 'draft'`,
				p.TenantID, v.ID, v.EffectiveFrom); err != nil {
				return err
			}
		}
		for _, v := range plan.Activate {
			if err := ensureNoActiveOverlap(ctx, tx, p.TenantID, v); err != nil {
				return err
			}
		}
		if prev == domain.EcoSubmitted && e.State != domain.EcoRejected && e.State != domain.EcoCancelled {
			if err := emit(ctx, tx, p, e.ID, engevents.EcoApproved, aggregateEco, e.ID, e.EventData()); err != nil {
				return err
			}
		}
		for _, data := range plan.ExpiredEvents(e.ID) {
			if err := emit(ctx, tx, p, e.ID, engevents.BomVersionExpired, aggregateBomVersion, data.BomVersionID, data); err != nil {
				return err
			}
		}
		for _, data := range plan.ActivatedEvents() {
			if err := emit(ctx, tx, p, e.ID, engevents.BomVersionActivated, aggregateBomVersion, data.BomVersionID, data); err != nil {
				return err
			}
		}
		if e.State == domain.EcoReleased {
			return emit(ctx, tx, p, e.ID, engevents.EcoReleased, aggregateEco, e.ID, e.EventData())
		}
		return nil
	})
}

// ActivateBomVersion makes a draft BOM version active under the product lock and emits
// bomversion.activated. An already active version is returned unchanged with no second event. check
// sees the active and expired versions reachable from the draft and may refuse (cycle, overlap, or
// ENG-BOM-ECO-REQUIRED when the product and site already have a version). A draft that an open ECO
// will release is refused with ENG-BOM-IN-ECO, so the ECO release cannot find it already active.
func (s *Postgres) ActivateBomVersion(ctx context.Context, id string, check func(Snapshot, domain.BomVersion) error) (v domain.BomVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var done bool
		if v, done, err = lockedDraft(ctx, tx, p.TenantID, id, domain.StatusActive); err != nil || done {
			return err
		}
		if err := ensureNotInOpenEco(ctx, tx, p.TenantID, id, "an open ECO releases this draft; approve and release the ECO instead"); err != nil {
			return err
		}
		snapshot, err := loadSnapshot(ctx, tx, p.TenantID, versionRoots(v), false)
		if err != nil {
			return err
		}
		if err := check(snapshot, v); err != nil {
			return err
		}
		if err := draftTo(ctx, tx, p.TenantID, id, domain.StatusActive); err != nil {
			return err
		}
		v.Status, v.Version = domain.StatusActive, v.Version+1
		return emit(ctx, tx, p, id, engevents.BomVersionActivated, aggregateBomVersion, id, v.ActivatedEvent(""))
	})
	return v, err
}

// CancelBomVersion moves a draft BOM version to cancelled (BR-9.1). The row and its lines are kept,
// so the draft stays auditable, and an already cancelled version is returned unchanged. A draft that
// an open ECO will release is refused with ENG-BOM-IN-ECO; once that ECO is rejected or cancelled the
// draft can be cancelled. Creating a draft emits no event, so cancelling one emits none either.
func (s *Postgres) CancelBomVersion(ctx context.Context, id string) (v domain.BomVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var done bool
		if v, done, err = lockedDraft(ctx, tx, p.TenantID, id, domain.StatusCancelled); err != nil || done {
			return err
		}
		if err := ensureNotInOpenEco(ctx, tx, p.TenantID, id, "an open ECO releases this draft; reject or cancel the ECO first"); err != nil {
			return err
		}
		if err := draftTo(ctx, tx, p.TenantID, id, domain.StatusCancelled); err != nil {
			return err
		}
		v.Status, v.Version = domain.StatusCancelled, v.Version+1
		return nil
	})
	return v, err
}

// UpdateBomDraft replaces the header (site, effectivity, quantity range) and every line of a draft BOM
// version (BR-9.3). The product is fixed. v carries the new content under the stored id; expectedVersion
// is the version number the caller edited: another save in between answers ENG-BOM-VERSION-CONFLICT and
// nothing is written. Only a draft can be edited (ENG-BOM-STATE), and not one an open ECO will release
// (ENG-BOM-IN-ECO), because the approvers reviewed its lines. check runs the create-time cycle check
// under the product lock. The save adds 1 to version and emits no event, as creating a draft emits
// none. A replayed Idempotency-Key returns the version as it is now without writing again.
func (s *Postgres) UpdateBomDraft(ctx context.Context, v domain.BomVersion, expectedVersion int, idem Idem, check func(Snapshot) error) (updated domain.BomVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			current, err := loadVersions(ctx, tx, p.TenantID, `id = $2::uuid`, id)
			if err != nil || len(current) == 0 {
				return errors.Join(err, fmt.Errorf("idempotent BOM version %s is missing", id))
			}
			updated = current[0]
			return nil
		}
		stored, _, err := lockedDraft(ctx, tx, p.TenantID, v.ID, "")
		if err != nil {
			return err
		}
		if stored.Version != expectedVersion {
			return &domain.Error{Code: domain.CodeBomVersionConflict, Detail: fmt.Sprintf("the draft is at version %d, you edited version %d; reload it and merge your changes", stored.Version, expectedVersion)}
		}
		if err := ensureNotInOpenEco(ctx, tx, p.TenantID, v.ID, "an open ECO releases this draft; reject or cancel the ECO before editing it"); err != nil {
			return err
		}
		if v.ProductID != stored.ProductID {
			return &domain.Error{Code: domain.CodeBomState, Detail: "the product of a draft cannot change; copy the version to another product instead"}
		}
		snapshot, err := loadSnapshot(ctx, tx, p.TenantID, versionRoots(v), false)
		if err != nil {
			return err
		}
		if err := check(snapshot); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update bom_version set site_id = $3, effective_from = $4, effective_to = $5, from_qty = $6::numeric, to_qty = $7::numeric, version = version + 1
			where tenant_id = $1 and id = $2::uuid and status = 'draft' and version = $8`,
			p.TenantID, v.ID, v.SiteID, v.EffectiveFrom, zeroToNil(v.EffectiveTo), decimalToNil(v.FromQty), decimalToNil(v.ToQty), expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return &domain.Error{Code: domain.CodeBomVersionConflict, Detail: "the draft changed concurrently; reload it and merge your changes"}
		}
		if _, err := tx.Exec(ctx, `delete from bom_line where tenant_id = $1 and bom_version_id = $2::uuid`, p.TenantID, v.ID); err != nil {
			return err
		}
		if err := insertLines(ctx, tx, p.TenantID, v); err != nil {
			return err
		}
		if err := remember(ctx, tx, p.TenantID, idem, v.ID); err != nil {
			return err
		}
		updated = v
		updated.Status, updated.CopiedFrom, updated.Version = stored.Status, stored.CopiedFrom, expectedVersion+1
		return nil
	})
	return updated, conflictOnDuplicate(err)
}

// lockedDraft loads a BOM version, takes its product lock (the same lock activation and ECO release
// take) and loads it again. done is true when the version is already in target, which callers return
// unchanged; any other status than draft is refused with ENG-BOM-STATE.
func lockedDraft(ctx context.Context, tx pgx.Tx, tenantID, id, target string) (v domain.BomVersion, done bool, err error) {
	load := func() error {
		found, err := loadVersions(ctx, tx, tenantID, `id = $2::uuid`, id)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return notFound(CodeBomNotFound, "BOM version not found")
		}
		v = found[0]
		return nil
	}
	if err := load(); err != nil || v.Status == target {
		return v, err == nil, err
	}
	if err := lockProducts(ctx, tx, tenantID, v.ProductID); err != nil {
		return v, false, err
	}
	if err := load(); err != nil || v.Status == target {
		return v, err == nil, err
	}
	return v, false, v.EnsureDraft()
}

// ensureNotInOpenEco refuses with ENG-BOM-IN-ECO when an ECO that is not yet released, rejected or
// cancelled replaces an old version with this draft.
func ensureNotInOpenEco(ctx context.Context, tx pgx.Tx, tenantID, id, detail string) error {
	var pending bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from eco_replacement r join eco e on e.tenant_id = r.tenant_id and e.id = r.eco_id
		where r.tenant_id = $1 and r.new_version_id = $2::uuid and e.state not in ('released', 'rejected', 'cancelled'))`, tenantID, id).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return &domain.Error{Code: domain.CodeBomInEco, Detail: detail}
	}
	return nil
}

func draftTo(ctx context.Context, tx pgx.Tx, tenantID, id, status string) error {
	tag, err := tx.Exec(ctx, `update bom_version set status = $3, version = version + 1 where tenant_id = $1 and id = $2::uuid and status = 'draft'`, tenantID, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return &domain.Error{Code: domain.CodeBomState, Detail: "the BOM version changed concurrently, reload and retry"}
	}
	return nil
}
