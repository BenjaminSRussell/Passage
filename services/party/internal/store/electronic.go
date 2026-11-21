package store

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/numbering"
)

const (
	CodeElectronicInvalid = "PTY-ELECTRONIC-INVALID"
	electronicColumns     = `id::text, party_id::text, legal_entity_id::text, kind, value`
)

type ElectronicStore interface {
	AddElectronicAddress(ctx context.Context, partyID string, in domain.NewElectronicAddress) (domain.ElectronicAddress, error)
	ListElectronicAddresses(ctx context.Context, partyID string) ([]domain.ElectronicAddress, error)
}

func scanElectronicAddress(row pgx.Row) (domain.ElectronicAddress, error) {
	var a domain.ElectronicAddress
	err := row.Scan(&a.ID, &a.PartyID, &a.LegalEntityID, &a.Kind, &a.Value)
	return a, err
}

func (s *Postgres) AddElectronicAddress(ctx context.Context, partyID string, in domain.NewElectronicAddress) (domain.ElectronicAddress, error) {
	caller, _ := tenant.FromContext(ctx)
	scopes := tenant.LegalEntityScopeList(caller)
	if len(scopes) != 1 {
		return domain.ElectronicAddress{}, httpx.NewProblem(http.StatusUnprocessableEntity, numbering.CodeEntityRequired, "an electronic address belongs to exactly one legal entity; the caller must carry one")
	}
	id, err := canonicalID(partyID)
	if err != nil {
		return domain.ElectronicAddress{}, ErrNotFound
	}
	var address domain.ElectronicAddress
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from party where tenant_id = $1 and id = $2)`, caller.TenantID, id).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrNotFound
		}
		var err error
		address, err = scanElectronicAddress(tx.QueryRow(ctx, `insert into party_electronic_address (tenant_id, id, party_id, legal_entity_id, kind, value, normalized, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, party_id, legal_entity_id, kind, normalized) do update set value = excluded.value
			returning `+electronicColumns, caller.TenantID, ids.New(), id, scopes[0], in.Kind, in.Value, in.Normalized(), caller.UserID))
		return err
	})
	return address, err
}

func (s *Postgres) ListElectronicAddresses(ctx context.Context, partyID string) ([]domain.ElectronicAddress, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(partyID)
	if err != nil {
		return nil, ErrNotFound
	}
	addresses := []domain.ElectronicAddress{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from party where tenant_id = $1 and id = $2)`, caller.TenantID, id).Scan(&known); err != nil {
			return err
		}
		if !known {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, `select `+electronicColumns+` from party_electronic_address where tenant_id = $1 and party_id = $2 order by kind, normalized`, caller.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanElectronicAddress(rows)
			if err != nil {
				return err
			}
			addresses = append(addresses, a)
		}
		return rows.Err()
	})
	return addresses, err
}
