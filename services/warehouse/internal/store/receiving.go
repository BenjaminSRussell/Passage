package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

// putAwayWorkType is the work type of the put-away work a received pallet
// creates; the warehouse's PURCHASE_PUTAWAY directives place it.
const putAwayWorkType = "PURCHASE_PUTAWAY"

// RegisterASN stores an ASN packing structure as an OPEN receipt with every
// pallet EXPECTED (FR-31.9). A second receipt for the same ASN reference in
// the warehouse is ErrConflict.
func (s *Store) RegisterASN(ctx context.Context, idem Idem, r domain.Receipt) (domain.Receipt, bool, error) {
	var (
		out      domain.Receipt
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (domain.Receipt, error) {
			r.Status, r.Version = domain.ReceiptOpen, 1
			if err := tx.QueryRow(ctx, `insert into receipt (tenant_id, id, warehouse_id, asn_ref, tolerance_pct, status)
				values ($1, $2, $3, $4, $5, $6) returning created_at, updated_at`,
				tenantID(ctx), r.ID, r.WarehouseID, r.ASNRef, r.TolerancePct, string(r.Status)).Scan(&r.CreatedAt, &r.UpdatedAt); err != nil {
				return r, err
			}
			for i := range r.Lines {
				l := &r.Lines[i]
				l.Status = domain.PalletExpected
				l.ReceivedQty, l.VarianceQty, l.VariancePct = decimal.Zero, decimal.Zero, decimal.Zero
				if domain.ValidSSCC(l.LPID) && l.SSCC == "" {
					l.SSCC = l.LPID // an ASN that names an SSCC as the license plate: that is the pallet's SSCC
				}
				if _, err := tx.Exec(ctx, `insert into receipt_line (tenant_id, receipt_id, line_no, item_id, lp_id, po_line_ref,
					expected_qty, weight, volume, status, sscc, lot, expiry_date) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
					tenantID(ctx), r.ID, l.LineNo, l.ItemID, l.LPID, l.POLineRef, l.ExpectedQty, l.Weight, l.Volume, string(l.Status),
					l.SSCC, l.Lot, nullable(l.ExpiryDate)); err != nil {
					return r, err
				}
			}
			return r, nil
		})
		return err
	})
	return out, replayed, err
}

// ReceiveInput receives one pallet. IfMatch is the receipt version the caller
// last saw, or 0 to skip the check.
type ReceiveInput struct {
	ReceiptID string
	LineNo    int
	Qty       decimal.Decimal
	// Lot and ExpiryDate (YYYY-MM-DD), when given, replace what the ASN said: the label shows what was counted.
	Lot        string
	ExpiryDate string
	User       string
	IfMatch    int
	Now        time.Time
}

// Received is the receipt after a pallet arrived, the put-away work created
// for it, the directive trace of its line, and the WMS-0107 warning when the
// pallet is outside tolerance.
type Received struct {
	Receipt  domain.Receipt  `json:"receipt"`
	Work     domain.Work     `json:"work"`
	Traces   []domain.Result `json:"traces"`
	Warnings []Warning       `json:"warnings"`
}

// Warning is a non-blocking catalog message on a command's answer.
type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// ReceiveLine receives pallet n of a receipt (FR-31.9, BR-31.10) under a row
// lock on the receipt: it records the quantity and its variance, creates
// PURCHASE_PUTAWAY work for the pallet placed by the warehouse's directives
// (a pallet no directive can place gives BLOCKED_NO_LOCATION work; the receipt
// posts either way), and enqueues work.created and receipt.registered in the
// same transaction. Stock reaches the location when the put-away is confirmed.
func (s *Store) ReceiveLine(ctx context.Context, idem Idem, in ReceiveInput) (Received, bool, error) {
	var (
		out      Received
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusOK, func() (Received, error) {
			r, err := readReceipt(ctx, tx, in.ReceiptID, true)
			if err != nil {
				return Received{}, err
			}
			if in.IfMatch != 0 && in.IfMatch != r.Version {
				return Received{}, fmt.Errorf("%w: current version is %d", ErrVersionConflict, r.Version)
			}
			line, err := r.Receive(in.LineNo, in.Qty, in.User, in.Now)
			if err != nil {
				return Received{}, err
			}
			if in.Lot != "" {
				line.Lot = in.Lot
			}
			if in.ExpiryDate != "" {
				line.ExpiryDate = in.ExpiryDate
			}
			if err := identifyPallet(ctx, tx, &line); err != nil {
				return Received{}, err
			}
			for i := range r.Lines {
				if r.Lines[i].LineNo == line.LineNo {
					r.Lines[i].LPID, r.Lines[i].SSCC, r.Lines[i].Lot, r.Lines[i].ExpiryDate = line.LPID, line.SSCC, line.Lot, line.ExpiryDate
				}
			}
			planned, err := planWork(ctx, tx, PlanInput{WarehouseID: r.WarehouseID, WorkType: putAwayWorkType,
				Lines: []domain.NewLine{line.PutAwayLine(r.ID)}})
			if err != nil {
				return Received{}, err
			}
			line.WorkID = planned.Work.ID
			for i := range r.Lines {
				if r.Lines[i].LineNo == line.LineNo {
					r.Lines[i].WorkID = line.WorkID
				}
			}
			if _, err := tx.Exec(ctx, `update receipt_line set status = $3, received_qty = $4, variance_qty = $5, variance_pct = $6,
				discrepancy = $7, reason = $8, work_id = $9, received_by = $10, received_at = $11,
				lp_id = $12, sscc = $13, lot = $14, expiry_date = $15
				where receipt_id = $1 and line_no = $2`,
				r.ID, line.LineNo, string(line.Status), line.ReceivedQty, line.VarianceQty, line.VariancePct,
				line.Discrepancy, line.Reason, line.WorkID, line.ReceivedBy, line.ReceivedAt,
				line.LPID, line.SSCC, line.Lot, nullable(line.ExpiryDate)); err != nil {
				return Received{}, err
			}
			if err := tx.QueryRow(ctx, `update receipt set status = $2, version = version + 1, updated_at = now()
				where id = $1 returning version, updated_at`, r.ID, string(r.Status)).Scan(&r.Version, &r.UpdatedAt); err != nil {
				return Received{}, err
			}
			if err := enqueueFor(ctx, tx, "receipt", r.ID, evts.ReceiptRegistered, registeredData(r, line, in.User)); err != nil {
				return Received{}, err
			}
			res := Received{Receipt: r, Work: planned.Work, Traces: planned.Traces, Warnings: []Warning{}}
			if line.Discrepancy {
				res.Warnings = append(res.Warnings, Warning{Code: domain.CodeVariance, Detail: line.Reason})
			}
			return res, nil
		})
		return err
	})
	return out, replayed, err
}

// GetReceipt returns one receipt with its pallets.
func (s *Store) GetReceipt(ctx context.Context, id string) (domain.Receipt, error) {
	var out domain.Receipt
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readReceipt(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ReceiptFilter narrows ListReceipts. AfterASN is the keyset cursor: the page
// starts after that ASN reference.
type ReceiptFilter struct {
	WarehouseID string
	Status      string
	AfterASN    string
	Limit       int
}

// ListReceipts returns a page of a warehouse's receipts ordered by ASN
// reference, each with its pallet, received and discrepancy counts.
func (s *Store) ListReceipts(ctx context.Context, f ReceiptFilter) ([]domain.ReceiptSummary, error) {
	var out []domain.ReceiptSummary
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select r.id::text, r.warehouse_id::text, r.asn_ref, r.tolerance_pct::text, r.status,
			count(l.line_no)::int, (count(*) filter (where l.status = 'RECEIVED'))::int, (count(*) filter (where l.discrepancy))::int,
			r.created_at, r.updated_at
			from receipt r left join receipt_line l on l.tenant_id = r.tenant_id and l.receipt_id = r.id
			where r.warehouse_id = $1 and ($2::text = '' or r.status = $2::text) and r.asn_ref > $3::text
			group by r.tenant_id, r.id order by r.asn_ref limit $4`, f.WarehouseID, f.Status, f.AfterASN, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r domain.ReceiptSummary
			var tol, status string
			if err := rows.Scan(&r.ID, &r.WarehouseID, &r.ASNRef, &tol, &status, &r.Lines, &r.Received, &r.Discrepancies,
				&r.CreatedAt, &r.UpdatedAt); err != nil {
				return err
			}
			r.Status = domain.ReceiptStatus(status)
			if r.TolerancePct, err = dec(tol); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// readReceipt loads a receipt and its pallets; lock takes a row lock on the header.
func readReceipt(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.Receipt, error) {
	var r domain.Receipt
	var tol, status string
	q := `select id::text, warehouse_id::text, asn_ref, tolerance_pct::text, status, version, created_at, updated_at
		from receipt where id = $1`
	if lock {
		q += ` for update`
	}
	if err := tx.QueryRow(ctx, q, id).Scan(&r.ID, &r.WarehouseID, &r.ASNRef, &tol, &status, &r.Version, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return r, err
	}
	r.Status = domain.ReceiptStatus(status)
	var err error
	if r.TolerancePct, err = dec(tol); err != nil {
		return r, err
	}
	rows, err := tx.Query(ctx, `select line_no, item_id, lp_id, po_line_ref, expected_qty::text, weight::text, volume::text, status,
		received_qty::text, variance_qty::text, variance_pct::text, discrepancy, reason, coalesce(work_id::text, ''),
		received_by, received_at, sscc, lot, coalesce(expiry_date::text, '')
		from receipt_line where receipt_id = $1 order by line_no`, r.ID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.ReceiptLine
		var lstatus, exp, weight, volume, recv, vqty, vpct string
		if err := rows.Scan(&l.LineNo, &l.ItemID, &l.LPID, &l.POLineRef, &exp, &weight, &volume, &lstatus, &recv, &vqty, &vpct,
			&l.Discrepancy, &l.Reason, &l.WorkID, &l.ReceivedBy, &l.ReceivedAt, &l.SSCC, &l.Lot, &l.ExpiryDate); err != nil {
			return r, err
		}
		l.Status = domain.ReceiptLineStatus(lstatus)
		for _, f := range []struct {
			raw string
			dst *decimal.Decimal
		}{{exp, &l.ExpectedQty}, {weight, &l.Weight}, {volume, &l.Volume}, {recv, &l.ReceivedQty}, {vqty, &l.VarianceQty}, {vpct, &l.VariancePct}} {
			if *f.dst, err = dec(f.raw); err != nil {
				return r, err
			}
		}
		if l.ReceivedAt != nil {
			utc := l.ReceivedAt.UTC()
			l.ReceivedAt = &utc
		}
		r.Lines = append(r.Lines, l)
	}
	return r, rows.Err()
}

func registeredData(r domain.Receipt, l domain.ReceiptLine, user string) evts.ReceiptRegisteredData {
	refs := []string{}
	if l.POLineRef != "" {
		refs = append(refs, l.POLineRef)
	}
	return evts.ReceiptRegisteredData{ReceiptID: r.ID, WarehouseID: r.WarehouseID, ASNRef: r.ASNRef, LineNo: l.LineNo,
		POLineRefs: refs, LPIDs: []string{l.LPID}, Items: []evts.ItemQty{{ItemID: l.ItemID, Qty: l.ReceivedQty.String()}},
		ExpectedQty: l.ExpectedQty.String(), VarianceQty: l.VarianceQty.String(), VariancePct: l.VariancePct.String(),
		Discrepancy: l.Discrepancy, WorkID: l.WorkID, User: user}
}
