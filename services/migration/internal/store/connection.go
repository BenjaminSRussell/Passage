package store

import (
	"bytes"
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
	CodeConnectionNotFound = "AUTH-0404"
	CodeConnectionName     = "MIG-CONNECTION-NAME-EXISTS"
	CodeConnectionUnknown  = "MIG-CONNECTION-UNKNOWN"
)

// ConnectionProfile is a connection profile as the API shows it: settings and whether a secret is set, never the
// secret or its ciphertext.
type ConnectionProfile struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	SourceSystem    string         `json:"sourceSystem"`
	Config          map[string]any `json:"config"`
	SecretSet       bool           `json:"secretSet"`
	SecretUpdatedAt *time.Time     `json:"secretUpdatedAt,omitempty"`
	Status          string         `json:"status"`
	Version         int            `json:"version"`
	CreatedBy       string         `json:"createdBy"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

type ConnectionFilter struct {
	Kind   string
	Status string
	After  *ProjectCursor // (name, id) keyset, like projects
	Limit  int
}

func connectionNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeConnectionNotFound, "connection profile not found")
}

func connectionNameUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeConnectionName, "a connection profile with this name already exists")
}

// ConnectionUnknown is the 422 for a project that names a profile this tenant does not have.
func ConnectionUnknown() error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeConnectionUnknown, "connectionProfileId names no connection profile")
	p.Errors = map[string]string{"connectionProfileId": "no connection profile has this id"}
	return p
}

func sameConnection(a, b ConnectionProfile) bool {
	return a.Name == b.Name && a.Kind == b.Kind && a.SourceSystem == b.SourceSystem && reflect.DeepEqual(normalize(a.Config), normalize(b.Config))
}

// normalize round-trips through JSON so numbers compare the same whether they came from a request or the database.
func normalize(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

const connectionColumns = `id, name, kind, source_system, config, secret_sealed is not null, secret_updated_at, status, version, created_by, created_at, updated_at`

func scanConnection(row pgx.Row) (ConnectionProfile, error) {
	var c ConnectionProfile
	var cfg []byte
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.SourceSystem, &cfg, &c.SecretSet, &c.SecretUpdatedAt, &c.Status, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(cfg, &c.Config); err != nil {
		return c, err
	}
	if c.Config == nil {
		c.Config = map[string]any{}
	}
	return c, nil
}

// SaveConnectionProfile inserts a profile; created is false when the Idempotency-Key already stored the same one.
func (s *Postgres) SaveConnectionProfile(ctx context.Context, in ConnectionProfile, idempotencyKey string) (ConnectionProfile, bool, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	cfg, err := json.Marshal(normalize(in.Config))
	if err != nil {
		return ConnectionProfile{}, false, err
	}
	var saved ConnectionProfile
	created := false
	byKey := func(tx pgx.Tx) (ConnectionProfile, error) {
		return scanConnection(tx.QueryRow(ctx, `select `+connectionColumns+` from migration.connection_profile where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idempotencyKey != "" {
			prior, err := byKey(tx)
			if err == nil {
				if !sameConnection(prior, in) {
					return keyReuse()
				}
				saved = prior
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		saved, err = scanConnection(tx.QueryRow(ctx, `insert into migration.connection_profile
			(tenant_id, id, name, kind, source_system, config, status, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+connectionColumns, p.TenantID, in.ID, in.Name, in.Kind, in.SourceSystem, cfg, in.Status, p.UserID, nullIfEmpty(idempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			prior, err := byKey(tx)
			if err != nil {
				return err
			}
			if !sameConnection(prior, in) {
				return keyReuse()
			}
			saved = prior
			return nil
		}
		created = err == nil
		return err
	})
	if isUniqueViolation(err, "connection_profile_tenant_id_name_key") {
		return ConnectionProfile{}, false, connectionNameUsed()
	}
	return saved, created, err
}

func (s *Postgres) GetConnectionProfile(ctx context.Context, id string) (ConnectionProfile, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return ConnectionProfile{}, connectionNotFound()
	}
	var out ConnectionProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanConnection(tx.QueryRow(ctx, `select `+connectionColumns+` from migration.connection_profile where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectionProfile{}, connectionNotFound()
	}
	return out, err
}

// ListConnectionProfiles pages profiles in (name, id) order, at most Limit rows.
func (s *Postgres) ListConnectionProfiles(ctx context.Context, f ConnectionFilter) ([]ConnectionProfile, error) {
	p, _ := tenant.FromContext(ctx)
	afterName, afterID := "", "00000000-0000-0000-0000-000000000000"
	if f.After != nil {
		afterName, afterID = f.After.Name, f.After.ID
	}
	var out []ConnectionProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+connectionColumns+` from migration.connection_profile
			where tenant_id = $1 and ($2 = '' or kind = $2) and ($3 = '' or status = $3)
			and ($4::boolean is false or (name, id) > ($5, $6::uuid))
			order by name, id limit $7`, p.TenantID, f.Kind, f.Status, f.After != nil, afterName, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConnection(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateConnectionProfile applies edit under the profile's version; the edit may change name, config and status, and
// may set SecretSet to false to clear the stored secret (it cannot set one: that is SetConnectionSecret).
func (s *Postgres) UpdateConnectionProfile(ctx context.Context, id string, version int, edit func(*ConnectionProfile) error) (ConnectionProfile, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return ConnectionProfile{}, connectionNotFound()
	}
	var out ConnectionProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanConnection(tx.QueryRow(ctx, `select `+connectionColumns+` from migration.connection_profile where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return connectionNotFound()
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
		cfg, err := json.Marshal(normalize(cur.Config))
		if err != nil {
			return err
		}
		out, err = scanConnection(tx.QueryRow(ctx, `update migration.connection_profile set name = $3, config = $4, status = $5,
			secret_sealed = case when $6 then secret_sealed end, secret_key_id = case when $6 then secret_key_id end,
			secret_updated_at = case when $6 then secret_updated_at end, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+connectionColumns, p.TenantID, id, cur.Name, cfg, cur.Status, cur.SecretSet))
		return err
	})
	if isUniqueViolation(err, "connection_profile_tenant_id_name_key") {
		return ConnectionProfile{}, connectionNameUsed()
	}
	return out, err
}

// SetConnectionSecret stores an already sealed secret (the caller seals it with fieldcrypt, bound to the tenant and the
// profile id) and bumps the version. keyID names the master key that sealed it.
func (s *Postgres) SetConnectionSecret(ctx context.Context, id string, sealed []byte, keyID string) (ConnectionProfile, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return ConnectionProfile{}, connectionNotFound()
	}
	var out ConnectionProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanConnection(tx.QueryRow(ctx, `update migration.connection_profile set secret_sealed = $3, secret_key_id = $4, secret_updated_at = now(), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+connectionColumns, p.TenantID, id, sealed, keyID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectionProfile{}, connectionNotFound()
	}
	return out, err
}

// ConnectionSecret returns the sealed secret and the key id that sealed it; sealed is nil when none is set.
func (s *Postgres) ConnectionSecret(ctx context.Context, id string) (sealed []byte, keyID string, err error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return nil, "", connectionNotFound()
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var k *string
		if err := tx.QueryRow(ctx, `select secret_sealed, secret_key_id from migration.connection_profile where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&sealed, &k); err != nil {
			return err
		}
		if k != nil {
			keyID = *k
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", connectionNotFound()
	}
	return sealed, keyID, err
}

type memSecret struct {
	sealed []byte
	keyID  string
}

type memConnections struct {
	mu      sync.Mutex
	items   []ConnectionProfile
	secrets map[string]memSecret
	byKey   map[string]string
}

func (m *Mem) cn() *memConnections {
	m.cnOnce.Do(func() { m.cnState = &memConnections{byKey: map[string]string{}, secrets: map[string]memSecret{}} })
	return m.cnState
}

func (m *Mem) SaveConnectionProfile(ctx context.Context, in ConnectionProfile, idempotencyKey string) (ConnectionProfile, bool, error) {
	if m.SaveErr != nil {
		return ConnectionProfile{}, false, m.SaveErr
	}
	st := m.cn()
	st.mu.Lock()
	defer st.mu.Unlock()
	if id, ok := st.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		for _, c := range st.items {
			if c.ID == id {
				if !sameConnection(c, in) {
					return ConnectionProfile{}, false, keyReuse()
				}
				return c, false, nil
			}
		}
	}
	for _, c := range st.items {
		if c.Name == in.Name {
			return ConnectionProfile{}, false, connectionNameUsed()
		}
	}
	now := time.Now().UTC()
	in.ID, in.Version, in.CreatedAt, in.UpdatedAt = ids.New(), 1, now, now
	in.Config = normalize(in.Config)
	if p, ok := tenant.FromContext(ctx); ok {
		in.CreatedBy = p.UserID
	}
	st.items = append(st.items, in)
	if idempotencyKey != "" {
		st.byKey[idempotencyKey] = in.ID
	}
	return in, true, nil
}

