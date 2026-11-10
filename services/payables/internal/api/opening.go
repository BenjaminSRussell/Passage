package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	dutyOpeningImport = "opening-item.import"
	dutyOpeningRead   = "opening-item.import.read"
	pathOpeningBatch  = "/v1/payables/opening-batches"

	CodeOpeningInvalid      = "AP-OPN-001"
	CodeOpeningKeyConflict  = "AP-OPN-002"
	CodeOpeningControlTotal = "AP-OPN-003"
	CodeOpeningReference    = "AP-OPN-004"

	maxOpeningRows   = 1000
	maxOpeningAmount = int64(1) << 53
)

var openingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

type OpeningStore interface {
	ImportOpeningBatch(ctx context.Context, in store.OpeningImport) (store.OpeningBatch, bool, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int) ([]store.OpeningBatch, error)
}

type openingDeps struct {
	store     OpeningStore
	imported  atomic.Int64
	replayed  atomic.Int64
	refused   atomic.Int64
	itemsLoad atomic.Int64
}

// WithOpeningItems mounts the cutover import of open vendor items; vendors are checked against the
// party vendor master given by WithVendorBanks.
func (a *API) WithOpeningItems(s OpeningStore) *API {
	a.opening = &openingDeps{store: s}
	return a
}

func (a *API) registerOpening(r chi.Router) {
	if a.opening == nil {
		return
	}
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(pathOpeningBatch, a.importOpeningBatch)
	r.With(authz.RequireDuty(dutyOpeningRead)).Get(pathOpeningBatch, a.listOpeningBatches)
}

type openingItemRequest struct {
	Reference     string `json:"reference"`
	VendorID      string `json:"vendorId"`
	InvoiceNumber string `json:"invoiceNumber"`
	InvoiceDate   string `json:"invoiceDate"`
	DueDate       string `json:"dueDate"`
	Currency      string `json:"currency"`
	AmountMinor   int64  `json:"amountMinor"`
}

type openingBatchRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	ControlTotals map[string]int64     `json:"controlTotalsMinor"`
	Items         []openingItemRequest `json:"items"`
}

func rowField(row int, name string) string { return "items[" + strconv.Itoa(row) + "]." + name }

// importOpeningBatch loads open vendor items at cutover for one legal entity, all or nothing. Rows are
// numbered from 1. Nothing is posted to the ledger: the GL opening journal carries the AP control balance.
func (a *API) importOpeningBatch(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	var req openingBatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	le, ok := a.resolveEntity(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !openingKeyPattern.MatchString(req.BatchKey) {
		fields["batchKey"] = "is required: 1 to 100 letters, digits, dots, dashes, colons or underscores"
	}
	if n := len(req.Items); n == 0 || n > maxOpeningRows {
		fields["items"] = fmt.Sprintf("must hold 1 to %d items; send larger snapshots in several batches", maxOpeningRows)
	}
	if len(req.ControlTotals) == 0 {
		fields["controlTotalsMinor"] = "is required: the batch total per currency in minor units"
	}
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	in, rowErrors := a.openingImport(le, caller.UserID, req)
	if len(rowErrors) == 0 {
		rowErrors = a.unknownVendors(w, r, le, req)
		if rowErrors == nil {
			return
		}
	}
	if len(rowErrors) > 0 {
		a.refuseOpening(w, r, http.StatusUnprocessableEntity, CodeOpeningInvalid, "the batch has rows that cannot be imported; nothing was stored", rowErrors)
		return
	}
	if mismatch := controlTotalMismatch(in.ControlTotals, in.Items); len(mismatch) > 0 {
		a.refuseOpening(w, r, http.StatusUnprocessableEntity, CodeOpeningControlTotal, "the items do not add up to the declared control totals; nothing was stored", mismatch)
		return
	}
	batch, created, err := a.opening.store.ImportOpeningBatch(r.Context(), in)
	var taken *store.OpeningReferenceError
	switch {
	case errors.Is(err, store.ErrOpeningKeyConflict):
		a.refuseOpening(w, r, http.StatusConflict, CodeOpeningKeyConflict, "batch key "+req.BatchKey+" was already imported with different content", nil)
	case errors.As(err, &taken):
		a.refuseOpening(w, r, http.StatusUnprocessableEntity, CodeOpeningReference, "item references were already imported by another batch; nothing was stored", takenRows(req.Items, taken.Taken))
	case err != nil:
		a.fault(w, r, "opening batch import failed", err)
	case created:
		a.opening.imported.Add(1)
		a.opening.itemsLoad.Add(int64(batch.ItemCount))
		slog.Info("opening batch imported", "legalEntityId", le, "batchKey", req.BatchKey, "items", batch.ItemCount, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusCreated, batch)
	default:
		a.opening.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, batch)
	}
}

