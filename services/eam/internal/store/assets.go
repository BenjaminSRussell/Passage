package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
)

// Asset is an installed or purchasable piece of equipment (FR-44.2).
type Asset struct {
	ID                   string    `json:"id"`
	AssetNumber          string    `json:"assetNumber"`
	Name                 string    `json:"name"`
	AssetType            string    `json:"assetType"`
	SiteID               string    `json:"siteId"`
	Criticality          int       `json:"criticality"`
	LifecycleState       string    `json:"lifecycleState"`
	ProductionResourceID *string   `json:"productionResourceId,omitempty"`
	FunctionalLocationID *string   `json:"functionalLocationId,omitempty"`
	Version              int       `json:"version"`
	CreatedAt            time.Time `json:"createdAt"`
}

const assetColumns = `id::text, asset_number, name, asset_type, site_id::text, criticality, lifecycle_state,
  production_resource_id::text, functional_location_id::text, version, created_at`

func scanAsset(row pgx.Row) (Asset, error) {
	var a Asset
	err := row.Scan(&a.ID, &a.AssetNumber, &a.Name, &a.AssetType, &a.SiteID, &a.Criticality,
		&a.LifecycleState, &a.ProductionResourceID, &a.FunctionalLocationID, &a.Version, &a.CreatedAt)
	return a, err
}

func getAsset(ctx context.Context, tx pgx.Tx, t, id string, lock bool) (Asset, error) {
	q := `select ` + assetColumns + ` from asset where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	a, err := scanAsset(tx.QueryRow(ctx, q, t, id))
	return a, notFound(err, "asset")
}

// CreateAsset registers an asset in state CREATED with a generated asset number.
func (s *Postgres) CreateAsset(ctx context.Context, in Asset, key string) (Asset, error) {
	var out Asset
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "asset", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getAsset(ctx, tx, t, id, false)
			return gerr
		}
		id := ids.New()
		a, err := scanAsset(tx.QueryRow(ctx, `insert into asset (tenant_id, id, asset_number, name, asset_type, site_id,
		    criticality, production_resource_id, idempotency_key)
		  values ($1, $2, 'AST-' || nextval('asset_number_seq'), $3, $4, $5, $6, $7, $8)
		  returning `+assetColumns,
			t, id, in.Name, in.AssetType, in.SiteID, in.Criticality, in.ProductionResourceID, optStr(key)))
		out = a
		return err
	})
	return out, err
}

// ActivateAsset moves an asset to ACTIVE; the caller's version must match (If-Match).
func (s *Postgres) ActivateAsset(ctx context.Context, id string, version int) (Asset, error) {
	var out Asset
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		cur, err := getAsset(ctx, tx, t, id, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return problem(412, CodeVersion, "asset version changed")
		}
		if !domain.ActivationAllowed(cur.LifecycleState) {
			return problem(422, CodeAssetState, "asset is "+cur.LifecycleState+"; only CREATED or INACTIVE assets can be activated")
		}
		out, err = scanAsset(tx.QueryRow(ctx, `update asset set lifecycle_state = 'ACTIVE', version = version + 1,
		    updated_at = now() where tenant_id = $1 and id = $2 returning `+assetColumns, t, id))
		return err
	})
	return out, err
}

// Counter is a usage meter on an asset (FR-44.3).
type Counter struct {
	ID            string     `json:"id"`
	AssetID       string     `json:"assetId"`
	Name          string     `json:"name"`
	Unit          string     `json:"unit"`
	Cumulative    bool       `json:"cumulative"`
	Source        string     `json:"source"`
	LastReading   float64    `json:"lastReading"`
	LastReadingAt *time.Time `json:"lastReadingAt,omitempty"`
	AvgDailyUsage float64    `json:"avgDailyUsage"`
}

const counterColumns = `id::text, asset_id::text, name, unit, cumulative, source,
  last_reading::float8, last_reading_at, avg_daily_usage::float8`

func scanCounter(row pgx.Row) (Counter, error) {
	var c Counter
	err := row.Scan(&c.ID, &c.AssetID, &c.Name, &c.Unit, &c.Cumulative, &c.Source,
		&c.LastReading, &c.LastReadingAt, &c.AvgDailyUsage)
	return c, err
}

// CreateCounter adds a counter to an existing asset.
func (s *Postgres) CreateCounter(ctx context.Context, assetID string, in Counter, key string) (Counter, error) {
	var out Counter
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		if id, err := existingID(ctx, tx, "counter", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getCounter(ctx, tx, t, id)
			return gerr
		}
		if dup, err := taken(ctx, tx, "counter", "tenant_id = $1 and asset_id = $2 and name = $3", t, assetID, in.Name); err != nil || dup {
			if err != nil {
				return err
			}
			return problem(409, CodeDuplicate, "the asset already has a counter named "+in.Name)
		}
		id := ids.New()
		_, err := tx.Exec(ctx, `insert into counter (tenant_id, id, asset_id, name, unit, cumulative, source, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			t, id, assetID, in.Name, in.Unit, in.Cumulative, in.Source, optStr(key))
		if err != nil {
			return err
		}
		out, err = getCounter(ctx, tx, t, id)
		return err
	})
	return out, err
}

