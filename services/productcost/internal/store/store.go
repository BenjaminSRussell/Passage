// Package store persists the costing master data: cost components, cost groups
// (FR-39.1) and costing versions (FR-39.3). Every method runs in a tenant
// transaction (dbx.WithTenantTx), so row-level security applies to every read
// and write; every create records its Idempotency-Key in idempotency_keys, and
// every change writes its event to the outbox in that same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
)

const (
	// CodeIdempotencyReuse is a key reused with a different request (422).
	CodeIdempotencyReuse = "idempotency_key_reuse"
	// MaxPageSize bounds one page of costing versions.
	MaxPageSize = 500

	uniqueViolation = "23505"
	rlsViolation    = "42501"
	eventSource     = "productcost"
)

// Component is a stored cost component.
type Component struct {
	ID string `json:"id"`
	domain.CostComponent
	CreatedAt time.Time `json:"createdAt"`
}

// Group is a stored cost group with the code of the component it maps to.
type Group struct {
	ID string `json:"id"`
	domain.CostGroup
	ComponentCode string    `json:"componentCode"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Version is a stored costing version. Version is the optimistic-concurrency
// counter a lifecycle action must name.
type Version struct {
	ID                string     `json:"id"`
	LegalEntityID     string     `json:"legalEntityId"`
	Code              string     `json:"code"`
	Type              string     `json:"type"`
	Status            string     `json:"status"`
	Fallback          string     `json:"fallback"`
	FallbackVersionID *string    `json:"fallbackVersionId,omitempty"`
	SiteIDs           []string   `json:"siteIds"`
	EffectiveFrom     *time.Time `json:"effectiveFrom,omitempty"`
	ActivatedAt       *time.Time `json:"activatedAt,omitempty"`
	Version           int        `json:"version"`
	CreatedAt         time.Time  `json:"createdAt"`
}

// Lifecycle returns the state the version lifecycle rules read.
func (v Version) Lifecycle() domain.Version {
	out := domain.Version{ID: v.ID, Type: v.Type, Status: v.Status, Fallback: v.Fallback, SiteIDs: v.SiteIDs}
	if v.FallbackVersionID != nil {
		out.FallbackVersionID = *v.FallbackVersionID
	}
	return out
}

// VersionFilter narrows a version listing; empty fields do not filter.
type VersionFilter struct {
	LegalEntityID string
	Status        string
	Type          string
}

// VersionPage is one page of versions in id (creation) order. Next is the
// cursor of the following page, empty on the last page.
type VersionPage struct {
	Items []Version `json:"items"`
	Next  string    `json:"next,omitempty"`
}

// Postgres is the costing repository.
type Postgres struct {
	pool *pgxpool.Pool
}

// New wraps a pool. The pool connects as the application role (no BYPASSRLS).
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database is reachable (readiness).
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, p tenant.Principal, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, p.TenantID, data)
	if err != nil {
		return err
	}
	evt.Actor = p.UserID
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// idempotent runs a create at most once per (tenant, Idempotency-Key). The key
// is claimed first, so a concurrent request with the same key waits on the
// primary key and then replays; the response is stored in the same transaction
// as the change. A key reused with a different request (or on another command,
// through scope) is 422. replayed reports a stored response.
func idempotent[T any](ctx context.Context, tx pgx.Tx, tenantID, key, scope string, request any, run func() (T, error)) (out T, replayed bool, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return out, false, err
	}
	sum := sha256.Sum256(append([]byte(scope+"\n"), raw...))
	hash := hex.EncodeToString(sum[:])
	tag, err := tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status) values ($1, $2, $3, 0)
		on conflict (tenant_id, key) do nothing`, tenantID, key, hash)
	if err != nil {
		return out, false, err
	}
	if tag.RowsAffected() == 0 {
		var stored string
		var body []byte
		if err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body); err != nil {
			return out, false, err
		}
		if stored != hash {
			return out, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request")
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return out, false, fmt.Errorf("replay %s: %w", key, err)
		}
		return out, true, nil
	}
	if out, err = run(); err != nil {
		return out, false, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return out, false, err
	}
	_, err = tx.Exec(ctx, `update idempotency_keys set response_status = $3, response_body = $4 where tenant_id = $1 and key = $2`,
		tenantID, key, http.StatusCreated, body)
	return out, false, err
}

