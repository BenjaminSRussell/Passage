package api

import (
	"crypto/sha256"
	"encoding/hex"
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

// maxStatementBytes bounds one camt.053 upload, below httpx.Decode's body cap like a lockbox file.
const maxStatementBytes = maxLockboxBytes

// bankStatementRoutes mounts the camt.053 import (FR-30.5): receivables.payment-file.import imports, receivables.view reads.
func (a *API) bankStatementRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyImport)).Post("/bank-statements/imports", a.importBankStatement)
	r.With(authz.RequireDuty(dutyView)).Get("/bank-statements/imports", a.listBankStatementImports)
}

// bankStatementImportRequest carries one ISO 20022 camt.053 document as received from the bank.
type bankStatementImportRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Content       string `json:"content"`
	// AutoApply matches each imported payment and applies the ones the matcher is sure of (needs receivables.payment.post).
	AutoApply bool `json:"autoApply"`
}

// importBankStatement handles POST /bank-statements/imports: parse a camt.053 file
// (AR-0004 with the line of a malformed entry), refuse a duplicate file by SHA-256
// (AR-0003), and create one unidentified CAMT053 payment per booked customer
// credit not imported before (FR-30.5, BR-30.7). Matching is the clerk's next
// step, through payments/{id}:match, as for lockbox payments.
func (a *API) importBankStatement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req bankStatementImportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > maxStatementBytes {
		fields["content"] = fmt.Sprintf("must be a non-empty camt.053 document of at most %d bytes", maxStatementBytes)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	if req.AutoApply && !require(w, r, dutyPayment) {
		return
	}
	stmt, err := domain.ParseCAMT053([]byte(req.Content))
	if err != nil {
		var ce *domain.CAMTError
		if errors.As(err, &ce) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeCAMTMalformed, ce.Error()))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	sum := sha256.Sum256([]byte(req.Content))
	out, err := a.store.ImportBankStatement(r.Context(), store.BankStatementImport{
		LegalEntityID:  req.LegalEntityID,
		FileSHA256:     hex.EncodeToString(sum[:]),
		StatementCount: stmt.Statements,
		SkippedCount:   stmt.Skipped,
		Credits:        stmt.Credits,
	})
	if err != nil {
		a.fail("import bank statement", w, r, err)
		return
	}
	a.received.Add(int64(out.PaymentCount))
	applied, ok := a.autoApplyImported(w, r, req.AutoApply, out.Payments)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusCreated, statementImportResponse{BankStatementBatch: out, AutoApply: applied})
}

// listBankStatementImports handles GET /bank-statements/imports: imported files, newest first.
func (a *API) listBankStatementImports(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListBankStatementImports(r.Context(), limit)
	if err != nil {
		a.fail("list bank statement imports", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
