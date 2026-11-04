package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Operations routes. The rebuild is the InventoryAdmin duty (3.16.5); it is an
// internal path the web BFF does not expose.
const (
	dutyAdmin          = "inventory.admin"
	rebuildPath        = "/internal/v1/inventory/onhand:rebuild"
	backfillSupplyPath = "/internal/v1/inventory/transfer-orders:backfill-supply"
)

type rebuildDiffResponse struct {
	LegalEntityID    string   `json:"legalEntityId"`
	ProductID        string   `json:"productId"`
	DimensionSetHash string   `json:"dimensionSetHash"`
	Fields           []string `json:"fields"`
}

type rebuildResponse struct {
	Checked   int                   `json:"checked"`
	DiffCount int                   `json:"diffCount"`
	Repaired  bool                  `json:"repaired"`
	Diffs     []rebuildDiffResponse `json:"diffs"`
}

// registerOps mounts the journal reversal and the projection rebuild.
func (a *API) registerOps(r chi.Router) {
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}:reverse", journalRoute(a.reverseJournal))
	r.With(authz.RequireDuty(dutyJournalWrite)).Post(journalsPath+"/{id}:copy", journalRoute(a.copyJournal))
	r.With(authz.RequireDuty(dutyAdmin)).Post(rebuildPath, a.rebuildOnHand)
	r.With(authz.RequireDuty(dutyAdmin)).Post(backfillSupplyPath, a.backfillTransferSupply)
}

type backfillSupplyResponse struct {
	Checked int    `json:"checked"`
	Booked  int    `json:"booked"`
	More    bool   `json:"more"`
	Next    string `json:"next,omitempty"`
}

// backfillTransferSupply books the supply and demand pair of the open transfer
// orders that predate migration 0025 (one-off, per tenant), at most limit
// orders after the order id after per call. Idempotent: an order that already
// holds its pair books nothing, so no Idempotency-Key is stored.
func (a *API) backfillTransferSupply(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields := map[string]string{}
	limit := pageLimit(r, 200, maxPageSize, fields)
	after := r.URL.Query().Get("after")
	if after != "" && !ids.Valid(after) {
		fields["after"] = "must be a transfer order id (UUID)"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	rep, err := a.store.BackfillTransferSupply(r.Context(), after, limit)
	if err != nil {
		a.failed(w, r, "backfill transfer supply", err)
		return
	}
	httpx.JSON(w, http.StatusOK, backfillSupplyResponse{Checked: rep.Checked, Booked: rep.Booked, More: rep.More, Next: rep.Next})
}

// rebuildOnHand recomputes the projection from the transactions and active
// reservations and reports the rows that differ (FR-16.3, AC2). ?repair=true
// overwrites the differing rows in the same run. The result is a function of
// the ledger, so re-running is safe and no Idempotency-Key is stored.
func (a *API) rebuildOnHand(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var repair bool
	switch r.URL.Query().Get("repair") {
	case "", "false":
	case "true":
		repair = true
	default:
		fieldProblem(w, r, map[string]string{"repair": "must be true or false"})
		return
	}
	rep, err := a.store.RebuildOnHand(r.Context(), repair)
	if err != nil {
		a.failed(w, r, "rebuild on-hand", err)
		return
	}
	resp := rebuildResponse{Checked: rep.Checked, DiffCount: rep.DiffCount, Repaired: rep.Repaired, Diffs: make([]rebuildDiffResponse, 0, len(rep.Diffs))}
	for _, d := range rep.Diffs {
		resp.Diffs = append(resp.Diffs, rebuildDiffResponse{LegalEntityID: d.LegalEntityID, ProductID: d.ProductID, DimensionSetHash: d.DimensionSetHash, Fields: d.Fields})
	}
	httpx.JSON(w, http.StatusOK, resp)
}
