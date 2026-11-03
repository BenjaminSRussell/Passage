package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// Error codes of the drop-ship flow.
const (
	// CodeDropShipNotFound: no such drop-ship in the caller's tenant and scope.
	CodeDropShipNotFound = "FUL-0060"
	// CodeDropShipExists: the order line already has an active drop-ship.
	CodeDropShipExists = "FUL-0061"
	// CodeDropShipOnShipment: the order line is on a warehouse shipment that is
	// not cancelled, so it ships from stock and cannot also be drop-shipped.
	CodeDropShipOnShipment = "FUL-0062"
	// CodeDropShipQty: a vendor confirmation names more than the open quantity.
	CodeDropShipQty = "FUL-0063"
	// CodeDropShipShipmentRef: the vendor shipment reference is already booked on the drop-ship.
	CodeDropShipShipmentRef = "FUL-0064"
)

// DropShipStore is the drop-ship flow (FR-28.7): a vendor ships a purchase
// order line straight to the customer and its confirmation books the cost.
type DropShipStore interface {
	CreateDropShip(ctx context.Context, in DropShipInput, idem Idem) (DropShip, error)
	ListDropShips(ctx context.Context, f DropShipFilter) ([]DropShip, error)
	GetDropShip(ctx context.Context, id string) (DropShip, error)
	ConfirmDropShip(ctx context.Context, id string, version int, in DropShipConfirmInput, idem Idem) (DropShip, error)
	CancelDropShip(ctx context.Context, id string, version int, idem Idem) (DropShip, error)
	CloseDropShipRemainder(ctx context.Context, id string, version int, idem Idem) (DropShip, error)
}

// DropShip is a sales order line fulfilled by a vendor, linked to its purchase
// order line. Journal is empty until the vendor confirms.
type DropShip struct {
	ID                  string          `json:"id"`
	Number              string          `json:"dropShipNumber"`
	Status              string          `json:"status"`
	LegalEntityID       string          `json:"legalEntityId"`
	OrderID             string          `json:"orderId"`
	OrderLineID         string          `json:"orderLineId"`
	CustomerID          string          `json:"customerId"`
	ProductID           string          `json:"productId"`
	Unit                string          `json:"unit"`
	Qty                 decimal.Decimal `json:"qty"`
	QtyConfirmed        decimal.Decimal `json:"qtyConfirmed"`
	QtyClosed           decimal.Decimal `json:"qtyClosed"`
	QtyOpen             decimal.Decimal `json:"qtyOpen"`
	VendorID            string          `json:"vendorId"`
	PurchaseOrderID     string          `json:"purchaseOrderId"`
	PurchaseOrderLineID string          `json:"purchaseOrderLineId"`
	UnitCost            decimal.Decimal `json:"unitCost"`
	Currency            string          `json:"currency"`
	VendorShipmentRef   *string         `json:"vendorShipmentRef"`
	ShippedOn           *time.Time      `json:"shippedOn"`
	PostingDate         *time.Time      `json:"postingDate"`
	Journal             []JournalEntry  `json:"journal"`
	// Confirmations are the vendor's shipments in order; filled by GetDropShip only.
	Confirmations []DropShipConfirmation `json:"confirmations"`
	Version       int                    `json:"version"`
	CreatedAt     time.Time              `json:"createdAt"`
}

// DropShipConfirmation is one vendor shipment of a drop-ship with the journal it booked.
type DropShipConfirmation struct {
	Seq               int             `json:"seq"`
	Qty               decimal.Decimal `json:"qty"`
	VendorShipmentRef *string         `json:"vendorShipmentRef"`
	ShippedOn         time.Time       `json:"shippedOn"`
	PostingDate       time.Time       `json:"postingDate"`
	Journal           []JournalEntry  `json:"journal"`
	CreatedAt         time.Time       `json:"createdAt"`
}

