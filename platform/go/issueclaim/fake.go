package issueclaim

import (
	"context"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Fake is an in-memory Claimer that applies inventory's rule: with any
// quantity held, what the product's claims stand for together with the new
// one (Outstanding: claims of every kind, those sharing a ref counted once)
// may not exceed physical - held. Physical stays where Set put it, as it
// does in inventory while these movements are not journals there. Release of
// a hold (a usage decision) is Set with held = 0.
type Fake struct {
	mu     sync.Mutex
	stock  map[string]stock
	claims map[string]Claim
}

type stock struct{ physical, held decimal.Decimal }

func (f *Fake) Set(product string, physical, held decimal.Decimal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stock == nil {
		f.stock = map[string]stock{}
	}
	f.stock[product] = stock{physical: physical, held: held}
}

func (f *Fake) Claim(_ context.Context, c Claim) error {
	if !ids.Valid(c.ProductID) {
		return ErrNotProduct
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, replay := f.claims[c.Key]; replay {
		return nil
	}
	st := f.stock[c.ProductID]
	if st.held.IsPositive() && Outstanding(append(f.productClaims(c.ProductID), c)).GreaterThan(st.physical.Sub(st.held)) {
		return ErrHeld
	}
	if f.claims == nil {
		f.claims = map[string]Claim{}
	}
	f.claims[c.Key] = c
	return nil
}

func (f *Fake) productClaims(product string) []Claim {
	var out []Claim
	for _, c := range f.claims {
		if c.ProductID == product {
			out = append(out, c)
		}
	}
	return out
}

func (f *Fake) Release(_ context.Context, c Claim) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claims, c.Key)
}

// Claimed is the quantity of a product that claims of kind have taken.
func (f *Fake) Claimed(product string, kind Kind) decimal.Decimal {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := decimal.Zero
	for _, c := range f.claims {
		if c.ProductID == product && c.Kind == kind {
			total = total.Add(c.Qty)
		}
	}
	return total
}

// Outstanding is what the product's claims stand for together.
func (f *Fake) Outstanding(product string) decimal.Decimal {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Outstanding(f.productClaims(product))
}
