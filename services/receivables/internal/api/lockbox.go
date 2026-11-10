package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// lockboxImportRequest: TypeCodes are the bank profile's check credit codes (empty
// uses the defaults); Content is the BAI2 file as received from the bank.
type lockboxImportRequest struct {
	LegalEntityID string   `json:"legalEntityId"`
	ReceivedDate  string   `json:"receivedDate"`
	TypeCodes     []string `json:"typeCodes"`
	Content       string   `json:"content"`
	// AutoApply matches each imported payment and applies the ones the matcher is sure of (needs receivables.payment.post).
	AutoApply bool `json:"autoApply"`
}

// importLockbox handles POST /lockbox/imports: parse a BAI2 file (AR-0004 on a
// malformed record), refuse a duplicate file by SHA-256 (AR-0003), and create one
// unidentified CHECK payment per check credit (FR-30.3).
func (a *API) importLockbox(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req lockboxImportRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if _, err := store.ParseDay(req.ReceivedDate); err != nil {
		fields["receivedDate"] = "must be a YYYY-MM-DD date"
	}
	if strings.TrimSpace(req.Content) == "" || len(req.Content) > maxLockboxBytes {
		fields["content"] = fmt.Sprintf("must be a non-empty BAI2 file of at most %d bytes", maxLockboxBytes)
	}
	for i, c := range req.TypeCodes {
		if !domain.CreditTypeCode(c) {
			fields[fmt.Sprintf("typeCodes[%d]", i)] = "must be a three-digit BAI2 credit type code (100 to 399)"
		}
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	if req.AutoApply && !require(w, r, dutyPayment) {
		return
	}

	file, err := domain.ParseBAI2(req.Content)
	if err != nil {
		var pe *domain.ParseError
		if errors.As(err, &pe) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeBAI2Malformed, pe.Error()))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	codes := req.TypeCodes
	if len(codes) == 0 {
		codes = domain.DefaultLockboxTypeCodes
	}
	sum := sha256.Sum256([]byte(req.Content))
	out, err := a.store.ImportLockbox(r.Context(), store.LockboxImport{
		LegalEntityID: req.LegalEntityID,
		FileSHA256:    hex.EncodeToString(sum[:]),
		ReceivedDate:  req.ReceivedDate,
		AccountCount:  len(file.Accounts),
		Checks:        file.Checks(codes),
	})
	if err != nil {
		a.fail("import lockbox", w, r, err)
		return
	}
	a.received.Add(int64(len(out.Payments)))
	applied, ok := a.autoApplyImported(w, r, req.AutoApply, out.Payments)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusCreated, lockboxImportResponse{LockboxBatch: out, AutoApply: applied})
}

// listLockboxBatches handles GET /lockbox/imports: imported files, newest first.
func (a *API) listLockboxBatches(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListLockboxBatches(r.Context(), limit)
	if err != nil {
		a.fail("list lockbox imports", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
