package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// maxAdviceBytes bounds one remittance advice upload, below httpx.Decode's body cap like a lockbox file.
const maxAdviceBytes = maxLockboxBytes

func (a *API) adviceRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyPayment)).Post("/remittance-advices", a.importAdvice)
}

type adviceRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Format        string `json:"format"`
	Content       string `json:"content"`
	AutoApply     bool   `json:"autoApply"`
}

type adviceResponse struct {
	store.AdviceResult
	AutoApply *store.AutoApplyResult `json:"autoApply,omitempty"`
}

// importAdvice handles POST /remittance-advices (FR-30.4, 30-4): parse a CSV, JSON or EDI 820 remittance advice (400
// AR-ADVICE-MALFORMED names the row or segment), attach each payment's references to the payment it names, and optionally
// auto-apply the payments that got one. The advice is the payer's own list of the invoices a payment pays, so it
// feeds the matcher's document-number rule (R1).
func (a *API) importAdvice(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req adviceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !domain.ValidAdviceFormat(req.Format) {
		fields["format"] = "must be CSV, JSON or EDI820"
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > maxAdviceBytes {
		fields["content"] = fmt.Sprintf("must be a non-empty advice of at most %d bytes", maxAdviceBytes)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	lines, err := domain.ParseAdvice(req.Format, req.Content)
	if err != nil {
		var ae *domain.AdviceError
		if errors.As(err, &ae) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeAdviceMalformed, ae.Error()))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.ApplyRemittanceAdvice(r.Context(), req.LegalEntityID, lines)
	if err != nil {
		a.fail("apply remittance advice", w, r, err)
		return
	}
	resp := adviceResponse{AdviceResult: out}
	if req.AutoApply {
		applied, err := a.store.AutoApplyPayments(r.Context(), out.PaymentIDs)
		if err != nil {
			a.fail("auto-apply after advice", w, r, err)
			return
		}
		a.applied.Add(int64(applied.Applied))
		resp.AutoApply = &applied
	}
	httpx.JSON(w, http.StatusOK, resp)
}

type lockboxImportResponse struct {
	store.LockboxBatch
	AutoApply *store.AutoApplyResult `json:"autoApply,omitempty"`
}

type statementImportResponse struct {
	store.BankStatementBatch
	AutoApply *store.AutoApplyResult `json:"autoApply,omitempty"`
}

// autoApplyImported matches and applies the payments a file import created, in file order, when the caller asked for it.
// It answers nil, true when nothing was asked, and false after writing the failure.
func (a *API) autoApplyImported(w http.ResponseWriter, r *http.Request, want bool, payments []store.Payment) (*store.AutoApplyResult, bool) {
	if !want {
		return nil, true
	}
	paymentIDs := make([]string, len(payments))
	for i, p := range payments {
		paymentIDs[i] = p.ID
	}
	res, err := a.store.AutoApplyPayments(r.Context(), paymentIDs)
	if err != nil {
		a.fail("auto-apply imported payments", w, r, err)
		return nil, false
	}
	a.applied.Add(int64(res.Applied))
	return &res, true
}
