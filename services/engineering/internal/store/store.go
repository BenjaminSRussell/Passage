package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	eventSource          = "/passage/engineering/engineering"
	uniqueViolation      = "23505"
	aggregateBomVersion  = "bomversion"
	aggregateEco         = "eco"
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

type BomVersionPage struct {
	Items  []domain.BomVersion
	NextID string
}

type EcoRecord struct {
	Eco      domain.Eco
	Versions []domain.BomVersion
}

type Store interface {
	CreateBomVersion(ctx context.Context, v domain.BomVersion, idem Idem, check func(Snapshot) error) (domain.BomVersion, error)
	ListBomVersions(ctx context.Context, afterID string, limit int) (BomVersionPage, error)
	Catalog(ctx context.Context, roots []string, up bool) (Snapshot, error)
	CreateEco(ctx context.Context, e domain.Eco, idem Idem) (domain.Eco, error)
	GetEco(ctx context.Context, id string) (EcoRecord, error)
	ReleaseEco(ctx context.Context, e domain.Eco, prev domain.EcoState, plan domain.ReleasePlan) error
	Ping(ctx context.Context) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const (
	versionColumns = `id::text, product_id, site_id, effective_from, effective_to, from_qty::text, to_qty::text, status`
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
		if err := rows.Scan(&v.ID, &v.ProductID, &v.SiteID, &v.EffectiveFrom, &to, &fromQty, &toQty, &v.Status); err != nil {
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
	if _, err := tx.Exec(ctx, `insert into bom_version (tenant_id, id, product_id, site_id, effective_from, effective_to, from_qty, to_qty, status)
		values ($1, $2, $3, $4, $5, $6, $7::numeric, $8::numeric, $9)`,
		tenantID, v.ID, v.ProductID, v.SiteID, v.EffectiveFrom, zeroToNil(v.EffectiveTo), decimalToNil(v.FromQty), decimalToNil(v.ToQty), v.Status); err != nil {
		return err
	}
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
		return remember(ctx, tx, p.TenantID, idem, v.ID)
	})
	return created, conflictOnDuplicate(err)
}

func (s *Postgres) ListBomVersions(ctx context.Context, afterID string, limit int) (BomVersionPage, error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var page BomVersionPage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		page.Items, err = queryVersions(ctx, tx, `select `+versionColumns+` from bom_version v where tenant_id = $1 and id > $2::uuid order by id limit $3`, p.TenantID, afterID, limit+1)
		return err
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextID = page.Items[limit-1].ID
	}
	return page, err
}

func getEco(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.Eco, error) {
	var e domain.Eco
	var releaseDate *time.Time
	err := tx.QueryRow(ctx, `select id::text, number, state, release_date, decided_by from eco where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&e.ID, &e.Number, &e.State, &releaseDate, &e.DecidedBy)
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
		if err := tx.QueryRow(ctx, `select count(*) from bom_version where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, versionIDs).Scan(&known); err != nil {
			return err
		}
		if known != len(versionIDs) {
			return httpx.NewProblem(http.StatusNotFound, CodeVersionNotFound, "a replaced or replacing BOM version does not exist")
		}
		if _, err := tx.Exec(ctx, `insert into eco (tenant_id, id, number, state, release_date) values ($1, $2, $3, $4, $5)`,
			p.TenantID, e.ID, e.Number, string(e.State), zeroToNil(e.ReleaseDate)); err != nil {
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

func transition(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	tag, err := tx.Exec(ctx, query, args...)
	if err == nil && tag.RowsAffected() != 1 {
		return httpx.NewProblem(http.StatusConflict, domain.CodeEcoState, "the ECO or its BOM versions changed concurrently, reload and retry")
	}
	return err
}

func (s *Postgres) ReleaseEco(ctx context.Context, e domain.Eco, prev domain.EcoState, plan domain.ReleasePlan) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := transition(ctx, tx, `update eco set state = $3, decided_by = $4, version = version + 1 where tenant_id = $1 and id = $2::uuid and state = $5`,
			p.TenantID, e.ID, string(e.State), e.DecidedBy, string(prev)); err != nil {
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
		if prev == domain.EcoSubmitted && e.State != domain.EcoRejected {
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
