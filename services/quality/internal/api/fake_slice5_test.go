package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

// fakeSlice5 holds the slice 5 state of the fake store.
type fakeSlice5 struct {
	receiptChecks []domain.ReceiptCheck
	receiptKeys   map[string]domain.ReceiptCheck
	coas          []domain.CoA
	coaKeys       map[string]domain.CoA
}

// TransitionStudy mirrors the Postgres store: the If-Match version, then the change.
func (f *fakeStore) TransitionStudy(_ context.Context, id string, expected int, change func(domain.Study) (domain.Study, error)) (domain.Study, error) {
	cur, ok := f.studies[id]
	if !ok {
		return cur, httpx.NewProblem(http.StatusNotFound, domain.CodeStudyMissing, "stability study not found")
	}
	if cur.Version != expected {
		return cur, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, fmt.Sprintf("current version is %d, not %d", cur.Version, expected))
	}
	next, err := change(cur)
	if err != nil {
		return cur, err
	}
	next.Version = cur.Version + 1
	f.studies[id] = next
	return next, nil
}

// CheckReceipt mirrors the Postgres store: key replay, the receipt line rule, and
// the accepted quantity of closed SOURCE_INSPECTION orders less earlier covers.
func (f *fakeStore) CheckReceipt(_ context.Context, in domain.ReceiptCheckInput, key string,
	check func(accepted, coveredBefore decimal.Decimal) (domain.ReceiptCheck, error)) (domain.ReceiptCheck, bool, error) {
	if prior, ok := f.s5.receiptKeys[key]; ok {
		if prior.ReceiptLineID != in.ReceiptLineID {
			return prior, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeKeyReuse, "key used for another receipt line")
		}
		return prior, false, nil
	}
	covered := decimal.Zero
	for _, c := range f.s5.receiptChecks {
		if c.ReceiptLineID == in.ReceiptLineID {
			if !c.SameReceipt(in) {
				return c, false, httpx.NewProblem(http.StatusConflict, domain.CodeReceiptCheckDupe, "receipt line checked with other values")
			}
			return c, false, nil
		}
		if c.POLineID == in.POLineID && c.Batch == in.Batch {
			covered = covered.Add(c.CoveredQty)
		}
	}
	accepted := decimal.Zero
	for _, d := range f.decisions {
		o := f.orders[d.OrderID]
		if o.Trigger == domain.TriggerSourceInspect && o.Status == domain.StatusClosed && o.SourceRef.ID == in.POLineID && o.Dims["batch"] == in.Batch {
			accepted = accepted.Add(d.Split.Accept).Add(d.Split.Deviation)
		}
	}
	c, err := check(accepted, covered)
	if err != nil {
		return c, false, err
	}
	c.ID = ids.New()
	if f.s5.receiptKeys == nil {
		f.s5.receiptKeys = map[string]domain.ReceiptCheck{}
	}
	f.s5.receiptKeys[key] = c
	f.s5.receiptChecks = append(f.s5.receiptChecks, c)
	return c, true, nil
}

func (f *fakeStore) ListReceiptChecks(_ context.Context, filter store.ReceiptCheckFilter) ([]domain.ReceiptCheck, error) {
	out := []domain.ReceiptCheck{}
	for _, c := range f.s5.receiptChecks {
		if (filter.POLineID == "" || c.POLineID == filter.POLineID) && (filter.Batch == "" || c.Batch == filter.Batch) {
			out = append(out, c)
		}
	}
	return out, nil
}

// IssueCoA mirrors the Postgres store: key replay (refused on another order), the
// order check, and the latest revision of the (order, shipment) chain.
func (f *fakeStore) IssueCoA(_ context.Context, req domain.CoARequest, key string, build func(prev *domain.CoA) (domain.CoA, error)) (domain.CoA, bool, error) {
	if prior, ok := f.s5.coaKeys[key]; ok {
		if prior.QualityOrderID != req.QualityOrderID {
			return prior, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeKeyReuse, "key used for another order")
		}
		return prior, false, nil
	}
	if _, ok := f.orders[req.QualityOrderID]; !ok {
		return domain.CoA{}, false, httpx.NewProblem(http.StatusNotFound, domain.CodeOrderMissing, "quality order not found")
	}
	var prev *domain.CoA
	for i := range f.s5.coas {
		c := f.s5.coas[i]
		if c.QualityOrderID == req.QualityOrderID && c.ShipmentID == req.ShipmentID && (prev == nil || c.Revision > prev.Revision) {
			prev = &c
		}
	}
	doc, err := build(prev)
	if err != nil {
		return doc, false, err
	}
	doc.ID = ids.New()
	if f.s5.coaKeys == nil {
		f.s5.coaKeys = map[string]domain.CoA{}
	}
	f.s5.coaKeys[key] = doc
	f.s5.coas = append(f.s5.coas, doc)
	return doc, true, nil
}

func (f *fakeStore) ListCoAs(_ context.Context, orderID string) ([]domain.CoA, error) {
	out := []domain.CoA{}
	for _, c := range f.s5.coas {
		if c.QualityOrderID == orderID {
			out = append(out, c)
		}
	}
	return out, nil
}
