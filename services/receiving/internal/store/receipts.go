package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	uniqueViolation       = "23505"
	packingSlipConstraint = "receiving_receipt_tenant_id_vendor_id_packing_slip_key"
	serialConstraint      = "receiving_serial_pkey"
	defaultListLimit      = 50
	maxListLimit          = 200
	receiptAggregate      = "receipt"

	receiptColumns = `id::text, COALESCE(receipt_number, ''), legal_entity_id::text, vendor_id::text, purchase_order_id::text, packing_slip,
		COALESCE(carrier, ''), pallet_count, state, receipt_date, COALESCE(currency, ''), exchange_rate, posting_date, posted_at,
		COALESCE(ledger_entry_id, ''), created_at`
	lineColumns = `line_no, COALESCE(po_line_id::text, ''), product_id::text, qty, COALESCE(ordered_remaining, 0), COALESCE(over_delivery_pct, 0),
		unit_price, currency, COALESCE(unit, ''), COALESCE(batch, ''), COALESCE(location_id::text, ''), inventory_status,
		batch_tracked, serial_tracked, amount, amount_acct`
)

type scanner interface{ Scan(dest ...any) error }

func tenantOf(ctx context.Context) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("store: no principal in context")
	}
	return p.TenantID, nil
}

func isUnique(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraint
}

func execBatch(ctx context.Context, tx pgx.Tx, sql string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, args := range rows {
		batch.Queue(sql, args...)
	}
	results := tx.SendBatch(ctx, batch)
	for range rows {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}

func allocateNumber(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	var n int64
	err := tx.QueryRow(ctx, `INSERT INTO receiving_number_counter (tenant_id, prefix, next_value) VALUES ($1, 'RCV', 2)
		ON CONFLICT (tenant_id, prefix) DO UPDATE SET next_value = receiving_number_counter.next_value + 1
		RETURNING next_value - 1`, tenantID).Scan(&n)
	return fmt.Sprintf("RCV-%08d", n), err
}

func scanReceipt(row scanner) (Receipt, error) {
	var r Receipt
	err := row.Scan(&r.ID, &r.Number, &r.LegalEntityID, &r.VendorID, &r.PurchaseOrderID, &r.PackingSlip, &r.Carrier, &r.PalletCount,
		&r.State, &r.Date, &r.Currency, &r.ExchangeRate, &r.PostingDate, &r.PostedAt, &r.LedgerEntryID, &r.CreatedAt)
	return r, err
}

func scanLine(row scanner) (Line, error) {
	var l Line
	err := row.Scan(&l.LineNo, &l.POLineID, &l.ProductID, &l.Qty, &l.OrderedRemaining, &l.OverDeliveryPct, &l.UnitPrice, &l.Currency,
		&l.Unit, &l.Batch, &l.LocationID, &l.InventoryStatus, &l.BatchTracked, &l.SerialTracked, &l.Amount, &l.AmountAcct)
	return l, err
}

func loadReceipt(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Receipt, error) {
	query := `SELECT ` + receiptColumns + ` FROM receiving_receipt WHERE tenant_id = $1 AND id = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	r, err := scanReceipt(tx.QueryRow(ctx, query, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}
	rows, err := tx.Query(ctx, `SELECT `+lineColumns+` FROM receiving_receipt_line WHERE tenant_id = $1 AND receipt_id = $2 ORDER BY line_no`, tenantID, id)
	if err != nil {
		return Receipt{}, err
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return Receipt{}, err
		}
		r.Lines = append(r.Lines, l)
	}
	if err := rows.Err(); err != nil {
		return Receipt{}, err
	}
	rows.Close()
	serials, err := tx.Query(ctx, `SELECT line_no, serial FROM receiving_serial WHERE tenant_id = $1 AND receipt_id = $2 ORDER BY line_no, serial`, tenantID, id)
	if err != nil {
		return Receipt{}, err
	}
	defer serials.Close()
	byLine := map[int]*Line{}
	for i := range r.Lines {
		byLine[r.Lines[i].LineNo] = &r.Lines[i]
	}
	for serials.Next() {
		var lineNo int
		var serial string
		if err := serials.Scan(&lineNo, &serial); err != nil {
			return Receipt{}, err
		}
		if l := byLine[lineNo]; l != nil {
			l.Serials = append(l.Serials, serial)
		}
	}
	return r, serials.Err()
}

func recordedSerials(ctx context.Context, tx pgx.Tx, tenantID string, productIDs []string) (map[domain.SerialKey]struct{}, error) {
	recorded := map[domain.SerialKey]struct{}{}
	rows, err := tx.Query(ctx, `SELECT product_id::text, serial FROM receiving_serial WHERE tenant_id = $1 AND product_id = ANY($2::uuid[])`, tenantID, productIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key domain.SerialKey
		if err := rows.Scan(&key.ProductID, &key.Serial); err != nil {
			return nil, err
		}
		recorded[key] = struct{}{}
	}
	return recorded, rows.Err()
}

func (s *Postgres) RecordedSerials(ctx context.Context, productIDs []string) (map[domain.SerialKey]struct{}, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var recorded map[domain.SerialKey]struct{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		recorded, err = recordedSerials(ctx, tx, tenantID, productIDs)
		return err
	})
	return recorded, err
}

func prepareArrival(in Receipt) Receipt {
	if in.ID == "" {
		in.ID = ids.New()
	}
	if in.ExchangeRate.Sign() == 0 {
		in.ExchangeRate = decimal.NewFromInt(1)
	}
	return in
}

func insertArrival(ctx context.Context, tx pgx.Tx, tenantID string, in Receipt) error {
	number, err := allocateNumber(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO receiving_receipt (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, packing_slip, state, receipt_date,
		receipt_number, carrier, pallet_count, currency, exchange_rate, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'ARRIVED', $7, $8, NULLIF($9, ''), $10, NULLIF($11, ''), $12, now())`,
		tenantID, in.ID, in.LegalEntityID, in.VendorID, in.PurchaseOrderID, in.PackingSlip, in.Date,
		number, in.Carrier, in.PalletCount, in.Currency, in.ExchangeRate); err != nil {
		return err
	}
	rows := make([][]any, len(in.Lines))
	for i, l := range in.Lines {
		lineNo, currency := l.LineNo, l.Currency
		if lineNo == 0 {
			lineNo = i + 1
		}
		if currency == "" {
			currency = in.Currency
		}
		rows[i] = []any{tenantID, in.ID, lineNo, in.LegalEntityID, l.ProductID, l.OrderedRemaining, l.UnitPrice, currency,
			l.POLineID, l.Unit, l.OrderedRemaining, l.OverDeliveryPct, l.BatchTracked, l.SerialTracked}
	}
	return execBatch(ctx, tx, `INSERT INTO receiving_receipt_line (tenant_id, receipt_id, line_no, legal_entity_id, product_id, qty, unit_price, currency,
		po_line_id, unit, ordered_remaining, over_delivery_pct, batch_tracked, serial_tracked)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, '')::uuid, NULLIF($10, ''), $11, $12, $13, $14)`, rows)
}

