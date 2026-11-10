package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var ErrAsnNotFound = errors.New("advance ship notice not found")

const asnTrackingConstraint = "receiving_asn_tracking_ux"

type Asn struct {
	ID              string
	LegalEntityID   string
	VendorID        string
	PurchaseOrderID string
	Status          string
	Carrier         string
	TrackingNumber  string
	ShipDate        time.Time
	ExpectedArrival time.Time
	Lines           []domain.AsnLine
	CreatedBy       string
	CreatedAt       time.Time
	ReceiptID       string
}

type AsnFilter struct {
	VendorID        string
	PurchaseOrderID string
	CursorCreatedAt time.Time
	CursorID        string
	Limit           int
}

func AsnTrackingKey(tracking string) string { return strings.ToLower(strings.TrimSpace(tracking)) }

func AsnCommitted(holding []Asn) map[int]decimal.Decimal {
	committed := map[int]decimal.Decimal{}
	for _, a := range holding {
		for _, l := range a.Lines {
			committed[l.POLineNo] = committed[l.POLineNo].Add(l.Qty)
		}
	}
	return committed
}

func CheckAsnLimits(in Asn, holding []Asn, limits map[int]decimal.Decimal) error {
	committed := AsnCommitted(holding)
	added := map[int]decimal.Decimal{}
	for _, l := range in.Lines {
		added[l.POLineNo] = added[l.POLineNo].Add(l.Qty)
	}
	for _, l := range in.Lines {
		remaining := decimal.Max(limits[l.POLineNo].Sub(committed[l.POLineNo]), decimal.Zero)
		if added[l.POLineNo].GreaterThan(remaining) {
			return &domain.Error{Code: domain.CodeAsnOverQty, Detail: fmt.Sprintf("ASN quantity %s exceeds remaining %s plus tolerance on purchase order line %d", added[l.POLineNo], remaining, l.POLineNo)}
		}
	}
	return nil
}

func AsnOpenQuantities(ordered, received map[int]decimal.Decimal) map[int]decimal.Decimal {
	open := make(map[int]decimal.Decimal, len(ordered))
	for no, qty := range ordered {
		open[no] = decimal.Max(qty.Sub(received[no]), decimal.Zero)
	}
	return open
}

func AsnTrackingTaken(tracking, otherID string) error {
	return &domain.Error{Code: domain.CodeAsnTracking, Detail: fmt.Sprintf("Tracking number %s already used on ASN %s", tracking, otherID)}
}

func AsnConsumed(a Asn) error {
	return &domain.Error{Code: domain.CodeAsnConsumed, Detail: fmt.Sprintf("ASN %s already started receipt %s", a.ID, a.ReceiptID)}
}

const asnColumns = `a.id::text, a.legal_entity_id::text, a.vendor_id::text, a.purchase_order_id::text, a.status, COALESCE(a.carrier, ''), COALESCE(a.tracking_number, ''),
	a.ship_date, a.expected_arrival, a.lines, a.created_by, a.created_at, COALESCE(a.receipt_id::text, '')`

func scanAsn(row pgx.Row) (Asn, error) {
	var a Asn
	var ship, arrival *time.Time
	var lines []byte
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.VendorID, &a.PurchaseOrderID, &a.Status, &a.Carrier, &a.TrackingNumber, &ship, &arrival, &lines, &a.CreatedBy, &a.CreatedAt, &a.ReceiptID); err != nil {
		return Asn{}, err
	}
	if ship != nil {
		a.ShipDate = *ship
	}
	if arrival != nil {
		a.ExpectedArrival = *arrival
	}
	return a, json.Unmarshal(lines, &a.Lines)
}

