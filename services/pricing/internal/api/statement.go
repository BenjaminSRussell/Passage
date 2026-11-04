package api

import (
	"bufio"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	rebateStatementPath = rebateAgreementsPath + "/{id}/statement"
	journalAuditPath    = journalsPath + "/{id}/audit-extract"
	// maxAuditExtractJSONLines caps the lines of the JSON audit extract; the CSV carries every line.
	maxAuditExtractJSONLines = 5000
)

type statementLineView struct {
	InvoiceLineID string `json:"invoiceLineId"`
	InvoiceDate   string `json:"invoiceDate"`
	// BasisValue and BasisQuantity are the line's own basis; a RETRO_DELTA line has none.
	BasisValue    string `json:"basisValue,omitempty"`
	BasisQuantity string `json:"basisQuantity,omitempty"`
	Amount        string `json:"amount"`
	Status        string `json:"status"`
	Retroactive   bool   `json:"retroactive"`
	Source        string `json:"source"`
}

type rebateStatementView struct {
	AgreementID     string              `json:"agreementId"`
	Name            string              `json:"name"`
	PartyRole       string              `json:"partyRole"`
	CustomerID      string              `json:"customerId,omitempty"`
	CustomerGroupID string              `json:"customerGroupId,omitempty"`
	VendorID        string              `json:"vendorId,omitempty"`
	Basis           string              `json:"basis"`
	Currency        string              `json:"currency"`
	Period          string              `json:"period"`
	Tier            *tierDTO            `json:"tier,omitempty"`
	BasisValue      string              `json:"basisValue"`
	BasisQuantity   string              `json:"basisQuantity"`
	TargetAmount    string              `json:"targetAmount"`
	AccruedAmount   string              `json:"accruedAmount"`
	LinesAccrued    string              `json:"linesAccruedAmount"`
	SettledAmount   string              `json:"settledAmount"`
	OpenAmount      string              `json:"openAmount"`
	Reconciled      bool                `json:"reconciled"`
	LineCount       int                 `json:"lineCount"`
	LinesTruncated  bool                `json:"linesTruncated"`
	Lines           []statementLineView `json:"lines"`
}

func minor(d decimal.Decimal, currency string) string {
	return d.RoundBank(money.MinorUnits(currency)).StringFixed(money.MinorUnits(currency))
}

func statementResponse(s domain.RebateStatement) rebateStatementView {
	a, cur := s.Agreement, s.Agreement.Currency
	v := rebateStatementView{AgreementID: a.ID, Name: a.Name, PartyRole: domain.PartyRoleOf(a), CustomerID: a.CustomerID,
		CustomerGroupID: a.CustomerGroupID, VendorID: a.VendorID, Basis: a.Basis, Currency: cur, Period: s.PeriodKey,
		BasisValue: s.BasisValue.String(), BasisQuantity: s.BasisQty.String(), TargetAmount: minor(s.Target, cur),
		AccruedAmount: minor(s.Accrued, cur), LinesAccrued: minor(s.RowsAccrued, cur), SettledAmount: minor(s.Settled, cur), OpenAmount: minor(s.Open, cur),
		Reconciled: s.Reconciled, LineCount: s.LineCount, LinesTruncated: s.Truncated, Lines: make([]statementLineView, 0, len(s.Lines))}
	if s.Tier != nil {
		v.Tier = &tierDTO{From: s.Tier.From.String(), RatePct: s.Tier.RatePct.String(), AmountPerUnit: s.Tier.AmountPerUnit.String()}
	}
	for _, l := range s.Lines {
		lv := statementLineView{InvoiceLineID: l.InvoiceLineID, InvoiceDate: l.InvoiceDate, Amount: minor(l.Accrued, cur),
			Status: l.Status, Retroactive: l.Retroactive, Source: l.Source()}
		if lv.Source != domain.SourceRetroDelta {
			lv.BasisValue, lv.BasisQuantity = l.BasisValue.String(), l.BasisQty.String()
		}
		v.Lines = append(v.Lines, lv)
	}
	return v
}

// rebateStatementColumns is the header of the statement CSV: rowType is LINE for an accrual
// row and TOTAL_ACCRUED, TOTAL_SETTLED or OPEN for the footer rows (amount only); source is
// INVOICE, REPLAYED or RETRO_DELTA (which has no basis of its own).
var rebateStatementColumns = []string{"rowType", "period", "invoiceLineId", "invoiceDate", "basisValue", "basisQuantity", "amount", "currency", "status", "source"}

