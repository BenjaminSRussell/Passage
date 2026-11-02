package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
)

type PostingAccounts struct {
	LegalEntityID     string    `json:"legalEntityId"`
	InventoryAccount  string    `json:"inventoryAccount"`
	CostChangeAccount string    `json:"costChangeAccount"`
	UpdatedBy         string    `json:"updatedBy"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func (s *Postgres) GetPostingAccounts(ctx context.Context, legalEntityID string) (PostingAccounts, error) {
	var out PostingAccounts
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		err := tx.QueryRow(ctx, `select legal_entity_id::text, inventory_account, cost_change_account, updated_by, updated_at
			from posting_account where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, legalEntityID).
			Scan(&out.LegalEntityID, &out.InventoryAccount, &out.CostChangeAccount, &out.UpdatedBy, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.CodedError{Code: domain.CodePostingAccounts, Detail: "no posting accounts are configured for legal entity " + legalEntityID}
		}
		return err
	})
	return out, err
}

func (s *Postgres) SetPostingAccounts(ctx context.Context, in PostingAccounts) (PostingAccounts, error) {
	out := in
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		return tx.QueryRow(ctx, `insert into posting_account (tenant_id, legal_entity_id, inventory_account, cost_change_account, updated_by)
			values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, legal_entity_id) do update
			  set inventory_account = excluded.inventory_account, cost_change_account = excluded.cost_change_account,
			      updated_by = excluded.updated_by, updated_at = now()
			returning updated_by, updated_at`, p.TenantID, in.LegalEntityID, in.InventoryAccount, in.CostChangeAccount, p.UserID).
			Scan(&out.UpdatedBy, &out.UpdatedAt)
	})
	return out, err
}
