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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/events"
)

var ErrSettlementMismatch = errors.New("AP settlement does not match the approved expense report")

// DecideReport approves or rejects a SUBMITTED report. The user who submitted it may not
// decide it (EXP-SOD-001). An approval with a reimbursable amount records decidedOn as the
// approval date and waits for its general ledger posting (MarkGLBooked), which publishes
// dev.passage.expense.report.approved.v1 for payables. A report with nothing to reimburse
// goes straight to PAID.
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
			return httpx.NewProblem(http.StatusForbidden, CodeDeciderUnknown, "the report does not record who created and submitted it, so it cannot be decided")
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
			approved_on=case when $3::text <> 'REJECTED' then $5::date end,
			reimbursed_at=case when $3::text = 'PAID' then now() end, updated_at=now(), version=version+1
			where tenant_id=$1 and id=$2`, p.TenantID, id, string(target), p.UserID, decidedOn.Format(time.DateOnly)); err != nil {
			return err
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
// disagrees with the stored report on worker (payee_worker_id when payables paid the vendor
// the worker is registered to, else vendor_id), legal entity or currency, reports a fully paid item that is not SETTLED, or leaves a negative or larger-than-reimbursable open amount, is refused; a partial settlement (open amount above zero) waits for the rest.
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
		payee := d.PayeeWorkerID
		if payee == "" {
			payee = d.VendorID
		}
		open, err := decimal.NewFromString(d.OpenAmount)
		if err != nil || cur.Status != string(domain.ReportApproved) || cur.WorkerID != payee || cur.LegalEntityID != d.LegalEntityID ||
			cur.Currency != d.Currency || open.IsNegative() || open.GreaterThan(cur.Reimbursable) {
			return ErrSettlementMismatch
		}
		if open.IsPositive() {
			return nil
		}
		if d.Status != reimbursement.StatusSettled {
			return ErrSettlementMismatch
		}
		marked = true
		_, err = tx.Exec(ctx, `update expense_report set status=$3, ap_open_item_id=$4::uuid, reimbursed_at=now(), updated_at=now(), version=version+1
			where tenant_id=$1 and id=$2`, p.TenantID, cur.ID, string(domain.ReportPaid), d.OpenItemID)
		return err
	})
	return marked, err
}

// GLReport is an approved report whose reimbursement liability is not in the general ledger yet.
type GLReport struct {
	ID            string
	LegalEntityID string
	WorkerID      string
	Currency      string
	Status        string
	Reimbursable  decimal.Decimal
	ApprovedOn    string
	// ByCategory sums the employee-paid lines per category: the debits of the journal.
	ByCategory map[string]decimal.Decimal
}

// DueGLReports returns up to limit reports of the legal entity, oldest approval first, that are
// APPROVED (or PAID before this posting existed) with a reimbursable amount and no journal entry,
// with their employee-paid lines summed per category. more reports a further page is due.
func (s *Store) DueGLReports(ctx context.Context, legalEntityID string, limit int) ([]GLReport, bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, false, err
	}
	if !tenant.InLegalEntityScope(p, legalEntityID) {
		return nil, false, outOfScope()
	}
	var out []GLReport
	more := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, worker_id::text, currency::text, status, reimbursable_amount::text, approved_on::text
			from expense_report where tenant_id=$1 and legal_entity_id=$2 and gl_entry_id is null and reimbursable_amount > 0
			and status in ('APPROVED', 'PAID') and approved_on is not null order by approved_on, id limit $3`, p.TenantID, legalEntityID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		byID := map[string]int{}
		ids := []string{}
		for rows.Next() {
			var g GLReport
			var amount string
			if err := rows.Scan(&g.ID, &g.LegalEntityID, &g.WorkerID, &g.Currency, &g.Status, &amount, &g.ApprovedOn); err != nil {
				return err
			}
			if g.Reimbursable, err = decimal.NewFromString(amount); err != nil {
				return err
			}
			if len(out) == limit {
				more = true
				break
			}
			g.ByCategory = map[string]decimal.Decimal{}
			byID[g.ID] = len(out)
			ids = append(ids, g.ID)
			out = append(out, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		lines, err := tx.Query(ctx, `select report_id::text, category, sum(amount)::text from expense_line
			where tenant_id=$1 and report_id = any($2::uuid[]) and paid_by = $3 group by report_id, category`, p.TenantID, ids, domain.PaidByEmployee)
		if err != nil {
			return err
		}
		defer lines.Close()
		for lines.Next() {
			var id, category, amount string
			if err := lines.Scan(&id, &category, &amount); err != nil {
				return err
			}
			sum, err := decimal.NewFromString(amount)
			if err != nil {
				return err
			}
			out[byID[id]].ByCategory[category] = sum
		}
		return lines.Err()
	})
	return out, more, err
}

// MarkGLBooked records the journal entry of the report's liability. For an APPROVED report it
// commits dev.passage.expense.report.approved.v1 to the outbox in the same transaction, with the
// stored reimbursable amount and approval date, so payables creates the employee's open item only
// once the AP control account carries the credit its payment will debit. A report already booked
// changes nothing and returns false.
func (s *Store) MarkGLBooked(ctx context.Context, id, entryID string) (bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return false, err
	}
	booked := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var le, worker, currency, status, amount, approvedOn string
		err := tx.QueryRow(ctx, `update expense_report set gl_entry_id=$3, gl_posted_at=now(), updated_at=now()
			where tenant_id=$1 and id=$2 and gl_entry_id is null and approved_on is not null
			returning legal_entity_id::text, worker_id::text, currency::text, status, reimbursable_amount::text, approved_on::text`, p.TenantID, id, entryID).
			Scan(&le, &worker, &currency, &status, &amount, &approvedOn)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		booked = true
		if status != string(domain.ReportApproved) {
			return nil
		}
		reimbursable, err := decimal.NewFromString(amount)
		if err != nil {
			return err
		}
		evt, err := kevents.New(reimbursement.ReportApprovedType, events.Source, id, p.TenantID, reimbursement.ReportApproved{
			ReportID: id, LegalEntityID: le, WorkerID: worker, Currency: currency,
			Amount: reimbursable.StringFixed(money.MinorUnits(currency)), ApprovedOn: approvedOn,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, outboxAggregate, id, evt)
	})
	return booked, err
}