// rebateStatement is the rebate agreement statement of one accrual period (PRD §5.26.15):
// header, one line per accrual row and the footer accrued, settled and open. format=csv
// downloads the same document as CSV.
func (a *API) rebateStatement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "no rebate agreement "+id))
		return
	}
	q := r.URL.Query()
	format := q.Get("format")
	if format != "" && format != "json" && format != "csv" {
		validationFailed(w, r, map[string]string{"format": "must be json or csv"})
		return
	}
	key := q.Get("period")
	agreement, in, err := a.store.RebateStatementInput(r.Context(), id, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	st := domain.BuildRebateStatement(agreement, key, in)
	if format != "csv" {
		httpx.JSON(w, http.StatusOK, statementResponse(st))
		return
	}
	v := statementResponse(st)
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="rebate-statement-`+id+`-`+key+`.csv"`)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if v.LinesTruncated {
		h.Set("X-Passage-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	cw := csv.NewWriter(w)
	_ = cw.Write(rebateStatementColumns)
	c := domain.CSVCell
	for _, l := range v.Lines {
		_ = cw.Write([]string{"LINE", key, c(l.InvoiceLineID), l.InvoiceDate, c(l.BasisValue), c(l.BasisQuantity), c(l.Amount), v.Currency, l.Status, l.Source})
	}
	for _, f := range [][2]string{{"TOTAL_ACCRUED", v.AccruedAmount}, {"TOTAL_SETTLED", v.SettledAmount}, {"OPEN", v.OpenAmount}} {
		_ = cw.Write([]string{f[0], key, "", "", "", "", c(f[1]), v.Currency, "", ""})
	}
	// The document is at most MaxStatementLines rows and was read in full before the 200, so the
	// only failure left is the client's connection; log it rather than pretend it was delivered.
	cw.Flush()
	if err := cw.Error(); err != nil {
		slog.Warn("rebate statement CSV write failed", "err", err, "agreementId", id, "correlationId", middleware.GetReqID(r.Context()))
	}
}

// auditExtractColumns is the header of the price journal audit extract CSV: the journal
// header repeated on every row, the line, and the key and values of the record before and after.
var auditExtractColumns = []string{"journalId", "journalName", "journalStatus", "legalEntityId", "createdBy", "postedBy", "postedAt",
	"reversesJournalId", "reversedByJournalId", "lineNo", "op", "recordId", "expectedVersion", "adjustPct",
	"type", "customerKind", "customerKey", "productKind", "productKey", "variant", "currency", "unit",
	"beforeFromQuantity", "beforeAmount", "beforeDiscPct1", "beforeDiscPct2", "beforeValidFrom", "beforeValidTo", "beforeStatus", "beforeVersion",
	"afterFromQuantity", "afterAmount", "afterDiscPct1", "afterDiscPct2", "afterValidFrom", "afterValidTo", "afterStatus", "afterVersion"}

// journalAuditExtract is the price journal audit extract (PRD §5.26.15): the header and every
// line with its before and after values, for a POSTED or REVERSED journal (the extract is
// triggered by the post or the reversal; PRC-0064 otherwise). The before and after values are
// the ones stored on the line when the journal posted, never recomputed, so a later change to
// the record leaves the extract as it was. CSV (default) streams every line as it is read (a
// journal can hold 100,000 lines) and aborts the connection if the read fails after the 200, so
// a client never keeps a short file that looks complete; JSON carries at most
// maxAuditExtractJSONLines and flags linesTruncated.
func (a *API) journalAuditExtract(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := journalID(w, r)
	if !ok {
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "csv" {
		validationFailed(w, r, map[string]string{"format": "must be json or csv"})
		return
	}
	posted := func(j store.Journal) error {
		if j.Status != domain.JournalPosted && j.Status != domain.JournalReversed {
			return httpx.NewProblem(http.StatusConflict, store.CodeJournalNotPosted,
				"price journal "+id+" is "+j.Status+"; the audit extract exists once it is posted or reversed")
		}
		return nil
	}
	if format == "json" {
		a.journalAuditJSON(w, r, id, posted)
		return
	}
	bw := bufio.NewWriter(w)
	cw := csv.NewWriter(bw)
	flusher, _ := w.(http.Flusher)
	flush := func() error {
		cw.Flush()
		if err := cw.Error(); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	c := domain.CSVCell
	var head []string
	var legalEntityID string
	started, n := false, 0
	start := func(j store.Journal) error {
		if err := posted(j); err != nil {
			return err
		}
		hv := journalResponse(j)
		postedAt := ""
		if hv.PostedAt != nil {
			postedAt = hv.PostedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		head = []string{hv.ID, c(hv.Name), hv.Status, hv.LegalEntityID, c(hv.CreatedBy), c(hv.PostedBy), postedAt,
			hv.ReversesJournalID, hv.ReversedByJournalID}
		legalEntityID = j.LegalEntityID
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="price-journal-`+id+`-audit.csv"`)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		started = true
		return cw.Write(auditExtractColumns)
	}
	line := func(l domain.JournalLine) error {
		lv := journalLineOf(legalEntityID, l)
		key := lv.After
		if key == nil {
			key = lv.Before
		}
		row := append(append(make([]string, 0, len(auditExtractColumns)), head...),
			strconv.Itoa(lv.LineNo), lv.Op, lv.RecordID, strconv.Itoa(lv.ExpectedVersion), c(lv.AdjustPct))
		if key != nil {
			row = append(row, key.Type, key.CustomerKind, c(key.CustomerKey), key.ProductKind, c(key.ProductKey), c(key.Variant), c(key.Currency), c(key.Unit))
		} else {
			row = append(row, "", "", "", "", "", "", "", "")
		}
		for _, snap := range []*recordView{lv.Before, lv.After} {
			if snap == nil {
				row = append(row, "", "", "", "", "", "", "", "")
				continue
			}
			row = append(row, c(snap.FromQuantity), c(snap.Amount), c(snap.DiscPct1), c(snap.DiscPct2), snap.ValidFrom, snap.ValidTo, snap.Status, strconv.Itoa(snap.Version))
		}
		if err := cw.Write(row); err != nil {
			return err
		}
		if n++; n%exportFlushRows == 0 {
			return flush()
		}
		return nil
	}
	err := a.store.StreamJournal(r.Context(), id, start, line)
	if err == nil {
		err = flush()
	}
	if err == nil {
		return
	}
	if !started {
		httpx.Fail(w, r, err)
		return
	}
	// The status line is sent: abort the connection so the client sees a failed download, not a
	// short file that looks complete.
	slog.Error("price journal audit extract failed", "err", errors.Join(errExportStarted, err), "journalId", id, "rows", n,
		"correlationId", middleware.GetReqID(r.Context()))
	panic(http.ErrAbortHandler)
}

