package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeHierarchyTypeNotFound     = "ORG-HIER-TYPE-NOT-FOUND"
	CodeHierarchyTypeCode         = "ORG-HIER-TYPE-CODE-DUPLICATE"
	CodeHierarchyVersionNotFound  = "ORG-HIER-VERSION-NOT-FOUND"
	CodeHierarchyDraftExists      = "ORG-HIER-DRAFT-EXISTS"
	CodeHierarchyVersionImmutable = "ORG-HIER-VERSION-IMMUTABLE"
	CodeHierarchyNodeUnknown      = "ORG-HIER-NODE-UNKNOWN"
	CodeHierarchyOverlap          = "ORG-HIER-OVERLAP"
	CodeHierarchyBackdateDuty     = "ORG-HIER-BACKDATE-DUTY"
	CodeHierarchyNoVersion        = "ORG-HIER-NO-VERSION"
	CodeHierarchyNodeAbsent       = "ORG-HIER-NODE-ABSENT"
	minPublishReason              = 10
	maxPublishReason              = 500
	hierarchyTypeColumns          = "id, code, name, purposes, backdate_allowed, status, version, created_at, updated_at"
	hierarchyVersionColumns       = "id, hierarchy_type_id, version_no, status, effective_from, effective_to, node_count, copied_from_version_id, version, created_at, updated_at, published_at, published_by, publish_reason"
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
	PublishedAt         *time.Time      `json:"publishedAt"`
	PublishedBy         *string         `json:"publishedBy"`
	PublishReason       *string         `json:"publishReason"`
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

