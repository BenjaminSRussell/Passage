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
	ID         string `json:"id"`
	Entity     string `json:"entity"`
	Version    int    `json:"version"`
	Checksum   string `json:"checksum"`
	FieldCount int    `json:"fieldCount"`
	TestCount  int    `json:"testCount"`
	// ProjectID is the migration project the spec belongs to; empty is a tenant-wide spec.
	ProjectID string `json:"projectId,omitempty"`
	// NaturalKey is the mapped columns that identify a record, as the mapping names them.
	NaturalKey []string        `json:"naturalKey"`
	Spec       json.RawMessage `json:"-"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// SpecScope narrows a spec listing: the specs of one project, or the tenant-wide ones (no project). The zero value
// is every spec.
type SpecScope struct {
	ProjectID  string
	TenantWide bool
}

// RunCursor is the keyset position after the last run of a page (newest first).
type RunCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        string    `json:"i"`
}

// SpecCursor is the keyset position after the last mapping spec of a page (entity, then newest version first).
// The id breaks the tie between specs of different projects that share an entity and version.
type SpecCursor struct {
	Entity  string `json:"e"`
	Version int    `json:"v"`
	ID      string `json:"i,omitempty"`
}

// TemplateCursor is the keyset position after the last data template of a page (entity, name, newest version first).
type TemplateCursor struct {
	Entity  string `json:"e"`
	Name    string `json:"n"`
	Version int    `json:"v"`
}

type RunFilter struct {
	Entity    string
	Status    string
	ProjectID string
	// BindingID keeps the runs the sync scheduler made for one entity binding.
	BindingID string
	After     *RunCursor
	Limit     int
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	// One more than the largest page, so a handler can fetch limit+1 rows to learn whether another page follows.
	return min(n, maxListLimit+1)
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
	DataTemplateID string          `json:"dataTemplateId,omitempty"`
	ProjectID      string          `json:"projectId,omitempty"`
	BindingID      string          `json:"bindingId,omitempty"`
	Report         domain.Report   `json:"report"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type Store interface {
	SaveRun(ctx context.Context, run Run, idempotencyKey string) (Run, error)
	RunByKey(ctx context.Context, idempotencyKey string) (Run, bool, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, f RunFilter) ([]Run, error)
	// BeginProjectRun claims a project for the run that will be saved as runID (BR-10.2): 404 for an unknown
	// project, 409 MIG-PROJECT-ARCHIVED for an archived one and 409 MIG-RUN-ACTIVE, naming the other run, while
	// another run holds a claim younger than the abandonment window. EndProjectRun releases runID's claim only.
	BeginProjectRun(ctx context.Context, projectID, runID string) (Project, error)
	EndProjectRun(ctx context.Context, projectID, runID string) error
	// SaveMappingSpec stores an immutable mapping version. created is false when the same
	// (project, entity, version) already holds the same checksum; a different checksum is a conflict.
	SaveMappingSpec(ctx context.Context, spec MappingSpec, idempotencyKey string) (saved MappingSpec, created bool, err error)
	GetMappingSpec(ctx context.Context, id string) (MappingSpec, error)
	// SaveDataTemplate stores an immutable template version (FR-10.18). created is false when the same
	// (entity, name, version) already holds the same checksum; a different checksum is a conflict.
	SaveDataTemplate(ctx context.Context, tmpl DataTemplate, idempotencyKey string) (saved DataTemplate, created bool, err error)
	GetDataTemplate(ctx context.Context, id string) (DataTemplate, error)
	ListDataTemplates(ctx context.Context, entity string, after *TemplateCursor, limit int) ([]DataTemplate, error)
	// RunLedger lists a run's per-row lineage in source key order; templatedOnly keeps rows that took a template default.
	RunLedger(ctx context.Context, runID string, templatedOnly bool, afterKey string, limit int) ([]LedgerRow, error)
	ListMappingSpecs(ctx context.Context, entity string, after *SpecCursor, limit int) ([]MappingSpec, error)
	// ListMappingSpecsIn lists the specs of one scope, in the same order and paging as ListMappingSpecs.
	ListMappingSpecsIn(ctx context.Context, scope SpecScope, entity string, after *SpecCursor, limit int) ([]MappingSpec, error)
	// SaveConfigPackage records an applied package once; a repeated Idempotency-Key returns the stored row
	// (created false) or a 422 when the stored package differs.
	SaveConfigPackage(ctx context.Context, pkg ConfigPackage, idempotencyKey string) (saved ConfigPackage, created bool, err error)
	ConfigPackageByKey(ctx context.Context, idempotencyKey string) (ConfigPackage, bool, error)
	AppendPackageApplied(ctx context.Context, id string, entry PackageApplied) (ConfigPackage, error)
	GetConfigPackage(ctx context.Context, id string) (ConfigPackage, error)
	ListConfigPackages(ctx context.Context, limit int) ([]ConfigPackage, error)
	// SaveProject creates a migration project (EN-064); created is false when the Idempotency-Key already stored it.
	SaveProject(ctx context.Context, p Project, idempotencyKey string) (saved Project, created bool, err error)
	GetProject(ctx context.Context, id string) (Project, error)
	ListProjects(ctx context.Context, f ProjectFilter) ([]Project, error)
	// UpdateProject applies edit under the project's version (409 MIG-VERSION-CONFLICT otherwise) and bumps it.
	UpdateProject(ctx context.Context, id string, version int, edit func(*Project) error) (Project, error)
	// ReplaceRewriteRules swaps a project's whole rule set under its version and bumps the version.
	ReplaceRewriteRules(ctx context.Context, projectID string, version int, rules []domain.KeyRewrite) (Project, error)
	RewriteRules(ctx context.Context, projectID string) ([]domain.KeyRewrite, error)
	// Connection profiles (integration track S1). The store keeps the sealed secret as bytes; sealing and opening
	// belong to the caller, which holds the fieldcrypt keys. Nothing returned by Get or List carries it.
	SaveConnectionProfile(ctx context.Context, c ConnectionProfile, idempotencyKey string) (saved ConnectionProfile, created bool, err error)
	GetConnectionProfile(ctx context.Context, id string) (ConnectionProfile, error)
	ListConnectionProfiles(ctx context.Context, f ConnectionFilter) ([]ConnectionProfile, error)
	UpdateConnectionProfile(ctx context.Context, id string, version int, edit func(*ConnectionProfile) error) (ConnectionProfile, error)
	SetConnectionSecret(ctx context.Context, id string, sealed []byte, keyID string) (ConnectionProfile, error)
	ConnectionSecret(ctx context.Context, id string) (sealed []byte, keyID string, err error)
	// Entity bindings (integration track S2): a table, query, endpoint or file of a profile feeding one entity.
	SaveEntityBinding(ctx context.Context, b EntityBinding, idempotencyKey string) (saved EntityBinding, created bool, err error)
	GetEntityBinding(ctx context.Context, id string) (EntityBinding, error)
	ListEntityBindings(ctx context.Context, f BindingFilter) ([]EntityBinding, error)
	UpdateEntityBinding(ctx context.Context, id string, version int, edit func(*EntityBinding) error) (EntityBinding, error)
	// ListXref browses the legacy-key cross-reference in keyset order; XrefSummary counts keys per source system and entity.
	ListXref(ctx context.Context, f XrefFilter) ([]XrefRow, error)
	ResolveXref(ctx context.Context, system, entity string, keys []string) (map[string]string, error)
	XrefSummary(ctx context.Context) ([]XrefCount, error)
	// Tenant settings (FR-10.16): defaults until the tenant writes them.
	GetTenantSettings(ctx context.Context) (TenantSettings, error)
	PutTenantSettings(ctx context.Context, version, recurringMinIntervalMin int) (TenantSettings, error)
	SyncStore
	OutboundStore
	LookupStore
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

const runColumns = `id, source_system, mode, entity, dry_run, mapping_version, mapping, coalesce(parent_run_id::text, ''), coalesce(mapping_spec_id::text, ''), coalesce(data_template_id::text, ''), report, created_at, coalesce(project_id::text, ''), coalesce(binding_id::text, '')`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var report []byte
	if err := row.Scan(&r.ID, &r.SourceSystem, &r.Mode, &r.Entity, &r.DryRun, &r.MappingVersion, &r.Mapping, &r.ParentRunID, &r.MappingSpecID, &r.DataTemplateID, &report, &r.CreatedAt, &r.ProjectID, &r.BindingID); err != nil {
		return Run{}, err
	}
	return r, json.Unmarshal(report, &r.Report)
}