// DropShipInput is a new drop-ship; UnitCost is the purchase order cost.
type DropShipInput struct {
	LegalEntityID, OrderID, OrderLineID, CustomerID, ProductID, Unit string
	Qty                                                              decimal.Decimal
	VendorID, PurchaseOrderID, PurchaseOrderLineID                   string
	UnitCost                                                         decimal.Decimal
	Currency                                                         string
}

// DropShipConfirmInput is the vendor's ship confirmation. PostingDate is the
// day the cost is booked (the ship date when the caller names none). Qty is the
// quantity of this vendor shipment; nil confirms everything still open.
type DropShipConfirmInput struct {
	Qty               *decimal.Decimal
	VendorShipmentRef string
	ShippedOn         time.Time
	PostingDate       *time.Time
}

// DropShipFilter narrows the list; empty fields match everything.
type DropShipFilter struct {
	Status, OrderID, VendorID string
	Limit                     int
}

const dropShipColumns = `id, drop_ship_number, status, legal_entity_id, order_id, order_line_id, customer_id, product_id, unit, qty, qty_confirmed, qty_closed, vendor_id,
	purchase_order_id, purchase_order_line_id, unit_cost, currency, vendor_shipment_ref, shipped_on, posting_date, journal, version, created_at`

func scanDropShip(row pgx.Row) (DropShip, error) {
	var d DropShip
	var journal []byte
	err := row.Scan(&d.ID, &d.Number, &d.Status, &d.LegalEntityID, &d.OrderID, &d.OrderLineID, &d.CustomerID, &d.ProductID, &d.Unit, &d.Qty, &d.QtyConfirmed, &d.QtyClosed, &d.VendorID,
		&d.PurchaseOrderID, &d.PurchaseOrderLineID, &d.UnitCost, &d.Currency, &d.VendorShipmentRef, &d.ShippedOn, &d.PostingDate, &journal, &d.Version, &d.CreatedAt)
	if err != nil {
		return d, err
	}
	d.Journal = []JournalEntry{}
	d.Confirmations = []DropShipConfirmation{}
	d.QtyOpen = domain.DropShipOpenQty(d.Qty, d.QtyConfirmed, d.QtyClosed)
	if d.Status == domain.DropShipCancelled {
		d.QtyOpen = decimal.Zero
	}
	return d, json.Unmarshal(journal, &d.Journal)
}

func dropShipData(d DropShip) fulevents.DropShipData {
	out := fulevents.DropShipData{DropShipID: d.ID, DropShipNumber: d.Number, Status: d.Status, LegalEntityID: d.LegalEntityID, OrderID: d.OrderID,
		OrderLineID: d.OrderLineID, CustomerID: d.CustomerID, ProductID: d.ProductID, Unit: d.Unit, Qty: d.Qty.String(), VendorID: d.VendorID,
		PurchaseOrderID: d.PurchaseOrderID, PurchaseOrderLine: d.PurchaseOrderLineID, UnitCost: d.UnitCost.String(), Currency: d.Currency,
		QtyConfirmed: d.QtyConfirmed.String(), QtyClosed: d.QtyClosed.String(), Journal: []fulevents.JournalEntry{}}
	if d.VendorShipmentRef != nil {
		out.VendorShipmentRef = *d.VendorShipmentRef
	}
	if d.ShippedOn != nil {
		out.ShippedOn = d.ShippedOn.Format("2006-01-02")
	}
	if d.PostingDate != nil {
		out.PostingDate = d.PostingDate.Format("2006-01-02")
	}
	return out
}

func dropShipNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeDropShipNotFound, "drop-ship not found")
}

