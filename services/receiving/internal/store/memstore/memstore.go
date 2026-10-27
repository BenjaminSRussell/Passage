package memstore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
	keyLifetime      = 24 * time.Hour
)

var _ store.Store = (*Store)(nil)

type claim struct {
	hash        string
	status      int
	body        []byte
	completed   bool
	lockedUntil time.Time
	expiresAt   time.Time
}

type tenantData struct {
	receipts map[string]store.Receipt
	order    []string
	serials  map[domain.SerialKey]struct{}
	grir     []store.GrirItem
	keys     map[string]*claim
	invoices map[string]struct{}
	counter  int
}

type Store struct {
	mu      sync.Mutex
	tenants map[string]*tenantData
	events  []events.Event
}

func New() *Store { return &Store{tenants: map[string]*tenantData{}} }

func (s *Store) Events() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

func (s *Store) with(ctx context.Context, fn func(*tenantData) error) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("memstore: no principal in context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tenants[p.TenantID]
	if t == nil {
		t = &tenantData{receipts: map[string]store.Receipt{}, serials: map[domain.SerialKey]struct{}{}, keys: map[string]*claim{}, invoices: map[string]struct{}{}}
		s.tenants[p.TenantID] = t
	}
	return fn(t)
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneReceipt(r store.Receipt) store.Receipt {
	r.PalletCount, r.PostingDate, r.PostedAt = clonePtr(r.PalletCount), clonePtr(r.PostingDate), clonePtr(r.PostedAt)
	r.Lines = slices.Clone(r.Lines)
	for i := range r.Lines {
		r.Lines[i].Serials = slices.Clone(r.Lines[i].Serials)
	}
	return r
}

func (t *tenantData) find(id string) (store.Receipt, error) {
	r, ok := t.receipts[id]
	if !ok {
		return store.Receipt{}, store.ErrNotFound
	}
	return cloneReceipt(r), nil
}

func (t *tenantData) recordedFor(productIDs []string) map[domain.SerialKey]struct{} {
	recorded := map[domain.SerialKey]struct{}{}
	for key := range t.serials {
		if slices.Contains(productIDs, key.ProductID) {
			recorded[key] = struct{}{}
		}
	}
	return recorded
}

func (t *tenantData) newArrival(in store.Receipt) (store.Receipt, error) {
	for _, r := range t.receipts {
		if r.VendorID == in.VendorID && r.PackingSlip == in.PackingSlip {
			return store.Receipt{}, &store.DuplicatePackingSlipError{ReceiptNumber: r.Number}
		}
	}
	in = cloneReceipt(in)
	if in.ID == "" {
		in.ID = ids.New()
	}
	if in.ExchangeRate.Sign() == 0 {
		in.ExchangeRate = decimal.NewFromInt(1)
	}
	in.State, in.Version, in.Number, in.CreatedAt = domain.Arrived, 1, fmt.Sprintf("RCV-%08d", t.counter+1), time.Now().UTC()
	for i := range in.Lines {
		l := &in.Lines[i]
		if l.LineNo == 0 {
			l.LineNo = i + 1
		}
		if l.Currency == "" {
			l.Currency = in.Currency
		}
		l.Qty, l.InventoryStatus = l.OrderedRemaining, "AVAILABLE"
	}
	return in, nil
}

func (t *tenantData) commitArrival(r store.Receipt) {
	t.counter++
	t.receipts[r.ID] = r
	t.order = append(t.order, r.ID)
}

func (t *tenantData) register(r store.Receipt, fn store.RegisterFunc) (store.Receipt, error) {
	productIDs := make([]string, len(r.Lines))
	for i, l := range r.Lines {
		productIDs[i] = l.ProductID
	}
	lines, err := fn(cloneReceipt(r), t.recordedFor(productIDs))
	if err != nil {
		return store.Receipt{}, err
	}
	if r.State != domain.Arrived {
		return store.Receipt{}, &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is not ARRIVED", r.ID)}
	}
	added := map[domain.SerialKey]struct{}{}
	for _, l := range lines {
		for _, serial := range l.Serials {
			key := domain.SerialKey{ProductID: l.ProductID, Serial: serial}
			_, dupAdded := added[key]
			_, dupRecorded := t.serials[key]
			if dupAdded || dupRecorded {
				return store.Receipt{}, &domain.Error{Code: domain.CodeDuplicateSerial, Detail: "a serial is already recorded for the product"}
			}
			added[key] = struct{}{}
		}
	}
	for key := range added {
		t.serials[key] = struct{}{}
	}
	r.State = domain.Registered
	r.Version++
	for _, l := range lines {
		for i := range r.Lines {
			if r.Lines[i].LineNo != l.LineNo {
				continue
			}
			r.Lines[i].Qty, r.Lines[i].Batch, r.Lines[i].LocationID = l.Qty, l.Batch, l.LocationID
			r.Lines[i].InventoryStatus = cmp.Or(l.InventoryStatus, "AVAILABLE")
			r.Lines[i].Serials = slices.Sorted(slices.Values(l.Serials))
		}
	}
	return r, nil
}

func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) CreateArrival(ctx context.Context, in store.Receipt) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		r, err := t.newArrival(in)
		if err != nil {
			return err
		}
		t.commitArrival(r)
		out = cloneReceipt(r)
		return nil
	})
	return out, err
}

