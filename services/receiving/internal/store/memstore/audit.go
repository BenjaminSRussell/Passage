package memstore

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func (s *Store) InvoiceVendor(ctx context.Context, id string) (vendorID string, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		canonical, valid := ids.Canonical(id)
		inv, ok := t.invoiceDocs[canonical]
		if !valid || !ok {
			return store.ErrInvoiceNotFound
		}
		vendorID = inv.VendorID
		return nil
	})
	return vendorID, err
}

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

func (s *Store) SetAuditPolicy(ctx context.Context, legalEntityID string, policy domain.AuditPolicy, by string, fn store.AuditPolicyFunc) error {
	return s.with(ctx, func(t *tenantData) error {
		old := t.auditRates[legalEntityID]
		policy.RuleVersion = old.RuleVersion
		if old.RuleVersion == 0 || !old.RatePct.Equal(policy.RatePct) || !old.Threshold.Equal(policy.Threshold) || old.ThresholdCurrency != policy.ThresholdCurrency {
			policy.RuleVersion++
		}
		evt, err := fn(old, policy)
		if err != nil {
			return err
		}
		t.policyHistory = append(t.policyHistory, PolicyChange{Kind: "audit", LegalEntityID: legalEntityID, Old: old.RatePct.String(), New: policy.RatePct.String(), By: by})
		t.auditRates[legalEntityID] = policy
		s.events = append(s.events, evt)
		return nil
	})
}

func (s *Store) AuditPolicy(ctx context.Context, legalEntityID string) (policy domain.AuditPolicy, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		policy = t.auditRates[legalEntityID]
		return nil
	})
	return policy, err
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

func (s *Store) ConfirmErsPrice(ctx context.Context, legalEntityID, receiptID string, lineNo int, price decimal.Decimal, by string, fn store.ErsPriceFunc) error {
	return s.with(ctx, func(t *tenantData) error {
		rec, err := t.find(receiptID)
		if err != nil || rec.LegalEntityID != legalEntityID || !slices.ContainsFunc(rec.Lines, func(l store.Line) bool { return l.LineNo == lineNo }) {
			return store.ErrNotFound
		}
		if t.ersPrices == nil {
			t.ersPrices = map[string]store.PriceConfirmation{}
		}
		key := fmt.Sprintf("%s|%d", rec.ID, lineNo)
		var old decimal.NullDecimal
		if prior, ok := t.ersPrices[key]; ok {
			old = decimal.NullDecimal{Decimal: prior.Price, Valid: true}
		}
		evt, err := fn(old)
		if err != nil {
			return err
		}
		t.ersPrices[key] = store.PriceConfirmation{Price: price, By: by}
		t.ersPriceHistory = append(t.ersPriceHistory, key+"|"+old.Decimal.String()+">"+price.String()+"|"+by)
		s.events = append(s.events, evt)
		return nil
	})
}

func (s *Store) ErsPriceConfirmations(ctx context.Context, receiptID string) (map[int]store.PriceConfirmation, error) {
	out := map[int]store.PriceConfirmation{}
	err := s.with(ctx, func(t *tenantData) error {
		rec, err := t.find(receiptID)
		if err != nil {
			return nil
		}
		for _, l := range rec.Lines {
			if c, ok := t.ersPrices[fmt.Sprintf("%s|%d", rec.ID, l.LineNo)]; ok {
				out[l.LineNo] = c
			}
		}
		return nil
	})
	return out, err
}
