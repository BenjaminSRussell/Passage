package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

func (s *Postgres) CreateOrganization(ctx context.Context, o domain.EngineeringOrganization, idem Idem) (domain.EngineeringOrganization, error) {
	if o.ID == "" {
		o.ID = ids.New()
	}
	return createOnce(s, ctx, idem, o.ID, o, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.EngineeringOrganization) error {
		_, err := tx.Exec(ctx, `insert into engineering_organization (tenant_id, id, legal_entity_id, code, name) values ($1, $2, $3, $4, $5)`,
			tenantID, v.ID, v.LegalEntityID, v.Code, v.Name)
		return err
	}, s.GetOrganization)
}

func (s *Postgres) GetOrganization(ctx context.Context, id string) (domain.EngineeringOrganization, error) {
	var o domain.EngineeringOrganization
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		err := tx.QueryRow(ctx, `select id::text, legal_entity_id, code, name from engineering_organization where tenant_id = $1 and id = $2::uuid`, tenantID, id).
			Scan(&o.ID, &o.LegalEntityID, &o.Code, &o.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-ORG-NOTFOUND", "engineering organization not found")
		}
		return err
	})
	return o, err
}

func (s *Postgres) ListOrganizations(ctx context.Context, afterID string, limit int) (Page[domain.EngineeringOrganization], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, legal_entity_id, code, name from engineering_organization where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.EngineeringOrganization, error) {
			var o domain.EngineeringOrganization
			return o, row.Scan(&o.ID, &o.LegalEntityID, &o.Code, &o.Name)
		})
}

func (s *Postgres) CreateNomenclature(ctx context.Context, n domain.Nomenclature, idem Idem) (domain.Nomenclature, error) {
	if n.ID == "" {
		n.ID = ids.New()
	}
	return createOnce(s, ctx, idem, n.ID, n, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.Nomenclature) error {
		raw, err := json.Marshal(v.Segments)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into version_nomenclature (tenant_id, id, code, separator, segments) values ($1, $2, $3, $4, $5)`,
			tenantID, v.ID, v.Code, v.Separator, raw)
		return err
	}, s.GetNomenclature)
}

func (s *Postgres) GetNomenclature(ctx context.Context, id string) (domain.Nomenclature, error) {
	var n domain.Nomenclature
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		var raw []byte
		err := tx.QueryRow(ctx, `select id::text, code, separator, segments from version_nomenclature where tenant_id = $1 and id = $2::uuid`, tenantID, id).
			Scan(&n.ID, &n.Code, &n.Separator, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-NOMEN-NOTFOUND", "nomenclature not found")
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &n.Segments)
	})
	return n, err
}

func (s *Postgres) ListNomenclatures(ctx context.Context, afterID string, limit int) (Page[domain.Nomenclature], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, code, separator, segments from version_nomenclature where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.Nomenclature, error) {
			var n domain.Nomenclature
			var raw []byte
			if err := row.Scan(&n.ID, &n.Code, &n.Separator, &raw); err != nil {
				return n, err
			}
			return n, json.Unmarshal(raw, &n.Segments)
		})
}

func (s *Postgres) CreateCategory(ctx context.Context, c domain.EngineeringCategory, idem Idem) (domain.EngineeringCategory, error) {
	if c.ID == "" {
		c.ID = ids.New()
	}
	return createOnce(s, ctx, idem, c.ID, c, func(ctx context.Context, tx pgx.Tx, tenantID string, v domain.EngineeringCategory) error {
		_, err := tx.Exec(ctx, `insert into engineering_category (tenant_id, id, code, name, product_type, track_versions, version_as_dimension, nomenclature_id, readiness_policy_id, release_policy_id, change_controlled)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid, nullif($9, '')::uuid, nullif($10, '')::uuid, $11)`,
			tenantID, v.ID, v.Code, v.Name, v.ProductType, v.TrackVersions, v.VersionAsDimension, v.NomenclatureID, v.ReadinessPolicyID, v.ReleasePolicyID, v.ChangeControlled)
		return err
	}, s.GetCategory)
}

func (s *Postgres) GetCategory(ctx context.Context, id string) (domain.EngineeringCategory, error) {
	var c domain.EngineeringCategory
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		err := tx.QueryRow(ctx, `select id::text, code, name, product_type, track_versions, version_as_dimension, coalesce(nomenclature_id::text, ''), coalesce(readiness_policy_id::text, ''), coalesce(release_policy_id::text, ''), change_controlled
			from engineering_category where tenant_id = $1 and id = $2::uuid`, tenantID, id).
			Scan(&c.ID, &c.Code, &c.Name, &c.ProductType, &c.TrackVersions, &c.VersionAsDimension, &c.NomenclatureID, &c.ReadinessPolicyID, &c.ReleasePolicyID, &c.ChangeControlled)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-CAT-NOTFOUND", "engineering category not found")
		}
		return err
	})
	return c, err
}

func (s *Postgres) ListCategories(ctx context.Context, afterID string, limit int) (Page[domain.EngineeringCategory], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, code, name, product_type, track_versions, version_as_dimension, coalesce(nomenclature_id::text, ''), coalesce(readiness_policy_id::text, ''), coalesce(release_policy_id::text, ''), change_controlled
		from engineering_category where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (domain.EngineeringCategory, error) {
			var c domain.EngineeringCategory
			return c, row.Scan(&c.ID, &c.Code, &c.Name, &c.ProductType, &c.TrackVersions, &c.VersionAsDimension, &c.NomenclatureID, &c.ReadinessPolicyID, &c.ReleasePolicyID, &c.ChangeControlled)
		})
}

func (s *Postgres) CreateEngineeringVersion(ctx context.Context, v domain.EngineeringVersion, idem Idem) (domain.EngineeringVersion, error) {
	if v.ID == "" {
		v.ID = ids.New()
	}
	return createOnce(s, ctx, idem, v.ID, v, func(ctx context.Context, tx pgx.Tx, tenantID string, row domain.EngineeringVersion) error {
		var owner string
		err := tx.QueryRow(ctx, `select engineering_le_id from engineering_version where tenant_id = $1 and product_id = $2 order by id limit 1`, tenantID, row.ProductID).Scan(&owner)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := domain.CheckOwnedField("engineering versions of "+row.ProductID, owner, row.EngineeringLEID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into engineering_version (tenant_id, id, product_id, version_code, engineering_le_id, category_id, bom_version_id, route_version_id, lifecycle_state)
			values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, nullif($7, '')::uuid, nullif($8, '')::uuid, $9)`,
			tenantID, row.ID, row.ProductID, row.VersionCode, row.EngineeringLEID, row.CategoryID, row.BOMVersionID, row.RouteVersionID, row.LifecycleState)
		return err
	}, s.GetEngineeringVersion)
}