func (s *Postgres) SaveRun(ctx context.Context, run Run, idempotencyKey string) (Run, error) {
	p, _ := tenant.FromContext(ctx)
	var key, parent, specID, templateID, projectID, bindingID *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	if run.ParentRunID != "" {
		parent = &run.ParentRunID
	}
	if run.MappingSpecID != "" {
		specID = &run.MappingSpecID
	}
	if run.DataTemplateID != "" {
		templateID = &run.DataTemplateID
	}
	if run.ProjectID != "" {
		projectID = &run.ProjectID
	}
	if run.BindingID != "" {
		bindingID = &run.BindingID
	}
	report, err := json.Marshal(run.Report)
	if err != nil {
		return Run{}, err
	}
	if run.ID == "" {
		run.ID = ids.New() // a project run arrives with the id its claim was taken under
	}
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into migration.run (tenant_id, id, source_system, mode, entity, status, dry_run, mapping_version, mapping, parent_run_id, report, idempotency_key, mapping_spec_id, data_template_id, project_id, binding_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
			on conflict (tenant_id, idempotency_key) do nothing`,
			p.TenantID, run.ID, run.SourceSystem, run.Mode, run.Entity, run.Report.Status, run.DryRun, run.MappingVersion, run.Mapping, parent, report, key, specID, templateID, projectID, bindingID)
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
	keys, targets, idem, hashes, actions, fields := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, e := range ledger {
		keys[i], targets[i], idem[i], hashes[i], actions[i] = e.SourceKey, e.TargetID, e.IdempotencyKey, e.ContentHash, e.Action
		// Mapping targets are free text (a comma is legal), so each row's template targets travel as a JSON array.
		tf := e.TemplateFields
		if tf == nil {
			tf = []string{}
		}
		encoded, err := json.Marshal(tf)
		if err != nil {
			return err
		}
		fields[i] = string(encoded)
	}
	_, err := tx.Exec(ctx, `insert into migration.run_ledger (tenant_id, run_id, source_key, target_id, idempotency_key, content_hash, action, template_fields)
		select $1, $2, l.source_key, nullif(l.target_id, '')::uuid, l.idempotency_key, l.content_hash, l.action,
		  array(select f.v from jsonb_array_elements_text(l.template_fields::jsonb) with ordinality as f(v, i) order by f.i)
		from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[]) as l(source_key, target_id, idempotency_key, content_hash, action, template_fields)`,
		tenantID, runID, keys, targets, idem, hashes, actions, fields)
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
	afterAt, afterID := time.Time{}, "00000000-0000-0000-0000-000000000000"
	if f.After != nil {
		afterAt, afterID = f.After.CreatedAt, f.After.ID
	}
	var runs []Run
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from migration.run
			where tenant_id = $1 and ($2 = '' or entity = $2) and ($3 = '' or status = $3)
			and ($4 = '' or project_id = nullif($4, '')::uuid) and ($9 = '' or binding_id = nullif($9, '')::uuid)
			and ($5::boolean is false or (created_at, id) < ($6::timestamptz, $7::uuid))
			order by created_at desc, id desc limit $8`, p.TenantID, f.Entity, f.Status, f.ProjectID, f.After != nil, afterAt, afterID, clampLimit(f.Limit), f.BindingID)
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

