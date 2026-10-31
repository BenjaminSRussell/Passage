package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type IntegrityReport struct {
	CustomerRoles            int
	VendorRoles              int
	VendorRolesOwnedByMerged int
	RolesOwnedByMerged       int
	InvoiceRefsToMerged      int
	MergedWithoutRecord      int
	MergeRecordsNotMerged    int
}

func (r IntegrityReport) Clean() bool {
	return r.RolesOwnedByMerged == 0 && r.VendorRolesOwnedByMerged == 0 && r.InvoiceRefsToMerged == 0 && r.MergedWithoutRecord == 0 && r.MergeRecordsNotMerged == 0
}

func (s *Postgres) Audit(ctx context.Context) (IntegrityReport, error) {
	var r IntegrityReport
	p, err := fullScope(ctx)
	if err != nil {
		return r, err
	}
	err = adminTx(ctx, s.pool, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select
			(select count(*) from customer_role where tenant_id = $1),
			(select count(*) from vendor_role where tenant_id = $1),
			(select count(*) from vendor_role v join party p on p.tenant_id = v.tenant_id and p.id = v.party_id
				where v.tenant_id = $1 and p.status = 'merged'),
			(select count(*) from customer_role c join party p on p.tenant_id = c.tenant_id and p.id = c.party_id
				where c.tenant_id = $1 and p.status = 'merged'),
			(select count(*) from customer_role c join party p on p.tenant_id = c.tenant_id and p.id = c.invoice_account_party_id
				where c.tenant_id = $1 and p.status = 'merged'),
			(select count(*) from party p where p.tenant_id = $1 and p.status = 'merged'
				and not exists (select 1 from party_merge m where m.tenant_id = p.tenant_id and m.merged_party_id = p.id
					and m.survivor_party_id = p.merged_into_id)),
			(select count(*) from party_merge m join party p on p.tenant_id = m.tenant_id and p.id = m.merged_party_id
				where m.tenant_id = $1 and p.status <> 'merged')`, p.TenantID).
			Scan(&r.CustomerRoles, &r.VendorRoles, &r.VendorRolesOwnedByMerged, &r.RolesOwnedByMerged, &r.InvoiceRefsToMerged, &r.MergedWithoutRecord, &r.MergeRecordsNotMerged)
	})
	return r, err
}
