package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeBindingNotFound = "AUTH-0404"
	CodeBindingExists   = "MIG-BINDING-EXISTS"
)

// EntityBinding is a binding as the API shows it (integration track S2).
type EntityBinding struct {
	ID                  string         `json:"id"`
	ConnectionProfileID string         `json:"connectionProfileId"`
	Entity              string         `json:"entity"`
	Source              map[string]any `json:"source"`
	KeyFields           []string       `json:"keyFields"`
	CursorField         string         `json:"cursorField,omitempty"`
	CursorType          string         `json:"cursorType,omitempty"`
	MappingSpecID       string         `json:"mappingSpecId,omitempty"`
	Status              string         `json:"status"`
	Version             int            `json:"version"`
	CreatedBy           string         `json:"createdBy"`
	CreatedAt           time.Time      `json:"createdAt"`
	UpdatedAt           time.Time      `json:"updatedAt"`
}

// BindingCursor is the keyset position after the last binding of a page: (entity, id).
type BindingCursor struct {
	Entity string `json:"e"`
	ID     string `json:"i"`
}

type BindingFilter struct {
	ConnectionProfileID string
	Entity              string
	Status              string
	After               *BindingCursor
	Limit               int
}

func bindingNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeBindingNotFound, "entity binding not found")
}

func bindingExists() error {
	return httpx.NewProblem(http.StatusConflict, CodeBindingExists, "this connection profile already has a binding for this entity")
}

// SpecUnknown is the 422 for a binding that names a mapping spec this tenant does not have.
func SpecUnknown() error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, "MIG-SPEC-UNKNOWN", "mappingSpecId names no mapping spec")
	p.Errors = map[string]string{"mappingSpecId": "no mapping spec has this id"}
	return p
}

func sameBinding(a, b EntityBinding) bool {
	return a.ConnectionProfileID == b.ConnectionProfileID && a.Entity == b.Entity && slices.Equal(a.KeyFields, b.KeyFields) &&
		a.CursorField == b.CursorField && a.CursorType == b.CursorType && a.MappingSpecID == b.MappingSpecID &&
		reflect.DeepEqual(normalize(a.Source), normalize(b.Source))
}

const bindingColumns = `id, connection_profile_id, entity, source, key_fields, cursor_field, cursor_type, coalesce(mapping_spec_id::text, ''), status, version, created_by, created_at, updated_at`

func scanBinding(row pgx.Row) (EntityBinding, error) {
	var b EntityBinding
	var src, keys []byte
	if err := row.Scan(&b.ID, &b.ConnectionProfileID, &b.Entity, &src, &keys, &b.CursorField, &b.CursorType, &b.MappingSpecID, &b.Status, &b.Version, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return b, err
	}
	if err := json.Unmarshal(src, &b.Source); err != nil {
		return b, err
	}
	if err := json.Unmarshal(keys, &b.KeyFields); err != nil {
		return b, err
	}
	if b.Source == nil {
		b.Source = map[string]any{}
	}
	return b, nil
}

func bindingViolation(err error) error {
	switch {
	case isUniqueViolation(err, "entity_binding_tenant_id_connection_profile_id_entity_key"):
		return bindingExists()
	case isForeignKeyViolation(err, "entity_binding_tenant_id_connection_profile_id_fkey"):
		return ConnectionUnknown()
	case isForeignKeyViolation(err, "entity_binding_tenant_id_mapping_spec_id_fkey"):
		return SpecUnknown()
	}
	return err
}