func (s *Store) DirectReceipt(ctx context.Context, in store.Receipt, fn store.RegisterFunc) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		r, err := t.newArrival(in)
		if err != nil {
			return err
		}
		if r, err = t.register(r, fn); err != nil {
			return err
		}
		t.commitArrival(r)
		out = cloneReceipt(r)
		return nil
	})
	return out, err
}

func (s *Store) RecordedSerials(ctx context.Context, productIDs []string) (out map[domain.SerialKey]struct{}, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		out = t.recordedFor(productIDs)
		return nil
	})
	return out, err
}

func (s *Store) Register(ctx context.Context, id string, fn store.RegisterFunc) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		current, err := t.find(id)
		if err != nil {
			return err
		}
		if current, err = t.register(current, fn); err != nil {
			return err
		}
		t.receipts[id] = current
		out = cloneReceipt(current)
		return nil
	})
	return out, err
}

func (s *Store) Post(ctx context.Context, id string, fn store.PostFunc) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		current, err := t.find(id)
		if err != nil {
			return err
		}
		if current.State != domain.Registered {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is %s, not REGISTERED", id, current.State)}
		}
		outcome, err := fn(cloneReceipt(current))
		if err != nil {
			return err
		}
		amounts := map[int]store.LineAmount{}
		for _, a := range outcome.Amounts {
			amounts[a.LineNo] = a
		}
		now := time.Now().UTC()
		var items []store.GrirItem
		for i := range current.Lines {
			l := &current.Lines[i]
			a, ok := amounts[l.LineNo]
			if !ok {
				return fmt.Errorf("post outcome for receipt %s lacks line %d", id, l.LineNo)
			}
			l.Amount, l.AmountAcct = decimal.NullDecimal{Decimal: a.Amount, Valid: true}, decimal.NullDecimal{Decimal: a.AmountAcct, Valid: true}
			items = append(items, store.GrirItem{
				ID: ids.New(), ReceiptID: id, ReceiptNumber: current.Number, LineNo: l.LineNo, VendorID: current.VendorID, POLineID: l.POLineID,
				ReceivedQty: l.Qty, ReceivedAmount: a.Amount, OpenAmount: a.Amount, Currency: l.Currency, Status: "OPEN", CreatedAt: now,
			})
		}
		current.State, current.PostingDate, current.PostedAt, current.LedgerEntryID = domain.Posted, &outcome.PostingDate, &now, outcome.LedgerEntryID
		current.Version++
		t.receipts[id] = current
		t.grir = append(t.grir, items...)
		s.events = append(s.events, outcome.Event)
		out = cloneReceipt(current)
		return nil
	})
	return out, err
}

func (s *Store) GetReceipt(ctx context.Context, id string) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) (err error) {
		out, err = t.find(id)
		return err
	})
	return out, err
}

func skipForCursor(at time.Time, id string, cursorAt time.Time, cursorID string) bool {
	return !cursorAt.IsZero() && !at.Before(cursorAt) && !(at.Equal(cursorAt) && id < cursorID)
}

func pageLimit(limit int) int {
	if limit <= 0 {
		limit = defaultListLimit
	}
	return min(limit, maxListLimit)
}

