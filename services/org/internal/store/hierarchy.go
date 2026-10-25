package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

const (
	CodeHierarchyTypeNotFound     = "ORG-HIER-TYPE-NOT-FOUND"
	CodeHierarchyTypeCode         = "ORG-HIER-TYPE-CODE-DUPLICATE"
	CodeHierarchyVersionNotFound  = "ORG-HIER-VERSION-NOT-FOUND"
	CodeHierarchyDraftExists      = "ORG-HIER-DRAFT-EXISTS"
	CodeHierarchyVersionImmutable = "ORG-HIER-VERSION-IMMUTABLE"
	CodeHierarchyNodeUnknown      = "ORG-HIER-NODE-UNKNOWN"
	hierarchyTypeColumns          = "id, code, name, purposes, backdate_allowed, status, version, created_at, updated_at"
	hierarchyVersionColumns       = "id, hierarchy_type_id, version_no, status, effective_from, effective_to, node_count, copied_from_version_id, version, created_at, updated_at"
	hierarchyOneDraftIndex        = "hierarchy_version_one_draft"
	kindLegalEntity               = "legal_entity"
	kindOperatingUnit             = "operating_unit"
)

type HierarchyType struct {
	ID              string    `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Purposes        []string  `json:"purposes"`
	BackdateAllowed bool      `json:"backdateAllowed"`
	Status          string    `json:"status"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type HierarchyNode struct {
	OrgUnitID       string  `json:"orgUnitId"`
	Kind            string  `json:"kind"`
	ParentOrgUnitID *string `json:"parentOrgUnitId"`
	SortOrder       int     `json:"sortOrder"`
}

type HierarchyVersion struct {
	ID                  string          `json:"id"`
	HierarchyTypeID     string          `json:"hierarchyTypeId"`
	VersionNo           int             `json:"versionNo"`
	Status              string          `json:"status"`
	EffectiveFrom       *string         `json:"effectiveFrom"`
	EffectiveTo         *string         `json:"effectiveTo"`
	NodeCount           int             `json:"nodeCount"`
	CopiedFromVersionID *string         `json:"copiedFromVersionId"`
	Version             int             `json:"version"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
	Nodes               []HierarchyNode `json:"nodes"`
}

func hierarchyTypeNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeHierarchyTypeNotFound, "hierarchy type not found")
}

func hierarchyVersionNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeHierarchyVersionNotFound, "hierarchy version not found")
}

func idempotencyReuse() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

func scanHierarchyType(row pgx.Row, extra ...any) (HierarchyType, error) {
	var t HierarchyType
	err := row.Scan(append([]any{&t.ID, &t.Code, &t.Name, &t.Purposes, &t.BackdateAllowed, &t.Status, &t.Version, &t.CreatedAt, &t.UpdatedAt}, extra...)...)
	return t, err
}

func (s *Postgres) CreateHierarchyType(ctx context.Context, in HierarchyType, idempotencyKey string) (HierarchyType, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var t HierarchyType
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanHierarchyType(tx.QueryRow(ctx, `select `+hierarchyTypeColumns+` from hierarchy_type where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				t = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var inserted bool
		var err error
		t, err = scanHierarchyType(tx.QueryRow(ctx, `insert into hierarchy_type (tenant_id, id, code, name, purposes, backdate_allowed, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, idempotency_key) do update set version = hierarchy_type.version
			returning `+hierarchyTypeColumns+`, (xmax = 0)`, p.TenantID, ids.New(), in.Code, in.Name, in.Purposes, in.BackdateAllowed, key), &inserted)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return HierarchyType{}, httpx.NewProblem(http.StatusConflict, CodeHierarchyTypeCode, "a hierarchy type with this code already exists")
	}
	if err == nil && key != nil && (t.Code != in.Code || t.Name != in.Name || t.BackdateAllowed != in.BackdateAllowed || !slices.Equal(t.Purposes, in.Purposes)) {
		return HierarchyType{}, idempotencyReuse()
	}
	return t, err
}

