package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Subledger parallel-run replay (49-4, FR-49.5 step 1, D4): beside the journal documents, a legacy period's
// subledger documents (AR and AP invoices, credit notes and payments, inventory movements, fixed-asset
// transactions) are posted through the owning subledger's posting API, in their legacy sequence, and each
// subledger's period movement per party, item-and-site or asset is read back and diffed against the legacy
// side as the INV, AP, AR and FA tuples of the parallel run.

// Subledger areas a document can belong to.
const (
	AreaAR  Area = "AR"
	AreaAP  Area = "AP"
	AreaINV Area = "INV"
	AreaFA  Area = "FA"
)

// SubledgerAreas lists the areas a subledger document can carry (the parallel-run areas besides TB).
var SubledgerAreas = []Area{AreaINV, AreaAP, AreaAR, AreaFA}

// Document kinds per area, with the sign the movement has on the subledger balance: +1 raises it, -1 lowers it, 0 either.
var subledgerKinds = map[Area]map[string]int{
	AreaAR:  {"INVOICE": 1, "CREDIT_NOTE": -1, "PAYMENT": -1, "ADJUSTMENT": 0},
	AreaAP:  {"INVOICE": 1, "CREDIT_NOTE": -1, "PAYMENT": -1, "ADJUSTMENT": 0},
	AreaINV: {"RECEIPT": 1, "ISSUE": -1, "TRANSFER": 0, "ADJUSTMENT": 0},
	AreaFA:  {"ACQUISITION": 1, "DEPRECIATION": -1, "DISPOSAL": -1, "ADJUSTMENT": 0},
}

// SubledgerDocument is one legacy subledger document of the period. Amount is its signed effect on the subledger
// balance of Key (the party for AR and AP, "item@site" for INV, the asset for FA): an invoice, receipt or acquisition
// raises it, a credit note, payment, issue or depreciation lowers it.
type SubledgerDocument struct {
	Seq         int
	DocumentID  string
	Area        Area
	Kind        string
	PostingDate string
	Key         string
	Currency    string
	Amount      decimal.Decimal
}

// ValidateSubledger checks the documents of a replay: a known area and kind, a unique id and sequence among them, a
// posting date inside the period, a key, an ISO 4217 currency and a non-zero amount of at most 6 decimals whose sign
// agrees with the kind.
func ValidateSubledger(period string, docs []SubledgerDocument) error {
	ids, seqs := map[string]bool{}, map[int]bool{}
	for _, d := range docs {
		if strings.TrimSpace(d.DocumentID) == "" || ids[d.DocumentID] {
			return fmt.Errorf("%w: subledger document id %q is blank or repeated", ErrInvalid, d.DocumentID)
		}
		ids[d.DocumentID] = true
		if d.Seq < 1 || seqs[d.Seq] {
			return fmt.Errorf("%w: subledger document %s sequence %d must be positive and unique", ErrInvalid, d.DocumentID, d.Seq)
		}
		seqs[d.Seq] = true
		kinds, ok := subledgerKinds[d.Area]
		if !ok {
			return fmt.Errorf("%w: subledger document %s area %q must be one of INV, AP, AR, FA", ErrInvalid, d.DocumentID, d.Area)
		}
		sign, ok := kinds[d.Kind]
		if !ok {
			return fmt.Errorf("%w: subledger document %s kind %q is not a %s document", ErrInvalid, d.DocumentID, d.Kind, d.Area)
		}
		if t, err := time.Parse(time.DateOnly, d.PostingDate); err != nil || t.Format("2006-01") != period {
			return fmt.Errorf("%w: subledger document %s posting date %q is not a date in %s", ErrInvalid, d.DocumentID, d.PostingDate, period)
		}
		if strings.TrimSpace(d.Key) == "" || strings.Contains(d.Key, "|") {
			return fmt.Errorf("%w: subledger document %s key %q is blank or contains '|'", ErrInvalid, d.DocumentID, d.Key)
		}
		if !validCurrency(d.Currency) {
			return fmt.Errorf("%w: subledger document %s currency %q is not ISO 4217", ErrInvalid, d.DocumentID, d.Currency)
		}
		if d.Amount.IsZero() || !d.Amount.Equal(d.Amount.Round(6)) {
			return fmt.Errorf("%w: subledger document %s amount must be non-zero with at most 6 decimals", ErrInvalid, d.DocumentID)
		}
		if (sign > 0 && !d.Amount.IsPositive()) || (sign < 0 && !d.Amount.IsNegative()) {
			return fmt.Errorf("%w: subledger document %s is a %s %s, so its amount must be %s", ErrInvalid, d.DocumentID, d.Area, d.Kind, map[bool]string{true: "positive", false: "negative"}[sign > 0])
		}
	}
	return nil
}

