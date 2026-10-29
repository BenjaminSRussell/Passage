package domain

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PostingKind says which re-keying file a posting goes to (FR-49.9): a JOURNAL line is a GL
// journal line, a DOCUMENT line belongs to a subledger document (invoice, order, receipt).
type PostingKind string

// Posting kinds.
const (
	KindJournal  PostingKind = "JOURNAL"
	KindDocument PostingKind = "DOCUMENT"
)

// ParsePostingKind validates a wire posting kind.
func ParsePostingKind(s string) (PostingKind, error) {
	switch k := PostingKind(strings.ToUpper(strings.TrimSpace(s))); k {
	case KindJournal, KindDocument:
		return k, nil
	}
	return "", fmt.Errorf("%w: posting kind %q is not JOURNAL or DOCUMENT", ErrInvalid, s)
}

// RekeyPosting is one Passage posting line that a rollback hands back to the legacy system for
// re-keying. Amount is signed: debit positive, credit negative. Line numbers the lines of one
// document from 1 in the order they were given.
type RekeyPosting struct {
	Kind        PostingKind
	Entity      string
	DocumentRef string
	Line        int
	PostedAt    time.Time
	Account     string
	Dimensions  string
	Currency    string
	Amount      decimal.Decimal
	Text        string
}

// RekeyExport is what a rollback exports: the journal and document lines posted since T-0, in
// file order, and the number of lines left out because they were posted before T-0 (they were
// made in the legacy system and are already there).
type RekeyExport struct {
	Journals  []RekeyPosting
	Documents []RekeyPosting
	Excluded  int
}

// RekeySummary is the stored outcome of a rollback's export.
type RekeySummary struct {
	JournalLines  int
	DocumentLines int
	Excluded      int
}

// Summary counts the export's lines.
func (e RekeyExport) Summary() RekeySummary {
	return RekeySummary{JournalLines: len(e.Journals), DocumentLines: len(e.Documents), Excluded: e.Excluded}
}

func (p RekeyPosting) validate(entities map[string]bool) error {
	if strings.TrimSpace(p.Entity) == "" || strings.TrimSpace(p.DocumentRef) == "" || strings.TrimSpace(p.Account) == "" {
		return fmt.Errorf("%w: every posting needs entity, documentRef and account", ErrInvalid)
	}
	if !entities[p.Entity] {
		return fmt.Errorf("%w: posting %s is for entity %q, which this cutover plan does not cover", ErrInvalid, p.DocumentRef, p.Entity)
	}
	if !validCurrency(p.Currency) {
		return fmt.Errorf("%w: posting %s currency %q is not an ISO 4217 code", ErrInvalid, p.DocumentRef, p.Currency)
	}
	if p.PostedAt.IsZero() {
		return fmt.Errorf("%w: posting %s needs postedAt", ErrInvalid, p.DocumentRef)
	}
	if !p.Amount.Equal(p.Amount.Round(6)) {
		return fmt.Errorf("%w: posting %s amount %s has more than 6 decimal places", ErrInvalid, p.DocumentRef, p.Amount)
	}
	return nil
}

type docKey struct {
	kind     PostingKind
	entity   string
	ref      string
	currency string
}

// BuildRekeyExport validates the postings of a cutover being rolled back and splits them into
// the journal and document files (FR-49.9). Every posting must be for an entity of the plan.
// Postings dated before T-0 are counted as excluded and left out. The lines of each journal
// (entity, document, currency) must balance to zero, since a legacy journal import rejects an
// unbalanced voucher; documents are single-sided and are not balanced. Lines are ordered by
// entity, posting time and document and keep their given order inside a document, so the same
// postings always produce the same files. One bad posting rejects the whole export.
func BuildRekeyExport(plan Plan, postings []RekeyPosting) (RekeyExport, error) {
	entities := make(map[string]bool, len(plan.Entities))
	for _, e := range plan.Entities {
		entities[e] = true
	}
	var out RekeyExport
	var kept []RekeyPosting
	balance := map[docKey]decimal.Decimal{}
	var order []docKey
	for i, p := range postings {
		if p.Kind != KindJournal && p.Kind != KindDocument {
			return RekeyExport{}, fmt.Errorf("%w: posting %d kind %q is not JOURNAL or DOCUMENT", ErrInvalid, i+1, p.Kind)
		}
		if err := p.validate(entities); err != nil {
			return RekeyExport{}, err
		}
		if p.PostedAt.Before(plan.T0) {
			out.Excluded++
			continue
		}
		p.PostedAt = p.PostedAt.UTC()
		kept = append(kept, p)
		if p.Kind == KindJournal {
			k := docKey{p.Kind, p.Entity, p.DocumentRef, p.Currency}
			if _, seen := balance[k]; !seen {
				order = append(order, k)
			}
			balance[k] = balance[k].Add(p.Amount)
		}
	}
	for _, k := range order {
		if !balance[k].IsZero() {
			return RekeyExport{}, fmt.Errorf("%w: journal %s of %s does not balance in %s (difference %s)", ErrInvalid, k.ref, k.entity, k.currency, balance[k].StringFixed(6))
		}
	}
	// A document keeps the posting time of its first line, so its lines stay together.
	first := map[docKey]time.Time{}
	for _, p := range kept {
		k := docKey{p.Kind, p.Entity, p.DocumentRef, ""}
		if t, ok := first[k]; !ok || p.PostedAt.Before(t) {
			first[k] = p.PostedAt
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		ka, kb := docKey{a.Kind, a.Entity, a.DocumentRef, ""}, docKey{b.Kind, b.Entity, b.DocumentRef, ""}
		if a.Entity != b.Entity {
			return a.Entity < b.Entity
		}
		if ta, tb := first[ka], first[kb]; !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return a.DocumentRef < b.DocumentRef
	})
	lines := map[docKey]int{}
	for _, p := range kept {
		k := docKey{p.Kind, p.Entity, p.DocumentRef, ""}
		lines[k]++
		p.Line = lines[k]
		if p.Kind == KindJournal {
			out.Journals = append(out.Journals, p)
		} else {
			out.Documents = append(out.Documents, p)
		}
	}
	return out, nil
}

// Re-keying file headers. Journals carry debit and credit columns, as legacy journal imports
// (AX general journal, SAP FB01-style templates) expect; documents carry the signed amount.
var (
	JournalHeader  = []string{"entity", "document_ref", "line", "posted_at", "account", "dimensions", "currency", "debit", "credit", "text"}
	DocumentHeader = []string{"entity", "document_ref", "line", "posted_at", "account", "dimensions", "currency", "amount", "text"}
)

// RenderRekeyCSV writes the lines of one re-keying file as RFC 4180 CSV with its header. Amounts
// are fixed to 6 decimal places, times are RFC 3339 in UTC.
func RenderRekeyCSV(kind PostingKind, lines []RekeyPosting) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	header := DocumentHeader
	if kind == KindJournal {
		header = JournalHeader
	}
	if err := w.Write(header); err != nil {
		return nil, err
	}
	for _, p := range lines {
		row := []string{p.Entity, p.DocumentRef, strconv.Itoa(p.Line), p.PostedAt.UTC().Format(time.RFC3339), p.Account, p.Dimensions, p.Currency}
		if kind == KindJournal {
			debit, credit := "", ""
			if p.Amount.IsNegative() {
				credit = p.Amount.Neg().StringFixed(6)
			} else {
				debit = p.Amount.StringFixed(6)
			}
			row = append(row, debit, credit)
		} else {
			row = append(row, p.Amount.StringFixed(6))
		}
		row = append(row, p.Text)
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}
