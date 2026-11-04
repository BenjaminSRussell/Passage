package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

const maxResolveReason = 500

type attemptList struct {
	Items []store.Attempt `json:"items"`
}

// listActivationAttempts shows the pending activation attempts, flagging the ones
// past the ledger replay window that wait for an operator (productcost.activation-attempt.resolve).
func (a *API) listActivationAttempts(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	items, err := a.store.ListActivationAttempts(r.Context())
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, attemptList{Items: items})
}

type resolveAttemptRequest struct {
	Version              *int   `json:"version"`
	ReversesActivationID string `json:"reversesActivationId"`
	Outcome              string `json:"outcome"`
	JournalEntryID       string `json:"journalEntryId"`
	Reason               string `json:"reason"`
}

// resolveActivationAttempt records the operator's decision on an attempt past the
// ledger replay window: the entry did land (posted, naming it) or did not (not-posted).
func (a *API) resolveActivationAttempt(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	var req resolveAttemptRequest
	if !decode(w, r, &req) {
		return
	}
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	fields := map[string]string{}
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "the decision records the operator who made it; no user in the request"))
		return
	}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.ReversesActivationID != "" {
		if canonical, ok := ids.Canonical(req.ReversesActivationID); ok {
			req.ReversesActivationID = canonical
		} else {
			fields["reversesActivationId"] = "must be a UUID"
		}
	}
	switch req.Outcome {
	case store.OutcomePosted:
		if canonical, ok := ids.Canonical(req.JournalEntryID); ok {
			req.JournalEntryID = canonical
		} else {
			fields["journalEntryId"] = "is required for posted and must be the UUID of the ledger entry"
		}
	case store.OutcomeNotPosted:
		if req.JournalEntryID != "" {
			fields["journalEntryId"] = "must be empty for not-posted"
		}
	default:
		fields["outcome"] = "must be posted or not-posted"
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > maxResolveReason {
		fields["reason"] = "is required, at most 500 characters"
	}
	key := idempotencyKey(r, fields)
	if !validation(w, r, fields) {
		return
	}
	in := store.ResolveInput{VersionID: id, Expected: *req.Version, ReversesID: req.ReversesActivationID, Outcome: req.Outcome,
		JournalEntryID: req.JournalEntryID, Reason: req.Reason, Key: key, Fingerprint: struct {
			ID  string
			Req resolveAttemptRequest
		}{id, req}}
	res, replayed, err := a.store.ResolveActivationAttempt(r.Context(), in)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if replayed {
		w.Header().Set(headerReplayed, "true")
	} else if res.Activation != nil {
		a.activations.Add(1)
	}
	httpx.JSON(w, http.StatusOK, res)
}
