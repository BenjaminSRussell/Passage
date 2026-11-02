package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Edge fleet problem codes (see README).
const (
	CodeEdgeNotFound = "SF-0150"
	// CodeEdgeConfigNotFound is a configuration version that does not exist: a 404 when a
	// connector pulls before anything is published, a 422 when a heartbeat reports a version
	// the node was never given.
	CodeEdgeConfigNotFound = "SF-0151"
	CodeEdgeInactive       = "SF-0152"
	CodeEdgeNameTaken      = "SF-0153"
	// CodeEdgeNotConnector is a configuration pull or heartbeat from an identity other than the
	// connector bound to the node (or for a node with no connector bound).
	CodeEdgeNotConnector = "SF-0154"

	edgeSource = "edgenode"
)

// EdgeNode is one iot-edge connector (FR-25.19). Status is derived from the last heartbeat
// when the node is read.
type EdgeNode struct {
	ID                   string                 `json:"id"`
	SiteID               *string                `json:"siteId"`
	Name                 string                 `json:"name"`
	Platform             string                 `json:"platform"`
	Ring                 string                 `json:"ring"`
	ConnectorSubject     *string                `json:"connectorSubject"`
	Status               string                 `json:"status"`
	ConnectorVersion     *string                `json:"connectorVersion"`
	CertSerial           *string                `json:"certSerial"`
	CertExpiresAt        *time.Time             `json:"certExpiresAt"`
	LastHeartbeatAt      *time.Time             `json:"lastHeartbeatAt"`
	BufferFillPct        *decimal.Decimal       `json:"bufferFillPct"`
	Machines             []domain.MachineStatus `json:"machines"`
	AppliedConfigVersion *int                   `json:"appliedConfigVersion"`
	LatestConfigVersion  *int                   `json:"latestConfigVersion"`
	Active               bool                   `json:"active"`
	Version              int                    `json:"version"`
	CreatedBy            string                 `json:"createdBy"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
}

// EdgeNodeInput is what a create or update writes. Platform never changes after creation.
// ConnectorSubject binds the node to the identity its connector authenticates as; nil leaves
// the node unbound, so it refuses configuration pulls and heartbeats.
type EdgeNodeInput struct {
	SiteID           *string
	Name             string
	Platform         string
	Ring             string
	ConnectorSubject *string
	Active           bool
}

// EdgeConfigVersion is one immutable configuration document of an edge node. DocumentSHA256
// is the hash of the stored document, carried on the published event for the audit trail.
type EdgeConfigVersion struct {
	ID             string          `json:"id"`
	EdgeID         string          `json:"edgeId"`
	Version        int             `json:"version"`
	Document       json.RawMessage `json:"document"`
	DocumentSHA256 string          `json:"documentSha256"`
	PublishedBy    string          `json:"publishedBy"`
	PublishedAt    time.Time       `json:"publishedAt"`
	AppliedAt      *time.Time      `json:"appliedAt"`
}

// HeartbeatInput is what a connector reports every 30 s. AppliedConfigVersion is the
// version it runs with; the certificate fields are sent after an enrollment or renewal.
type HeartbeatInput struct {
	ConnectorVersion     *string
	BufferFillPct        decimal.Decimal
	Machines             []domain.MachineStatus
	AppliedConfigVersion *int
	CertSerial           *string
	CertExpiresAt        *time.Time
}

// HeartbeatResult tells the connector what to do next.
type HeartbeatResult struct {
	EdgeID                   string `json:"edgeId"`
	Ring                     string `json:"ring"`
	AppliedConfigVersion     *int   `json:"appliedConfigVersion"`
	LatestConfigVersion      *int   `json:"latestConfigVersion"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}

func edgeNotFound(status int) error {
	return httpx.NewProblem(status, CodeEdgeNotFound, "edge node not found")
}

func edgeInactive() error {
	return httpx.NewProblem(http.StatusConflict, CodeEdgeInactive, "edge node is switched off")
}

func edgeNameTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		if pgErr.ConstraintName == "edge_node_connector_subject" {
			return httpx.NewProblem(http.StatusConflict, CodeEdgeNameTaken, "this connector identity is already bound to another edge node")
		}
		return httpx.NewProblem(http.StatusConflict, CodeEdgeNameTaken, "an edge node with this name already exists")
	}
	return err
}

