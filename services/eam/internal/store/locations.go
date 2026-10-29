package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
)

// FunctionalLocation is a node of a site's plant structure tree (FR-44.1).
type FunctionalLocation struct {
	ID           string    `json:"id"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	SiteID       string    `json:"siteId"`
	ParentID     *string   `json:"parentId,omitempty"`
	LocationType *string   `json:"locationType,omitempty"`
	Depth        int       `json:"depth"`
	Path         string    `json:"path"`
	CreatedAt    time.Time `json:"createdAt"`
}

// LocationDetail is a location with its direct children and the assets
// installed at it (Plant structure tree, 8.44.6).
type LocationDetail struct {
	FunctionalLocation
	Children []FunctionalLocation `json:"children"`
	Assets   []Asset              `json:"assets"`
}

// LocationHistory is one install, move or removal of an asset (FR-44.2), with
// the paths of the locations it left and went to.
type LocationHistory struct {
	ID       string    `json:"id"`
	AssetID  string    `json:"assetId"`
	Action   string    `json:"action"`
	FromID   *string   `json:"fromLocationId,omitempty"`
	FromPath *string   `json:"fromPath,omitempty"`
	ToID     *string   `json:"toLocationId,omitempty"`
	ToPath   *string   `json:"toPath,omitempty"`
	Actor    string    `json:"actor"`
	At       time.Time `json:"at"`
}

// LocationListOptions narrows the location list: one site, and optionally the
// subtree under one location (the location itself included).
type LocationListOptions struct {
	SiteID string
	Under  string
	Cursor string
	Limit  int
}

const locationColumns = `id::text, code, name, site_id::text, parent_id::text, location_type, depth, path, created_at`

func scanLocation(row pgx.Row) (FunctionalLocation, error) {
	var l FunctionalLocation
	err := row.Scan(&l.ID, &l.Code, &l.Name, &l.SiteID, &l.ParentID, &l.LocationType, &l.Depth, &l.Path, &l.CreatedAt)
	return l, err
}

func getLocation(ctx context.Context, tx pgx.Tx, t, id string) (FunctionalLocation, error) {
	l, err := scanLocation(tx.QueryRow(ctx, `select `+locationColumns+` from functional_location
	  where tenant_id = $1 and id = $2`, t, id))
	return l, notFound(err, "functional location")
}

func collectLocations(rows pgx.Rows) ([]FunctionalLocation, error) {
	defer rows.Close()
	out := []FunctionalLocation{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// CreateLocation adds a functional location, a root of its site or a child of a
// parent in the same site (EAM-FL-002). Depth is limited to 12 (EAM-FL-001) and a
// path is unique per site (EAM-DUP-001). A repeated key returns the stored location.
func (s *Postgres) CreateLocation(ctx context.Context, in FunctionalLocation, key string) (FunctionalLocation, error) {
	var out FunctionalLocation
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "functional_location", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var gerr error
			out, gerr = getLocation(ctx, tx, t, id)
			return gerr
		}
		parentDepth, parentPath := 0, ""
		if in.ParentID != nil {
			parent, err := getLocation(ctx, tx, t, *in.ParentID)
			if err != nil {
				return err
			}
			if parent.SiteID != in.SiteID {
				return problem(422, domain.CodeLocationRule, "the parent location "+parent.Path+" belongs to another site")
			}
			parentDepth, parentPath = parent.Depth, parent.Path
		}
		depth, path, err := domain.ChildLocation(parentDepth, parentPath, in.Code)
		if err != nil {
			return gateProblem(err)
		}
		if dup, err := taken(ctx, tx, "functional_location", "tenant_id = $1 and site_id = $2 and path = $3", t, in.SiteID, path); err != nil || dup {
			if err != nil {
				return err
			}
			return problem(409, CodeDuplicate, "functional location "+path+" already exists at this site")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into functional_location (tenant_id, id, code, name, site_id, parent_id,
		    location_type, depth, path, idempotency_key, created_at)
		  values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			t, id, in.Code, in.Name, in.SiteID, in.ParentID, in.LocationType, depth, path, optStr(key), s.now()); err != nil {
			return err
		}
		out, err = getLocation(ctx, tx, t, id)
		return err
	})
	return out, err
}

// GetLocation returns one functional location with its children and installed assets.
func (s *Postgres) GetLocation(ctx context.Context, id string) (LocationDetail, error) {
	var out LocationDetail
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		l, err := getLocation(ctx, tx, t, id)
		if err != nil {
			return err
		}
		out = LocationDetail{FunctionalLocation: l, Assets: []Asset{}}
		rows, err := tx.Query(ctx, `select `+locationColumns+` from functional_location
		  where tenant_id = $1 and parent_id = $2 order by path`, t, id)
		if err != nil {
			return err
		}
		if out.Children, err = collectLocations(rows); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select `+assetColumns+` from asset
		  where tenant_id = $1 and functional_location_id = $2 order by asset_number`, t, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return err
			}
			out.Assets = append(out.Assets, a)
		}
		return rows.Err()
	})
	return out, err
}

