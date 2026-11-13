package target

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// The inventory client (H-S9-1) sends opening stock to POST /v1/inventory/opening-batches and reads the stored batches
// back from GET; the treasury client (H-S10-1) does the same with POST /v1/treasury/opening-balances. Both sign as the
// factory like the payables client does (own audience, one duty per token).

const (
	audInventory = "inventory"
	audTreasury  = "treasury"

	dutyInventoryImport     = "inventory.import.write"
	dutyInventoryImportRead = "inventory.import.read"
	dutyInventoryRead       = "inventory.read"
	pathInventoryOnHand     = "/v1/inventory/onhand"
	dutyTreasuryImport      = "treasury.import.write"
	dutyTreasuryImportRead  = "treasury.import.read"

	pathInventoryBatches = "/v1/inventory/opening-batches"
	pathTreasuryBatches  = "/v1/treasury/opening-balances"
	openingPage          = 200
	maxOpeningPages      = 500
)

// Problem codes of the two targets the factory acts on.
const (
	CodeInventoryRowsInvalid = "INV-OPB-001"
	// CodeInventoryRowsHeld names rows whose reference another batch of the legal entity already opened (a cross-batch duplicate).
	CodeInventoryRowsHeld   = "INV-OPB-005"
	CodeTreasuryRowsInvalid = "TRS-0601"
	CodeTreasuryRowsRefused = "TRS-0602"
)

// OpeningBatchResult is a target's answer to an accepted batch.
type OpeningBatchResult struct {
	ItemCount int
	// Replayed is true when the target already held the batch under the key with the same content.
	Replayed bool
}

// Inventory is the opening stock client.
type Inventory struct{ c *Receivables }

// NewInventory builds the client. A zero Config is valid and reports Configured false.
func NewInventory(cfg Config) *Inventory {
	c := NewReceivables(cfg)
	c.aud = audInventory
	return &Inventory{c: c}
}

// Configured reports whether a load can be attempted: an address and a signing key.
func (i *Inventory) Configured() bool { return i != nil && i.c.Configured() }

type stockRequest struct {
	LegalEntityID string             `json:"legalEntityId"`
	BatchKey      string             `json:"batchKey"`
	PostingDate   string             `json:"postingDate"`
	Currency      string             `json:"currency"`
	Items         []domain.StockItem `json:"items"`
}

type stockBatch struct {
	BatchKey  string `json:"batchKey"`
	Currency  string `json:"currency"`
	ItemCount int    `json:"itemCount"`
	Qty       string `json:"qty"`
	Value     string `json:"value"`
}

// LoadBatch sends one batch of 1 to 1000 items of one currency. A refusal for rows (INV-OPB-001, or INV-OPB-005 for a
// reference another batch opened) is a *RowsRefused; any
// other 4xx is ErrRefused.
func (i *Inventory) LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey, postingDate, currency string, items []domain.StockItem) (OpeningBatchResult, error) {
	if !i.Configured() {
		return OpeningBatchResult{}, ErrNotConfigured
	}
	if len(items) == 0 || len(items) > domain.MaxOpeningBatchItems {
		return OpeningBatchResult{}, fmt.Errorf("a batch holds 1 to %d items, got %d", domain.MaxOpeningBatchItems, len(items))
	}
	payload, err := json.Marshal(stockRequest{LegalEntityID: legalEntityID, BatchKey: batchKey, PostingDate: postingDate, Currency: currency, Items: items})
	if err != nil {
		return OpeningBatchResult{}, err
	}
	status, body, err := i.c.do(ctx, http.MethodPost, pathInventoryBatches, tenantID, legalEntityID, dutyInventoryImport, "", payload)
	if err != nil {
		return OpeningBatchResult{}, err
	}
	return openingAnswer(status, body, batchKey, len(items), CodeInventoryRowsInvalid, CodeInventoryRowsHeld)
}

