package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func (s *Store) ReleaseInvoice(ctx context.Context, id, legalEntityID, by string, fn store.ReleaseFunc) (out store.Invoice, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		inv, ok := t.invoiceDocs[id]
		if !ok {
			return store.ErrInvoiceNotFound
		}
		if err := store.CheckRelease(inv, legalEntityID, by); err != nil {
			return err
		}
		inv.Lines = slices.Clone(inv.Lines)
		outcome, err := fn(inv)
		if err != nil {
			return err
		}
		if err := s.markDone(ctx, id, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		now := time.Now().UTC()
		inv.LedgerEntryID, inv.Status, inv.ReleasedBy, inv.ReleasedAt = outcome.LedgerEntryID, domain.InvoiceStatusPosted, by, &now
		t.invoiceDocs[id] = inv
		s.events = append(s.events, outcome.Event)
		out = inv
		return nil
	})
	return out, err
}

func policyKey(legalEntityID, subject string) string { return legalEntityID + "|" + subject }

func (s *Store) RejectInvoice(ctx context.Context, id, legalEntityID, by, reason string, fn store.RejectFunc) (out store.Invoice, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		inv, ok := t.invoiceDocs[id]
		if !ok {
			return store.ErrInvoiceNotFound
		}
		if err := store.CheckRelease(inv, legalEntityID, by); err != nil {
			return err
		}
		evt, err := fn(inv)
		if err != nil {
			return err
		}
		for _, l := range inv.Lines {
			g := &t.grir[t.grirFor(l.ReceiptID, l.ReceiptLineNo)]
			g.InvoicedQty, g.ClearedAmount = g.InvoicedQty.Sub(l.Qty), g.ClearedAmount.Sub(l.GrirCleared)
			g.Status = grirStatus(g.InvoicedQty, g.ReceivedQty)
			r := t.receipts[l.ReceiptID]
			for i := range r.Lines {
				if r.Lines[i].LineNo == l.ReceiptLineNo {
					r.Lines[i].InvoicedQty = r.Lines[i].InvoicedQty.Sub(l.Qty)
				}
			}
			t.receipts[l.ReceiptID] = r
		}
		now := time.Now().UTC()
		inv.Status, inv.RejectedBy, inv.RejectedAt, inv.RejectReason = domain.InvoiceStatusAuditRejected, by, &now, reason
		t.invoiceDocs[id] = inv
		s.events = append(s.events, evt)
		out = inv
		return nil
	})
	return out, err
}

func (s *Store) SetErsVendor(ctx context.Context, legalEntityID, vendorID string, enabled bool, _ string, fn store.ErsVendorFunc) error {
	return s.with(ctx, func(t *tenantData) error {
		evt, err := fn(t.ersVendors[policyKey(legalEntityID, vendorID)])
		if err != nil {
			return err
		}
		t.ersVendors[policyKey(legalEntityID, vendorID)] = enabled
		s.events = append(s.events, evt)
		return nil
	})
}

func (s *Store) ErsVendor(ctx context.Context, legalEntityID, vendorID string) (enabled bool, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		enabled = t.ersVendors[policyKey(legalEntityID, vendorID)]
		return nil
	})
	return enabled, err
}

func (s *Store) SetAuditRate(ctx context.Context, legalEntityID string, ratePct decimal.Decimal, _ string, fn store.AuditRateFunc) error {
	return s.with(ctx, func(t *tenantData) error {
		evt, err := fn(t.auditRates[legalEntityID])
		if err != nil {
			return err
		}
		t.auditRates[legalEntityID] = ratePct
		s.events = append(s.events, evt)
		return nil
	})
}

func (s *Store) AuditRate(ctx context.Context, legalEntityID string) (rate decimal.Decimal, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		rate = t.auditRates[legalEntityID]
		return nil
	})
	return rate, err
}
