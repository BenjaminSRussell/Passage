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

var structureNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("passage:productcost:item-structure"))

type RollInput struct {
	VersionID   string
	SiteID      string
	Key         string
	Fingerprint any
	Options     domain.RollOptions
	Catalog     domain.Catalog
	Run         domain.RollRun
}

type RollError struct {
	ItemID      string              `json:"itemId"`
	Diagnostics []domain.Diagnostic `json:"diagnostics"`
}

type RollResult struct {
	VersionID   string      `json:"costingVersionId"`
	SiteID      string      `json:"siteId"`
	View        string      `json:"view"`
	ItemCount   int         `json:"itemCount"`
	StoredCount int         `json:"storedCount"`
	ErrorCount  int         `json:"errorCount"`
	Errors      []RollError `json:"errors"`
}

func structureID(kind string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return uuid.NewSHA1(structureNamespace, append([]byte(kind+"\n"), raw...)).String(), nil
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

func storeRoll(ctx context.Context, tx pgx.Tx, p tenant.Principal, in RollInput) (RollResult, error) {
	res := RollResult{VersionID: in.VersionID, SiteID: in.SiteID, View: in.Options.View, ItemCount: in.Run.ItemCount, ErrorCount: in.Run.ErrorCount, Errors: []RollError{}}
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
	var rolled, items, calcIDs, boms, routes, costs, diags []string
	for _, r := range in.Run.Results {
		rolled = append(rolled, r.ItemID)
		if r.Status == domain.StatusError {
			res.Errors = append(res.Errors, RollError{ItemID: r.ItemID, Diagnostics: r.Diagnostics})
			continue
		}
		item := in.Catalog[r.ItemID]
		bom, err := structureID("bom", item.BOM)
		if err != nil {
			return res, err
		}
		route, err := structureID("route", item.Operations)
		if err != nil {
			return res, err
		}
		diagnostics, err := json.Marshal(append([]domain.Diagnostic{}, r.Diagnostics...))
		if err != nil {
			return res, err
		}
		items, calcIDs, boms, routes = append(items, r.ItemID), append(calcIDs, uuid.NewString()), append(boms, bom), append(routes, route)
		costs, diags = append(costs, r.Unit.Round(domain.UnitScale).String()), append(diags, string(diagnostics))
		res.StoredCount++
	}
	if _, err := tx.Exec(ctx, `update cost_calculation set status = 'Superseded'
		where tenant_id = $1 and costing_version_id = $2 and site_id = $3 and valuation_view = $4 and status = 'Pending' and item_id = any($5::uuid[])`,
		p.TenantID, in.VersionID, in.SiteID, in.Options.View, rolled); err != nil {
		return res, err
	}
	_, err = tx.Exec(ctx, `insert into cost_calculation
		(tenant_id, id, legal_entity_id, costing_version_id, item_id, site_id, valuation_view, calc_quantity, explosion_mode, bom_version_id, route_version_id, unit_cost, status, diagnostics)
		select $1, u.id, $2, $3, u.item, $4, $5, 1, $6, u.bom, u.route, u.cost::numeric, 'Pending', u.diag::jsonb
		from unnest($7::uuid[], $8::uuid[], $9::uuid[], $10::uuid[], $11::text[], $12::text[]) as u(id, item, bom, route, cost, diag)`,
		p.TenantID, cur.LegalEntityID, in.VersionID, in.SiteID, in.Options.View, string(in.Options.Mode), calcIDs, items, boms, routes, costs, diags)
	return res, err
}
