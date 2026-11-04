package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

func (s *Store) PutTradeProfile(ctx context.Context, partnerID string, p domain.TradeProfile) (domain.TradeProfile, error) {
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into partner_trade_profile (tenant_id, partner_id, legal_entity_id, customer_id,
			ship_from_name, ship_from_gln, ship_to_name, ship_to_gln, bill_to_name, bill_to_gln, remit_to_name, remit_to_gln, default_scac)
			select $1::uuid, id, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11, $12, $13 from trading_partner where id = $2
			on conflict (tenant_id, partner_id) do update set legal_entity_id = excluded.legal_entity_id, customer_id = excluded.customer_id,
			ship_from_name = excluded.ship_from_name, ship_from_gln = excluded.ship_from_gln, ship_to_name = excluded.ship_to_name,
			ship_to_gln = excluded.ship_to_gln, bill_to_name = excluded.bill_to_name, bill_to_gln = excluded.bill_to_gln,
			remit_to_name = excluded.remit_to_name, remit_to_gln = excluded.remit_to_gln, default_scac = excluded.default_scac,
			version = partner_trade_profile.version + 1, updated_at = now()`,
			tid, partnerID, p.LegalEntityID, p.CustomerID, p.ShipFrom.Name, p.ShipFrom.GLN, p.ShipTo.Name, p.ShipTo.GLN,
			p.BillTo.Name, p.BillTo.GLN, p.RemitTo.Name, p.RemitTo.GLN, p.DefaultSCAC)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42501" {
			// The partner's profile belongs to a legal entity outside the caller's scope (RLS refuses the upsert).
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	return p, err
}

func (s *Store) GetTradeProfile(ctx context.Context, partnerID string) (domain.TradeProfile, error) {
	var p domain.TradeProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select legal_entity_id::text, customer_id::text, ship_from_name, ship_from_gln, ship_to_name, ship_to_gln,
			bill_to_name, bill_to_gln, remit_to_name, remit_to_gln, default_scac from partner_trade_profile where partner_id = $1`, partnerID).
			Scan(&p.LegalEntityID, &p.CustomerID, &p.ShipFrom.Name, &p.ShipFrom.GLN, &p.ShipTo.Name, &p.ShipTo.GLN,
				&p.BillTo.Name, &p.BillTo.GLN, &p.RemitTo.Name, &p.RemitTo.GLN, &p.DefaultSCAC)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return p, err
}
