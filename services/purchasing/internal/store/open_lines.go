package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// OpenOrderLine is open supply: a line of a CONFIRMED purchase order that still has quantity to
// receive, with where it is delivered. Master planning (wave 22) reads it per warehouse.
type OpenOrderLine struct {
	PurchaseOrderID string          `json:"purchaseOrderId"`
	Number          string          `json:"number"`
	LineNo          int             `json:"lineNo"`
	LegalEntityID   string          `json:"legalEntityId"`
	VendorID        string          `json:"vendorId"`
	ProductID       string          `json:"productId,omitempty"`
	Description     string          `json:"description,omitempty"`
	SiteID          string          `json:"siteId,omitempty"`
	WarehouseID     string          `json:"warehouseId,omitempty"`
	Unit            string          `json:"unit"`
	Qty             decimal.Decimal `json:"qty"`
	ReceivedQty     decimal.Decimal `json:"receivedQty"`
	OpenQty         decimal.Decimal `json:"openQty"`
	DeliveryDate    domain.Day      `json:"deliveryDate"`
	// ConfirmedDate is the vendor's confirmed date of the earliest schedule row when the line has one.
	ConfirmedDate *domain.Day `json:"confirmedDate,omitempty"`
}

// OpenLineFilter narrows ListOpenOrderLines; empty fields do not filter. The warehouse and site are
// the effective destination of the line (its own, else the PO's).
type OpenLineFilter struct {
	LegalEntityID string
	WarehouseID   string
	SiteID        string
	ProductID     string
	DueFrom       *domain.Day
	DueTo         *domain.Day
}

// ListOpenOrderLines pages the open lines of confirmed POs by delivery date, then PO and line number.
// The cursor is "<date>|<po id>|<line no>" of the last row returned.
func (s *Postgres) ListOpenOrderLines(ctx context.Context, f OpenLineFilter, after string, limit int) (Page[OpenOrderLine], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var curDate *time.Time
	var curPO *string
	var curLine *int
	if after != "" {
		parts := strings.Split(after, "|")
		d, derr := domain.ParseDay(parts[0])
		n, nerr := 0, error(nil)
		if len(parts) == 3 {
			n, nerr = strconv.Atoi(parts[2])
		}
		if len(parts) != 3 || derr != nil || nerr != nil || !ids.Valid(parts[1]) {
			return Page[OpenOrderLine]{}, &domain.Error{Code: domain.CodeValidation, Detail: "cursor is not one this list returned"}
		}
		curDate, curPO, curLine = &d.Time, &parts[1], &n
	}
	var from, to *time.Time
	if f.DueFrom != nil {
		from = &f.DueFrom.Time
	}
	if f.DueTo != nil {
		to = &f.DueTo.Time
	}
	var items []OpenOrderLine
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		// The filters compare uuid columns with uuid parameters (NULL = no filter) so the product and
		// legal entity predicates stay index conditions; the effective warehouse and site are the line's
		// own or, when it has none, the PO header's. Legal entities outside the caller's scope are left
		// out by row-level security on purchase_order.
		rows, err := tx.Query(ctx, `select o.id, o.number, l.line_no, o.legal_entity_id, o.vendor_id, coalesce(l.product_id::text, ''),
			l.description, coalesce(coalesce(l.site_id, o.site_id)::text, ''), coalesce(coalesce(l.warehouse_id, o.warehouse_id)::text, ''),
			l.unit, l.qty, l.received_qty, l.qty - l.received_qty, l.delivery_date,
			(select min(ds.confirmed_date) from purchase_delivery_schedule ds
			  where ds.tenant_id = l.tenant_id and ds.purchase_order_id = l.purchase_order_id and ds.line_no = l.line_no
			  and ds.confirmed_date is not null)
			from purchase_order_line l
			join purchase_order o on o.tenant_id = l.tenant_id and o.id = l.purchase_order_id
			where l.tenant_id = $1 and o.approval_status = 'CONFIRMED' and l.status = 'OPEN' and l.qty > l.received_qty
			and ($2::uuid is null or o.legal_entity_id = $2::uuid)
			and ($3::uuid is null or l.warehouse_id = $3::uuid or (l.warehouse_id is null and o.warehouse_id = $3::uuid))
			and ($4::uuid is null or l.site_id = $4::uuid or (l.site_id is null and o.site_id = $4::uuid))
			and ($5::uuid is null or l.product_id = $5::uuid)
			and ($6::date is null or l.delivery_date >= $6::date)
			and ($7::date is null or l.delivery_date <= $7::date)
			and ($8::date is null or (l.delivery_date, l.purchase_order_id, l.line_no) > ($8::date, $9::uuid, $10::int))
			order by l.delivery_date, l.purchase_order_id, l.line_no limit $11`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.WarehouseID), nullable(f.SiteID), nullable(f.ProductID),
			from, to, curDate, curPO, curLine, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (OpenOrderLine, error) {
			var l OpenOrderLine
			var due time.Time
			var confirmed *time.Time
			err := row.Scan(&l.PurchaseOrderID, &l.Number, &l.LineNo, &l.LegalEntityID, &l.VendorID, &l.ProductID, &l.Description,
				&l.SiteID, &l.WarehouseID, &l.Unit, &l.Qty, &l.ReceivedQty, &l.OpenQty, &due, &confirmed)
			l.DeliveryDate = day(due)
			if confirmed != nil {
				d := day(*confirmed)
				l.ConfirmedDate = &d
			}
			return l, err
		})
		return err
	})
	if err != nil {
		return Page[OpenOrderLine]{}, err
	}
	if items == nil {
		items = []OpenOrderLine{}
	}
	return pageOf(items, limit, func(l OpenOrderLine) string {
		return fmt.Sprintf("%s|%s|%d", l.DeliveryDate, l.PurchaseOrderID, l.LineNo)
	}), nil
}
