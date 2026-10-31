package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
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
		LineID: optionalText(lineID), Reason: optionalText(reason)}
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
			recount_rounds = recount_rounds + 1 where tenant_id = $1 and sheet_id = $2 and id = $3 and counted_qty is not null`,
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
func checkCountFreeze(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, exemptSheet string, rs []*row) error {
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
			return nil
		}
		return err
	}
	return problem(http.StatusConflict, domain.CodeCountFrozen,
		fmt.Sprintf("dimension set %s is locked by open count sheet %s; post or cancel the sheet first", describeDims(domain.Dimensions(dims)), number))
}

// orderedRows lists rows in the given key order.
func orderedRows(rows map[string]*row, order []string) []*row {
	out := make([]*row, 0, len(order))
	for _, k := range order {
		out = append(out, rows[k])
	}
	return out
}
