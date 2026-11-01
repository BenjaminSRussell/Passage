package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// CodePolicyStale (PRC-POL-003) is raised when If-Match is not the saved
// policy version (0 when none is saved).
const CodePolicyStale = "PRC-POL-003"

// loadPolicy reads the policy of a legal entity inside tx; a legal entity with
// no saved policy has domain.DefaultPolicy at version 0.
func loadPolicy(ctx context.Context, tx pgx.Tx, legalEntityID string) (domain.Policy, error) {
	p, _ := tenant.FromContext(ctx)
	out := domain.DefaultPolicy(legalEntityID)
	var updatedBy *string
	err := tx.QueryRow(ctx, `select change_management_default, po_four_eyes, allow_invoice_before_receipt, category_access_mode, row_version,
		updated_by::text, updated_at from purchasing_policy where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, legalEntityID).
		Scan(&out.ChangeManagementDefault, &out.POFourEyes, &out.AllowInvoiceBeforeReceipt, &out.CategoryAccessMode, &out.Version, &updatedBy, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultPolicy(legalEntityID), nil
	}
	if updatedBy != nil {
		out.UpdatedBy = *updatedBy
	}
	return out, err
}

// GetPolicy returns the purchasing policy of a legal entity, or the defaults.
func (s *Postgres) GetPolicy(ctx context.Context, legalEntityID string) (domain.Policy, error) {
	var out domain.Policy
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadPolicy(ctx, tx, legalEntityID)
		return err
	})
	return out, err
}

// ListPolicies returns every saved policy visible to the caller, by legal entity.
func (s *Postgres) ListPolicies(ctx context.Context) ([]domain.Policy, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Policy{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select legal_entity_id::text, change_management_default, po_four_eyes,
			allow_invoice_before_receipt, category_access_mode, row_version, coalesce(updated_by::text, ''), updated_at
			from purchasing_policy where tenant_id = $1 order by legal_entity_id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pol domain.Policy
			if err := rows.Scan(&pol.LegalEntityID, &pol.ChangeManagementDefault, &pol.POFourEyes, &pol.AllowInvoiceBeforeReceipt,
				&pol.CategoryAccessMode, &pol.Version, &pol.UpdatedBy, &pol.UpdatedAt); err != nil {
				return err
			}
			out = append(out, pol)
		}
		return rows.Err()
	})
	return out, err
}

// PutPolicy saves the policy of in.LegalEntityID under If-Match: 0 creates it,
// n replaces version n. It writes policy.changed.v1 in the same transaction.
func (s *Postgres) PutPolicy(ctx context.Context, in domain.Policy, ifMatch int) (domain.Policy, error) {
	p, _ := tenant.FromContext(ctx)
	if in.CategoryAccessMode == "" {
		in.CategoryAccessMode = domain.AccessSoft
	}
	var out domain.Policy
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		stale := func(cur int) error {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodePolicyStale,
				fmt.Sprintf("purchasing policy is at version %d, not %d", cur, ifMatch))
		}
		if ifMatch == 0 {
			tag, err := tx.Exec(ctx, `insert into purchasing_policy (tenant_id, legal_entity_id, change_management_default,
				po_four_eyes, allow_invoice_before_receipt, category_access_mode, updated_by) values ($1, $2, $3, $4, $5, $6, $7)
				on conflict (tenant_id, legal_entity_id) do nothing`,
				p.TenantID, in.LegalEntityID, in.ChangeManagementDefault, in.POFourEyes, in.AllowInvoiceBeforeReceipt, in.CategoryAccessMode, userOrNil(p.UserID))
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				cur, err := loadPolicy(ctx, tx, in.LegalEntityID)
				if err != nil {
					return err
				}
				return stale(cur.Version)
			}
		} else {
			tag, err := tx.Exec(ctx, `update purchasing_policy set change_management_default = $3, po_four_eyes = $4,
				allow_invoice_before_receipt = $5, category_access_mode = $6, updated_by = $7, updated_at = now(), row_version = row_version + 1
				where tenant_id = $1 and legal_entity_id = $2 and row_version = $8`,
				p.TenantID, in.LegalEntityID, in.ChangeManagementDefault, in.POFourEyes, in.AllowInvoiceBeforeReceipt, in.CategoryAccessMode, userOrNil(p.UserID), ifMatch)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				cur, err := loadPolicy(ctx, tx, in.LegalEntityID)
				if err != nil {
					return err
				}
				return stale(cur.Version)
			}
		}
		var err error
		if out, err = loadPolicy(ctx, tx, in.LegalEntityID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "policy", out.LegalEntityID, purchasingevents.PolicyChanged, purchasingevents.PolicyChangedData{
			LegalEntityID: out.LegalEntityID, Version: out.Version, ChangeManagementDefault: out.ChangeManagementDefault,
			POFourEyes: out.POFourEyes, AllowInvoiceBeforeReceipt: out.AllowInvoiceBeforeReceipt, CategoryAccessMode: out.CategoryAccessMode, UpdatedBy: out.UpdatedBy,
		})
	})
	return out, err
}
