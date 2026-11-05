// Package replay runs the parallel-run replay (FR-49.5 step 1, D4): it posts a legacy period's
// journal documents, in their original sequence, through the ledger's public posting API into a
// rehearsal ledger, reads back the trial balance the ledger holds for the accounts the replay
// touched, and builds the parallel-run input that diffs it against the legacy trial balance.
package replay

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

var (
	// ErrLedgerForeign is returned when the legal entity does not own the rehearsal ledger.
	ErrLedgerForeign = errors.New("ledger not owned by the legal entity")
	// ErrLedgerUnavailable is returned when the ledger fails in a way a retry may fix; the replay
	// stops and can be run again, since every document is posted under a fixed idempotency key.
	ErrLedgerUnavailable = errors.New("ledger unavailable")
)

// Request is one replay: the period and system, the rehearsal ledger with its legal entity, the
// legacy documents and, optionally, the legacy trial balance extract. Without one, the legacy side
// is the documents' own trial balance.
type Request struct {
	Period          string
	System          domain.SourceSystem
	LegalEntityID   string
	LedgerID        string
	Documents       []domain.ReplayDocument
	LegacyTB        []domain.TBAccount
	Revenue         decimal.Decimal
	ExplainedMaxPct decimal.Decimal
}

// Rejected is a document the ledger refused, with its code; its amounts are missing from the
// Passage trial balance and show as variances.
type Rejected struct {
	DocumentID string `json:"documentId"`
	Seq        int    `json:"seq"`
	Code       string `json:"code"`
}

// Result is the replay's outcome: the parallel-run input to diff and store, the posted entry ids by
// document, and the rejected documents.
type Result struct {
	Input    domain.ParallelInput
	Posted   map[string]string
	Rejected []Rejected
}

// Run replays the documents and reads back the trial balance. It refuses a ledger the legal entity
// does not own. A document the ledger rejects (a 4xx, such as an unknown account or a closed period)
// is recorded and the replay goes on; any other failure stops it with ErrLedgerUnavailable. The
// Passage side reads, per account the replay or the legacy trial balance touched, the ledger's
// balance for the period, so other accounts of the ledger are not compared.
func Run(ctx context.Context, ledger ledgerclient.Ledger, in Request) (Result, error) {
	if err := domain.ValidateReplay(in.Period, in.Documents); err != nil {
		return Result{}, err
	}
	owner, err := ledger.LegalEntity(ctx, in.LegalEntityID, in.LedgerID)
	if err != nil {
		if ledgerclient.Rejected(err) {
			return Result{}, fmt.Errorf("%w: ledger %s is unknown or not readable for legal entity %s (%s)", ErrLedgerForeign, in.LedgerID, in.LegalEntityID, ledgerclient.CodeOf(err))
		}
		return Result{}, fmt.Errorf("%w: ledger lookup: %v", ErrLedgerUnavailable, err)
	}
	if owner != in.LegalEntityID {
		return Result{}, fmt.Errorf("%w: ledger %s belongs to another legal entity", ErrLedgerForeign, in.LedgerID)
	}
	res := Result{Posted: map[string]string{}}
	for _, d := range domain.OrderReplay(in.Documents) {
		id, err := ledger.Post(ctx, domain.ReplayEntry(in.System, in.Period, d, in.LegalEntityID, in.LedgerID))
		switch {
		case err == nil:
			res.Posted[d.DocumentID] = id
		case ledgerclient.Rejected(err) || errors.Is(err, ledgerclient.ErrEmptyEntry) || errors.Is(err, ledgerclient.ErrUnbalanced):
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = "LEDGER_REFUSED"
			}
			res.Rejected = append(res.Rejected, Rejected{DocumentID: d.DocumentID, Seq: d.Seq, Code: code})
		default:
			return Result{}, fmt.Errorf("%w: document %s: %v", ErrLedgerUnavailable, d.DocumentID, err)
		}
	}
	legacy := in.LegacyTB
	if len(legacy) == 0 {
		legacy = domain.TrialBalanceFromDocuments(in.Documents)
	}
	accounts := map[string]bool{}
	for _, a := range legacy {
		accounts[a.Account] = true
	}
	for _, d := range in.Documents {
		for _, l := range d.Lines {
			accounts[l.Account] = true
		}
	}
	names := make([]string, 0, len(accounts))
	for a := range accounts {
		names = append(names, a)
	}
	sort.Strings(names)
	var passage []domain.TBAccount
	for _, a := range names {
		b, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, Account: a,
			PeriodFrom: in.Period, PeriodTo: in.Period})
		if err != nil {
			return Result{}, fmt.Errorf("%w: balance of %s: %v", ErrLedgerUnavailable, a, err)
		}
		if net := b.Net(); !net.IsZero() {
			passage = append(passage, domain.TBAccount{Account: a, Currency: b.Currency, Net: net})
		}
	}
	res.Input = domain.ParallelInput{Period: in.Period, SourceSystem: in.System, DocumentsReplayed: len(res.Posted),
		Revenue: in.Revenue, ExplainedMaxPct: in.ExplainedMaxPct,
		Legacy: domain.TBLines(legacy, in.Period, "legacy"), Passage: domain.TBLines(passage, in.Period, "passage")}
	return res, nil
}
