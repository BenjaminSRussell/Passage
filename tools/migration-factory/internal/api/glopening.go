package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

const (
	CodeOpeningRejected   = "MIG-1541"
	CodeOpeningTarget     = "MIG-1542"
	CodeOpeningConflict   = "MIG-1543"
	CodeOpeningUnbalanced = "MIG-1540"
)

type openingJournalRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	PostingDate   string `json:"postingDate"`
}

type openingJournalDTO struct {
	Currency      string     `json:"currency"`
	LegalEntityID string     `json:"legalEntityId"`
	LedgerID      string     `json:"ledgerId"`
	PostingDate   string     `json:"postingDate"`
	LineCount     int        `json:"lineCount"`
	Debit         string     `json:"debit"`
	Credit        string     `json:"credit"`
	EntryID       string     `json:"entryId"`
	AlreadyPosted bool       `json:"alreadyPosted"`
	PostedAt      *time.Time `json:"postedAt,omitempty"`
}

type openingJournalsDTO struct {
	RunID    string              `json:"runId"`
	Journals []openingJournalDTO `json:"journals"`
}

func toOpeningJournalsDTO(runID string, in []store.OpeningPosting) openingJournalsDTO {
	out := openingJournalsDTO{RunID: runID, Journals: make([]openingJournalDTO, 0, len(in))}
	for _, p := range in {
		out.Journals = append(out.Journals, openingJournalDTO{Currency: p.Currency, LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID,
			PostingDate: p.PostingDate, LineCount: p.LineCount, Debit: p.Debit.String(), Credit: p.Credit.String(), EntryID: p.EntryID,
			AlreadyPosted: p.AlreadyPosted, PostedAt: p.PostedAt})
	}
	return out
}

func postedNote(posted []store.OpeningPosting) string {
	var cur []string
	for _, p := range posted {
		cur = append(cur, p.Currency)
	}
	if len(cur) == 0 {
		return "no currency was posted"
	}
	return "already posted: " + strings.Join(cur, ", ")
}

func failOpening(w http.ResponseWriter, r *http.Request, err error, posted []store.OpeningPosting) {
	var le *ledgerclient.Error
	switch {
	case errors.Is(err, store.ErrOpeningTargetTaken):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeOpeningTarget, err.Error()))
	case errors.Is(err, domain.ErrOpeningUnbalanced):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOpeningUnbalanced, err.Error()))
	case errors.As(err, &le) && le.Status == http.StatusConflict:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeOpeningConflict,
			"the general ledger already holds a journal under this opening key with different lines, probably from an earlier attempt before the balances changed; reverse it in the ledger, then post again ("+postedNote(posted)+")"))
	case errors.As(err, &le) && ledgerclient.Rejected(err):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOpeningRejected,
			"the general ledger refused the opening journal ("+le.Code+"); "+postedNote(posted)+"; a call after the cause is fixed posts the rest"))
	case errors.As(err, &le), errors.Is(err, ledgerclient.ErrNoPrincipal):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeLedgerUnavailable, "the general ledger did not answer; call again to post what is left ("+postedNote(posted)+")"))
	default:
		failLedger(w, r, err)
	}
}

// postOpeningJournal posts the opening journal of a gl_balances run's legacy company to a legal
// entity's ledger through the ledger's posting API: one journal per currency, refused whole when any
// currency does not balance, and idempotent (FR-49.1, FR-49.2).
func (s *Server) postOpeningJournal(w http.ResponseWriter, r *http.Request) {
	if !s.ledgerReady(w, r) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var req openingJournalRequest
	if _, err := readCommand(r, &req, 1<<16); err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := legalEntity(p, req.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	ledgerID := strings.TrimSpace(req.LedgerID)
	if ledgerID == "" {
		fail(w, r, invalid("ledgerId is required"))
		return
	}
	date, err := domain.ParsePostingDate(strings.TrimSpace(req.PostingDate))
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := s.store.PostOpeningJournal(r.Context(), s.ledger, store.OpeningJournalRequest{RunID: id, LegalEntityID: req.LegalEntityID,
		LedgerID: ledgerID, PostingDate: date, By: p.UserID})
	if err != nil {
		failOpening(w, r, err, res.Journals)
		return
	}
	httpx.JSON(w, http.StatusOK, toOpeningJournalsDTO(res.RunID, res.Journals))
}

func (s *Server) getOpeningJournal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	posts, err := s.store.OpeningPostings(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toOpeningJournalsDTO(id, posts))
}