// tenantTx runs fn in a tenant transaction and maps a row-level security
// refusal (a row outside the caller's legal-entity scope) to CST-0718.
func (s *Postgres) tenantTx(ctx context.Context, fn func(tx pgx.Tx, p tenant.Principal) error) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("store: no principal in context")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, p) })
	if pgCode(err) == rlsViolation {
		return httpx.NewProblem(http.StatusForbidden, domain.CodeLegalEntityScope, "the record is outside your legal-entity scope")
	}
	return err
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// lockTenantSet serialises creates of one kind per tenant, so the per-tenant
// limit is checked against every committed row (two concurrent creates cannot
// both see 19 components).
func lockTenantSet(ctx context.Context, tx pgx.Tx, tenantID, kind string) error {
	_, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "productcost."+kind+":"+tenantID)
	return err
}

// ListComponents returns the tenant's cost components ordered by code.
func (s *Postgres) ListComponents(ctx context.Context) ([]Component, error) {
	out := []Component{}
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		rows, err := tx.Query(ctx, `select id::text, code, name, cost_group_type, behaviour, created_at from cost_component
			where tenant_id = $1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Component
			if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.CostGroupType, &c.Behaviour, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// CreateComponent stores a cost component: at most domain.MaxComponents per
// tenant (CST-0790), codes unique per tenant (CST-0791, 409).
func (s *Postgres) CreateComponent(ctx context.Context, in domain.CostComponent, key string) (Component, bool, error) {
	var out Component
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, p.TenantID, key, "costcomponent.create", in, func() (Component, error) {
			c := Component{ID: ids.New(), CostComponent: in}
			if err := lockTenantSet(ctx, tx, p.TenantID, "cost_component"); err != nil {
				return c, err
			}
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from cost_component where tenant_id = $1`, p.TenantID).Scan(&n); err != nil {
				return c, err
			}
			if err := domain.CheckComponentCapacity(n); err != nil {
				return c, err
			}
			err := tx.QueryRow(ctx, `insert into cost_component (tenant_id, id, code, name, cost_group_type, behaviour)
				values ($1, $2, $3, $4, $5, $6) returning created_at`,
				p.TenantID, c.ID, c.Code, c.Name, c.CostGroupType, c.Behaviour).Scan(&c.CreatedAt)
			if pgCode(err) == uniqueViolation {
				return c, httpx.NewProblem(http.StatusConflict, domain.CodeComponentDuplicate, "a cost component with code "+c.Code+" already exists")
			}
			if err != nil {
				return c, err
			}
			return c, enqueue(ctx, tx, "costcomponent", c.ID, costevents.CostComponentCreated, p, costevents.CostComponentCreatedData{
				ID: c.ID, Code: c.Code, Name: c.Name, CostGroupType: c.CostGroupType, Behaviour: c.Behaviour,
			})
		})
		return err
	})
	return out, replayed, err
}

