package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/product/internal/events"
)

const (
	uniqueViolation = "23505"
	MaxPage         = 500
)

type Store interface {
	Ping(ctx context.Context) error
	CreateDimensionGroup(ctx context.Context, in domain.DimensionGroup) (domain.DimensionGroup, error)
	GetDimensionGroup(ctx context.Context, id string) (domain.DimensionGroup, error)
	CreateItemGroup(ctx context.Context, in domain.ItemGroup) (domain.ItemGroup, error)
	CreateItemModelGroup(ctx context.Context, in domain.ItemModelGroup) (domain.ItemModelGroup, error)
	CreateProduct(ctx context.Context, in domain.Product, idem string) (domain.Product, error)
	GetProduct(ctx context.Context, id string) (domain.Product, error)
	GetSavedConfiguration(ctx context.Context, id, productID string) (domain.ConfigurationRef, error)
	ListProducts(ctx context.Context, limit int) ([]domain.Product, error)
	ExplodeVariants(ctx context.Context, masterID string, selected map[string][]string, limit int) (domain.ExplosionResult, error)
	ListReleasedIDs(ctx context.Context, after string, limit int) ([]string, error)
	LookupVariant(ctx context.Context, masterID string, values map[string]string) (domain.Product, error)
	Release(ctx context.Context, productID string, defaults domain.ReleaseDefaults, itemNumber string) (domain.ReleasedProduct, error)
	GetReleased(ctx context.Context, id string) (domain.ReleasedProduct, error)
	PatchReleased(ctx context.Context, id string, patch domain.ReleasedProduct) (domain.ReleasedProduct, error)
	PutSiteDefault(ctx context.Context, releasedID string, site domain.SiteDefault) (domain.SiteDefault, error)
	ResolveSiteDefault(ctx context.Context, releasedID, siteID string) (domain.SiteDefault, error)
	AddIdentifier(ctx context.Context, productID, value, uom string, allocate bool) (domain.Identifier, error)
	GetIdentifier(ctx context.Context, value string) (domain.Identifier, error)
	SetAttributes(ctx context.Context, productID string, values map[string]string) error
	AddRelation(ctx context.Context, in domain.Relation) ([]domain.Relation, error)
	EvaluatePolicy(ctx context.Context, releasedID, process string) (domain.ProcessDecision, error)
	ConvertUOM(ctx context.Context, productID, from, to, qty string) (string, error)
	Search(ctx context.Context, q string, limit int) ([]domain.Product, error)
	SeedLifecycle(ctx context.Context) error
	UpsertAttribute(ctx context.Context, def domain.AttributeType) error
	SetGS1Prefix(ctx context.Context, prefix string, next int64) error
	AddProductConversion(ctx context.Context, productID string, c domain.Conversion) error
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func principal(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return tenant.Principal{}, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing tenant")
	}
	return p, nil
}

func problem(err error, code, detail string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, code, detail)
	}
	return err
}

func domainProblem(err error) error {
	var d *domain.Error
	if errors.As(err, &d) {
		status := http.StatusUnprocessableEntity
		if d.Code == domain.CodeDimGroupLocked || d.Code == domain.CodeCfgTimeout {
			status = http.StatusConflict
		}
		if d.Code == domain.CodeReleaseDraft {
			status = http.StatusUnprocessableEntity
		}
		return httpx.NewProblem(status, d.Code, d.Detail)
	}
	return err
}

func emit(ctx context.Context, tx pgx.Tx, typ, aggregate, id string, data any) error {
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(typ, "passage://product", id, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregate, id, evt)
}

func (s *Postgres) CreateDimensionGroup(ctx context.Context, in domain.DimensionGroup) (domain.DimensionGroup, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.DimensionGroup{}, err
	}
	if in.ID == "" {
		in.ID = ids.New()
	}
	raw, err := json.Marshal(in.Fields)
	if err != nil {
		return domain.DimensionGroup{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into dimension_group (tenant_id, id, code, name, kind, fields) values ($1,$2,$3,$4,$5,$6)`,
			p.TenantID, in.ID, in.Code, in.Name, in.Kind, raw)
		return err
	})
	return in, problem(err, "PRD-NUM-FORMAT", "dimension group code already exists")
}

func (s *Postgres) GetDimensionGroup(ctx context.Context, id string) (domain.DimensionGroup, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.DimensionGroup{}, err
	}
	var g domain.DimensionGroup
	var raw []byte
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, code, name, kind, fields from dimension_group where tenant_id=$1 and id=$2`, p.TenantID, id).
			Scan(&g.ID, &g.Code, &g.Name, &g.Kind, &raw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return g, httpx.NewProblem(http.StatusNotFound, "COM-0001", "dimension group not found")
	}
	if err != nil {
		return g, err
	}
	_ = json.Unmarshal(raw, &g.Fields)
	return g, nil
}

