package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const CodePackageNotFound = "AUTH-0404"

// PackageApplied is one entry of a package's applied history.
type PackageApplied struct {
	TenantID   string    `json:"tenantId"`
	AppliedAt  time.Time `json:"appliedAt"`
	Key        string    `json:"idempotencyKey,omitempty"`
	DiffCounts Counts    `json:"diffCounts"`
}

type Counts struct {
	Create    int `json:"create"`
	Update    int `json:"update"`
	Unchanged int `json:"unchanged"`
}

type ConfigPackage struct {
	ID            string           `json:"id"`
	Version       string           `json:"version"`
	SchemaVersion int              `json:"schemaVersion"`
	Manifest      domain.Manifest  `json:"manifest"`
	Signature     []byte           `json:"-"`
	CreatedBy     string           `json:"createdBy"`
	Applied       []PackageApplied `json:"applied"`
	CreatedAt     time.Time        `json:"createdAt"`
}

func packageNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodePackageNotFound, "configuration package not found")
}

func keyReuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
}

func samePackage(a, b ConfigPackage) bool {
	am, _ := json.Marshal(a.Manifest)
	bm, _ := json.Marshal(b.Manifest)
	return a.Version == b.Version && a.SchemaVersion == b.SchemaVersion && bytes.Equal(a.Signature, b.Signature) && bytes.Equal(am, bm)
}

const packageColumns = `id, version, schema_version, manifest, signature, created_by, applied, created_at`

func scanPackage(row pgx.Row) (ConfigPackage, error) {
	var c ConfigPackage
	var manifest, applied []byte
	if err := row.Scan(&c.ID, &c.Version, &c.SchemaVersion, &manifest, &c.Signature, &c.CreatedBy, &applied, &c.CreatedAt); err != nil {
		return ConfigPackage{}, err
	}
	if err := json.Unmarshal(manifest, &c.Manifest); err != nil {
		return ConfigPackage{}, err
	}
	return c, json.Unmarshal(applied, &c.Applied)
}

func (s *Postgres) SaveConfigPackage(ctx context.Context, pkg ConfigPackage, idempotencyKey string) (ConfigPackage, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	manifest, err := json.Marshal(pkg.Manifest)
	if err != nil {
		return ConfigPackage{}, false, err
	}
	pkg.ID = ids.New()
	var saved ConfigPackage
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into migration.config_package (tenant_id, id, version, schema_version, manifest, signature, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8) on conflict do nothing`,
			p.TenantID, pkg.ID, pkg.Version, pkg.SchemaVersion, manifest, pkg.Signature, p.UserID, key)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if created {
			saved, err = scanPackage(tx.QueryRow(ctx, `select `+packageColumns+` from migration.config_package where tenant_id = $1 and id = $2`, p.TenantID, pkg.ID))
			return err
		}
		saved, err = scanPackage(tx.QueryRow(ctx, `select `+packageColumns+` from migration.config_package where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		if err != nil {
			return err
		}
		if !samePackage(saved, pkg) {
			return keyReuse()
		}
		return nil
	})
	return saved, created, err
}