const specColumns = `id, entity, version, checksum, field_count, test_count, spec, created_at, coalesce(project_id::text, ''), natural_key`

func scanSpec(row pgx.Row) (MappingSpec, error) {
	var m MappingSpec
	err := row.Scan(&m.ID, &m.Entity, &m.Version, &m.Checksum, &m.FieldCount, &m.TestCount, &m.Spec, &m.CreatedAt, &m.ProjectID, &m.NaturalKey)
	if m.NaturalKey == nil {
		m.NaturalKey = []string{}
	}
	return m, err
}

func specNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeSpecNotFound, "mapping spec not found")
}

// keyOrEmpty is the natural key as stored: never nil, so the column stays NOT NULL.
func keyOrEmpty(k []string) []string {
	if k == nil {
		return []string{}
	}
	return k
}

func versionUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeSpecVersionUsed, "this entity and version already hold a different mapping in this scope; publish a higher version")
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
		tag, err := tx.Exec(ctx, `insert into migration.mapping_spec (tenant_id, id, entity, version, checksum, field_count, test_count, spec, idempotency_key, project_id, natural_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) on conflict do nothing`,
			p.TenantID, spec.ID, spec.Entity, spec.Version, spec.Checksum, spec.FieldCount, spec.TestCount, spec.Spec, key, nullIfEmpty(spec.ProjectID), keyOrEmpty(spec.NaturalKey))
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
				if saved.Entity != spec.Entity || saved.Version != spec.Version || saved.Checksum != spec.Checksum || saved.ProjectID != spec.ProjectID {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
				}
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		saved, err = scanSpec(tx.QueryRow(ctx, `select `+specColumns+` from migration.mapping_spec where tenant_id = $1 and entity = $2 and version = $3
			and coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid) = coalesce($4::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`, p.TenantID, spec.Entity, spec.Version, nullIfEmpty(spec.ProjectID)))
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