// CreateDropShip records a drop-ship for an order line against its purchase
// order line. An order line has at most one active (not cancelled) drop-ship,
// FUL-0061 otherwise, and a line already on a warehouse shipment that is not
// cancelled is refused with FUL-0062 (release skips drop-shipped lines the same
// way). dropship.created is written in the same transaction.
func (s *Postgres) CreateDropShip(ctx context.Context, in DropShipInput, idem Idem) (DropShip, error) {
	p, _ := tenant.FromContext(ctx)
	var out DropShip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (DropShip, error) {
			var onShipment bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from shipment_line sl join shipment s on s.tenant_id = sl.tenant_id and s.id = sl.shipment_id
				where sl.order_line_id = $1 and s.status <> 'CANCELLED')`, in.OrderLineID).Scan(&onShipment); err != nil {
				return DropShip{}, err
			}
			if onShipment {
				return DropShip{}, httpx.NewProblem(http.StatusConflict, CodeDropShipOnShipment, "the order line is on a warehouse shipment; cancel the shipment before drop-shipping it")
			}
			if err := checkDropShipKept(ctx, tx, in.OrderLineID, in.Qty); err != nil {
				return DropShip{}, err
			}
			seq, err := nextNumber(ctx, tx, p.TenantID, "DS:"+in.LegalEntityID)
			if err != nil {
				return DropShip{}, err
			}
			id := ids.New()
			d, err := scanDropShip(tx.QueryRow(ctx, `insert into drop_ship (tenant_id, id, drop_ship_number, legal_entity_id, order_id, order_line_id, customer_id, product_id, unit, qty,
					vendor_id, purchase_order_id, purchase_order_line_id, unit_cost, currency)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) returning `+dropShipColumns,
				p.TenantID, id, fmt.Sprintf("DS-%07d", seq), in.LegalEntityID, in.OrderID, in.OrderLineID, in.CustomerID, in.ProductID, in.Unit, in.Qty,
				in.VendorID, in.PurchaseOrderID, in.PurchaseOrderLineID, in.UnitCost, in.Currency))
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				switch pgErr.Code {
				case "23505":
					return DropShip{}, httpx.NewProblem(http.StatusConflict, CodeDropShipExists, "the order line already has an active drop-ship; cancel it first")
				case "42501":
					return DropShip{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLegalEntityScope, "the legal entity is outside your legal-entity scope")
				}
			}
			if err != nil {
				return DropShip{}, err
			}
			return d, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipCreated, p.TenantID, dropShipData(d))
		})
		out = res
		return err
	})
	return out, err
}

// ListDropShips lists drop-ships newest first.
func (s *Postgres) ListDropShips(ctx context.Context, f DropShipFilter) ([]DropShip, error) {
	out := []DropShip{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+dropShipColumns+` from drop_ship
			where ($1 = '' or status = $1) and ($2 = '' or order_id::text = $2) and ($3 = '' or vendor_id::text = $3)
			order by created_at desc, drop_ship_number desc limit $4`, f.Status, f.OrderID, f.VendorID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDropShip(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDropShip returns one drop-ship, FUL-0060 when it is not visible.
func (s *Postgres) GetDropShip(ctx context.Context, id string) (DropShip, error) {
	var d DropShip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		d, err = scanDropShip(tx.QueryRow(ctx, `select `+dropShipColumns+` from drop_ship where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return dropShipNotFound()
		}
		if err != nil {
			return err
		}
		d.Confirmations, err = dropShipConfirmations(ctx, tx, id)
		return err
	})
	return d, err
}

