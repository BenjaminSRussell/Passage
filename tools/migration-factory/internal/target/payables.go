package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// The payables client (49-3): the AP lines of an open_items run go to POST /v1/payables/opening-batches and the
// stored batches are read back from GET /v1/payables/opening-batches. It signs as the factory like the receivables
// client does (same service, own audience): the batch only opening-item.import, the read only
// opening-item.import.read, so each token carries one duty of the grant.

const (
	audPayables       = "payables"
	dutyOpeningImport = "opening-item.import"
	dutyOpeningRead   = "opening-item.import.read"
	pathOpeningBatch  = "/v1/payables/opening-batches"
	batchPage         = 200
	// maxBatchPages bounds the read-back; a legal entity with more stored opening batches than this cannot be reconciled.
	maxBatchPages = 500
)

// Payables problem codes the factory acts on.
const (
	CodeOpeningInvalid     = "AP-OPB-001"
	CodeOpeningKeyConflict = "AP-OPB-002"
	CodeOpeningTotals      = "AP-OPB-003"
	CodeOpeningReference   = "AP-OPB-004"
)

// ErrRowsRefused means payables refused a batch because of rows in it (422 AP-OPB-001 or AP-OPB-004). Nothing was
// stored; the rows named in RowsRefused carry the reasons and the rest of the batch may be sent again.
var ErrRowsRefused = errors.New("payables refused rows of the batch")

// RowsRefused is the per-row answer of a refused batch: the 1-based row numbers payables named, with its reason.
type RowsRefused struct {
	Code string
	Rows map[int]string
}

func (e *RowsRefused) Error() string {
	return fmt.Sprintf("%s: %s: %d rows", ErrRowsRefused, e.Code, len(e.Rows))
}

func (e *RowsRefused) Unwrap() error { return ErrRowsRefused }

// Payables is the client.
type Payables struct{ c *Receivables }

// NewPayables builds the client. A zero Config is valid and reports Configured false.
func NewPayables(cfg Config) *Payables {
	c := NewReceivables(cfg)
	c.aud = audPayables
	return &Payables{c: c}
}

// Configured reports whether a load can be attempted: an address and a signing key.
func (p *Payables) Configured() bool { return p != nil && p.c.Configured() }

// Batch is a stored opening batch as payables reports it.
type Batch struct {
	ID          string           `json:"id"`
	BatchKey    string           `json:"batchKey"`
	ItemCount   int              `json:"itemCount"`
	TotalsMinor map[string]int64 `json:"totalsMinor"`
}

// BatchResult is payables' answer to an accepted batch.
type BatchResult struct {
	Batch
	// Replayed is true when payables already held the batch under the key with the same content.
	Replayed bool
}

type openingRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	ControlTotals map[string]int64     `json:"controlTotalsMinor"`
	Items         []domain.PayablesRow `json:"items"`
}

