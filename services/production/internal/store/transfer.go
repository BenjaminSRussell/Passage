package store

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func splitRefused(format string, args ...any) error {
	return httpx.NewProblem(http.StatusConflict, domain.CodeSplitRefused, fmt.Sprintf(format, args...))
}

// checkStartedSplit refuses a split of a Started order that cannot be shared:
// one that already reported output (the receipts belong to the original), that
// has draft journals (they name the original's lines and operations and would
// post against the wrong share) or that holds as-built records (FR-21.19: each
// names a finished serial or batch of the original and is checked against its
// consumption, which the split reduces; BR-21.10).
func checkStartedSplit(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead) error {
	if h.qtyRAF.IsPositive() || h.qtyError.IsPositive() {
		return splitRefused("a Started order with reported output cannot be split; reset the report as finished first")
	}
	var drafts, asBuilt int
	if err := tx.QueryRow(ctx, `select (select count(*) from production_journal where tenant_id = $1 and order_id = $2 and not posted),
			(select count(*) from production_as_built where tenant_id = $1 and order_id = $2 and detached_at is null)`,
		tenantID, h.id).Scan(&drafts, &asBuilt); err != nil {
		return err
	}
	if drafts > 0 {
		return splitRefused("the order has %d draft journals; post or delete them before splitting", drafts)
	}
	if asBuilt > 0 {
		return splitRefused("the order has %d as-built records; detach them before splitting, they belong to the original's finished serials and batches", asBuilt)
	}
	return nil
}

