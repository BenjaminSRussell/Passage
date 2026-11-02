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
)

// DropShipStore is the drop-ship flow (FR-28.7): a vendor ships a purchase
// order line straight to the customer and its confirmation books the cost.
type DropShipStore interface {
	CreateDropShip(ctx context.Context, in DropShipInput, idem Idem) (DropShip, error)
	ListDropShips(ctx context.Context, f DropShipFilter) ([]DropShip, error)
	GetDropShip(ctx context.Context, id string) (DropShip, error)
	ConfirmDropShip(ctx context.Context, id string, version int, in DropShipConfirmInput, idem Idem) (DropShip, error)
	CancelDropShip(ctx context.Context, id string, version int, idem Idem) (DropShip, error)
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
	VendorID            string          `json:"vendorId"`
	PurchaseOrderID     string          `json:"purchaseOrderId"`
	PurchaseOrderLineID string          `json:"purchaseOrderLineId"`
	UnitCost            decimal.Decimal `json:"unitCost"`
	Currency            string          `json:"currency"`
	VendorShipmentRef   *string         `json:"vendorShipmentRef"`
	ShippedOn           *time.Time      `json:"shippedOn"`
	PostingDate         *time.Time      `json:"postingDate"`
	Journal             []JournalEntry  `json:"journal"`
	Version             int             `json:"version"`
	CreatedAt           time.Time       `json:"createdAt"`
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
// day the cost is booked (the ship date when the caller names none).
type DropShipConfirmInput struct {
	VendorShipmentRef string
	ShippedOn         time.Time
	PostingDate       *time.Time
}

// DropShipFilter narrows the list; empty fields match everything.
type DropShipFilter struct {
	Status, OrderID, VendorID string
	Limit                     int
}

const dropShipColumns = `id, drop_ship_number, status, legal_entity_id, order_id, order_line_id, customer_id, product_id, unit, qty, vendor_id,
	purchase_order_id, purchase_order_line_id, unit_cost, currency, vendor_shipment_ref, shipped_on, posting_date, journal, version, created_at`

func scanDropShip(row pgx.Row) (DropShip, error) {
	var d DropShip
	var journal []byte
	err := row.Scan(&d.ID, &d.Number, &d.Status, &d.LegalEntityID, &d.OrderID, &d.OrderLineID, &d.CustomerID, &d.ProductID, &d.Unit, &d.Qty, &d.VendorID,
		&d.PurchaseOrderID, &d.PurchaseOrderLineID, &d.UnitCost, &d.Currency, &d.VendorShipmentRef, &d.ShippedOn, &d.PostingDate, &journal, &d.Version, &d.CreatedAt)
	if err != nil {
		return d, err
	}
	d.Journal = []JournalEntry{}
	return d, json.Unmarshal(journal, &d.Journal)
}

func dropShipData(d DropShip) fulevents.DropShipData {
	out := fulevents.DropShipData{DropShipID: d.ID, DropShipNumber: d.Number, Status: d.Status, LegalEntityID: d.LegalEntityID, OrderID: d.OrderID,
		OrderLineID: d.OrderLineID, CustomerID: d.CustomerID, ProductID: d.ProductID, Unit: d.Unit, Qty: d.Qty.String(), VendorID: d.VendorID,
		PurchaseOrderID: d.PurchaseOrderID, PurchaseOrderLine: d.PurchaseOrderLineID, UnitCost: d.UnitCost.String(), Currency: d.Currency,
		Journal: journalData(d.Journal)}
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
		return err
	})
	return d, err
}

// lockDropShip reads a drop-ship FOR UPDATE and checks the version the caller
// read and that it is OPEN. A confirmed drop-ship is final: its journal is
// booked and there is no reversal (see NOT-IMPLEMENTED).
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
	switch d.Status {
	case domain.DropShipOpen:
	case domain.DropShipConfirmed:
		return d, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus,
			"drop-ship is CONFIRMED: the vendor shipment is booked, so it cannot be cancelled or confirmed again")
	default:
		return d, httpx.NewProblem(http.StatusUnprocessableEntity, CodeStatus,
			"drop-ship is "+d.Status+"; only an OPEN drop-ship can change, create a new one for the order line")
	}
	return d, nil
}

// ConfirmDropShip is the vendor's ship confirmation (AC-28.5): the drop-ship
// becomes CONFIRMED and books Dr 5000 / Cr 2110 at the purchase order cost,
// with no inventory transaction. The journal is stored and carried by
// dropship.confirmed, written in the same transaction. A stale version is
// refused (412) and so is a drop-ship that is not OPEN (FUL-0001).
func (s *Postgres) ConfirmDropShip(ctx context.Context, id string, version int, in DropShipConfirmInput, idem Idem) (DropShip, error) {
	p, _ := tenant.FromContext(ctx)
	var out DropShip
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (DropShip, error) {
			d, err := lockDropShip(ctx, tx, id, version)
			if err != nil {
				return d, err
			}
			journal, err := domain.DropShipPosting(d.OrderLineID, d.Qty, d.UnitCost, d.Currency)
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
			d, err = scanDropShip(tx.QueryRow(ctx, `update drop_ship set status = 'CONFIRMED', vendor_shipment_ref = nullif($2, ''), shipped_on = $3, posting_date = $4,
					journal = $5, version = version + 1, updated_at = now() where id = $1 returning `+dropShipColumns,
				id, in.VendorShipmentRef, dateOnly(in.ShippedOn), posting, raw))
			if err != nil {
				return d, err
			}
			return d, enqueueFor(ctx, tx, "drop_ship", d.ID, fulevents.DropShipConfirmed, p.TenantID, dropShipData(d))
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
