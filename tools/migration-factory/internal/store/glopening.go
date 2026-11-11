package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

type OpeningJournalRequest struct {
	RunID         string
	LegalEntityID string
	LedgerID      string
	PostingDate   string
	By            string
}

type OpeningPosting struct {
	Currency      string          `json:"currency"`
	LegalEntityID string          `json:"legalEntityId"`
	LedgerID      string          `json:"ledgerId"`
	PostingDate   string          `json:"postingDate"`
	LineCount     int             `json:"lineCount"`
	Debit         decimal.Decimal `json:"-"`
	Credit        decimal.Decimal `json:"-"`
	EntryID       string          `json:"entryId"`
	Posted        bool            `json:"-"`
	AlreadyPosted bool            `json:"-"`
	PostedAt      *time.Time      `json:"-"`
}

type OpeningJournalResult struct {
	RunID         string
	LegalEntityID string
	LedgerID      string
	PostingDate   string
	Journals      []OpeningPosting
}

func openingRun(ctx context.Context, tx pgx.Tx, runID string) (domain.PackRun, error) {
	run, err := getRun(ctx, tx, runID, false)
	if err != nil {
		return domain.PackRun{}, err
	}
	if run.Pack != domain.PackGLBalances {
		return domain.PackRun{}, fmt.Errorf("%w: run %s is a %s run; the opening journal is posted from a gl_balances run", domain.ErrInvalid, runID, run.Pack)
	}
	if run.Status != domain.StatusLoaded && run.Status != domain.StatusReconciled {
		return domain.PackRun{}, fmt.Errorf("%w: run %s is %s; its balances must be loaded before the opening journal is posted", domain.ErrTransition, runID, run.Status)
	}
	return run, nil
}

func readOpeningBalances(ctx context.Context, tx pgx.Tx, system, company string) ([]domain.GLBalance, error) {
	rows, err := tx.Query(ctx, `select legacy_id, account, dimensions, currency, balance::text from gl_opening_balance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2
		order by legacy_id`, system, company)
	if err != nil {
		return nil, fmt.Errorf("read gl_opening_balance: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.GLBalance, error) {
		var g domain.GLBalance
		var raw string
		if err := r.Scan(&g.LegacyID, &g.Account, &g.Dimensions, &g.Currency, &raw); err != nil {
			return g, err
		}
		var err error
		g.Balance, err = decimal.NewFromString(raw)
		return g, err
	})
}

var ErrOpeningTargetTaken = errors.New("opening target taken")

type openingTarget struct{ legalEntityID, ledgerID string }

