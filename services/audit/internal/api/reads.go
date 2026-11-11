package api

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
)

const (
	// dutyReadLogWrite is held by services that record a read of protected health information on a caller's behalf.
	dutyReadLogWrite  = "audit.read-log.write"
	dutyReadLogView   = "audit.read-log.view"
	dutyReadLogExport = "audit.read-log.export"

	defaultReadLimit = 100
	maxReadLimit     = 500
	exportCSV        = "csv"
	exportNDJSON     = "ndjson"
)

// readRequest is the body of POST /v1/audit/reads. UserID exists only so a request that names the reader is refused with
// AUD-0002: the reader is always the verified caller.
type readRequest struct {
	Entity     string    `json:"entity"`
	RecordID   string    `json:"recordId"`
	Fields     []string  `json:"fields"`
	Purpose    string    `json:"purpose"`
	TraceID    string    `json:"traceId"`
	OccurredAt time.Time `json:"occurredAt"`
	UserID     *string   `json:"userId"`
}

// recordRead is POST /v1/audit/reads.
func (a *API) recordRead(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req readRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.UserID != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeReaderNamed,
			"userId must not be sent: the reader is the verified caller of the request"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	now := time.Now().UTC()
	if req.OccurredAt.IsZero() {
		req.OccurredAt = now
	}
	if req.TraceID == "" {
		req.TraceID = middleware.GetReqID(r.Context())
	}
	read := domain.Read{
		Entity: req.Entity, RecordID: req.RecordID, Fields: req.Fields, Purpose: req.Purpose,
		UserID: p.UserID, Source: p.Service, TraceID: req.TraceID, OccurredAt: req.OccurredAt,
	}
	if fields := domain.ValidateRead(read.Normalize(), now); len(fields) > 0 {
		pr := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		pr.Errors = fields
		httpx.Fail(w, r, pr)
		return
	}
	rec, err := a.store.RecordRead(r.Context(), read)
	if err != nil {
		a.readsFailed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.reads.Add(1)
	httpx.JSON(w, http.StatusCreated, rec)
}

// readFilter parses the shared list and export filters; ok is false once a problem was written.
func readFilter(w http.ResponseWriter, r *http.Request) (store.ReadFilter, bool) {
	q := r.URL.Query()
	f := store.ReadFilter{Entity: q.Get("entity"), RecordID: q.Get("recordId"), UserID: q.Get("userId")}
	bad := map[string]string{}
	parse := func(name string, dst **time.Time) {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				bad[name] = "must be an RFC 3339 time, for example 2026-01-31T00:00:00Z"
				return
			}
			t = t.UTC()
			*dst = &t
		}
	}
	parse("from", &f.From)
	parse("to", &f.To)
	if f.From != nil && f.To != nil && !f.To.After(*f.From) {
		bad["to"] = "must be after from"
	}
	if len(bad) > 0 {
		pr := httpx.NewProblem(http.StatusBadRequest, domain.CodeReadQuery, "the read-log filter is not valid")
		pr.Errors = bad
		httpx.Fail(w, r, pr)
		return f, false
	}
	return f, true
}

// listReads is GET /v1/audit/reads: rows in chain order, a page at a time.
func (a *API) listReads(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := readFilter(w, r)
	if !ok {
		return
	}
	f.Limit = defaultReadLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxReadLimit {
			badQuery(w, r, "limit", "must be a whole number from 1 to "+strconv.Itoa(maxReadLimit))
			return
		}
		f.Limit = n
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		raw, err := base64.RawURLEncoding.DecodeString(v)
		seq, perr := strconv.ParseInt(string(raw), 10, 64)
		if err != nil || perr != nil || seq < 1 {
			badQuery(w, r, "cursor", "is not a cursor this service issued")
			return
		}
		f.AfterSeq = seq
	}
	want := f.Limit
	f.Limit = want + 1
	rows, err := a.store.ListReads(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := map[string]any{}
	if len(rows) > want {
		rows = rows[:want]
		out["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(rows[want-1].Seq, 10)))
	}
	out["items"] = rows
	httpx.JSON(w, http.StatusOK, out)
}

func badQuery(w http.ResponseWriter, r *http.Request, name, msg string) {
	pr := httpx.NewProblem(http.StatusBadRequest, domain.CodeReadQuery, "the read-log filter is not valid")
	pr.Errors = map[string]string{name: msg}
	httpx.Fail(w, r, pr)
}

// verifyReads is GET /v1/audit/reads:verify.
func (a *API) verifyReads(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	chain, err := a.store.VerifyReads(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, chain)
}

// Chain verification headers of the export: an auditor keeps the head to prove later that no row was cut off the end.
const (
	headerChainVerified = "X-Passage-Read-Chain-Verified"
	headerChainEntries  = "X-Passage-Read-Chain-Entries"
	headerChainHead     = "X-Passage-Read-Chain-Head"
	headerChainBroken   = "X-Passage-Read-Chain-Broken-At"
)

// exportReads is GET /v1/audit/reads:export?format=csv|ndjson for one tenant. The chain is verified first and the result
// is in the response headers; the body holds the filtered rows in chain order.
func (a *API) exportReads(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = exportCSV
	}
	if format != exportCSV && format != exportNDJSON {
		badQuery(w, r, "format", "must be csv or ndjson")
		return
	}
	f, ok := readFilter(w, r)
	if !ok {
		return
	}
	var (
		cw  *csv.Writer
		enc *json.Encoder
	)
	begin := func(chain store.ReadChain) error {
		h := w.Header()
		h.Set(headerChainVerified, strconv.FormatBool(chain.Verified))
		h.Set(headerChainEntries, strconv.FormatInt(chain.Entries, 10))
		h.Set(headerChainHead, chain.Head)
		if chain.BrokenAt != nil {
			h.Set(headerChainBroken, strconv.FormatInt(*chain.BrokenAt, 10))
		}
		h.Set("Cache-Control", "no-store")
		if format == exportCSV {
			h.Set("Content-Type", "text/csv; charset=utf-8")
			h.Set("Content-Disposition", `attachment; filename="phi-read-log.csv"`)
			cw = csv.NewWriter(w)
			return cw.Write(csvHeader)
		}
		h.Set("Content-Type", "application/x-ndjson")
		h.Set("Content-Disposition", `attachment; filename="phi-read-log.ndjson"`)
		enc = json.NewEncoder(w)
		return nil
	}
	each := func(rec store.ReadRecord) error {
		if cw != nil {
			return cw.Write(csvRow(rec))
		}
		return enc.Encode(rec)
	}
	if err := a.store.ExportReads(r.Context(), f, begin, each); err != nil {
		if cw == nil && enc == nil {
			httpx.Fail(w, r, err)
		}
		return
	}
	if cw != nil {
		cw.Flush()
	}
	a.exports.Add(1)
}

var csvHeader = []string{"seq", "id", "occurredAt", "userId", "entity", "recordId", "fields", "purpose", "sourceService", "traceId", "prevHash", "hash"}

func csvRow(rec store.ReadRecord) []string {
	return []string{
		strconv.FormatInt(rec.Seq, 10), rec.ID, rec.OccurredAt.UTC().Format(time.RFC3339Nano), csvCell(rec.UserID), csvCell(rec.Entity),
		csvCell(rec.RecordID), csvCell(strings.Join(rec.Fields, ";")), csvCell(rec.Purpose), csvCell(rec.SourceService), csvCell(rec.TraceID),
		rec.PrevHash, rec.Hash,
	}
}

// csvCell stops a spreadsheet from running a free-text cell as a formula. The NDJSON export is the lossless one: an
// auditor recomputing the hash chain reads that.
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}