func (s *Postgres) CreateItemGroup(ctx context.Context, in domain.ItemGroup) (domain.ItemGroup, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ItemGroup{}, err
	}
	if in.ID == "" {
		in.ID = ids.New()
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into item_group (tenant_id, id, code, name) values ($1,$2,$3,$4)`, p.TenantID, in.ID, in.Code, in.Name)
		return err
	})
	return in, problem(err, "PRD-NUM-FORMAT", "item group code already exists")
}

func (s *Postgres) CreateItemModelGroup(ctx context.Context, in domain.ItemModelGroup) (domain.ItemModelGroup, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ItemModelGroup{}, err
	}
	if in.ID == "" {
		in.ID = ids.New()
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into item_model_group (tenant_id, id, code, name, costing_method, stocked, physical_negative, financial_negative, post_physical, post_financial)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.TenantID, in.ID, in.Code, in.Name, in.CostingMethod, in.Stocked, in.PhysicalNegative, in.FinancialNegative, in.PostPhysical, in.PostFinancial)
		return err
	})
	return in, problem(err, "PRD-NUM-FORMAT", "item model group code already exists")
}

func (s *Postgres) CreateProduct(ctx context.Context, in domain.Product, idem string) (domain.Product, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Product{}, err
	}
	if err := domain.ValidateProductNumber(in.ProductNumber); err != nil {
		return domain.Product{}, domainProblem(err)
	}
	if in.ID == "" {
		in.ID = ids.New()
	}
	if in.LifecycleState == "" {
		in.LifecycleState = domain.StateDraft
	}
	if in.SearchName == "" {
		in.SearchName = domain.SearchName(in)
	}
	if in.DimensionValues == nil {
		in.DimensionValues = map[string]string{}
	}
	vals, _ := json.Marshal(in.DimensionValues)
	svc, _ := json.Marshal(in.Service)
	if in.Service == nil {
		svc = nil
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem != "" {
			var existing string
			qerr := tx.QueryRow(ctx, `select response_body->>'id' from idempotency_keys where tenant_id=$1 and key=$2`, p.TenantID, idem).Scan(&existing)
			if qerr == nil && existing != "" {
				return tx.QueryRow(ctx, `select id, product_number, subtype, coalesce(master_id::text,''), product_type, name, lifecycle_state, version from product where tenant_id=$1 and id=$2`, p.TenantID, existing).
					Scan(&in.ID, &in.ProductNumber, &in.Subtype, &in.MasterID, &in.ProductType, &in.Name, &in.LifecycleState, &in.Version)
			}
		}
		_, err := tx.Exec(ctx, `insert into product (tenant_id, id, product_number, subtype, master_id, product_type, name, description, search_name, base_uom, dimension_group_id, storage_dimension_group_id, tracking_dimension_group_id, lifecycle_state, dimension_values, variant_hash, service)
			values ($1,$2,$3,$4,nullif($5,'')::uuid,$6,$7,$8,$9,$10,nullif($11,'')::uuid,nullif($12,'')::uuid,nullif($13,'')::uuid,$14,$15,$16,$17)`,
			p.TenantID, in.ID, in.ProductNumber, in.Subtype, in.MasterID, in.ProductType, in.Name, in.Description, in.SearchName, in.BaseUOM,
			in.DimensionGroupID, in.StorageDimensionGroupID, in.TrackingDimensionGroupID, in.LifecycleState, vals, in.VariantHash, svc)
		if err != nil {
			return err
		}
		if idem != "" {
			body, _ := json.Marshal(map[string]string{"id": in.ID})
			if _, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1,$2,$3,201,$4)`,
				p.TenantID, idem, in.ProductNumber, body); err != nil {
				return err
			}
		}
		return emit(ctx, tx, pevents.ProductCreated, "product", in.ID, pevents.ProductCreatedData{
			ProductID: in.ID, ProductNumber: in.ProductNumber, Subtype: in.Subtype, ProductType: in.ProductType, BaseUOM: in.BaseUOM,
		})
	})
	return in, problem(err, domain.CodeNumFormat, "product number already exists")
}

