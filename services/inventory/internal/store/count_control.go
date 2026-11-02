package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// optionalDecimal stores a zero percentage as NULL (no sample).
func optionalDecimal(d decimal.Decimal) *string {
	if d.IsZero() {
		return nil
	}
	s := d.String()
	return &s
}

// lockedSheet reads and locks a sheet and checks the If-Match version.
func lockedSheet(ctx context.Context, tx pgx.Tx, id string, version int) (domain.CountSheet, error) {
	sheet, err := loadCountSheet(ctx, tx, id, true)
	if err != nil {
		return sheet, err
	}
	if sheet.Version != version {
		return sheet, problem(http.StatusPreconditionFailed, CodeConflictVersion, "count sheet changed since it was read; reread and retry")
	}
	return sheet, nil
}

func sheetChanged(sheet domain.CountSheet, action string, lineID, reason string) invevents.CountSheetChangedData {
	return invevents.CountSheetChangedData{SheetID: sheet.ID, LegalEntityID: sheet.LegalEntityID, PlanID: sheet.PlanID, Number: sheet.Number,
		Action: action, Status: sheet.Status, LineCount: sheet.LineCount, CountedCount: sheet.CountedCount, RecountLines: sheet.RecountLines,
		LineID: optionalText(lineID), Reason: optionalText(reason), RecountSource: recountSource(action, lineID)}
}

// recountSource names who raised a RECOUNT_REQUESTED: a supervisor's request
// names its line, a movement's flag (which may touch several lines) does not.
func recountSource(action, lineID string) *string {
	switch {
	case action != invevents.CountActionRecountRequested:
		return nil
	case lineID == "":
		return optionalText(domain.RecountByMovement)
	default:
		return optionalText(domain.RecountBySupervisor)
	}
}

// moveSheet updates a sheet header's status and version, reloads it and
// enqueues countsheet.changed in the same transaction.
func moveSheet(ctx context.Context, tx pgx.Tx, sheetID, action, lineID, reason, setSQL string, args ...any) (domain.CountSheet, error) {
	tp, _ := tenant.FromContext(ctx)
	all := append([]any{tp.TenantID, sheetID}, args...)
	if _, err := tx.Exec(ctx, `update count_sheet set `+setSQL+`, version = version + 1 where tenant_id = $1 and id = $2`, all...); err != nil {
		return domain.CountSheet{}, err
	}
	out, err := loadCountSheet(ctx, tx, sheetID, false)
	if err != nil {
		return out, err
	}
	return out, enqueue(ctx, tx, "countsheet", sheetID, invevents.CountSheetChanged, tp.TenantID, sheetChanged(out, action, lineID, reason))
}

// SubmitCountSheet closes counting: an OPEN sheet with at least one counted
// line and no outstanding recount becomes COUNTED and no longer takes
// entries. Submitting a COUNTED sheet again returns it unchanged.
func (s *Postgres) SubmitCountSheet(ctx context.Context, sheetID string, version int) (domain.CountSheet, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := loadCountSheet(ctx, tx, sheetID, true)
		if err != nil {
			return err
		}
		if sheet.Status == domain.CountCounted {
			out = sheet
			return nil
		}
		if err := domain.RequireOpen(sheet.Status); err != nil {
			return domainProblem(err)
		}
		if sheet.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count sheet changed since it was read; reread and retry")
		}
		if n := domain.OutstandingRecounts(sheet.Lines); n > 0 {
			return problem(http.StatusConflict, domain.CodeCountRecount, fmt.Sprintf("%d line(s) are flagged for recount; enter them again before submitting", n))
		}
		if sheet.CountedCount == 0 {
			return problem(http.StatusUnprocessableEntity, domain.CodeCountNothing, "no line of the count sheet has been counted")
		}
		out, err = moveSheet(ctx, tx, sheetID, invevents.CountActionSubmitted, "", "",
			`status = 'COUNTED', submitted_by = $3, submitted_at = now()`, optionalText(tp.UserID))
		return err
	})
	return out, err
}

// ApproveCountSheet records the supervisor's review of a COUNTED sheet
// (CycleCountApprove). On a four-eyes plan the approver must not be one of the
// counters (INV-CNT-007). Approving an APPROVED sheet returns it unchanged.
func (s *Postgres) ApproveCountSheet(ctx context.Context, sheetID string, version int) (domain.CountSheet, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := loadCountSheet(ctx, tx, sheetID, true)
		if err != nil {
			return err
		}
		if sheet.Status == domain.CountApproved {
			out = sheet
			return nil
		}
		if sheet.Status == domain.CountOpen {
			return problem(http.StatusConflict, domain.CodeCountNotReady, "submit the count sheet for review before approving it")
		}
		if err := domain.RequireStatus(sheet.Status, domain.CountCounted); err != nil {
			return domainProblem(err)
		}
		if sheet.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count sheet changed since it was read; reread and retry")
		}
		if sheet.FourEyes {
			if err := domain.CheckFourEyes(tp.UserID, sheet.Lines); err != nil {
				return problem(http.StatusForbidden, domain.CodeCountFourEyes, err.(*domain.Error).Detail)
			}
		}
		out, err = moveSheet(ctx, tx, sheetID, invevents.CountActionApproved, "", "",
			`status = 'APPROVED', approved_by = $3, approved_at = now()`, optionalText(tp.UserID))
		return err
	})
	return out, err
}

