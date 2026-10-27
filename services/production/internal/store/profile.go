package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// PostingProfile is a stored posting profile (FR-21.13) with its version for If-Match.
type PostingProfile struct {
	LegalEntityID string `json:"legalEntityId"`
	domain.Profile
	Version   int       `json:"version"`
	UpdatedBy string    `json:"updatedBy"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProfileSave creates or replaces the profile of a legal entity × item group
// × site. IfMatch is 0 to create; replacing an existing profile needs its version.
type ProfileSave struct {
	LegalEntityID string
	Profile       domain.Profile
	IfMatch       int
	Actor         string
}

const profileColumns = `legal_entity_id::text, item_group, site_key, wip_account, inventory_account, finished_goods_account,
	labor_absorption_account, overhead_absorption_account, lot_size_variance_account, quantity_variance_account,
	price_variance_account, substitution_variance_account, rework_variance_account, version, updated_by, updated_at`

func scanProfile(row pgx.Row) (PostingProfile, error) {
	var p PostingProfile
	err := row.Scan(&p.LegalEntityID, &p.ItemGroup, &p.SiteID, &p.WIP, &p.Inventory, &p.FinishedGoods, &p.LaborAbsorption,
		&p.OverheadAbsorption, &p.VarLotSize, &p.VarQuantity, &p.VarPrice, &p.VarSubstitution, &p.VarRework, &p.Version, &p.UpdatedBy, &p.UpdatedAt)
	return p, err
}

func loadProfiles(ctx context.Context, q querier, tenantID, legalEntityID string) ([]PostingProfile, error) {
	rows, err := q.Query(ctx, `select `+profileColumns+` from production_posting_profile
		where tenant_id = $1 and legal_entity_id = $2 order by item_group, site_key`, tenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PostingProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// resolveProfile finds the accounts for the order's item group and site.
func resolveProfile(ctx context.Context, q querier, tenantID string, h orderHead) (domain.Profile, error) {
	stored, err := loadProfiles(ctx, q, tenantID, h.legalEntityID)
	if err != nil {
		return domain.Profile{}, err
	}
	profiles := make([]domain.Profile, len(stored))
	for i, p := range stored {
		profiles[i] = p.Profile
	}
	p, err := domain.ResolveProfile(profiles, h.itemGroup, h.siteID)
	if err != nil {
		return domain.Profile{}, domainProblem(err)
	}
	return p, nil
}

// ListPostingProfiles returns the posting profiles of a legal entity.
func (s *Postgres) ListPostingProfiles(ctx context.Context, legalEntityID string) ([]PostingProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var out []PostingProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadProfiles(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// SavePostingProfile creates a profile (IfMatch 0) or replaces it under
// If-Match; created reports which.
func (s *Postgres) SavePostingProfile(ctx context.Context, in ProfileSave) (PostingProfile, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out PostingProfile
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr := in.Profile
		var version int
		err := tx.QueryRow(ctx, `select version from production_posting_profile
			where tenant_id = $1 and legal_entity_id = $2 and item_group = $3 and site_key = $4 for update`,
			p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID).Scan(&version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if in.IfMatch != 0 {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the posting profile does not exist; create it without If-Match")
			}
			created = true
			_, err = tx.Exec(ctx, `insert into production_posting_profile (tenant_id, legal_entity_id, item_group, site_key, wip_account,
					inventory_account, finished_goods_account, labor_absorption_account, overhead_absorption_account, lot_size_variance_account,
					quantity_variance_account, price_variance_account, substitution_variance_account, updated_by, rework_variance_account)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
				p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID, pr.WIP, pr.Inventory, pr.FinishedGoods, pr.LaborAbsorption,
				pr.OverheadAbsorption, pr.VarLotSize, pr.VarQuantity, pr.VarPrice, pr.VarSubstitution, in.Actor, pr.VarRework)
		case err != nil:
			return err
		case in.IfMatch == 0:
			return httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the profile version is required to replace it")
		case in.IfMatch != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the posting profile was changed by another user; current version is %d", version))
		default:
			_, err = tx.Exec(ctx, `update production_posting_profile set wip_account = $5, inventory_account = $6, finished_goods_account = $7,
					labor_absorption_account = $8, overhead_absorption_account = $9, lot_size_variance_account = $10,
					quantity_variance_account = $11, price_variance_account = $12, substitution_variance_account = $13,
					updated_by = $14, rework_variance_account = $15, updated_at = now(), version = version + 1
				where tenant_id = $1 and legal_entity_id = $2 and item_group = $3 and site_key = $4`,
				p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID, pr.WIP, pr.Inventory, pr.FinishedGoods, pr.LaborAbsorption,
				pr.OverheadAbsorption, pr.VarLotSize, pr.VarQuantity, pr.VarPrice, pr.VarSubstitution, in.Actor, pr.VarRework)
		}
		if err != nil {
			return err
		}
		out, err = scanProfile(tx.QueryRow(ctx, `select `+profileColumns+` from production_posting_profile
			where tenant_id = $1 and legal_entity_id = $2 and item_group = $3 and site_key = $4`,
			p.TenantID, in.LegalEntityID, pr.ItemGroup, pr.SiteID))
		return err
	})
	return out, created, err
}