func scanProduct(row pgx.Row) (domain.Product, error) {
	var p domain.Product
	var master, dim, storage, tracking *string
	var vals, svc []byte
	err := row.Scan(&p.ID, &p.ProductNumber, &p.Subtype, &master, &p.ProductType, &p.Name, &p.Description, &p.SearchName, &p.BaseUOM,
		&dim, &storage, &tracking, &p.LifecycleState, &vals, &p.VariantHash, &svc, &p.Version)
	if master != nil {
		p.MasterID = *master
	}
	if dim != nil {
		p.DimensionGroupID = *dim
	}
	if storage != nil {
		p.StorageDimensionGroupID = *storage
	}
	if tracking != nil {
		p.TrackingDimensionGroupID = *tracking
	}
	_ = json.Unmarshal(vals, &p.DimensionValues)
	if len(svc) > 0 {
		var sa domain.ServiceAttributes
		if json.Unmarshal(svc, &sa) == nil {
			p.Service = &sa
		}
	}
	return p, err
}

const productSelect = `id, product_number, subtype, master_id::text, product_type, name, description, search_name, base_uom,
	dimension_group_id::text, storage_dimension_group_id::text, tracking_dimension_group_id::text, lifecycle_state, dimension_values, variant_hash, service, version`

func (s *Postgres) GetProduct(ctx context.Context, id string) (domain.Product, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Product{}, err
	}
	var out domain.Product
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var e error
		out, e = scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and id=$2`, p.TenantID, id))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewProblem(http.StatusNotFound, "COM-0001", "product not found")
	}
	return out, err
}

func (s *Postgres) GetSavedConfiguration(ctx context.Context, id, productID string) (domain.ConfigurationRef, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ConfigurationRef{}, err
	}
	var out domain.ConfigurationRef
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select c.id::text, c.model_id::text, m.master_id::text, c.status,
				exists (select 1 from product x where x.tenant_id = c.tenant_id and x.id = $3::uuid and (x.id = m.master_id or x.master_id = m.master_id))
			from saved_configuration c
			join configuration_model m on m.tenant_id = c.tenant_id and m.id = c.model_id
			where c.tenant_id = $1 and c.id = $2`, p.TenantID, id, productID).
			Scan(&out.ID, &out.ModelID, &out.MasterID, &out.Status, &out.BelongsToProduct)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewProblem(http.StatusNotFound, "PRD-CONFIGURATION-NOT-FOUND", "saved configuration not found")
	}
	return out, err
}

func (s *Postgres) ListProducts(ctx context.Context, limit int) ([]domain.Product, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []domain.Product
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+productSelect+` from product where tenant_id=$1 and subtype <> 'variant' order by product_number limit $2`, p.TenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanProduct(rows)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	if out == nil {
		out = []domain.Product{}
	}
	return out, err
}