// errAuditJSONFull stops the journal read once the JSON extract holds one line past its cap.
var errAuditJSONFull = errors.New("audit extract JSON is full")

// journalAuditJSON is the JSON audit extract: the journal and at most maxAuditExtractJSONLines
// lines (linesTruncated past that), read up to one line past the cap and written in one piece.
func (a *API) journalAuditJSON(w http.ResponseWriter, r *http.Request, id string, posted func(store.Journal) error) {
	var v journalView
	var legalEntityID string
	start := func(j store.Journal) error {
		if err := posted(j); err != nil {
			return err
		}
		v, legalEntityID = journalResponse(j), j.LegalEntityID
		v.Lines = make([]journalLineView, 0, min(j.LineCount, maxAuditExtractJSONLines))
		return nil
	}
	line := func(l domain.JournalLine) error {
		if len(v.Lines) == maxAuditExtractJSONLines {
			v.LinesTruncated = true
			return errAuditJSONFull
		}
		v.Lines = append(v.Lines, journalLineOf(legalEntityID, l))
		return nil
	}
	if err := a.store.StreamJournal(r.Context(), id, start, line); err != nil && !errors.Is(err, errAuditJSONFull) {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, v)
}

// registerStatementRoutes mounts the printed-document reads: the rebate agreement statement
// and the price journal audit extract (PRD §5.26.15).
func (a *API) registerStatementRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyRebateView)).Get(rebateStatementPath, a.rebateStatement)
	r.With(authz.RequireDuty(dutyView)).Get(journalAuditPath, a.journalAuditExtract)
}