// RequestRecount flags one line of an OPEN, COUNTED or APPROVED sheet for a
// recount (CycleCountApprove): the sheet goes back to OPEN, any approval is
// cleared, and the line must be entered again before the sheet can be
// submitted or posted. The reason is required.
func (s *Postgres) RequestRecount(ctx context.Context, sheetID, lineID string, version int, reason string) (domain.CountSheet, error) {
	reason = strings.TrimSpace(reason)
	if n := len([]rune(reason)); n == 0 || n > 500 {
		return domain.CountSheet{}, problem(http.StatusBadRequest, domain.CodeInvalid, "a recount reason of 1 to 500 characters is required")
	}
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := lockedSheet(ctx, tx, sheetID, version)
		if err != nil {
			return err
		}
		if err := domain.RequireStatus(sheet.Status, domain.CountOpen, domain.CountCounted, domain.CountApproved); err != nil {
			return domainProblem(err)
		}
		tag, err := tx.Exec(ctx, `update count_sheet_line set recount_requested = true, recount_reason = $4, recount_by = $5,
			recount_rounds = recount_rounds + 1, recount_source = 'SUPERVISOR' where tenant_id = $1 and sheet_id = $2 and id = $3 and counted_qty is not null`,
			tp.TenantID, sheetID, lineID, reason, optionalText(tp.UserID))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			for _, l := range sheet.Lines {
				if l.ID == lineID {
					return problem(http.StatusConflict, domain.CodeCountSheetState, fmt.Sprintf("line %d has not been counted yet; there is nothing to recount", l.LineNo))
				}
			}
			return problem(http.StatusNotFound, domain.CodeCountNotFound, "count line not found on this sheet")
		}
		out, err = moveSheet(ctx, tx, sheetID, invevents.CountActionRecountRequested, lineID, reason,
			`status = 'OPEN', approved_by = null, approved_at = null, submitted_by = null, submitted_at = null`)
		return err
	})
	return out, err
}

// CancelCountSheet abandons a sheet that is not POSTED, lifting its freeze.
// Cancelling a CANCELLED sheet returns it unchanged.
func (s *Postgres) CancelCountSheet(ctx context.Context, sheetID string, version int) (domain.CountSheet, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.CountSheet
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		sheet, err := loadCountSheet(ctx, tx, sheetID, true)
		if err != nil {
			return err
		}
		if sheet.Status == domain.CountCancelled {
			out = sheet
			return nil
		}
		if err := domain.RequireStatus(sheet.Status, domain.CountOpen, domain.CountCounted, domain.CountApproved); err != nil {
			return domainProblem(err)
		}
		if sheet.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "count sheet changed since it was read; reread and retry")
		}
		out, err = moveSheet(ctx, tx, sheetID, invevents.CountActionCancelled, "", "",
			`status = 'CANCELLED', cancelled_by = $3, cancelled_at = now()`, optionalText(tp.UserID))
		return err
	})
	return out, err
}