func (s *Postgres) ListReleasedIDs(ctx context.Context, after string, limit int) ([]string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxPage {
		limit = MaxPage
	}
	out := []string{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from released_product where tenant_id=$1 and (nullif($2,'')::uuid is null or id > nullif($2,'')::uuid) order by id limit $3`, p.TenantID, after, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) ExplodeVariants(ctx context.Context, masterID string, selected map[string][]string, limit int) (domain.ExplosionResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ExplosionResult{}, err
	}
	var result domain.ExplosionResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		master, err := scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and id=$2`, p.TenantID, masterID))
		if err != nil {
			return err
		}
		if master.Subtype != domain.SubtypeMaster {
			return httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", "not a product master")
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `select fields from dimension_group where tenant_id=$1 and id=$2`, p.TenantID, master.DimensionGroupID).Scan(&raw); err != nil {
			return err
		}
		var dims []domain.Dimension
		if err := json.Unmarshal(raw, &dims); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select dimension_values, variant_hash, product_number from product where tenant_id=$1 and master_id=$2`, p.TenantID, masterID)
		if err != nil {
			return err
		}
		var existing []domain.Variant
		for rows.Next() {
			var v domain.Variant
			var dv []byte
			if err := rows.Scan(&dv, &v.VariantHash, &v.ProductNumber); err != nil {
				rows.Close()
				return err
			}
			_ = json.Unmarshal(dv, &v.DimensionValues)
			existing = append(existing, v)
		}
		rows.Close()
		result, err = domain.Explode(existing, domain.ExplosionRequest{MasterNumber: master.ProductNumber, Dimensions: dims, Selected: selected})
		if err != nil {
			return domainProblem(err)
		}
		if limit > 0 && len(result.Created) > limit {
			result.Remaining = len(result.Created) - limit
			result.Created = result.Created[:limit]
		}
		var idsCreated []string
		var values []map[string]string
		for i, v := range result.Created {
			id := ids.New()
			dv, _ := json.Marshal(v.DimensionValues)
			if _, err := tx.Exec(ctx, `insert into product (tenant_id, id, product_number, subtype, master_id, product_type, name, search_name, base_uom, dimension_group_id, lifecycle_state, dimension_values, variant_hash)
				values ($1,$2,$3,'variant',$4,$5,$6,$6,$7,$8,$9,$10,$11)`,
				p.TenantID, id, v.ProductNumber, masterID, master.ProductType, master.Name+" "+v.ProductNumber, master.BaseUOM, master.DimensionGroupID, domain.StateDraft, dv, v.VariantHash); err != nil {
				return err
			}
			idsCreated = append(idsCreated, id)
			values = append(values, v.DimensionValues)
			if (i+1)%domain.ExplodeCheckpoint == 0 {
				if err := emit(ctx, tx, pevents.VariantCreated, "product", masterID, pevents.VariantCreatedData{MasterID: masterID, VariantIDs: idsCreated, DimensionValues: values}); err != nil {
					return err
				}
				idsCreated, values = nil, nil
			}
		}
		if len(idsCreated) > 0 {
			return emit(ctx, tx, pevents.VariantCreated, "product", masterID, pevents.VariantCreatedData{MasterID: masterID, VariantIDs: idsCreated, DimensionValues: values})
		}
		return nil
	})
	return result, err
}

func (s *Postgres) LookupVariant(ctx context.Context, masterID string, values map[string]string) (domain.Product, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Product{}, err
	}
	hash := domain.VariantHash(values)
	var out domain.Product
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var e error
		out, e = scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and master_id=$2 and variant_hash=$3`, p.TenantID, masterID, hash))
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, httpx.NewProblem(http.StatusNotFound, "COM-0001", "variant not found")
	}
	return out, err
}

func (s *Postgres) Release(ctx context.Context, productID string, defaults domain.ReleaseDefaults, itemNumber string) (domain.ReleasedProduct, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ReleasedProduct{}, err
	}
	var rp domain.ReleasedProduct
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		prod, err := scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and id=$2`, p.TenantID, productID))
		if err != nil {
			return err
		}
		rp, err = domain.ApplyReleaseProfile(prod, defaults, itemNumber)
		if err != nil {
			return domainProblem(err)
		}
		rp.ID = ids.New()
		if _, err := tx.Exec(ctx, `insert into released_product (tenant_id, id, product_id, legal_entity_id, item_number, item_group_id, item_model_group_id, default_site_id, default_warehouse_id, lifecycle_state)
			values ($1,$2,$3,$4,$5,$6,$7,nullif($8,'')::uuid,nullif($9,'')::uuid,$10)`,
			p.TenantID, rp.ID, productID, defaults.LegalEntityID, rp.ItemNumber, defaults.ItemGroupID, defaults.ItemModelGroupID, rp.DefaultSiteID, rp.DefaultWarehouseID, rp.LifecycleState); err != nil {
			return err
		}
		for _, site := range rp.SiteDefaults {
			if _, err := tx.Exec(ctx, `insert into released_product_site_default (tenant_id, released_product_id, legal_entity_id, site_id, warehouse_id, min_qty, multiple_qty, std_qty, lead_time_days)
				values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, rp.ID, defaults.LegalEntityID, site.SiteID, site.WarehouseID, site.MinQty, site.MultipleQty, site.StdQty, site.LeadTimeDays); err != nil {
				return err
			}
		}
		return emit(ctx, tx, pevents.ProductReleased, "released_product", rp.ID, pevents.ProductReleasedData{
			ProductID: productID, ReleasedProductID: rp.ID, LegalEntityID: defaults.LegalEntityID, ItemNumber: rp.ItemNumber,
			ItemGroupID: defaults.ItemGroupID, ItemModelGroupID: defaults.ItemModelGroupID, DefaultSiteID: rp.DefaultSiteID, DefaultWarehouseID: rp.DefaultWarehouseID,
		})
	})
	return rp, problem(err, "PRD-RELEASE-DEFAULT-MISSING", "released product already exists for this legal entity")
}