func readOpeningTarget(ctx context.Context, tx pgx.Tx, system, company string) (*openingTarget, error) {
	var t openingTarget
	err := tx.QueryRow(ctx, `select target_legal_entity_id::text, ledger_id from gl_opening_target
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2`, system, company).Scan(&t.legalEntityID, &t.ledgerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

func openingTargetTaken(company string) error {
	return fmt.Errorf("%w: the opening balances of %s are already assigned to another legal entity or ledger; one company is booked to one legal entity and ledger only", ErrOpeningTargetTaken, company)
}

type postedKey struct{ ledgerID, hash string }

func readOpeningPostings(ctx context.Context, tx pgx.Tx, system, company, legalEntityID string) (map[string]OpeningPosting, map[string]postedKey, error) {
	q := `select currency, legal_entity_id::text, ledger_id, posting_date::text, line_count, debit::text, credit::text, entry_id, posted_at, content_hash
		from gl_opening_posting where tenant_id = current_setting('passage.tenant_id', true)::uuid and source_system = $1 and source_company = $2`
	args := []any{system, company}
	if legalEntityID != "" {
		q += ` and legal_entity_id = $3`
		args = append(args, legalEntityID)
	}
	rows, err := tx.Query(ctx, q+` order by legal_entity_id, currency`, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("read gl_opening_posting: %w", err)
	}
	defer rows.Close()
	posts, keys := map[string]OpeningPosting{}, map[string]postedKey{}
	for rows.Next() {
		var p OpeningPosting
		var debit, credit, hash string
		if err := rows.Scan(&p.Currency, &p.LegalEntityID, &p.LedgerID, &p.PostingDate, &p.LineCount, &debit, &credit, &p.EntryID, &p.PostedAt, &hash); err != nil {
			return nil, nil, err
		}
		p.Debit, p.Credit, p.Posted = decimal.RequireFromString(debit), decimal.RequireFromString(credit), true
		posts[p.LegalEntityID+"|"+p.Currency] = p
		keys[p.LegalEntityID+"|"+p.Currency] = postedKey{p.LedgerID, hash}
	}
	return posts, keys, rows.Err()
}

// PostOpeningJournal posts the migrated GL balances of the run's legacy company to the general ledger
// as one opening journal per currency for the legal entity, through the ledger's ordinary posting API
// (period checks, audit and balancing are the ledger's). Every currency must balance or nothing is
// posted. A currency already posted with the same lines is not sent again; the same balances posted
// to another ledger or date, or changed since, are refused. The ledger books each key once, so a
// lost answer is repaired by calling again.
func (s *Postgres) PostOpeningJournal(ctx context.Context, ledger ledgerclient.Ledger, in OpeningJournalRequest) (OpeningJournalResult, error) {
	var run domain.PackRun
	var journals []domain.OpeningJournal
	var held map[string]OpeningPosting
	pinned := false
	if err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		if run, err = openingRun(ctx, tx, in.RunID); err != nil {
			return err
		}
		rows, err := readOpeningBalances(ctx, tx, string(run.SourceSystem), run.SourceCompany)
		if err != nil {
			return err
		}
		if journals, err = domain.BuildOpeningJournals(rows, run.SourceSystem, run.SourceCompany, in.LegalEntityID, in.LedgerID, in.PostingDate); err != nil {
			return err
		}
		mapped, err := readOpeningMapping(ctx, tx, string(run.SourceSystem), run.SourceCompany)
		if err != nil {
			return err
		}
		if mapped != nil && (mapped.legalEntityID != in.LegalEntityID || mapped.ledgerID != in.LedgerID) {
			return openingTargetMapped(run.SourceCompany)
		}
		pin, err := readOpeningTarget(ctx, tx, string(run.SourceSystem), run.SourceCompany)
		if err != nil {
			return err
		}
		if pin != nil && (pin.legalEntityID != in.LegalEntityID || pin.ledgerID != in.LedgerID) {
			return openingTargetTaken(run.SourceCompany)
		}
		pinned = pin != nil
		var keys map[string]postedKey
		if held, keys, err = readOpeningPostings(ctx, tx, string(run.SourceSystem), run.SourceCompany, in.LegalEntityID); err != nil {
			return err
		}
		current := map[string]bool{}
		for _, j := range journals {
			current[j.Currency] = true
		}
		for _, p := range held {
			if !current[p.Currency] && p.LegalEntityID == in.LegalEntityID {
				return fmt.Errorf("%w: the %s opening journal of legal entity %s was posted, but the company no longer holds a balance in that currency; correct it in the ledger by reversal",
					domain.ErrTransition, p.Currency, in.LegalEntityID)
			}
		}
		for _, j := range journals {
			k, ok := keys[in.LegalEntityID+"|"+j.Currency]
			if ok && (k.hash != j.Hash || held[in.LegalEntityID+"|"+j.Currency].PostingDate != in.PostingDate) {
				return fmt.Errorf("%w: the %s opening journal of legal entity %s was already posted with other balances or another date; correct it in the ledger by reversal, not by posting again",
					domain.ErrTransition, j.Currency, in.LegalEntityID)
			}
		}
		return nil
	}); err != nil {
		return OpeningJournalResult{}, err
	}
	res := OpeningJournalResult{RunID: run.RunID, LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, PostingDate: in.PostingDate}
	pending := false
	for _, j := range journals {
		if _, ok := held[in.LegalEntityID+"|"+j.Currency]; !ok {
			pending = true
		}
	}
	if pending {
		owner, err := ledger.LegalEntity(ctx, in.LegalEntityID, in.LedgerID)
		if err != nil {
			if ledgerclient.Rejected(err) {
				return OpeningJournalResult{}, fmt.Errorf("%w: ledger %s is unknown or not readable for legal entity %s (%s)", ErrLedgerForeign, in.LedgerID, in.LegalEntityID, ledgerclient.CodeOf(err))
			}
			return OpeningJournalResult{}, fmt.Errorf("ledger lookup: %w", err)
		}
		if owner != in.LegalEntityID {
			return OpeningJournalResult{}, fmt.Errorf("%w: ledger %s belongs to another legal entity", ErrLedgerForeign, in.LedgerID)
		}
	}
	// Only a pin this call created can be released again: an older pin may belong to a lost answer.
	createdPin := false
	if !pinned && len(journals) > 0 {
		var err error
		if createdPin, err = s.pinOpeningTarget(ctx, run, in); err != nil {
			return OpeningJournalResult{}, err
		}
	}
	for _, j := range journals {
		p := OpeningPosting{Currency: j.Currency, LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, PostingDate: in.PostingDate, LineCount: j.LineCount, Debit: j.Debit, Credit: j.Credit}
		if have, ok := held[in.LegalEntityID+"|"+j.Currency]; ok {
			p.EntryID, p.Posted, p.AlreadyPosted = have.EntryID, true, true
			res.Journals = append(res.Journals, p)
			continue
		}
		entryID, err := ledger.Post(ctx, j.Entry)
		if err != nil {
			slog.ErrorContext(ctx, "opening journal not posted", "run", in.RunID, "legalEntity", in.LegalEntityID, "currency", j.Currency, "key", j.Key, "err", err)
			// A definite refusal books nothing; free the company to be posted to another legal entity or ledger.
			// A lost answer may have booked the journal, so that pin stays.
			if createdPin && ledgerclient.Rejected(err) {
				s.releaseOpeningTarget(ctx, run, in)
			}
			return res, fmt.Errorf("post the %s opening journal: %w", j.Currency, err)
		}
		if err := s.tx(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into gl_opening_posting (tenant_id, source_system, source_company, legal_entity_id, currency, ledger_id, posting_date,
				idempotency_key, content_hash, line_count, debit, credit, entry_id, run_id, posted_by)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) on conflict do nothing`,
				string(run.SourceSystem), run.SourceCompany, in.LegalEntityID, j.Currency, in.LedgerID, in.PostingDate, j.Key, j.Hash, j.LineCount,
				j.Debit.String(), j.Credit.String(), entryID, run.RunID, in.By)
			return err
		}); err != nil {
			return res, fmt.Errorf("record the %s opening journal %s: %w", j.Currency, entryID, err)
		}
		p.EntryID, p.Posted = entryID, true
		res.Journals = append(res.Journals, p)
	}
	return res, nil
}

// releaseOpeningTarget drops the company's pin when no posting row exists (the database refuses otherwise).
func (s *Postgres) releaseOpeningTarget(ctx context.Context, run domain.PackRun, in OpeningJournalRequest) {
	err := s.tx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from gl_opening_target t
			where t.tenant_id = current_setting('passage.tenant_id', true)::uuid and t.source_system = $1 and t.source_company = $2
			and t.target_legal_entity_id = $3 and t.ledger_id = $4
			and not exists (select 1 from gl_opening_posting p where p.tenant_id = t.tenant_id and p.source_system = t.source_system and p.source_company = t.source_company)`,
			string(run.SourceSystem), run.SourceCompany, in.LegalEntityID, in.LedgerID)
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "opening target not released", "run", in.RunID, "legalEntity", in.LegalEntityID, "err", err)
	}
}

