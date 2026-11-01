package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const pickStoredPath = "/v1/production/batch-picking:propose-stored"

func (a *API) registerPickStored(r chi.Router) {
	r.With(authz.RequireDuty(DutyRead)).Post(pickStoredPath, a.proposePickingStored)
}

type onHandDTO struct {
	BatchID string          `json:"batchId"`
	Qty     decimal.Decimal `json:"qty"`
}

type potencyStoredDTO struct {
	// Attribute defaults to the item's potency attribute.
	Attribute string          `json:"attribute,omitempty"`
	Min       decimal.Decimal `json:"min"`
	Max       decimal.Decimal `json:"max"`
}

type pickStoredRequest struct {
	ItemID         string            `json:"itemId"`
	PostingDate    string            `json:"postingDate"`
	Qty            decimal.Decimal   `json:"qty"`
	Potency        *potencyStoredDTO `json:"potency,omitempty"`
	OverrideReason string            `json:"overrideReason,omitempty"`
	OnHand         []onHandDTO       `json:"onHand"`
}

type pickStoredResponse struct {
	pickingResponse
	// BatchNumbers maps each batch id in the proposal to its batch number.
	BatchNumbers map[string]string `json:"batchNumbers"`
}

// proposePickingStored proposes the batches a line consumes from the stored
// batch register (FR-24.3, FR-24.5, FR-24.6, AC-24.10): the disposition,
// expiry and potency of every batch are read from the quality record instead
// of being supplied, so a batch put on hold or past its expiry cannot be
// picked by leaving that out of the request. Only the on-hand quantity per
// batch is passed, because the register keeps none. The rules and their codes
// are those of batch-picking:proposal.
func (a *API) proposePickingStored(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var in pickStoredRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	item, ok := ids.Canonical(in.ItemID)
	if !ok {
		invalid(w, r, map[string]string{"itemId": "must be a UUID"})
		return
	}
	if len(in.OnHand) == 0 || len(in.OnHand) > maxBatches {
		invalid(w, r, map[string]string{"onHand": "must list 1 to 200 batches"})
		return
	}
	seen := map[string]bool{}
	onHand := make([]store.OnHand, len(in.OnHand))
	for i, oh := range in.OnHand {
		// Canonical form, so an upper-case id matches the register and
		// cannot be listed twice under two spellings.
		id, ok := ids.Canonical(oh.BatchID)
		if !ok || seen[id] {
			invalid(w, r, map[string]string{"onHand": "batchId must be a distinct UUID per entry"})
			return
		}
		if !validQtyOrZero(oh.Qty) {
			invalid(w, r, map[string]string{"onHand": "qty must be from 0 below 10^12 with at most 6 decimals"})
			return
		}
		seen[id] = true
		onHand[i] = store.OnHand{BatchID: id, Qty: oh.Qty}
	}
	cands, potencyAttr, err := a.store.PickCandidates(r.Context(), item, onHand)
	if err != nil {
		fail(w, r, err)
		return
	}
	req := pickingRequest{PostingDate: in.PostingDate, Qty: in.Qty, OverrideReason: in.OverrideReason}
	if b := in.Potency; b != nil {
		name := b.Attribute
		if name == "" {
			name = potencyAttr
		}
		if name == "" {
			fail(w, r, invalidf("potency.attribute is required: the item defines no potency attribute"))
			return
		}
		req.Potency = &potencyBandDTO{Attribute: name, Min: b.Min, Max: b.Max}
	}
	batches := make([]domain.Batch, len(cands))
	numbers := make(map[string]string, len(cands))
	for i, c := range cands {
		batches[i] = c.Batch
		numbers[c.Batch.ID] = c.BatchNumber
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := pickBatches(req, batches, p.HasDuty(DutyConsumeExpired))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, pickStoredResponse{pickingResponse: out, BatchNumbers: numbers})
}
