package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	pmoney "github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

// Problem codes of the reimbursement liability posting (listed in the service README).
const (
	CodeGLNotConfigured = "EXP-GL-001"
	CodeGLUnavailable   = "EXP-GL-002"
	CodeGLNoProfile     = "EXP-GL-003"
	CodeGLNotMapped     = "EXP-GL-004"
	CodeGLRefused       = "EXP-GL-005"
	CodeGLLinesMismatch = "EXP-GL-006"
	dutyGLPost          = "expense.gl.post"
	glSourceType        = "expense_report"
	glKeyPrefix         = "expense-report-"
	defaultGLBatch      = 200
	EnvGLProfiles       = "EXPENSE_GL_PROFILES"
)

var accountText = regexp.MustCompile(`^[A-Za-z0-9._-]{1,20}$`)

// GLProfile is the server-side posting configuration of one legal entity: its ledger, the AP
// control account payables pays reimbursements from, and the expense account of each line category.
type GLProfile struct {
	LegalEntityID    string            `json:"legalEntityId"`
	LedgerID         string            `json:"ledgerId"`
	APControlAccount string            `json:"apControlAccount"`
	ExpenseAccounts  map[string]string `json:"expenseAccounts"`
}

// GLBooks is the persistence of the liability posting (satisfied by *store.Store).
type GLBooks interface {
	DueGLReports(ctx context.Context, legalEntityID string, limit int) ([]store.GLReport, bool, error)
	MarkGLBooked(ctx context.Context, id, entryID string) (bool, error)
}

// ParseGLProfiles reads EXPENSE_GL_PROFILES: a JSON array of GLProfile, one per legal entity.
func ParseGLProfiles(raw string) (map[string]GLProfile, error) {
	var list []GLProfile
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of profiles: %w", EnvGLProfiles, err)
	}
	out := map[string]GLProfile{}
	for _, p := range list {
		le, leOK := ids.Canonical(p.LegalEntityID)
		ledger, ledgerOK := ids.Canonical(p.LedgerID)
		valid := leOK && ledgerOK && accountText.MatchString(p.APControlAccount) && len(p.ExpenseAccounts) > 0
		for category, account := range p.ExpenseAccounts {
			valid = valid && category != "" && accountText.MatchString(account)
		}
		if !valid {
			return nil, fmt.Errorf("%s entry for %q needs legalEntityId and ledgerId UUIDs, apControlAccount and expenseAccounts (category to account, at most 20 characters)", EnvGLProfiles, p.LegalEntityID)
		}
		if _, dup := out[le]; dup {
			return nil, fmt.Errorf("%s names legal entity %s twice", EnvGLProfiles, le)
		}
		p.LegalEntityID, p.LedgerID = le, ledger
		out[le] = p
	}
	return out, nil
}

// WithGL attaches the general ledger posting of reimbursement liabilities.
func (a *API) WithGL(ledger ledgerclient.Ledger, books GLBooks, profiles map[string]GLProfile) *API {
	a.ledger, a.glBooks, a.glProfiles = ledger, books, profiles
	return a
}

func (a *API) registerGL(r chi.Router) {
	r.With(authz.RequireDuty(dutyGLPost)).Post("/v1/expense/gl-postings", a.postGL)
}

type glPostingRequest struct {
	LegalEntityID string `json:"legalEntityId"`
}

type glBookedOut struct {
	ReportID       string `json:"reportId"`
	Currency       string `json:"currency"`
	Amount         string `json:"amount"`
	PostingDate    string `json:"postingDate"`
	JournalEntryID string `json:"journalEntryId"`
}