func getCounter(ctx context.Context, tx pgx.Tx, t, id string) (Counter, error) {
	c, err := scanCounter(tx.QueryRow(ctx, `select `+counterColumns+` from counter where tenant_id = $1 and id = $2`, t, id))
	return c, notFound(err, "counter")
}

// Rule is a threshold condition on a counter or IoT tag (BR-44.4).
type Rule struct {
	ID              string  `json:"id"`
	CounterID       string  `json:"counterId"`
	Name            string  `json:"name"`
	Operator        string  `json:"operator"`
	Threshold       float64 `json:"threshold"`
	Severity        int     `json:"severity"`
	CooldownMinutes int     `json:"cooldownMinutes"`
}

// CreateRule attaches a condition rule to a counter.
func (s *Postgres) CreateRule(ctx context.Context, counterID string, in Rule, key string) (Rule, error) {
	in.CounterID = counterID
	if in.CooldownMinutes == 0 {
		in.CooldownMinutes = int(domain.DefaultCooldown.Minutes())
	}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getCounter(ctx, tx, t, counterID); err != nil {
			return err
		}
		if id, err := existingID(ctx, tx, "condition_rule", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			in, gerr = getRule(ctx, tx, t, id)
			if gerr == nil && in.CounterID != counterID {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used for a rule on another counter")
			}
			return gerr
		}
		in.ID = ids.New()
		_, err := tx.Exec(ctx, `insert into condition_rule (tenant_id, id, counter_id, name, operator, threshold,
		    severity, cooldown_minutes, idempotency_key)
		  values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9)`,
			t, in.ID, counterID, in.Name, in.Operator, numText(in.Threshold), in.Severity, in.CooldownMinutes, optStr(key))
		return err
	})
	return in, err
}

func getRule(ctx context.Context, tx pgx.Tx, t, id string) (Rule, error) {
	var r Rule
	err := tx.QueryRow(ctx, `select id::text, counter_id::text, name, operator, threshold::float8, severity, cooldown_minutes
	  from condition_rule where tenant_id = $1 and id = $2`, t, id).
		Scan(&r.ID, &r.CounterID, &r.Name, &r.Operator, &r.Threshold, &r.Severity, &r.CooldownMinutes)
	return r, notFound(err, "condition rule")
}

