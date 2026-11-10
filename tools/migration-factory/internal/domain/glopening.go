package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
)

var ErrOpeningUnbalanced = errors.New("opening journal out of balance")

const (
	GLOpeningSourceType = "opening_balance"
	MaxOpeningLines     = 10000
)

type OpeningJournal struct {
	Currency  string
	Key       string
	Hash      string
	Debit     decimal.Decimal
	Credit    decimal.Decimal
	LineCount int
	Entry     ledgerclient.Entry
}

func OpeningKey(system SourceSystem, company, currency string) string {
	sum := sha256.Sum256([]byte(string(system) + "\x1f" + company + "\x1f" + currency))
	return "mf-gl-open-" + hex.EncodeToString(sum[:16])
}

func ParsePostingDate(s string) (string, error) {
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return "", fmt.Errorf("%w: postingDate must be a date as YYYY-MM-DD", ErrInvalid)
	}
	return s, nil
}

// BuildOpeningJournals turns the migrated balances of one legacy company into one opening journal per
// currency for a legal entity. Every currency must balance exactly and every amount must be a whole
// minor unit; otherwise nothing is built and the error names each currency that is out of balance
// (FR-49.2, BR-49.2).
func BuildOpeningJournals(rows []GLBalance, system SourceSystem, company, legalEntityID, ledgerID, postingDate string) ([]OpeningJournal, error) {
	byCurrency := map[string][]GLBalance{}
	for _, g := range rows {
		if g.Balance.IsZero() {
			continue
		}
		if !g.Balance.Equal(g.Balance.Round(money.MinorUnits(g.Currency))) {
			return nil, fmt.Errorf("%w: balance %s of account %s (%s) in %s has more decimals than the currency allows; the opening journal does not round", ErrInvalid,
				g.Balance, g.Account, g.LegacyID, g.Currency)
		}
		byCurrency[g.Currency] = append(byCurrency[g.Currency], g)
	}
	currencies := make([]string, 0, len(byCurrency))
	for c := range byCurrency {
		currencies = append(currencies, c)
	}
	sort.Strings(currencies)
	var out []OpeningJournal
	var unbalanced []string
	for _, cur := range currencies {
		list := byCurrency[cur]
		sort.Slice(list, func(i, j int) bool { return list[i].LegacyID < list[j].LegacyID })
		j := OpeningJournal{Currency: cur, Key: OpeningKey(system, company, cur), LineCount: len(list)}
		j.Entry = ledgerclient.Entry{LegalEntityID: legalEntityID, LedgerID: ledgerID, DocumentID: "opening:" + company + ":" + cur,
			PostingDate: postingDate, Currency: cur, IdempotencyKey: j.Key, SourceType: GLOpeningSourceType}
		var hashed []string
		for _, g := range list {
			credit := g.Balance.IsNegative()
			if credit {
				j.Credit = j.Credit.Add(g.Balance.Abs())
			} else {
				j.Debit = j.Debit.Add(g.Balance)
			}
			text := strings.TrimSpace(fmt.Sprintf("Opening balance %s %s", g.LegacyID, g.Dimensions))
			j.Entry.Lines = append(j.Entry.Lines, ledgerclient.Line{Account: g.Account, Credit: credit, Amount: g.Balance.Abs(), Text: text})
			hashed = append(hashed, g.LegacyID+"|"+g.Account+"|"+g.Dimensions+"|"+g.Balance.String())
		}
		if !j.Debit.Equal(j.Credit) {
			unbalanced = append(unbalanced, fmt.Sprintf("%s debits %s, credits %s, difference %s", cur, j.Debit, j.Credit, j.Debit.Sub(j.Credit)))
			continue
		}
		if len(j.Entry.Lines) > MaxOpeningLines {
			return nil, fmt.Errorf("%w: the opening journal in %s has %d lines, more than the %d one journal may hold", ErrInvalid, cur, len(j.Entry.Lines), MaxOpeningLines)
		}
		sum := sha256.Sum256([]byte(strings.Join(hashed, "\n")))
		j.Hash = hex.EncodeToString(sum[:])
		out = append(out, j)
	}
	if len(unbalanced) > 0 {
		return nil, fmt.Errorf("%w: MIG-1540 the opening journal of legal entity %s is out of balance: %s; quarantined or missing balances are the usual cause, nothing was posted",
			ErrOpeningUnbalanced, legalEntityID, strings.Join(unbalanced, "; "))
	}
	return out, nil
}