func (s *Postgres) loadReleased(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.ReleasedProduct, error) {
	var rp domain.ReleasedProduct
	err := tx.QueryRow(ctx, `select id, product_id, legal_entity_id, item_number, item_group_id, item_model_group_id, coalesce(default_site_id::text,''), coalesce(default_warehouse_id::text,''), lifecycle_state, blocked_purchase, blocked_sales, blocked_inventory, blocked_production, has_financial_txn, version
		from released_product where tenant_id=$1 and id=$2`, tenantID, id).Scan(
		&rp.ID, &rp.ProductID, &rp.LegalEntityID, &rp.ItemNumber, &rp.ItemGroupID, &rp.ItemModelGroupID, &rp.DefaultSiteID, &rp.DefaultWarehouseID,
		&rp.LifecycleState, &rp.BlockedPurchase, &rp.BlockedSales, &rp.BlockedInventory, &rp.BlockedProduction, &rp.HasFinancialTxn, &rp.Version)
	if err != nil {
		return rp, err
	}
	rows, err := tx.Query(ctx, `select site_id, warehouse_id, min_qty, multiple_qty, std_qty, lead_time_days from released_product_site_default where tenant_id=$1 and released_product_id=$2`, tenantID, id)
	if err != nil {
		return rp, err
	}
	defer rows.Close()
	rp.SiteDefaults = map[string]domain.SiteDefault{}
	for rows.Next() {
		var d domain.SiteDefault
		if err := rows.Scan(&d.SiteID, &d.WarehouseID, &d.MinQty, &d.MultipleQty, &d.StdQty, &d.LeadTimeDays); err != nil {
			return rp, err
		}
		rp.SiteDefaults[d.SiteID] = d
	}
	return rp, rows.Err()
}

func (s *Postgres) GetReleased(ctx context.Context, id string) (domain.ReleasedProduct, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ReleasedProduct{}, err
	}
	var rp domain.ReleasedProduct
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var e error
		rp, e = s.loadReleased(ctx, tx, p.TenantID, id)
		return e
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return rp, httpx.NewProblem(http.StatusNotFound, "COM-0001", "released product not found")
	}
	return rp, err
}

