package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	kevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/events"
)

var ErrSettlementMismatch = errors.New("AP settlement does not match the approved expense report")

// DecideReport approves or rejects a SUBMITTED report. The user who submitted it may not
// decide it (EXP-SOD-001). An approval with a reimbursable amount commits
// dev.passage.expense.report.approved.v1 to the outbox in the same transaction, with the
// amount read from the stored report; payables turns it into the employee's vendor open
// item. A report with nothing to reimburse goes straight to PAID.
func (s *Store) DecideReport(ctx context.Context, id string, approve bool, decidedOn time.Time, c Command) (Report, bool, error) {
	p, err := decider(ctx)
	if err != nil {
		return Report{}, false, err
	}
	var out Report
	replayed := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, _, err := lockReport(ctx, tx, p.TenantID, id); err != nil {
			return err
		}
		cur, err := loadReport(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		found, err := priorCommand(ctx, tx, p.TenantID, c)
		if err != nil {
			return err
		}
		if found {
			replayed, out = true, cur
			return nil
		}
		if err := checkVersion(c, cur.Version, CodeVersionConflict, "report"); err != nil {
			return err
		}
		if cur.Status != string(domain.ReportSubmitted) {
			return httpx.NewProblem(http.StatusConflict, CodeReportState, "report is "+cur.Status+" and cannot be decided")
		}
		if cur.SubmittedBy == "" || cur.CreatedBy == "" {
			return httpx.NewProblem(http.StatusForbidden, CodeOwnDecision, "the report does not record who created and submitted it, so it cannot be decided")
		}
		if p.UserID == cur.SubmittedBy || p.UserID == cur.CreatedBy {
			return ownDecision("report")
		}
		target := domain.ReportRejected
		payNow := approve && !cur.Reimbursable.IsPositive()
		switch {
		case payNow:
			target = domain.ReportPaid
		case approve:
			target = domain.ReportApproved
		}
		if _, err := tx.Exec(ctx, `update expense_report set status=$3::text, decided_by=$4, decided_at=now(),
			reimbursed_at=case when $3::text = 'PAID' then now() end, updated_at=now(), version=version+1
			where tenant_id=$1 and id=$2`, p.TenantID, id, string(target), p.UserID); err != nil {
			return err
		}
		if target == domain.ReportApproved {
			evt, err := kevents.New(reimbursement.ReportApprovedType, events.Source, id, p.TenantID, reimbursement.ReportApproved{
				ReportID: id, LegalEntityID: cur.LegalEntityID, WorkerID: cur.WorkerID, Currency: cur.Currency,
				Amount: cur.Reimbursable.StringFixed(money.MinorUnits(cur.Currency)), ApprovedOn: decidedOn.Format(time.DateOnly),
			})
			if err != nil {
				return err
			}
			if err := outbox.Enqueue(ctx, tx, outboxAggregate, id, evt); err != nil {
				return err
			}
		}
		if err := remember(ctx, tx, p.TenantID, c.Key, c.Hash, http.StatusOK, map[string]string{"reportId": id}); err != nil {
			return err
		}
		out, err = loadReport(ctx, tx, p.TenantID, id)
		return err
	})
	if err != nil {
		return Report{}, false, err
	}
	return out, replayed, nil
}

// MarkReimbursed marks the APPROVED report that d's source document names as PAID once
// payables has settled its open item in full. An event for a document that is not an
// expense report, or for a report already PAID, changes nothing; an event that
// disagrees with the stored report on worker, legal entity or currency, or leaves a negative or larger-than-reimbursable open amount, is refused; a partial settlement (open amount above zero) waits for the rest.
func (s *Store) MarkReimbursed(ctx context.Context, d reimbursement.OpenItemSettled) (bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return false, err
	}
	marked := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanReport(tx.QueryRow(ctx, `select `+reportColumns+` from expense_report where tenant_id=$1 and id=$2 for update`, p.TenantID, d.SourceInvoiceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if cur.Status == string(domain.ReportPaid) {
			return nil
		}
		open, err := decimal.NewFromString(d.OpenAmount)
		if err != nil || cur.Status != string(domain.ReportApproved) || cur.WorkerID != d.VendorID || cur.LegalEntityID != d.LegalEntityID ||
			cur.Currency != d.Currency || open.IsNegative() || open.GreaterThan(cur.Reimbursable) {
			return ErrSettlementMismatch
		}
		if open.IsPositive() {
			return nil
		}
		marked = true
		_, err = tx.Exec(ctx, `update expense_report set status=$3, ap_open_item_id=$4::uuid, reimbursed_at=now(), updated_at=now(), version=version+1
			where tenant_id=$1 and id=$2`, p.TenantID, cur.ID, string(domain.ReportPaid), d.OpenItemID)
		return err
	})
	return marked, err
}
