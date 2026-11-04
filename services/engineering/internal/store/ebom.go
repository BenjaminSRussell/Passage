package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const aggregateEbom = "ebom"

func (s *Postgres) ImportEbom(ctx context.Context, in EbomImportInput, idem Idem) (EbomImportRecord, error) {
	rec := EbomImportRecord{ID: ids.New(), Source: in.Source, PartNumber: in.Payload.PartNumber, Revision: in.Payload.Revision, Status: domain.EbomDraft, Payload: in.Payload}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			loaded, err := getEbomImport(ctx, tx, p.TenantID, id)
			rec = loaded
			return err
		}
		var existingID, existingStatus string
		err = tx.QueryRow(ctx, `select id::text, status from ebom_import where tenant_id = $1 and source_code = $2 and part_number = $3 and revision = $4`,
			p.TenantID, in.Source, in.Payload.PartNumber, in.Payload.Revision).Scan(&existingID, &existingStatus)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if existingID != "" {
			if err := (domain.EbomImport{Status: existingStatus, Revision: in.Payload.Revision, PartNumber: in.Payload.PartNumber}).CanReimport(); err != nil {
				return err
			}
			loaded, err := getEbomImport(ctx, tx, p.TenantID, existingID)
			rec = loaded
			return err
		}
		known, err := resolveParts(ctx, tx, p.TenantID, in.Payload)
		if err != nil {
			return err
		}
		var active *domain.BomVersion
		productID := known[in.Payload.PartNumber]
		if productID == "" {
			productID = in.Payload.PartNumber
		}
		foundActive, err := loadVersions(ctx, tx, p.TenantID, `product_id = $2 and status = 'active'`, productID)
		if err != nil {
			return err
		}
		if len(foundActive) > 0 {
			active = &foundActive[0]
		}
		result, err := domain.ImportEbom(in.Payload, known, active)
		if err != nil {
			return err
		}
		if err := lockProducts(ctx, tx, p.TenantID, result.Draft.ProductID); err != nil {
			return err
		}
		snapshot, err := loadSnapshot(ctx, tx, p.TenantID, versionRoots(*result.Draft), false)
		if err != nil {
			return err
		}
		for _, l := range result.Draft.Lines {
			if err := domain.CheckCycle(snapshot, result.Draft.ProductID, l.ComponentProductID, result.Draft.EffectiveFrom); err != nil {
				return err
			}
		}
		result.Draft.ID = ids.New()
		if err := insertVersion(ctx, tx, p.TenantID, *result.Draft); err != nil {
			return err
		}
		rec.BOMVersionID = result.Draft.ID
		if result.ECR != nil {
			result.ECR.ID = ids.New()
			result.ECR.RequestedBy = in.Actor
			if result.ECR.RequestedBy == "" {
				result.ECR.RequestedBy = p.UserID
			}
			if _, err := tx.Exec(ctx, `insert into ecr (tenant_id, id, number, title, reason, priority, state, requested_by) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				p.TenantID, result.ECR.ID, result.ECR.Number, result.ECR.Title, result.ECR.Reason, result.ECR.Priority, string(result.ECR.State), result.ECR.RequestedBy); err != nil {
				return err
			}
			for _, product := range result.ECR.Products {
				if _, err := tx.Exec(ctx, `insert into ecr_product (tenant_id, ecr_id, product_id) values ($1, $2, $3)`, p.TenantID, result.ECR.ID, product); err != nil {
					return err
				}
			}
			rec.EcrID = result.ECR.ID
		}
		payload, err := json.Marshal(in.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into ebom_import (tenant_id, id, source_code, part_number, revision, status, payload, bom_version_id, ecr_id, added, removed, changed)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid, nullif($9, '')::uuid, $10, $11, $12)`,
			p.TenantID, rec.ID, rec.Source, rec.PartNumber, rec.Revision, rec.Status, payload, rec.BOMVersionID, rec.EcrID, result.Comparison.Added, result.Comparison.Removed, result.Comparison.Changed); err != nil {
			return err
		}
		for _, ex := range result.Exceptions {
			if _, err := tx.Exec(ctx, `insert into ebom_exception (tenant_id, import_id, part_number, code) values ($1, $2, $3, $4)`, p.TenantID, rec.ID, ex.PartNumber, ex.Code); err != nil {
				return err
			}
		}
		rec.Exceptions, rec.Comparison = result.Exceptions, result.Comparison
		if err := emit(ctx, tx, p, rec.ID, engevents.EbomImported, aggregateEbom, rec.ID, engevents.EbomImportedData{ImportID: rec.ID, PartNumber: rec.PartNumber, Revision: rec.Revision, Added: rec.Comparison.Added, Exceptions: len(rec.Exceptions)}); err != nil {
			return err
		}
		return remember(ctx, tx, p.TenantID, idem, rec.ID)
	})
	return rec, conflictOnDuplicate(err)
}

