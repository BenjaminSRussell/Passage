package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/replay"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// Problem codes of the ledger-facing endpoints (slice 17).
const (
	CodeLedgerNotConfigured = "MIG-1530"
	CodeLedgerForeign       = "MIG-1531"
	CodeLedgerPinned        = "MIG-1532"
	CodeLedgerUnavailable   = "MIG-1533"
	// CodeSubledgerNotConfigured is a replay with documents of an area no subledger port posts (49-4).
	CodeSubledgerNotConfigured = "MIG-1534"
	// CodeSubledgerUnavailable is a subledger that failed during the replay; it can be repeated.
	CodeSubledgerUnavailable = "MIG-1535"
)

// WithLedger sets the general ledger the parallel-run replay and the rollback compensations post
// through. Without one those endpoints answer 503 MIG-1530.
func (s *Server) WithLedger(l ledgerclient.Ledger) *Server { s.ledger = l; return s }

// WithSubledgers sets the ports the parallel-run replay posts subledger documents through, one per area (AR, AP,
// INV, FA). A replay with documents of an area without a port answers 503 MIG-1534.
func (s *Server) WithSubledgers(p replay.Ports) *Server { s.subledgers = p; return s }

func (s *Server) ledgerReady(w http.ResponseWriter, r *http.Request) bool {
	if s.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLedgerNotConfigured, "the general ledger is not configured for the migration factory (LEDGER_URL)"))
		return false
	}
	return true
}

// failLedger maps the ledger-facing errors, then the shared ones.
func failLedger(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrLedgerPinned):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeLedgerPinned, err.Error()))
	case errors.Is(err, store.ErrLedgerForeign), errors.Is(err, replay.ErrLedgerForeign):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerForeign, err.Error()))
	case errors.Is(err, replay.ErrLedgerUnavailable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable, err.Error()))
	case errors.Is(err, replay.ErrNoSubledgerPort):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSubledgerNotConfigured,
			"no subledger port is configured for the replayed area: "+err.Error()))
	case errors.Is(err, replay.ErrSubledgerUnavailable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeSubledgerUnavailable, err.Error()))
	default:
		fail(w, r, err)
	}
}

// legalEntity checks that the caller may act for the legal entity.
func legalEntity(p tenant.Principal, id string) error {
	if !ids.Valid(id) || !tenant.InLegalEntityScope(p, id) {
		return invalid("legalEntityId must be a legal entity of the caller")
	}
	return nil
}

type replayLineDTO struct {
	Account string `json:"account"`
	Amount  string `json:"amount"`
	Text    string `json:"text,omitempty"`
}

type replayDocumentDTO struct {
	Seq         int             `json:"seq"`
	DocumentID  string          `json:"documentId"`
	PostingDate string          `json:"postingDate"`
	Currency    string          `json:"currency"`
	Lines       []replayLineDTO `json:"lines"`
}