func (s *Postgres) GetEngineeringVersion(ctx context.Context, id string) (domain.EngineeringVersion, error) {
	var v domain.EngineeringVersion
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		err := tx.QueryRow(ctx, `select id::text, product_id, version_code, engineering_le_id, coalesce(category_id::text, ''), coalesce(bom_version_id::text, ''), coalesce(route_version_id::text, ''), lifecycle_state
			from engineering_version where tenant_id = $1 and id = $2::uuid`, tenantID, id).
			Scan(&v.ID, &v.ProductID, &v.VersionCode, &v.EngineeringLEID, &v.CategoryID, &v.BOMVersionID, &v.RouteVersionID, &v.LifecycleState)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "ENG-VERSION-NOTFOUND", "engineering version not found")
		}
		return err
	})
	return v, err
}

func (s *Postgres) ListEngineeringVersions(ctx context.Context, productID, afterID string, limit int) (Page[domain.EngineeringVersion], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []domain.EngineeringVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, product_id, version_code, engineering_le_id, coalesce(category_id::text, ''), coalesce(bom_version_id::text, ''), coalesce(route_version_id::text, ''), lifecycle_state
			from engineering_version where tenant_id = $1 and id > $2::uuid and ($3 = '' or product_id = $3) order by id limit $4`, p.TenantID, afterID, productID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v domain.EngineeringVersion
			if err := rows.Scan(&v.ID, &v.ProductID, &v.VersionCode, &v.EngineeringLEID, &v.CategoryID, &v.BOMVersionID, &v.RouteVersionID, &v.LifecycleState); err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return trimPage(items, limit, func(v domain.EngineeringVersion) string { return v.ID }), err
}

func (s *Postgres) PutIdentifier(ctx context.Context, idType, value, productID string) error {
	return withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) error {
		_, err := tx.Exec(ctx, `insert into product_identifier (tenant_id, id_type, value, product_id) values ($1, $2, $3, $4)
			on conflict (tenant_id, id_type, value) do update set product_id = excluded.product_id`, tenantID, idType, value, productID)
		return err
	})
}

func withTenant(s *Postgres, ctx context.Context, fn func(context.Context, pgx.Tx, string) error) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(ctx, tx, p.TenantID) })
}

func createOnce[T any](s *Postgres, ctx context.Context, idem Idem, id string, v T, insert func(context.Context, pgx.Tx, string, T) error, get func(context.Context, string) (T, error)) (T, error) {
	p, _ := tenant.FromContext(ctx)
	created := v
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replayID, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			got, err := get(ctx, replayID)
			created = got
			return err
		}
		if err := insert(ctx, tx, p.TenantID, v); err != nil {
			return err
		}
		return remember(ctx, tx, p.TenantID, idem, id)
	})
	return created, conflictOnDuplicate(err)
}

func listSimple[T any](s *Postgres, ctx context.Context, afterID string, limit int, query string, scan func(pgx.Row) (T, error)) (Page[T], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	var items []T
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, p.TenantID, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scan(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return trimPage(items, limit, func(v T) string {
		switch t := any(v).(type) {
		case domain.EngineeringOrganization:
			return t.ID
		case domain.Nomenclature:
			return t.ID
		case domain.EngineeringCategory:
			return t.ID
		case domain.ReleasePolicy:
			return t.ID
		case domain.ReadinessPolicy:
			return t.ID
		case domain.DocumentType:
			return t.ID
		case domain.Document:
			return t.ID
		case domain.ReleaseSession:
			return t.ID
		case EbomImportRecord:
			return t.ID
		case ReleasedProduct:
			return t.ID
		default:
			return ""
		}
	}), err
}
