package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

type releaseRequest struct {
	// Party and Account correct the quarantined record; blank keeps the extracted value.
	Party   string `json:"party,omitempty"`
	Account string `json:"account,omitempty"`
	// BankAccount corrects a quarantined bank_balances line.
	BankAccount string `json:"bankAccount,omitempty"`
	// Item, Site, Warehouse, Location, Batch and Unit correct a quarantined onhand line.
	Item      string `json:"item,omitempty"`
	Site      string `json:"site,omitempty"`
	Warehouse string `json:"warehouse,omitempty"`
	Location  string `json:"location,omitempty"`
	Batch     string `json:"batch,omitempty"`
	Unit      string `json:"unit,omitempty"`
	// Parties are master-data ids added since the run, checked with the run's snapshot: parties
	// for open_items, bank accounts for bank_balances, items for onhand.
	Parties []string `json:"parties,omitempty"`
	Version int      `json:"version,omitempty"`
}

// releaseQuarantined corrects a quarantined open item, bank balance or on-hand line and loads it when the suite now passes
// (FR-49.11, 9.49.5). Releasing a record that is already released returns the run with
// Idempotent-Replayed: true (idempotent by record).
func (s *Server) releaseQuarantined(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	seq, err := seqParam(r, "rec")
	if err != nil {
		fail(w, r, err)
		return
	}
	var req releaseRequest
	idem, err := readCommand(r, &req, 1<<20)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if req.Version < 0 {
		fail(w, r, invalid("version must be >= 1 when given"))
		return
	}
	run, replayed, err := s.store.ReleaseQuarantined(r.Context(), idem, store.Release{
		RunID: id, Seq: seq, Fix: domain.Correction{Party: req.Party, Account: req.Account},
		BankFix:   domain.BankCorrection{BankAccount: req.BankAccount},
		OnhandFix: domain.OnhandCorrection{Item: req.Item, Site: req.Site, Warehouse: req.Warehouse, Location: req.Location, Batch: req.Batch, Unit: req.Unit},
		Parties:   splitIDs(req.Parties), ExpectedVersion: req.Version, By: p.UserID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, toRunDTO(run))
}
