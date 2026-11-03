package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var ErrInvalidEvent = errors.New("invalid payables.openitem.settled event")

type reportStore interface {
	MarkReimbursed(ctx context.Context, d reimbursement.OpenItemSettled) (bool, error)
}

// HandleOpenItemSettled marks the expense report whose AP open item a payment run settled
// as reimbursed. Partial settlements and open items of other documents are ignored.
func HandleOpenItemSettled(ctx context.Context, reports reportStore, evt events.Event) error {
	if evt.Type != reimbursement.OpenItemSettledType {
		return fmt.Errorf("%w: type %q", ErrInvalidEvent, evt.Type)
	}
	var d reimbursement.OpenItemSettled
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	if d.Status != reimbursement.StatusSettled {
		return nil
	}
	for field, id := range map[string]string{"tenant_id": evt.TenantID, "open_item_id": d.OpenItemID, "legal_entity_id": d.LegalEntityID, "vendor_id": d.VendorID, "source_invoice_id": d.SourceInvoiceID} {
		if !ids.Valid(id) {
			return fmt.Errorf("%w: %s %q is not a uuid", ErrInvalidEvent, field, id)
		}
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: evt.TenantID, LegalEntityID: d.LegalEntityID, UserID: "system:payables.openitem.settled"})
	_, err := reports.MarkReimbursed(ctx, d)
	return err
}
