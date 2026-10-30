package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// glBalanceDTO is one legacy GL balance at (account, dimension combination, currency).
type glBalanceDTO struct {
	LegacyID   string `json:"legacyId"`
	Account    string `json:"account"`
	Dimensions string `json:"dimensions,omitempty"`
	Currency   string `json:"currency"`
	Balance    string `json:"balance"`
}

// glRunRequest starts a gl_balances run. Accounts is the chart of accounts the referential rule
// checks against. OpenItemsRunID optionally names the open_items run of the same company to
// reconcile the control accounts with (BR-49.2).
type glRunRequest struct {
	SourceSystem     string         `json:"sourceSystem"`
	SourceCompany    string         `json:"sourceCompany"`
	CutoffTS         time.Time      `json:"cutoffTs"`
	FreezeTS         time.Time      `json:"freezeTs"`
	QuarantineMaxPct string         `json:"quarantineMaxPct,omitempty"`
	Accounts         []string       `json:"accounts"`
	OpenItemsRunID   string         `json:"openItemsRunId,omitempty"`
	Records          []glBalanceDTO `json:"records"`
}

// toGLBalances parses the wire balances. A balance that is not a decimal, or has more than six
// decimal places, rejects the request; rule failures (blank id, unknown account, currency)
// quarantine the balance.
func toGLBalances(in []glBalanceDTO) ([]domain.GLBalance, error) {
	out := make([]domain.GLBalance, 0, len(in))
	for _, rec := range in {
		amt, err := decimal.NewFromString(strings.TrimSpace(rec.Balance))
		if err != nil {
			return nil, fmt.Errorf("%w: balance %q of %q is not a decimal", domain.ErrInvalid, rec.Balance, rec.LegacyID)
		}
		if !amt.Equal(amt.Round(6)) {
			// gl_opening_balance holds numeric(28,6); a finer balance would be stored rounded
			// while the run reconciled the unrounded value.
			return nil, fmt.Errorf("%w: balance %q of %q has more than 6 decimal places", domain.ErrInvalid, rec.Balance, rec.LegacyID)
		}
		out = append(out, domain.GLBalance{LegacyID: strings.TrimSpace(rec.LegacyID), Account: strings.TrimSpace(rec.Account),
			Dimensions: strings.TrimSpace(rec.Dimensions), Currency: strings.TrimSpace(rec.Currency), Balance: amt})
	}
	return out, nil
}

// startGLRun validates a gl_balances extract, refuses one whose debits differ from its credits,
// quarantines failing balances, loads the rest idempotently by legacy_xref as opening balances and
// reconciles them, and the control accounts of the named open_items run (FR-49.1, FR-49.2,
// FR-49.10, FR-49.11, BR-49.2).
func (s *Server) startGLRun(w http.ResponseWriter, r *http.Request) {
	var req glRunRequest
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
		fail(w, r, invalid("sourceCompany is required (DataAreaId or BUKRS)"))
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
	openItemsRun := strings.TrimSpace(req.OpenItemsRunID)
	if openItemsRun != "" && !ids.Valid(openItemsRun) {
		fail(w, r, invalid("openItemsRunId must be a UUID"))
		return
	}
	chart := splitIDs(req.Accounts)
	if len(chart) == 0 {
		fail(w, r, invalid("accounts must list the chart of accounts the balances are checked against"))
		return
	}
	if len(req.Records) == 0 {
		fail(w, r, invalid("records must hold at least one balance"))
		return
	}
	balances, err := toGLBalances(req.Records)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := domain.ExtractBalanced(balances); err != nil {
		fail(w, r, err)
		return
	}
	accounts := make(map[string]struct{}, len(chart))
	for _, a := range chart {
		accounts[a] = struct{}{}
	}
	accepted, quarantined := domain.ValidateGL(balances, accounts)
	if err := domain.QuarantineShareExceeded(len(quarantined), len(balances), maxPct); err != nil {
		fail(w, r, err)
		return
	}
	run := domain.PackRun{
		RunID: ids.New(), TenantID: p.TenantID, Pack: domain.PackGLBalances, SourceSystem: sys, SourceCompany: company,
		CutoffTS: req.CutoffTS.UTC(), InputHash: domain.HashGL(balances), Status: domain.StatusExtracted,
		RecordsIn: len(balances), RecordsQuarantined: len(quarantined), Quarantined: quarantined,
	}
	if err := run.Advance(domain.StatusValidated); err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.RunGLBalances(r.Context(), idem, store.GLBalancesRun{
		Run: run, Balances: balances, Accepted: accepted, OpenItemsRunID: openItemsRun, StartedBy: p.UserID,
	})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusCreated, replayed, toRunDTO(out))
}
