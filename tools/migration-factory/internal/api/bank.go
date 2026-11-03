package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// bankBalanceDTO is one legacy bank account balance at the cutoff.
type bankBalanceDTO struct {
	LegacyID    string `json:"legacyId"`
	BankAccount string `json:"bankAccount"`
	Currency    string `json:"currency"`
	Balance     string `json:"balance"`
}

// bankRunRequest starts a bank_balances run. BankAccounts is the bank account master the
// referential rule checks against.
type bankRunRequest struct {
	SourceSystem     string           `json:"sourceSystem"`
	SourceCompany    string           `json:"sourceCompany"`
	CutoffTS         time.Time        `json:"cutoffTs"`
	FreezeTS         time.Time        `json:"freezeTs"`
	QuarantineMaxPct string           `json:"quarantineMaxPct,omitempty"`
	BankAccounts     []string         `json:"bankAccounts"`
	Records          []bankBalanceDTO `json:"records"`
}

// startBankRun validates a bank_balances extract, quarantines failing lines, loads the rest
// idempotently by legacy_xref as opening balances and reconciles it (FR-49.1, FR-49.2, FR-49.10,
// FR-49.11).
func (s *Server) startBankRun(w http.ResponseWriter, r *http.Request) {
	var req bankRunRequest
	idem, err := readCommand(r, &req, packRunBodyLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	sys, err := domain.ParseSourceSystem(req.SourceSystem)
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(req.SourceCompany)
	if company == "" {
		fail(w, r, invalid("sourceCompany is required (DataAreaId or WERKS company code)"))
		return
	}
	if req.CutoffTS.IsZero() || req.FreezeTS.IsZero() {
		fail(w, r, invalid("cutoffTs and freezeTs are required"))
		return
	}
	if err := domain.CheckCutoff(req.CutoffTS, req.FreezeTS); err != nil {
		fail(w, r, err)
		return
	}
	maxPct, err := domain.ParseQuarantineMaxPct(req.QuarantineMaxPct)
	if err != nil {
		fail(w, r, err)
		return
	}
	master := splitIDs(req.BankAccounts)
	if len(master) == 0 {
		fail(w, r, invalid("bankAccounts must list the bank account master the lines are checked against"))
		return
	}
	if len(req.Records) == 0 {
		fail(w, r, invalid("records must hold at least one line"))
		return
	}
	lines := make([]domain.BankBalanceLine, 0, len(req.Records))
	for _, rec := range req.Records {
		bal, err := parseMoney(rec.Balance)
		if err != nil {
			fail(w, r, fmt.Errorf("%w: balance %q of %q %s", domain.ErrInvalid, rec.Balance, rec.LegacyID, err))
			return
		}
		t := strings.TrimSpace
		lines = append(lines, domain.BankBalanceLine{LegacyID: t(rec.LegacyID), BankAccount: t(rec.BankAccount), Currency: t(rec.Currency), Balance: bal})
	}
	accounts := make(map[string]struct{}, len(master))
	for _, a := range master {
		accounts[a] = struct{}{}
	}
	accepted, quarantined := domain.ValidateBankBalances(lines, accounts)
	if err := domain.QuarantineShareExceeded(len(quarantined), len(lines), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: domain.PackBankBal, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashBankBalances(lines), Status: domain.StatusExtracted,
		RecordsIn: len(lines), RecordsQuarantined: len(quarantined), Quarantined: quarantined,
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.RunBankBalances(r.Context(), idem, store.BankRun{Run: run, Lines: lines, Accepted: accepted, StartedBy: p.UserID})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toRunDTO(out))
}
