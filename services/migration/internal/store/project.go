package store

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

const (
	CodeProjectNotFound = "AUTH-0404"
	CodeProjectName     = "MIG-PROJECT-NAME-EXISTS"
	CodeVersionConflict = "MIG-VERSION-CONFLICT"
	CodeProjectArchived = "MIG-PROJECT-ARCHIVED"
	CodeRunActive       = "MIG-RUN-ACTIVE"

	// activeRunStale is how long a project claim holds before it counts as abandoned: the 35-minute run deadline
	// plus a margin.
	activeRunStale = 40 * time.Minute
)

// Project is a migration project row (EN-064).
type Project struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Type                string     `json:"projectType"`
	SourceSystem        string     `json:"sourceSystem,omitempty"`
	SourceLegalEntityID string     `json:"sourceLegalEntityId,omitempty"`
	TargetLegalEntityID string     `json:"targetLegalEntityId,omitempty"`
	ConnectionProfileID string     `json:"connectionProfileId,omitempty"`
	Status              string     `json:"status"`
	CutoverAt           *time.Time `json:"cutoverAt,omitempty"`
	ScheduleCron        string     `json:"scheduleCron,omitempty"`
	Version             int        `json:"version"`
	CreatedBy           string     `json:"createdBy"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

// ProjectCursor is the keyset position after the last project of a page (name order).
type ProjectCursor struct {
	Name string `json:"n"`
	ID   string `json:"i"`
}

type ProjectFilter struct {
	Type   string
	Status string
	After  *ProjectCursor
	Limit  int
}

func projectNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeProjectNotFound, "migration project not found")
}

func projectNameUsed() error {
	return httpx.NewProblem(http.StatusConflict, CodeProjectName, "a migration project with this name already exists")
}

// ProjectArchived refuses a change to an archived project other than reactivating it.
func ProjectArchived() error {
	return httpx.NewProblem(http.StatusConflict, CodeProjectArchived, "the project is archived; reactivate it first")
}

func versionConflict(current int) error {
	return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, "current version is "+strconv.Itoa(current))
}

func sameProject(a, b Project) bool {
	return a.Name == b.Name && a.Type == b.Type && a.SourceSystem == b.SourceSystem && a.SourceLegalEntityID == b.SourceLegalEntityID &&
		a.TargetLegalEntityID == b.TargetLegalEntityID && a.ConnectionProfileID == b.ConnectionProfileID && a.ScheduleCron == b.ScheduleCron && sameTime(a.CutoverAt, b.CutoverAt)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

const projectColumns = `id, name, project_type, coalesce(source_system, ''), coalesce(source_legal_entity_id::text, ''), coalesce(target_legal_entity_id::text, ''),
	coalesce(connection_profile_id::text, ''), status, cutover_at, coalesce(schedule_cron, ''), version, created_by, created_at, updated_at`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Type, &p.SourceSystem, &p.SourceLegalEntityID, &p.TargetLegalEntityID, &p.ConnectionProfileID, &p.Status, &p.CutoverAt, &p.ScheduleCron,
		&p.Version, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func isForeignKeyViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503" && pg.ConstraintName == constraint
}

func isUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == constraint
}

// SaveProject inserts a project; created is false when the Idempotency-Key already stored the same project. A used
// name is 409 MIG-PROJECT-NAME-EXISTS and a key reused for a different project is 422.
func (s *Postgres) SaveProject(ctx context.Context, in Project, idempotencyKey string) (Project, bool, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var saved Project
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idempotencyKey != "" {
			prior, err := scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from migration.project where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
			if err == nil {
				if !sameProject(prior, in) {
					return keyReuse()
				}
				saved = prior
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		// A concurrent request with the same key may insert first: the key conflict is skipped and its row replayed.
		var err error
		saved, err = scanProject(tx.QueryRow(ctx, `insert into migration.project
			(tenant_id, id, name, project_type, source_system, source_legal_entity_id, target_legal_entity_id, connection_profile_id, status, cutover_at, schedule_cron, created_by, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+projectColumns,
			p.TenantID, in.ID, in.Name, in.Type, nullIfEmpty(in.SourceSystem), nullIfEmpty(in.SourceLegalEntityID), nullIfEmpty(in.TargetLegalEntityID),
			nullIfEmpty(in.ConnectionProfileID), in.Status, in.CutoverAt, nullIfEmpty(in.ScheduleCron), p.UserID, nullIfEmpty(idempotencyKey)))
		if errors.Is(err, pgx.ErrNoRows) {
			prior, err := scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from migration.project where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
			if err != nil {
				return err
			}
			if !sameProject(prior, in) {
				return keyReuse()
			}
			saved = prior
			return nil
		}
		created = err == nil
		return err
	})
	if isForeignKeyViolation(err, "project_connection_profile_fk") {
		return Project{}, false, ConnectionUnknown()
	}
	if isUniqueViolation(err, "project_tenant_id_name_key") {
		// The name can collide with a concurrent request carrying the same key that committed first: replay it.
		if idempotencyKey != "" {
			if prior, rerr := s.projectByKey(ctx, idempotencyKey); rerr == nil {
				if !sameProject(prior, in) {
					return Project{}, false, keyReuse()
				}
				return prior, false, nil
			}
		}
		return Project{}, false, projectNameUsed()
	}
	return saved, created, err
}