func resolveParts(ctx context.Context, tx pgx.Tx, tenantID string, payload domain.EbomPayload) (map[string]string, error) {
	parts := []string{payload.PartNumber}
	for _, line := range payload.Lines {
		parts = append(parts, line.PartNumber)
	}
	slices.Sort(parts)
	parts = slices.Compact(parts)
	rows, err := tx.Query(ctx, `select value, product_id from (
			select value, product_id, 1 as rank from product_identifier where tenant_id = $1 and id_type in ('cad_part', 'product_number') and value = any($2)
			union all select product_id, product_id, 2 from engineering_version where tenant_id = $1 and product_id = any($2)
			union all select product_id, product_id, 2 from bom_version where tenant_id = $1 and product_id = any($2)) known
		order by rank`, tenantID, parts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := make(map[string]string, len(parts))
	for rows.Next() {
		var part, productID string
		if err := rows.Scan(&part, &productID); err != nil {
			return nil, err
		}
		if _, ok := known[part]; !ok {
			known[part] = productID
		}
	}
	return known, rows.Err()
}

func (s *Postgres) GetEbomImport(ctx context.Context, id string) (EbomImportRecord, error) {
	var rec EbomImportRecord
	err := withTenant(s, ctx, func(ctx context.Context, tx pgx.Tx, tenantID string) (err error) {
		rec, err = getEbomImport(ctx, tx, tenantID, id)
		return err
	})
	return rec, err
}

func getEbomImport(ctx context.Context, tx pgx.Tx, tenantID, id string) (EbomImportRecord, error) {
	var rec EbomImportRecord
	var payload []byte
	err := tx.QueryRow(ctx, `select id::text, source_code, part_number, revision, status, payload, coalesce(bom_version_id::text, ''), coalesce(ecr_id::text, ''), added, removed, changed
		from ebom_import where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&rec.ID, &rec.Source, &rec.PartNumber, &rec.Revision, &rec.Status, &payload, &rec.BOMVersionID, &rec.EcrID, &rec.Comparison.Added, &rec.Comparison.Removed, &rec.Comparison.Changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, httpx.NewProblem(http.StatusNotFound, "ENG-EBOM-NOTFOUND", "eBOM import not found")
	}
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(payload, &rec.Payload); err != nil {
		return rec, err
	}
	rows, err := tx.Query(ctx, `select part_number, code from ebom_exception where tenant_id = $1 and import_id = $2::uuid order by part_number`, tenantID, id)
	if err != nil {
		return rec, err
	}
	defer rows.Close()
	for rows.Next() {
		var ex domain.EbomException
		if err := rows.Scan(&ex.PartNumber, &ex.Code); err != nil {
			return rec, err
		}
		rec.Exceptions = append(rec.Exceptions, ex)
	}
	return rec, rows.Err()
}

func (s *Postgres) ListEbomImports(ctx context.Context, afterID string, limit int) (Page[EbomImportRecord], error) {
	return listSimple(s, ctx, afterID, limit, `select id::text, source_code, part_number, revision, status, payload, coalesce(bom_version_id::text, ''), coalesce(ecr_id::text, ''), added, removed, changed
		from ebom_import where tenant_id = $1 and id > $2::uuid order by id limit $3`,
		func(row pgx.Row) (EbomImportRecord, error) {
			var rec EbomImportRecord
			var payload []byte
			if err := row.Scan(&rec.ID, &rec.Source, &rec.PartNumber, &rec.Revision, &rec.Status, &payload, &rec.BOMVersionID, &rec.EcrID, &rec.Comparison.Added, &rec.Comparison.Removed, &rec.Comparison.Changed); err != nil {
				return rec, err
			}
			return rec, json.Unmarshal(payload, &rec.Payload)
		})
}
