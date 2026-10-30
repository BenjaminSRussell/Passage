package memstore

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func (s *Store) ReleaseInvoice(ctx context.Context, id, legalEntityID, by string, fn store.ReleaseFunc) (out store.Invoice, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		canonical, valid := ids.Canonical(id)
		inv, ok := t.invoiceDocs[canonical]
		if !valid || !ok {
			return store.ErrInvoiceNotFound
		}
		id = canonical
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
		canonical, valid := ids.Canonical(id)
		inv, ok := t.invoiceDocs[canonical]
		if !valid || !ok {
			return store.ErrInvoiceNotFound
		}
		id = canonical
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

func (s *Store) SetErsVendor(ctx context.Context, legalEntityID, vendorID string, enabled bool, by string, fn store.ErsVendorFunc) error {
	return s.with(ctx, func(t *tenantData) error {
		old := t.ersVendors[policyKey(legalEntityID, vendorID)]
		evt, err := fn(old)
		if err != nil {
			return err
		}
		t.policyHistory = append(t.policyHistory, PolicyChange{Kind: "ers", LegalEntityID: legalEntityID, Subject: vendorID, Old: strconv.FormatBool(old), New: strconv.FormatBool(enabled), By: by})
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

func (s *Store) SetAuditRate(ctx context.Context, legalEntityID string, ratePct decimal.Decimal, by string, fn store.AuditRateFunc) error {
	return s.with(ctx, func(t *tenantData) error {
		old := t.auditRates[legalEntityID]
		evt, err := fn(old)
		if err != nil {
			return err
		}
		t.policyHistory = append(t.policyHistory, PolicyChange{Kind: "audit", LegalEntityID: legalEntityID, Old: old.String(), New: ratePct.String(), By: by})
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

func (s *Store) RejectedInvoiceCount(ctx context.Context, vendorID, number string) (n int, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		base := strings.ToLower(number)
		for _, inv := range t.invoiceDocs {
			name := strings.ToLower(inv.InvoiceNumber)
			if inv.Status == domain.InvoiceStatusAuditRejected && inv.VendorID == vendorID && (name == base || strings.HasPrefix(name, base+"-r")) {
				n++
			}
		}
		return nil
	})
	return n, err
}

type PolicyChange struct {
	Kind, LegalEntityID, Subject, Old, New, By string
}

func (s *Store) PolicyHistory(ctx context.Context) (out []PolicyChange, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		out = slices.Clone(t.policyHistory)
		return nil
	})
	return out, err
}