func (s *Postgres) mapWriteError(ctx context.Context, tenantID string, in Receipt, err error) error {
	switch {
	case isUnique(err, packingSlipConstraint):
		return s.duplicateSlip(ctx, tenantID, in)
	case isUnique(err, serialConstraint):
		return &domain.Error{Code: domain.CodeDuplicateSerial, Detail: "a serial is already recorded for the product"}
	}
	return err
}

func (s *Postgres) CreateArrival(ctx context.Context, in Receipt) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	in = prepareArrival(in)
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertArrival(ctx, tx, tenantID, in); err != nil {
			return err
		}
		out, err = loadReceipt(ctx, tx, tenantID, in.ID, false)
		return err
	})
	if err != nil {
		return Receipt{}, s.mapWriteError(ctx, tenantID, in, err)
	}
	return out, nil
}

func (s *Postgres) DirectReceipt(ctx context.Context, in Receipt, fn RegisterFunc) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	in = prepareArrival(in)
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := insertArrival(ctx, tx, tenantID, in); err != nil {
			return err
		}
		out, err = registerTx(ctx, tx, tenantID, in.ID, fn)
		return err
	})
	if err != nil {
		return Receipt{}, s.mapWriteError(ctx, tenantID, in, err)
	}
	return out, nil
}

