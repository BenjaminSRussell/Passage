package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const (
	CodeLookupNotFound    = "MIG-LOOKUP-NOT-FOUND"
	CodeLookupVersionGone = "MIG-LOOKUP-VERSION-NOT-FOUND"
	CodeLookupNameExists  = "MIG-LOOKUP-NAME-EXISTS"
	CodeLookupArchived    = "MIG-LOOKUP-ARCHIVED"
	lookupNameUniqueIndex = "lookup_table_tenant_id_name_key"
	lookupColumns         = `t.id, t.name, t.kind, t.status, t.version, t.entry_count, t.created_by, t.created_at, t.updated_at`
	lookupFrom            = ` from migration.lookup_table t `
)

// LookupTable is a lookup table as the API shows it: the header, and the definition of one version (the current one
// unless a version was asked for; lists leave it out).
type LookupTable struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"`
	Version    int             `json:"version"`
	EntryCount int             `json:"entryCount"`
	Checksum   string          `json:"checksum,omitempty"`
	Definition json.RawMessage `json:"definition,omitempty"`
	CreatedBy  string          `json:"createdBy"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

// LookupVersion is one row of a table's history.
type LookupVersion struct {
	Version    int       `json:"version"`
	EntryCount int       `json:"entryCount"`
	Checksum   string    `json:"checksum"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
}

type LookupFilter struct {
	Status string
	Kind   string
	After  *ProjectCursor // (name, id) keyset
	Limit  int
}

// LookupStore stores the lookup tables of outbound mappings. Like outbound mappings they are tenant data, read and
// written through outboundTx (a tenant administrator holding the method's duty, or the integration hub for LookupValues).
type LookupStore interface {
	SaveLookupTable(ctx context.Context, in LookupTable, idempotencyKey string) (saved LookupTable, created bool, err error)
	// GetLookupTable returns the header with the definition of version (0 is the current one).
	GetLookupTable(ctx context.Context, id string, version int) (LookupTable, error)
	ListLookupTables(ctx context.Context, f LookupFilter) ([]LookupTable, error)
	ListLookupVersions(ctx context.Context, id string) ([]LookupVersion, error)
	// UpdateLookupTable applies edit under the table's version and writes version+1 (409 MIG-VERSION-CONFLICT otherwise).
	UpdateLookupTable(ctx context.Context, id string, version int, edit func(*LookupTable) error) (LookupTable, error)
	domain.LookupResolver
}

func lookupNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeLookupNotFound, "lookup table not found")
}

func lookupVersionGone(v int) error {
	return httpx.NewProblem(http.StatusNotFound, CodeLookupVersionGone, "lookup table has no version "+strconv.Itoa(v))
}

func lookupNameUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeLookupNameExists, "a lookup table with this name already exists")
}

