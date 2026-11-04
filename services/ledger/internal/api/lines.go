package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

// defaultLinePage is the page size when limit is absent (FR-11.14 caps at 1,000).
const defaultLinePage = 100

// cursorRe matches the keyset cursor "<posting_sequence>.<line_number>".
var cursorRe = regexp.MustCompile(`^[0-9]{1,18}\.[0-9]{1,5}$`)

// lineCursor parses a cursor. The empty string means the first page.
func lineCursor(s string) (*store.LineCursor, bool) {
	if s == "" {
		return nil, true
	}
	if !cursorRe.MatchString(s) {
		return nil, false
	}
	seq, lineNo, _ := strings.Cut(s, ".")
	n, err1 := strconv.ParseInt(seq, 10, 64)
	ln, err2 := strconv.Atoi(lineNo)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	return &store.LineCursor{PostingSequence: n, LineNumber: ln}, true
}

// formatCursor renders the keyset position of the last returned line.
func formatCursor(c *store.LineCursor) string {
	return strconv.FormatInt(c.PostingSequence, 10) + "." + strconv.Itoa(c.LineNumber)
}

// listLines serves the ledger transactions inquiry (FR-11.14): posted lines
// filtered by ledger, account range, posting date, voucher, source system,
// document id and dimension values, in posting order, one keyset page at a time.
func (a *API) listLines(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.LineFilter{
		LedgerID: q.Get("ledger"), AccountFrom: q.Get("account_from"), AccountTo: q.Get("account_to"),
		DateFrom: q.Get("date_from"), DateTo: q.Get("date_to"), Voucher: q.Get("voucher"),
		SourceSystem: q.Get("source_system"), SourceType: q.Get("source_type"), DocumentID: q.Get("documentId"), Dimensions: map[string]string{}, Limit: defaultLinePage,
	}
	if !ids.Valid(f.LedgerID) {
		fields["ledger"] = "is required and must be a UUID"
	}
	for name, v := range map[string]string{"account_from": f.AccountFrom, "account_to": f.AccountTo} {
		if v != "" && !accountNumberRe.MatchString(v) {
			fields[name] = "must be 1 to 20 characters of letters, digits, dot, underscore or dash"
		}
	}
	from, fromOK := parseDate(f.DateFrom)
	to, toOK := parseDate(f.DateTo)
	if f.DateFrom != "" && !fromOK {
		fields["date_from"] = "must be a date as YYYY-MM-DD"
	}
	if f.DateTo != "" && !toOK {
		fields["date_to"] = "must be a date as YYYY-MM-DD"
	}
	if fromOK && toOK && to.Before(from) {
		fields["date_to"] = "must not be before date_from"
	}
	if f.Voucher != "" && utf8.RuneCountInString(f.Voucher) > 30 {
		fields["voucher"] = "must be at most 30 characters"
	}
	if f.SourceSystem != "" && utf8.RuneCountInString(f.SourceSystem) > 64 {
		fields["source_system"] = "must be at most 64 characters"
	}
	if f.SourceType != "" && utf8.RuneCountInString(f.SourceType) > 64 {
		fields["source_type"] = "must be at most 64 characters"
	}
	if f.DocumentID != "" && !ids.Valid(f.DocumentID) {
		fields["documentId"] = "must be a UUID"
	}
	for key, values := range q {
		attr, ok := strings.CutPrefix(key, "dim.")
		if !ok {
			continue
		}
		if !dimAttrRe.MatchString(attr) {
			fields[key] = "the attribute must be A-Z, digits or underscore, starting with a letter"
			continue
		}
		if len(values) != 1 || utf8.RuneCountInString(values[0]) > 20 {
			fields[key] = "must be given once, at most 20 characters (empty: the attribute is not set)"
			continue
		}
		f.Dimensions[attr] = values[0]
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			fields["limit"] = "must be an integer from 1 to 1000"
		} else {
			f.Limit = n
		}
	}
	after, ok := lineCursor(q.Get("cursor"))
	if !ok {
		fields["cursor"] = "is not a cursor returned by this endpoint"
	}
	f.After = after
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListLines(r.Context(), f)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	items := page.Items
	if items == nil {
		items = []store.JournalLine{}
	}
	body := map[string]any{"items": items}
	if page.Next != nil {
		body["nextCursor"] = formatCursor(page.Next)
	}
	httpx.JSON(w, http.StatusOK, body)
}