type glFailedOut struct {
	ReportID string `json:"reportId"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
}

// glEntry builds the liability journal of a report: Dr the expense account of each category of its
// employee-paid lines, Cr AP control for the reimbursable amount. The ledger key is derived from the
// report id, so a retry after a lost answer returns the entry posted first.
func glEntry(p GLProfile, g store.GLReport) (ledgerclient.Entry, string, string) {
	mu := pmoney.MinorUnits(g.Currency)
	e := ledgerclient.Entry{LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: g.ID, PostingDate: g.ApprovedOn,
		Currency: g.Currency, IdempotencyKey: glKeyPrefix + g.ID, SourceType: glSourceType}
	text := "expense report " + g.ID[:8]
	categories := make([]string, 0, len(g.ByCategory))
	for c := range g.ByCategory {
		categories = append(categories, c)
	}
	sort.Strings(categories)
	debits := decimal.Zero
	for _, c := range categories {
		account, ok := p.ExpenseAccounts[c]
		if !ok {
			return e, CodeGLNotMapped, "category " + c + " has no expense account in the posting profile"
		}
		amount := g.ByCategory[c].Round(mu)
		if !amount.IsPositive() {
			continue
		}
		debits = debits.Add(amount)
		e.Lines = append(e.Lines, ledgerclient.Line{Account: account, Amount: amount, Text: text + " " + strings.ToLower(c)})
	}
	credit := g.Reimbursable.Round(mu)
	if !debits.Equal(credit) {
		return e, CodeGLLinesMismatch, "the employee-paid lines total " + debits.StringFixed(mu) + ", not the reimbursable " + credit.StringFixed(mu)
	}
	e.Lines = append(e.Lines, ledgerclient.Line{Account: p.APControlAccount, Credit: true, Amount: credit, Text: text + " reimbursement"})
	return e, "", ""
}

// postGL posts the reimbursement liability of every approved report of the legal entity not yet in
// the general ledger, oldest approval first, and only then hands each report to payables
// (store.MarkGLBooked publishes report.approved). Ledger, accounts and amounts come from the posting
// profile and the stored report, never the request. A report the ledger refuses, or whose lines
// cannot be mapped, is listed in failed and stays due; a ledger that does not answer ends the call
// with a 502 naming the reports booked so far.
func (a *API) postGL(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	if a.ledger == nil || a.glBooks == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLNotConfigured, "the general ledger is not configured for this service"))
		return
	}
	var req glPostingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	le, valid := ids.Canonical(req.LegalEntityID)
	if !valid {
		refuse(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeOutOfScope, "the caller is scoped to another legal entity"))
		return
	}
	profile, ok := a.glProfiles[le]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeGLNoProfile, "no GL posting profile is configured for this legal entity"))
		return
	}
	ctx, cid := r.Context(), middleware.GetReqID(r.Context())
	due, more, err := a.glBooks.DueGLReports(ctx, le, defaultGLBatch)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, failed := make([]glBookedOut, 0, len(due)), make([]glFailedOut, 0)
	for _, g := range due {
		entry, code, detail := glEntry(profile, g)
		if code != "" {
			slog.WarnContext(ctx, "reimbursement liability not posted", "reportId", g.ID, "code", code, "detail", detail, "correlationId", cid)
			failed = append(failed, glFailedOut{ReportID: g.ID, Code: code, Detail: detail})
			continue
		}
		entryID, err := a.ledger.Post(ctx, entry)
		// A key conflict means an earlier attempt posted other lines (the profile changed since): it needs a person, not a retry.
		if ledgerclient.Rejected(err) || ledgerclient.CodeOf(err) == ledgerclient.CodeIdempotencyConflict {
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = CodeGLRefused
			}
			slog.WarnContext(ctx, "general ledger refused a reimbursement liability", "reportId", g.ID, "ledgerCode", code, "correlationId", cid)
			failed = append(failed, glFailedOut{ReportID: g.ID, Code: code, Detail: "the general ledger refused the journal"})
			continue
		}
		if err != nil {
			slog.ErrorContext(ctx, "general ledger call failed", "reportId", g.ID, "ledgerCode", ledgerclient.CodeOf(err), "err", err, "correlationId", cid)
			prob := httpx.NewProblem(http.StatusBadGateway, CodeGLUnavailable, "the general ledger did not answer")
			booked := make([]string, len(items))
			for i, it := range items {
				booked[i] = it.ReportID
			}
			prob.Errors = map[string]string{"booked": strings.Join(booked, ",")}
			httpx.Fail(w, r, prob)
			return
		}
		newly, err := a.glBooks.MarkGLBooked(ctx, g.ID, entryID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if newly {
			a.glPostings.Add(1)
		}
		items = append(items, glBookedOut{ReportID: g.ID, Currency: g.Currency, Amount: g.Reimbursable.StringFixed(pmoney.MinorUnits(g.Currency)),
			PostingDate: g.ApprovedOn, JournalEntryID: entryID})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "failed": failed, "more": more})
}
