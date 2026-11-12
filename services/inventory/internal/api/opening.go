package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Opening stock import (#49, H-S9-1). The migration factory sends a cutover snapshot of a legal entity's stock as
// batches of one currency; each batch becomes one OPENING_BALANCE journal that the import creates, approves and
// posts in one call, so the opening journal is a ledger document like any other (the posting profile's opening
// account is its offset). A batch key makes a resend a replay; the same key with other content is refused.
const (
	dutyOpeningImport     = "inventory.import.write"
	dutyOpeningImportRead = "inventory.import.read"
	openingBatchesPath    = "/v1/inventory/opening-batches"
	maxOpeningItems       = 1000
	maxOpeningBatchPage   = 200

	codeOpeningInvalid = "INV-OPB-001"
	// codeOpeningReferenceHeld is a reference another batch of the legal entity already opened.
	codeOpeningReferenceHeld = "INV-OPB-005"
	openingKeyPrefix         = "opening-batch:"
)

// OpeningBatches is what the opening import needs of the store.
type OpeningBatches interface {
	FindOpeningBatch(ctx context.Context, key string) (store.OpeningBatch, bool, error)
	RecordOpeningBatch(ctx context.Context, b store.OpeningBatch) error
	ReserveOpeningReferences(ctx context.Context, legalEntityID, batchKey string, refs []string) (map[string]string, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, afterKey string, limit int) ([]store.OpeningBatch, error)
}

// WithOpeningBatches enables the opening import routes; without it they answer 503.
func (a *API) WithOpeningBatches(o OpeningBatches) *API {
	a.opening = o
	return a
}

func (a *API) registerOpening(r chi.Router) {
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(openingBatchesPath, a.importOpeningBatch)
	r.With(authz.RequireDuty(dutyOpeningImportRead)).Get(openingBatchesPath, a.listOpeningBatches)
}

type openingItemRequest struct {
	Reference   string            `json:"reference"`
	ProductID   string            `json:"productId"`
	Dimensions  map[string]string `json:"dimensions"`
	EnteredUnit string            `json:"enteredUnit"`
	Qty         string            `json:"qty"`
	CostPrice   string            `json:"costPrice"`
}

type openingBatchRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	PostingDate   string               `json:"postingDate"`
	Currency      string               `json:"currency"`
	Items         []openingItemRequest `json:"items"`
}

type openingBatchResponse struct {
	JournalID     string `json:"journalId"`
	LegalEntityID string `json:"legalEntityId"`
	BatchKey      string `json:"batchKey"`
	Currency      string `json:"currency"`
	ItemCount     int    `json:"itemCount"`
	Qty           string `json:"qty"`
	Value         string `json:"value"`
}

func openingResponse(b store.OpeningBatch) openingBatchResponse {
	return openingBatchResponse{JournalID: b.JournalID, LegalEntityID: b.LegalEntityID, BatchKey: b.BatchKey, Currency: b.Currency,
		ItemCount: b.ItemCount, Qty: b.Qty.StringFixed(6), Value: b.Value.StringFixed(12)}
}

func (a *API) openingUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if a.opening != nil {
		return false
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "INV-OPB-003", "the opening stock import is not enabled on this deployment"))
	return true
}

