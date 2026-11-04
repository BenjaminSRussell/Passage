package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// TenantParameters are the pricing parameters of the whole tenant (PRD 5.26.13 scope tenant).
// A tenant with no stored row reads as the defaults at Version 0.
type TenantParameters struct {
	// CacheTTLSeconds is the record snapshot lifetime, 10 to 600; 0 means the tenant follows the
	// deployment's PRICING_CACHE_TTL.
	CacheTTLSeconds int
	// CampaignKillSwitchEffect is NEXT_RESOLVE (default) or IMMEDIATE_CACHE_FLUSH (FR-26.10).
	CampaignKillSwitchEffect string
	Version                  int
	UpdatedBy                string
	UpdatedAt                *time.Time
}

// DefaultTenantParameters are the parameters of a tenant with nothing stored (version 0).
func DefaultTenantParameters() TenantParameters {
	return TenantParameters{CampaignKillSwitchEffect: domain.KillEffectNextResolve}
}

// GetTenantParameters returns the caller's tenant parameters, or the defaults when none are stored.
func (s *Postgres) GetTenantParameters(ctx context.Context) (TenantParameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out TenantParameters
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadTenantParameters(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}

// SetTenantParameters stores the tenant parameters. version is the stored version the caller
// read (0 when none was stored); a different version is 412 conflict_version. The change is
// announced as tenantparameter.changed through the outbox in the same transaction. A repeated
// Idempotency-Key replays the first response.
func (s *Postgres) SetTenantParameters(ctx context.Context, in TenantParameters, version int, idempotencyKey string) (TenantParameters, error) {
	p, _ := tenant.FromContext(ctx)
	var out TenantParameters
	if in.CampaignKillSwitchEffect == "" {
		in.CampaignKillSwitchEffect = domain.KillEffectNextResolve
	}
	if !domain.ValidKillEffect(in.CampaignKillSwitchEffect) || (in.CacheTTLSeconds != 0 && !domain.ValidCacheTTL(in.CacheTTLSeconds)) {
		return TenantParameters{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "tenant parameter is not allowed")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		hash := commandHash("tenant-parameters-set", strconv.Itoa(version), strconv.Itoa(in.CacheTTLSeconds), in.CampaignKillSwitchEffect)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		var ttl any // 0 is stored as NULL: follow the deployment
		if in.CacheTTLSeconds != 0 {
			ttl = in.CacheTTLSeconds
		}
		var tag pgconn.CommandTag
		var err error
		if version == 0 {
			tag, err = tx.Exec(ctx, `insert into pricing_tenant_parameter (tenant_id, cache_ttl_seconds, campaign_kill_switch_effect, updated_by)
				values ($1, $2, $3, $4) on conflict (tenant_id) do nothing`, p.TenantID, ttl, in.CampaignKillSwitchEffect, p.UserID)
		} else {
			tag, err = tx.Exec(ctx, `update pricing_tenant_parameter set cache_ttl_seconds = $3, campaign_kill_switch_effect = $4,
					version = version + 1, updated_by = $5, updated_at = now()
				where tenant_id = $1 and version = $2`, p.TenantID, version, ttl, in.CampaignKillSwitchEffect, p.UserID)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "tenant parameters version does not match")
		}
		if out, err = loadTenantParameters(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := enqueueEvent(ctx, tx, "parameters", p.TenantID, pevents.TenantParametersChanged, pevents.TenantParametersChangedData{
			CacheTTLSeconds: out.CacheTTLSeconds, CampaignKillSwitchEffect: out.CampaignKillSwitchEffect,
			Version: out.Version, UpdatedBy: out.UpdatedBy}); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

func loadTenantParameters(ctx context.Context, tx pgx.Tx, tenantID string) (TenantParameters, error) {
	out := DefaultTenantParameters()
	var ttl *int
	err := tx.QueryRow(ctx, `select cache_ttl_seconds, campaign_kill_switch_effect, version, updated_by, updated_at
		from pricing_tenant_parameter where tenant_id = $1`, tenantID).
		Scan(&ttl, &out.CampaignKillSwitchEffect, &out.Version, &out.UpdatedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultTenantParameters(), nil
	}
	if err != nil {
		return out, err
	}
	if ttl != nil {
		out.CacheTTLSeconds = *ttl
	}
	return out, nil
}