func (s *Postgres) projectByKey(ctx context.Context, idempotencyKey string) (Project, error) {
	p, _ := tenant.FromContext(ctx)
	var out Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from migration.project where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		return err
	})
	return out, err
}

func (s *Postgres) GetProject(ctx context.Context, id string) (Project, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return Project{}, projectNotFound()
	}
	var out Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from migration.project where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, projectNotFound()
	}
	return out, err
}

// ListProjects pages projects in (name, id) order, at most Limit rows.
func (s *Postgres) ListProjects(ctx context.Context, f ProjectFilter) ([]Project, error) {
	p, _ := tenant.FromContext(ctx)
	afterName, afterID := "", "00000000-0000-0000-0000-000000000000"
	if f.After != nil {
		afterName, afterID = f.After.Name, f.After.ID
	}
	var out []Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+projectColumns+` from migration.project
			where tenant_id = $1 and ($2 = '' or project_type = $2) and ($3 = '' or status = $3)
			and ($4::boolean is false or (name, id) > ($5, $6::uuid))
			order by name, id limit $7`, p.TenantID, f.Type, f.Status, f.After != nil, afterName, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pr, err := scanProject(rows)
			if err != nil {
				return err
			}
			out = append(out, pr)
		}
		return rows.Err()
	})
	return out, err
}

