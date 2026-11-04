package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/verify"
)

// DutyVerify is "Verify the reporting balances against the ledger" (FR-15.1).
// It is new in slice 10 and not seeded yet (NOT-IMPLEMENTED.md, authz seed).
const DutyVerify = "reporting.balance.verify"

// DutyLedgerBalanceRead is the ledger's own trial balance duty. The check reads
// the ledger as the finreport service, so the ledger never sees the end user's
// duties: finreport requires the user to hold it before it reads the ledger for
// them, and withholds stored ledger amounts from a user without it, so this
// route never shows ledger balances to someone the ledger would refuse.
const DutyLedgerBalanceRead = "ledger.balance.read"

// Verifier runs and serves balance verifications; *verify.Service implements it.
type Verifier interface {
	Run(ctx context.Context, req verify.Request) (store.Verification, error)
	List(ctx context.Context, ledgerID string, limit int) ([]store.VerificationSummary, error)
	Get(ctx context.Context, id string) (store.Verification, error)
}

// WithVerifier enables the balance verification routes; without it they answer 503.
func (a *API) WithVerifier(v Verifier) *API {
	a.verifier = v
	return a
}

type verifyRequest struct {
	LedgerID      string `json:"ledgerId"`
	LegalEntityID string `json:"legalEntityId"`
}

func (a *API) notConfigured(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, verify.CodeLedgerUnavailable,
		"balance verification is not configured: LEDGER_URL and the service signing key are not set"))
}

func (a *API) createVerification(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	if !p.HasDuty(DutyLedgerBalanceRead) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003",
			"missing duty ledger.balance.read: the check shows the ledger's trial balance"))
		return
	}
	if a.verifier == nil {
		a.notConfigured(w, r)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0003", "Idempotency-Key header is required"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var req verifyRequest
	if err := decodeStrict(raw, &req); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	if !ids.Valid(req.LedgerID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0002", "ledgerId must be a UUID"))
		return
	}
	if req.LegalEntityID != "" && !ids.Valid(req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0002", "legalEntityId must be a UUID"))
		return
	}
	sum := sha256.Sum256(raw)
	v, err := a.verifier.Run(r.Context(), verify.Request{LedgerID: req.LedgerID, LegalEntityID: req.LegalEntityID,
		RequestedBy: p.UserID, IdempotencyKey: key, RequestHash: hex.EncodeToString(sum[:])})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/reporting/balance-verifications/"+v.ID)
	status := http.StatusCreated
	if v.Replayed {
		status = http.StatusOK
	}
	httpx.JSON(w, status, v)
}

func (a *API) listVerifications(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	if a.verifier == nil {
		a.notConfigured(w, r)
		return
	}
	q := r.URL.Query()
	ledger := q.Get("ledgerId")
	if ledger != "" && !ids.Valid(ledger) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "ledgerId must be a UUID"))
		return
	}
	limit := store.MaxVerificationPage
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxVerificationPage {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", fmt.Sprintf("limit must be 1 to %d", store.MaxVerificationPage)))
			return
		}
		limit = n
	}
	items, err := a.verifier.List(r.Context(), ledger, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getVerification(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	if a.verifier == nil {
		a.notConfigured(w, r)
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeVerificationNotFound, "balance verification "+id+" does not exist"))
		return
	}
	v, err := a.verifier.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !p.HasDuty(DutyLedgerBalanceRead) && len(v.Diffs) > 0 {
		v.Diffs, v.DiffsWithheld = []store.VerificationDiff{}, true
	}
	httpx.JSON(w, http.StatusOK, v)
}
