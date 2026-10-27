package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// SSCCFilter selects the SSCC register (section 5.28.14: prefix, date range,
// customer). CompanyPrefix ("" = any) matches the GS1 company prefix that
// follows the extension digit (BR-28.5). From and To are inclusive UTC dates
// on the container's issue date; a zero time leaves that bound open.
// AfterSSCC is the keyset cursor: only SSCCs that sort after it are returned.
type SSCCFilter struct {
	CompanyPrefix string
	CustomerID    string
	From, To      time.Time
	AfterSSCC     string
	Limit         int
}

// SSCCRow is one issued SSCC with its container and shipment.
type SSCCRow struct {
	SSCC           string    `json:"sscc"`
	ContainerID    string    `json:"containerId"`
	ContainerType  string    `json:"containerType"`
	Status         string    `json:"status"`
	ShipmentID     string    `json:"shipmentId"`
	ShipmentNumber string    `json:"shipmentNumber"`
	OrderID        string    `json:"orderId"`
	CustomerID     string    `json:"customerId"`
	PackingSlipID  *string   `json:"packingSlipId"`
	IssuedAt       time.Time `json:"issuedAt"`
}

func dateParam(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format("2006-01-02")
}

// ListSSCCs returns the SSCC register ordered by SSCC, at most f.Limit rows.
// Containers are read through their shipment, so the tenant and legal-entity
// row-level security of the shipment applies.
func (s *Postgres) ListSSCCs(ctx context.Context, f SSCCFilter) ([]SSCCRow, error) {
	out := []SSCCRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select c.sscc::text, c.id::text, c.container_type, c.status, sh.id::text, sh.shipment_number,
				sh.order_id::text, sh.customer_id::text, c.packing_slip_id::text, c.created_at
			from container c
			join shipment sh on sh.tenant_id = c.tenant_id and sh.id = c.shipment_id
			where ($1::text = '' or substr(c.sscc, 2, char_length($1::text)) = $1::text)
			  and ($2::text = '' or sh.customer_id = nullif($2::text, '')::uuid)
			  and ($3::date is null or c.created_at >= ($3::date)::timestamp at time zone 'UTC')
			  and ($4::date is null or c.created_at < ($4::date + 1)::timestamp at time zone 'UTC')
			  and ($5::text = '' or c.sscc > $5::text::char(18))
			order by c.sscc
			limit $6`,
			f.CompanyPrefix, f.CustomerID, dateParam(f.From), dateParam(f.To), f.AfterSSCC, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r SSCCRow
			if err := rows.Scan(&r.SSCC, &r.ContainerID, &r.ContainerType, &r.Status, &r.ShipmentID, &r.ShipmentNumber,
				&r.OrderID, &r.CustomerID, &r.PackingSlipID, &r.IssuedAt); err != nil {
				return err
			}
			r.IssuedAt = r.IssuedAt.UTC()
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