// checkCountFreeze refuses a stock movement on a projection row held by an
// unfinished (OPEN, COUNTED or APPROVED) sheet of a freeze plan (INV-CNT-002).
// Every path that changes physical stock calls it: journal posting, transfer
// ship / receive, blocking and release, and status steps that move physical
// quantity. exemptSheet is the sheet whose own COUNT journal is being posted:
// that posting is what the freeze waits for. Any other COUNT journal, such as
// the reversal of an earlier count or another sheet's count, is refused like
// any movement.
//
// On a plan without a freeze the movement flags counted lines instead
// (flagMovedLines, BR-16.8). rowsLocked says whether the caller already holds
// on-hand row locks (or, for a count posting, its own sheet): such a caller
// must not wait for another sheet's lock, so it is refused with 409
// INV-CNT-009 when one is busy.
func checkCountFreeze(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, exemptSheet string, rowsLocked bool, rs []*row) error {
	if len(rs) == 0 {
		return nil
	}
	products := make([]string, 0, len(rs))
	hashes := make([][]byte, 0, len(rs))
	for _, r := range rs {
		products, hashes = append(products, r.product), append(hashes, r.hash)
	}
	var number string
	var dims map[string]string
	err := tx.QueryRow(ctx, `select s.number, d.dims
		from count_sheet_line l
		join count_sheet s on s.tenant_id = l.tenant_id and s.id = l.sheet_id
		join count_plan p on p.tenant_id = s.tenant_id and p.id = s.plan_id
		join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
		join unnest($3::uuid[], $4::bytea[]) as r(product, hash) on r.product = l.product_id and r.hash = l.dimension_set_hash
		where l.tenant_id = $1 and s.legal_entity_id = $2 and p.freeze_stock and s.status in ('OPEN', 'COUNTED', 'APPROVED')
		  and ($5 = '' or s.id::text <> $5)
		order by s.number limit 1`, tenantID, legalEntityID, products, hashes, exemptSheet).Scan(&number, &dims)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return flagMovedLines(ctx, tx, tenantID, legalEntityID, exemptSheet, rowsLocked, products, hashes)
		}
		return err
	}
	return problem(http.StatusConflict, domain.CodeCountFrozen,
		fmt.Sprintf("dimension set %s is locked by open count sheet %s; post or cancel the sheet first", describeDims(domain.Dimensions(dims)), number))
}

// movedRecountReason is the reason written on a line flagged because stock of
// its row moved after the line was counted.
const movedRecountReason = "Stock of this row moved after the line was counted; count it again"

// flagMovedLines applies BR-16.8 to plans without a freeze: a movement on a row
// whose line on an OPEN, COUNTED or APPROVED sheet was already counted flags
// that line for recount (source MOVEMENT), sends the sheet back to OPEN with any
// approval cleared and raises countsheet.changed (RECOUNT_REQUESTED), all in the
// movement's transaction. Uncounted lines are left alone (the counter will see
// the new quantity) and so is the sheet whose own COUNT journal is posting.
//
// Lock order: a journal posting calls it before any on-hand row lock, so it
// waits for the sheets in id order like every other sheet action. A caller that
// already holds row locks (transfers, blockings, status steps) or its own sheet
// (a count posting, which locks the sheet and then its rows) takes the sheet
// locks NOWAIT: waiting there would close a cycle with a count posting of the
// same rows (sheet, then rows), so a busy sheet is a retryable 409 instead.
func flagMovedLines(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, exemptSheet string, rowsLocked bool, products []string, hashes [][]byte) error {
	lock := `for update of s`
	if rowsLocked {
		lock += ` nowait`
	}
	rows, err := tx.Query(ctx, `select s.id::text from count_sheet s join count_plan p on p.tenant_id = s.tenant_id and p.id = s.plan_id
		where s.tenant_id = $1 and s.legal_entity_id = $2 and not p.freeze_stock and s.status in ('OPEN', 'COUNTED', 'APPROVED')
		  and ($5 = '' or s.id::text <> $5)
		  and exists (select 1 from count_sheet_line l
		       join unnest($3::uuid[], $4::bytea[]) as r(product, hash) on r.product = l.product_id and r.hash = l.dimension_set_hash
		       where l.tenant_id = s.tenant_id and l.sheet_id = s.id and l.counted_qty is not null and not l.recount_requested)
		order by s.id `+lock, tenantID, legalEntityID, products, hashes, exemptSheet)
	var sheetIDs []string
	if err == nil {
		sheetIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == lockNotAvailable {
			return problem(http.StatusConflict, domain.CodeCountBusy,
				"a count sheet holding this stock is being changed by another action and must be flagged for recount; retry")
		}
		return err
	}
	for _, id := range sheetIDs {
		// Rechecked under the sheet lock: a movement that committed while this
		// one waited may have flagged the lines already, and then the sheet
		// must not be reopened or announced a second time.
		tag, err := tx.Exec(ctx, `update count_sheet_line l set recount_requested = true, recount_reason = $5, recount_by = null,
			recount_rounds = l.recount_rounds + 1, recount_source = 'MOVEMENT'
			from unnest($3::uuid[], $4::bytea[]) as r(product, hash)
			where l.tenant_id = $1 and l.sheet_id = $2 and r.product = l.product_id and r.hash = l.dimension_set_hash
			  and l.counted_qty is not null and not l.recount_requested`, tenantID, id, products, hashes, movedRecountReason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if _, err := moveSheet(ctx, tx, id, invevents.CountActionRecountRequested, "", movedRecountReason,
			`status = 'OPEN', approved_by = null, approved_at = null, submitted_by = null, submitted_at = null`); err != nil {
			return err
		}
	}
	return nil
}

// orderedRows lists rows in the given key order.
func orderedRows(rows map[string]*row, order []string) []*row {
	out := make([]*row, 0, len(order))
	for _, k := range order {
		out = append(out, rows[k])
	}
	return out
}