// OrderSubledger returns the documents in the legacy posting sequence: by posting date, then Seq.
func OrderSubledger(docs []SubledgerDocument) []SubledgerDocument {
	out := append([]SubledgerDocument(nil), docs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PostingDate != out[j].PostingDate {
			return out[i].PostingDate < out[j].PostingDate
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// SubledgerReplayKey is the idempotency key of a replayed subledger document, so a replay run twice posts it once.
func SubledgerReplayKey(system SourceSystem, period string, area Area, documentID string) string {
	return ReplayKey(system, period, string(area)+"/"+documentID)
}

// SubledgerKey is the tuple key of a subledger movement: the party, item-and-site or asset, the currency and the period.
func SubledgerKey(key, currency, period string) string { return key + "|" + currency + "|" + period }

// SubledgerBalance is the net movement of one key of a subledger in a period, from either side.
type SubledgerBalance struct {
	Area     Area
	Key      string
	Currency string
	Net      decimal.Decimal
}

// SubledgerFromDocuments sums the documents into the movement per (area, key, currency).
func SubledgerFromDocuments(docs []SubledgerDocument) []SubledgerBalance {
	type k struct {
		area     Area
		key, cur string
	}
	sums := map[k]decimal.Decimal{}
	for _, d := range docs {
		sums[k{d.Area, d.Key, d.Currency}] = sums[k{d.Area, d.Key, d.Currency}].Add(d.Amount)
	}
	out := make([]SubledgerBalance, 0, len(sums))
	for key, v := range sums {
		out = append(out, SubledgerBalance{Area: key.area, Key: key.key, Currency: key.cur, Net: v})
	}
	sortBalances(out)
	return out
}

func sortBalances(b []SubledgerBalance) {
	sort.Slice(b, func(i, j int) bool {
		if b[i].Area != b[j].Area {
			return b[i].Area < b[j].Area
		}
		if b[i].Key != b[j].Key {
			return b[i].Key < b[j].Key
		}
		return b[i].Currency < b[j].Currency
	})
}

// SubledgerDocumentCounts counts, per (area, tuple key), the documents that moved it; the tolerance of a per-document
// rule (RND-001, BR-49.7) scales with it.
func SubledgerDocumentCounts(docs []SubledgerDocument, period string) map[string]int {
	out := map[string]int{}
	for _, d := range docs {
		out[string(d.Area)+"|"+SubledgerKey(d.Key, d.Currency, period)]++
	}
	return out
}

// SubledgerLines turns the movements into the parallel run's tuples for one side ("legacy" or "passage"). Movements
// of a repeated key add up. counts gives each tuple's document count (SubledgerDocumentCounts); nil leaves them out.
func SubledgerLines(b []SubledgerBalance, period, side string, counts map[string]int) []Line {
	type k struct {
		area Area
		key  string
	}
	sums := map[k]decimal.Decimal{}
	for _, x := range b {
		key := k{x.Area, SubledgerKey(x.Key, x.Currency, period)}
		sums[key] = sums[key].Add(x.Net)
	}
	keys := make([]k, 0, len(sums))
	for key := range sums {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].area != keys[j].area {
			return keys[i].area < keys[j].area
		}
		return keys[i].key < keys[j].key
	})
	out := make([]Line, 0, len(keys))
	for _, key := range keys {
		l := Line{Area: key.area, Key: key.key, Documents: counts[string(key.area)+"|"+key.key]}
		if side == "legacy" {
			l.Legacy = sums[key]
		} else {
			l.Passage = sums[key]
		}
		out = append(out, l)
	}
	return out
}