// dropShipConfirmations reads the vendor shipments of a drop-ship in order.
func dropShipConfirmations(ctx context.Context, tx pgx.Tx, id string) ([]DropShipConfirmation, error) {
	rows, err := tx.Query(ctx, `select seq, qty, vendor_shipment_ref, shipped_on, posting_date, journal, created_at
		from drop_ship_confirmation where drop_ship_id = $1 order by seq`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DropShipConfirmation{}
	for rows.Next() {
		var c DropShipConfirmation
		var journal []byte
		if err := rows.Scan(&c.Seq, &c.Qty, &c.VendorShipmentRef, &c.ShippedOn, &c.PostingDate, &journal, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Journal = []JournalEntry{}
		if err := json.Unmarshal(journal, &c.Journal); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// lockDropShip reads a drop-ship FOR UPDATE and checks the version the caller
// read. The caller checks the status: a vendor confirmation books a journal and
// there is no reversal (see NOT-IMPLEMENTED).
func lockDropShip(ctx context.Context, tx pgx.Tx, id string, version int) (DropShip, error) {
	d, err := scanDropShip(tx.QueryRow(ctx, `select `+dropShipColumns+` from drop_ship where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, dropShipNotFound()
	}
	if err != nil {
		return d, err
	}
	if d.Version != version {
		return d, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, "drop-ship changed since it was read; reload and retry")
	}
	return d, nil
}

func dropShipStatusProblem(status, detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus, "drop-ship is "+status+"; "+detail)
}

// ConfirmDropShip is the vendor's ship confirmation (AC-28.5): it books
// Dr 5000 / Cr 2110 at the purchase order cost for the quantity of this vendor
// shipment (all the open quantity when Qty is nil), with no inventory
// transaction. A vendor may ship in parts: the drop-ship is PARTIALLY_CONFIRMED
// while quantity is open and CONFIRMED when none is left. The journal of the
// confirmation is stored and carried by dropship.confirmed, written in the same
// transaction. A stale version is refused (412), a drop-ship that is not OPEN or
// PARTIALLY_CONFIRMED (FUL-0001), a quantity above the open quantity (FUL-0063)
// and a vendor shipment reference already booked on it (FUL-0064).
func (s *Postgres) ConfirmDropShip(ctx context.Context, id string, version int, in DropShipConfirmInput, idem Idem) (DropShip, error) {
	p, _ := tenant.FromContext(ctx)
	var out DropShip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (DropShip, error) {
			d, err := lockDropShip(ctx, tx, id, version)
			if err != nil {
				return d, err
			}
			switch d.Status {
			case domain.DropShipOpen, domain.DropShipPartial:
			case domain.DropShipConfirmed:
				return d, dropShipStatusProblem(d.Status, "the vendor shipment is booked, so it cannot be cancelled or confirmed again")
			default:
				return d, dropShipStatusProblem(d.Status, "only an OPEN or PARTIALLY_CONFIRMED drop-ship can be confirmed, create a new one for the order line")
			}
			qty := d.QtyOpen
			if in.Qty != nil {
				qty = *in.Qty
			}
			if !qty.IsPositive() || qty.GreaterThan(d.QtyOpen) {
				return d, httpx.NewProblem(http.StatusUnprocessableEntity, CodeDropShipQty,
					fmt.Sprintf("the confirmation names %s %s but %s are open on the drop-ship", qty.String(), d.Unit, d.QtyOpen.String()))
			}
			if err := checkDropShipKept(ctx, tx, d.OrderLineID, d.QtyConfirmed.Add(qty)); err != nil {
				return d, err
			}
			journal, err := domain.DropShipPosting(d.OrderLineID, d.QtyConfirmed, qty, d.UnitCost, d.Currency)
			if err != nil {
				return d, mapDomain(err)
			}
			posting := dateOnly(in.ShippedOn)
			if in.PostingDate != nil {
				posting = dateOnly(*in.PostingDate)
			}
			entries := toEntries(journal)
			raw, err := json.Marshal(entries)
			if err != nil {
				return d, err
			}
			var seq int
			if err := tx.QueryRow(ctx, `select coalesce(max(seq), 0) + 1 from drop_ship_confirmation where drop_ship_id = $1`, id).Scan(&seq); err != nil {
				return d, err
			}
			if _, err := tx.Exec(ctx, `insert into drop_ship_confirmation (tenant_id, id, drop_ship_id, legal_entity_id, seq, qty, vendor_shipment_ref, shipped_on, posting_date, journal)
				values ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8, $9, $10)`,
				p.TenantID, ids.New(), id, d.LegalEntityID, seq, qty, in.VendorShipmentRef, dateOnly(in.ShippedOn), posting, raw); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "drop_ship_confirmation_ref" {
					return d, httpx.NewProblem(http.StatusConflict, CodeDropShipShipmentRef, "the vendor shipment reference is already booked on this drop-ship")
				}
				return d, err
			}
			confirmed := d.QtyConfirmed.Add(qty)
			d, err = scanDropShip(tx.QueryRow(ctx, `update drop_ship set status = $2, qty_confirmed = $3, vendor_shipment_ref = nullif($4, ''), shipped_on = $5, posting_date = $6,
					journal = journal || $7::jsonb, version = version + 1, updated_at = now() where id = $1 returning `+dropShipColumns,
				id, domain.DropShipStatusAfter(d.Qty, confirmed, d.QtyClosed), confirmed, in.VendorShipmentRef, dateOnly(in.ShippedOn), posting, raw))
			if err != nil {
				return d, err
			}
			data := dropShipData(d)
			data.ConfirmationSeq, data.ConfirmedQty, data.Journal = seq, qty.String(), journalData(entries)
			return d, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipConfirmed, p.TenantID, data)
		})
		out = res
		return err
	})
	return out, err
}

// CancelDropShip cancels an OPEN drop-ship (nothing was booked); the order line
// can then be drop-shipped again. dropship.cancelled is written in the same transaction.
func (s *Postgres) CancelDropShip(ctx context.Context, id string, version int, idem Idem) (DropShip, error) {
	p, _ := tenant.FromContext(ctx)
	var out DropShip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (DropShip, error) {
			d, err := lockDropShip(ctx, tx, id, version)
			if err != nil {
				return d, err
			}
			switch d.Status {
			case domain.DropShipOpen:
			case domain.DropShipConfirmed:
				return d, dropShipStatusProblem(d.Status, "the vendor shipment is booked, so it cannot be cancelled or confirmed again")
			case domain.DropShipPartial:
				return d, dropShipStatusProblem(d.Status, "part of the vendor shipment is booked, so it cannot be cancelled; close the remainder instead")
			default:
				return d, dropShipStatusProblem(d.Status, "only an OPEN drop-ship can be cancelled, create a new one for the order line")
			}
			d, err = scanDropShip(tx.QueryRow(ctx, `update drop_ship set status = 'CANCELLED', version = version + 1, updated_at = now() where id = $1 returning `+dropShipColumns, id))
			if err != nil {
				return d, err
			}
			return d, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipCancelled, p.TenantID, dropShipData(d))
		})
		out = res
		return err
	})
	return out, err
}

// CloseDropShipRemainder ends a PARTIALLY_CONFIRMED drop-ship whose vendor will
// not ship the rest: the open quantity moves to qty_closed, the drop-ship is
// CONFIRMED and nothing is booked for the closed quantity. dropship.closed is
// written in the same transaction. A drop-ship with nothing confirmed is
// cancelled instead (FUL-0001).
func (s *Postgres) CloseDropShipRemainder(ctx context.Context, id string, version int, idem Idem) (DropShip, error) {
	p, _ := tenant.FromContext(ctx)
	var out DropShip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (DropShip, error) {
			d, err := lockDropShip(ctx, tx, id, version)
			if err != nil {
				return d, err
			}
			switch d.Status {
			case domain.DropShipPartial:
			case domain.DropShipOpen:
				return d, dropShipStatusProblem(d.Status, "the vendor has shipped nothing yet, cancel it instead of closing the remainder")
			default:
				return d, dropShipStatusProblem(d.Status, "only a PARTIALLY_CONFIRMED drop-ship has a remainder to close")
			}
			d, err = scanDropShip(tx.QueryRow(ctx, `update drop_ship set status = 'CONFIRMED', qty_closed = qty_closed + $2, closed_at = now(), version = version + 1, updated_at = now()
				where id = $1 returning `+dropShipColumns, id, d.QtyOpen))
			if err != nil {
				return d, err
			}
			return d, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipClosed, p.TenantID, dropShipData(d))
		})
		out = res
		return err
	})
	return out, err
}
