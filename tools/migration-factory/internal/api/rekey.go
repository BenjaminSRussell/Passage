package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// rollbackBodyLimit bounds a rollback request, which carries the postings made since T-0.
const rollbackBodyLimit = 16 << 20

// postingDTO is one Passage posting line since T-0 that a rollback exports for re-keying
// (FR-49.9). The amount is a signed decimal string: debit positive, credit negative.
type postingDTO struct {
	Kind        string    `json:"kind"`
	Entity      string    `json:"entity"`
	DocumentRef string    `json:"documentRef"`
	PostedAt    time.Time `json:"postedAt"`
	Account     string    `json:"account"`
	Dimensions  string    `json:"dimensions,omitempty"`
	Currency    string    `json:"currency"`
	Amount      string    `json:"amount"`
	Text        string    `json:"text,omitempty"`
}

// rekeyingDTO is the re-keying export of a rolled-back plan.
type rekeyingDTO struct {
	JournalLines  int `json:"journalLines"`
	DocumentLines int `json:"documentLines"`
	Excluded      int `json:"excluded"`
}

// toPostings parses the wire postings. A bad kind or amount is a rule violation, never a
// silent zero; the plan-dependent rules are applied by domain.BuildRekeyExport.
func toPostings(in []postingDTO) ([]domain.RekeyPosting, error) {
	out := make([]domain.RekeyPosting, 0, len(in))
	for i, p := range in {
		kind, err := domain.ParsePostingKind(p.Kind)
		if err != nil {
			return nil, err
		}
		amt, err := parseMoney(p.Amount)
		if err != nil {
			return nil, fmt.Errorf("%w: posting %d (%s) amount %q %s", domain.ErrInvalid, i+1, p.DocumentRef, p.Amount, err)
		}
		out = append(out, domain.RekeyPosting{Kind: kind, Entity: strings.TrimSpace(p.Entity), DocumentRef: strings.TrimSpace(p.DocumentRef),
			PostedAt: p.PostedAt, Account: strings.TrimSpace(p.Account), Dimensions: p.Dimensions, Currency: p.Currency, Amount: amt, Text: p.Text})
	}
	return out, nil
}

// rekeyFiles maps the published file names to their kind.
var rekeyFiles = map[string]domain.PostingKind{
	"journals.csv":  domain.KindJournal,
	"documents.csv": domain.KindDocument,
}

// rekeyFile serves one re-keying file of a rolled-back plan as CSV (FR-49.9, 9.49.15).
func (s *Server) rekeyFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	name := chi.URLParam(r, "file")
	kind, ok := rekeyFiles[name]
	if !ok {
		fail(w, r, invalid("file must be journals.csv or documents.csv"))
		return
	}
	lines, err := s.store.RekeyLines(r.Context(), id, kind)
	if err != nil {
		fail(w, r, err)
		return
	}
	body, err := domain.RenderRekeyCSV(kind, lines)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="rekey-%s-%s"`, id, name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
