package memstore

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func (t *tenantData) invoiceBase(invoiceID string) (store.InvoiceBase, error) {
	inv, ok := t.invoiceDocs[invoiceID]
	if !ok {
		return store.InvoiceBase{}, store.ErrInvoiceNotFound
	}
	base := store.InvoiceBase{Invoice: inv}
	for i, l := range inv.Lines {
		base.Lines = append(base.Lines, domain.InvoiceLineState{LineNo: i + 1, Amount: l.Amount, StandardCost: l.StandardCost})
	}
	for _, adj := range t.adjustments {
		if adj.OriginalInvoiceID != invoiceID {
			continue
		}
		for _, l := range adj.Lines {
			state := &base.Lines[l.InvoiceLineNo-1]
			if adj.Kind == domain.AdjustmentCredit {
				state.Credited = state.Credited.Add(l.Amount)
			} else {
				state.Debited = state.Debited.Add(l.Amount)
			}
		}
	}
	return base, nil
}

func (s *Store) PostInvoiceAdjustment(ctx context.Context, in store.InvoiceAdjustment, fn store.InvoiceAdjustmentFunc) (out store.InvoiceAdjustment, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		for _, prior := range t.adjustments {
			if prior.OriginalInvoiceID == in.OriginalInvoiceID && strings.EqualFold(prior.DocumentNumber, in.DocumentNumber) {
				return store.DuplicateAdjustment(in.DocumentNumber)
			}
		}
		base, err := t.invoiceBase(in.OriginalInvoiceID)
		if err != nil {
			return err
		}
		if base.Invoice.LegalEntityID != in.LegalEntityID {
			return &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "legalEntityId differs from the legal entity of the original invoice"}
		}
		outcome, err := fn(base)
		if err != nil {
			return err
		}
		if err := s.markDone(ctx, in.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		in.VendorID, in.Currency = base.Invoice.VendorID, base.Invoice.Currency
		in.LedgerEntryID, in.Total, in.CreatedAt = outcome.LedgerEntryID, outcome.Total, time.Now().UTC()
		in.Lines = slices.Clone(in.Lines)
		t.adjustments[in.ID] = in
		s.events = append(s.events, outcome.Event)
		out = in
		return nil
	})
	return out, err
}

func settlementKey(kind, source string) string { return kind + "|" + source }

func (s *Store) PostSettlement(ctx context.Context, in store.Settlement, fn store.SettlementFunc) (out store.Settlement, replayed bool, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		if prior, ok := t.settlements[settlementKey(in.Kind, in.SourceRef)]; ok {
			out, replayed = prior, true
			return nil
		}
		outcome, err := fn()
		if err != nil {
			return err
		}
		if err := s.markDone(ctx, in.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		in.LedgerEntryID, in.CreatedAt = outcome.LedgerEntryID, time.Now().UTC()
		t.settlements[settlementKey(in.Kind, in.SourceRef)] = in
		s.events = append(s.events, outcome.Event)
		out = in
		return nil
	})
	return out, replayed, err
}

func (s *Store) SettledSources(ctx context.Context, kind string, sources []string) (settled map[string]struct{}, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		settled = map[string]struct{}{}
		for _, source := range sources {
			if _, ok := t.settlements[settlementKey(kind, source)]; ok {
				settled[source] = struct{}{}
			}
		}
		return nil
	})
	return settled, err
}
