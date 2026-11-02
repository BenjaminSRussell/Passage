package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func cloneSheet(s store.ServiceSheet) store.ServiceSheet {
	s.Lines = slices.Clone(s.Lines)
	s.DecidedAt = clonePtr(s.DecidedAt)
	return s
}

func (s *Store) CreateSheet(ctx context.Context, in store.ServiceSheet, limits map[int]decimal.Decimal) (out store.ServiceSheet, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		var existing []store.ServiceSheet
		for _, sheet := range t.sheets {
			if sheet.PurchaseOrderID == in.PurchaseOrderID {
				existing = append(existing, sheet)
			}
		}
		if err := store.CheckSheetLimits(in, existing, limits); err != nil {
			return err
		}
		in.Status, in.CreatedAt = domain.SheetPending, time.Now().UTC()
		t.sheets[in.ID] = cloneSheet(in)
		out = cloneSheet(in)
		return nil
	})
	return out, err
}

func (s *Store) GetSheet(ctx context.Context, id string) (out store.ServiceSheet, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		canonical, valid := ids.Canonical(id)
		sheet, ok := t.sheets[canonical]
		if !valid || !ok {
			return store.ErrSheetNotFound
		}
		out = cloneSheet(sheet)
		return nil
	})
	return out, err
}

func (s *Store) DecideSheet(ctx context.Context, id, by string, accept bool, reason string, fn store.SheetDecisionFunc) (out store.ServiceSheet, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		canonical, valid := ids.Canonical(id)
		sheet, ok := t.sheets[canonical]
		if !valid || !ok {
			return store.ErrSheetNotFound
		}
		if err := store.CheckSheetDecision(sheet, by); err != nil {
			return err
		}
		now := time.Now().UTC()
		sheet = cloneSheet(sheet)
		sheet.Status, sheet.DecidedBy, sheet.DecidedAt, sheet.RejectReason = domain.SheetRejected, by, &now, reason
		if accept {
			sheet.Status, sheet.RejectReason = domain.SheetAccepted, ""
		}
		evt, err := fn(sheet)
		if err != nil {
			return err
		}
		t.sheets[canonical] = sheet
		s.events = append(s.events, evt)
		out = cloneSheet(sheet)
		return nil
	})
	return out, err
}