func openingAnswer(status int, body []byte, key string, n int, rowCodes ...string) (OpeningBatchResult, error) {
	switch status {
	case http.StatusOK, http.StatusCreated:
		var b stockBatch
		if json.Unmarshal(body, &b) != nil || b.BatchKey != key || b.ItemCount != n {
			return OpeningBatchResult{}, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the answer does not describe the batch sent"}
		}
		return OpeningBatchResult{ItemCount: b.ItemCount, Replayed: status == http.StatusOK}, nil
	case http.StatusUnprocessableEntity:
		var pb problemBody
		_ = json.Unmarshal(body, &pb)
		for _, c := range rowCodes {
			if c != "" && pb.Code == c {
				if rows := rowErrors(pb.Errors, n); len(rows) > 0 {
					return OpeningBatchResult{}, &RowsRefused{Code: pb.Code, Rows: rows}
				}
			}
		}
	}
	return OpeningBatchResult{}, answerError(status, body)
}

// OnHand reads back what GET /v1/inventory/onhand reports on hand in the legal entity for each product (H-S9-2): one inquiry per
// product grouped by legal entity, the physical inventory of the entity's group. A truncated or unreadable answer is an error, so a
// partial read never passes for a total.
func (i *Inventory) OnHand(ctx context.Context, tenantID, legalEntityID string, products []string) (map[string]decimal.Decimal, error) {
	if !i.Configured() {
		return nil, ErrNotConfigured
	}
	out := make(map[string]decimal.Decimal, len(products))
	for _, p := range products {
		q := url.Values{"product": {p}, "groupBy": {"legal_entity"}}
		status, body, err := i.c.do(ctx, http.MethodGet, pathInventoryOnHand+"?"+q.Encode(), tenantID, legalEntityID, dutyInventoryRead, "", nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, answerError(status, body)
		}
		var res struct {
			Items []struct {
				Group          map[string]string `json:"group"`
				PhysicalInvent string            `json:"physicalInvent"`
			} `json:"items"`
			Truncated bool `json:"truncated"`
		}
		if json.Unmarshal(body, &res) != nil || res.Truncated {
			return nil, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the on-hand inquiry of product " + p + " is not readable"}
		}
		sum := decimal.Zero
		for _, it := range res.Items {
			if !strings.EqualFold(it.Group["legal_entity"], legalEntityID) {
				continue
			}
			v, err := decimal.NewFromString(it.PhysicalInvent)
			if err != nil {
				return nil, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the on-hand quantity of product " + p + " is not a number"}
			}
			sum = sum.Add(v)
		}
		out[p] = sum
	}
	return out, nil
}

// BatchTotals reads every opening batch the inventory ledger holds for the legal entity and sums the ones the factory
// wrote for the pack of the company (keep reports whether a batch key is one): the number of items and, per currency,
// the quantity and the value.
func (i *Inventory) BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.OpeningTotals, error) {
	sum := domain.NewOpeningTotals()
	if !i.Configured() {
		return sum, ErrNotConfigured
	}
	err := readPages(ctx, i.c, pathInventoryBatches, tenantID, legalEntityID, dutyInventoryImportRead, func(raw json.RawMessage) error {
		var b stockBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if !keep(b.BatchKey) {
			return nil
		}
		q, err := decimal.NewFromString(b.Qty)
		if err != nil {
			return err
		}
		v, err := decimal.NewFromString(b.Value)
		if err != nil {
			return err
		}
		sum.Items += b.ItemCount
		sum.Qty[b.Currency] = sum.Qty[b.Currency].Add(q)
		sum.Value[b.Currency] = sum.Value[b.Currency].Add(v)
		return nil
	})
	return sum, err
}

// Treasury is the opening bank balance client.
type Treasury struct{ c *Receivables }

// NewTreasury builds the client. A zero Config is valid and reports Configured false.
func NewTreasury(cfg Config) *Treasury {
	c := NewReceivables(cfg)
	c.aud = audTreasury
	return &Treasury{c: c}
}