// pinOpeningTarget reports whether this call created the pin.
func (s *Postgres) pinOpeningTarget(ctx context.Context, run domain.PackRun, in OpeningJournalRequest) (created bool, err error) {
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// Under the company's lock the cutover plan's mapping is read again: a mapping recorded after this call's
		// first check must still decide the target, not the pin this call is about to create (#549).
		if err := lockOpeningCompany(ctx, tx, string(run.SourceSystem), run.SourceCompany); err != nil {
			return err
		}
		mapped, err := readOpeningMapping(ctx, tx, string(run.SourceSystem), run.SourceCompany)
		if err != nil {
			return err
		}
		if mapped != nil && (mapped.legalEntityID != in.LegalEntityID || mapped.ledgerID != in.LedgerID) {
			return openingTargetMapped(run.SourceCompany)
		}
		tag, err := tx.Exec(ctx, `insert into gl_opening_target (tenant_id, source_system, source_company, target_legal_entity_id, ledger_id, pinned_by)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5) on conflict do nothing`,
			string(run.SourceSystem), run.SourceCompany, in.LegalEntityID, in.LedgerID, in.By)
		if err != nil {
			return fmt.Errorf("pin gl_opening_target: %w", err)
		}
		created = tag.RowsAffected() == 1
		t, err := readOpeningTarget(ctx, tx, string(run.SourceSystem), run.SourceCompany)
		if err != nil {
			return err
		}
		if t == nil || t.legalEntityID != in.LegalEntityID || t.ledgerID != in.LedgerID {
			return openingTargetTaken(run.SourceCompany)
		}
		return nil
	})
	return created && err == nil, err
}