func loadHierarchyVersion(ctx context.Context, tx pgx.Tx, tenantID, id string, lock string) (HierarchyVersion, error) {
	var v HierarchyVersion
	var from, to *time.Time
	err := tx.QueryRow(ctx, `select `+hierarchyVersionColumns+` from hierarchy_version where tenant_id = $1 and id = $2`+lock, tenantID, id).
		Scan(&v.ID, &v.HierarchyTypeID, &v.VersionNo, &v.Status, &from, &to, &v.NodeCount, &v.CopiedFromVersionID, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, hierarchyVersionNotFound()
	}
	if err != nil {
		return v, err
	}
	v.EffectiveFrom, v.EffectiveTo = formatDate(from), formatDate(to)
	rows, err := tx.Query(ctx, `select n.org_unit_kind, n.org_unit_id, p.org_unit_id, n.sort_order
		from hierarchy_node n
		left join hierarchy_node p on p.tenant_id = n.tenant_id and p.version_id = n.version_id and p.id = n.parent_node_id
		where n.tenant_id = $1 and n.version_id = $2 order by n.sort_order, n.org_unit_id`, tenantID, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	v.Nodes = []HierarchyNode{}
	for rows.Next() {
		var n HierarchyNode
		if err := rows.Scan(&n.Kind, &n.OrgUnitID, &n.ParentOrgUnitID, &n.SortOrder); err != nil {
			return v, err
		}
		v.Nodes = append(v.Nodes, n)
	}
	return v, rows.Err()
}

func formatDate(d *time.Time) *string {
	if d == nil {
		return nil
	}
	formatted := d.Format(fiscalDateLayout)
	return &formatted
}

func writeHierarchyNodes(ctx context.Context, tx pgx.Tx, tenantID, versionID string, nodes []HierarchyNode) error {
	if len(nodes) == 0 {
		return nil
	}
	nodeIDs := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nodeIDs[n.OrgUnitID] = ids.New()
	}
	idList, kinds, units, sorts := make([]string, len(nodes)), make([]string, len(nodes)), make([]string, len(nodes)), make([]int32, len(nodes))
	parents := make([]*string, len(nodes))
	for i, n := range nodes {
		idList[i], kinds[i], units[i], sorts[i] = nodeIDs[n.OrgUnitID], n.Kind, n.OrgUnitID, int32(n.SortOrder)
		if n.ParentOrgUnitID != nil {
			parent := nodeIDs[*n.ParentOrgUnitID]
			parents[i] = &parent
		}
	}
	_, err := tx.Exec(ctx, `insert into hierarchy_node (tenant_id, version_id, id, org_unit_kind, org_unit_id, parent_node_id, sort_order)
		select $1::uuid, $2::uuid, n.id::uuid, n.kind, n.unit::uuid, n.parent::uuid, n.sort_order
		from unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::int[]) as n(id, kind, unit, parent, sort_order)`,
		tenantID, versionID, idList, kinds, units, parents, sorts)
	return err
}