func (s *Postgres) ListMappingSpecs(ctx context.Context, entity string, after *SpecCursor, limit int) ([]MappingSpec, error) {
	return s.ListMappingSpecsIn(ctx, SpecScope{}, entity, after, limit)
}

func (s *Postgres) ListMappingSpecsIn(ctx context.Context, scope SpecScope, entity string, after *SpecCursor, limit int) ([]MappingSpec, error) {
	if scope.ProjectID != "" && !ids.Valid(scope.ProjectID) {
		return []MappingSpec{}, nil
	}
	projectID := scope.ProjectID
	p, _ := tenant.FromContext(ctx)
	cur := SpecCursor{}
	if after != nil {
		cur = *after
	}
	var out []MappingSpec
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+specColumns+` from migration.mapping_spec
			where tenant_id = $1 and ($2 = '' or entity = $2)
			and ($3::boolean is false or entity > $4 or (entity = $4 and (version < $5 or (version = $5 and $8 <> '' and id > nullif($8, '')::uuid))))
			and ($7 = '' or project_id = nullif($7, '')::uuid) and ($9::boolean is false or project_id is null)
			order by entity, version desc, id limit $6`, p.TenantID, entity, after != nil, cur.Entity, cur.Version, clampLimit(limit), projectID, cur.ID, scope.TenantWide)
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
		return tx.QueryRow(ctx, `select target_id::text, content_hash, target_version, coalesce(legal_entity_id::text, '') from migration.xref
			where tenant_id = $1 and source_system = $2 and source_entity = $3 and source_key = $4`,
			p.TenantID, k.SourceSystem, k.Entity, k.SourceKey).Scan(&e.TargetID, &e.ContentHash, &e.TargetVersion, &e.LegalEntityID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.XrefEntry{}, false, nil
	}
	return e, err == nil, err
}