// ListAssets returns a page of assets, newest first, optionally in one lifecycle state.
func (s *Postgres) ListAssets(ctx context.Context, opt ListOptions) (Page[Asset], error) {
	var out Page[Asset]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		rows, err := tx.Query(ctx, `select `+assetColumns+` from asset
		  where tenant_id = $1 and ($2::text is null or lifecycle_state = $2) and ($3::uuid is null or id < $3)
		  order by id desc limit $4`, t, optStr(opt.Status), optStr(opt.Cursor), opt.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		var items []Asset
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return err
			}
			items = append(items, a)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(a Asset) string { return a.ID })
		return nil
	})
	return out, err
}

// AssetDetail is an asset with its counters, their rules and its cost rollup (8.44.6 Asset detail).
type AssetDetail struct {
	Asset
	Counters       []Counter `json:"counters"`
	Rules          []Rule    `json:"rules"`
	OpenWorkOrders int       `json:"openWorkOrders"`
	PartsCost      string    `json:"partsCost"`
	LaborCost      string    `json:"laborCost"`
	// Location is the functional location the asset is installed at (FR-44.1).
	Location *FunctionalLocation `json:"location,omitempty"`
}

// GetAsset returns one asset with its counters, rules, current functional
// location and the sum of its work order costs.
func (s *Postgres) GetAsset(ctx context.Context, id string) (AssetDetail, error) {
	var out AssetDetail
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		a, err := getAsset(ctx, tx, t, id, false)
		if err != nil {
			return err
		}
		out = AssetDetail{Asset: a, Counters: []Counter{}, Rules: []Rule{}}
		if a.FunctionalLocationID != nil {
			loc, err := getLocation(ctx, tx, t, *a.FunctionalLocationID)
			if err != nil {
				return err
			}
			out.Location = &loc
		}
		rows, err := tx.Query(ctx, `select `+counterColumns+` from counter where tenant_id = $1 and asset_id = $2 order by name`, t, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			c, err := scanCounter(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out.Counters = append(out.Counters, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select r.id::text, r.counter_id::text, r.name, r.operator, r.threshold::float8, r.severity,
		    r.cooldown_minutes
		  from condition_rule r join counter c on c.tenant_id = r.tenant_id and c.id = r.counter_id
		  where r.tenant_id = $1 and c.asset_id = $2 order by r.name`, t, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var r Rule
			if err := rows.Scan(&r.ID, &r.CounterID, &r.Name, &r.Operator, &r.Threshold, &r.Severity, &r.CooldownMinutes); err != nil {
				rows.Close()
				return err
			}
			out.Rules = append(out.Rules, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*) filter (where status not in ('COMPLETED', 'CLOSED', 'CANCELED')),
		    coalesce(sum(parts_cost), 0)::text, coalesce(sum(labor_cost), 0)::text
		  from work_order where tenant_id = $1 and asset_id = $2`, t, id).
			Scan(&out.OpenWorkOrders, &out.PartsCost, &out.LaborCost)
	})
	return out, err
}

// Workspace holds the asset management workspace tiles (8.44.6).
type Workspace struct {
	RequestsToTriage int `json:"requestsToTriage"`
	DueIn7Days       int `json:"dueIn7Days"`
	Overdue          int `json:"overdue"`
	OpenWorkOrders   int `json:"openWorkOrders"`
}

// Workspace counts the open requests, the open work orders and those due in
// the next 7 days or already overdue, as of the repository clock.
func (s *Postgres) Workspace(ctx context.Context) (Workspace, error) {
	var out Workspace
	now := s.now()
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if err := tx.QueryRow(ctx, `select count(*) from maintenance_request
		  where tenant_id = $1 and status in ('NEW', 'TRIAGED')`, t).Scan(&out.RequestsToTriage); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select count(*),
		    count(*) filter (where due_at >= $2 and due_at < $2 + interval '7 days'),
		    count(*) filter (where due_at < $2)
		  from work_order where tenant_id = $1 and status not in ('COMPLETED', 'CLOSED', 'CANCELED')`, t, now).
			Scan(&out.OpenWorkOrders, &out.DueIn7Days, &out.Overdue)
	})
	return out, err
}
