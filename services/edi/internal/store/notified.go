package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// ClaimNoticeSSCCs claims the containers for the advance ship notice under key and returns the ones key owns: those it claims now
// and those it claimed before (a retry of the same notice). A container another notice claimed first is left out, so a partner is
// notified of each container once whichever of the warehouse confirmation and the packing slip comes first.
func (s *Store) ClaimNoticeSSCCs(ctx context.Context, partnerID, legalEntityID, set, key string, ssccs []string) ([]string, error) {
	owned := []string{}
	if len(ssccs) == 0 {
		return owned, nil
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into notified_sscc (tenant_id, partner_id, transaction_set, sscc, legal_entity_id, notice_key)
			select $1::uuid, $2::uuid, $3, unnest($4::text[]), $5::uuid, $6 on conflict do nothing`, tid, partnerID, set, ssccs, legalEntityID, key); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select sscc from notified_sscc where partner_id = $1 and transaction_set = $2 and notice_key = $3 and sscc = any($4)`,
			partnerID, set, key, ssccs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			owned = append(owned, v)
		}
		return rows.Err()
	})
	return owned, err
}