func scanLookup(row pgx.Row) (LookupTable, error) {
	var t LookupTable
	err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Status, &t.Version, &t.EntryCount, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func entryCount(definition []byte) int {
	var d struct {
		Entries map[string]json.RawMessage `json:"entries"`
	}
	_ = json.Unmarshal(definition, &d)
	return len(d.Entries)
}

func (s *Postgres) SaveLookupTable(ctx context.Context, in LookupTable, idempotencyKey string) (LookupTable, bool, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	in.Checksum = OutboundChecksum(in.Definition)
	n := entryCount(in.Definition)
	var saved LookupTable
	created := false
	err := s.outboundTx(ctx, obWrite, func(tx pgx.Tx) error {
		byKey := func() (LookupTable, error) {
			prior, err := scanLookup(tx.QueryRow(ctx, `select `+lookupColumns+lookupFrom+`where t.tenant_id = $1 and t.idempotency_key = $2`, p.TenantID, idempotencyKey))
			if err != nil {
				return prior, err
			}
			var checksum string
			if err := tx.QueryRow(ctx, `select checksum from migration.lookup_table_version where tenant_id = $1 and table_id = $2 and version = 1`, p.TenantID, prior.ID).Scan(&checksum); err != nil {
				return prior, err
			}
			if prior.Name != in.Name || checksum != in.Checksum {
				return prior, keyReuse()
			}
			return prior, nil
		}
		if idempotencyKey != "" {
			prior, err := byKey()
			if err == nil {
				saved = prior
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `insert into migration.lookup_table (tenant_id, id, name, kind, entry_count, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7) on conflict (tenant_id, idempotency_key) do nothing`,
			p.TenantID, in.ID, in.Name, in.Kind, n, p.UserID, nullIfEmpty(idempotencyKey))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 { // a concurrent call with the same key won
			prior, err := byKey()
			saved = prior
			return err
		}
		if _, err := tx.Exec(ctx, `insert into migration.lookup_table_version (tenant_id, table_id, version, definition, checksum, created_by)
			values ($1, $2, 1, $3, $4, $5)`, p.TenantID, in.ID, in.Definition, in.Checksum, p.UserID); err != nil {
			return err
		}
		created = true
		saved, err = scanLookup(tx.QueryRow(ctx, `select `+lookupColumns+lookupFrom+`where t.tenant_id = $1 and t.id = $2`, p.TenantID, in.ID))
		return err
	})
	if isUniqueViolation(err, lookupNameUniqueIndex) {
		return LookupTable{}, false, lookupNameUsed()
	}
	if err != nil {
		return LookupTable{}, false, err
	}
	if created {
		saved.Definition, saved.Checksum = in.Definition, in.Checksum
	}
	return saved, created, nil
}

func (s *Postgres) GetLookupTable(ctx context.Context, id string, version int) (LookupTable, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return LookupTable{}, lookupNotFound()
	}
	var out LookupTable
	err := s.outboundTx(ctx, obRead, func(tx pgx.Tx) error {
		var err error
		out, err = scanLookup(tx.QueryRow(ctx, `select `+lookupColumns+lookupFrom+`where t.tenant_id = $1 and t.id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return lookupNotFound()
		}
		if err != nil {
			return err
		}
		want := version
		if want == 0 {
			want = out.Version
		}
		err = tx.QueryRow(ctx, `select definition, checksum from migration.lookup_table_version where tenant_id = $1 and table_id = $2 and version = $3`,
			p.TenantID, id, want).Scan(&out.Definition, &out.Checksum)
		if errors.Is(err, pgx.ErrNoRows) {
			return lookupVersionGone(want)
		}
		if err != nil {
			return err
		}
		out.Version = want
		out.EntryCount = entryCount(out.Definition)
		return nil
	})
	if err != nil {
		return LookupTable{}, err
	}
	return out, nil
}

func (s *Postgres) ListLookupTables(ctx context.Context, f LookupFilter) ([]LookupTable, error) {
	p, _ := tenant.FromContext(ctx)
	afterName, afterID := "", zeroUUID
	if f.After != nil {
		afterName, afterID = f.After.Name, f.After.ID
	}
	var out []LookupTable
	err := s.outboundTx(ctx, obRead, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+lookupColumns+lookupFrom+`
			where t.tenant_id = $1 and ($2 = '' or t.status = $2) and ($3 = '' or t.kind = $3) and ($4::boolean is false or (t.name, t.id) > ($5, $6::uuid))
			order by t.name, t.id limit $7`, p.TenantID, f.Status, f.Kind, f.After != nil, afterName, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanLookup(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) ListLookupVersions(ctx context.Context, id string) ([]LookupVersion, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return nil, lookupNotFound()
	}
	out := []LookupVersion{}
	err := s.outboundTx(ctx, obRead, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from migration.lookup_table where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return lookupNotFound()
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select version, (select count(*) from jsonb_object_keys(definition -> 'entries'))::int, checksum, created_by, created_at from migration.lookup_table_version
			where tenant_id = $1 and table_id = $2 order by version desc`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v LookupVersion
			if err := rows.Scan(&v.Version, &v.EntryCount, &v.Checksum, &v.CreatedBy, &v.CreatedAt); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) UpdateLookupTable(ctx context.Context, id string, version int, edit func(*LookupTable) error) (LookupTable, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return LookupTable{}, lookupNotFound()
	}
	var out LookupTable
	err := s.outboundTx(ctx, obWrite, func(tx pgx.Tx) error {
		cur, err := scanLookup(tx.QueryRow(ctx, `select `+lookupColumns+lookupFrom+`where t.tenant_id = $1 and t.id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return lookupNotFound()
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionConflict(cur.Version)
		}
		if err := tx.QueryRow(ctx, `select definition from migration.lookup_table_version where tenant_id = $1 and table_id = $2 and version = $3`, p.TenantID, id, cur.Version).Scan(&cur.Definition); err != nil {
			return err
		}
		if err := edit(&cur); err != nil {
			return err
		}
		next := cur.Version + 1
		checksum := OutboundChecksum(cur.Definition)
		n := entryCount(cur.Definition)
		if _, err := tx.Exec(ctx, `insert into migration.lookup_table_version (tenant_id, table_id, version, definition, checksum, created_by) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, id, next, cur.Definition, checksum, p.UserID); err != nil {
			return err
		}
		out, err = scanLookup(tx.QueryRow(ctx, `update migration.lookup_table t set name = $3, kind = $4, status = $5, version = $6, entry_count = $7, updated_at = now()
			where t.tenant_id = $1 and t.id = $2 returning `+lookupColumns, p.TenantID, id, cur.Name, cur.Kind, cur.Status, next, n))
		out.Definition, out.Checksum = cur.Definition, checksum
		return err
	})
	if isUniqueViolation(err, lookupNameUniqueIndex) {
		return LookupTable{}, lookupNameUsed()
	}
	return out, err
}

// LookupValues reads the entries of the current version of the table called table, for keys. The answer says whether
// the table exists and is archived, its kind and the version read; the entries that exist are in Values.
func (s *Postgres) LookupValues(ctx context.Context, table string, keys []string) (domain.LookupAnswer, error) {
	p, _ := tenant.FromContext(ctx)
	ans := domain.LookupAnswer{Values: map[string]string{}}
	err := s.outboundTx(ctx, obRender, func(tx pgx.Tx) error {
		var status string
		var values map[string]string
		err := tx.QueryRow(ctx, `select t.status, t.kind, t.version,
				coalesce((select jsonb_object_agg(k.key, v.definition -> 'entries' ->> k.key) from unnest($3::text[]) as k(key)
					where v.definition -> 'entries' ? k.key), '{}'::jsonb)
			from migration.lookup_table t
			join migration.lookup_table_version v on v.tenant_id = t.tenant_id and v.table_id = t.id and v.version = t.version
			where t.tenant_id = $1 and t.name = $2`, p.TenantID, table, keys).Scan(&status, &ans.Kind, &ans.Version, &values)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ans.Found, ans.Archived = true, status == OutboundArchivedStatus
		if !ans.Archived {
			ans.Values = values
		}
		return nil
	})
	return ans, err
}

// Mem is the in-memory store of the API tests.

type memLookup struct {
	mu       sync.Mutex
	items    []LookupTable
	versions map[string][]memLookupVersion
	byKey    map[string]string
}

type memLookupVersion struct {
	LookupVersion
	Definition json.RawMessage
}

func (m *Mem) lk() *memLookup {
	m.lkOnce.Do(func() { m.lkState = &memLookup{versions: map[string][]memLookupVersion{}, byKey: map[string]string{}} })
	return m.lkState
}

func (st *memLookup) find(id string) int {
	return slices.IndexFunc(st.items, func(x LookupTable) bool { return x.ID == id })
}

func (m *Mem) SaveLookupTable(_ context.Context, in LookupTable, idempotencyKey string) (LookupTable, bool, error) {
	if m.SaveErr != nil {
		return LookupTable{}, false, m.SaveErr
	}
	st := m.lk()
	st.mu.Lock()
	defer st.mu.Unlock()
	in.Checksum = OutboundChecksum(in.Definition)
	if id, ok := st.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		prior := st.items[st.find(id)]
		if prior.Name != in.Name || st.versions[id][0].Checksum != in.Checksum {
			return LookupTable{}, false, keyReuse()
		}
		return prior, false, nil
	}
	if slices.ContainsFunc(st.items, func(x LookupTable) bool { return x.Name == in.Name }) {
		return LookupTable{}, false, lookupNameUsed()
	}
	now := time.Now().UTC()
	in.ID, in.Version, in.Status, in.CreatedAt, in.UpdatedAt, in.EntryCount = ids.New(), 1, OutboundActive, now, now, entryCount(in.Definition)
	st.items = append(st.items, in)
	st.versions[in.ID] = []memLookupVersion{{LookupVersion{1, in.EntryCount, in.Checksum, in.CreatedBy, now}, in.Definition}}
	if idempotencyKey != "" {
		st.byKey[idempotencyKey] = in.ID
	}
	return in, true, nil
}

func (m *Mem) GetLookupTable(_ context.Context, id string, version int) (LookupTable, error) {
	st := m.lk()
	st.mu.Lock()
	defer st.mu.Unlock()
	i := st.find(id)
	if i < 0 {
		return LookupTable{}, lookupNotFound()
	}
	out := st.items[i]
	if version == 0 {
		version = out.Version
	}
	if version < 1 || version > len(st.versions[id]) {
		return LookupTable{}, lookupVersionGone(version)
	}
	v := st.versions[id][version-1]
	out.Version, out.Definition, out.Checksum, out.EntryCount = version, slices.Clone(v.Definition), v.Checksum, v.EntryCount
	return out, nil
}

func (m *Mem) ListLookupTables(_ context.Context, f LookupFilter) ([]LookupTable, error) {
	st := m.lk()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []LookupTable
	for _, x := range st.items {
		if (f.Status != "" && x.Status != f.Status) || (f.Kind != "" && x.Kind != f.Kind) {
			continue
		}
		if f.After != nil && (x.Name < f.After.Name || (x.Name == f.After.Name && x.ID <= f.After.ID)) {
			continue
		}
		x.Definition, x.Checksum = nil, ""
		out = append(out, x)
	}
	slices.SortFunc(out, func(a, b LookupTable) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) ListLookupVersions(_ context.Context, id string) ([]LookupVersion, error) {
	st := m.lk()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.find(id) < 0 {
		return nil, lookupNotFound()
	}
	out := []LookupVersion{}
	for _, v := range slices.Backward(st.versions[id]) {
		out = append(out, v.LookupVersion)
	}
	return out, nil
}

func (m *Mem) UpdateLookupTable(_ context.Context, id string, version int, edit func(*LookupTable) error) (LookupTable, error) {
	st := m.lk()
	st.mu.Lock()
	defer st.mu.Unlock()
	i := st.find(id)
	if i < 0 {
		return LookupTable{}, lookupNotFound()
	}
	cur := st.items[i]
	if cur.Version != version {
		return LookupTable{}, versionConflict(cur.Version)
	}
	cur.Definition = slices.Clone(st.versions[id][cur.Version-1].Definition)
	if err := edit(&cur); err != nil {
		return LookupTable{}, err
	}
	if slices.ContainsFunc(st.items, func(x LookupTable) bool { return x.ID != id && x.Name == cur.Name }) {
		return LookupTable{}, lookupNameUsed()
	}
	now := time.Now().UTC()
	cur.Version++
	cur.Checksum, cur.UpdatedAt, cur.EntryCount = OutboundChecksum(cur.Definition), now, entryCount(cur.Definition)
	st.versions[id] = append(st.versions[id], memLookupVersion{LookupVersion{cur.Version, cur.EntryCount, cur.Checksum, cur.CreatedBy, now}, cur.Definition})
	header := cur
	header.Definition, header.Checksum = nil, ""
	st.items[i] = header
	return cur, nil
}

func (m *Mem) LookupValues(_ context.Context, table string, keys []string) (domain.LookupAnswer, error) {
	st := m.lk()
	st.mu.Lock()
	defer st.mu.Unlock()
	ans := domain.LookupAnswer{Values: map[string]string{}}
	i := slices.IndexFunc(st.items, func(x LookupTable) bool { return x.Name == table })
	if i < 0 {
		return ans, nil
	}
	t := st.items[i]
	ans.Found, ans.Archived, ans.Kind, ans.Version = true, t.Status == OutboundArchivedStatus, t.Kind, t.Version
	var d domain.LookupDefinition
	if err := json.Unmarshal(st.versions[t.ID][t.Version-1].Definition, &d); err != nil || ans.Archived {
		return ans, err
	}
	for _, k := range keys {
		if v, ok := d.Entries[k]; ok {
			ans.Values[k] = v
		}
	}
	return ans, nil
}
