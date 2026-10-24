package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (store *Postgres) Ping(ctx context.Context) error { return store.pool.Ping(ctx) }

func (store *Postgres) Publish(ctx context.Context, tenantID string, entity *domain.Entity) (Version, error) {
	var version Version
	err := dbx.WithTenantTx(ctx, store.pool, func(tx pgx.Tx) error {
		var err error
		version, err = publishTx(ctx, tx, tenantID, entity)
		return err
	})
	return version, err
}

func publishTx(ctx context.Context, tx pgx.Tx, tenantID string, entity *domain.Entity) (Version, error) {
	if violations := domain.ValidateEffective(entity); len(violations) > 0 {
		errs := make([]error, len(violations))
		for i, violation := range violations {
			errs[i] = violation
		}
		return Version{}, errors.Join(errs...)
	}
	current, number, err := currentTx(ctx, tx, tenantID, entity.Name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Version{}, err
	}
	if current != nil {
		if err := domain.CompareVersions(current, entity); err != nil {
			return Version{}, err
		}
	}
	version := Version{Number: number + 1, Entity: clone(entity), Changes: domain.Diff(current, entity)}
	definition, _ := json.Marshal(version.Entity)
	changes, _ := json.Marshal(version.Changes)
	principal, _ := tenant.FromContext(ctx)
	actor := principal.UserID
	if !ids.Valid(actor) {
		actor = ids.Nil
	}
	_, err = tx.Exec(ctx, `insert into metadata_entity_version
		(tenant_id, entity_name, version, definition, changes, created_by)
		values ($1, $2, $3, $4, $5, $6)`,
		tenantID, entity.Name, version.Number, definition, changes, actor)
	if err != nil {
		return Version{}, fmt.Errorf("publish metadata version: %w", err)
	}
	return version, nil
}

func (store *Postgres) Current(ctx context.Context, tenantID, entity string) (*domain.Entity, error) {
	var result *domain.Entity
	err := dbx.WithTenantTx(ctx, store.pool, func(tx pgx.Tx) error {
		var err error
		result, _, err = currentTx(ctx, tx, tenantID, entity)
		return err
	})
	return result, err
}

func currentTx(ctx context.Context, tx pgx.Tx, tenantID, entity string) (*domain.Entity, int, error) {
	var definition []byte
	var version int
	err := tx.QueryRow(ctx, `select definition, version
		from metadata_entity_version
		where tenant_id=$1 and entity_name=$2
		order by version desc limit 1`, tenantID, entity).Scan(&definition, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	var result domain.Entity
	if err := json.Unmarshal(definition, &result); err != nil {
		return nil, 0, fmt.Errorf("decode metadata definition: %w", err)
	}
	return &result, version, nil
}

func (store *Postgres) Extend(ctx context.Context, tenantID string, extension domain.Extension) (Version, error) {
	var version Version
	err := dbx.WithTenantTx(ctx, store.pool, func(tx pgx.Tx) error {
		current, _, err := currentTx(ctx, tx, tenantID, extension.Entity)
		if err != nil {
			return err
		}
		effective, err := domain.ApplyExtension(current, extension)
		if err != nil {
			return err
		}
		version, err = publishTx(ctx, tx, tenantID, effective)
		if err != nil {
			return err
		}
		definition, _ := json.Marshal(extension)
		principal, _ := tenant.FromContext(ctx)
		actor := principal.UserID
		if !ids.Valid(actor) {
			actor = ids.Nil
		}
		_, err = tx.Exec(ctx, `insert into metadata_extension
			(tenant_id, name, entity_name, definition, applied_version, applied_by)
			values ($1, $2, $3, $4, $5, $6)`,
			tenantID, extension.Name, extension.Entity, definition, version.Number, actor)
		return err
	})
	return version, err
}

func (store *Postgres) CreateRecord(ctx context.Context, tenantID, entity string, input map[string]any) (map[string]any, error) {
	var result map[string]any
	err := dbx.WithTenantTx(ctx, store.pool, func(tx pgx.Tx) error {
		definition, _, err := currentTx(ctx, tx, tenantID, entity)
		if err != nil {
			return err
		}
		result, err = validateRecord(definition, input)
		if err != nil {
			return err
		}
		id := ids.New()
		result["id"] = id
		result["tenantId"] = tenantID
		result["version"] = 1
		data, _ := json.Marshal(result)
		principal, _ := tenant.FromContext(ctx)
		actor := principal.UserID
		if !ids.Valid(actor) {
			actor = ids.Nil
		}
		_, err = tx.Exec(ctx, `insert into metadata_record
			(tenant_id, entity_name, id, data, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $5)`, tenantID, entity, id, data, actor)
		return err
	})
	return result, err
}

func (store *Postgres) ListRecords(ctx context.Context, tenantID, entity string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	result := []map[string]any{}
	err := dbx.WithTenantTx(ctx, store.pool, func(tx pgx.Tx) error {
		if _, _, err := currentTx(ctx, tx, tenantID, entity); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select data from metadata_record
			where tenant_id=$1 and entity_name=$2
			order by created_at desc, id desc limit $3`, tenantID, entity, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				return err
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				return err
			}
			result = append(result, record)
		}
		return rows.Err()
	})
	return result, err
}