func (a *API) refuseOpening(w http.ResponseWriter, r *http.Request, status int, code, detail string, fields map[string]string) {
	a.opening.refused.Add(1)
	slog.Warn("opening batch refused", "code", code, "rows", len(fields), "correlationId", middleware.GetReqID(r.Context()))
	a.countClientError(r)
	p := httpx.NewProblem(status, code, detail)
	p.Errors = fields
	fail(w, r, p)
}

func (a *API) openingImport(le, user string, req openingBatchRequest) (store.OpeningImport, map[string]string) {
	in := store.OpeningImport{LegalEntityID: le, BatchKey: req.BatchKey, ImportedBy: user, ControlTotals: req.ControlTotals, Items: make([]store.OpeningItem, 0, len(req.Items))}
	bad := map[string]string{}
	seen := map[string]int{}
	for cur, total := range req.ControlTotals {
		if !currencyPattern.MatchString(cur) || total <= 0 || total > maxOpeningAmount {
			bad["controlTotalsMinor."+cur] = "must be a positive amount in minor units for a 3-letter currency"
		}
	}
	for i, it := range req.Items {
		row := i + 1
		if it.Reference == "" || len(it.Reference) > 100 {
			bad[rowField(row, "reference")] = "is required, at most 100 characters"
		} else if first, dup := seen[it.Reference]; dup {
			bad[rowField(row, "reference")] = fmt.Sprintf("appears again; first at row %d", first)
		} else {
			seen[it.Reference] = row
		}
		vendor, vok := ids.Canonical(it.VendorID)
		if !vok {
			bad[rowField(row, "vendorId")] = "must be a UUID"
		}
		if it.InvoiceNumber == "" || len(it.InvoiceNumber) > 100 {
			bad[rowField(row, "invoiceNumber")] = "is required, at most 100 characters"
		}
		invoiceDate, dueDate := parseDay(bad, rowField(row, "invoiceDate"), it.InvoiceDate, true), parseDay(bad, rowField(row, "dueDate"), it.DueDate, true)
		if !invoiceDate.IsZero() && !dueDate.IsZero() && dueDate.Before(invoiceDate) {
			bad[rowField(row, "dueDate")] = "must not be before the invoice date"
		}
		if !currencyPattern.MatchString(it.Currency) {
			bad[rowField(row, "currency")] = "must be a 3-letter ISO 4217 code in capitals"
		}
		if it.AmountMinor <= 0 || it.AmountMinor > maxOpeningAmount {
			bad[rowField(row, "amountMinor")] = "must be a positive whole number of minor units"
		}
		in.Items = append(in.Items, store.OpeningItem{Reference: it.Reference, VendorID: vendor, InvoiceNumber: it.InvoiceNumber, InvoiceDate: it.InvoiceDate,
			DueDate: it.DueDate, Currency: it.Currency, AmountMinor: it.AmountMinor})
	}
	in.ContentHash = openingHash(in)
	return in, bad
}