// checkNoSplitTransfer refuses a status reset back before Started of an order
// that took part in a split transfer: reversing the picking lists and route
// cards posted before the transfer would take back more than the order now
// holds, and the transfer itself cannot be reversed on one order alone
// (BR-21.11). Leaving ReportedAsFinished stays allowed: it reverses only the
// reports as finished, which a split never shares (a split refuses reported output).
func checkNoSplitTransfer(ctx context.Context, tx pgx.Tx, tenantID, orderID string, types []domain.JournalType) error {
	if !slices.Contains(types, domain.JournalPickingList) && !slices.Contains(types, domain.JournalRouteCard) && !slices.Contains(types, domain.JournalJobCard) {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from production_journal where tenant_id = $1 and order_id = $2 and journal_type = $3`,
		tenantID, orderID, string(domain.JournalSplitTransfer)).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return httpx.NewProblem(http.StatusConflict, domain.CodeIllegalTransition,
			"the order took part in a split that moved posted consumption and WIP; a reset before Started can no longer reverse its journals")
	}
	return nil
}

// planTransfer reads what was posted on the original (consumed quantity and
// value per BOM line, hours, quantities and value per operation, reversals and
// earlier transfers included) and computes the share that moves (BR-21.11).
func planTransfer(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, splitQty decimal.Decimal, lines []domain.OrderLine, ops []domain.Operation) (domain.Transfer, error) {
	issue := map[string]decimal.Decimal{}
	rows, err := tx.Query(ctx, `select l.bom_line_number, sum(l.amount)::text from production_journal_line l
		join production_journal j on j.tenant_id = l.tenant_id and j.id = l.journal_id
		where l.tenant_id = $1 and j.order_id = $2 and j.posted and l.kind = 'issue' group by l.bom_line_number`, tenantID, h.id)
	if err != nil {
		return domain.Transfer{}, err
	}
	for rows.Next() {
		var no, amount string
		if err := rows.Scan(&no, &amount); err != nil {
			rows.Close()
			return domain.Transfer{}, err
		}
		issue[no] = dec(amount)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.Transfer{}, err
	}
	type pair struct{ labor, overhead decimal.Decimal }
	hours := map[int]pair{}
	rows, err = tx.Query(ctx, `select l.oper_number, sum(l.amount)::text, sum(l.overhead_amount)::text from production_journal_line l
		join production_journal j on j.tenant_id = l.tenant_id and j.id = l.journal_id
		where l.tenant_id = $1 and j.order_id = $2 and j.posted and l.kind = 'hours' group by l.oper_number`, tenantID, h.id)
	if err != nil {
		return domain.Transfer{}, err
	}
	for rows.Next() {
		var n int
		var labor, overhead string
		if err := rows.Scan(&n, &labor, &overhead); err != nil {
			rows.Close()
			return domain.Transfer{}, err
		}
		hours[n] = pair{dec(labor), dec(overhead)}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.Transfer{}, err
	}
	var lin []domain.TransferLineIn
	for _, l := range lines {
		lin = append(lin, domain.TransferLineIn{LineNo: l.LineNo, ItemID: l.ItemID, Qty: l.QtyConsumed, Amount: issue[l.LineNo]})
	}
	var oin []domain.TransferOpIn
	for _, o := range ops {
		v := hours[o.OperNumber]
		oin = append(oin, domain.TransferOpIn{OperNumber: o.OperNumber, Hours: o.HoursReported, Good: o.QtyGood, Error: o.QtyError, Labor: v.labor, Overhead: v.overhead})
	}
	return domain.PlanTransfer(h.quantity, splitQty, h.currency, lin, oin), nil
}

// applyTransfer moves the planned share from the original to the new order:
// the BOM lines' consumed quantity and the operations' reported hours and
// quantities (an operation that was finished stays finished on both, one that
// moved feedback is started on the new order), and a pair of posted
// split-transfer journals with their balanced WIP postings and journal.posted
// events, so each order's cost, WIP balance and ledger lines follow (BR-21.11).
func applyTransfer(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, newID string, tr domain.Transfer, ops []domain.Operation, in Split) error {
	status := map[int]domain.OperStatus{}
	for _, o := range ops {
		status[o.OperNumber] = o.Status
	}
	for _, l := range tr.Lines {
		switch l.Kind {
		case KindIssue:
			if _, err := tx.Exec(ctx, `update production_bom_line set qty_consumed = qty_consumed - $4 where tenant_id = $1 and order_id = $2 and line_number = $3`,
				tenantID, h.id, l.LineNo, l.Qty.String()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update production_bom_line set qty_consumed = $4 where tenant_id = $1 and order_id = $2 and line_number = $3`,
				tenantID, newID, l.LineNo, l.Qty.String()); err != nil {
				return err
			}
		case KindHours:
			if _, err := tx.Exec(ctx, `update production_operation set hours_reported = hours_reported - $4, qty_good = qty_good - $5, qty_error = qty_error - $6
				where tenant_id = $1 and order_id = $2 and oper_number = $3`, tenantID, h.id, l.OperNumber, l.Qty.String(), l.Good.String(), l.Error.String()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update production_operation set hours_reported = $4, qty_good = $5, qty_error = $6, status = $7
				where tenant_id = $1 and order_id = $2 and oper_number = $3`, tenantID, newID, l.OperNumber, l.Qty.String(), l.Good.String(), l.Error.String(),
				string(domain.OperStarted)); err != nil {
				return err
			}
		}
	}
	// An operation closed on the original is closed on the new order too.
	for n, st := range status {
		if st == domain.OperFinished {
			if _, err := tx.Exec(ctx, `update production_operation set status = 'finished' where tenant_id = $1 and order_id = $2 and oper_number = $3`,
				tenantID, newID, n); err != nil {
				return err
			}
		}
	}

	profile, err := resolveProfile(ctx, tx, tenantID, h)
	if err != nil {
		return err
	}
	date := today()
	var last *string
	if err := tx.QueryRow(ctx, `select max(posting_date)::text from production_journal where tenant_id = $1 and order_id = $2 and posted`,
		tenantID, h.id).Scan(&last); err != nil {
		return err
	}
	if last != nil && *last > date {
		date = *last
	}
	newHead := h
	newHead.id = newID
	for _, side := range []struct {
		head    orderHead
		other   string
		out     bool
		keySufx string
	}{{h, newID, true, ":transfer-out"}, {newHead, h.id, false, ":transfer-in"}} {
		if err := postTransferJournal(ctx, tx, tenantID, side.head, side.other, tr, profile, side.out, date, in, in.IdempotencyKey+side.keySufx); err != nil {
			return err
		}
	}
	return nil
}

// postTransferJournal writes one side of the transfer as a posted journal: the
// moved lines (negated on the original), their postings and the event.
func postTransferJournal(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, counterpart string, tr domain.Transfer, profile domain.Profile, out bool, date string, in Split, key string) error {
	number, err := nextJournalNumber(ctx, tx, tenantID, h.legalEntityID)
	if err != nil {
		return err
	}
	j := Journal{ID: ids.New(), OrderID: h.id, Type: domain.JournalSplitTransfer, JournalNumber: number, PostingDate: date, Posted: true,
		CounterpartOrderID: &counterpart, Lines: []JournalLine{}}
	for _, l := range tr.Lines {
		line := JournalLine{Kind: l.Kind, Qty: l.Qty, QtyGood: l.Good, QtyError: l.Error, Amount: l.Amount, OverheadAmount: l.Overhead}
		if l.Kind == KindIssue {
			no, item := l.LineNo, l.ItemID
			line.LineNo, line.ItemID = &no, &item
		} else {
			n := l.OperNumber
			line.OperNumber = &n
		}
		if !l.Qty.IsZero() {
			line.UnitCost = l.Amount.Div(l.Qty).Round(6)
		}
		if out {
			line.Qty, line.QtyGood, line.QtyError = line.Qty.Neg(), line.QtyGood.Neg(), line.QtyError.Neg()
			line.Amount, line.OverheadAmount = line.Amount.Neg(), line.OverheadAmount.Neg()
		}
		j.Lines = append(j.Lines, line)
	}
	j.Postings = domain.TransferPostings(profile, tr, out)
	if !domain.Balanced(j.Postings) {
		return fmt.Errorf("split transfer %s postings do not balance", j.ID)
	}
	if _, err := tx.Exec(ctx, `insert into production_journal (tenant_id, id, legal_entity_id, order_id, journal_type, journal_number,
			posting_date, posted, posted_at, posted_by, counterpart_order_id, idempotency_key, request_hash, post_key, created_by)
		values ($1, $2, $3, $4, $5, $6, $7::date, true, now(), $8, $9, $10, $11, $10, $8)`,
		tenantID, j.ID, h.legalEntityID, h.id, string(j.Type), number, date, in.Actor, counterpart, key, in.RequestHash); err != nil {
		return err
	}
	if err := insertJournalLines(ctx, tx, tenantID, j.ID, j.Lines); err != nil {
		return err
	}
	if err := insertPostings(ctx, tx, tenantID, h, &j.ID, date, j.Postings); err != nil {
		return err
	}
	return enqueueJournalPosted(ctx, tx, tenantID, h, j, in.Actor)
}