// validateOpeningItems checks every row and returns the journal lines with the row-keyed errors
// (items[<1-based row>].<field>), so the caller can drop the named rows and send the rest again.
func validateOpeningItems(in []openingItemRequest) ([]domain.JournalLine, map[string]string) {
	errs := map[string]string{}
	lines := make([]domain.JournalLine, 0, len(in))
	seen := map[string]int{}
	for i, it := range in {
		prefix := fmt.Sprintf("items[%d].", i+1)
		ref := strings.TrimSpace(it.Reference)
		switch {
		case ref == "" || len(ref) > maxKeyLength:
			errs[prefix+"reference"] = fmt.Sprintf("is required (1 to %d characters)", maxKeyLength)
		case seen[ref] > 0:
			errs[prefix+"reference"] = fmt.Sprintf("appears twice in the batch (first at items[%d])", seen[ref])
		default:
			seen[ref] = i + 1
		}
		if !ids.Valid(it.ProductID) {
			errs[prefix+"productId"] = "must be a UUID"
		}
		qty, err := decimal.NewFromString(it.Qty)
		switch {
		case err != nil:
			errs[prefix+"qty"] = "must be a decimal number"
		case !qty.IsPositive():
			errs[prefix+"qty"] = "must be positive; opening stock is never zero or negative"
		case qty.Exponent() < -6:
			errs[prefix+"qty"] = "has more than six decimal places"
		}
		cost, err := decimal.NewFromString(it.CostPrice)
		switch {
		case err != nil:
			errs[prefix+"costPrice"] = "must be a decimal number"
		case cost.Sign() < 0:
			errs[prefix+"costPrice"] = "must not be negative"
		case cost.Exponent() < -6:
			errs[prefix+"costPrice"] = "has more than six decimal places"
		}
		if !unitCode.MatchString(it.EnteredUnit) {
			errs[prefix+"enteredUnit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
		}
		dims, derr := domain.Dimensions(it.Dimensions).Normalize()
		switch {
		case derr != nil:
			errs[prefix+"dimensions"] = derr.Error()
		case dims[domain.DimSite] == "" || dims[domain.DimWarehouse] == "":
			errs[prefix+"dimensions"] = "site and warehouse are required"
		}
		lines = append(lines, domain.JournalLine{ClientKey: ref, ProductID: it.ProductID, Dims: domain.Dimensions(it.Dimensions), Qty: qty,
			EnteredQty: qty, EnteredUnit: it.EnteredUnit, CostPrice: cost, LineNo: i + 1})
	}
	return lines, errs
}

// openingHash identifies the batch content: the legal entity, the date, the currency and the rows by reference.
func openingHash(req openingBatchRequest, lines []domain.JournalLine) string {
	rows := make([]string, len(lines))
	for i, l := range lines {
		d, _ := l.Dims.Normalize()
		rows[i] = strings.Join([]string{l.ClientKey, strings.ToLower(l.ProductID), d.Canonical(), l.EnteredUnit, l.Qty.String(), l.CostPrice.String()}, "\x1f")
	}
	sort.Strings(rows)
	h := sha256.New()
	for _, p := range append([]string{strings.ToLower(req.LegalEntityID), req.PostingDate, req.Currency}, rows...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// importOpeningBatch handles POST /v1/inventory/opening-batches.
func (a *API) importOpeningBatch(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || a.openingUnavailable(w, r) {
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
	if k := req.BatchKey; k == "" || len(k) > 200 {
		fields["batchKey"] = "is required (1 to 200 characters)"
	}
	date, err := time.Parse(time.DateOnly, req.PostingDate)
	if err != nil {
		fields["postingDate"] = "must be a date (YYYY-MM-DD)"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if n := len(req.Items); n == 0 || n > maxOpeningItems {
		fields["items"] = fmt.Sprintf("must hold 1 to %d items; send larger snapshots in several batches", maxOpeningItems)
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeOutOfScope, "the legal entity is outside your scope"))
		return
	}
	lines, rowErrs := validateOpeningItems(req.Items)
	if len(rowErrs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, codeOpeningInvalid, "one or more rows cannot be opened as stock")
		p.Errors = rowErrs
		httpx.Fail(w, r, p)
		return
	}
	hash := openingHash(req, lines)
	ctx := r.Context()
	if held, ok, err := a.opening.FindOpeningBatch(ctx, req.BatchKey); err != nil {
		a.failed(w, r, "find opening batch", err)
		return
	} else if ok {
		if held.ContentHash != hash {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeOpeningKeyConflict, "the batch key was already used with other content"))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, openingResponse(held))
		return
	}
	// A reference opens stock once per legal entity across batches: reserve them under the key before the journal exists, so two
	// batches holding the same line can never both post it. The refused rows are named like any other bad row.
	refs := make([]string, len(lines))
	for i, l := range lines {
		refs[i] = l.ClientKey
	}
	held, err := a.opening.ReserveOpeningReferences(ctx, req.LegalEntityID, req.BatchKey, refs)
	if err != nil {
		a.failed(w, r, "reserve opening references", err)
		return
	}
	if len(held) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, codeOpeningReferenceHeld, "one or more rows were already opened by another batch of the legal entity")
		p.Errors = map[string]string{}
		for i, ref := range refs {
			if key, ok := held[ref]; ok {
				p.Errors[fmt.Sprintf("items[%d].reference", i+1)] = "was already opened by batch " + key
			}
		}
		httpx.Fail(w, r, p)
		return
	}
	j, inserted, err := a.store.CreateJournal(ctx, domain.Journal{LegalEntityID: req.LegalEntityID, Type: domain.JournalOpeningBalance, PostingDate: date, Currency: req.Currency},
		openingKeyPrefix+req.BatchKey)
	if err != nil {
		a.failed(w, r, "create opening journal", err)
		return
	}
	// A journal that was there before must hold exactly the lines of this request (or none yet): whatever else sits under the key
	// is not this batch and is never posted or recorded as it.
	if !inserted && !sameOpeningLines(j.Lines, lines) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeOpeningKeyConflict, "the batch key was already used with other content"))
		return
	}
	// Each step is skipped when the journal is already past it, so a call that stopped half way is finished by the resend.
	if j.Status == domain.JournalDraft {
		if j, err = a.store.AddJournalLines(ctx, j.ID, lines); err == nil {
			j, err = a.store.TransitionJournal(ctx, j.ID, j.Version, domain.JournalSubmitted)
		}
		if err != nil {
			a.openingLost(w, r, req.BatchKey, hash, err)
			return
		}
	}
	if j.Status == domain.JournalSubmitted {
		if j, err = a.store.TransitionJournal(ctx, j.ID, j.Version, domain.JournalApproved); err != nil {
			a.openingLost(w, r, req.BatchKey, hash, err)
			return
		}
	}
	if j.Status == domain.JournalApproved {
		if j, err = a.store.PostJournal(ctx, j.ID, j.Version, a.post); err != nil {
			a.openingLost(w, r, req.BatchKey, hash, err)
			return
		}
		a.posted.Add(1)
	}
	if j.Status != domain.JournalPosted {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "INV-OPB-004", "the opening journal of this batch is "+string(j.Status)+" and cannot be posted"))
		return
	}
	// The totals are those of the posted journal, not of the request.
	b := store.OpeningBatch{LegalEntityID: req.LegalEntityID, BatchKey: req.BatchKey, ContentHash: hash, JournalID: j.ID, Currency: j.Currency, ItemCount: len(j.Lines)}
	for _, l := range j.Lines {
		b.Qty = b.Qty.Add(l.Qty)
		b.Value = b.Value.Add(l.Qty.Mul(l.CostPrice))
	}
	if err := a.opening.RecordOpeningBatch(ctx, b); err != nil {
		a.failed(w, r, "record opening batch", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, openingResponse(b))
}