func openingHash(in store.OpeningImport) string {
	items := slices.Clone(in.Items)
	slices.SortFunc(items, func(a, b store.OpeningItem) int {
		switch {
		case a.Reference < b.Reference:
			return -1
		case a.Reference > b.Reference:
			return 1
		}
		return 0
	})
	raw, _ := json.Marshal(struct {
		LegalEntityID string
		Totals        map[string]int64
		Items         []store.OpeningItem
	}{in.LegalEntityID, in.ControlTotals, items})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// unknownVendors checks each distinct vendor once against the party vendor master. A party fault answers
// the request itself and returns nil with the response already written.
func (a *API) unknownVendors(w http.ResponseWriter, r *http.Request, le string, req openingBatchRequest) map[string]string {
	if a.runs == nil || a.runs.banks == nil {
		a.problem(w, r, http.StatusServiceUnavailable, codeNotConfigured, "the party vendor master is not configured; vendors cannot be checked")
		return nil
	}
	known := map[string]bool{}
	bad := map[string]string{}
	for i, it := range req.Items {
		vendor, _ := ids.Canonical(it.VendorID)
		exists, checked := known[vendor]
		if !checked {
			_, err := a.runs.banks.BankAccounts(r.Context(), le, vendor)
			switch {
			case errors.Is(err, party.ErrVendorNotFound):
			case err != nil:
				a.partyUnavailable(w, r, err)
				return nil
			default:
				exists = true
			}
			known[vendor] = exists
		}
		if !exists {
			bad[rowField(i+1, "vendorId")] = "vendor " + vendor + " is not in the party vendor master"
		}
	}
	return bad
}

func controlTotalMismatch(declared map[string]int64, items []store.OpeningItem) map[string]string {
	actual := map[string]int64{}
	for _, it := range items {
		actual[it.Currency] += it.AmountMinor
	}
	bad := map[string]string{}
	for cur, want := range declared {
		if actual[cur] != want {
			bad["controlTotalsMinor."+cur] = fmt.Sprintf("declared %d but the items add up to %d", want, actual[cur])
		}
	}
	for cur, got := range actual {
		if _, ok := declared[cur]; !ok {
			bad["controlTotalsMinor."+cur] = fmt.Sprintf("no total declared; the items add up to %d", got)
		}
	}
	return bad
}

func takenRows(items []openingItemRequest, taken map[string]string) map[string]string {
	bad := map[string]string{}
	for i, it := range items {
		if key, ok := taken[it.Reference]; ok {
			bad[rowField(i+1, "reference")] = "already imported by batch " + key
		}
	}
	return bad
}

// listOpeningBatches returns the imported batches of a legal entity with item counts and totals read
// back from the stored items, so the loader can verify what landed.
func (a *API) listOpeningBatches(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	le, ok := a.resolveEntity(w, r, r.URL.Query().Get("legal_entity"))
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			a.invalid(w, r, map[string]string{"limit": "must be a whole number from 1 to 200"})
			return
		}
		limit = n
	}
	batches, err := a.opening.store.ListOpeningBatches(r.Context(), le, r.URL.Query().Get("batch_key"), limit)
	if err != nil {
		a.fault(w, r, "opening batch list failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": batches, "asOf": a.now().UTC().Format(time.RFC3339)})
}

func (a *API) writeOpeningMetrics(w http.ResponseWriter) {
	if a.opening == nil {
		return
	}
	fmt.Fprintf(w, "# HELP payables_opening_batches_total Opening-item batches by outcome.\n# TYPE payables_opening_batches_total counter\npayables_opening_batches_total{outcome=\"imported\"} %d\npayables_opening_batches_total{outcome=\"replayed\"} %d\npayables_opening_batches_total{outcome=\"refused\"} %d\n", a.opening.imported.Load(), a.opening.replayed.Load(), a.opening.refused.Load())
	fmt.Fprintf(w, "# HELP payables_opening_items_total Opening items stored by imported batches.\n# TYPE payables_opening_items_total counter\npayables_opening_items_total %d\n", a.opening.itemsLoad.Load())
}
