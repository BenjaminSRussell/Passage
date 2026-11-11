package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

// Opening bank balances (#49, H-S10-1): the migration factory's cutover snapshot of each bank account's book balance.
const (
	dutyOpeningImport     = "treasury.import.write"
	dutyOpeningImportRead = "treasury.import.read"
	openingBalancesPath   = "/v1/treasury/opening-balances"
	maxOpeningItems       = 1000
	maxOpeningPage        = 200
	// maxJSONInteger is the largest integer a JSON number carries exactly.
	maxJSONInteger = int64(1) << 53
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// OpeningBalances is what the opening balance routes need of the store.
type OpeningBalances interface {
	ImportOpeningBalances(ctx context.Context, in store.OpeningImport) (store.OpeningBatch, bool, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, afterKey string, limit int) ([]store.OpeningBatch, error)
}

// WithOpeningBalances mounts the opening balance routes; without it they answer 503.
func (a *API) WithOpeningBalances(o OpeningBalances) *API {
	a.openings = o
	return a
}

func (a *API) registerOpening(r chi.Router) {
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(openingBalancesPath, a.importOpening)
	r.With(authz.RequireDuty(dutyOpeningImportRead)).Get(openingBalancesPath, a.listOpening)
}

type openingRequest struct {
	LegalEntityID string              `json:"legalEntityId"`
	BatchKey      string              `json:"batchKey"`
	AsOf          string              `json:"asOf"`
	Items         []store.OpeningItem `json:"items"`
}

func (a *API) openingReady(w http.ResponseWriter, r *http.Request) bool {
	if !validTenant(w, r) {
		return false
	}
	if a.openings == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "TRS-0605", "the opening balance import is not enabled in this deployment"))
		return false
	}
	return true
}

func (a *API) importOpening(w http.ResponseWriter, r *http.Request) {
	if !a.openingReady(w, r) {
		return
	}
	var req openingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if k := req.BatchKey; k == "" || len(k) > 200 {
		fields["batchKey"] = "is required (1 to 200 characters)"
	}
	if _, err := time.Parse(time.DateOnly, req.AsOf); err != nil {
		fields["asOf"] = "must be a date (YYYY-MM-DD)"
	}
	if n := len(req.Items); n == 0 || n > maxOpeningItems {
		fields["items"] = fmt.Sprintf("must hold 1 to %d items; send larger snapshots in several batches", maxOpeningItems)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	rowErrs := map[string]string{}
	seen := map[string]int{}
	for i, it := range req.Items {
		prefix := fmt.Sprintf("items[%d].", i+1)
		switch {
		case it.Reference == "" || len(it.Reference) > 255:
			rowErrs[prefix+"reference"] = "is required (1 to 255 characters)"
		case seen[it.Reference] > 0:
			rowErrs[prefix+"reference"] = fmt.Sprintf("appears twice in the batch (first at items[%d])", seen[it.Reference])
		default:
			seen[it.Reference] = i + 1
		}
		if !ids.Valid(it.BankAccountID) {
			rowErrs[prefix+"bankAccountId"] = "must be a UUID"
		}
		if !currencyPattern.MatchString(it.Currency) {
			rowErrs[prefix+"currency"] = "must be an ISO 4217 alpha-3 code"
		}
		if it.BalanceMinor > maxJSONInteger || it.BalanceMinor < -maxJSONInteger {
			rowErrs[prefix+"balanceMinor"] = "is larger than a JSON number carries exactly"
		}
	}
	if len(rowErrs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeOpeningInvalid, "one or more rows are not valid opening balances")
		p.Errors = rowErrs
		httpx.Fail(w, r, p)
		return
	}
	b, replayed, err := a.openings.ImportOpeningBalances(r.Context(), store.OpeningImport{LegalEntityID: req.LegalEntityID, BatchKey: req.BatchKey, AsOf: req.AsOf, Items: req.Items})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, b)
		return
	}
	httpx.JSON(w, http.StatusCreated, b)
}

func (a *API) listOpening(w http.ResponseWriter, r *http.Request) {
	if !a.openingReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	le := q.Get("legal_entity")
	if !ids.Valid(le) {
		fields["legal_entity"] = "must be a UUID"
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxOpeningPage {
			fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", maxOpeningPage)
		}
		limit = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	rows, err := a.openings.ListOpeningBatches(r.Context(), le, q.Get("cursor"), limit+1)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = rows[limit-1].BatchKey
	}
	body["items"] = rows
	httpx.JSON(w, http.StatusOK, body)
}