func (s *Store) ListReceipts(ctx context.Context, f store.ReceiptFilter) (out []store.Receipt, err error) {
	limit := pageLimit(f.Limit)
	err = s.with(ctx, func(t *tenantData) error {
		for _, id := range slices.Backward(t.order) {
			r := t.receipts[id]
			if (f.VendorID != "" && r.VendorID != f.VendorID) || (f.PurchaseOrderID != "" && r.PurchaseOrderID != f.PurchaseOrderID) ||
				(f.State != "" && r.State != f.State) || (!f.From.IsZero() && r.Date.Before(f.From)) || (!f.To.IsZero() && r.Date.After(f.To)) {
				continue
			}
			if skipForCursor(r.CreatedAt, r.ID, f.CursorCreatedAt, f.CursorID) {
				continue
			}
			r.Lines = nil
			out = append(out, cloneReceipt(r))
			if len(out) == limit+1 {
				break
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) ListGrir(ctx context.Context, f store.GrirFilter) (page store.GrirPage, err error) {
	limit := pageLimit(f.Limit)
	err = s.with(ctx, func(t *tenantData) error {
		open := map[string]decimal.Decimal{}
		counts := map[string]int{}
		for _, g := range slices.Backward(t.grir) {
			if (f.VendorID != "" && g.VendorID != f.VendorID) || (f.PurchaseOrderID != "" && t.receipts[g.ReceiptID].PurchaseOrderID != f.PurchaseOrderID) ||
				(f.Status != "" && g.Status != f.Status) || (f.AgeGt != nil && time.Since(g.CreatedAt) <= time.Duration(*f.AgeGt)*24*time.Hour) {
				continue
			}
			g.AgeDays = int(time.Since(g.CreatedAt).Hours() / 24)
			g.OpenAmount = g.ReceivedAmount.Sub(g.ClearedAmount)
			if len(page.Items) <= limit && !skipForCursor(g.CreatedAt, g.ID, f.CursorCreatedAt, f.CursorID) {
				page.Items = append(page.Items, g)
			}
			open[g.Currency] = open[g.Currency].Add(g.ReceivedAmount.Sub(g.ClearedAmount))
			counts[g.Currency]++
		}
		currencies := make([]string, 0, len(open))
		for c := range open {
			currencies = append(currencies, c)
		}
		slices.Sort(currencies)
		for _, c := range currencies {
			page.Totals.OpenAmount = append(page.Totals.OpenAmount, store.CurrencyAmount{Amount: open[c], Currency: c})
			page.Totals.Count += counts[c]
		}
		return nil
	})
	return page, err
}

func (s *Store) ClaimKey(ctx context.Context, key, hash string, lockFor time.Duration) (res store.ClaimResult, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		now := time.Now()
		c := t.keys[key]
		if c == nil || c.expiresAt.Before(now) || (!c.completed && c.lockedUntil.Before(now)) {
			t.keys[key] = &claim{hash: hash, lockedUntil: now.Add(lockFor), expiresAt: now.Add(keyLifetime)}
			res.State = store.ClaimNew
			return nil
		}
		switch {
		case c.hash != hash:
			res.State = store.ClaimMismatch
		case c.completed:
			res = store.ClaimResult{State: store.ClaimCompleted, Status: c.status, Body: slices.Clone(c.body)}
		default:
			res.State = store.ClaimInFlight
		}
		return nil
	})
	return res, err
}

func (s *Store) CompleteKey(ctx context.Context, key string, status int, body []byte) error {
	return s.with(ctx, func(t *tenantData) error {
		if c := t.keys[key]; c != nil {
			c.completed, c.status, c.body = true, status, slices.Clone(body)
		}
		return nil
	})
}

func (s *Store) ExpireKey(ctx context.Context, key string) error {
	return s.with(ctx, func(t *tenantData) error {
		if c := t.keys[key]; c != nil {
			c.expiresAt = time.Now().Add(-time.Hour)
		}
		return nil
	})
}

func (s *Store) ReleaseKey(ctx context.Context, key string) error {
	return s.with(ctx, func(t *tenantData) error {
		delete(t.keys, key)
		return nil
	})
}

func (t *tenantData) grirFor(receiptID string, lineNo int) int {
	return slices.IndexFunc(t.grir, func(g store.GrirItem) bool { return g.ReceiptID == receiptID && g.LineNo == lineNo })
}

func (s *Store) emit(adj store.Adjustment) {
	if adj.Event != nil {
		s.events = append(s.events, *adj.Event)
	}
}

func (s *Store) Correct(ctx context.Context, id string, fn store.AdjustFunc) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		current, err := t.find(id)
		if err != nil {
			return err
		}
		if current.State != domain.Posted {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is %s, not POSTED", id, current.State)}
		}
		adj, err := fn(cloneReceipt(current))
		if err != nil {
			return err
		}
		for _, c := range adj.Lines {
			for i := range current.Lines {
				if l := &current.Lines[i]; l.LineNo == c.LineNo {
					l.Qty, l.Amount, l.AmountAcct = c.Qty, decimal.NullDecimal{Decimal: c.Amount, Valid: true}, decimal.NullDecimal{Decimal: c.AmountAcct, Valid: true}
				}
			}
			if g := t.grirFor(id, c.LineNo); g >= 0 {
				item := &t.grir[g]
				item.ReceivedQty, item.ReceivedAmount = c.Qty, c.Amount
				item.Status = grirStatus(item.InvoicedQty, c.Qty)
			}
		}
		current.CorrectionSeq++
		current.Version++
		t.receipts[id] = current
		s.emit(adj)
		out = cloneReceipt(current)
		return nil
	})
	return out, err
}

