package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// dutyOpeningImport is held by the migration loader's service token (integration
// handoff H5). It opens only POST /open-items:batch, inside the token's own legal entity.
const dutyOpeningImport = "receivables.import.write"

type openingRowRequest struct {
	LegacyKey          string          `json:"legacyKey"`
	DocumentNumber     string          `json:"documentNumber"`
	CustomerID         string          `json:"customerId"`
	Kind               string          `json:"kind"`
	Side               string          `json:"side"`
	DocumentDate       string          `json:"documentDate"`
	DueDate            string          `json:"dueDate"`
	Currency           string          `json:"currency"`
	OriginalAmount     decimal.Decimal `json:"originalAmount"`
	OpenAmount         decimal.Decimal `json:"openAmount"`
	CashDiscountDate   *string         `json:"cashDiscountDate"`
	CashDiscountAmount decimal.Decimal `json:"cashDiscountAmount"`
}

type openingBatchRequest struct {
	LegalEntityID string              `json:"legalEntityId"`
	ImportSource  string              `json:"importSource"`
	Atomic        bool                `json:"atomic"`
	Items         []openingRowRequest `json:"items"`
}

// importOpeningItems handles POST /open-items:batch: load a cutover snapshot of a
// legal entity's open items from a legacy system. Results are per row; with
// atomic=true one refused row refuses the batch (422, nothing stored).
func (a *API) importOpeningItems(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req openingBatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !domain.ImportSourceName.MatchString(req.ImportSource) || len(req.ImportSource) > domain.MaxImportSource {
		fields["importSource"] = fmt.Sprintf("must be 1 to %d letters, digits, dots, dashes or underscores naming the legacy system", domain.MaxImportSource)
	}
	if n := len(req.Items); n == 0 || n > domain.MaxOpeningRows {
		fields["items"] = fmt.Sprintf("must hold 1 to %d items; send larger snapshots in several batches", domain.MaxOpeningRows)
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
	in := store.OpeningImport{LegalEntityID: req.LegalEntityID, ImportSource: req.ImportSource, Atomic: req.Atomic,
		Items: make([]store.OpeningItemInput, 0, len(req.Items))}
	seen := map[string]int{}
	for i, it := range req.Items {
		row := domain.OpeningRow{LegacyKey: it.LegacyKey, DocumentNumber: it.DocumentNumber, CustomerID: it.CustomerID, Kind: it.Kind,
			Side: it.Side, DocumentDate: it.DocumentDate, DueDate: it.DueDate, Currency: it.Currency, OriginalAmount: it.OriginalAmount,
			OpenAmount: it.OpenAmount, CashDiscountDate: it.CashDiscountDate, CashDiscountAmount: it.CashDiscountAmount}
		input := store.OpeningItemInput{Row: row}
		if bad := domain.ValidateOpeningRow(row); len(bad) > 0 {
			input.RejectCode, input.RejectDetail = domain.CodeOpeningInvalid, describeFields(bad)
		} else if first, dup := seen[row.LegacyKey]; dup {
			input.RejectCode = domain.CodeOpeningDuplicateKey
			input.RejectDetail = fmt.Sprintf("legacy key %s already appears at items[%d] of this batch", row.LegacyKey, first)
		} else {
			seen[row.LegacyKey] = i
		}
		in.Items = append(in.Items, input)
	}
	out, err := a.store.ImportOpeningItems(r.Context(), in)
	if err != nil {
		a.fail("import opening items", w, r, err)
		return
	}
	if !out.Committed {
		httpx.JSON(w, http.StatusUnprocessableEntity, out)
		return
	}
	status := http.StatusOK
	if out.Created > 0 && out.Updated == 0 && out.Unchanged == 0 && out.Rejected == 0 {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

// describeFields renders a field map as one stable sentence.
func describeFields(f map[string]string) string {
	order := []string{"legacyKey", "documentNumber", "customerId", "kind", "side", "documentDate", "dueDate", "currency",
		"originalAmount", "openAmount", "cashDiscountDate", "cashDiscountAmount"}
	parts := make([]string, 0, len(f))
	for _, k := range order {
		if msg, ok := f[k]; ok {
			parts = append(parts, k+" "+msg)
		}
	}
	return strings.Join(parts, "; ")
}

// listOpeningImports handles GET /open-item-imports: what each import source loaded.
func (a *API) listOpeningImports(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListOpeningImports(r.Context(), limit)
	if err != nil {
		a.fail("list opening imports", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