func (s *Postgres) PatchReleased(ctx context.Context, id string, patch domain.ReleasedProduct) (domain.ReleasedProduct, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ReleasedProduct{}, err
	}
	var rp domain.ReleasedProduct
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := s.loadReleased(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		if patch.HasFinancialTxn || cur.HasFinancialTxn {
			if err := domain.AssertDimensionGroupUnlocked(true); patch.ItemModelGroupID != "" && patch.ItemModelGroupID != cur.ItemModelGroupID && err != nil {
				return domainProblem(err)
			}
		}
		if patch.LifecycleState != "" && patch.LifecycleState != cur.LifecycleState {
			prod, err := scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and id=$2`, p.TenantID, cur.ProductID))
			if err != nil {
				return err
			}
			if err := domain.AssertLifecycleTransition(cur.LifecycleState, patch.LifecycleState, prod.LifecycleState); err != nil {
				return domainProblem(err)
			}
			cur.LifecycleState = patch.LifecycleState
		}
		cur.BlockedPurchase = patch.BlockedPurchase
		cur.BlockedSales = patch.BlockedSales
		cur.BlockedInventory = patch.BlockedInventory
		cur.BlockedProduction = patch.BlockedProduction
		if patch.HasFinancialTxn {
			cur.HasFinancialTxn = true
		}
		if _, err := tx.Exec(ctx, `update released_product set lifecycle_state=$3, blocked_purchase=$4, blocked_sales=$5, blocked_inventory=$6, blocked_production=$7, has_financial_txn=$8, version=version+1 where tenant_id=$1 and id=$2`,
			p.TenantID, id, cur.LifecycleState, cur.BlockedPurchase, cur.BlockedSales, cur.BlockedInventory, cur.BlockedProduction, cur.HasFinancialTxn); err != nil {
			return err
		}
		rp = cur
		return emit(ctx, tx, pevents.ReleasedUpdated, "released_product", id, pevents.ReleasedUpdatedData{
			ReleasedProductID: id, ChangedFields: []string{"lifecycleState", "blocks"}, LifecycleState: cur.LifecycleState,
			Blocks: map[string]bool{"purchase": cur.BlockedPurchase, "sales": cur.BlockedSales, "inventory": cur.BlockedInventory, "production": cur.BlockedProduction},
		})
	})
	return rp, err
}

func (s *Postgres) PutSiteDefault(ctx context.Context, releasedID string, site domain.SiteDefault) (domain.SiteDefault, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.SiteDefault{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rp, err := s.loadReleased(ctx, tx, p.TenantID, releasedID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into released_product_site_default (tenant_id, released_product_id, legal_entity_id, site_id, warehouse_id, min_qty, multiple_qty, std_qty, lead_time_days)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			on conflict (tenant_id, released_product_id, site_id) do update set warehouse_id=excluded.warehouse_id, min_qty=excluded.min_qty, multiple_qty=excluded.multiple_qty, std_qty=excluded.std_qty, lead_time_days=excluded.lead_time_days`,
			p.TenantID, releasedID, rp.LegalEntityID, site.SiteID, site.WarehouseID, site.MinQty, site.MultipleQty, site.StdQty, site.LeadTimeDays)
		return err
	})
	return site, err
}

func (s *Postgres) ResolveSiteDefault(ctx context.Context, releasedID, siteID string) (domain.SiteDefault, error) {
	rp, err := s.GetReleased(ctx, releasedID)
	if err != nil {
		return domain.SiteDefault{}, err
	}
	got, err := domain.ResolveSiteDefault(domain.ReleaseDefaults{
		LegalEntityID: rp.LegalEntityID, DefaultSiteID: rp.DefaultSiteID, DefaultWarehouseID: rp.DefaultWarehouseID, SiteDefaults: rp.SiteDefaults,
	}, siteID)
	if err != nil {
		return domain.SiteDefault{}, domainProblem(err)
	}
	return got, nil
}

func (s *Postgres) AddIdentifier(ctx context.Context, productID, value, uom string, allocate bool) (domain.Identifier, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Identifier{}, err
	}
	var idn domain.Identifier
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if allocate {
			var prefix string
			var next int64
			if err := tx.QueryRow(ctx, `select prefix, next_number from gs1_prefix where tenant_id=$1`, p.TenantID).Scan(&prefix, &next); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					prefix, next = "0614141", 0
					if _, err := tx.Exec(ctx, `insert into gs1_prefix (tenant_id, prefix, next_number) values ($1,$2,$3)`, p.TenantID, prefix, next); err != nil {
						return err
					}
				} else {
					return err
				}
			}
			gtin, nxt, err := domain.AllocateGTIN13(prefix, next)
			if err != nil {
				return domainProblem(err)
			}
			value = gtin
			if _, err := tx.Exec(ctx, `update gs1_prefix set next_number=$2 where tenant_id=$1`, p.TenantID, nxt); err != nil {
				return err
			}
		}
		if err := domain.ValidateGTIN(value); err != nil {
			return domainProblem(err)
		}
		idn = domain.Identifier{ProductID: productID, UOM: uom, Type: domain.IdentifierTypeForLength(value), Value: value, Primary: true}
		if _, err := tx.Exec(ctx, `insert into product_identifier (tenant_id, id, product_id, uom, type, value, is_primary) values ($1,$2,$3,$4,$5,$6,true)`,
			p.TenantID, ids.New(), productID, uom, idn.Type, value); err != nil {
			return err
		}
		return emit(ctx, tx, pevents.IdentifierAdded, "product", productID, pevents.IdentifierAddedData{ProductID: productID, Type: idn.Type, Value: value, UOM: uom})
	})
	return idn, problem(err, domain.CodeGTINDuplicate, "GTIN already belongs to another product")
}

