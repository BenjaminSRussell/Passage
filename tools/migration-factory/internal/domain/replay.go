package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
)

// Parallel-run replay (FR-49.5 step 1, D4): the legacy period's journal documents are posted, in
// their original sequence, through the ledger's public posting API into a rehearsal ledger; the
// trial balance the ledger then holds for the period is diffed with the legacy trial balance.

// ReplaySourceType is the source type the ledger records for a replayed document.
const ReplaySourceType = "migration_parallel_replay"

// ReplayLine is one line of a legacy journal document: debit positive, credit negative.
type ReplayLine struct {
	Account string
	Amount  decimal.Decimal
	Text    string
}

// ReplayDocument is one legacy journal document of the period (AX GeneralJournalEntry, SAP BKPF/BSEG):
// Seq is its position in the legacy posting sequence.
type ReplayDocument struct {
	Seq         int
	DocumentID  string
	PostingDate string
	Currency    string
	Lines       []ReplayLine
}

// ValidateReplay checks the documents of a replay: a unique id and sequence, a posting date inside
// the period, an ISO 4217 currency, at least two lines with an account and a non-zero amount of at
// most 6 decimals, and debits equal to credits. One bad document rejects the replay.
func ValidateReplay(period string, docs []ReplayDocument) error {
	if len(docs) == 0 {
		return fmt.Errorf("%w: a replay needs at least one document", ErrInvalid)
	}
	ids, seqs := map[string]bool{}, map[int]bool{}
	for _, d := range docs {
		if strings.TrimSpace(d.DocumentID) == "" || ids[d.DocumentID] {
			return fmt.Errorf("%w: document id %q is blank or repeated", ErrInvalid, d.DocumentID)
		}
		ids[d.DocumentID] = true
		if d.Seq < 1 || seqs[d.Seq] {
			return fmt.Errorf("%w: document %s sequence %d must be positive and unique", ErrInvalid, d.DocumentID, d.Seq)
		}
		seqs[d.Seq] = true
		if t, err := time.Parse(time.DateOnly, d.PostingDate); err != nil || t.Format("2006-01") != period {
			return fmt.Errorf("%w: document %s posting date %q is not a date in %s", ErrInvalid, d.DocumentID, d.PostingDate, period)
		}
		if !validCurrency(d.Currency) {
			return fmt.Errorf("%w: document %s currency %q is not ISO 4217", ErrInvalid, d.DocumentID, d.Currency)
		}
		if len(d.Lines) < 2 {
			return fmt.Errorf("%w: document %s needs at least two lines", ErrInvalid, d.DocumentID)
		}
		sum := decimal.Zero
		for _, l := range d.Lines {
			if strings.TrimSpace(l.Account) == "" || l.Amount.IsZero() || !l.Amount.Equal(l.Amount.Round(6)) {
				return fmt.Errorf("%w: document %s has a line without account, with a zero amount or with more than 6 decimals", ErrInvalid, d.DocumentID)
			}
			sum = sum.Add(l.Amount)
		}
		if !sum.IsZero() {
			return fmt.Errorf("%w: document %s does not balance (difference %s)", ErrInvalid, d.DocumentID, sum.StringFixed(6))
		}
	}
	return nil
}

// OrderReplay returns the documents in the legacy posting sequence: by posting date, then Seq.
func OrderReplay(docs []ReplayDocument) []ReplayDocument {
	out := append([]ReplayDocument(nil), docs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].PostingDate != out[j].PostingDate {
			return out[i].PostingDate < out[j].PostingDate
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// ReplayKey is the idempotency key of a replayed document, so a replay run twice books it once.
func ReplayKey(system SourceSystem, period, documentID string) string {
	sum := sha256.Sum256([]byte(string(system) + "\x1f" + period + "\x1f" + documentID))
	return "mf-replay-" + hex.EncodeToString(sum[:16])
}

// ReplayEntry is the ledger entry a legacy document is replayed as.
func ReplayEntry(system SourceSystem, period string, d ReplayDocument, legalEntityID, ledgerID string) ledgerclient.Entry {
	e := ledgerclient.Entry{LegalEntityID: legalEntityID, LedgerID: ledgerID, DocumentID: d.DocumentID, PostingDate: d.PostingDate,
		Currency: d.Currency, IdempotencyKey: ReplayKey(system, period, d.DocumentID), SourceType: ReplaySourceType}
	for _, l := range d.Lines {
		e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Amount.IsNegative(), Amount: l.Amount.Abs(), Text: l.Text})
	}
	return e
}

// TBKey is the trial-balance tuple key of an account in a currency and period.
func TBKey(account, currency, period string) string { return account + "|" + currency + "|" + period }

// TBAccount is one account's net balance (debit positive) in a currency, from either side.
type TBAccount struct {
	Account  string
	Currency string
	Net      decimal.Decimal
}

// TrialBalanceFromDocuments sums the documents into a trial balance per (account, currency).
func TrialBalanceFromDocuments(docs []ReplayDocument) []TBAccount {
	sums := map[string]*TBAccount{}
	for _, d := range docs {
		for _, l := range d.Lines {
			k := l.Account + "\x1f" + d.Currency
			if sums[k] == nil {
				sums[k] = &TBAccount{Account: l.Account, Currency: d.Currency}
			}
			sums[k].Net = sums[k].Net.Add(l.Amount)
		}
	}
	out := make([]TBAccount, 0, len(sums))
	for _, k := range sortedKeys(sums) {
		out = append(out, *sums[k])
	}
	return out
}

// TBLines turns a trial balance into the parallel run's TB tuples for one side ("legacy" or
// "passage"). Accounts are summed per key, so a repeated account adds up.
func TBLines(tb []TBAccount, period, side string) []Line {
	byKey := map[string]decimal.Decimal{}
	for _, a := range tb {
		k := TBKey(a.Account, a.Currency, period)
		byKey[k] = byKey[k].Add(a.Net)
	}
	out := make([]Line, 0, len(byKey))
	for _, k := range sortedKeys(byKey) {
		l := Line{Area: "TB", Key: k}
		if side == "legacy" {
			l.Legacy = byKey[k]
		} else {
			l.Passage = byKey[k]
		}
		out = append(out, l)
	}
	return out
}
