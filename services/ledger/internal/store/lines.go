package store

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// LineFilter selects posted journal lines for the ledger transactions inquiry
// (FR-11.14). AccountFrom and AccountTo compare account numbers as text, as the
// trial balance does. Dates are YYYY-MM-DD or empty. Voucher and SourceSystem
// match exactly, as does DocumentID (the entry's source document). Dimensions
// match the line's combination values exactly; an empty value matches lines
// whose combination does not set the attribute. After is the keyset position of
// the last line already returned.
type LineFilter struct {
	LedgerID     string
	AccountFrom  string
	AccountTo    string
	DateFrom     string
	DateTo       string
	Voucher      string
	SourceSystem string
	DocumentID   string
	Dimensions   map[string]string
	After        *LineCursor
	Limit        int
}

// LineCursor is the keyset position of a line: (posting sequence, line number)
// is unique within a ledger and is the posting order.
type LineCursor struct {
	PostingSequence int64
	LineNumber      int
}

// JournalLine is one posted line with its entry context, for drill-down to the
// entry, the source document and the trace.
type JournalLine struct {
	ID              string            `json:"id"`
	EntryID         string            `json:"entryId"`
	Voucher         string            `json:"voucher"`
	PostingSequence int64             `json:"postingSequence"`
	PostingDate     string            `json:"postingDate"`
	LineNumber      int               `json:"lineNumber"`
	Account         string            `json:"account"`
	DisplayName     string            `json:"displayName"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
	DebitCredit     string            `json:"debitCredit"`
	Amount          string            `json:"amount"`
	AmountAcct      string            `json:"amountAcct"`
	Currency        string            `json:"currency"`
	NegativePosting bool              `json:"negativePosting"`
	Text            string            `json:"text,omitempty"`
	Description     string            `json:"description,omitempty"`
	SourceSystem    string            `json:"sourceSystem"`
	SourceType      string            `json:"sourceType"`
	SourceID        string            `json:"sourceId"`
	SourceURI       string            `json:"sourceUri,omitempty"`
	SourceLineRef   string            `json:"sourceLineRef,omitempty"`
	Quantity        string            `json:"quantity,omitempty"`
	Unit            string            `json:"unit,omitempty"`
	ReversalOfEntry string            `json:"reversalOfEntryId,omitempty"`
	// IsRealignment marks a line of a dimension realignment entry or its reversal (FR-11.24).
	IsRealignment bool   `json:"isRealignment"`
	TraceID       string `json:"traceId"`
}

// LinePage is one keyset page. Next is set when more lines follow.
type LinePage struct {
	Items []JournalLine
	Next  *LineCursor
}

// ListLines returns the ledger's posted lines in posting order. An unknown
// ledger, or one outside the caller's legal-entity scope, is 404
// LEDGER_NOT_FOUND. It reads Limit+1 rows so a full page knows whether another
// page follows.
func (s *Postgres) ListLines(ctx context.Context, f LineFilter) (LinePage, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return LinePage{}, err
	}
	if f.Limit < 1 || f.Limit > MaxPageSize {
		f.Limit = MaxPageSize
	}
	args := []any{p.TenantID, f.LedgerID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := "e.tenant_id = $1 and e.ledger_id = $2"
	if f.AccountFrom != "" {
		where += " and m.account_number >= " + arg(f.AccountFrom) + "::text"
	}
	if f.AccountTo != "" {
		where += " and m.account_number <= " + arg(f.AccountTo) + "::text"
	}
	if f.DateFrom != "" {
		where += " and e.posting_date >= " + arg(f.DateFrom) + "::date"
	}
	if f.DateTo != "" {
		where += " and e.posting_date <= " + arg(f.DateTo) + "::date"
	}
	if f.Voucher != "" {
		where += " and e.voucher = " + arg(f.Voucher) + "::text"
	}
	if f.SourceSystem != "" {
		where += " and e.source_system = " + arg(f.SourceSystem) + "::text"
	}
	if f.DocumentID != "" {
		where += " and e.document_id = " + arg(f.DocumentID) + "::uuid"
	}
	// Containment: every requested attribute=value pair is on the combination
	// (an empty value: the attribute is not set).
	where += dimensionWhere(f.Dimensions, arg)
	if f.After != nil {
		where += " and (e.posting_sequence, l.line_number) > (" + arg(f.After.PostingSequence) + "::bigint, " + arg(f.After.LineNumber) + "::int)"
	}
	limit := arg(f.Limit + 1)

	out := LinePage{Items: []JournalLine{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, f.LedgerID, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select l.id::text, e.id::text, e.voucher, e.posting_sequence, e.posting_date::text, l.line_number,
				m.account_number, d.display_name, d.dimensions, l.debit_credit::text, l.amount_txn::text, l.amount_acct::text, l.currency_txn::text,
				l.negative_posting, coalesce(l.line_text, ''), coalesce(e.description, ''), e.source_system, e.source_type, e.source_id,
				coalesce(e.source_uri, ''), coalesce(l.source_line_ref, ''), coalesce(e.reversal_of_entry_id::text, ''), e.is_realignment, e.trace_id,
				coalesce(l.quantity::text, ''), coalesce(l.unit, '')
			from journal_entry e
			join journal_line l on l.tenant_id = e.tenant_id and l.entry_id = e.id
			join main_account m on m.tenant_id = l.tenant_id and m.id = l.main_account_id
			join dimension_combination d on d.tenant_id = l.tenant_id and d.id = l.dimension_combination_id
			where `+where+`
			order by e.posting_sequence, l.line_number
			limit `+limit, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j JournalLine
			if err := rows.Scan(&j.ID, &j.EntryID, &j.Voucher, &j.PostingSequence, &j.PostingDate, &j.LineNumber,
				&j.Account, &j.DisplayName, &j.Dimensions, &j.DebitCredit, &j.Amount, &j.AmountAcct, &j.Currency,
				&j.NegativePosting, &j.Text, &j.Description, &j.SourceSystem, &j.SourceType, &j.SourceID,
				&j.SourceURI, &j.SourceLineRef, &j.ReversalOfEntry, &j.IsRealignment, &j.TraceID, &j.Quantity, &j.Unit); err != nil {
				return err
			}
			j.Amount = fmtMoney(j.Amount, j.Currency)
			j.AmountAcct = fmtMoney(j.AmountAcct, ledger.Currency)
			out.Items = append(out.Items, j)
		}
		return rows.Err()
	})
	if err != nil {
		return LinePage{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		last := out.Items[len(out.Items)-1]
		out.Next = &LineCursor{PostingSequence: last.PostingSequence, LineNumber: last.LineNumber}
	}
	return out, nil
}