// Configured reports whether a load can be attempted: an address and a signing key.
func (t *Treasury) Configured() bool { return t != nil && t.c.Configured() }

type bankRequest struct {
	LegalEntityID string            `json:"legalEntityId"`
	BatchKey      string            `json:"batchKey"`
	AsOf          string            `json:"asOf"`
	Items         []domain.BankItem `json:"items"`
}

type bankBatch struct {
	BatchKey    string           `json:"batchKey"`
	ItemCount   int              `json:"itemCount"`
	TotalsMinor map[string]int64 `json:"totalsMinor"`
}

// LoadBatch sends one batch of 1 to 1000 accounts. A refusal for rows (TRS-0601, TRS-0602) is a *RowsRefused; any other
// 4xx is ErrRefused (a changed batch under an old key, an account that already has an opening balance).
func (t *Treasury) LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey, asOf string, items []domain.BankItem) (OpeningBatchResult, error) {
	if !t.Configured() {
		return OpeningBatchResult{}, ErrNotConfigured
	}
	if len(items) == 0 || len(items) > domain.MaxOpeningBatchItems {
		return OpeningBatchResult{}, fmt.Errorf("a batch holds 1 to %d items, got %d", domain.MaxOpeningBatchItems, len(items))
	}
	payload, err := json.Marshal(bankRequest{LegalEntityID: legalEntityID, BatchKey: batchKey, AsOf: asOf, Items: items})
	if err != nil {
		return OpeningBatchResult{}, err
	}
	status, body, err := t.c.do(ctx, http.MethodPost, pathTreasuryBatches, tenantID, legalEntityID, dutyTreasuryImport, "", payload)
	if err != nil {
		return OpeningBatchResult{}, err
	}
	return openingAnswer(status, body, batchKey, len(items), CodeTreasuryRowsInvalid, CodeTreasuryRowsRefused)
}

// BatchTotals sums the batches the factory wrote for the pack of the company: the number of accounts and the balance
// per currency in minor units.
func (t *Treasury) BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.OpeningTotals, error) {
	sum := domain.NewOpeningTotals()
	if !t.Configured() {
		return sum, ErrNotConfigured
	}
	err := readPages(ctx, t.c, pathTreasuryBatches, tenantID, legalEntityID, dutyTreasuryImportRead, func(raw json.RawMessage) error {
		var b bankBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if !keep(b.BatchKey) {
			return nil
		}
		sum.Items += b.ItemCount
		for c, v := range b.TotalsMinor {
			sum.Minor[c] += v
		}
		return nil
	})
	return sum, err
}

// readPages pages through a target's opening batch list and hands each item to fn. More than maxOpeningPages pages is an
// unreadable answer rather than a partial total.
func readPages(ctx context.Context, c *Receivables, path, tenantID, legalEntityID, duty string, fn func(json.RawMessage) error) error {
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxOpeningPages {
			return &Error{Kind: ErrBadAnswer, Detail: c.aud + " lists more opening batches than the factory reads back; the totals cannot be read completely"}
		}
		q := url.Values{"legal_entity": {legalEntityID}, "limit": {strconv.Itoa(openingPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		status, body, err := c.do(ctx, http.MethodGet, path+"?"+q.Encode(), tenantID, legalEntityID, duty, "", nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return answerError(status, body)
		}
		var out struct {
			Items      []json.RawMessage `json:"items"`
			NextCursor string            `json:"nextCursor"`
		}
		if json.Unmarshal(body, &out) != nil {
			return &Error{Kind: ErrBadAnswer, Status: status, Detail: "the opening batches are not readable"}
		}
		for _, raw := range out.Items {
			if err := fn(raw); err != nil {
				return &Error{Kind: ErrBadAnswer, Status: status, Detail: "an opening batch is not readable: " + err.Error()}
			}
		}
		if out.NextCursor == "" {
			return nil
		}
		cursor = out.NextCursor
	}
}
