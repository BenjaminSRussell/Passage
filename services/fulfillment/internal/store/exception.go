package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
)

// Exception kinds: a case fulfillment records for the warehouse supervisor because
// it cannot fix it alone.
const (
	// ExceptionSlipPostedBeyondKept: a packing slip shipped more of an order line than
	// sales kept when it cancelled the remainder (the slip was posted before the
	// cancellation arrived). The goods left; the remedy is a return (wave 30) or a new order.
	ExceptionSlipPostedBeyondKept = "SLIP_POSTED_BEYOND_KEPT"
	// ExceptionPackedBeyondKept: packed, unposted quantity beyond the kept quantity. The
	// post is refused (FUL-0066); the supervisor unpacks and puts the goods back.
	ExceptionPackedBeyondKept = "PACKED_BEYOND_KEPT"
)

// Catalog codes of the remainder-cancelled slice.
const (
	CodeKeptExceeded      = "FUL-0066"
	CodeExceptionNotFound = "FUL-0067"
	CodeExceptionResolved = "FUL-0068"
)

// Exception is one case on the supervisor's list.
type Exception struct {
	ID             string     `json:"id"`
	LegalEntityID  string     `json:"legalEntityId"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	OrderID        string     `json:"orderId"`
	OrderLineID    string     `json:"orderLineId"`
	ShipmentID     *string    `json:"shipmentId"`
	PackingSlipID  *string    `json:"packingSlipId"`
	Qty            string     `json:"qty"`
	KeptQty        string     `json:"keptQty"`
	Detail         string     `json:"detail"`
	Version        int        `json:"version"`
	CreatedAt      time.Time  `json:"createdAt"`
	ResolvedAt     *time.Time `json:"resolvedAt"`
	ResolvedBy     *string    `json:"resolvedBy"`
	ResolutionNote *string    `json:"resolutionNote"`
}

// ExceptionFilter narrows the exception list; empty Status lists both.
type ExceptionFilter struct {
	Status string
	Limit  int
}

// ExceptionStore is the supervisor's exception list.
type ExceptionStore interface {
	ListExceptions(ctx context.Context, f ExceptionFilter) ([]Exception, error)
	GetException(ctx context.Context, id string) (Exception, error)
	ResolveException(ctx context.Context, id string, version int, note string, idem Idem) (Exception, error)
}

const exceptionCols = `id, legal_entity_id, kind, status, order_id, order_line_id, shipment_id, packing_slip_id, qty::text, kept_qty::text, detail, version, created_at, resolved_at, resolved_by, resolution_note`

func scanException(row pgx.Row) (Exception, error) {
	var e Exception
	err := row.Scan(&e.ID, &e.LegalEntityID, &e.Kind, &e.Status, &e.OrderID, &e.OrderLineID, &e.ShipmentID, &e.PackingSlipID, &e.Qty, &e.KeptQty,
		&e.Detail, &e.Version, &e.CreatedAt, &e.ResolvedAt, &e.ResolvedBy, &e.ResolutionNote)
	return e, err
}

func exceptionNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeExceptionNotFound, "exception not found")
}

// ListExceptions lists exceptions, open ones first and newest first within a status.
func (s *Postgres) ListExceptions(ctx context.Context, f ExceptionFilter) ([]Exception, error) {
	out := []Exception{}
	limit := f.Limit
	if limit < 1 || limit > 500 {
		limit = 100
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+exceptionCols+` from fulfillment_exception
			where ($1 = '' or status = $1) order by (status = 'OPEN') desc, created_at desc, id limit $2`, f.Status, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanException(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// GetException returns one exception.
func (s *Postgres) GetException(ctx context.Context, id string) (Exception, error) {
	var e Exception
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		e, err = scanException(tx.QueryRow(ctx, `select `+exceptionCols+` from fulfillment_exception where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return exceptionNotFound()
		}
		return err
	})
	return e, err
}

// ResolveException closes an exception with the supervisor's note. The version must match; an
// exception that is already resolved is refused (FUL-0068).
func (s *Postgres) ResolveException(ctx context.Context, id string, version int, note string, idem Idem) (Exception, error) {
	p, _ := tenant.FromContext(ctx)
	var out Exception
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		res, err := runIdempotent(ctx, tx, p.TenantID, idem, func() (Exception, error) {
			e, err := scanException(tx.QueryRow(ctx, `select `+exceptionCols+` from fulfillment_exception where id = $1 for update`, id))
			if errors.Is(err, pgx.ErrNoRows) {
				return e, exceptionNotFound()
			}
			if err != nil {
				return e, err
			}
			if e.Status == "RESOLVED" {
				return e, httpx.NewProblem(http.StatusConflict, CodeExceptionResolved, "the exception is already resolved")
			}
			if e.Version != version {
				return e, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersion, fmt.Sprintf("exception is at version %d, request sent %d", e.Version, version))
			}
			e, err = scanException(tx.QueryRow(ctx, `update fulfillment_exception set status = 'RESOLVED', version = version + 1, resolved_at = now(),
				resolved_by = $2, resolution_note = $3 where id = $1 returning `+exceptionCols, id, p.UserID, note))
			if err != nil {
				return e, err
			}
			return e, enqueueFor(ctx, tx, "exception", e.ID, fulevents.ExceptionResolved, p.TenantID, fulevents.ExceptionResolvedData{
				ExceptionID: e.ID, Kind: e.Kind, OrderID: e.OrderID, OrderLineID: e.OrderLineID, Note: note, ResolvedBy: p.UserID})
		})
		out = res
		return err
	})
	return out, err
}

// raiseException records one exception inside tx (the consumer's inbox transaction) and
// publishes exception.raised. A second record of the same case (same event, kind, order line
// and shipment) changes nothing.
func raiseException(ctx context.Context, tx pgx.Tx, tenantID string, e Exception, eventID string, qty, kept decimal.Decimal) (bool, error) {
	e.ID = ids.New()
	tag, err := tx.Exec(ctx, `insert into fulfillment_exception (tenant_id, id, legal_entity_id, kind, order_id, order_line_id, shipment_id, packing_slip_id, qty, kept_qty, detail, source_event_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) on conflict do nothing`,
		tenantID, e.ID, e.LegalEntityID, e.Kind, e.OrderID, e.OrderLineID, e.ShipmentID, e.PackingSlipID, qty, kept, e.Detail, eventID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	return true, enqueueFor(ctx, tx, "exception", e.ID, fulevents.ExceptionRaised, tenantID, fulevents.ExceptionRaisedData{
		ExceptionID: e.ID, Kind: e.Kind, OrderID: e.OrderID, OrderLineID: e.OrderLineID, ShipmentID: e.ShipmentID, PackingSlipID: e.PackingSlipID,
		Qty: qty.String(), KeptQty: kept.String(), Detail: e.Detail})
}