// LoadBatch sends one opening batch of 1 to 1000 items with its control total per currency. The same key with the
// same items is answered as replayed, so a retry or a later load of an unchanged snapshot is harmless. A refusal for
// rows (AP-OPB-001, AP-OPB-004) is a *RowsRefused; any other 4xx is ErrRefused (a defect, such as a changed batch
// under an old key, or control totals that do not add up).
func (p *Payables) LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey string, items []domain.PayablesRow) (BatchResult, error) {
	if !p.Configured() {
		return BatchResult{}, ErrNotConfigured
	}
	if len(items) == 0 || len(items) > domain.MaxPayablesBatchItems {
		return BatchResult{}, fmt.Errorf("a batch holds 1 to %d items, got %d", domain.MaxPayablesBatchItems, len(items))
	}
	totals := map[string]int64{}
	for _, it := range items {
		totals[it.Currency] += it.AmountMinor
	}
	payload, err := json.Marshal(openingRequest{LegalEntityID: legalEntityID, BatchKey: batchKey, ControlTotals: totals, Items: items})
	if err != nil {
		return BatchResult{}, err
	}
	status, body, err := p.c.do(ctx, http.MethodPost, pathOpeningBatch, tenantID, legalEntityID, dutyOpeningImport, batchKey, payload)
	if err != nil {
		return BatchResult{}, err
	}
	switch status {
	case http.StatusOK, http.StatusCreated:
		var b Batch
		if json.Unmarshal(body, &b) != nil || b.ID == "" || b.BatchKey != batchKey || b.ItemCount != len(items) {
			return BatchResult{}, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the answer does not describe the batch sent"}
		}
		for cur, want := range totals {
			if b.TotalsMinor[cur] != want {
				return BatchResult{}, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the stored totals differ from the batch sent in " + cur}
			}
		}
		return BatchResult{Batch: b, Replayed: status == http.StatusOK}, nil
	case http.StatusUnprocessableEntity:
		var pb problemBody
		_ = json.Unmarshal(body, &pb)
		if pb.Code == CodeOpeningInvalid || pb.Code == CodeOpeningReference {
			if rows := rowErrors(pb.Errors, len(items)); len(rows) > 0 {
				return BatchResult{}, &RowsRefused{Code: pb.Code, Rows: rows}
			}
		}
	}
	return BatchResult{}, answerError(status, body)
}

var rowKey = regexp.MustCompile(`^items\[(\d+)\]\.(.+)$`)

// rowErrors reads the errors of a refused batch keyed items[row].field into row numbers (1-based, within n) and one
// reason per row. An error not keyed by a row of the batch returns nothing: the refusal is not about rows.
func rowErrors(errs map[string]string, n int) map[int]string {
	out := map[int]string{}
	for k, v := range errs {
		m := rowKey.FindStringSubmatch(k)
		if m == nil {
			return nil
		}
		row, _ := strconv.Atoi(m[1])
		if row < 1 || row > n {
			return nil
		}
		if prev, ok := out[row]; ok {
			v = prev + "; " + m[2] + ": " + v
		} else {
			v = m[2] + ": " + v
		}
		out[row] = v
	}
	return out
}

// BatchTotals reads every opening batch payables holds for the legal entity and sums the ones the factory wrote for
// the import source (IsPayablesBatchKey): the item count and the total per currency in minor units. The list has no
// key-prefix filter, so all of the entity's batches are paged through; more than maxBatchPages pages is an unreadable
// answer rather than a partial total.
func (p *Payables) BatchTotals(ctx context.Context, tenantID, legalEntityID, importSource string) (domain.PayablesTotals, error) {
	sum := domain.PayablesTotals{TotalMinor: map[string]int64{}}
	if !p.Configured() {
		return sum, ErrNotConfigured
	}
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxBatchPages {
			return sum, &Error{Kind: ErrBadAnswer, Detail: "payables lists more opening batches than the factory reads back; the totals cannot be read completely"}
		}
		q := url.Values{"legal_entity": {legalEntityID}, "limit": {strconv.Itoa(batchPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		status, body, err := p.c.do(ctx, http.MethodGet, pathOpeningBatch+"?"+q.Encode(), tenantID, legalEntityID, dutyOpeningRead, "", nil)
		if err != nil {
			return sum, err
		}
		if status != http.StatusOK {
			return sum, answerError(status, body)
		}
		var out struct {
			Items      []Batch `json:"items"`
			NextCursor string  `json:"nextCursor"`
		}
		if json.Unmarshal(body, &out) != nil {
			return sum, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the opening batches are not readable"}
		}
		for _, b := range out.Items {
			if !domain.IsPayablesBatchKey(importSource, b.BatchKey) {
				continue
			}
			sum.Items += b.ItemCount
			for cur, v := range b.TotalsMinor {
				sum.TotalMinor[cur] += v
			}
		}
		if out.NextCursor == "" {
			return sum, nil
		}
		cursor = out.NextCursor
	}
}