func (m *Mem) GetConnectionProfile(_ context.Context, id string) (ConnectionProfile, error) {
	st := m.cn()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.items {
		if c.ID == id {
			return c, nil
		}
	}
	return ConnectionProfile{}, connectionNotFound()
}

func (m *Mem) ListConnectionProfiles(_ context.Context, f ConnectionFilter) ([]ConnectionProfile, error) {
	st := m.cn()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []ConnectionProfile
	for _, c := range st.items {
		if (f.Kind == "" || c.Kind == f.Kind) && (f.Status == "" || c.Status == f.Status) &&
			(f.After == nil || cmp.Or(strings.Compare(c.Name, f.After.Name), strings.Compare(c.ID, f.After.ID)) > 0) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b ConnectionProfile) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) UpdateConnectionProfile(_ context.Context, id string, version int, edit func(*ConnectionProfile) error) (ConnectionProfile, error) {
	st := m.cn()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.items {
		if st.items[i].ID != id {
			continue
		}
		if st.items[i].Version != version {
			return ConnectionProfile{}, versionConflict(st.items[i].Version)
		}
		cur := st.items[i]
		if err := edit(&cur); err != nil {
			return ConnectionProfile{}, err
		}
		for j := range st.items {
			if j != i && st.items[j].Name == cur.Name {
				return ConnectionProfile{}, connectionNameUsed()
			}
		}
		cur.Config = normalize(cur.Config)
		if !cur.SecretSet {
			delete(st.secrets, id)
			cur.SecretUpdatedAt = nil
		} else if !st.items[i].SecretSet {
			cur.SecretSet = false // an edit cannot invent a secret
		}
		cur.Version++
		cur.UpdatedAt = time.Now().UTC()
		st.items[i] = cur
		return cur, nil
	}
	return ConnectionProfile{}, connectionNotFound()
}

func (m *Mem) SetConnectionSecret(_ context.Context, id string, sealed []byte, keyID string) (ConnectionProfile, error) {
	st := m.cn()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.items {
		if st.items[i].ID != id {
			continue
		}
		now := time.Now().UTC()
		st.secrets[id] = memSecret{sealed: bytes.Clone(sealed), keyID: keyID}
		st.items[i].SecretSet, st.items[i].SecretUpdatedAt = true, &now
		st.items[i].Version++
		st.items[i].UpdatedAt = now
		return st.items[i], nil
	}
	return ConnectionProfile{}, connectionNotFound()
}

func (m *Mem) ConnectionSecret(_ context.Context, id string) ([]byte, string, error) {
	st := m.cn()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.items {
		if c.ID == id {
			s := st.secrets[id]
			return bytes.Clone(s.sealed), s.keyID, nil
		}
	}
	return nil, "", connectionNotFound()
}
