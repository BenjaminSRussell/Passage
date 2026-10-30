package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const (
	CodeRunNotFound     = "AUTH-0404"
	CodeSpecNotFound    = "AUTH-0404"
	CodeSpecVersionUsed = "MIG-SPEC-VERSION-EXISTS"
	CodeSpecKeyReuse    = "idempotency_key_reuse"
	maxListLimit        = 200
	defaultListLimit    = 50
)

type MappingSpec struct {
	ID         string          `json:"id"`
	Entity     string          `json:"entity"`
	Version    int             `json:"version"`
	Checksum   string          `json:"checksum"`
	FieldCount int             `json:"fieldCount"`
	TestCount  int             `json:"testCount"`
	Spec       json.RawMessage `json:"-"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type RunFilter struct {
	Entity string
	Status string
	Limit  int
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	return min(n, maxListLimit)
}

type Run struct {
	ID             string          `json:"id"`
	SourceSystem   string          `json:"sourceSystem"`
	Mode           string          `json:"mode"`
	Entity         string          `json:"entity"`
	DryRun         bool            `json:"dryRun"`
	MappingVersion int             `json:"mappingVersion"`
	Mapping        json.RawMessage `json:"-"`
	ParentRunID    string          `json:"parentRunId,omitempty"`
	MappingSpecID  string          `json:"mappingSpecId,omitempty"`
	Report         domain.Report   `json:"report"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type Store interface {
	SaveRun(ctx context.Context, run Run, idempotencyKey string) (Run, error)
	RunByKey(ctx context.Context, idempotencyKey string) (Run, bool, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, f RunFilter) ([]Run, error)
	// SaveMappingSpec stores an immutable mapping version. created is false when the same
	// (entity, version) already holds the same checksum; a different checksum is a conflict.
	SaveMappingSpec(ctx context.Context, spec MappingSpec, idempotencyKey string) (saved MappingSpec, created bool, err error)
	GetMappingSpec(ctx context.Context, id string) (MappingSpec, error)
	ListMappingSpecs(ctx context.Context, entity string, limit int) ([]MappingSpec, error)
	Xref() domain.XrefStore
	Ping(ctx context.Context) error
}

func notFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "migration run not found")
}

type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Postgres) Xref() domain.XrefStore { return pgXref{s.pool} }

const runColumns = `id, source_system, mode, entity, dry_run, mapping_version, mapping, coalesce(parent_run_id::text, ''), coalesce(mapping_spec_id::text, ''), report, created_at`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var report []byte
	if err := row.Scan(&r.ID, &r.SourceSystem, &r.Mode, &r.Entity, &r.DryRun, &r.MappingVersion, &r.Mapping, &r.ParentRunID, &r.MappingSpecID, &report, &r.CreatedAt); err != nil {
		return Run{}, err
	}
	return r, json.Unmarshal(report, &r.Report)
}

