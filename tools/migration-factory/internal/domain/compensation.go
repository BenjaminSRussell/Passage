package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
)

// Compensating journals of a rolled-back cutover (FR-49.9, D9). When a cutover is ROLLED_BACK the
// Passage journals posted since T-0 go back to the legacy system for re-keying; each one is
// compensated in the Passage ledger by an entry with the same accounts and amounts on the opposite
// side, so the ledger no longer carries what the legacy system will hold again. The lines are
// derived from the stored re-keying lines, never from the request.

// CompensationStatus is the state of one compensating journal.
type CompensationStatus string

// Compensation states.
const (
	CompensationPending CompensationStatus = "PENDING"
	CompensationPosted  CompensationStatus = "POSTED"
	CompensationParked  CompensationStatus = "PARKED"
)

// Compensation limits and the source type the ledger records.
const (
	CompensationSourceType  = "migration_rollback"
	MaxCompensationAttempts = 5
	maxCompensationBackoff  = time.Hour
	CompensationTotalName   = "rollback.compensation"
	MsgCompensationOpen     = "CUT-1610"
)

// Compensation is one compensating journal: the re-keying journal (entity, document, currency) it
// reverses, its fixed idempotency key and its posting state.
type Compensation struct {
	Entity        string
	DocumentRef   string
	Currency      string
	Key           string
	Status        CompensationStatus
	Attempts      int
	LastCode      string
	NextAttemptAt time.Time
	EntryID       string
	PostedAt      *time.Time
}

// CompensationKey is the idempotency key of the compensation of one journal of a plan: the same
// journal always gets the same key, so the ledger books it once however often it is sent.
func CompensationKey(planID, entity, documentRef, currency string) string {
	sum := sha256.Sum256([]byte(planID + "\x1f" + entity + "\x1f" + documentRef + "\x1f" + currency))
	return "mf-rollback-" + hex.EncodeToString(sum[:16])
}

// PlanCompensations lists the compensations the journal lines of a rolled-back plan need: one per
// (entity, document, currency), in file order. Document lines are subledger documents and are
// re-keyed only; they post no journal of their own here.
func PlanCompensations(planID string, journals []RekeyPosting) []Compensation {
	seen := map[string]bool{}
	var out []Compensation
	for _, p := range journals {
		if p.Kind != KindJournal {
			continue
		}
		k := p.Entity + "\x1f" + p.DocumentRef + "\x1f" + p.Currency
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Compensation{Entity: p.Entity, DocumentRef: p.DocumentRef, Currency: p.Currency,
			Key: CompensationKey(planID, p.Entity, p.DocumentRef, p.Currency), Status: CompensationPending})
	}
	return out
}

// CompensationEntry builds the ledger entry that compensates c from the stored journal lines of the
// plan: every line of the journal on the opposite side (a debit, stored positive, becomes a credit),
// in the ledger and legal entity the entity is pinned to, dated postingDate (YYYY-MM-DD). The lines
// must balance; a journal with no non-zero line has nothing to compensate.
func CompensationEntry(c Compensation, lines []RekeyPosting, legalEntityID, ledgerID, postingDate string) (ledgerclient.Entry, error) {
	e := ledgerclient.Entry{LegalEntityID: legalEntityID, LedgerID: ledgerID, DocumentID: "rollback:" + c.DocumentRef,
		PostingDate: postingDate, Currency: c.Currency, IdempotencyKey: c.Key, SourceType: CompensationSourceType}
	sum := decimal.Zero
	for _, p := range lines {
		if p.Kind != KindJournal || p.Entity != c.Entity || p.DocumentRef != c.DocumentRef || p.Currency != c.Currency || p.Amount.IsZero() {
			continue
		}
		sum = sum.Add(p.Amount)
		text := strings.TrimSpace(fmt.Sprintf("Rollback of %s line %d %s", p.DocumentRef, p.Line, p.Dimensions))
		e.Lines = append(e.Lines, ledgerclient.Line{Account: p.Account, Credit: p.Amount.IsPositive(), Amount: p.Amount.Abs(), Text: text})
	}
	if len(e.Lines) == 0 {
		return ledgerclient.Entry{}, fmt.Errorf("%w: journal %s of %s in %s has no line to compensate", ErrInvalid, c.DocumentRef, c.Entity, c.Currency)
	}
	if !sum.IsZero() {
		return ledgerclient.Entry{}, fmt.Errorf("%w: journal %s of %s does not balance in %s", ErrInvalid, c.DocumentRef, c.Entity, c.Currency)
	}
	return e, nil
}

// CompensationBackoff is how long a compensation waits after its n-th failed attempt: one minute
// doubled per attempt, at most an hour.
func CompensationBackoff(attempts int) time.Duration {
	d := time.Minute
	for i := 1; i < attempts && d < maxCompensationBackoff; i++ {
		d *= 2
	}
	return min(d, maxCompensationBackoff)
}

// FailCompensation records a failed attempt with the ledger's code: the compensation backs off, and
// parks when the ledger rejected it outright (a 4xx such as a closed period or unknown account,
// which a retry would only repeat) or after MaxCompensationAttempts. A parked compensation is tried
// again only when an operator asks for it.
func FailCompensation(c *Compensation, code string, rejected bool, now time.Time) {
	c.Attempts++
	c.LastCode = code
	c.NextAttemptAt = now.Add(CompensationBackoff(c.Attempts))
	if rejected || c.Attempts >= MaxCompensationAttempts {
		c.Status = CompensationParked
	}
}

// ReconcileCompensations compares, per (entity, account, currency), the net of the journals posted
// since T-0 (the source) with the net the posted compensations took off the ledger (the target):
// only this plan's re-keying journals and their compensations are counted, never the ledger's other
// movements. A journal not yet compensated leaves its accounts MISMATCH.
func ReconcileCompensations(journals []RekeyPosting, comps []Compensation) ([]ControlTotal, error) {
	posted := map[string]bool{}
	for _, c := range comps {
		if c.Status == CompensationPosted {
			posted[c.Entity+"\x1f"+c.DocumentRef+"\x1f"+c.Currency] = true
		}
	}
	src, tgt := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	keys := map[string]map[string]string{}
	for _, p := range journals {
		if p.Kind != KindJournal {
			continue
		}
		k := p.Entity + "\x1f" + p.Account + "\x1f" + p.Currency
		keys[k] = map[string]string{"entity": p.Entity, "account": p.Account, "currency": p.Currency}
		src[k] = src[k].Add(p.Amount)
		if posted[p.Entity+"\x1f"+p.DocumentRef+"\x1f"+p.Currency] {
			tgt[k] = tgt[k].Add(p.Amount)
		} else {
			tgt[k] = tgt[k].Add(decimal.Zero)
		}
	}
	out := make([]ControlTotal, 0, len(keys))
	for _, k := range sortedKeys(keys) {
		t, err := NewControlTotal(CompensationTotalName, keys[k], src[k], tgt[k], Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