func (s *Postgres) CreateHierarchyDraft(ctx context.Context, typeID, idempotencyKey string) (HierarchyVersion, error) {
	p, _ := tenant.FromContext(ctx)
	var v HierarchyVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var locked string
		err := tx.QueryRow(ctx, `select id from hierarchy_type where tenant_id = $1 and id = $2 for update`, p.TenantID, typeID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return hierarchyTypeNotFound()
		}
		if err != nil {
			return err
		}
		if idempotencyKey != "" {
			var existingID, existingType string
			err := tx.QueryRow(ctx, `select id, hierarchy_type_id from hierarchy_version where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey).Scan(&existingID, &existingType)
			if err == nil {
				if existingType != typeID {
					return idempotencyReuse()
				}
				v, err = loadHierarchyVersion(ctx, tx, p.TenantID, existingID, "")
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var source *string
		err = tx.QueryRow(ctx, `select id from hierarchy_version where tenant_id = $1 and hierarchy_type_id = $2 and status = 'published' order by effective_from desc limit 1`, p.TenantID, typeID).Scan(&source)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var versionNo int
		if err := tx.QueryRow(ctx, `select coalesce(max(version_no), 0) + 1 from hierarchy_version where tenant_id = $1 and hierarchy_type_id = $2`, p.TenantID, typeID).Scan(&versionNo); err != nil {
			return err
		}
		var key *string
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		versionID := ids.New()
		if _, err := tx.Exec(ctx, `insert into hierarchy_version (tenant_id, id, hierarchy_type_id, version_no, copied_from_version_id, idempotency_key) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, versionID, typeID, versionNo, source, key); err != nil {
			return err
		}
		var copied []HierarchyNode
		if source != nil {
			published, err := loadHierarchyVersion(ctx, tx, p.TenantID, *source, "")
			if err != nil {
				return err
			}
			copied = published.Nodes
		}
		if err := writeHierarchyNodes(ctx, tx, p.TenantID, versionID, copied); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update hierarchy_version set node_count = $3 where tenant_id = $1 and id = $2`, p.TenantID, versionID, len(copied)); err != nil {
			return err
		}
		v, err = loadHierarchyVersion(ctx, tx, p.TenantID, versionID, "")
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == hierarchyOneDraftIndex {
		return HierarchyVersion{}, httpx.NewProblem(http.StatusConflict, CodeHierarchyDraftExists, "the hierarchy type already has an open draft version")
	}
	return v, err
}

func resolveOrgUnitKinds(ctx context.Context, tx pgx.Tx, tenantID string, nodes []HierarchyNode) error {
	unitIDs := make([]string, len(nodes))
	for i, n := range nodes {
		unitIDs[i] = n.OrgUnitID
	}
	kinds := make(map[string]string, len(nodes))
	for table, kind := range map[string]string{"legal_entity": kindLegalEntity, "operating_unit": kindOperatingUnit} {
		rows, err := tx.Query(ctx, `select id from `+table+` where tenant_id = $1 and id = any($2::text[]::uuid[])`, tenantID, unitIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			kinds[id] = kind
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for i, n := range nodes {
		kind, ok := kinds[n.OrgUnitID]
		if !ok {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeHierarchyNodeUnknown, fmt.Sprintf("org unit %s is neither a legal entity nor an operating unit", n.OrgUnitID))
		}
		nodes[i].Kind = kind
	}
	return nil
}

func (s *Postgres) ReplaceHierarchyNodes(ctx context.Context, id string, version int, nodes []HierarchyNode) (HierarchyVersion, error) {
	p, _ := tenant.FromContext(ctx)
	var v HierarchyVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := loadHierarchyVersion(ctx, tx, p.TenantID, id, " for update")
		if err != nil {
			return err
		}
		if current.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodeHierarchyVersionImmutable, "only a draft hierarchy version can be edited")
		}
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		pairs := make([]domain.Node, len(nodes))
		for i, n := range nodes {
			pairs[i] = domain.Node{ID: n.OrgUnitID}
			if n.ParentOrgUnitID != nil {
				pairs[i].ParentID = *n.ParentOrgUnitID
			}
		}
		if err := domain.ValidateTree(pairs, domain.DefaultTreeLimits); err != nil {
			return err
		}
		if err := resolveOrgUnitKinds(ctx, tx, p.TenantID, nodes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from hierarchy_node where tenant_id = $1 and version_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := writeHierarchyNodes(ctx, tx, p.TenantID, id, nodes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update hierarchy_version set node_count = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, len(nodes)); err != nil {
			return err
		}
		v, err = loadHierarchyVersion(ctx, tx, p.TenantID, id, "")
		return err
	})
	return v, err
}