// openingPosted reports whether every currency the company holds a balance in is posted to the
// company's pinned legal entity with the lines of the balances held now.
func openingPosted(ctx context.Context, tx pgx.Tx, system, company string) (bool, error) {
	rows, err := readOpeningBalances(ctx, tx, system, company)
	if err != nil {
		return false, err
	}
	pin, err := readOpeningTarget(ctx, tx, system, company)
	if err != nil || pin == nil {
		return false, err
	}
	journals, err := domain.BuildOpeningJournals(rows, domain.SourceSystem(system), company, pin.legalEntityID, pin.ledgerID, "2000-01-01")
	if err != nil || len(journals) == 0 {
		return false, nil
	}
	_, keys, err := readOpeningPostings(ctx, tx, system, company, pin.legalEntityID)
	if err != nil {
		return false, err
	}
	if len(keys) != len(journals) {
		return false, nil
	}
	for _, j := range journals {
		if k, ok := keys[pin.legalEntityID+"|"+j.Currency]; !ok || k.hash != j.Hash {
			return false, nil
		}
	}
	return true, nil
}

// OpeningPostings lists the opening journals posted for the legacy company of a gl_balances run, in
// the legal entities the caller may see.
func (s *Postgres) OpeningPostings(ctx context.Context, runID string) ([]OpeningPosting, error) {
	var out []OpeningPosting
	err := s.tx(ctx, func(tx pgx.Tx) error {
		run, err := getRun(ctx, tx, runID, false)
		if err != nil {
			return err
		}
		if run.Pack != domain.PackGLBalances {
			return fmt.Errorf("%w: run %s is a %s run, not gl_balances", domain.ErrInvalid, runID, run.Pack)
		}
		posts, _, err := readOpeningPostings(ctx, tx, string(run.SourceSystem), run.SourceCompany, "")
		if err != nil {
			return err
		}
		for _, p := range posts {
			out = append(out, p)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].LegalEntityID+out[i].Currency < out[j].LegalEntityID+out[j].Currency
		})
		return nil
	})
	return out, err
}