// ListGroups returns the tenant's cost groups ordered by code.
func (s *Postgres) ListGroups(ctx context.Context) ([]Group, error) {
	out := []Group{}
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		rows, err := tx.Query(ctx, `select g.id::text, g.code, g.name, g.type, g.behaviour, g.component_id::text, c.code, g.created_at
			from cost_group g join cost_component c on c.tenant_id = g.tenant_id and c.id = g.component_id
			where g.tenant_id = $1 order by g.code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.Code, &g.Name, &g.Type, &g.Behaviour, &g.ComponentID, &g.ComponentCode, &g.CreatedAt); err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	return out, err
}

// CreateGroup stores a cost group mapped to an existing component (CST-0794
// otherwise): at most domain.MaxCostGroups per tenant (CST-0792), codes unique
// per tenant (CST-0793, 409).
func (s *Postgres) CreateGroup(ctx context.Context, in domain.CostGroup, key string) (Group, bool, error) {
	var out Group
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, p.TenantID, key, "costgroup.create", in, func() (Group, error) {
			g := Group{ID: ids.New(), CostGroup: in}
			if err := lockTenantSet(ctx, tx, p.TenantID, "cost_group"); err != nil {
				return g, err
			}
			err := tx.QueryRow(ctx, `select code from cost_component where tenant_id = $1 and id = $2`, p.TenantID, in.ComponentID).Scan(&g.ComponentCode)
			if errors.Is(err, pgx.ErrNoRows) {
				return g, &domain.CodedError{Code: domain.CodeGroupComponent, Detail: "cost component " + in.ComponentID + " does not exist"}
			}
			if err != nil {
				return g, err
			}
			var n int
			if err := tx.QueryRow(ctx, `select count(*) from cost_group where tenant_id = $1`, p.TenantID).Scan(&n); err != nil {
				return g, err
			}
			if err := domain.CheckGroupCapacity(n); err != nil {
				return g, err
			}
			err = tx.QueryRow(ctx, `insert into cost_group (tenant_id, id, code, name, type, behaviour, component_id)
				values ($1, $2, $3, $4, $5, $6, $7) returning created_at`,
				p.TenantID, g.ID, g.Code, g.Name, g.Type, g.Behaviour, g.ComponentID).Scan(&g.CreatedAt)
			if pgCode(err) == uniqueViolation {
				return g, httpx.NewProblem(http.StatusConflict, domain.CodeGroupDuplicate, "a cost group with code "+g.Code+" already exists")
			}
			if err != nil {
				return g, err
			}
			return g, enqueue(ctx, tx, "costgroup", g.ID, costevents.CostGroupCreated, p, costevents.CostGroupCreatedData{
				ID: g.ID, Code: g.Code, Name: g.Name, Type: g.Type, Behaviour: g.Behaviour, ComponentID: g.ComponentID, ComponentCode: g.ComponentCode,
			})
		})
		return err
	})
	return out, replayed, err
}

// versionColumns must match scanVersion's argument order.
const versionColumns = `id::text, legal_entity_id::text, code, type, status, fallback_principle,
  fallback_version_id::text, site_ids::text[], effective_from, activated_at, version, created_at`

func scanVersion(row pgx.Row) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.LegalEntityID, &v.Code, &v.Type, &v.Status, &v.Fallback,
		&v.FallbackVersionID, &v.SiteIDs, &v.EffectiveFrom, &v.ActivatedAt, &v.Version, &v.CreatedAt)
	if v.SiteIDs == nil {
		v.SiteIDs = []string{}
	}
	return v, err
}

func versionNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeVersionNotFound, "costing version "+id+" not found")
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListVersions returns one page of the versions visible to the caller (its
// legal-entity scope applies through row-level security), in creation order.
func (s *Postgres) ListVersions(ctx context.Context, f VersionFilter, after string, limit int) (VersionPage, error) {
	page := VersionPage{Items: []Version{}}
	if limit < 1 || limit > MaxPageSize {
		limit = MaxPageSize
	}
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		rows, err := tx.Query(ctx, `select `+versionColumns+` from costing_version
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2) and ($3::text is null or status = $3)
			  and ($4::text is null or type = $4) and ($5::uuid is null or id > $5)
			order by id limit $6`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.Status), nullable(f.Type), nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanVersion(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, v)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.Next = page.Items[limit-1].ID
	}
	return page, err
}

// GetVersion returns one version, or CST-0717 (404).
func (s *Postgres) GetVersion(ctx context.Context, id string) (Version, error) {
	var out Version
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		v, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return versionNotFound(id)
		}
		out = v
		return err
	})
	return out, err
}

// CreateVersion stores a new Open version. A SpecifiedVersion fallback must
// name a version of the same legal entity (CST-0716); a code is unique per
// legal entity (CST-0713, 409).
func (s *Postgres) CreateVersion(ctx context.Context, spec domain.VersionSpec, key string) (Version, bool, error) {
	var out Version
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, p.TenantID, key, "version.create", spec, func() (Version, error) {
			if spec.Fallback == domain.FallbackSpecified {
				if err := checkFallbackVersion(ctx, tx, p.TenantID, spec); err != nil {
					return Version{}, err
				}
			}
			v, err := scanVersion(tx.QueryRow(ctx, `insert into costing_version
				(tenant_id, id, legal_entity_id, code, type, status, fallback_principle, fallback_version_id, site_ids, effective_from, idempotency_key)
				values ($1, $2, $3, $4, $5, $6, $7, $8::uuid, $9::text[]::uuid[], $10, $11)
				returning `+versionColumns,
				p.TenantID, ids.New(), spec.LegalEntityID, spec.Code, spec.Type, domain.VersionOpen, spec.Fallback,
				nullable(spec.FallbackVersionID), spec.SiteIDs, spec.EffectiveFrom, key))
			if pgCode(err) == uniqueViolation {
				return v, httpx.NewProblem(http.StatusConflict, domain.CodeVersionDuplicate, "a costing version with code "+spec.Code+" already exists for this legal entity")
			}
			if err != nil {
				return v, err
			}
			return v, enqueue(ctx, tx, "costingversion", v.ID, costevents.VersionCreated, p, costevents.VersionCreatedData{
				ID: v.ID, LegalEntityID: v.LegalEntityID, Code: v.Code, Type: v.Type, Status: v.Status, Fallback: v.Fallback,
				FallbackVersionID: v.FallbackVersionID, SiteIDs: v.SiteIDs, EffectiveFrom: v.EffectiveFrom,
			})
		})
		return err
	})
	return out, replayed, err
}

func checkFallbackVersion(ctx context.Context, tx pgx.Tx, tenantID string, spec domain.VersionSpec) error {
	var le string
	err := tx.QueryRow(ctx, `select legal_entity_id::text from costing_version where tenant_id = $1 and id = $2`, tenantID, spec.FallbackVersionID).Scan(&le)
	if errors.Is(err, pgx.ErrNoRows) {
		return &domain.CodedError{Code: domain.CodeFallbackVersion, Detail: "fallback version " + spec.FallbackVersionID + " does not exist"}
	}
	if err != nil {
		return err
	}
	if le != spec.LegalEntityID {
		return &domain.CodedError{Code: domain.CodeFallbackVersion, Detail: "the fallback version belongs to another legal entity"}
	}
	return nil
}

// TransitionVersion applies a lifecycle action (BR-39.2) to the version when
// its stored counter equals expected (CST-0714, 409, otherwise). The rule is
// domain.Transition; a refused move is returned as its coded error.
// Activation stamps activated_at.
func (s *Postgres) TransitionVersion(ctx context.Context, id string, expected int, action string) (Version, error) {
	var out Version
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		cur, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return versionNotFound(id)
		}
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return httpx.NewProblem(http.StatusConflict, domain.CodeVersionConflict, fmt.Sprintf("current version is %d", cur.Version))
		}
		next, err := domain.Transition(cur.Lifecycle(), action)
		if err != nil {
			return err
		}
		out, err = scanVersion(tx.QueryRow(ctx, `update costing_version
			set status = $3, version = version + 1,
			    activated_at = case when $4 then now() else activated_at end
			where tenant_id = $1 and id = $2
			returning `+versionColumns, p.TenantID, id, next.Status, action == domain.ActionActivate))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, "costingversion", out.ID, costevents.VersionTransitioned, p, costevents.VersionTransitionedData{
			ID: out.ID, LegalEntityID: out.LegalEntityID, Code: out.Code, Type: out.Type, Action: action,
			FromStatus: cur.Status, ToStatus: out.Status, Version: out.Version, ActivatedAt: out.ActivatedAt, Actor: p.UserID,
		})
	})
	return out, err
}