func (s *Postgres) ConfigPackageByKey(ctx context.Context, idempotencyKey string) (ConfigPackage, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var c ConfigPackage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanPackage(tx.QueryRow(ctx, `select `+packageColumns+` from migration.config_package where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfigPackage{}, false, nil
	}
	return c, err == nil, err
}

func (s *Postgres) AppendPackageApplied(ctx context.Context, id string, entry PackageApplied) (ConfigPackage, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return ConfigPackage{}, packageNotFound()
	}
	entry.TenantID = p.TenantID
	raw, err := json.Marshal([]PackageApplied{entry})
	if err != nil {
		return ConfigPackage{}, err
	}
	var c ConfigPackage
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanPackage(tx.QueryRow(ctx, `update migration.config_package set applied = applied || $3::jsonb
			where tenant_id = $1 and id = $2 returning `+packageColumns, p.TenantID, id, raw))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfigPackage{}, packageNotFound()
	}
	return c, err
}

func (s *Postgres) GetConfigPackage(ctx context.Context, id string) (ConfigPackage, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return ConfigPackage{}, packageNotFound()
	}
	var c ConfigPackage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanPackage(tx.QueryRow(ctx, `select `+packageColumns+` from migration.config_package where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConfigPackage{}, packageNotFound()
	}
	return c, err
}

func (s *Postgres) ListConfigPackages(ctx context.Context, limit int) ([]ConfigPackage, error) {
	p, _ := tenant.FromContext(ctx)
	var out []ConfigPackage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+packageColumns+` from migration.config_package where tenant_id = $1 order by created_at desc, id desc limit $2`, p.TenantID, clampLimit(limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanPackage(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) SaveRewriteRules(ctx context.Context, projectID string, rules []domain.KeyRewrite) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for i, r := range rules {
			if _, err := tx.Exec(ctx, `insert into migration.key_rewrite_rule (tenant_id, id, project_id, field, from_value, to_value, position)
				values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, ids.New(), projectID, r.Field, r.From, r.To, i); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Postgres) RewriteRules(ctx context.Context, projectID string) ([]domain.KeyRewrite, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.KeyRewrite{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select field, from_value, to_value from migration.key_rewrite_rule
			where tenant_id = $1 and project_id = $2 order by position, id`, p.TenantID, projectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.KeyRewrite
			if err := rows.Scan(&r.Field, &r.From, &r.To); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// MemPackages is the in-memory package and rewrite-rule state of Mem.
type memPackages struct {
	mu    sync.Mutex
	pkgs  []ConfigPackage
	byKey map[string]string
	rules map[string][]domain.KeyRewrite
}

func (m *Mem) pk() *memPackages {
	m.pkgOnce.Do(func() { m.pkgState = &memPackages{byKey: map[string]string{}, rules: map[string][]domain.KeyRewrite{}} })
	return m.pkgState
}

func (m *Mem) SaveConfigPackage(ctx context.Context, pkg ConfigPackage, idempotencyKey string) (ConfigPackage, bool, error) {
	if m.SaveErr != nil {
		return ConfigPackage{}, false, m.SaveErr
	}
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	if id, ok := st.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		for _, c := range st.pkgs {
			if c.ID == id {
				if !samePackage(c, pkg) {
					return ConfigPackage{}, false, keyReuse()
				}
				return c, false, nil
			}
		}
	}
	pkg.ID, pkg.CreatedAt, pkg.Applied = ids.New(), time.Now().UTC(), []PackageApplied{}
	if p, ok := tenant.FromContext(ctx); ok {
		pkg.CreatedBy = p.UserID
	}
	st.pkgs = append(st.pkgs, pkg)
	if idempotencyKey != "" {
		st.byKey[idempotencyKey] = pkg.ID
	}
	return pkg, true, nil
}

func (m *Mem) ConfigPackageByKey(_ context.Context, idempotencyKey string) (ConfigPackage, bool, error) {
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	id, ok := st.byKey[idempotencyKey]
	if !ok {
		return ConfigPackage{}, false, nil
	}
	for _, c := range st.pkgs {
		if c.ID == id {
			return c, true, nil
		}
	}
	return ConfigPackage{}, false, nil
}

func (m *Mem) AppendPackageApplied(ctx context.Context, id string, entry PackageApplied) (ConfigPackage, error) {
	if p, ok := tenant.FromContext(ctx); ok {
		entry.TenantID = p.TenantID
	}
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.pkgs {
		if st.pkgs[i].ID == id {
			st.pkgs[i].Applied = append(append([]PackageApplied(nil), st.pkgs[i].Applied...), entry)
			return st.pkgs[i], nil
		}
	}
	return ConfigPackage{}, packageNotFound()
}

func (m *Mem) GetConfigPackage(_ context.Context, id string) (ConfigPackage, error) {
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.pkgs {
		if c.ID == id {
			return c, nil
		}
	}
	return ConfigPackage{}, packageNotFound()
}

func (m *Mem) ListConfigPackages(_ context.Context, limit int) ([]ConfigPackage, error) {
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []ConfigPackage
	for i := len(st.pkgs) - 1; i >= 0 && len(out) < clampLimit(limit); i-- {
		out = append(out, st.pkgs[i])
	}
	return out, nil
}

func (m *Mem) SaveRewriteRules(_ context.Context, projectID string, rules []domain.KeyRewrite) error {
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rules[projectID] = append(st.rules[projectID], rules...)
	return nil
}

func (m *Mem) RewriteRules(_ context.Context, projectID string) ([]domain.KeyRewrite, error) {
	st := m.pk()
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]domain.KeyRewrite{}, st.rules[projectID]...), nil
}