func (s *Postgres) GetIdentifier(ctx context.Context, value string) (domain.Identifier, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.Identifier{}, err
	}
	var idn domain.Identifier
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select product_id, uom, type, value, is_primary from product_identifier where tenant_id=$1 and value=$2`, p.TenantID, value).
			Scan(&idn.ProductID, &idn.UOM, &idn.Type, &idn.Value, &idn.Primary)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return idn, httpx.NewProblem(http.StatusNotFound, "COM-0001", "identifier not found")
	}
	return idn, err
}

func (s *Postgres) SetAttributes(ctx context.Context, productID string, values map[string]string) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		defs, err := loadAttributeDefs(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		prod, err := scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and id=$2`, p.TenantID, productID))
		if err != nil {
			return err
		}
		if err := domain.AssertActivationAttributes(domain.StateDraft, prod.LifecycleState, defs, values); err != nil && prod.LifecycleState == domain.StateActive {
			return domainProblem(err)
		}
		for code, val := range values {
			for _, d := range defs {
				if d.Code == code {
					if err := domain.ValidateAttribute(d, val); err != nil {
						return domainProblem(err)
					}
				}
			}
			if _, err := tx.Exec(ctx, `insert into product_attribute_value (tenant_id, product_id, attribute_code, value) values ($1,$2,$3,$4)
				on conflict (tenant_id, product_id, attribute_code) do update set value=excluded.value`, p.TenantID, productID, code, val); err != nil {
				return err
			}
		}
		return nil
	})
}

func loadAttributeDefs(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.AttributeType, error) {
	rows, err := tx.Query(ctx, `select code, kind, unit, min_value, max_value, enum_values, required from attribute_def where tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.AttributeType
	for rows.Next() {
		var d domain.AttributeType
		var enum []byte
		if err := rows.Scan(&d.Code, &d.Kind, &d.Unit, &d.Min, &d.Max, &enum, &d.Required); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(enum, &d.Enum)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Postgres) AddRelation(ctx context.Context, in domain.Relation) ([]domain.Relation, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Relation
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type_code, from_product_id, to_product_id, sort_order, valid_from, valid_to from product_relation where tenant_id=$1`, p.TenantID)
		if err != nil {
			return err
		}
		var existing []domain.Relation
		for rows.Next() {
			var r domain.Relation
			if err := rows.Scan(&r.TypeCode, &r.FromProductID, &r.ToProductID, &r.SortOrder, &r.ValidFrom, &r.ValidTo); err != nil {
				rows.Close()
				return err
			}
			existing = append(existing, r)
		}
		rows.Close()
		out, err = domain.AddRelation(existing, in, domain.SeededRelationTypes(), false)
		if err != nil {
			return domainProblem(err)
		}
		for _, r := range out {
			if _, err := tx.Exec(ctx, `insert into product_relation (tenant_id, type_code, from_product_id, to_product_id, sort_order, valid_from, valid_to)
				values ($1,$2,$3,$4,$5,$6,$7) on conflict do nothing`, p.TenantID, r.TypeCode, r.FromProductID, r.ToProductID, r.SortOrder, r.ValidFrom, r.ValidTo); err != nil {
				return err
			}
		}
		return emit(ctx, tx, pevents.RelationChanged, "product", in.FromProductID, pevents.RelationChangedData{FromProductID: in.FromProductID, ToProductID: in.ToProductID, TypeCode: in.TypeCode, Action: "created"})
	})
	return out, err
}

func (s *Postgres) EvaluatePolicy(ctx context.Context, releasedID, process string) (domain.ProcessDecision, error) {
	p, err := principal(ctx)
	if err != nil {
		return domain.ProcessDecision{}, err
	}
	var dec domain.ProcessDecision
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rp, err := s.loadReleased(ctx, tx, p.TenantID, releasedID)
		if err != nil {
			return err
		}
		prod, err := scanProduct(tx.QueryRow(ctx, `select `+productSelect+` from product where tenant_id=$1 and id=$2`, p.TenantID, rp.ProductID))
		if err != nil {
			return err
		}
		states, err := loadStates(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		if len(states) == 0 {
			states = domain.SeededLifecycleStates()
		}
		dec = domain.EvaluateProcessPolicy(process, rp, prod.LifecycleState, states)
		return nil
	})
	return dec, err
}