func scanHierarchyType(row pgx.Row) (HierarchyType, error) {
	var t HierarchyType
	err := row.Scan(&t.ID, &t.Code, &t.Name, &t.Purposes, &t.BackdateAllowed, &t.Status, &t.Version, &t.CreatedAt, &t.UpdatedAt)
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
		var err error
		t, err = scanHierarchyType(tx.QueryRow(ctx, `insert into hierarchy_type (tenant_id, id, code, name, purposes, backdate_allowed, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, idempotency_key) do update set version = hierarchy_type.version
			returning `+hierarchyTypeColumns, p.TenantID, ids.New(), in.Code, in.Name, in.Purposes, in.BackdateAllowed, key))
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
		Scan(&v.ID, &v.HierarchyTypeID, &v.VersionNo, &v.Status, &from, &to, &v.NodeCount, &v.CopiedFromVersionID, &v.Version, &v.CreatedAt, &v.UpdatedAt, &v.PublishedAt, &v.PublishedBy, &v.PublishReason)
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

type AsOfRelative struct {
	OrgUnitID string `json:"orgUnitId"`
	Depth     int    `json:"depth"`
}

type HierarchyAsOf struct {
	HierarchyTypeID string         `json:"hierarchyTypeId"`
	Purposes        []string       `json:"purposes"`
	VersionID       string         `json:"versionId"`
	VersionNo       int            `json:"versionNo"`
	EffectiveFrom   string         `json:"effectiveFrom"`
	EffectiveTo     *string        `json:"effectiveTo"`
	Date            string         `json:"date"`
	OrgUnitID       string         `json:"orgUnitId"`
	Kind            string         `json:"kind"`
	ParentOrgUnitID *string        `json:"parentOrgUnitId"`
	Ancestors       []AsOfRelative `json:"ancestors"`
	Descendants     []AsOfRelative `json:"descendants"`
}

func (s *Postgres) PublishHierarchyVersion(ctx context.Context, id string, version int, effectiveFrom time.Time, reason, idempotencyKey, publishedBy string, backdated bool) (HierarchyVersion, error) {
	p, _ := tenant.FromContext(ctx)
	reason = strings.TrimSpace(reason)
	var v HierarchyVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var typeID string
		err := tx.QueryRow(ctx, `select hierarchy_type_id from hierarchy_version where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&typeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return hierarchyVersionNotFound()
		}
		if err != nil {
			return err
		}
		var backdateAllowed bool
		var purposes []string
		if err := tx.QueryRow(ctx, `select backdate_allowed, purposes from hierarchy_type where tenant_id = $1 and id = $2 for update`, p.TenantID, typeID).Scan(&backdateAllowed, &purposes); err != nil {
			return err
		}
		if idempotencyKey != "" {
			var existingID string
			var existingFrom *time.Time
			err := tx.QueryRow(ctx, `select id, effective_from from hierarchy_version where tenant_id = $1 and publish_idempotency_key = $2`, p.TenantID, idempotencyKey).Scan(&existingID, &existingFrom)
			if err == nil {
				if existingID != id || existingFrom == nil || !existingFrom.Equal(effectiveFrom) {
					return idempotencyReuse()
				}
				v, err = loadHierarchyVersion(ctx, tx, p.TenantID, id, "")
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		current, err := loadHierarchyVersion(ctx, tx, p.TenantID, id, " for update")
		if err != nil {
			return err
		}
		if current.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodeHierarchyVersionImmutable, "only a draft hierarchy version can be published")
		}
		if current.Version != version {
			return httpx.NewProblem(http.StatusConflict, CodeVersionConflict, fmt.Sprintf("current version is %d", current.Version))
		}
		if backdated {
			if length := utf8.RuneCountInString(reason); !backdateAllowed || length < minPublishReason || length > maxPublishReason {
				return httpx.NewProblem(http.StatusForbidden, CodeHierarchyBackdateDuty, "Backdated publish requires the Backdate duty and a reason.")
			}
		}
		pairs := make([]domain.Node, len(current.Nodes))
		for i, n := range current.Nodes {
			pairs[i] = domain.Node{ID: n.OrgUnitID}
			if n.ParentOrgUnitID != nil {
				pairs[i].ParentID = *n.ParentOrgUnitID
			}
		}
		if err := domain.ValidateTree(pairs, domain.DefaultTreeLimits); err != nil {
			return err
		}
		var predecessorID string
		var predecessorNo int
		var predecessorFrom time.Time
		var predecessorTo *time.Time
		err = tx.QueryRow(ctx, `select id, version_no, effective_from, effective_to from hierarchy_version
			where tenant_id = $1 and hierarchy_type_id = $2 and status <> 'draft' order by effective_from desc limit 1`, p.TenantID, typeID).
			Scan(&predecessorID, &predecessorNo, &predecessorFrom, &predecessorTo)
		hasPredecessor := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if hasPredecessor && !effectiveFrom.After(predecessorFrom) {
			return httpx.NewProblem(http.StatusConflict, CodeHierarchyOverlap, fmt.Sprintf("Effective range overlaps version %d.", predecessorNo))
		}
		var superseded *string
		if hasPredecessor && predecessorTo == nil {
			if _, err := tx.Exec(ctx, `update hierarchy_version set effective_to = $3, status = 'superseded', version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
				p.TenantID, predecessorID, effectiveFrom); err != nil {
				return err
			}
			superseded = &predecessorID
		}
		var storedReason, key *string
		if reason != "" {
			storedReason = &reason
		}
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		if _, err := tx.Exec(ctx, `update hierarchy_version set status = 'published', effective_from = $3, effective_to = null, published_at = now(), published_by = $4, publish_reason = $5,
			publish_idempotency_key = $6, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, effectiveFrom, publishedBy, storedReason, key); err != nil {
			return err
		}
		if err := writeHierarchyClosure(ctx, tx, p.TenantID, id, domain.Closure(pairs)); err != nil {
			return err
		}
		var backdateReason *string
		if backdated {
			backdateReason = storedReason
		}
		if err := enqueue(ctx, tx, "hierarchyversion", id, orgevents.HierarchyVersionActivated, p.TenantID, orgevents.HierarchyVersionActivatedData{
			HierarchyTypeID: typeID, Purposes: purposes, VersionID: id, VersionNo: current.VersionNo, EffectiveFrom: effectiveFrom.Format(fiscalDateLayout),
			NodeCount: len(current.Nodes), SupersededVersionID: superseded, BackdateReason: backdateReason,
		}); err != nil {
			return err
		}
		v, err = loadHierarchyVersion(ctx, tx, p.TenantID, id, "")
		return err
	})
	return v, err
}

func writeHierarchyClosure(ctx context.Context, tx pgx.Tx, tenantID, versionID string, rows []domain.ClosureRow) error {
	if len(rows) == 0 {
		return nil
	}
	ancestors, descendants, depths := make([]string, len(rows)), make([]string, len(rows)), make([]int32, len(rows))
	for i, r := range rows {
		ancestors[i], descendants[i], depths[i] = r.AncestorID, r.DescendantID, int32(r.Depth)
	}
	_, err := tx.Exec(ctx, `insert into hierarchy_closure (tenant_id, version_id, ancestor_org_unit_id, descendant_org_unit_id, depth)
		select $1::uuid, $2::uuid, c.ancestor::uuid, c.descendant::uuid, c.depth
		from unnest($3::text[], $4::text[], $5::int[]) as c(ancestor, descendant, depth)`, tenantID, versionID, ancestors, descendants, depths)
	return err
}

func (s *Postgres) HierarchyAsOf(ctx context.Context, typeID string, date time.Time, orgUnitID string) (HierarchyAsOf, error) {
	p, _ := tenant.FromContext(ctx)
	out := HierarchyAsOf{HierarchyTypeID: typeID, Date: date.Format(fiscalDateLayout), OrgUnitID: orgUnitID, Ancestors: []AsOfRelative{}, Descendants: []AsOfRelative{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var versionID *string
		var versionNo *int
		var from, to *time.Time
		err := tx.QueryRow(ctx, `select t.purposes, v.id, v.version_no, v.effective_from, v.effective_to
			from hierarchy_type t
			left join hierarchy_version v on v.tenant_id = t.tenant_id and v.hierarchy_type_id = t.id and v.status <> 'draft'
				and v.effective_from <= $3 and (v.effective_to is null or $3 < v.effective_to)
			where t.tenant_id = $1 and t.id = $2`, p.TenantID, typeID, date).Scan(&out.Purposes, &versionID, &versionNo, &from, &to)
		if errors.Is(err, pgx.ErrNoRows) {
			return hierarchyTypeNotFound()
		}
		if err != nil {
			return err
		}
		if versionID == nil {
			return httpx.NewProblem(http.StatusNotFound, CodeHierarchyNoVersion, "no hierarchy version is in force on "+out.Date)
		}
		out.VersionID, out.VersionNo, out.EffectiveFrom, out.EffectiveTo = *versionID, *versionNo, *formatDate(from), formatDate(to)
		rows, err := tx.Query(ctx, `select n.org_unit_kind, p.org_unit_id, c.ancestor_org_unit_id, c.depth
			from hierarchy_node n
			left join hierarchy_node p on p.tenant_id = n.tenant_id and p.version_id = n.version_id and p.id = n.parent_node_id
			left join hierarchy_closure c on c.tenant_id = n.tenant_id and c.version_id = n.version_id and c.descendant_org_unit_id = n.org_unit_id
			where n.tenant_id = $1 and n.version_id = $2 and n.org_unit_id = $3 order by c.depth`, p.TenantID, *versionID, orgUnitID)
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var ancestor *string
			var depth *int
			if err := rows.Scan(&out.Kind, &out.ParentOrgUnitID, &ancestor, &depth); err != nil {
				rows.Close()
				return err
			}
			found = true
			if ancestor != nil {
				out.Ancestors = append(out.Ancestors, AsOfRelative{OrgUnitID: *ancestor, Depth: *depth})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if !found {
			return httpx.NewProblem(http.StatusNotFound, CodeHierarchyNodeAbsent, "org unit "+orgUnitID+" is not in the hierarchy version in force on "+out.Date)
		}
		rows, err = tx.Query(ctx, `select descendant_org_unit_id, depth from hierarchy_closure
			where tenant_id = $1 and version_id = $2 and ancestor_org_unit_id = $3 order by depth, descendant_org_unit_id`, p.TenantID, *versionID, orgUnitID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r AsOfRelative
			if err := rows.Scan(&r.OrgUnitID, &r.Depth); err != nil {
				return err
			}
			out.Descendants = append(out.Descendants, r)
		}
		return rows.Err()
	})
	return out, err
}
