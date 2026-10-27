package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/domain"
)

// Request bounds for the slice-3 previews.
const (
	// MaxReclassRules bounds the rules of one reclassification dry run.
	MaxReclassRules = 200
	// MaxReclassRows bounds the open items and the group balances of one dry run.
	MaxReclassRows = 100000
	// MaxJournalLines bounds the lines of one top-side journal.
	MaxJournalLines = 1000
)

// dateLayout is the ISO 8601 calendar date the API accepts and returns.
const dateLayout = "2006-01-02"

func (f decimals) date(name, raw string) time.Time {
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		f[name] = "must be a date such as 2026-12-31"
	}
	return t
}

func (f decimals) text(name, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		f[name] = "is required"
	}
	return v
}

// ---- E6 dividends within the group (FR-40.9, BR-40.9) ----

type dividendRequest struct {
	Currency     string `json:"currency"`
	OwnershipPct string `json:"ownershipPct"`
	Rate         string `json:"rate"`
	Amount       string `json:"amount"`
}

type dividendResponse struct {
	Currency string      `json:"currency"`
	Entries  []entryBody `json:"entries"`
	Totals   totalsBody  `json:"totals"`
	Rounding string      `json:"rounding"`
	Balanced bool        `json:"balanced"`
}

func (a *API) previewDividends(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req dividendRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	in := domain.Dividend{
		Currency:     req.Currency,
		OwnershipPct: f.get("ownershipPct", req.OwnershipPct, true),
		Rate:         f.get("rate", req.Rate, true),
		Amount:       f.get("amount", req.Amount, true),
	}
	if !validation(w, r, f) {
		return
	}
	entries, err := domain.EliminateDividends(in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.dividendPreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	rounding := decimal.Zero
	for _, e := range entries {
		if e.Account == domain.AcctRounding {
			rounding = e.Debit.Sub(e.Credit)
		}
	}
	dr, cr := domain.Totals(entries)
	httpx.JSON(w, http.StatusOK, dividendResponse{
		Currency: req.Currency, Entries: entriesOf(entries, places),
		Totals:   totalsBody{Debit: dr.StringFixed(places), Credit: cr.StringFixed(places)},
		Rounding: rounding.StringFixed(places), Balanced: domain.CheckBalanced(entries) == nil,
	})
}

// ---- reclassification and regrouping dry run (FR-40.17, BR-40.14, AC-40.13) ----

type reclassRuleBody struct {
	Code          string `json:"code"`
	Level         string `json:"level"`
	Type          string `json:"type"`
	Order         int    `json:"order"`
	Version       int    `json:"version"`
	SourceAccount string `json:"sourceAccount"`
	TargetAccount string `json:"targetAccount"`
	Threshold     string `json:"threshold"`
	TermMonths    int    `json:"termMonths"`
}

type openItemBody struct {
	Party   string `json:"party"`
	Account string `json:"account"`
	DueDate string `json:"dueDate"`
	Amount  string `json:"amount"`
}

type groupBalanceBody struct {
	Account string `json:"account"`
	Amount  string `json:"amount"`
}

type reclassRequest struct {
	Currency  string             `json:"currency"`
	PeriodEnd string             `json:"periodEnd"`
	Rules     []reclassRuleBody  `json:"rules"`
	OpenItems []openItemBody     `json:"openItems"`
	Balances  []groupBalanceBody `json:"balances"`
}

type reclassLineBody struct {
	Rule     string `json:"rule"`
	Level    string `json:"level"`
	Account  string `json:"account"`
	Party    string `json:"party,omitempty"`
	Debit    string `json:"debit"`
	Credit   string `json:"credit"`
	Reverses bool   `json:"reverses"`
}

type reclassResponse struct {
	Currency     string            `json:"currency"`
	PeriodEnd    string            `json:"periodEnd"`
	DryRun       bool              `json:"dryRun"`
	Applied      []string          `json:"applied"`
	ReversalDate string            `json:"reversalDate,omitempty"`
	Lines        []reclassLineBody `json:"lines"`
	Totals       totalsBody        `json:"totals"`
}

func (a *API) previewReclassification(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req reclassRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	periodEnd := f.date("periodEnd", req.PeriodEnd)
	switch {
	case len(req.Rules) == 0:
		f["rules"] = "must contain at least one rule"
	case len(req.Rules) > MaxReclassRules:
		f["rules"] = fmt.Sprintf("must contain at most %d rules", MaxReclassRules)
	}
	if len(req.OpenItems) > MaxReclassRows {
		f["openItems"] = fmt.Sprintf("must contain at most %d items", MaxReclassRows)
	}
	if len(req.Balances) > MaxReclassRows {
		f["balances"] = fmt.Sprintf("must contain at most %d balances", MaxReclassRows)
	}
	rules := make([]domain.ReclassRule, 0, len(req.Rules))
	if _, bad := f["rules"]; !bad {
		for i, rb := range req.Rules {
			at := func(name string) string { return fmt.Sprintf("rules[%d].%s", i, name) }
			rule := domain.ReclassRule{
				Code: f.text(at("code"), rb.Code), Level: rb.Level, Type: rb.Type, Order: rb.Order, Version: rb.Version,
				SourceAccount: f.text(at("sourceAccount"), rb.SourceAccount), TargetAccount: f.text(at("targetAccount"), rb.TargetAccount),
				Threshold: f.get(at("threshold"), rb.Threshold, false), TermMonths: rb.TermMonths,
			}
			if rule.Version == 0 {
				rule.Version = 1
			}
			rules = append(rules, rule)
		}
	}
	var items []domain.OpenItem
	if _, bad := f["openItems"]; !bad {
		for i, it := range req.OpenItems {
			at := func(name string) string { return fmt.Sprintf("openItems[%d].%s", i, name) }
			item := domain.OpenItem{Party: f.text(at("party"), it.Party), Account: f.text(at("account"), it.Account), Amount: f.get(at("amount"), it.Amount, true)}
			if it.DueDate != "" {
				item.DueDate = f.date(at("dueDate"), it.DueDate)
			}
			items = append(items, item)
		}
	}
	var balances []domain.GroupBalance
	if _, bad := f["balances"]; !bad {
		for i, b := range req.Balances {
			at := func(name string) string { return fmt.Sprintf("balances[%d].%s", i, name) }
			balances = append(balances, domain.GroupBalance{Account: f.text(at("account"), b.Account), Amount: f.get(at("amount"), b.Amount, true)})
		}
	}
	if !validation(w, r, f) {
		return
	}
	res, err := domain.RunReclassification(rules, items, balances, periodEnd, req.Currency)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.reclassPreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	out := reclassResponse{Currency: req.Currency, PeriodEnd: periodEnd.Format(dateLayout), DryRun: true,
		Applied: append([]string{}, res.Applied...), Lines: make([]reclassLineBody, 0, len(res.Lines))}
	if !res.ReversalDate.IsZero() {
		out.ReversalDate = res.ReversalDate.Format(dateLayout)
	}
	dr, cr := decimal.Zero, decimal.Zero
	for _, l := range res.Lines {
		out.Lines = append(out.Lines, reclassLineBody{Rule: l.RuleCode, Level: l.Level, Account: l.Account, Party: l.Party,
			Debit: l.Debit.StringFixed(places), Credit: l.Credit.StringFixed(places), Reverses: l.Reverses})
		dr, cr = dr.Add(l.Debit), cr.Add(l.Credit)
	}
	out.Totals = totalsBody{Debit: dr.StringFixed(places), Credit: cr.StringFixed(places)}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- top-side group journal check (FR-40.19, BR-40.18, CNS-0909) ----

type journalLineBody struct {
	FSItem      string `json:"fsItem"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
	PartnerUnit string `json:"partnerUnit"`
}

type groupJournalRequest struct {
	PeriodKey            string            `json:"periodKey"`
	DocumentType         string            `json:"documentType"`
	UnitID               string            `json:"unitId"`
	Currency             string            `json:"currency"`
	Reason               string            `json:"reason"`
	AttachmentRef        string            `json:"attachmentRef"`
	CarryForward         bool              `json:"carryForward"`
	FiscalYearStartMonth int               `json:"fiscalYearStartMonth"`
	ApprovalThreshold    string            `json:"approvalThreshold"`
	Lines                []journalLineBody `json:"lines"`
}

type groupJournalResponse struct {
	Currency             string        `json:"currency"`
	Level                string        `json:"level"`
	Total                string        `json:"total"`
	Balanced             bool          `json:"balanced"`
	ApprovalThreshold    string        `json:"approvalThreshold"`
	ApprovalRequired     bool          `json:"approvalRequired"`
	Periods              []string      `json:"periods"`
	ContinuesPastYearEnd bool          `json:"continuesPastYearEnd"`
	Warnings             []warningBody `json:"warnings"`
}

// previewGroupJournal checks a top-side journal before it is posted: the
// invariants (CNS-0903 when unbalanced), whether posting needs approval at the
// threshold (CNS-0909) and the periods it applies to (BR-40.18). Nothing is
// posted; posting and the approval workflow come with persistence.
func (a *API) previewGroupJournal(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req groupJournalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := decimals{}
	currencyField(f, req.Currency)
	if !periodText.MatchString(req.PeriodKey) {
		f["periodKey"] = "must be a period key such as 2026-11"
	}
	if !knownDocumentType(req.DocumentType) {
		f["documentType"] = "must be " + strings.Join(domain.DocumentTypes, ", ")
	}
	j := domain.GroupJournal{
		PeriodKey: req.PeriodKey, DocumentType: req.DocumentType, UnitID: strings.TrimSpace(req.UnitID), Currency: req.Currency,
		Reason: f.text("reason", req.Reason), AttachmentRef: f.text("attachmentRef", req.AttachmentRef), CarryForward: req.CarryForward,
	}
	fyStart := req.FiscalYearStartMonth
	if fyStart == 0 {
		fyStart = 1
	}
	if fyStart < 1 || fyStart > 12 {
		f["fiscalYearStartMonth"] = "must be 1 to 12"
	}
	threshold := domain.DefaultApprovalThreshold()
	if req.ApprovalThreshold != "" {
		threshold = f.get("approvalThreshold", req.ApprovalThreshold, true)
		if _, bad := f["approvalThreshold"]; !bad && !threshold.IsPositive() {
			f["approvalThreshold"] = "must be above 0"
		}
	}
	switch {
	case len(req.Lines) < 2:
		f["lines"] = "must contain at least two lines"
	case len(req.Lines) > MaxJournalLines:
		f["lines"] = fmt.Sprintf("must contain at most %d lines", MaxJournalLines)
	}
	if _, bad := f["lines"]; !bad {
		for i, l := range req.Lines {
			at := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
			line := domain.GroupJournalLine{
				FSItem: f.text(at("fsItem"), l.FSItem), PartnerUnit: strings.TrimSpace(l.PartnerUnit),
				Debit: f.get(at("debit"), l.Debit, false), Credit: f.get(at("credit"), l.Credit, false),
			}
			_, badDr := f[at("debit")]
			_, badCr := f[at("credit")]
			if !badDr && !badCr && line.Debit.IsPositive() == line.Credit.IsPositive() {
				f[at("debit")] = "enter a positive debit or a positive credit, not both"
			}
			j.Lines = append(j.Lines, line)
		}
	}
	if !validation(w, r, f) {
		return
	}
	if err := j.Validate(); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	periods, past, err := j.Periods(fyStart)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.groupJournalPreviews.Add(1)
	places := money.MinorUnits(req.Currency)
	out := groupJournalResponse{
		Currency: req.Currency, Level: "Group", Total: j.Total().StringFixed(places), Balanced: true,
		ApprovalThreshold: threshold.StringFixed(places), ApprovalRequired: j.ApprovalRequired(threshold),
		Periods: periods, ContinuesPastYearEnd: past, Warnings: []warningBody{},
	}
	if j.UnitID != "" {
		out.Level = "Unit"
	}
	var de *domain.Error
	if errors.As(j.CheckApproval(threshold), &de) {
		out.Warnings = append(out.Warnings, warningBody{Code: de.Code, Message: de.Detail})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// periodText is the ledger's period key format (wave 11).
var periodText = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

func knownDocumentType(t string) bool {
	for _, d := range domain.DocumentTypes {
		if d == t {
			return true
		}
	}
	return false
}