// edgeConnector loads the node a connector call names and checks that the caller is the
// connector bound to it (SF-0154, 403) and that the node is switched on (SF-0152, 409). The row
// is locked when lock is set.
func edgeConnector(ctx context.Context, tx pgx.Tx, tenantID, edgeID, subject string, lock bool) (applied *int, ring string, err error) {
	q := `select connector_subject, active, applied_version, ring from edge_node where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	var bound *string
	var active bool
	err = tx.QueryRow(ctx, q, tenantID, edgeID).Scan(&bound, &active, &applied, &ring)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, "", edgeNotFound(http.StatusNotFound)
	case err != nil:
		return nil, "", err
	case bound == nil || subject == "" || *bound != subject:
		return nil, "", httpx.NewProblem(http.StatusForbidden, CodeEdgeNotConnector, "the caller is not the connector bound to this edge node")
	case !active:
		return nil, "", edgeInactive()
	}
	return applied, ring, nil
}

const edgeColumns = `n.id::text, n.site_id::text, n.name, n.platform, n.ring, n.connector_subject, n.connector_version, n.cert_serial, n.cert_expires_at,
	n.last_heartbeat_at, n.buffer_fill_pct::text, n.machine_status, n.applied_version,
	(select max(c.version) from edge_config_version c where c.tenant_id = n.tenant_id and c.edge_id = n.id),
	n.active, n.version, n.created_by, n.created_at, n.updated_at`

func scanEdgeNode(row pgx.Row) (EdgeNode, error) {
	var n EdgeNode
	var fill *string
	var machines []byte
	if err := row.Scan(&n.ID, &n.SiteID, &n.Name, &n.Platform, &n.Ring, &n.ConnectorSubject, &n.ConnectorVersion, &n.CertSerial, &n.CertExpiresAt,
		&n.LastHeartbeatAt, &fill, &machines, &n.AppliedConfigVersion, &n.LatestConfigVersion,
		&n.Active, &n.Version, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return EdgeNode{}, err
	}
	if fill != nil {
		d, err := decimal.NewFromString(*fill)
		if err != nil {
			return EdgeNode{}, err
		}
		n.BufferFillPct = &d
	}
	n.Machines = []domain.MachineStatus{}
	if err := json.Unmarshal(machines, &n.Machines); err != nil {
		return EdgeNode{}, err
	}
	n.Status = domain.EdgeStatus(time.Now(), n.LastHeartbeatAt, n.Active)
	return n, nil
}

func edgeEvent(ctx context.Context, tx pgx.Tx, tenantID string, n EdgeNode, by string) error {
	return enqueue(ctx, tx, edgeSource, n.ID, sfevents.EdgeNodeSaved, tenantID, sfevents.EdgeNodeSavedData{
		EdgeID: n.ID, SiteID: n.SiteID, Name: n.Name, Platform: n.Platform, Ring: n.Ring, ConnectorSubject: n.ConnectorSubject, Active: n.Active,
		Version: n.Version, SavedBy: by})
}

// CreateEdgeNode registers a connector under the Idempotency-Key and publishes
// dev.passage.shopfloor.edgenode.saved.v1 in the same transaction. A name or connector identity
// already used is SF-0153.
func (s *Postgres) CreateEdgeNode(ctx context.Context, in EdgeNodeInput, createdBy string, idem Idem) (EdgeNode, error) {
	p, _ := tenant.FromContext(ctx)
	var out EdgeNode
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into edge_node (tenant_id, id, site_id, name, platform, ring, connector_subject, active, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, id, in.SiteID, in.Name, in.Platform, in.Ring, in.ConnectorSubject, in.Active, createdBy); err != nil {
			return edgeNameTaken(err)
		}
		var err error
		if out, err = scanEdgeNode(tx.QueryRow(ctx, `select `+edgeColumns+` from edge_node n where n.id = $1`, id)); err != nil {
			return err
		}
		if err := edgeEvent(ctx, tx, p.TenantID, out, createdBy); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// UpdateEdgeNode changes a node's name, site, ring, connector identity and status when version is the stored
// version (SF-0143, 412) and bumps the version. A heartbeat does not bump it.
func (s *Postgres) UpdateEdgeNode(ctx context.Context, id string, version int, in EdgeNodeInput, updatedBy string) (EdgeNode, error) {
	p, _ := tenant.FromContext(ctx)
	var out EdgeNode
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		err := tx.QueryRow(ctx, `select version from edge_node where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return edgeNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		if cur != version {
			return versionMismatch("edge node")
		}
		if _, err := tx.Exec(ctx, `update edge_node set site_id = $3, name = $4, ring = $5, active = $6, connector_subject = $7, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, in.SiteID, in.Name, in.Ring, in.Active, in.ConnectorSubject); err != nil {
			return edgeNameTaken(err)
		}
		if out, err = scanEdgeNode(tx.QueryRow(ctx, `select `+edgeColumns+` from edge_node n where n.id = $1`, id)); err != nil {
			return err
		}
		return edgeEvent(ctx, tx, p.TenantID, out, updatedBy)
	})
	return out, err
}

// GetEdgeNode returns an edge node, or SF-0150 (404).
func (s *Postgres) GetEdgeNode(ctx context.Context, id string) (EdgeNode, error) {
	var out EdgeNode
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanEdgeNode(tx.QueryRow(ctx, `select `+edgeColumns+` from edge_node n where n.id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return edgeNotFound(http.StatusNotFound)
		}
		return err
	})
	return out, err
}