func asnsHoldingQuantity(ctx context.Context, tx pgx.Tx, tenantID, purchaseOrderID string) ([]Asn, error) {
	rows, err := tx.Query(ctx, `SELECT `+asnColumns+` FROM receiving_asn a WHERE a.tenant_id = $1 AND a.purchase_order_id = $2 AND (a.status = 'OPEN'
		OR EXISTS (SELECT 1 FROM receiving_receipt r WHERE r.tenant_id = a.tenant_id AND r.id = a.receipt_id AND r.state = 'ARRIVED'))`, tenantID, purchaseOrderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Asn
	for rows.Next() {
		a, err := scanAsn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Postgres) CreateAsn(ctx context.Context, in Asn, ordered map[int]decimal.Decimal) (Asn, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Asn{}, err
	}
	lines, err := json.Marshal(in.Lines)
	if err != nil {
		return Asn{}, err
	}
	in.TrackingNumber = strings.TrimSpace(in.TrackingNumber)
	key := AsnTrackingKey(in.TrackingNumber)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(tenantID+"|asns|"+in.PurchaseOrderID)); err != nil {
			return err
		}
		holding, err := asnsHoldingQuantity(ctx, tx, tenantID, in.PurchaseOrderID)
		if err != nil {
			return err
		}
		received, err := receivedOnOrder(ctx, tx, tenantID, in.PurchaseOrderID)
		if err != nil {
			return err
		}
		if err := CheckAsnLimits(in, holding, AsnOpenQuantities(ordered, received)); err != nil {
			return err
		}
		if key != "" {
			var other string
			err := tx.QueryRow(ctx, `SELECT id::text FROM receiving_asn WHERE tenant_id = $1 AND legal_entity_id = $2 AND vendor_id = $3 AND tracking_key = $4`, tenantID, in.LegalEntityID, in.VendorID, key).Scan(&other)
			if err == nil {
				return AsnTrackingTaken(in.TrackingNumber, other)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO receiving_asn (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, status, carrier, tracking_number, tracking_key, ship_date, expected_arrival, lines, created_by)
			VALUES ($1, $2, $3, $4, $5, 'OPEN', NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, $12) RETURNING created_at`,
			tenantID, in.ID, in.LegalEntityID, in.VendorID, in.PurchaseOrderID, in.Carrier, in.TrackingNumber, key, nullTime(in.ShipDate), nullTime(in.ExpectedArrival), lines, in.CreatedBy).Scan(&in.CreatedAt)
	})
	if isUnique(err, asnTrackingConstraint) {
		return Asn{}, AsnTrackingTaken(in.TrackingNumber, "of another purchase order")
	}
	in.Status = domain.AsnOpen
	return in, err
}

func (s *Postgres) GetAsn(ctx context.Context, id string) (Asn, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Asn{}, err
	}
	var out Asn
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAsn(tx.QueryRow(ctx, `SELECT `+asnColumns+` FROM receiving_asn a WHERE a.tenant_id = $1 AND a.id = $2`, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Asn{}, ErrAsnNotFound
	}
	return out, err
}

func (s *Postgres) ListAsns(ctx context.Context, f AsnFilter) ([]Asn, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	var cursorID any
	if !f.CursorCreatedAt.IsZero() {
		cursorID = f.CursorID
	}
	var out []Asn
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+asnColumns+` FROM receiving_asn a
			WHERE a.tenant_id = $1 AND ($2 = '' OR a.vendor_id = $2::uuid) AND ($3 = '' OR a.purchase_order_id = $3::uuid)
			AND ($4::timestamptz IS NULL OR (a.created_at, a.id) < ($4::timestamptz, $5::uuid))
			ORDER BY a.created_at DESC, a.id DESC LIMIT $6`,
			tenantID, f.VendorID, f.PurchaseOrderID, nullTime(f.CursorCreatedAt), cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsn(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) StartAsnReceipt(ctx context.Context, id string, rec Receipt) (Asn, Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Asn{}, Receipt{}, err
	}
	rec = prepareArrival(rec)
	var asn Asn
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		asn, err = scanAsn(tx.QueryRow(ctx, `SELECT `+asnColumns+` FROM receiving_asn a WHERE a.tenant_id = $1 AND a.id = $2 FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAsnNotFound
		}
		if err != nil {
			return err
		}
		if asn.Status != domain.AsnOpen {
			return AsnConsumed(asn)
		}
		if err := insertArrival(ctx, tx, tenantID, rec); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE receiving_asn SET status = 'RECEIVING', receipt_id = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, id, rec.ID); err != nil {
			return err
		}
		asn.Status, asn.ReceiptID = domain.AsnReceiving, rec.ID
		out, err = loadReceipt(ctx, tx, tenantID, rec.ID, false)
		return err
	})
	if err != nil {
		return Asn{}, Receipt{}, s.mapWriteError(ctx, tenantID, rec, err)
	}
	return asn, out, nil
}

func reopenAsn(ctx context.Context, tx pgx.Tx, tenantID, receiptID string) error {
	_, err := tx.Exec(ctx, `UPDATE receiving_asn SET status = 'OPEN', receipt_id = NULL WHERE tenant_id = $1 AND receipt_id = $2`, tenantID, receiptID)
	return err
}