type tbAccountDTO struct {
	Account  string `json:"account"`
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type replayRequest struct {
	Period             string              `json:"period"`
	SourceSystem       string              `json:"sourceSystem"`
	LegalEntityID      string              `json:"legalEntityId"`
	LedgerID           string              `json:"ledgerId"`
	Revenue            string              `json:"revenue"`
	ExplainedMaxPct    string              `json:"explainedMaxPct,omitempty"`
	Documents          []replayDocumentDTO `json:"documents"`
	LegacyTrialBalance []tbAccountDTO      `json:"legacyTrialBalance,omitempty"`
	// Subledger are the period's AR, AP, INV and FA documents, posted through the subledger ports (49-4).
	Subledger []subledgerDocumentDTO `json:"subledger,omitempty"`
	// LegacySubledger is the legacy movement per (area, key, currency); without it the documents' own sums are the legacy side.
	LegacySubledger []subledgerBalanceDTO `json:"legacySubledger,omitempty"`
}

type subledgerDocumentDTO struct {
	Seq         int    `json:"seq"`
	DocumentID  string `json:"documentId"`
	Area        string `json:"area"`
	Kind        string `json:"kind"`
	PostingDate string `json:"postingDate"`
	Key         string `json:"key"`
	Currency    string `json:"currency"`
	Amount      string `json:"amount"`
}

type subledgerBalanceDTO struct {
	Area     string `json:"area"`
	Key      string `json:"key"`
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type replayDTO struct {
	parallelDTO
	Posted   int               `json:"posted"`
	Rejected []replay.Rejected `json:"rejected"`
}

// replayParallelRun replays a legacy period's journal documents through the ledger into a rehearsal
// ledger, in their original sequence, reads the trial balance back and stores the diff against the
// legacy trial balance as a parallel run for review (FR-49.5, D4). Every document is posted under a
// fixed idempotency key and the run is idempotent by (period, system), so a replay may be repeated.
func (s *Server) replayParallelRun(w http.ResponseWriter, r *http.Request) {
	var req replayRequest
	idem, err := readCommand(r, &req, parallelBodyLimit)
	if err != nil {
		fail(w, r, err)
		return
	}
	// The rehearsal ledger is needed for journal documents only; a replay of subledger documents alone does not read it.
	if (len(req.Documents) > 0 || len(req.Subledger) == 0) && !s.ledgerReady(w, r) {
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
	if err := legalEntity(p, req.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.LedgerID) == "" && len(req.Documents) > 0 {
		fail(w, r, invalid("ledgerId is required: the rehearsal ledger the documents are replayed into"))
		return
	}
	revenue, err := parseMoney(req.Revenue)
	if err != nil {
		fail(w, r, invalid(fmt.Sprintf("revenue %q %s", req.Revenue, err)))
		return
	}
	maxPct, err := domain.ParseExplainedMaxPct(req.ExplainedMaxPct)
	if err != nil {
		fail(w, r, err)
		return
	}
	in := replay.Request{Period: strings.TrimSpace(req.Period), System: sys, LegalEntityID: req.LegalEntityID, LedgerID: strings.TrimSpace(req.LedgerID),
		Revenue: revenue, ExplainedMaxPct: maxPct}
	for _, d := range req.Documents {
		doc := domain.ReplayDocument{Seq: d.Seq, DocumentID: strings.TrimSpace(d.DocumentID), PostingDate: strings.TrimSpace(d.PostingDate), Currency: strings.TrimSpace(d.Currency)}
		for _, l := range d.Lines {
			amt, err := parseMoney(l.Amount)
			if err != nil {
				fail(w, r, invalid(fmt.Sprintf("document %s amount %q %s", d.DocumentID, l.Amount, err)))
				return
			}
			doc.Lines = append(doc.Lines, domain.ReplayLine{Account: strings.TrimSpace(l.Account), Amount: amt, Text: l.Text})
		}
		in.Documents = append(in.Documents, doc)
	}
	for _, a := range req.LegacyTrialBalance {
		amt, err := parseMoney(a.Amount)
		if err != nil || strings.TrimSpace(a.Account) == "" {
			fail(w, r, invalid(fmt.Sprintf("legacy trial balance account %q amount %q is not valid", a.Account, a.Amount)))
			return
		}
		in.LegacyTB = append(in.LegacyTB, domain.TBAccount{Account: strings.TrimSpace(a.Account), Currency: strings.TrimSpace(a.Currency), Net: amt})
	}
	for _, d := range req.Subledger {
		amt, err := parseMoney(d.Amount)
		if err != nil {
			fail(w, r, invalid(fmt.Sprintf("subledger document %s amount %q %s", d.DocumentID, d.Amount, err)))
			return
		}
		area, err := domain.ParseArea(strings.TrimSpace(d.Area))
		if err != nil || area == "TB" {
			fail(w, r, invalid(fmt.Sprintf("subledger document %s area %q must be one of INV, AP, AR, FA", d.DocumentID, d.Area)))
			return
		}
		in.Subledger = append(in.Subledger, domain.SubledgerDocument{Seq: d.Seq, DocumentID: strings.TrimSpace(d.DocumentID), Area: area, Kind: strings.ToUpper(strings.TrimSpace(d.Kind)),
			PostingDate: strings.TrimSpace(d.PostingDate), Key: strings.TrimSpace(d.Key), Currency: strings.TrimSpace(d.Currency), Amount: amt})
	}
	for _, b := range req.LegacySubledger {
		amt, err := parseMoney(b.Amount)
		area, aerr := domain.ParseArea(strings.TrimSpace(b.Area))
		if err != nil || aerr != nil || area == "TB" || strings.TrimSpace(b.Key) == "" {
			fail(w, r, invalid(fmt.Sprintf("legacy subledger %s %q amount %q is not valid", b.Area, b.Key, b.Amount)))
			return
		}
		in.LegacySubledger = append(in.LegacySubledger, domain.SubledgerBalance{Area: area, Key: strings.TrimSpace(b.Key), Currency: strings.TrimSpace(b.Currency), Net: amt})
	}
	if len(in.Documents) > 0 || len(in.Subledger) == 0 {
		if err := domain.ValidateReplay(in.Period, in.Documents); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := domain.ValidateSubledger(in.Period, in.Subledger); err != nil {
		fail(w, r, err)
		return
	}
	in.Ports = s.subledgers
	res, err := replay.Run(r.Context(), s.ledger, in)
	if err != nil {
		failLedger(w, r, err)
		return
	}
	run, err := domain.StartParallelRun(ids.New(), res.Input, domain.DefaultRules(), p.UserID, ids.New)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, replayed, err := s.store.StartParallelRun(r.Context(), idem, run)
	if err != nil {
		fail(w, r, err)
		return
	}
	rejected := res.Rejected
	if rejected == nil {
		rejected = []replay.Rejected{}
	}
	respond(w, http.StatusCreated, replayed, replayDTO{parallelDTO: toParallelDTO(out, true), Posted: len(res.Posted), Rejected: rejected})
}

type compensationDTO struct {
	Entity         string     `json:"entity"`
	DocumentRef    string     `json:"documentRef"`
	Currency       string     `json:"currency"`
	IdempotencyKey string     `json:"idempotencyKey"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	LastCode       string     `json:"lastCode,omitempty"`
	NextAttemptAt  *time.Time `json:"nextAttemptAt,omitempty"`
	EntryID        string     `json:"entryId,omitempty"`
	PostedAt       *time.Time `json:"postedAt,omitempty"`
}

type compensationReportDTO struct {
	store.CompensationReport
	Compensations []compensationDTO `json:"compensations"`
	Totals        []totalDTO        `json:"reconciliation"`
}

func toCompensationReport(rep store.CompensationReport) compensationReportDTO {
	out := compensationReportDTO{CompensationReport: rep, Compensations: []compensationDTO{}, Totals: []totalDTO{}}
	for _, c := range rep.Compensations {
		d := compensationDTO{Entity: c.Entity, DocumentRef: c.DocumentRef, Currency: c.Currency, IdempotencyKey: c.Key, Status: string(c.Status),
			Attempts: c.Attempts, LastCode: c.LastCode, EntryID: c.EntryID, PostedAt: c.PostedAt}
		if c.Status == domain.CompensationPending && c.Attempts > 0 {
			at := c.NextAttemptAt
			d.NextAttemptAt = &at
		}
		out.Compensations = append(out.Compensations, d)
	}
	for _, t := range rep.Totals {
		out.Totals = append(out.Totals, toTotalDTO(t))
	}
	return out
}

// entityLedgers trims and checks the ledgers a request pins: each needs an entity and a ledger, and
// its legal entity must be one the caller may act for.
func entityLedgers(p tenant.Principal, in []store.EntityLedger) ([]store.EntityLedger, error) {
	out := make([]store.EntityLedger, 0, len(in))
	for _, l := range in {
		l.Entity, l.LedgerID = strings.TrimSpace(l.Entity), strings.TrimSpace(l.LedgerID)
		if l.Entity == "" || l.LedgerID == "" {
			return nil, invalid("every ledger needs entity, legalEntityId and ledgerId")
		}
		if err := legalEntity(p, l.LegalEntityID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

type compensationRequest struct {
	Ledgers     []store.EntityLedger `json:"ledgers"`
	RetryParked bool                 `json:"retryParked,omitempty"`
}

// postCompensations posts the compensating journals of a rolled-back plan (FR-49.9, D9): one entry per
// re-keying journal, the lines on the opposite side, in the ledger each entity is pinned to. A
// failing journal backs off and is parked; the others go on. It may be called again: posted journals
// are not sent twice and the ledger books each key once.
func (s *Server) postCompensations(w http.ResponseWriter, r *http.Request) {
	if !s.ledgerReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var req compensationRequest
	if _, err := readCommand(r, &req, 1<<20); err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ledgers, err := entityLedgers(p, req.Ledgers)
	if err != nil {
		fail(w, r, err)
		return
	}
	rep, err := s.store.PostCompensations(r.Context(), s.ledger, store.CompensationRun{PlanID: id, Ledgers: ledgers, RetryParked: req.RetryParked,
		By: p.UserID, Now: s.now().UTC()})
	if err != nil {
		failLedger(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCompensationReport(rep))
}

func (s *Server) getCompensations(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	rep, err := s.store.CompensationReport(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCompensationReport(rep))
}

type coexistDTO struct {
	SourceSystem  string     `json:"sourceSystem"`
	SourceCompany string     `json:"sourceCompany"`
	Entity        string     `json:"entity"`
	Mode          string     `json:"mode"`
	CutoverAt     *time.Time `json:"cutoverAt,omitempty"`
	Version       int        `json:"version"`
	ChangedBy     string     `json:"changedBy,omitempty"`
	ChangedAt     *time.Time `json:"changedAt,omitempty"`
}

func toCoexistDTO(f domain.CoexistFlag) coexistDTO {
	d := coexistDTO{SourceSystem: string(f.SourceSystem), SourceCompany: f.Company, Entity: f.Entity, Mode: string(f.Mode), CutoverAt: f.CutoverAt,
		Version: f.Version, ChangedBy: f.ChangedBy}
	if !f.ChangedAt.IsZero() {
		at := f.ChangedAt
		d.ChangedAt = &at
	}
	return d
}

// listCoexistence returns the entity-by-entity cutover flags of a legacy company (FR-49.7).
func (s *Server) listCoexistence(w http.ResponseWriter, r *http.Request) {
	sys, err := domain.ParseSourceSystem(r.URL.Query().Get("sourceSystem"))
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(r.URL.Query().Get("sourceCompany"))
	if company == "" {
		fail(w, r, invalid("sourceCompany is required"))
		return
	}
	flags, err := s.store.ListCoexistence(r.Context(), sys, company)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := make([]coexistDTO, 0, len(flags))
	for _, f := range flags {
		out = append(out, toCoexistDTO(f))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type coexistRequest struct {
	Mode            string `json:"mode"`
	ExpectedVersion int    `json:"expectedVersion"`
}

// setCoexistence cuts one entity kind over to Passage, or flips it back to the legacy system, as the
// signed-in cutover manager against the flag's version (BR-49.10).
func (s *Server) setCoexistence(w http.ResponseWriter, r *http.Request) {
	sys, err := domain.ParseSourceSystem(chi.URLParam(r, "system"))
	if err != nil {
		fail(w, r, err)
		return
	}
	company := strings.TrimSpace(chi.URLParam(r, "company"))
	entity, err := domain.ParseCoexistEntity(chi.URLParam(r, "entity"))
	if err != nil {
		fail(w, r, err)
		return
	}
	var req coexistRequest
	idem, err := readCommand(r, &req, 1<<16)
	if err != nil {
		fail(w, r, err)
		return
	}
	mode, err := domain.ParseCoexistMode(req.Mode)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	f, replayed, err := s.store.SetCoexistence(r.Context(), idem, store.CoexistChange{System: sys, Company: company, Entity: entity, Mode: mode,
		ExpectedVersion: req.ExpectedVersion, By: approverID(p), Now: s.now().UTC()})
	if err != nil {
		fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, replayed, toCoexistDTO(f))
}
