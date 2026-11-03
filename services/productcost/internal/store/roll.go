package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

const rollScope = "version.roll"

type RollSource struct {
	BOMVersionID   string
	RouteVersionID string
}

type RollInput struct {
	VersionID   string
	SiteID      string
	Key         string
	Fingerprint any
	Options     domain.RollOptions
	Roots       []string
	Sources     map[string]RollSource
	Run         domain.RollRun
}

type RollError struct {
	ItemID      string              `json:"itemId"`
	Diagnostics []domain.Diagnostic `json:"diagnostics"`
}

type RollResult struct {
	VersionID   string      `json:"costingVersionId"`
	Version     int         `json:"version"`
	SiteID      string      `json:"siteId"`
	View        string      `json:"view"`
	RolledBy    string      `json:"rolledBy"`
	ItemCount   int         `json:"itemCount"`
	StoredCount int         `json:"storedCount"`
	ErrorCount  int         `json:"errorCount"`
	Errors      []RollError `json:"errors"`
}

func (s *Postgres) StoreRoll(ctx context.Context, in RollInput) (RollResult, bool, error) {
	var out RollResult
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, p.TenantID, in.Key, rollScope, in.Fingerprint, func() (RollResult, error) {
			return storeRoll(ctx, tx, p, in)
		})
		return err
	})
	return out, replayed, err
}

func nullableUUID(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func storeRoll(ctx context.Context, tx pgx.Tx, p tenant.Principal, in RollInput) (RollResult, error) {
	res := RollResult{VersionID: in.VersionID, SiteID: in.SiteID, View: in.Options.View, RolledBy: p.UserID, ItemCount: len(in.Roots), Errors: []RollError{}}
	cur, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2 for update`, p.TenantID, in.VersionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, versionNotFound(in.VersionID)
	}
	if err != nil {
		return res, err
	}
	if cur.Status != domain.VersionOpen {
		return res, &domain.CodedError{Code: domain.CodeVersionNotRollable, Detail: "version " + cur.Code + " is " + cur.Status + "; only an Open version can be rolled"}
	}
	if len(cur.SiteIDs) > 0 && !slices.Contains(cur.SiteIDs, in.SiteID) {
		return res, &domain.CodedError{Code: domain.CodeVersionNotRollable, Detail: "site " + in.SiteID + " is outside the site scope of version " + cur.Code}
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, domain.RollLockKey(p.TenantID, in.VersionID)); err != nil {
		return res, err
	}
	byItem := make(map[string]domain.Result, len(in.Run.Results))
	for _, r := range in.Run.Results {
		byItem[r.ItemID] = r
	}
	var items, calcIDs, costs, diags []string
	var bomIDs, routeIDs []*string
	for _, id := range in.Roots {
		r := byItem[id]
		if r.Status == domain.StatusError {
			res.ErrorCount++
			res.Errors = append(res.Errors, RollError{ItemID: id, Diagnostics: r.Diagnostics})
			continue
		}
		diagnostics, err := json.Marshal(append([]domain.Diagnostic{}, r.Diagnostics...))
		if err != nil {
			return res, err
		}
		src := in.Sources[id]
		items, calcIDs = append(items, id), append(calcIDs, uuid.NewString())
		bomIDs, routeIDs = append(bomIDs, nullableUUID(src.BOMVersionID)), append(routeIDs, nullableUUID(src.RouteVersionID))
		costs, diags = append(costs, r.Unit.Round(domain.UnitScale).String()), append(diags, string(diagnostics))
		res.StoredCount++
	}
	if _, err := tx.Exec(ctx, `update cost_calculation set status = 'Superseded'
		where tenant_id = $1 and costing_version_id = $2 and site_id = $3 and valuation_view = $4 and status = 'Pending' and item_id = any($5::uuid[])`,
		p.TenantID, in.VersionID, in.SiteID, in.Options.View, in.Roots); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `insert into cost_calculation
		(tenant_id, id, legal_entity_id, costing_version_id, item_id, site_id, valuation_view, calc_quantity, explosion_mode, bom_version_id, route_version_id, unit_cost, status, diagnostics, rolled_by)
		select $1, u.id, $2, $3, u.item, $4, $5, 1, $6, u.bom, u.route, u.cost::numeric, 'Pending', u.diag::jsonb, $7
		from unnest($8::uuid[], $9::uuid[], $10::uuid[], $11::uuid[], $12::text[], $13::text[]) as u(id, item, bom, route, cost, diag)`,
		p.TenantID, cur.LegalEntityID, in.VersionID, in.SiteID, in.Options.View, string(in.Options.Mode), p.UserID, calcIDs, items, bomIDs, routeIDs, costs, diags); err != nil {
		return res, err
	}
	err = tx.QueryRow(ctx, `update costing_version set version = version + 1 where tenant_id = $1 and id = $2 returning version`, p.TenantID, in.VersionID).Scan(&res.Version)
	return res, err
}