func (s *Postgres) SaveRun(ctx context.Context, run Run, idempotencyKey string) (Run, error) {
	p, _ := tenant.FromContext(ctx)
	var key, parent, specID *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	if run.ParentRunID != "" {
		parent = &run.ParentRunID
	}
	if run.MappingSpecID != "" {
		specID = &run.MappingSpecID
	}
	report, err := json.Marshal(run.Report)
	if err != nil {
		return Run{}, err
	}
	run.ID = ids.New()
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into migration.run (tenant_id, id, source_system, mode, entity, status, dry_run, mapping_version, mapping, parent_run_id, report, idempotency_key, mapping_spec_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			on conflict (tenant_id, idempotency_key) do nothing`,
			p.TenantID, run.ID, run.SourceSystem, run.Mode, run.Entity, run.Report.Status, run.DryRun, run.MappingVersion, run.Mapping, parent, report, key, specID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			replayed = true
			return nil
		}
		return insertLedger(ctx, tx, p.TenantID, run.ID, run.Report.Ledger)
	})
	if err != nil {
		return Run{}, err
	}
	if replayed {
		existing, _, err := s.RunByKey(ctx, idempotencyKey)
		return existing, err
	}
	return s.GetRun(ctx, run.ID)
}

func insertLedger(ctx context.Context, tx pgx.Tx, tenantID, runID string, ledger []domain.LedgerEntry) error {
	n := len(ledger)
	keys, targets, idem, hashes, actions := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, e := range ledger {
		keys[i], targets[i], idem[i], hashes[i], actions[i] = e.SourceKey, e.TargetID, e.IdempotencyKey, e.ContentHash, e.Action
	}
	_, err := tx.Exec(ctx, `insert into migration.run_ledger (tenant_id, run_id, source_key, target_id, idempotency_key, content_hash, action)
		select $1, $2, l.source_key, nullif(l.target_id, '')::uuid, l.idempotency_key, l.content_hash, l.action
		from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) as l(source_key, target_id, idempotency_key, content_hash, action)`,
		tenantID, runID, keys, targets, idem, hashes, actions)
	return err
}

func (s *Postgres) RunByKey(ctx context.Context, idempotencyKey string) (Run, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from migration.run where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, false, nil
	}
	return run, err == nil, err
}

func (s *Postgres) GetRun(ctx context.Context, id string) (Run, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return Run{}, notFound()
	}
	var run Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from migration.run where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, notFound()
	}
	return run, err
}

func (s *Postgres) ListRuns(ctx context.Context, f RunFilter) ([]Run, error) {
	p, _ := tenant.FromContext(ctx)
	var runs []Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from migration.run
			where tenant_id = $1 and ($2 = '' or entity = $2) and ($3 = '' or status = $3)
			order by created_at desc, id desc limit $4`, p.TenantID, f.Entity, f.Status, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			runs = append(runs, r)
		}
		return rows.Err()
	})
	return runs, err
}

const specColumns = `id, entity, version, checksum, field_count, test_count, spec, created_at`

func scanSpec(row pgx.Row) (MappingSpec, error) {
	var m MappingSpec
	err := row.Scan(&m.ID, &m.Entity, &m.Version, &m.Checksum, &m.FieldCount, &m.TestCount, &m.Spec, &m.CreatedAt)
	return m, err
}

func specNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeSpecNotFound, "mapping spec not found")
}

func versionUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeSpecVersionUsed, "this entity and version already hold a different mapping; publish a higher version")
}

func (s *Postgres) SaveMappingSpec(ctx context.Context, spec MappingSpec, idempotencyKey string) (MappingSpec, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	spec.ID = ids.New()
	var saved MappingSpec
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into migration.mapping_spec (tenant_id, id, entity, version, checksum, field_count, test_count, spec, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict do nothing`,
			p.TenantID, spec.ID, spec.Entity, spec.Version, spec.Checksum, spec.FieldCount, spec.TestCount, spec.Spec, key)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if created {
			saved, err = scanSpec(tx.QueryRow(ctx, `select `+specColumns+` from migration.mapping_spec where tenant_id = $1 and id = $2`, p.TenantID, spec.ID))
			return err
		}
		if key != nil {
			saved, err = scanSpec(tx.QueryRow(ctx, `select `+specColumns+` from migration.mapping_spec where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				if saved.Entity != spec.Entity || saved.Version != spec.Version || saved.Checksum != spec.Checksum {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		saved, err = scanSpec(tx.QueryRow(ctx, `select `+specColumns+` from migration.mapping_spec where tenant_id = $1 and entity = $2 and version = $3`, p.TenantID, spec.Entity, spec.Version))
		if err != nil {
			return err
		}
		if saved.Checksum != spec.Checksum {
			return versionUsed()
		}
		return nil
	})
	return saved, created, err
}

