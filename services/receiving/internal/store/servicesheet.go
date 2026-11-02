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
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

var ErrSheetNotFound = errors.New("service entry sheet not found")

const sheetAggregate = "serviceentrysheet"

type ServiceSheet struct {
	ID              string
	LegalEntityID   string
	VendorID        string
	PurchaseOrderID string
	Currency        string
	Status          string
	Total           decimal.Decimal
	Lines           []domain.SheetLine
	CreatedBy       string
	CreatedAt       time.Time
	DecidedBy       string
	DecidedAt       *time.Time
	RejectReason    string
}

type SheetDecisionFunc func(ServiceSheet) (events.Event, error)

func SheetCommitted(sheets []ServiceSheet) map[int]decimal.Decimal {
	committed := map[int]decimal.Decimal{}
	for _, s := range sheets {
		if s.Status == domain.SheetRejected {
			continue
		}
		for _, l := range s.Lines {
			committed[l.POLineNo] = committed[l.POLineNo].Add(l.Amount)
		}
	}
	return committed
}

func CheckSheetLimits(in ServiceSheet, existing []ServiceSheet, limits map[int]decimal.Decimal) error {
	committed := SheetCommitted(existing)
	for no, added := range SheetCommitted([]ServiceSheet{in}) {
		if committed[no].Add(added).GreaterThan(limits[no]) {
			return &domain.Error{Code: domain.CodeSheetOverLine, Detail: fmt.Sprintf("purchase order line %d: sheets would total %s against a line value of %s", no, committed[no].Add(added), limits[no])}
		}
	}
	return nil
}

func CheckSheetDecision(s ServiceSheet, by string) error {
	switch {
	case s.Status != domain.SheetPending:
		return &domain.Error{Code: domain.CodeSheetNotPending, Detail: fmt.Sprintf("service entry sheet %s is already %s", s.ID, s.Status)}
	case strings.EqualFold(s.CreatedBy, by):
		return &domain.Error{Code: domain.CodeSheetSelfAccept, Detail: "the user who entered the sheet cannot decide it"}
	}
	return nil
}

const sheetColumns = `id, legal_entity_id, vendor_id, purchase_order_id, currency, status, total, lines, created_by, created_at, coalesce(decided_by, ''), decided_at, coalesce(reject_reason, '')`

func scanSheet(row pgx.Row) (ServiceSheet, error) {
	var s ServiceSheet
	var lines []byte
	if err := row.Scan(&s.ID, &s.LegalEntityID, &s.VendorID, &s.PurchaseOrderID, &s.Currency, &s.Status, &s.Total, &lines, &s.CreatedBy, &s.CreatedAt, &s.DecidedBy, &s.DecidedAt, &s.RejectReason); err != nil {
		return ServiceSheet{}, err
	}
	return s, json.Unmarshal(lines, &s.Lines)
}

func sheetsOfOrder(ctx context.Context, tx pgx.Tx, tenantID, purchaseOrderID string) ([]ServiceSheet, error) {
	rows, err := tx.Query(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND purchase_order_id = $2 AND status <> 'REJECTED'`, tenantID, purchaseOrderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceSheet
	for rows.Next() {
		s, err := scanSheet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *Postgres) CreateSheet(ctx context.Context, in ServiceSheet, limits map[int]decimal.Decimal) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	lines, err := json.Marshal(in.Lines)
	if err != nil {
		return ServiceSheet{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, strings.ToLower(tenantID+"|sheets|"+in.PurchaseOrderID)); err != nil {
			return err
		}
		existing, err := sheetsOfOrder(ctx, tx, tenantID, in.PurchaseOrderID)
		if err != nil {
			return err
		}
		if err := CheckSheetLimits(in, existing, limits); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO receiving_service_entry_sheet (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, currency, status, total, lines, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', $7, $8, $9) RETURNING created_at`,
			tenantID, in.ID, in.LegalEntityID, in.VendorID, in.PurchaseOrderID, in.Currency, in.Total, lines, in.CreatedBy).Scan(&in.CreatedAt)
	})
	in.Status = domain.SheetPending
	return in, err
}

func (s *Postgres) GetSheet(ctx context.Context, id string) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	var out ServiceSheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSheet(tx.QueryRow(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND id = $2`, tenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceSheet{}, ErrSheetNotFound
	}
	return out, err
}

func (s *Postgres) DecideSheet(ctx context.Context, id, by string, accept bool, reason string, fn SheetDecisionFunc) (ServiceSheet, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ServiceSheet{}, err
	}
	var out ServiceSheet
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := scanSheet(tx.QueryRow(ctx, `SELECT `+sheetColumns+` FROM receiving_service_entry_sheet WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSheetNotFound
		}
		if err != nil {
			return err
		}
		if err := CheckSheetDecision(sheet, by); err != nil {
			return err
		}
		sheet.Status, sheet.DecidedBy, sheet.RejectReason = domain.SheetRejected, by, reason
		if accept {
			sheet.Status, sheet.RejectReason = domain.SheetAccepted, ""
		}
		evt, err := fn(sheet)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE receiving_service_entry_sheet SET status = $3, decided_by = $4, decided_at = now(), reject_reason = NULLIF($5, '')
			WHERE tenant_id = $1 AND id = $2 RETURNING decided_at`, tenantID, id, sheet.Status, by, sheet.RejectReason).Scan(&sheet.DecidedAt); err != nil {
			return err
		}
		out = sheet
		return outbox.Enqueue(ctx, tx, sheetAggregate, id, evt)
	})
	return out, err
}
