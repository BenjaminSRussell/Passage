package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	maxPayeeName    = 140
	maxPayeeAddress = 300
	maxPayeeTaxID   = 40
	maxPayeeEmail   = 254
)

type payeeRequest struct {
	LegalEntityID string            `json:"legalEntityId"`
	Name          string            `json:"name"`
	Address       string            `json:"address"`
	Country       string            `json:"country"`
	TaxID         string            `json:"taxId"`
	Email         string            `json:"email"`
	BankAccount   *domain.PayeeBank `json:"bankAccount"`
	SubjectRef    string            `json:"subjectRef"`
}

type payeeBody struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	Country       string `json:"country"`
	CreatedAt     string `json:"createdAt"`
}

func decodePayee(r *http.Request, v *payeeRequest) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body is not valid JSON for a one-time payee")
	}
	return nil
}

func validatePayee(req *payeeRequest) map[string]string {
	fields := map[string]string{}
	checkUUID(fields, "legalEntityId", req.LegalEntityID)
	req.Name, req.Address, req.TaxID, req.Email = strings.TrimSpace(req.Name), strings.TrimSpace(req.Address), strings.TrimSpace(req.TaxID), strings.TrimSpace(req.Email)
	checkLength(fields, "name", req.Name, 1, maxPayeeName)
	checkLength(fields, "address", req.Address, 1, maxPayeeAddress)
	req.SubjectRef = strings.TrimSpace(req.SubjectRef)
	if len(req.SubjectRef) > maxSubjectRef {
		fields["subjectRef"] = "must be at most 200 characters"
	}
	if len(req.Country) != 2 || !isUpperAlpha(req.Country) {
		fields["country"] = "must be a 2-letter upper-case ISO 3166 code"
	}
	if req.TaxID != "" {
		checkLength(fields, "taxId", req.TaxID, 1, maxPayeeTaxID)
	}
	if req.Email != "" {
		checkLength(fields, "email", req.Email, 3, maxPayeeEmail)
		if !strings.Contains(req.Email, "@") {
			fields["email"] = "must be an email address"
		}
	}
	return fields
}

func (a *API) createOneTimePayee(w http.ResponseWriter, r *http.Request) {
	p, ok := principalWithUser(w, r)
	if !ok {
		return
	}
	var req payeeRequest
	if err := decodePayee(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if fields := validatePayee(&req); len(fields) > 0 {
		httpx.Fail(w, r, validationProblem(fields))
		return
	}
	if err := inScope(p, req.LegalEntityID); err != nil {
		writeError(w, r, err)
		return
	}
	in := store.OneTimePayee{
		ID: ids.New(), LegalEntityID: req.LegalEntityID, Country: req.Country, Name: req.Name, Address: req.Address,
		TaxID: req.TaxID, Email: req.Email, SubjectRef: req.SubjectRef, CreatedBy: p.UserID,
	}
	if req.BankAccount != nil {
		bank, err := req.BankAccount.Normalize()
		if err != nil {
			writeError(w, r, err)
			return
		}
		in.Bank = &bank
	}
	created, err := a.store.CreateOneTimePayee(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a.payeesCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, payeeBody{ID: created.ID, LegalEntityID: created.LegalEntityID, Country: created.Country, CreatedAt: created.CreatedAt.UTC().Format(time.RFC3339)})
}