// sameOpeningLines reports whether a stored journal holds exactly the request's lines: the same count, client keys and content.
func sameOpeningLines(held, want []domain.JournalLine) bool {
	if len(held) == 0 {
		return true
	}
	if len(held) != len(want) {
		return false
	}
	byKey := make(map[string]domain.JournalLine, len(held))
	for _, l := range held {
		byKey[l.ClientKey] = l
	}
	for _, w := range want {
		h, ok := byKey[w.ClientKey]
		if !ok || !h.SameContent(w) {
			return false
		}
	}
	return true
}

// openingLost answers a step that failed: when a concurrent call with the same content has recorded the batch meanwhile, this call
// is a replay of it; otherwise the failure stands.
func (a *API) openingLost(w http.ResponseWriter, r *http.Request, key, hash string, cause error) {
	if held, ok, err := a.opening.FindOpeningBatch(r.Context(), key); err == nil && ok && held.ContentHash == hash {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, openingResponse(held))
		return
	}
	a.failed(w, r, "import opening batch", cause)
}

// listOpeningBatches handles GET /v1/inventory/opening-batches?legal_entity=&cursor=&limit=: the batches the import
// stored, in key order, which is what the factory reconciles its load against.
func (a *API) listOpeningBatches(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || a.openingUnavailable(w, r) {
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
		if err != nil || n < 1 || n > maxOpeningBatchPage {
			fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", maxOpeningBatchPage)
		}
		limit = n
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeOutOfScope, "the legal entity is outside your scope"))
		return
	}
	rows, err := a.opening.ListOpeningBatches(r.Context(), le, q.Get("cursor"), limit+1)
	if err != nil {
		a.failed(w, r, "list opening batches", err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = rows[limit-1].BatchKey
	}
	items := make([]openingBatchResponse, len(rows))
	for i, b := range rows {
		items[i] = openingResponse(b)
	}
	body["items"] = items
	httpx.JSON(w, http.StatusOK, body)
}
