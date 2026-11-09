package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	ediev "github.com/BenjaminSRussell/passage/services/edi/internal/events"
)

const xrefColumns = `id::text, qualifier, partner_item, product_id::text, site_id::text, warehouse_id::text, version`

func scanXref(r pgx.Row) (domain.ItemXref, error) {
	var x domain.ItemXref
	err := r.Scan(&x.ID, &x.Qualifier, &x.PartnerItem, &x.ProductID, &x.SiteID, &x.WarehouseID, &x.Version)
	return x, err
}

func scopeRefused(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "42501" || pe.Code == "23503") {
		return ErrNotFound
	}
	if isUnique(err) {
		return ErrDuplicate
	}
	return err
}

func (s *Store) CreateXref(ctx context.Context, partnerID, legalEntityID string, x domain.ItemXref) (domain.ItemXref, error) {
	var out domain.ItemXref
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		out, err = scanXref(tx.QueryRow(ctx, `insert into partner_item_xref (tenant_id, id, partner_id, legal_entity_id, qualifier, partner_item,
			product_id, site_id, warehouse_id) values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+xrefColumns,
			tid, ids.New(), partnerID, legalEntityID, x.Qualifier, x.PartnerItem, x.ProductID, x.SiteID, x.WarehouseID))
		return scopeRefused(err)
	})
	return out, err
}

func (s *Store) ListXrefs(ctx context.Context, partnerID string) ([]domain.ItemXref, error) {
	out := []domain.ItemXref{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+xrefColumns+` from partner_item_xref where partner_id = $1 order by qualifier, partner_item`, partnerID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ItemXref, error) { return scanXref(r) })
		return err
	})
	return out, err
}

func (s *Store) UpdateXref(ctx context.Context, partnerID, id string, x domain.ItemXref) (domain.ItemXref, error) {
	var out domain.ItemXref
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanXref(tx.QueryRow(ctx, `update partner_item_xref set qualifier = $3, partner_item = $4, product_id = $5, site_id = $6,
			warehouse_id = $7, version = version + 1, updated_at = now() where id = $1 and partner_id = $2 returning `+xrefColumns,
			id, partnerID, x.Qualifier, x.PartnerItem, x.ProductID, x.SiteID, x.WarehouseID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return scopeRefused(err)
	})
	return out, err
}

func (s *Store) DeleteXref(ctx context.Context, partnerID, id string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from partner_item_xref where id = $1 and partner_id = $2`, id, partnerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// LookupXrefs returns the cross-references of the partner for the given item
// references, keyed by reference; a reference with none is absent.
func (s *Store) LookupXrefs(ctx context.Context, partnerID string, refs []domain.ItemRef) (map[domain.ItemRef]domain.ItemXref, error) {
	quals, items := make([]string, len(refs)), make([]string, len(refs))
	for i, r := range refs {
		quals[i], items[i] = r.Qualifier, r.Value
	}
	out := map[domain.ItemRef]domain.ItemXref{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+xrefColumns+` from partner_item_xref where partner_id = $1
			and (qualifier, partner_item) in (select * from unnest($2::text[], $3::text[]))`, partnerID, quals, items)
		if err != nil {
			return err
		}
		found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ItemXref, error) { return scanXref(r) })
		for _, x := range found {
			out[domain.ItemRef{Qualifier: x.Qualifier, Value: x.PartnerItem}] = x
		}
		return err
	})
	return out, err
}

func (s *Store) PutSalesDefaults(ctx context.Context, partnerID, legalEntityID string, d domain.SalesDefaults) (domain.SalesDefaults, error) {
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into partner_sales_defaults (tenant_id, partner_id, legal_entity_id, invoice_account_id, currency)
			values ($1, $2, $3, $4, $5) on conflict (tenant_id, partner_id) do update set legal_entity_id = excluded.legal_entity_id,
			invoice_account_id = excluded.invoice_account_id, currency = excluded.currency,
			version = partner_sales_defaults.version + 1, updated_at = now()`, tid, partnerID, legalEntityID, d.InvoiceAccountID, d.Currency)
		return scopeRefused(err)
	})
	return d, err
}

func (s *Store) GetSalesDefaults(ctx context.Context, partnerID string) (domain.SalesDefaults, error) {
	var d domain.SalesDefaults
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select invoice_account_id::text, currency from partner_sales_defaults where partner_id = $1`, partnerID).
			Scan(&d.InvoiceAccountID, &d.Currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return d, err
}

// PostState is what posting a message to sales needs besides its record.
type PostState struct {
	MessageRecord
	ParkCode     string
	SalesOrderID string
}

// Postable reports whether the message may be posted (again): it is mapped, or
// parked by an earlier posting attempt. A message that failed mapping is not.
func (p PostState) Postable() bool {
	return p.Status == "MAPPED" || (p.Status == "FAILED" && p.ParkCode != "")
}

func (s *Store) MessageForPost(ctx context.Context, id string) (PostState, error) {
	rec, err := s.GetMessage(ctx, id)
	if err != nil {
		return PostState{}, err
	}
	st := PostState{MessageRecord: rec}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select coalesce(park_code, ''), coalesce(sales_order_id::text, '') from message where id = $1`, id).
			Scan(&st.ParkCode, &st.SalesOrderID)
	})
	return st, err
}

const postableWhere = ` where id = $1 and (status = 'MAPPED' or (status = 'FAILED' and park_code is not null))`

// ParkMessage marks a message that cannot be posted FAILED with the park code
// and publishes message.failed. Parking again with the same reason is a no-op.
func (s *Store) ParkMessage(ctx context.Context, id, code, detail string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		text := code + ": " + detail
		var prev string
		var d ediev.MessageFailedData
		err = tx.QueryRow(ctx, `select coalesce(m.error, ''), i.id::text, i.partner_id::text, i.control_number
			from message m join interchange i on i.tenant_id = m.tenant_id and i.id = m.interchange_id
			where m.id = $1 and (m.status = 'MAPPED' or (m.status = 'FAILED' and m.park_code is not null)) for update of m`, id).
			Scan(&prev, &d.InterchangeID, &d.PartnerID, &d.ControlNumber)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if prev == text {
			return nil
		}
		if err := tx.QueryRow(ctx, `update message set status = 'FAILED', park_code = $2, error = $3`+postableWhere+` returning transaction_set, st_control`,
			id, code, text).Scan(&d.TransactionSet, &d.STControl); err != nil {
			return err
		}
		d.MessageID, d.Error = id, text
		return enqueueFailed(ctx, tx, tid, d)
	})
}

// MarkPosted records the sales order a message became and publishes
// message.posted. A message already POSTED to the same order is left alone.
func (s *Store) MarkPosted(ctx context.Context, id, salesOrderID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var d ediev.MessagePostedData
		err = tx.QueryRow(ctx, `update message set status = 'POSTED', sales_order_id = $2, park_code = null, error = null`+postableWhere+
			` returning transaction_set, st_control, interchange_id::text`, id, salesOrderID).Scan(&d.TransactionSet, &d.STControl, &d.InterchangeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select partner_id::text, control_number from interchange where id = $1`, d.InterchangeID).
			Scan(&d.PartnerID, &d.ControlNumber); err != nil {
			return err
		}
		d.MessageID, d.SalesOrderID = id, salesOrderID
		evt, err := events.New(ediev.MessagePosted, eventSource, id, tid, d)
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "message", id, evt)
	})
}