func grirStatus(invoiced, received decimal.Decimal) string {
	switch {
	case invoiced.GreaterThanOrEqual(received):
		return "CLEARED"
	case invoiced.Sign() > 0:
		return "PARTIAL"
	}
	return "OPEN"
}

func (s *Store) Cancel(ctx context.Context, id string, fn store.AdjustFunc) (out store.Receipt, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		current, err := t.find(id)
		if err != nil {
			return err
		}
		if !slices.Contains([]domain.ReceiptState{domain.Arrived, domain.Registered, domain.Posted}, current.State) {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is %s and cannot be cancelled", id, current.State)}
		}
		adj, err := fn(cloneReceipt(current))
		if err != nil {
			return err
		}
		if current.State == domain.Posted {
			for i := range t.grir {
				if g := &t.grir[i]; g.ReceiptID == id {
					g.ReceivedQty, g.ReceivedAmount, g.ClearedAmount, g.Status = decimal.Zero, decimal.Zero, decimal.Zero, "CLEARED"
				}
			}
		} else {
			t.releaseSerials(current)
		}
		current.State = domain.Cancelled
		current.Version++
		t.receipts[id] = current
		s.emit(adj)
		out = cloneReceipt(current)
		return nil
	})
	return out, err
}

func (t *tenantData) releaseSerials(r store.Receipt) {
	for _, l := range r.Lines {
		for _, serial := range l.Serials {
			delete(t.serials, domain.SerialKey{ProductID: l.ProductID, Serial: serial})
		}
	}
}

func invoiceKey(vendorID, number string) string { return vendorID + "|" + strings.ToLower(number) }

func (s *Store) PostInvoice(ctx context.Context, in store.Invoice, fn store.InvoiceFunc) (out store.Invoice, err error) {
	err = s.with(ctx, func(t *tenantData) error {
		key := invoiceKey(in.VendorID, in.InvoiceNumber)
		if _, dup := t.invoices[key]; dup {
			return &domain.Error{Code: domain.CodeDuplicateInvoice, Detail: fmt.Sprintf("invoice %q is already posted for the vendor", in.InvoiceNumber)}
		}
		working := map[store.ItemKey]*domain.GrirItem{}
		indexes := map[store.ItemKey]int{}
		items := make([]*domain.GrirItem, len(in.Lines))
		for i, l := range in.Lines {
			k := l.ItemKey()
			if working[k] == nil {
				g := t.grirFor(k.ReceiptID, k.LineNo)
				if g < 0 {
					return store.ErrGrirNotFound
				}
				item := t.grir[g]
				if t.receipts[k.ReceiptID].LegalEntityID != in.LegalEntityID || item.VendorID != in.VendorID {
					return &domain.Error{Code: domain.CodeInvalidReceipt, Detail: fmt.Sprintf("receipt %s line %d belongs to another vendor or legal entity", k.ReceiptID, k.LineNo)}
				}
				working[k], indexes[k] = &domain.GrirItem{
					ReceiptID: k.ReceiptID, LineNo: k.LineNo, ReceivedQty: item.ReceivedQty, InvoicedQty: item.InvoicedQty,
					ReceivedAmount: money.Amount{Value: item.ReceivedAmount, Currency: item.Currency},
					ClearedAmount:  money.Amount{Value: item.ClearedAmount, Currency: item.Currency},
				}, g
			}
			items[i] = working[k]
		}
		outcome, err := fn(items)
		if err != nil {
			return err
		}
		for k, item := range working {
			g := &t.grir[indexes[k]]
			g.InvoicedQty, g.ClearedAmount = item.InvoicedQty, item.ClearedAmount.Value
			g.Status = grirStatus(g.InvoicedQty, g.ReceivedQty)
			r := t.receipts[k.ReceiptID]
			for i := range r.Lines {
				if r.Lines[i].LineNo == k.LineNo {
					r.Lines[i].InvoicedQty = item.InvoicedQty
				}
			}
			t.receipts[k.ReceiptID] = r
		}
		t.invoices[key] = struct{}{}
		s.events = append(s.events, outcome.Event)
		in.LedgerEntryID, in.Total, in.CreatedAt = outcome.LedgerEntryID, outcome.Total, time.Now().UTC()
		out = in
		return nil
	})
	return out, err
}
