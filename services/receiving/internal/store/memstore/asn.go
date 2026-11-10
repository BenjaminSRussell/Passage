package memstore

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func cloneAsn(a store.Asn) store.Asn {
	a.Lines = slices.Clone(a.Lines)
	return a
}

func (t *tenantData) asnInScope(principal tenant.Principal, id string) (store.Asn, bool) {
	canonical, valid := ids.Canonical(id)
	a, ok := t.asns[canonical]
	return a, valid && ok && tenant.InLegalEntityScope(principal, a.LegalEntityID)
}

func (s *Store) CreateAsn(ctx context.Context, in store.Asn, limits map[int]decimal.Decimal) (out store.Asn, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		var existing []store.Asn
		for _, a := range t.asns {
			if a.PurchaseOrderID == in.PurchaseOrderID {
				existing = append(existing, a)
			}
			if in.TrackingNumber != "" && a.VendorID == in.VendorID && a.TrackingNumber == in.TrackingNumber {
				return store.AsnTrackingTaken(in.TrackingNumber, a.ID)
			}
		}
		if err := store.CheckAsnLimits(in, existing, limits); err != nil {
			return err
		}
		in.Status, in.CreatedAt = domain.AsnOpen, time.Now().UTC()
		t.asns[in.ID] = cloneAsn(in)
		out = cloneAsn(in)
		return nil
	})
	return out, err
}

func (s *Store) GetAsn(ctx context.Context, id string) (out store.Asn, err error) {
	principal, _ := tenant.FromContext(ctx)
	err = s.with(ctx, func(t *tenantData) error {
		a, ok := t.asnInScope(principal, id)
		if !ok {
			return store.ErrAsnNotFound
		}
		out = cloneAsn(a)
		return nil
	})
	return out, err
}

func (s *Store) ListAsns(ctx context.Context, f store.AsnFilter) (out []store.Asn, err error) {
	limit := pageLimit(f.Limit)
	principal, _ := tenant.FromContext(ctx)
	err = s.with(ctx, func(t *tenantData) error {
		for _, a := range t.asns {
			if !tenant.InLegalEntityScope(principal, a.LegalEntityID) || (f.VendorID != "" && !strings.EqualFold(a.VendorID, f.VendorID)) ||
				(f.PurchaseOrderID != "" && !strings.EqualFold(a.PurchaseOrderID, f.PurchaseOrderID)) || skipForCursor(a.CreatedAt, a.ID, f.CursorCreatedAt, f.CursorID) {
				continue
			}
			out = append(out, cloneAsn(a))
		}
		slices.SortFunc(out, func(a, b store.Asn) int {
			return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(b.ID, a.ID))
		})
		out = out[:min(len(out), limit+1)]
		return nil
	})
	return out, err
}

func (s *Store) StartAsnReceipt(ctx context.Context, id string, rec store.Receipt) (asnOut store.Asn, out store.Receipt, err error) {
	principal, _ := tenant.FromContext(ctx)
	err = s.with(ctx, func(t *tenantData) error {
		a, ok := t.asnInScope(principal, id)
		if !ok {
			return store.ErrAsnNotFound
		}
		if a.Status != domain.AsnOpen {
			return store.AsnConsumed(a)
		}
		r, err := t.newArrival(rec)
		if err != nil {
			return err
		}
		t.commitArrival(r)
		a = cloneAsn(a)
		a.Status, a.ReceiptID = domain.AsnReceiving, r.ID
		t.asns[a.ID] = a
		asnOut, out = cloneAsn(a), cloneReceipt(r)
		return nil
	})
	return asnOut, out, err
}