// SaveEntityBinding inserts a binding; created is false when the Idempotency-Key already stored the same one.
func (s *Postgres) SaveEntityBinding(ctx context.Context, in EntityBinding, idempotencyKey string) (EntityBinding, bool, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	src, err := json.Marshal(normalize(in.Source))
	if err != nil {
		return EntityBinding{}, false, err
	}
	keys, _ := json.Marshal(in.KeyFields)
	var saved EntityBinding
	created := false
	byKey := func(tx pgx.Tx) (EntityBinding, error) {
		return scanBinding(tx.QueryRow(ctx, `select `+bindingColumns+` from migration.entity_binding where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idempotencyKey != "" {
			prior, err := byKey(tx)
			if err == nil {
				if !sameBinding(prior, in) {
					return keyReuse()
				}
				saved = prior
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		saved, err = scanBinding(tx.QueryRow(ctx, `insert into migration.entity_binding
			(tenant_id, id, connection_profile_id, entity, source, key_fields, cursor_field, cursor_type, mapping_spec_id, status, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+bindingColumns, p.TenantID, in.ID, in.ConnectionProfileID, in.Entity, src, keys, in.CursorField, in.CursorType,
			nullIfEmpty(in.MappingSpecID), in.Status, p.UserID, nullIfEmpty(idempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			prior, err := byKey(tx)
			if err != nil {
				return err
			}
			if !sameBinding(prior, in) {
				return keyReuse()
			}
			saved = prior
			return nil
		}
		created = err == nil
		return err
	})
	if err != nil {
		return EntityBinding{}, false, bindingViolation(err)
	}
	return saved, created, nil
}

func (s *Postgres) GetEntityBinding(ctx context.Context, id string) (EntityBinding, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return EntityBinding{}, bindingNotFound()
	}
	var out EntityBinding
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanBinding(tx.QueryRow(ctx, `select `+bindingColumns+` from migration.entity_binding where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EntityBinding{}, bindingNotFound()
	}
	return out, err
}

// ListEntityBindings pages bindings in (entity, id) order, at most Limit rows.
func (s *Postgres) ListEntityBindings(ctx context.Context, f BindingFilter) ([]EntityBinding, error) {
	p, _ := tenant.FromContext(ctx)
	afterEntity, afterID := "", "00000000-0000-0000-0000-000000000000"
	if f.After != nil {
		afterEntity, afterID = f.After.Entity, f.After.ID
	}
	profile := f.ConnectionProfileID
	if profile != "" && !ids.Valid(profile) {
		return nil, nil
	}
	var out []EntityBinding
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+bindingColumns+` from migration.entity_binding
			where tenant_id = $1 and ($2 = '' or connection_profile_id = $2::uuid) and ($3 = '' or entity = $3) and ($4 = '' or status = $4)
			and ($5::boolean is false or (entity, id) > ($6, $7::uuid))
			order by entity, id limit $8`, p.TenantID, profile, f.Entity, f.Status, f.After != nil, afterEntity, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBinding(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateEntityBinding applies edit under the binding's version and bumps it. The connection profile and the entity are
// fixed at creation: a different source system or entity is a different binding.
func (s *Postgres) UpdateEntityBinding(ctx context.Context, id string, version int, edit func(*EntityBinding) error) (EntityBinding, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return EntityBinding{}, bindingNotFound()
	}
	var out EntityBinding
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanBinding(tx.QueryRow(ctx, `select `+bindingColumns+` from migration.entity_binding where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return bindingNotFound()
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		if err := edit(&cur); err != nil {
			return err
		}
		src, err := json.Marshal(normalize(cur.Source))
		if err != nil {
			return err
		}
		keys, _ := json.Marshal(cur.KeyFields)
		out, err = scanBinding(tx.QueryRow(ctx, `update migration.entity_binding set source = $3, key_fields = $4, cursor_field = $5, cursor_type = $6,
			mapping_spec_id = $7, status = $8, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+bindingColumns, p.TenantID, id, src, keys, cur.CursorField, cur.CursorType, nullIfEmpty(cur.MappingSpecID), cur.Status))
		return err
	})
	if err != nil {
		return EntityBinding{}, bindingViolation(err)
	}
	return out, nil
}

type memBindings struct {
	mu    sync.Mutex
	items []EntityBinding
	byKey map[string]string
}

func (m *Mem) bn() *memBindings {
	m.bnOnce.Do(func() { m.bnState = &memBindings{byKey: map[string]string{}} })
	return m.bnState
}

func (m *Mem) SaveEntityBinding(ctx context.Context, in EntityBinding, idempotencyKey string) (EntityBinding, bool, error) {
	if m.SaveErr != nil {
		return EntityBinding{}, false, m.SaveErr
	}
	if _, err := m.GetConnectionProfile(ctx, in.ConnectionProfileID); err != nil {
		return EntityBinding{}, false, ConnectionUnknown()
	}
	if in.MappingSpecID != "" {
		if _, err := m.GetMappingSpec(ctx, in.MappingSpecID); err != nil {
			return EntityBinding{}, false, SpecUnknown()
		}
	}
	st := m.bn()
	st.mu.Lock()
	defer st.mu.Unlock()
	if id, ok := st.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		for _, b := range st.items {
			if b.ID == id {
				if !sameBinding(b, in) {
					return EntityBinding{}, false, keyReuse()
				}
				return b, false, nil
			}
		}
	}
	for _, b := range st.items {
		if b.ConnectionProfileID == in.ConnectionProfileID && b.Entity == in.Entity {
			return EntityBinding{}, false, bindingExists()
		}
	}
	now := time.Now().UTC()
	in.ID, in.Version, in.CreatedAt, in.UpdatedAt = ids.New(), 1, now, now
	in.Source = normalize(in.Source)
	if p, ok := tenant.FromContext(ctx); ok {
		in.CreatedBy = p.UserID
	}
	st.items = append(st.items, in)
	if idempotencyKey != "" {
		st.byKey[idempotencyKey] = in.ID
	}
	return in, true, nil
}

func (m *Mem) GetEntityBinding(_ context.Context, id string) (EntityBinding, error) {
	st := m.bn()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, b := range st.items {
		if b.ID == id {
			return b, nil
		}
	}
	return EntityBinding{}, bindingNotFound()
}

func (m *Mem) ListEntityBindings(_ context.Context, f BindingFilter) ([]EntityBinding, error) {
	st := m.bn()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []EntityBinding
	for _, b := range st.items {
		if (f.ConnectionProfileID == "" || b.ConnectionProfileID == f.ConnectionProfileID) && (f.Entity == "" || b.Entity == f.Entity) &&
			(f.Status == "" || b.Status == f.Status) &&
			(f.After == nil || cmp.Or(strings.Compare(b.Entity, f.After.Entity), strings.Compare(b.ID, f.After.ID)) > 0) {
			out = append(out, b)
		}
	}
	slices.SortFunc(out, func(a, b EntityBinding) int {
		return cmp.Or(strings.Compare(a.Entity, b.Entity), strings.Compare(a.ID, b.ID))
	})
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) UpdateEntityBinding(ctx context.Context, id string, version int, edit func(*EntityBinding) error) (EntityBinding, error) {
	st := m.bn()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.items {
		if st.items[i].ID != id {
			continue
		}
		if st.items[i].Version != version {
			return EntityBinding{}, versionConflict(st.items[i].Version)
		}
		cur := st.items[i]
		if err := edit(&cur); err != nil {
			return EntityBinding{}, err
		}
		if cur.MappingSpecID != "" {
			if _, err := m.GetMappingSpec(ctx, cur.MappingSpecID); err != nil {
				return EntityBinding{}, SpecUnknown()
			}
		}
		cur.Source = normalize(cur.Source)
		cur.Version++
		cur.UpdatedAt = time.Now().UTC()
		st.items[i] = cur
		return cur, nil
	}
	return EntityBinding{}, bindingNotFound()
}
