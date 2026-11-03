package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

func HandleExpenseReportApproved(ctx context.Context, items *store.Postgres, evt events.Event) error {
	if evt.Type != reimbursement.ReportApprovedType {
		return fmt.Errorf("%w: type %q", ErrInvalidEvent, evt.Type)
	}
	var d reimbursement.ReportApproved
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	for field, id := range map[string]string{"tenant_id": evt.TenantID, "reportId": d.ReportID, "legalEntityId": d.LegalEntityID, "workerId": d.WorkerID} {
		if !uuidPattern.MatchString(id) {
			return fmt.Errorf("%w: %s %q is not a uuid", ErrInvalidEvent, field, id)
		}
	}
	if !currencyPattern.MatchString(d.Currency) {
		return fmt.Errorf("%w: currency %q", ErrInvalidEvent, d.Currency)
	}
	amount, err := decimal.NewFromString(d.Amount)
	if err != nil || !amount.IsPositive() || !amount.Equal(amount.Round(amountScale)) {
		return fmt.Errorf("%w: amount %q", ErrInvalidEvent, d.Amount)
	}
	if _, err := time.Parse(time.DateOnly, d.ApprovedOn); err != nil {
		return fmt.Errorf("%w: approvedOn %q", ErrInvalidEvent, d.ApprovedOn)
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: evt.TenantID, LegalEntityID: d.LegalEntityID, UserID: "system:expense.report.approved"})
	_, _, err = items.CreateFromInvoice(ctx, store.OpenItem{
		LegalEntityID: d.LegalEntityID, VendorID: d.WorkerID, SourceInvoiceID: d.ReportID, InvoiceNumber: reimbursement.InvoicePrefix + d.ReportID[:8],
		InvoiceDate: d.ApprovedOn, DueDate: d.ApprovedOn, DueDateSource: store.DueDateDefaulted, Currency: d.Currency, Amount: amount, DiscountAmount: decimal.Zero, PayeeUnverified: true,
	})
	return err
}