func loadStates(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.LifecycleState, error) {
	rows, err := tx.Query(ctx, `select code, name, category, default_for_new, applies_to, policies from lifecycle_state where tenant_id=$1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.LifecycleState
	for rows.Next() {
		var s domain.LifecycleState
		var pol []byte
		if err := rows.Scan(&s.Code, &s.Name, &s.Category, &s.DefaultForNew, &s.AppliesTo, &pol); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(pol, &s.Policies)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *Postgres) ConvertUOM(ctx context.Context, productID, from, to, qty string) (string, error) {
	p, err := principal(ctx)
	if err != nil {
		return "", err
	}
	q, err := parseDecimal(qty)
	if err != nil {
		return "", httpx.NewProblem(http.StatusBadRequest, "COM-0001", "qty must be a decimal")
	}
	var out string
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select from_uom, to_uom, factor, conv_offset from product_uom_conversion where tenant_id=$1 and product_id=$2`, p.TenantID, productID)
		if err != nil {
			return err
		}
		var product []domain.Conversion
		for rows.Next() {
			var c domain.Conversion
			if err := rows.Scan(&c.From, &c.To, &c.Factor, &c.Offset); err != nil {
				rows.Close()
				return err
			}
			c.ProductScoped = true
			product = append(product, c)
		}
		rows.Close()
		got, err := domain.ConvertQuantity(product, classConversions(), from, to, q, 6)
		if err != nil {
			return domainProblem(err)
		}
		out = got.String()
		return nil
	})
	return out, err
}

func (s *Postgres) Search(ctx context.Context, q string, limit int) ([]domain.Product, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	like := "%" + q + "%"
	var out []domain.Product
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+productSelect+` from product where tenant_id=$1 and (product_number ilike $2 or search_name ilike $2 or name ilike $2
			or exists (select 1 from product_identifier i where i.tenant_id=product.tenant_id and i.product_id=product.id and i.value ilike $2))
			order by product_number limit $3`, p.TenantID, like, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanProduct(rows)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return rows.Err()
	})
	if out == nil {
		out = []domain.Product{}
	}
	return out, err
}

func (s *Postgres) UpsertAttribute(ctx context.Context, def domain.AttributeType) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	enum, _ := json.Marshal(def.Enum)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into attribute_def (tenant_id, code, kind, unit, min_value, max_value, enum_values, required)
			values ($1,$2,$3,$4,$5,$6,$7,$8)
			on conflict (tenant_id, code) do update set kind=excluded.kind, min_value=excluded.min_value, max_value=excluded.max_value, enum_values=excluded.enum_values, required=excluded.required`,
			p.TenantID, def.Code, def.Kind, def.Unit, def.Min, def.Max, enum, def.Required)
		return err
	})
}

func (s *Postgres) SetGS1Prefix(ctx context.Context, prefix string, next int64) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into gs1_prefix (tenant_id, prefix, next_number) values ($1,$2,$3)
			on conflict (tenant_id) do update set prefix=excluded.prefix, next_number=excluded.next_number`, p.TenantID, prefix, next)
		return err
	})
}

func (s *Postgres) AddProductConversion(ctx context.Context, productID string, c domain.Conversion) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into product_uom_conversion (tenant_id, product_id, from_uom, to_uom, factor, conv_offset) values ($1,$2,$3,$4,$5,$6)
			on conflict do nothing`, p.TenantID, productID, c.From, c.To, c.Factor, c.Offset)
		return err
	})
}

func (s *Postgres) SeedLifecycle(ctx context.Context) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, st := range domain.SeededLifecycleStates() {
			pol, _ := json.Marshal(st.Policies)
			if _, err := tx.Exec(ctx, `insert into lifecycle_state (tenant_id, code, name, category, default_for_new, applies_to, policies)
				values ($1,$2,$3,$4,$5,$6,$7) on conflict do nothing`, p.TenantID, st.Code, st.Name, st.Category, st.DefaultForNew, st.AppliesTo, pol); err != nil {
				return err
			}
		}
		return nil
	})
}