func (s *Postgres) GetMappingSpec(ctx context.Context, id string) (MappingSpec, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return MappingSpec{}, specNotFound()
	}
	var m MappingSpec
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		m, err = scanSpec(tx.QueryRow(ctx, `select `+specColumns+` from migration.mapping_spec where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MappingSpec{}, specNotFound()
	}
	return m, err
}

func (s *Postgres) ListMappingSpecs(ctx context.Context, entity string, limit int) ([]MappingSpec, error) {
	p, _ := tenant.FromContext(ctx)
	var out []MappingSpec
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+specColumns+` from migration.mapping_spec
			where tenant_id = $1 and ($2 = '' or entity = $2)
			order by entity, version desc limit $3`, p.TenantID, entity, clampLimit(limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanSpec(rows)
			if err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

type pgXref struct{ pool *pgxpool.Pool }

func (x pgXref) Get(ctx context.Context, k domain.XrefKey) (domain.XrefEntry, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var e domain.XrefEntry
	err := dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select target_id::text, content_hash from migration.xref
			where tenant_id = $1 and source_system = $2 and source_entity = $3 and source_key = $4`,
			p.TenantID, k.SourceSystem, k.Entity, k.SourceKey).Scan(&e.TargetID, &e.ContentHash)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.XrefEntry{}, false, nil
	}
	return e, err == nil, err
}

func (x pgXref) Put(ctx context.Context, k domain.XrefKey, e domain.XrefEntry) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id, content_hash)
			values ($1, $2, $3, $4, $5, $6)
			on conflict (tenant_id, source_system, source_entity, source_key)
			do update set content_hash = excluded.content_hash, updated_at = now()`,
			p.TenantID, k.SourceSystem, k.Entity, k.SourceKey, e.TargetID, e.ContentHash)
		return err
	})
}

func (x pgXref) GetMany(ctx context.Context, sourceSystem, entity string, sourceKeys []string) (map[string]domain.XrefEntry, error) {
	p, _ := tenant.FromContext(ctx)
	found := make(map[string]domain.XrefEntry, len(sourceKeys))
	err := dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_key, target_id::text, content_hash from migration.xref
			where tenant_id = $1 and source_system = $2 and source_entity = $3 and source_key = any($4)`,
			p.TenantID, sourceSystem, entity, sourceKeys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var e domain.XrefEntry
			if err := rows.Scan(&key, &e.TargetID, &e.ContentHash); err != nil {
				return err
			}
			found[key] = e
		}
		return rows.Err()
	})
	return found, err
}

func (x pgXref) PutMany(ctx context.Context, sourceSystem, entity string, items []domain.XrefItem) error {
	if len(items) == 0 {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	keys, targets, hashes := make([]string, len(items)), make([]string, len(items)), make([]string, len(items))
	for i, it := range items {
		keys[i], targets[i], hashes[i] = it.SourceKey, it.Entry.TargetID, it.Entry.ContentHash
	}
	return dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id, content_hash)
			select $1, $2, $3, i.source_key, i.target_id::uuid, i.content_hash
			from unnest($4::text[], $5::text[], $6::text[]) as i(source_key, target_id, content_hash)
			on conflict (tenant_id, source_system, source_entity, source_key)
			do update set content_hash = excluded.content_hash, updated_at = now()`,
			p.TenantID, sourceSystem, entity, keys, targets, hashes)
		return err
	})
}

func (x pgXref) Reserve(ctx context.Context, sourceSystem, entity string, items []domain.XrefItem) (map[string]string, error) {
	won := make(map[string]string, len(items))
	if len(items) == 0 {
		return won, nil
	}
	p, _ := tenant.FromContext(ctx)
	keys, targets := make([]string, len(items)), make([]string, len(items))
	for i, it := range items {
		keys[i], targets[i] = it.SourceKey, it.Entry.TargetID
	}
	err := dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id)
			select $1, $2, $3, i.source_key, i.target_id::uuid
			from unnest($4::text[], $5::text[]) as i(source_key, target_id)
			on conflict (tenant_id, source_system, source_entity, source_key) do nothing`,
			p.TenantID, sourceSystem, entity, keys, targets); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select source_key, target_id::text from migration.xref
			where tenant_id = $1 and source_system = $2 and source_entity = $3 and source_key = any($4)`,
			p.TenantID, sourceSystem, entity, keys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, id string
			if err := rows.Scan(&key, &id); err != nil {
				return err
			}
			won[key] = id
		}
		return rows.Err()
	})
	return won, err
}

func (x pgXref) Keys(ctx context.Context, sourceSystem, entity string) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	var keys []string
	err := dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_key from migration.xref where tenant_id = $1 and source_system = $2 and source_entity = $3`, p.TenantID, sourceSystem, entity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return rows.Err()
	})
	return keys, err
}