// lockProject reads a project FOR UPDATE and checks the caller's version.
func lockProject(ctx context.Context, tx pgx.Tx, tenantID, id string, version int) (Project, error) {
	cur, err := scanProject(tx.QueryRow(ctx, `select `+projectColumns+` from migration.project where tenant_id = $1 and id = $2 for update`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, projectNotFound()
	}
	if err != nil {
		return Project{}, err
	}
	if cur.Version != version {
		return Project{}, versionConflict(cur.Version)
	}
	return cur, nil
}

// UpdateProject applies edit to the project under its version; the edit may change only the mutable fields.
func (s *Postgres) UpdateProject(ctx context.Context, id string, version int, edit func(*Project) error) (Project, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(id) {
		return Project{}, projectNotFound()
	}
	var out Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockProject(ctx, tx, p.TenantID, id, version)
		if err != nil {
			return err
		}
		if err := edit(&cur); err != nil {
			return err
		}
		out, err = scanProject(tx.QueryRow(ctx, `update migration.project set name = $3, status = $4, cutover_at = $5, schedule_cron = $6, connection_profile_id = $7, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+projectColumns, p.TenantID, id, cur.Name, cur.Status, cur.CutoverAt, nullIfEmpty(cur.ScheduleCron), nullIfEmpty(cur.ConnectionProfileID)))
		return err
	})
	if isForeignKeyViolation(err, "project_connection_profile_fk") {
		return Project{}, ConnectionUnknown()
	}
	if isUniqueViolation(err, "project_tenant_id_name_key") {
		return Project{}, projectNameUsed()
	}
	return out, err
}

// ReplaceRewriteRules swaps a project's whole rule set under the project's version and bumps the version.
func (s *Postgres) ReplaceRewriteRules(ctx context.Context, projectID string, version int, rules []domain.KeyRewrite) (Project, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(projectID) {
		return Project{}, projectNotFound()
	}
	var out Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockProject(ctx, tx, p.TenantID, projectID, version)
		if err != nil {
			return err
		}
		if cur.Status == domain.ProjectArchived {
			return ProjectArchived()
		}
		if _, err := tx.Exec(ctx, `delete from migration.key_rewrite_rule where tenant_id = $1 and project_id = $2`, p.TenantID, projectID); err != nil {
			return err
		}
		for i, r := range rules {
			if _, err := tx.Exec(ctx, `insert into migration.key_rewrite_rule (tenant_id, id, project_id, field, from_value, to_value, position)
				values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, ids.New(), projectID, r.Field, r.From, r.To, i); err != nil {
				return err
			}
		}
		out, err = scanProject(tx.QueryRow(ctx, `update migration.project set version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+projectColumns, p.TenantID, projectID))
		return err
	})
	return out, err
}

// BeginProjectRun claims the project for the run that will be saved as runID (BR-10.2). The claim is one
// conditional update, so of two concurrent starts exactly one passes: the second waits on the row lock and then
// re-checks the condition against the committed claim. Staleness is judged on the database clock, never the
// caller's. When the update matches nothing, a second read says why: unknown project, archived, or another run's
// claim (whose run id the 409 names).
func (s *Postgres) BeginProjectRun(ctx context.Context, projectID, runID string) (Project, error) {
	p, _ := tenant.FromContext(ctx)
	if !ids.Valid(projectID) {
		return Project{}, projectNotFound()
	}
	var out Project
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanProject(tx.QueryRow(ctx, `update migration.project set active_run_id = $3, active_run_started_at = now()
			where tenant_id = $1 and id = $2 and status = 'active'
			and (active_run_id is null or active_run_started_at < now() - make_interval(secs => $4))
			returning `+projectColumns, p.TenantID, projectID, runID, activeRunStale.Seconds()))
		if err == nil {
			out = cur
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var status string
		var active *string
		err = tx.QueryRow(ctx, `select status, active_run_id::text from migration.project where tenant_id = $1 and id = $2`, p.TenantID, projectID).Scan(&status, &active)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return projectNotFound()
		case err != nil:
			return err
		case status == domain.ProjectArchived:
			return ProjectArchived()
		case active != nil:
			return runActive(*active)
		}
		return errors.New("migration project claim matched no row for an unknown reason")
	})
	return out, err
}

// EndProjectRun releases the claim runID holds. It clears nothing when the claim has gone stale and another run has
// taken it since, and releasing a project that holds no claim is not an error.
func (s *Postgres) EndProjectRun(ctx context.Context, projectID, runID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update migration.project set active_run_id = null, active_run_started_at = null
			where tenant_id = $1 and id = $2 and active_run_id = $3`, p.TenantID, projectID, runID)
		return err
	})
}

func runActive(runID string) error {
	return httpx.NewProblem(http.StatusConflict, CodeRunActive, "Run "+runID+" is already active for this project; wait for it to finish")
}

type memClaim struct {
	runID string
	at    time.Time
}

type memProjects struct {
	mu     sync.Mutex
	items  []Project
	byKey  map[string]string
	active map[string]memClaim
}

