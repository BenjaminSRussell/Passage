package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Return receipts are the service-to-service command sales uses to restock an accepted RMA line (wave 30 AC3). The
// service duty holds only this route: one MOVEMENT journal of one receipt line, created, filled and posted in one call,
// keyed by the caller's per-line Idempotency-Key. It replaces granting sales the general journal duties.
const (
	dutyReturnReceipt      = "inventory.return-receipt.post"
	returnReceiptPath      = "/internal/v1/inventory/return-receipts"
	returnReceiptKeyPrefix = "return-receipt:"
	codeReturnReceiptState = "INV-RETURN-RECEIPT"
)

type returnReceiptRequest struct {
	LegalEntityID   string            `json:"legalEntityId"`
	PostingDate     string            `json:"postingDate"`
	Currency        string            `json:"currency"`
	OffsetAccountID string            `json:"offsetAccountId"`
	ProductID       string            `json:"productId"`
	Dimensions      map[string]string `json:"dimensions"`
	Qty             string            `json:"qty"`
	EnteredUnit     string            `json:"enteredUnit"`
	CostPrice       string            `json:"costPrice"`
}

func (a *API) registerReturnReceipts(r chi.Router) {
	r.With(authz.RequireDuty(dutyReturnReceipt)).Post(returnReceiptPath, a.postReturnReceipt)
}

// reservedJournalKey reports whether a journal Idempotency-Key lies in the return-receipt key space, which only
// postReturnReceipt may create journals under (so a journal user cannot squat an RMA line's key).
func reservedJournalKey(key string) bool {
	return strings.HasPrefix(key, returnReceiptKeyPrefix) || strings.HasPrefix(key, openingKeyPrefix)
}

// postReturnReceipt books one returned line into stock: a MOVEMENT journal under the journal key
// return-receipt:<Idempotency-Key> holding exactly one line whose client key is the Idempotency-Key, posted at once.
// A repeat finds the same journal and answers it (200) once it is POSTED; a journal under the key that holds other
// lines, or the line with other values, is refused, so a retry can never restock twice or trust a foreign journal.
func (a *API) postReturnReceipt(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req returnReceiptRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	fields := map[string]string{}
	if key == "" || len(key) > maxKeyLength-len(returnReceiptKeyPrefix) {
		fields[idempotencyHeader] = fmt.Sprintf("is required (1 to %d characters)", maxKeyLength-len(returnReceiptKeyPrefix))
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	date, err := time.Parse("2006-01-02", req.PostingDate)
	if err != nil {
		fields["postingDate"] = "must be a date (YYYY-MM-DD)"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	req.OffsetAccountID = strings.TrimSpace(req.OffsetAccountID)
	if !accountCode.MatchString(req.OffsetAccountID) {
		fields["offsetAccountId"] = "is required: an account id of 1 to 64 letters, digits, '.', '_' or '-'"
	}
	if strings.TrimSpace(req.Dimensions[domain.DimSite]) == "" || strings.TrimSpace(req.Dimensions[domain.DimWarehouse]) == "" {
		fields["dimensions"] = "site and warehouse are required"
	}
	lines, lineFields := parseLines([]lineRequest{{ClientKey: key, ProductID: req.ProductID, Dimensions: req.Dimensions, Qty: req.Qty,
		EnteredUnit: req.EnteredUnit, CostPrice: req.CostPrice}})
	for k, v := range lineFields {
		fields[strings.TrimPrefix(k, "lines[0].")] = v
	}
	if len(lineFields) == 0 && !lines[0].Qty.IsPositive() {
		fields["qty"] = "must be greater than 0"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	line := lines[0]
	ctx := r.Context()
	j, inserted, err := a.store.CreateJournal(ctx, domain.Journal{LegalEntityID: req.LegalEntityID, Type: domain.JournalMovement, PostingDate: date,
		OffsetAccountID: req.OffsetAccountID, Currency: req.Currency}, returnReceiptKeyPrefix+key)
	if err != nil {
		a.failed(w, r, "create return receipt", err)
		return
	}
	if err := receiptJournalHolds(j, line); err != nil {
		a.failed(w, r, "return receipt replay", err)
		return
	}
	if j.Status != domain.JournalPosted {
		if len(j.Lines) == 0 {
			if j, err = a.store.AddJournalLines(ctx, j.ID, []domain.JournalLine{line}); err != nil {
				a.failed(w, r, "add return receipt line", err)
				return
			}
		}
		posted, err := a.store.PostJournal(ctx, j.ID, j.Version, a.post)
		if err != nil {
			// A concurrent call with the same key may have posted it meanwhile; answer that journal if so.
			again, getErr := a.store.GetJournal(ctx, j.ID)
			if getErr != nil || again.Status != domain.JournalPosted || receiptJournalHolds(again, line) != nil {
				a.failed(w, r, "post return receipt", err)
				return
			}
			posted = again
		} else {
			a.posted.Add(1)
		}
		j = posted
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJournal(w, status, j)
}

// receiptJournalHolds checks that a journal found under a return-receipt key is that receipt: a MOVEMENT journal
// with no line but the receipt line, and a status a receipt can be in.
func receiptJournalHolds(j domain.Journal, line domain.JournalLine) error {
	if j.Type != domain.JournalMovement || j.ReversesJournalID != "" {
		return httpx.NewProblem(http.StatusConflict, codeReturnReceiptState, "the journal under this key is not a return receipt")
	}
	switch len(j.Lines) {
	case 0:
	case 1:
		if j.Lines[0].ClientKey != line.ClientKey {
			return httpx.NewProblem(http.StatusConflict, codeReturnReceiptState, "the journal under this key holds another line")
		}
		if !j.Lines[0].SameContent(line) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "Idempotency-Key was already used with a different receipt line")
		}
	default:
		return httpx.NewProblem(http.StatusConflict, codeReturnReceiptState, "the journal under this key holds more than the receipt line")
	}
	switch j.Status {
	case domain.JournalDraft, domain.JournalApproved, domain.JournalPosted:
		return nil
	default:
		return httpx.NewProblem(http.StatusConflict, codeReturnReceiptState, "the return receipt journal is "+string(j.Status)+"; it cannot be posted")
	}
}