func (x pgXref) Put(ctx context.Context, k domain.XrefKey, e domain.XrefEntry) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		// The legal entity is written once: a row tagged earlier keeps its tag, an untagged row takes the one given.
		_, err := tx.Exec(ctx, `insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id, content_hash, target_version, legal_entity_id)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($9, '')::uuid)
			on conflict (tenant_id, source_system, source_entity, source_key)
			do update set target_id = case when $8 then excluded.target_id else migration.xref.target_id end,
				content_hash = excluded.content_hash, target_version = excluded.target_version,
				legal_entity_id = coalesce(migration.xref.legal_entity_id, excluded.legal_entity_id), updated_at = now()`,
			p.TenantID, k.SourceSystem, k.Entity, k.SourceKey, e.TargetID, e.ContentHash, e.TargetVersion, e.Rebind, e.LegalEntityID)
		return err
	})
}

func (x pgXref) GetMany(ctx context.Context, sourceSystem, entity string, sourceKeys []string) (map[string]domain.XrefEntry, error) {
	p, _ := tenant.FromContext(ctx)
	found := make(map[string]domain.XrefEntry, len(sourceKeys))
	err := dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select source_key, target_id::text, content_hash, target_version, coalesce(legal_entity_id::text, '') from migration.xref
			where tenant_id = $1 and source_system = $2 and source_entity = $3 and source_key = any($4)`,
			p.TenantID, sourceSystem, entity, sourceKeys)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var e domain.XrefEntry
			if err := rows.Scan(&key, &e.TargetID, &e.ContentHash, &e.TargetVersion, &e.LegalEntityID); err != nil {
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
	keys, targets, hashes, versions := make([]string, len(items)), make([]string, len(items)), make([]string, len(items)), make([]int32, len(items))
	rebinds, entities := make([]bool, len(items)), make([]string, len(items))
	for i, it := range items {
		keys[i], targets[i], hashes[i], versions[i], rebinds[i], entities[i] = it.SourceKey, it.Entry.TargetID, it.Entry.ContentHash, int32(it.Entry.TargetVersion), it.Entry.Rebind, it.Entry.LegalEntityID
	}
	return dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into migration.xref (tenant_id, source_system, source_entity, source_key, target_id, content_hash, target_version, legal_entity_id)
			select $1, $2, $3, i.source_key, i.target_id::uuid, i.content_hash, i.target_version, nullif(i.legal_entity_id, '')::uuid
			from unnest($4::text[], $5::text[], $6::text[], $7::int[], $8::text[]) as i(source_key, target_id, content_hash, target_version, legal_entity_id)
			on conflict (tenant_id, source_system, source_entity, source_key)
			do update set content_hash = excluded.content_hash, target_version = excluded.target_version,
				legal_entity_id = coalesce(migration.xref.legal_entity_id, excluded.legal_entity_id), updated_at = now()`,
			p.TenantID, sourceSystem, entity, keys, targets, hashes, versions, entities)
		if err != nil || !slices.Contains(rebinds, true) {
			return err
		}
		// The target assigned its own ids to these records: point the keys at them.
		_, err = tx.Exec(ctx, `update migration.xref x set target_id = i.target_id::uuid
			from unnest($4::text[], $5::text[], $6::bool[]) as i(source_key, target_id, rebind)
			where x.tenant_id = $1 and x.source_system = $2 and x.source_entity = $3 and x.source_key = i.source_key and i.rebind`,
			p.TenantID, sourceSystem, entity, keys, targets, rebinds)
		return err
	})
}