// ListLocations pages a site's functional locations in path order, so the tree
// reads top-down. Under narrows the list to that location's subtree. Cursor is
// the last id of the previous page.
func (s *Postgres) ListLocations(ctx context.Context, opt LocationListOptions) (Page[FunctionalLocation], error) {
	var out Page[FunctionalLocation]
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		prefix := ""
		if opt.Under != "" {
			under, err := getLocation(ctx, tx, t, opt.Under)
			if err != nil {
				return err
			}
			if opt.SiteID != "" && under.SiteID != opt.SiteID {
				return problem(422, domain.CodeLocationRule, "the location "+under.Path+" belongs to another site")
			}
			opt.SiteID, prefix = under.SiteID, under.Path
		}
		var afterPath string
		var afterID *string
		if opt.Cursor != "" {
			if err := tx.QueryRow(ctx, `select path from functional_location where tenant_id = $1 and id = $2`,
				t, opt.Cursor).Scan(&afterPath); err != nil {
				return notFound(err, "cursor location")
			}
			afterID = &opt.Cursor
		}
		// The subtree pattern escapes LIKE wildcards: codes may contain "_".
		rows, err := tx.Query(ctx, `select `+locationColumns+` from functional_location
		  where tenant_id = $1 and ($2::uuid is null or site_id = $2)
		    and ($3 = '' or path = $3 or path like replace(replace(replace($3, '\', '\\'), '%', '\%'), '_', '\_') || '/%')
		    and ($5::uuid is null or (path, id) > ($4, $5::uuid))
		  order by path, id limit $6`, t, optStr(opt.SiteID), prefix, afterPath, afterID, opt.Limit+1)
		if err != nil {
			return err
		}
		items, err := collectLocations(rows)
		if err != nil {
			return err
		}
		out = pageOf(items, opt.Limit, func(l FunctionalLocation) string { return l.ID })
		return nil
	})
	return out, err
}

// PlaceAsset installs, moves or removes an asset's functional location (FR-44.1,
// FR-44.2) and appends the change to the location history. toLocationID "" removes
// the asset. The location must be in the asset's site and a DISPOSED asset cannot
// be placed (EAM-FL-002, EAM-AST-001). The caller's version must match (If-Match)
// and the asset's version moves on. A repeated key returns the asset unchanged.
func (s *Postgres) PlaceAsset(ctx context.Context, assetID, toLocationID string, version int, key string) (Asset, error) {
	var out Asset
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if id, err := existingID(ctx, tx, "asset_location_history", t, key); err != nil || id != "" {
			if err != nil {
				return err
			}
			var owner string
			if err := tx.QueryRow(ctx, `select asset_id::text from asset_location_history where tenant_id = $1 and id = $2`,
				t, id).Scan(&owner); err != nil {
				return err
			}
			if owner != assetID {
				return problem(422, CodeKeyReuse, "Idempotency-Key was already used to place another asset")
			}
			var gerr error
			out, gerr = getAsset(ctx, tx, t, assetID, false)
			return gerr
		}
		cur, err := getAsset(ctx, tx, t, assetID, true)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return problem(412, CodeVersion, "asset version changed")
		}
		if cur.LifecycleState == "DISPOSED" {
			return problem(422, CodeAssetState, "asset is DISPOSED; it cannot be placed at a functional location")
		}
		if toLocationID != "" {
			loc, err := getLocation(ctx, tx, t, toLocationID)
			if err != nil {
				return err
			}
			if loc.SiteID != cur.SiteID {
				return problem(422, domain.CodeLocationRule, "functional location "+loc.Path+" belongs to another site than the asset")
			}
		}
		current := ""
		if cur.FunctionalLocationID != nil {
			current = *cur.FunctionalLocationID
		}
		move, err := domain.LocationChange(current, toLocationID)
		if err != nil {
			return gateProblem(err)
		}
		out, err = scanAsset(tx.QueryRow(ctx, `update asset set functional_location_id = $3,
		    version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 returning `+assetColumns,
			t, assetID, optStr(toLocationID)))
		if err != nil {
			return err
		}
		return recordLocationChange(ctx, tx, t, assetID, move, current, toLocationID, actorOf(ctx), s.now(), key)
	})
	return out, err
}

// recordLocationChange appends one row to the asset's location history.
func recordLocationChange(ctx context.Context, tx pgx.Tx, t, assetID string, move domain.AssetMove, from, to, actor string,
	at time.Time, key string) error {
	_, err := tx.Exec(ctx, `insert into asset_location_history (tenant_id, id, asset_id, action, from_location_id,
	    to_location_id, actor, at, idempotency_key) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		t, ids.New(), assetID, string(move), optStr(from), optStr(to), actor, at, optStr(key))
	return err
}

// ListLocationHistory returns an asset's install, move and removal history, newest first.
func (s *Postgres) ListLocationHistory(ctx context.Context, assetID string) ([]LocationHistory, error) {
	out := []LocationHistory{}
	err := s.withTx(ctx, func(tx pgx.Tx, t string) error {
		if _, err := getAsset(ctx, tx, t, assetID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select h.id::text, h.asset_id::text, h.action, h.from_location_id::text, f.path,
		    h.to_location_id::text, l.path, h.actor, h.at
		  from asset_location_history h
		  left join functional_location f on f.tenant_id = h.tenant_id and f.id = h.from_location_id
		  left join functional_location l on l.tenant_id = h.tenant_id and l.id = h.to_location_id
		  where h.tenant_id = $1 and h.asset_id = $2 order by h.at desc, h.id desc`, t, assetID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h LocationHistory
			if err := rows.Scan(&h.ID, &h.AssetID, &h.Action, &h.FromID, &h.FromPath, &h.ToID, &h.ToPath, &h.Actor, &h.At); err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}
