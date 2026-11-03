package api

import (
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type ebomLineBody struct {
	FindNumber int    `json:"findNumber"`
	PartNumber string `json:"partNumber"`
	Quantity   string `json:"quantity"`
	Unit       string `json:"unit,omitempty"`
}

type ebomImportBody struct {
	ID           string              `json:"id,omitempty"`
	Source       string              `json:"source"`
	PartNumber   string              `json:"partNumber"`
	Revision     string              `json:"revision"`
	Status       string              `json:"status,omitempty"`
	BOMVersionID string              `json:"bomVersionId,omitempty"`
	EcrID        string              `json:"ecrId,omitempty"`
	Lines        []ebomLineBody      `json:"lines"`
	Exceptions   []ebomExceptionBody `json:"exceptions,omitempty"`
	Added        int                 `json:"added"`
	Removed      int                 `json:"removed"`
	Changed      int                 `json:"changed"`
}

type ebomExceptionBody struct {
	PartNumber string `json:"partNumber"`
	Code       string `json:"code"`
}

func bodyFromEbom(rec store.EbomImportRecord) ebomImportBody {
	body := ebomImportBody{ID: rec.ID, Source: rec.Source, PartNumber: rec.PartNumber, Revision: rec.Revision, Status: rec.Status, BOMVersionID: rec.BOMVersionID, EcrID: rec.EcrID, Added: rec.Comparison.Added, Removed: rec.Comparison.Removed, Changed: rec.Comparison.Changed, Lines: make([]ebomLineBody, len(rec.Payload.Lines)), Exceptions: make([]ebomExceptionBody, len(rec.Exceptions))}
	for i, l := range rec.Payload.Lines {
		body.Lines[i] = ebomLineBody{FindNumber: l.FindNumber, PartNumber: l.PartNumber, Quantity: l.Quantity.String(), Unit: l.Unit}
	}
	for i, e := range rec.Exceptions {
		body.Exceptions[i] = ebomExceptionBody{PartNumber: e.PartNumber, Code: e.Code}
	}
	return body
}

func (a *API) importEbom(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body ebomImportBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	validID("source", body.Source, fields)
	validID("partNumber", body.PartNumber, fields)
	validID("revision", body.Revision, fields)
	payload := domain.EbomPayload{PartNumber: body.PartNumber, Revision: body.Revision}
	for _, l := range body.Lines {
		if l.PartNumber == "" {
			fields["lines"] = "each line needs a partNumber"
			break
		}
		qty := decimal.NewFromInt(1)
		if l.Quantity != "" {
			parsed, err := decimal.NewFromString(l.Quantity)
			if err != nil {
				fields["lines"] = "quantity must be a decimal string"
				break
			}
			qty = parsed
		}
		payload.Lines = append(payload.Lines, domain.EbomLine{FindNumber: l.FindNumber, PartNumber: l.PartNumber, Quantity: qty, Unit: l.Unit})
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	rec, err := a.store.ImportEbom(r.Context(), store.EbomImportInput{Source: body.Source, Payload: payload, Actor: p.UserID}, idemOf(r, body))
	if err != nil {
		a.fail(w, r, "import ebom", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusCreated, bodyFromEbom(rec))
}

func (a *API) listEbomImports(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	listPage(a, w, r, "list ebom imports", &a.bomFailed, a.store.ListEbomImports, bodyFromEbom)
}

func (a *API) getEbomImport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rec, err := a.store.GetEbomImport(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get ebom import", err, &a.bomFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromEbom(rec))
}