// ListEdgeNodes returns edge nodes by name, at most limit rows. ring and status ("" for any)
// filter; the status is the derived one (pending, online, offline, inactive).
func (s *Postgres) ListEdgeNodes(ctx context.Context, ring, status string, limit int) ([]EdgeNode, error) {
	out := []EdgeNode{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+edgeColumns+` from edge_node n
			where ($1 = '' or n.ring = $1)
			  and ($2 = '' or $2 = case when not n.active then 'inactive' when n.last_heartbeat_at is null then 'pending'
			         when now() - n.last_heartbeat_at > make_interval(secs => $3) then 'offline' else 'online' end)
			order by n.name, n.id limit $4`, ring, status, domain.EdgeOnlineWindow.Seconds(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanEdgeNode(rows)
			if err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

const edgeConfigColumns = `id::text, edge_id::text, version, document, encode(sha256(convert_to(document::text, 'UTF8')), 'hex'), published_by, published_at, applied_at`

func scanEdgeConfig(row pgx.Row) (EdgeConfigVersion, error) {
	var c EdgeConfigVersion
	err := row.Scan(&c.ID, &c.EdgeID, &c.Version, &c.Document, &c.DocumentSHA256, &c.PublishedBy, &c.PublishedAt, &c.AppliedAt)
	return c, err
}

// PublishEdgeConfig stores the next configuration version of a node under the
// Idempotency-Key and publishes dev.passage.shopfloor.edgeconfig.published.v1. Versions count
// up from 1 per node; the node row is locked so two publishers get different numbers. The
// document is validated again with its final version number written into it.
func (s *Postgres) PublishEdgeConfig(ctx context.Context, edgeID string, document json.RawMessage, publishedBy string, idem Idem) (EdgeConfigVersion, error) {
	p, _ := tenant.FromContext(ctx)
	var out EdgeConfigVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var next int
		err := tx.QueryRow(ctx, `select coalesce((select max(c.version) from edge_config_version c where c.tenant_id = n.tenant_id and c.edge_id = n.id), 0) + 1
			from edge_node n where n.tenant_id = $1 and n.id = $2 for update`, p.TenantID, edgeID).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return edgeNotFound(http.StatusNotFound)
		}
		if err != nil {
			return err
		}
		doc, fields := domain.PrepareEdgeConfig(document, next)
		if len(fields) > 0 {
			return httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", "configuration document is invalid")
		}
		out, err = scanEdgeConfig(tx.QueryRow(ctx, `insert into edge_config_version (tenant_id, id, edge_id, version, document, published_by)
			values ($1, $2, $3, $4, $5, $6) returning `+edgeConfigColumns, p.TenantID, ids.New(), edgeID, next, []byte(doc), publishedBy))
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, edgeSource, edgeID, sfevents.EdgeConfigPublished, p.TenantID, sfevents.EdgeConfigPublishedData{
			EdgeID: edgeID, Version: next, DocumentSHA256: out.DocumentSHA256, WriteRuleNodes: domain.WriteRuleNodes(doc), PublishedBy: publishedBy}); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// ListEdgeConfigs returns a node's configuration versions, newest first, at most limit.
func (s *Postgres) ListEdgeConfigs(ctx context.Context, edgeID string, limit int) ([]EdgeConfigVersion, error) {
	out := []EdgeConfigVersion{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from edge_node where id = $1)`, edgeID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return edgeNotFound(http.StatusNotFound)
		}
		rows, err := tx.Query(ctx, `select `+edgeConfigColumns+` from edge_config_version where edge_id = $1 order by version desc limit $2`, edgeID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanEdgeConfig(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// CurrentEdgeConfig is what a connector pulls: the newest version published for its node.
// subject is the caller's identity, which must be the connector bound to the node (SF-0154,
// 403). Nothing published is SF-0151 (404); a switched-off node is SF-0152 (409).
func (s *Postgres) CurrentEdgeConfig(ctx context.Context, edgeID, subject string) (EdgeConfigVersion, error) {
	p, _ := tenant.FromContext(ctx)
	var out EdgeConfigVersion
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := edgeConnector(ctx, tx, p.TenantID, edgeID, subject, false); err != nil {
			return err
		}
		var err error
		out, err = scanEdgeConfig(tx.QueryRow(ctx, `select `+edgeConfigColumns+` from edge_config_version where edge_id = $1 order by version desc limit 1`, edgeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeEdgeConfigNotFound, "no configuration has been published for this edge node")
		}
		return err
	})
	return out, err
}

// Heartbeat records a connector's report: its version, buffer fill, machine states and the
// configuration version it runs. Reporting a version other than the applied one makes it the
// applied one (one at a time), stamps applied_at on that version the first time it is applied
// and publishes dev.passage.shopfloor.edgeconfig.applied.v1. A version the node was never
// given is SF-0151 (422); a caller other than the bound connector is SF-0154 (403); a
// switched-off node is SF-0152 (409). A repeated heartbeat only moves the last-heartbeat time.
func (s *Postgres) Heartbeat(ctx context.Context, edgeID, subject string, in HeartbeatInput) (HeartbeatResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out HeartbeatResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		applied, ring, err := edgeConnector(ctx, tx, p.TenantID, edgeID, subject, true)
		if err != nil {
			return err
		}
		out.Ring = ring
		if v := in.AppliedConfigVersion; v != nil && (applied == nil || *applied != *v) {
			tag, err := tx.Exec(ctx, `update edge_config_version set applied_at = coalesce(applied_at, now()) where tenant_id = $1 and edge_id = $2 and version = $3`, p.TenantID, edgeID, *v)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeEdgeConfigNotFound, "configuration version was never published for this edge node")
			}
			if err := enqueue(ctx, tx, edgeSource, edgeID, sfevents.EdgeConfigApplied, p.TenantID, sfevents.EdgeConfigAppliedData{
				EdgeID: edgeID, Version: *v, PreviousVersion: applied}); err != nil {
				return err
			}
			applied = v
		}
		machines, err := json.Marshal(in.Machines)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update edge_node set last_heartbeat_at = now(), buffer_fill_pct = $3, machine_status = $4, applied_version = $5,
				connector_version = coalesce($6, connector_version), cert_serial = coalesce($7, cert_serial), cert_expires_at = coalesce($8, cert_expires_at)
			where tenant_id = $1 and id = $2`,
			p.TenantID, edgeID, in.BufferFillPct.String(), machines, applied, in.ConnectorVersion, in.CertSerial, in.CertExpiresAt); err != nil {
			return err
		}
		out.EdgeID, out.AppliedConfigVersion = edgeID, applied
		out.HeartbeatIntervalSeconds = int(domain.HeartbeatInterval.Seconds())
		return tx.QueryRow(ctx, `select max(version) from edge_config_version where tenant_id = $1 and edge_id = $2`, p.TenantID, edgeID).Scan(&out.LatestConfigVersion)
	})
	return out, err
}