func (s *Postgres) duplicateSlip(ctx context.Context, tenantID string, in Receipt) error {
	dup := &DuplicatePackingSlipError{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT COALESCE(receipt_number, '') FROM receiving_receipt WHERE tenant_id = $1 AND vendor_id = $2 AND packing_slip = $3`,
			tenantID, in.VendorID, in.PackingSlip).Scan(&dup.ReceiptNumber)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	return dup
}

func registerTx(ctx context.Context, tx pgx.Tx, tenantID, id string, fn RegisterFunc) (Receipt, error) {
	current, err := loadReceipt(ctx, tx, tenantID, id, true)
	if err != nil {
		return Receipt{}, err
	}
	productIDs := make([]string, len(current.Lines))
	for i, l := range current.Lines {
		productIDs[i] = l.ProductID
	}
	recorded, err := recordedSerials(ctx, tx, tenantID, productIDs)
	if err != nil {
		return Receipt{}, err
	}
	lines, err := fn(current, recorded)
	if err != nil {
		return Receipt{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE receiving_receipt SET state = 'REGISTERED', updated_at = now() WHERE tenant_id = $1 AND id = $2 AND state = 'ARRIVED'`, tenantID, id)
	if err != nil {
		return Receipt{}, err
	}
	if tag.RowsAffected() != 1 {
		return Receipt{}, &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is not ARRIVED", id)}
	}
	var lineRows, serialRows [][]any
	for _, l := range lines {
		lineRows = append(lineRows, []any{tenantID, id, l.LineNo, l.Qty, l.Batch, l.LocationID, l.InventoryStatus})
		for _, serial := range l.Serials {
			serialRows = append(serialRows, []any{tenantID, l.ProductID, serial, current.LegalEntityID, id, l.LineNo})
		}
	}
	if err := execBatch(ctx, tx, `UPDATE receiving_receipt_line SET qty = $4, batch = NULLIF($5, ''), location_id = NULLIF($6, '')::uuid,
		inventory_status = COALESCE(NULLIF($7, ''), 'AVAILABLE') WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, lineRows); err != nil {
		return Receipt{}, err
	}
	if err := execBatch(ctx, tx, `INSERT INTO receiving_serial (tenant_id, product_id, serial, legal_entity_id, receipt_id, line_no)
		VALUES ($1, $2, $3, $4, $5, $6)`, serialRows); err != nil {
		return Receipt{}, err
	}
	return loadReceipt(ctx, tx, tenantID, id, false)
}

func (s *Postgres) Register(ctx context.Context, id string, fn RegisterFunc) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = registerTx(ctx, tx, tenantID, id, fn)
		return err
	})
	if err != nil {
		return Receipt{}, s.mapWriteError(ctx, tenantID, Receipt{}, err)
	}
	return out, nil
}

func (s *Postgres) Post(ctx context.Context, id string, fn PostFunc) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := loadReceipt(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if current.State != domain.Registered {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is %s, not REGISTERED", id, current.State)}
		}
		poLineIDs := make([]string, 0, len(current.Lines))
		for _, l := range current.Lines {
			if l.POLineID != "" {
				poLineIDs = append(poLineIDs, l.POLineID)
			}
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM receiving_grir_open_item WHERE tenant_id = $1 AND po_line_id = ANY($2::uuid[]) FOR UPDATE`, tenantID, poLineIDs); err != nil {
			return err
		}
		outcome, err := fn(current)
		if err != nil {
			return err
		}
		amounts := map[int]LineAmount{}
		for _, a := range outcome.Amounts {
			amounts[a.LineNo] = a
		}
		var lineRows, grirRows [][]any
		for _, l := range current.Lines {
			a, ok := amounts[l.LineNo]
			if !ok {
				return fmt.Errorf("post outcome for receipt %s lacks line %d", id, l.LineNo)
			}
			lineRows = append(lineRows, []any{tenantID, id, l.LineNo, a.Amount, a.AmountAcct})
			grirRows = append(grirRows, []any{tenantID, ids.New(), current.LegalEntityID, id, l.LineNo, l.Qty, a.Amount, l.Currency, current.VendorID, l.POLineID})
		}
		if _, err := tx.Exec(ctx, `UPDATE receiving_receipt SET state = 'POSTED', posting_date = $3, posted_at = now(), ledger_entry_id = $4, updated_at = now()
			WHERE tenant_id = $1 AND id = $2`, tenantID, id, outcome.PostingDate, outcome.LedgerEntryID); err != nil {
			return err
		}
		if err := execBatch(ctx, tx, `UPDATE receiving_receipt_line SET amount = $4, amount_acct = $5 WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, lineRows); err != nil {
			return err
		}
		if err := execBatch(ctx, tx, `INSERT INTO receiving_grir_open_item (tenant_id, id, legal_entity_id, receipt_id, line_no, received_qty, received_amount, currency, vendor_id, po_line_id, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid, 'OPEN')`, grirRows); err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, receiptAggregate, id, outcome.Event); err != nil {
			return err
		}
		out, err = loadReceipt(ctx, tx, tenantID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) GetReceipt(ctx context.Context, id string) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadReceipt(ctx, tx, tenantID, id, false)
		return err
	})
	return out, err
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *Postgres) ListReceipts(ctx context.Context, f ReceiptFilter) ([]Receipt, error) {
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
	var out []Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+receiptColumns+` FROM receiving_receipt
			WHERE tenant_id = $1
			AND ($2 = '' OR vendor_id = $2::uuid)
			AND ($3 = '' OR purchase_order_id = $3::uuid)
			AND ($4 = '' OR state = $4)
			AND ($5::date IS NULL OR receipt_date >= $5::date)
			AND ($6::date IS NULL OR receipt_date <= $6::date)
			AND ($7::timestamptz IS NULL OR (created_at, id) < ($7::timestamptz, $8::uuid))
			ORDER BY created_at DESC, id DESC LIMIT $9`,
			tenantID, f.VendorID, f.PurchaseOrderID, string(f.State), nullTime(f.From), nullTime(f.To), nullTime(f.CursorCreatedAt), cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReceipt(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
