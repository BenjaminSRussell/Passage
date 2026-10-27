package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	dutyConfirmation    = "BalanceConfirmationRun"
	maxReminderDays     = 60
	maxReplyComment     = 500
	defaultReminderDays = 14
)

// defaultConfirmThreshold is confirmation_threshold's default (§5.30.13).
var defaultConfirmThreshold = decimal.NewFromInt(10000)

// confirmationRoutes mounts balance confirmations (FR-30.20). Runs and replies
// need BalanceConfirmationRun; reads need ARView, which auditors hold.
func (a *API) confirmationRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyConfirmation)).Post("/balance-confirmations", a.createConfirmationRun)
	r.With(authz.RequireDuty(dutyView)).Get("/balance-confirmations", a.listConfirmationRuns)
	r.With(authz.RequireDuty(dutyView)).Get("/balance-confirmations/{ref}", a.getConfirmationRun)
	r.With(authz.RequireDuty(dutyView)).Get("/balance-confirmations/{ref}/differences", a.confirmationDifferences)
	// :reply is a custom method on the run; confirmationAction checks the duty.
	r.Post("/balance-confirmations/{ref}", a.confirmationAction)
}

type createConfirmationRunRequest struct {
	LegalEntityID string           `json:"legalEntityId"`
	AsOfDate      string           `json:"asOfDate"`
	Threshold     *decimal.Decimal `json:"threshold"`
	SampleSize    *int             `json:"sampleSize"`
	IncludeZero   bool             `json:"includeZeroBalances"`
	ReminderDays  *int             `json:"reminderDays"`
}

// createConfirmationRun handles POST /balance-confirmations.
func (a *API) createConfirmationRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req createConfirmationRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if _, err := store.ParseDay(req.AsOfDate); err != nil {
		fields["asOfDate"] = "must be a YYYY-MM-DD date"
	}
	threshold := defaultConfirmThreshold
	if req.Threshold != nil {
		threshold = *req.Threshold
	}
	if threshold.IsNegative() {
		fields["threshold"] = "must not be negative"
	}
	if req.SampleSize != nil && (*req.SampleSize < 1 || *req.SampleSize > store.MaxPageSize) {
		fields["sampleSize"] = fmt.Sprintf("must be 1 to %d when set", store.MaxPageSize)
	}
	reminder := defaultReminderDays
	if req.ReminderDays != nil {
		reminder = *req.ReminderDays
	}
	if reminder < 1 || reminder > maxReminderDays {
		fields["reminderDays"] = fmt.Sprintf("must be 1 to %d", maxReminderDays)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.CreateConfirmationRun(r.Context(), store.ConfirmationRun{LegalEntityID: req.LegalEntityID, AsOfDate: req.AsOfDate,
		Threshold: threshold, SampleSize: req.SampleSize, IncludeZero: req.IncludeZero, ReminderDays: reminder}, key)
	if err != nil {
		a.fail("create balance confirmation run", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// listConfirmationRuns handles GET /balance-confirmations, newest first.
func (a *API) listConfirmationRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListConfirmationRuns(r.Context(), limit)
	if err != nil {
		a.fail("list balance confirmation runs", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func runID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeConfirmationNotFound, "balance confirmation run not found"))
		return "", false
	}
	return id, true
}

// getConfirmationRun handles GET /balance-confirmations/{id}.
func (a *API) getConfirmationRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := runID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetConfirmationRun(r.Context(), id)
	if err != nil {
		a.fail("get balance confirmation run", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// confirmationDifferences handles GET /balance-confirmations/{id}/differences.
func (a *API) confirmationDifferences(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := runID(w, r)
	if !ok {
		return
	}
	items, err := a.store.ConfirmationDifferences(r.Context(), id)
	if err != nil {
		a.fail("balance confirmation differences", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type replyRequest struct {
	ConfirmationID  string           `json:"confirmationId"`
	Reply           string           `json:"reply"`
	CustomerBalance *decimal.Decimal `json:"customerBalance"`
	Comment         *string          `json:"comment"`
}

// confirmationAction handles POST /balance-confirmations/{id}:reply. A reply is
// immutable once recorded; repeating the same reply returns it.
func (a *API) confirmationAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || action != "reply" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeConfirmationNotFound, "unknown balance confirmation run or action"))
		return
	}
	if !require(w, r, dutyConfirmation) {
		return
	}
	var req replyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.ConfirmationID) {
		fields["confirmationId"] = "must be a UUID"
	}
	if !domain.ValidReply(req.Reply) {
		fields["reply"] = "must be AGREED or DISAGREED"
	}
	if req.Reply == domain.ReplyDisagreed && req.CustomerBalance == nil {
		fields["customerBalance"] = "is required when the reply is DISAGREED"
	}
	if req.Comment != nil && len(*req.Comment) > maxReplyComment {
		fields["comment"] = fmt.Sprintf("must be at most %d characters", maxReplyComment)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	out, err := a.store.RecordConfirmationReply(r.Context(), id, store.ConfirmationReply{ConfirmationID: req.ConfirmationID,
		Reply: req.Reply, CustomerBalance: req.CustomerBalance, Comment: req.Comment})
	if err != nil {
		a.fail("record balance confirmation reply", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
