package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// AutoPartner is a partner that receives documents automatically, with the transaction sets it asked for.
type AutoPartner struct {
	PartnerID       string
	TransactionSets []string
}

// PutAutoOutbound sets the transaction sets a partner receives when a shipment ships (856, DESADV) or an invoice posts (810,
// INVOIC). An empty list switches the automatic sending off. The legal entity is the one of the partner's trade profile, so the
// consumer of that entity's events finds the partner; a partner with no profile (or one outside the caller's scope) is ErrNotFound.
func (s *Store) PutAutoOutbound(ctx context.Context, partnerID string, sets []string) ([]string, error) {
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if len(sets) == 0 {
			_, err := tx.Exec(ctx, `delete from partner_auto_outbound where partner_id = $1`, partnerID)
			return err
		}
		tag, err := tx.Exec(ctx, `insert into partner_auto_outbound (tenant_id, partner_id, legal_entity_id, transaction_sets)
			select $1::uuid, partner_id, legal_entity_id, $3::text[] from partner_trade_profile where partner_id = $2
			on conflict (tenant_id, partner_id) do update set transaction_sets = excluded.transaction_sets,
			legal_entity_id = excluded.legal_entity_id, updated_at = now()`, tid, partnerID, sets)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sets, nil
}

// GetAutoOutbound returns the partner's automatic transaction sets (empty when it has none).
func (s *Store) GetAutoOutbound(ctx context.Context, partnerID string) ([]string, error) {
	sets := []string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select transaction_sets from partner_auto_outbound where partner_id = $1`, partnerID).Scan(&sets)
		if errors.Is(err, pgx.ErrNoRows) {
			sets = []string{}
			return nil
		}
		return err
	})
	return sets, err
}

// AutoPartners lists the active partners of a legal entity whose automatic sets include one of the wanted sets, with the
// wanted sets they asked for.
func (s *Store) AutoPartners(ctx context.Context, legalEntityID string, want []string) ([]AutoPartner, error) {
	var out []AutoPartner
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select a.partner_id::text, array(select unnest(a.transaction_sets) intersect select unnest($2::text[]))
			from partner_auto_outbound a join trading_partner p on p.tenant_id = a.tenant_id and p.id = a.partner_id
			where a.legal_entity_id = $1 and p.status = 'active' and a.transaction_sets && $2::text[] order by a.partner_id`, legalEntityID, want)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ap AutoPartner
			if err := rows.Scan(&ap.PartnerID, &ap.TransactionSets); err != nil {
				return err
			}
			out = append(out, ap)
		}
		return rows.Err()
	})
	return out, err
}