type Mem struct {
	mu      sync.Mutex
	runs    []Run
	specs   []MappingSpec
	specKey map[string]string
	byKey   map[string]string
	xref    *domain.MemXref
	PingErr error
	SaveErr error
}

func NewMem() *Mem {
	return &Mem{byKey: map[string]string{}, specKey: map[string]string{}, xref: domain.NewMemXref()}
}

func (m *Mem) Ping(context.Context) error { return m.PingErr }

func (m *Mem) Xref() domain.XrefStore { return m.xref }

func (m *Mem) SaveRun(_ context.Context, run Run, idempotencyKey string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SaveErr != nil {
		return Run{}, m.SaveErr
	}
	if id, ok := m.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		return m.find(id), nil
	}
	run.ID, run.CreatedAt = ids.New(), time.Now().UTC()
	m.runs = append(m.runs, run)
	if idempotencyKey != "" {
		m.byKey[idempotencyKey] = run.ID
	}
	return run, nil
}

func (m *Mem) RunByKey(_ context.Context, idempotencyKey string) (Run, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byKey[idempotencyKey]
	if !ok {
		return Run{}, false, nil
	}
	return m.find(id), true, nil
}

func (m *Mem) GetRun(_ context.Context, id string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.find(id); r.ID != "" {
		return r, nil
	}
	return Run{}, notFound()
}

func (m *Mem) find(id string) Run {
	for _, r := range m.runs {
		if r.ID == id {
			return r
		}
	}
	return Run{}
}

func (m *Mem) ListRuns(_ context.Context, f RunFilter) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for i := len(m.runs) - 1; i >= 0 && len(out) < clampLimit(f.Limit); i-- {
		r := m.runs[i]
		if (f.Entity == "" || r.Entity == f.Entity) && (f.Status == "" || r.Report.Status == f.Status) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *Mem) SaveMappingSpec(_ context.Context, spec MappingSpec, idempotencyKey string) (MappingSpec, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.SaveErr != nil {
		return MappingSpec{}, false, m.SaveErr
	}
	if id, ok := m.specKey[idempotencyKey]; ok && idempotencyKey != "" {
		for _, s := range m.specs {
			if s.ID == id {
				if s.Entity != spec.Entity || s.Version != spec.Version || s.Checksum != spec.Checksum {
					return MappingSpec{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
				}
				return s, false, nil
			}
		}
	}
	for _, s := range m.specs {
		if s.Entity == spec.Entity && s.Version == spec.Version {
			if s.Checksum != spec.Checksum {
				return MappingSpec{}, false, versionUsed()
			}
			return s, false, nil
		}
	}
	spec.ID, spec.CreatedAt = ids.New(), time.Now().UTC()
	m.specs = append(m.specs, spec)
	if idempotencyKey != "" {
		m.specKey[idempotencyKey] = spec.ID
	}
	return spec, true, nil
}

func (m *Mem) GetMappingSpec(_ context.Context, id string) (MappingSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.specs {
		if s.ID == id {
			return s, nil
		}
	}
	return MappingSpec{}, specNotFound()
}

func (m *Mem) ListMappingSpecs(_ context.Context, entity string, limit int) ([]MappingSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MappingSpec
	for _, s := range m.specs {
		if entity == "" || s.Entity == entity {
			out = append(out, s)
		}
	}
	// Same order as Postgres: entity, then newest version first.
	slices.SortFunc(out, func(a, b MappingSpec) int {
		return cmp.Or(strings.Compare(a.Entity, b.Entity), b.Version-a.Version)
	})
	return out[:min(len(out), clampLimit(limit))], nil
}
