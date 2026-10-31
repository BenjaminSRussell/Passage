package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// ---- production parameters (PRD 4.21.13) ----

// StoredParameters is a stored parameter row with its version for If-Match.
type StoredParameters struct {
	LegalEntityID string `json:"legalEntityId"`
	domain.Parameters
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ParametersSave creates (IfMatch 0) or replaces the parameters of a legal entity × site.
type ParametersSave struct {
	LegalEntityID string
	Parameters    domain.Parameters
	IfMatch       int
	Actor         string
}

const parameterColumns = `legal_entity_id::text, site_key, raf_tolerance_pct::text, over_consumption_pct::text, auto_start_on_feedback,
	estimate_on_create, split_allocation, trigger_points_enabled, as_built_required, version, updated_by, updated_at`

func scanParameters(row pgx.Row) (StoredParameters, error) {
	var p StoredParameters
	var raf, over, alloc string
	err := row.Scan(&p.LegalEntityID, &p.SiteID, &raf, &over, &p.AutoStartOnFeedback, &p.EstimateOnCreate, &alloc,
		&p.TriggerPointsEnabled, &p.AsBuiltRequired, &p.Version, &p.UpdatedBy, &p.UpdatedAt)
	p.RAFTolerancePct, p.OverConsumptionPct, p.SplitAllocation = dec(raf), dec(over), domain.Allocation(alloc)
	return p, err
}

func loadParameters(ctx context.Context, q querier, tenantID, legalEntityID string) ([]StoredParameters, error) {
	rows, err := q.Query(ctx, `select `+parameterColumns+` from production_parameters
		where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) order by legal_entity_id, site_key`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredParameters{}
	for rows.Next() {
		p, err := scanParameters(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// effectiveParameters returns the parameters that apply to a legal entity's site:
// the site's row, else the All row, else defaults. It reads only those two rows by
// primary key, since every journal post and status command calls it.
func effectiveParameters(ctx context.Context, q querier, tenantID, legalEntityID, siteID string, defaults domain.Parameters) (domain.Parameters, error) {
	rows, err := q.Query(ctx, `select `+parameterColumns+` from production_parameters
		where tenant_id = $1 and legal_entity_id = $2 and site_key = any($3)`, tenantID, legalEntityID, []string{siteID, domain.AllKey})
	if err != nil {
		return defaults, err
	}
	defer rows.Close()
	stored := []domain.Parameters{}
	for rows.Next() {
		r, err := scanParameters(rows)
		if err != nil {
			return defaults, err
		}
		stored = append(stored, r.Parameters)
	}
	if err := rows.Err(); err != nil {
		return defaults, err
	}
	p, _ := domain.ResolveParameters(stored, siteID, defaults)
	return p, nil
}

// ListParameters returns the stored parameters of a legal entity.
func (s *Postgres) ListParameters(ctx context.Context, legalEntityID string) ([]StoredParameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out []StoredParameters
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadParameters(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// SaveParameters creates the parameters of a site (IfMatch 0) or replaces them under
// If-Match; created reports which. parameters.saved is enqueued in the same transaction.
func (s *Postgres) SaveParameters(ctx context.Context, in ParametersSave) (StoredParameters, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out StoredParameters
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr := in.Parameters
		var version int
		err := tx.QueryRow(ctx, `select version from production_parameters
			where tenant_id = $1 and legal_entity_id = $2 and site_key = $3 for update`, p.TenantID, in.LegalEntityID, pr.SiteID).Scan(&version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if in.IfMatch != 0 {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the parameters do not exist; create them without If-Match")
			}
			created = true
			// A concurrent create of the same site wins the insert; this one is a stale write.
			var tag pgconn.CommandTag
			tag, err = tx.Exec(ctx, `insert into production_parameters (tenant_id, legal_entity_id, site_key, raf_tolerance_pct, over_consumption_pct,
					auto_start_on_feedback, estimate_on_create, split_allocation, trigger_points_enabled, as_built_required, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) on conflict do nothing`, p.TenantID, in.LegalEntityID, pr.SiteID, pr.RAFTolerancePct.String(),
				pr.OverConsumptionPct.String(), pr.AutoStartOnFeedback, pr.EstimateOnCreate, string(pr.SplitAllocation), pr.TriggerPointsEnabled,
				pr.AsBuiltRequired, in.Actor)
			if err == nil && tag.RowsAffected() == 0 {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the parameters were created by another user; reload them and save with If-Match")
			}
		case err != nil:
			return err
		case in.IfMatch == 0:
			return httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the parameters version is required to replace them")
		case in.IfMatch != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the parameters were changed by another user; current version is %d", version))
		default:
			_, err = tx.Exec(ctx, `update production_parameters set raf_tolerance_pct = $4, over_consumption_pct = $5, auto_start_on_feedback = $6,
					estimate_on_create = $7, split_allocation = $8, trigger_points_enabled = $9, as_built_required = $10, updated_by = $11,
					updated_at = now(), version = version + 1
				where tenant_id = $1 and legal_entity_id = $2 and site_key = $3`, p.TenantID, in.LegalEntityID, pr.SiteID, pr.RAFTolerancePct.String(),
				pr.OverConsumptionPct.String(), pr.AutoStartOnFeedback, pr.EstimateOnCreate, string(pr.SplitAllocation), pr.TriggerPointsEnabled,
				pr.AsBuiltRequired, in.Actor)
		}
		if err != nil {
			return err
		}
		out, err = scanParameters(tx.QueryRow(ctx, `select `+parameterColumns+` from production_parameters
			where tenant_id = $1 and legal_entity_id = $2 and site_key = $3`, p.TenantID, in.LegalEntityID, pr.SiteID))
		if err != nil {
			return err
		}
		return enqueue(ctx, tx, in.LegalEntityID, prodevents.ParametersSaved, p.TenantID, in.Actor, prodevents.ParametersSavedData{
			LegalEntityID: out.LegalEntityID, SiteID: out.SiteID, RAFTolerancePct: out.RAFTolerancePct.String(), OverConsumptionPct: out.OverConsumptionPct.String(),
			AutoStartOnFeedback: out.AutoStartOnFeedback, EstimateOnCreate: out.EstimateOnCreate, SplitAllocation: string(out.SplitAllocation),
			TriggerPointsEnabled: out.TriggerPointsEnabled, AsBuiltRequired: out.AsBuiltRequired, Version: out.Version, Actor: in.Actor,
			OccurredAt: out.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	})
	return out, created, err
}

// orderParameters resolves the parameters of an order's legal entity and site. The
// defaults are the service settings the request carried; fields the caller does not
// use stay at their zero value, except the trigger switch (on) and the split
// allocation (proportional), which default to the behaviour before the table.
func orderParameters(ctx context.Context, q querier, tenantID string, h orderHead, defaults domain.Parameters) (domain.Parameters, error) {
	defaults.TriggerPointsEnabled = true
	return effectiveParameters(ctx, q, tenantID, h.legalEntityID, h.siteID, defaults)
}

// countAttachedAsBuilt counts the order's attached as-built records.
func countAttachedAsBuilt(ctx context.Context, q querier, tenantID, orderID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `select count(*) from production_as_built where tenant_id = $1 and order_id = $2 and detached_at is null`,
		tenantID, orderID).Scan(&n)
	return n, err
}