func (x pgXref) TagLegalEntities(ctx context.Context, sourceSystem, entity string, tags map[string]string) error {
	if len(tags) == 0 {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	keys, les := make([]string, 0, len(tags)), make([]string, 0, len(tags))
	for k, le := range tags {
		keys, les = append(keys, k), append(les, le)
	}
	return dbx.WithTenantTx(ctx, x.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update migration.xref x set legal_entity_id = t.legal_entity_id::uuid, updated_at = now()
			from unnest($4::text[], $5::text[]) as t(source_key, legal_entity_id)
			where x.tenant_id = $1 and x.source_system = $2 and x.source_entity = $3 and x.source_key = t.source_key
			  and x.legal_entity_id is null`, p.TenantID, sourceSystem, entity, keys, les)
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
	mu          sync.Mutex
	runs        []Run
	specs       []MappingSpec
	specKey     map[string]string
	templates   []DataTemplate
	templateKey map[string]string
	byKey       map[string]string
	xref        *domain.MemXref
	PingErr     error
	SaveErr     error

	pkgOnce  sync.Once
	pkgState *memPackages
	pjOnce   sync.Once
	pjState  *memProjects
	cnOnce   sync.Once
	cnState  *memConnections
	bnOnce   sync.Once
	bnState  *memBindings
	syOnce   sync.Once
	syState  *memSync
	obOnce   sync.Once
	obState  *memOutbound
	lkOnce   sync.Once
	lkState  *memLookup
	stOnce   sync.Once
	stState  *memSettings
}

func NewMem() *Mem {
	return &Mem{byKey: map[string]string{}, specKey: map[string]string{}, templateKey: map[string]string{}, xref: domain.NewMemXref()}
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
	if run.ID == "" {
		run.ID = ids.New()
	}
	run.CreatedAt = time.Now().UTC()
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
	// Same order as Postgres: newest first, id breaking a created_at tie.
	newestFirst := func(a, b Run) int { return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID)) }
	var out []Run
	for _, r := range m.runs {
		if f.After != nil && newestFirst(r, Run{CreatedAt: f.After.CreatedAt, ID: f.After.ID}) <= 0 {
			continue
		}
		if (f.Entity == "" || r.Entity == f.Entity) && (f.Status == "" || r.Report.Status == f.Status) && (f.ProjectID == "" || r.ProjectID == f.ProjectID) && (f.BindingID == "" || r.BindingID == f.BindingID) {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, newestFirst)
	return out[:min(len(out), clampLimit(f.Limit))], nil
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
				if s.Entity != spec.Entity || s.Version != spec.Version || s.Checksum != spec.Checksum || s.ProjectID != spec.ProjectID {
					return MappingSpec{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecKeyReuse, "Idempotency-Key was already used with a different request")
				}
				return s, false, nil
			}
		}
	}
	for _, s := range m.specs {
		if s.Entity == spec.Entity && s.Version == spec.Version && s.ProjectID == spec.ProjectID {
			if s.Checksum != spec.Checksum {
				return MappingSpec{}, false, versionUsed()
			}
			return s, false, nil
		}
	}
	spec.ID, spec.CreatedAt = ids.New(), time.Now().UTC()
	spec.NaturalKey = keyOrEmpty(spec.NaturalKey)
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

func (m *Mem) ListMappingSpecs(ctx context.Context, entity string, after *SpecCursor, limit int) ([]MappingSpec, error) {
	return m.ListMappingSpecsIn(ctx, SpecScope{}, entity, after, limit)
}

func (m *Mem) ListMappingSpecsIn(_ context.Context, scope SpecScope, entity string, after *SpecCursor, limit int) ([]MappingSpec, error) {
	projectID := scope.ProjectID
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MappingSpec
	for _, s := range m.specs {
		if (projectID == "" || s.ProjectID == projectID) && (!scope.TenantWide || s.ProjectID == "") && (entity == "" || s.Entity == entity) && (after == nil || s.Entity > after.Entity || (s.Entity == after.Entity && (s.Version < after.Version || (s.Version == after.Version && after.ID != "" && s.ID > after.ID)))) {
			out = append(out, s)
		}
	}
	// Same order as Postgres: entity, then newest version first.
	slices.SortFunc(out, func(a, b MappingSpec) int {
		return cmp.Or(strings.Compare(a.Entity, b.Entity), b.Version-a.Version, strings.Compare(a.ID, b.ID))
	})
	return out[:min(len(out), clampLimit(limit))], nil
}
