package ac1load

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const targetSchemaSQL = `
drop schema if exists ac1 cascade;
create schema ac1;
create table ac1.target (
  tenant_id uuid not null,
  entity text not null,
  source_key text not null,
  fields jsonb not null,
  primary key (tenant_id, entity, source_key)
);
alter table ac1.target enable row level security;
alter table ac1.target force row level security;
create policy target_tenant_access on ac1.target as permissive
  using (tenant_id = current_setting('passage.tenant_id', true)::uuid)
  with check (tenant_id = current_setting('passage.tenant_id', true)::uuid);
create policy target_legal_entity_scope on ac1.target as restrictive
  using (current_setting('passage.legal_entity_scopes', true) = '*');
do $$
begin
  if exists (select 1 from pg_roles where rolname = 'passage_app') then
    grant usage on schema ac1 to passage_app;
    grant select, insert, update, delete on ac1.target to passage_app;
  end if;
end
$$;`

func SetupTarget(ctx context.Context, owner *pgxpool.Pool) error {
	_, err := owner.Exec(ctx, targetSchemaSQL)
	return err
}

func DropTarget(ctx context.Context, owner *pgxpool.Pool) error {
	_, err := owner.Exec(ctx, `drop schema if exists ac1 cascade`)
	return err
}

type PGTarget struct{ pool *pgxpool.Pool }

func NewPGTarget(pool *pgxpool.Pool) *PGTarget { return &PGTarget{pool: pool} }

func (t *PGTarget) LoadBatch(ctx context.Context, entity string, items []domain.LoadItem) ([]domain.LoadResult, error) {
	p, _ := tenant.FromContext(ctx)
	keys, docs := make([]string, len(items)), make([]string, len(items))
	for i, it := range items {
		raw, err := json.Marshal(it.Fields)
		if err != nil {
			return nil, fmt.Errorf("encode %s %s: %w", entity, it.SourceKey, err)
		}
		keys[i], docs[i] = it.SourceKey, string(raw)
	}
	err := dbx.WithTenantTx(ctx, t.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into ac1.target (tenant_id, entity, source_key, fields)
			select $1, $2, i.source_key, i.fields::jsonb
			from unnest($3::text[], $4::text[]) as i(source_key, fields)
			on conflict (tenant_id, entity, source_key) do update set fields = excluded.fields`,
			p.TenantID, entity, keys, docs)
		return err
	})
	return make([]domain.LoadResult, len(items)), err
}

func (t *PGTarget) Read(ctx context.Context, entity string) ([]domain.Record, error) {
	p, _ := tenant.FromContext(ctx)
	var out []domain.Record
	err := dbx.WithTenantTx(ctx, t.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_key, fields::text from ac1.target where tenant_id = $1 and entity = $2`, p.TenantID, entity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, raw string
			if err := rows.Scan(&key, &raw); err != nil {
				return err
			}
			fields, err := decodeFields(raw)
			if err != nil {
				return fmt.Errorf("decode %s %s: %w", entity, key, err)
			}
			out = append(out, domain.Record{Key: key, Fields: fields})
		}
		return rows.Err()
	})
	return out, err
}

func decodeFields(raw string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var fields map[string]any
	if err := dec.Decode(&fields); err != nil {
		return nil, err
	}
	for k, v := range fields {
		if n, ok := v.(json.Number); ok {
			i, err := n.Int64()
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", k, err)
			}
			fields[k] = i
		}
	}
	return fields, nil
}
