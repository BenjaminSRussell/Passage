package api

import (
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const maxRecurringName = 80

// registerRecurring mounts recurring journals (FR-11.10). Maintaining and
// generating them is "Maintain ledger journals"; the batches they make are
// posted by the normal post duty.
func (a *API) registerRecurring(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/recurring-journals", a.listRecurring)
	r.With(authz.RequireDuty(dutyRead)).Get("/recurring-journals/{id}", a.getRecurring)
	r.With(authz.RequireDuty(dutyJournalWrite)).Put("/recurring-journals/{id}", a.putRecurring)
	r.With(authz.RequireDuty(dutyJournalWrite)).Post("/recurring-journals/{id}/pause", a.pauseRecurring)
	r.With(authz.RequireDuty(dutyJournalWrite)).Post("/recurring-journals/{id}/resume", a.resumeRecurring)
	r.With(authz.RequireDuty(dutyJournalWrite)).Post("/recurring-journals/{id}/generate", a.generateRecurring)
	r.With(authz.RequireDuty(dutyRead)).Get("/recurring-journals/{id}/runs", a.listRecurringRuns)
}

type recurringLineDTO struct {
	Account     string            `json:"account"`
	Dimensions  map[string]string `json:"dimensions"`
	DebitCredit string            `json:"debitCredit"`
	AmountKind  string            `json:"amountKind"`
	Amount      amountDTO         `json:"amount"`
	Text        string            `json:"text"`
}

type recurringRequest struct {
	LedgerID    string             `json:"ledgerId"`
	JournalName string             `json:"journalName"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Frequency   string             `json:"frequency"`
	DayOfMonth  int                `json:"dayOfMonth"`
	StartDate   string             `json:"startDate"`
	EndDate     string             `json:"endDate"`
	BaseAmount  string             `json:"baseAmount"`
	Lines       []recurringLineDTO `json:"lines"`
}

type generateRecurringRequest struct {
	ScheduledDate string `json:"scheduledDate"`
	BaseAmount    string `json:"baseAmount"`
}

func recurringTarget(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	if _, ok := principal(w, r); !ok {
		return "", 0, false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRecurringNotFound, "recurring journal not found"))
		return "", 0, false
	}
	version, ok := parseIfMatch(r.Header.Get("If-Match"))
	if !ok {
		validationFailed(w, r, map[string]string{"If-Match": `must be the recurring journal version as an entity tag, for example "3"`})
		return "", 0, false
	}
	return id, version, true
}

func writeRecurring(w http.ResponseWriter, status int, rj store.RecurringJournal) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(rj.Version)))
	httpx.JSON(w, status, rj)
}

func (a *API) listRecurring(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != store.RecurringActive && status != store.RecurringPaused {
		validationFailed(w, r, map[string]string{"status": "must be active or paused"})
		return
	}
	items, err := a.store.ListRecurring(r.Context(), status)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRecurring(w http.ResponseWriter, r *http.Request) {
	id, _, ok := recurringTarget(w, r)
	if !ok {
		return
	}
	rj, err := a.store.GetRecurring(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeRecurring(w, http.StatusOK, rj)
}

// checkRecurringLines validates line shape: a fixed amount follows the
// currency's minor units, a percentage is 0 to 1000 with at most 6 decimals.
func checkRecurringLines(lines []recurringLineDTO, fields map[string]string) []store.RecurringLine {
	if len(lines) == 0 {
		fields["lines"] = "at least one line is required"
		return nil
	}
	if len(lines) > domain.MaxLinesPerEntry {
		fields["lines"] = fmt.Sprintf("at most %d lines", domain.MaxLinesPerEntry)
		return nil
	}
	out := make([]store.RecurringLine, 0, len(lines))
	for i, l := range lines {
		prefix := "lines[" + strconv.Itoa(i) + "]."
		if !accountNumberRe.MatchString(l.Account) {
			fields[prefix+"account"] = "must be 1 to 20 characters of A-Z, a-z, 0-9, '.', '_' or '-'"
		}
		if l.DebitCredit != domain.SideDebit && l.DebitCredit != domain.SideCredit {
			fields[prefix+"debitCredit"] = "must be D or C"
		}
		if l.AmountKind != domain.AmountFixed && l.AmountKind != domain.AmountPercent {
			fields[prefix+"amountKind"] = "must be fixed or percent"
		}
		currencyOK := currencyRe.MatchString(l.Amount.Currency)
		if !currencyOK {
			fields[prefix+"amount.currency"] = "must be an ISO 4217 alpha-3 code"
		}
		amount, err := decimal.NewFromString(l.Amount.Value)
		switch {
		case err != nil || amount.IsNegative():
			fields[prefix+"amount.value"] = "must be a non-negative decimal string"
		case l.AmountKind == domain.AmountPercent:
			if amount.GreaterThan(decimal.NewFromInt(1000)) || !amount.Round(6).Equal(amount) {
				fields[prefix+"amount.value"] = "a percentage must be at most 1000 with at most 6 decimals"
			}
		case currencyOK && !amount.Round(money.MinorUnits(l.Amount.Currency)).Equal(amount):
			fields[prefix+"amount.value"] = "has more decimals than the currency allows"
		case amount.GreaterThanOrEqual(maxBatchAmount):
			fields[prefix+"amount.value"] = "is too large"
		}
		for attr, value := range l.Dimensions {
			if !dimAttrRe.MatchString(attr) || value == "" || utf8.RuneCountInString(value) > 20 {
				fields[prefix+"dimensions"] = "attribute names are A-Z with digits or underscores; values are 1 to 20 characters"
			}
		}
		if utf8.RuneCountInString(l.Text) > maxTextLength {
			fields[prefix+"text"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
		}
		out = append(out, store.RecurringLine{Account: l.Account, Dimensions: l.Dimensions, DebitCredit: l.DebitCredit,
			AmountKind: l.AmountKind, Amount: amount.String(), Currency: l.Amount.Currency, Text: l.Text})
	}
	return out
}

// putRecurring creates (201) or replaces (200) a recurring journal. Naturally
// idempotent; If-Match, when sent, must be the current version.
func (a *API) putRecurring(w http.ResponseWriter, r *http.Request) {
	id, version, ok := recurringTarget(w, r)
	if !ok {
		return
	}
	var req recurringRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.RecurringInput{LedgerID: req.LedgerID, JournalName: req.JournalName, Description: req.Description,
		Frequency: req.Frequency, DayOfMonth: req.DayOfMonth, Name: req.Name}
	if !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if !journalNameRe.MatchString(req.JournalName) {
		fields["journalName"] = "must be a journal name code (1 to 20 characters of A-Z, a-z, 0-9, underscore or dash)"
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > maxRecurringName {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", maxRecurringName)
	}
	if utf8.RuneCountInString(req.Description) > maxTextLength {
		fields["description"] = fmt.Sprintf("must be at most %d characters", maxTextLength)
	}
	if !domain.ValidFrequency(req.Frequency) {
		fields["frequency"] = "must be monthly, quarterly or yearly"
	}
	if req.DayOfMonth < 0 || req.DayOfMonth > 31 {
		fields["dayOfMonth"] = "must be 1 to 31, or 0 for the last day of the month"
	}
	if d, ok := parseDate(req.StartDate); ok {
		in.StartDate = d
	} else {
		fields["startDate"] = "must be a date as YYYY-MM-DD"
	}
	if req.EndDate != "" {
		if d, ok := parseDate(req.EndDate); !ok {
			fields["endDate"] = "must be a date as YYYY-MM-DD"
		} else if !in.StartDate.IsZero() && d.Before(in.StartDate) {
			fields["endDate"] = "must not be before startDate"
		} else {
			in.EndDate = &d
		}
	}
	if req.BaseAmount != "" {
		b, err := decimal.NewFromString(req.BaseAmount)
		if err != nil || b.IsNegative() || b.GreaterThanOrEqual(maxBatchAmount) || !b.Round(6).Equal(b) {
			fields["baseAmount"] = "must be a non-negative decimal string with at most 6 decimals"
		} else {
			s := b.String()
			in.BaseAmount = &s
		}
	}
	in.Lines = checkRecurringLines(req.Lines, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rj, created, err := a.store.PutRecurring(r.Context(), id, version, in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeRecurring(w, status, rj)
}

func (a *API) pauseRecurring(w http.ResponseWriter, r *http.Request) {
	a.setRecurringStatus(w, r, store.RecurringPaused)
}

func (a *API) resumeRecurring(w http.ResponseWriter, r *http.Request) {
	a.setRecurringStatus(w, r, store.RecurringActive)
}

func (a *API) setRecurringStatus(w http.ResponseWriter, r *http.Request, status string) {
	id, version, ok := recurringTarget(w, r)
	if !ok {
		return
	}
	rj, err := a.store.SetRecurringStatus(r.Context(), id, version, status, a.today())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeRecurring(w, http.StatusOK, rj)
}

// generateRecurring creates the draft batch of a scheduled date: 201 for a new
// run, 200 when that date already has one. The date is required, so a retried
// call can never generate the following period.
func (a *API) generateRecurring(w http.ResponseWriter, r *http.Request) {
	id, _, ok := recurringTarget(w, r)
	if !ok {
		return
	}
	var req generateRecurringRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := store.GenerateRecurringInput{ID: id}
	if d, ok := parseDate(req.ScheduledDate); ok {
		in.ScheduledDate = &d
	} else {
		fields["scheduledDate"] = "is required: a date of the schedule as YYYY-MM-DD, such as the next run date"
	}
	if req.BaseAmount != "" {
		b, err := decimal.NewFromString(req.BaseAmount)
		if err != nil || b.IsNegative() || b.GreaterThanOrEqual(maxBatchAmount) || !b.Round(6).Equal(b) {
			fields["baseAmount"] = "must be a non-negative decimal string with at most 6 decimals"
		} else {
			s := b.String()
			in.BaseAmount = &s
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	run, created, err := a.store.GenerateRecurring(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, run)
}

func (a *API) listRecurringRuns(w http.ResponseWriter, r *http.Request) {
	id, _, ok := recurringTarget(w, r)
	if !ok {
		return
	}
	runs, err := a.store.ListRecurringRuns(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": runs})
}
