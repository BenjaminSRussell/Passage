package memstore

import (
	"context"
	"fmt"
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

func (s *Store) AcceptSheet(ctx context.Context, id, by string, fn store.SheetAcceptFunc) (out store.ServiceSheet, err error) {
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
		sheet.Status, sheet.DecidedBy, sheet.DecidedAt, sheet.RejectReason = domain.SheetAccepted, by, &now, ""
		allocated := store.Receipt{ID: ids.New()}
		outcome, err := fn(sheet, allocated)
		if err != nil {
			return err
		}
		number := fmt.Sprintf("RCV-%08d", t.counter+1)
		rec := cloneReceipt(outcome.Receipt)
		if rec.ID == "" {
			rec.ID = allocated.ID
		}
		if rec.ExchangeRate.Sign() == 0 {
			rec.ExchangeRate = decimal.NewFromInt(1)
		}
		if rec.Source == "" {
			rec.Source = domain.ReceiptSourceService
		}
		rec.Number, rec.State, rec.Version, rec.CreatedAt = number, domain.Posted, 1, now
		t.counter++
		posted := now
		rec.PostingDate, rec.PostedAt, rec.PostedBy, rec.LedgerEntryID = &rec.Date, &posted, by, outcome.LedgerEntryID
		amounts := map[int]store.LineAmount{}
		for _, a := range outcome.Amounts {
			amounts[a.LineNo] = a
		}
		var items []store.GrirItem
		for i := range rec.Lines {
			l := &rec.Lines[i]
			if l.LineNo == 0 {
				l.LineNo = i + 1
			}
			if l.Currency == "" {
				l.Currency = rec.Currency
			}
			a := amounts[l.LineNo]
			l.Amount, l.AmountAcct = decimal.NullDecimal{Decimal: a.Amount, Valid: true}, decimal.NullDecimal{Decimal: a.AmountAcct, Valid: true}
			items = append(items, store.GrirItem{
				ID: ids.New(), ReceiptID: rec.ID, ReceiptNumber: rec.Number, LineNo: l.LineNo, VendorID: rec.VendorID, POLineID: l.POLineID,
				ReceivedQty: l.Qty, ReceivedAmount: a.Amount, OpenAmount: a.Amount, Currency: l.Currency, UnitPrice: l.UnitPrice, Status: "OPEN", CreatedAt: now,
			})
		}
		t.receipts[rec.ID] = rec
		t.order = append(t.order, rec.ID)
		t.grir = append(t.grir, items...)
		if err := s.markDone(ctx, rec.ID, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		if err := s.markDone(ctx, id, outcome.LedgerEntryID != ""); err != nil {
			return err
		}
		sheet.ReceiptID, sheet.LedgerEntryID = rec.ID, outcome.LedgerEntryID
		t.sheets[canonical] = sheet
		accepted := outcome.Events
		if outcome.PostedEvent != nil {
			postedEvt, err := outcome.PostedEvent(number)
			if err != nil {
				return err
			}
			accepted = append(slices.Clone(accepted), postedEvt)
		}
		s.events = append(s.events, accepted...)
		out = cloneSheet(sheet)
		return nil
	})
	return out, err
}

func (s *Store) putSelfBill(t *tenantData, in store.Invoice, build store.SelfBillBuild) error {
	if build == nil {
		return nil
	}
	if t.selfBills == nil {
		t.selfBills = map[string]store.SelfBill{}
	}
	if t.selfBillSeq == nil {
		t.selfBillSeq = map[string]int{}
	}
	doc, evt, err := build(store.SelfBillNumber(t.selfBillSeq[in.LegalEntityID] + 1))
	if err != nil {
		return err
	}
	t.selfBillSeq[in.LegalEntityID]++
	if doc.ContentType == "" {
		doc.ContentType = "application/pdf"
	}
	doc.CreatedAt = time.Now().UTC()
	doc.Body = slices.Clone(doc.Body)
	t.selfBills[in.ID] = doc
	s.events = append(s.events, evt)
	return nil
}

func (s *Store) GetSelfBill(ctx context.Context, invoiceID string) (out store.SelfBill, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		canonical, valid := ids.Canonical(invoiceID)
		doc, ok := t.selfBills[canonical]
		if !valid {
			doc, ok = t.selfBills[invoiceID]
		}
		if !ok {
			return store.ErrSelfBillNotFound
		}
		out = doc
		out.Body = slices.Clone(doc.Body)
		return nil
	})
	return out, err
}