func (m *Mem) BeginProjectRun(_ context.Context, projectID, runID string) (Project, error) {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	i := slices.IndexFunc(st.items, func(c Project) bool { return c.ID == projectID })
	if i < 0 {
		return Project{}, projectNotFound()
	}
	if st.items[i].Status == domain.ProjectArchived {
		return Project{}, ProjectArchived()
	}
	if c, ok := st.active[projectID]; ok && time.Since(c.at) < activeRunStale {
		return Project{}, runActive(c.runID)
	}
	if st.active == nil {
		st.active = map[string]memClaim{}
	}
	st.active[projectID] = memClaim{runID: runID, at: time.Now()}
	return st.items[i], nil
}

func (m *Mem) EndProjectRun(_ context.Context, projectID, runID string) error {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.active[projectID].runID == runID {
		delete(st.active, projectID)
	}
	return nil
}

func (m *Mem) pj() *memProjects {
	m.pjOnce.Do(func() { m.pjState = &memProjects{byKey: map[string]string{}} })
	return m.pjState
}

func (m *Mem) SaveProject(ctx context.Context, in Project, idempotencyKey string) (Project, bool, error) {
	if m.SaveErr != nil {
		return Project{}, false, m.SaveErr
	}
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	if id, ok := st.byKey[idempotencyKey]; ok && idempotencyKey != "" {
		for _, c := range st.items {
			if c.ID == id {
				if !sameProject(c, in) {
					return Project{}, false, keyReuse()
				}
				return c, false, nil
			}
		}
	}
	for _, c := range st.items {
		if c.Name == in.Name {
			return Project{}, false, projectNameUsed()
		}
	}
	now := time.Now().UTC()
	in.ID, in.Version, in.CreatedAt, in.UpdatedAt = ids.New(), 1, now, now
	if p, ok := tenant.FromContext(ctx); ok {
		in.CreatedBy = p.UserID
	}
	st.items = append(st.items, in)
	if idempotencyKey != "" {
		st.byKey[idempotencyKey] = in.ID
	}
	return in, true, nil
}

func (m *Mem) GetProject(_ context.Context, id string) (Project, error) {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.items {
		if c.ID == id {
			return c, nil
		}
	}
	return Project{}, projectNotFound()
}

func (m *Mem) ListProjects(_ context.Context, f ProjectFilter) ([]Project, error) {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []Project
	for _, c := range st.items {
		if (f.Type == "" || c.Type == f.Type) && (f.Status == "" || c.Status == f.Status) &&
			(f.After == nil || cmp.Or(strings.Compare(c.Name, f.After.Name), strings.Compare(c.ID, f.After.ID)) > 0) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Project) int { return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID)) })
	return out[:min(len(out), clampLimit(f.Limit))], nil
}

func (m *Mem) UpdateProject(_ context.Context, id string, version int, edit func(*Project) error) (Project, error) {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.items {
		if st.items[i].ID != id {
			continue
		}
		if st.items[i].Version != version {
			return Project{}, versionConflict(st.items[i].Version)
		}
		cur := st.items[i]
		if err := edit(&cur); err != nil {
			return Project{}, err
		}
		for j := range st.items {
			if j != i && st.items[j].Name == cur.Name {
				return Project{}, projectNameUsed()
			}
		}
		cur.Version++
		cur.UpdatedAt = time.Now().UTC()
		st.items[i] = cur
		return cur, nil
	}
	return Project{}, projectNotFound()
}

func (m *Mem) ReplaceRewriteRules(_ context.Context, projectID string, version int, rules []domain.KeyRewrite) (Project, error) {
	st := m.pj()
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.items {
		if st.items[i].ID != projectID {
			continue
		}
		if st.items[i].Version != version {
			return Project{}, versionConflict(st.items[i].Version)
		}
		if st.items[i].Status == domain.ProjectArchived {
			return Project{}, ProjectArchived()
		}
		pk := m.pk()
		pk.mu.Lock()
		pk.rules[projectID] = append([]domain.KeyRewrite{}, rules...)
		pk.mu.Unlock()
		st.items[i].Version++
		st.items[i].UpdatedAt = time.Now().UTC()
		return st.items[i], nil
	}
	return Project{}, projectNotFound()
}
