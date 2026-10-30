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

func (s *Store) SetErsVendor(ctx context.Context, legalEntityID, vendorID string, enabled bool, _ string) error {
	return s.with(ctx, func(t *tenantData) error {
		t.ersVendors[policyKey(legalEntityID, vendorID)] = enabled
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

func (s *Store) SetAuditRate(ctx context.Context, legalEntityID string, ratePct decimal.Decimal, _ string) error {
	return s.with(ctx, func(t *tenantData) error {
		t.auditRates[legalEntityID] = ratePct
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
