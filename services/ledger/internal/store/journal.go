package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// writeLine is one line ready to insert.
type writeLine struct {
	id               string
	valid            domain.ValidLine
	reversalOfLineID *string
}

// newEntry is everything writeEntry needs to insert one entry.
type newEntry struct {
	ledger       Ledger
	documentID   string
	postingDate  time.Time
	documentDate *time.Time
	description  string
	source       ledgerevents.Source
	key          string
	hash         []byte
	actor        string
	traceID      string
	reversalOf   *string
	lines        []writeLine
}

// PostEntry validates and posts one entry atomically. The ledger row lock taken
// first serializes posting per ledger, so the gap-free posting_sequence and the
// idempotency check are race-free.
func (s *Postgres) PostEntry(ctx context.Context, in PostInput) (EntryResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return EntryResult{}, err
	}
	var out EntryResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
		if err != nil {
			return err
		}
		replayed, err := replayIdempotent(ctx, tx, p.TenantID, ledger.LegalEntityID, in.Key, in.Hash, &out)
		if err != nil || replayed {
			return err
		}
		accounts, err := loadAccounts(ctx, tx, p.TenantID, ledger.ChartCode, accountNumbers(in.Lines))
		if err != nil {
			return err
		}
		res := domain.ValidateEntry(in.rules(ledger), accounts, in.PostingDate, in.Today, in.Lines)
		if !res.Valid() {
			return &domain.RejectedError{Violations: res.Violations}
		}
		lines := make([]writeLine, 0, len(res.Lines))
		for _, v := range res.Lines {
			lines = append(lines, writeLine{id: ids.New(), valid: v})
		}
		entry, err := writeEntry(ctx, tx, p.TenantID, newEntry{
			ledger: ledger, documentID: in.DocumentID, postingDate: in.PostingDate, documentDate: in.DocumentDate,
			description: in.Description, source: in.Source, key: in.Key, hash: in.Hash, actor: in.Actor,
			traceID: in.TraceID, lines: lines,
		})
		if err != nil {
			return err
		}
		out = EntryResult{Entries: []Entry{entry}, Idempotency: Idempotency{Key: in.Key}, TraceID: in.TraceID}
		return storeIdempotent(ctx, tx, p.TenantID, ledger.LegalEntityID, in.Key, in.Hash, out)
	})
	return out, idempotencyRace(err)
}

// ReverseEntry posts an exact negating entry for a posted entry (FR-11.8). An
// entry can be reversed once; the unique reversal_of_entry_id enforces it.
func (s *Postgres) ReverseEntry(ctx context.Context, in ReverseInput) (EntryResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return EntryResult{}, err
	}
	var out EntryResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var ledgerID string
		err := tx.QueryRow(ctx, `select ledger_id from journal_entry where tenant_id = $1 and id = $2`, p.TenantID, in.EntryID).Scan(&ledgerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "LEDGER_ENTRY_NOT_FOUND", "journal entry not found")
		}
		if err != nil {
			return err
		}
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		replayed, err := replayIdempotent(ctx, tx, p.TenantID, ledger.LegalEntityID, in.Key, in.Hash, &out)
		if err != nil || replayed {
			return err
		}
		var orig struct {
			voucher, postingDate, documentID string
		}
		if err := tx.QueryRow(ctx, `select voucher, posting_date::text, document_id::text from journal_entry where tenant_id = $1 and id = $2`,
			p.TenantID, in.EntryID).Scan(&orig.voucher, &orig.postingDate, &orig.documentID); err != nil {
			return err
		}
		var reversingVoucher string
		err = tx.QueryRow(ctx, `select voucher from journal_entry where tenant_id = $1 and reversal_of_entry_id = $2`,
			p.TenantID, in.EntryID).Scan(&reversingVoucher)
		if err == nil {
			return &domain.RejectedError{Violations: []domain.Violation{{Code: "LEDGER_REVERSAL_ALREADY_REVERSED",
				Field: "id", Message: fmt.Sprintf("entry %s was already reversed by %s", orig.voucher, reversingVoucher)}}}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		origDate, err := time.Parse(time.DateOnly, orig.postingDate)
		if err != nil {
			return err
		}
		postingDate := origDate
		if in.PostingDate != nil {
			postingDate = *in.PostingDate
		}
		if v := domain.CheckReversalDate(ledger.Rules(), origDate, postingDate, in.Today); len(v) > 0 {
			return &domain.RejectedError{Violations: v}
		}
		lines, err := reversalLines(ctx, tx, p.TenantID, in.EntryID)
		if err != nil {
			return err
		}
		description := in.Description
		if description == "" {
			description = "Reversal of " + orig.voucher
		}
		origID := in.EntryID
		entry, err := writeEntry(ctx, tx, p.TenantID, newEntry{
			ledger: ledger, documentID: orig.documentID, postingDate: postingDate, description: description,
			source: ledgerevents.Source{System: "ledger", Type: "reversal", ID: orig.voucher, URI: "passage://ledger/journal-entries/" + origID},
			key:    in.Key, hash: in.Hash, actor: in.Actor, traceID: in.TraceID, reversalOf: &origID, lines: lines,
		})
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, origID, ledgerevents.JournalReversed, p.TenantID, ledgerevents.JournalReversedData{
			OriginalEntryID: origID, ReversingEntryID: entry.ID, LedgerID: ledger.ID, PostingDate: entry.PostingDate,
		}); err != nil {
			return err
		}
		out = EntryResult{Entries: []Entry{entry}, Idempotency: Idempotency{Key: in.Key}, TraceID: in.TraceID}
		return storeIdempotent(ctx, tx, p.TenantID, ledger.LegalEntityID, in.Key, in.Hash, out)
	})
	return out, idempotencyRace(err)
}

// reversalLines loads the original lines with swapped sides and the same
// accounts, combinations and amounts.
func reversalLines(ctx context.Context, tx pgx.Tx, tenantID, entryID string) ([]writeLine, error) {
	rows, err := tx.Query(ctx, `select l.id, l.main_account_id, a.account_number, l.dimension_combination_id, l.debit_credit, l.amount_acct::text,
			coalesce(l.line_text, ''), coalesce(l.source_line_ref, ''), c.display_name, c.dimensions
		from journal_line l
		join main_account a on a.tenant_id = l.tenant_id and a.id = l.main_account_id
		join dimension_combination c on c.tenant_id = l.tenant_id and c.id = l.dimension_combination_id
		where l.tenant_id = $1 and l.entry_id = $2 order by l.line_number`, tenantID, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []writeLine
	for rows.Next() {
		var origID, accountID, accountNumber, side, amountText, text, ref, display string
		var combo []byte
		var dims map[string]string
		if err := rows.Scan(&origID, &accountID, &accountNumber, &combo, &side, &amountText, &text, &ref, &display, &dims); err != nil {
			return nil, err
		}
		amount, err := decimal.NewFromString(amountText)
		if err != nil {
			return nil, err
		}
		orig := origID
		out = append(out, writeLine{id: ids.New(), reversalOfLineID: &orig, valid: domain.ValidLine{
			Index: len(out), Account: domain.Account{ID: accountID, Number: accountNumber}, Dimensions: dims, Side: domain.ReversalSide(side),
			Amount: amount, Text: text, SourceLineRef: ref, CombinationID: combo, DisplayName: display,
		}})
	}
	return out, rows.Err()
}

// writeEntry inserts an entry, its lines, new dimension combinations and the
// balance projection deltas, and enqueues journal.posted. It runs inside the
// caller's transaction, after the ledger row lock.
func writeEntry(ctx context.Context, tx pgx.Tx, tenantID string, ne newEntry) (Entry, error) {
	l := ne.ledger
	seq := l.NextSequence
	if _, err := tx.Exec(ctx, `update ledger set next_sequence = next_sequence + 1 where tenant_id = $1 and id = $2`, tenantID, l.ID); err != nil {
		return Entry{}, err
	}
	voucher := fmt.Sprintf("%s-%07d", l.Code, seq)
	entryID := ids.New()
	period := domain.PeriodKey(ne.postingDate)
	postingDate := ne.postingDate.Format(time.DateOnly)
	docDate := ""
	if ne.documentDate != nil {
		docDate = ne.documentDate.Format(time.DateOnly)
	}
	if _, err := tx.Exec(ctx, `insert into journal_entry (tenant_id, id, legal_entity_id, ledger_id, document_id, voucher, posting_sequence, posting_date, period_key,
			document_date, description, source_system, source_type, source_id, source_uri, idempotency_key, payload_hash, reversal_of_entry_id, posted_by, trace_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8::text::date, $9, nullif($10::text, '')::date, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		tenantID, entryID, l.LegalEntityID, l.ID, ne.documentID, voucher, seq, postingDate, period, docDate, ne.description,
		ne.source.System, ne.source.Type, ne.source.ID, ne.source.URI, ne.key, ne.hash, ne.reversalOf, ne.actor, ne.traceID); err != nil {
		return Entry{}, err
	}

	batch := &pgx.Batch{}
	for _, wl := range ne.lines {
		dims := wl.valid.Dimensions
		if dims == nil {
			dims = map[string]string{}
		}
		raw, err := json.Marshal(dims)
		if err != nil {
			return Entry{}, err
		}
		batch.Queue(`insert into dimension_combination (tenant_id, id, main_account_id, display_name, dimensions) values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, id) do nothing`, tenantID, wl.valid.CombinationID, wl.valid.Account.ID, wl.valid.DisplayName, string(raw))
	}
	for i, wl := range ne.lines {
		batch.Queue(`insert into journal_line (tenant_id, id, legal_entity_id, entry_id, line_number, main_account_id, dimension_combination_id,
				debit_credit, currency_txn, amount_txn, amount_acct, negative_posting, line_text, source_line_ref, reversal_of_line_id)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::text::numeric, $10::text::numeric, false, nullif($11, ''), nullif($12, ''), $13)`,
			tenantID, wl.id, l.LegalEntityID, entryID, i+1, wl.valid.Account.ID, wl.valid.CombinationID,
			wl.valid.Side, l.Currency, wl.valid.Amount.String(), wl.valid.Text, wl.valid.SourceLineRef, wl.reversalOfLineID)
	}
	valid := make([]domain.ValidLine, 0, len(ne.lines))
	for _, wl := range ne.lines {
		valid = append(valid, wl.valid)
	}
	for _, d := range domain.AggregateBalances(period, valid) {
		batch.Queue(`insert into balance (tenant_id, legal_entity_id, ledger_id, main_account_id, dimension_combination_id, period_key, currency,
				debit, credit, net, line_count, last_posting_sequence)
			values ($1, $2, $3, $4, $5, $6, $7, $8::text::numeric, $9::text::numeric, $10::text::numeric, $11, $12)
			on conflict (tenant_id, ledger_id, main_account_id, dimension_combination_id, period_key, currency_type) do update set
				debit = balance.debit + excluded.debit, credit = balance.credit + excluded.credit, net = balance.net + excluded.net,
				line_count = balance.line_count + excluded.line_count,
				last_posting_sequence = greatest(balance.last_posting_sequence, excluded.last_posting_sequence)`,
			tenantID, l.LegalEntityID, l.ID, d.MainAccountID, d.Combination, d.PeriodKey, l.Currency,
			d.Debit.String(), d.Credit.String(), domain.Net(d.Debit, d.Credit).String(), d.LineCount, seq)
	}
	if batch.Len() > 0 {
		if err := execBatch(ctx, tx, batch); err != nil {
			return Entry{}, err
		}
	}

	entry := Entry{
		ID: entryID, LedgerID: l.ID, DocumentID: ne.documentID, Voucher: voucher, PostingSequence: seq,
		PostingDate: postingDate, PeriodKey: period, Description: ne.description, Source: ne.source,
		ReversalOfEntryID: ne.reversalOf, PostedAt: time.Now().UTC(), PostedBy: ne.actor, TraceID: ne.traceID,
	}
	if docDate != "" {
		entry.DocumentDate = &docDate
	}
	for i, wl := range ne.lines {
		entry.Lines = append(entry.Lines, lineView(wl, i+1, l.Currency))
	}
	entry.Totals = totals(l.Currency, entry.Lines)
	if err := enqueue(ctx, tx, entryID, ledgerevents.JournalPosted, tenantID, ledgerevents.JournalPostedData{
		EntryID: entryID, LedgerID: l.ID, DocumentID: ne.documentID, Voucher: voucher, PostingSequence: seq,
		PostingDate: postingDate, PeriodKey: period, LineCount: len(ne.lines), Totals: entry.Totals,
		Source: ne.source, TraceID: ne.traceID,
	}); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func execBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch) error {
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

func lineView(wl writeLine, number int, currency string) Line {
	v := wl.valid
	return Line{
		ID: wl.id, LineNumber: number, Account: v.Account.Number, DimensionCombinationID: hex.EncodeToString(v.CombinationID),
		DisplayName: v.DisplayName, Dimensions: v.Dimensions, DebitCredit: v.Side,
		Amount: v.Amount.StringFixed(money.MinorUnits(currency)), Text: v.Text, SourceLineRef: v.SourceLineRef,
		ReversalOfLineID: wl.reversalOfLineID,
	}
}

// totals sums debit and credit lines in the ledger currency.
func totals(currency string, lines []Line) ledgerevents.Amounts {
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range lines {
		amount := decimal.RequireFromString(l.Amount)
		if l.DebitCredit == domain.SideCredit {
			credit = credit.Add(amount)
		} else {
			debit = debit.Add(amount)
		}
	}
	minor := money.MinorUnits(currency)
	return ledgerevents.Amounts{Currency: currency, Debit: debit.StringFixed(minor), Credit: credit.StringFixed(minor)}
}

// GetEntry returns one entry with its lines.
func (s *Postgres) GetEntry(ctx context.Context, id string) (Entry, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `select e.id, e.ledger_id, e.document_id, e.voucher, e.posting_sequence, e.posting_date::text, e.period_key,
				e.document_date::text, coalesce(e.description, ''), e.source_system, e.source_type, e.source_id, coalesce(e.source_uri, ''),
				e.reversal_of_entry_id::text, e.posted_at, e.posted_by, e.trace_id, l.currency
			from journal_entry e join ledger l on l.tenant_id = e.tenant_id and l.id = e.ledger_id
			where e.tenant_id = $1 and e.id = $2`, p.TenantID, id)
		var currency string
		var src ledgerevents.Source
		err := row.Scan(&e.ID, &e.LedgerID, &e.DocumentID, &e.Voucher, &e.PostingSequence, &e.PostingDate, &e.PeriodKey,
			&e.DocumentDate, &e.Description, &src.System, &src.Type, &src.ID, &src.URI, &e.ReversalOfEntryID, &e.PostedAt,
			&e.PostedBy, &e.TraceID, &currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, "LEDGER_ENTRY_NOT_FOUND", "journal entry not found")
		}
		if err != nil {
			return err
		}
		e.Source = src
		e.Lines, err = loadLines(ctx, tx, p.TenantID, id, currency)
		if err != nil {
			return err
		}
		e.Totals = totals(currency, e.Lines)
		return nil
	})
	return e, err
}

func loadLines(ctx context.Context, tx pgx.Tx, tenantID, entryID, currency string) ([]Line, error) {
	rows, err := tx.Query(ctx, `select l.id, l.line_number, a.account_number, l.dimension_combination_id, c.display_name, c.dimensions,
			l.debit_credit, l.amount_acct::text, coalesce(l.line_text, ''), coalesce(l.source_line_ref, ''), l.reversal_of_line_id::text
		from journal_line l
		join main_account a on a.tenant_id = l.tenant_id and a.id = l.main_account_id
		join dimension_combination c on c.tenant_id = l.tenant_id and c.id = l.dimension_combination_id
		where l.tenant_id = $1 and l.entry_id = $2 order by l.line_number`, tenantID, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var ln Line
		var combo []byte
		var amountText string
		if err := rows.Scan(&ln.ID, &ln.LineNumber, &ln.Account, &combo, &ln.DisplayName, &ln.Dimensions, &ln.DebitCredit,
			&amountText, &ln.Text, &ln.SourceLineRef, &ln.ReversalOfLineID); err != nil {
			return nil, err
		}
		amount, err := decimal.NewFromString(amountText)
		if err != nil {
			return nil, err
		}
		ln.DimensionCombinationID = hex.EncodeToString(combo)
		ln.Amount = amount.StringFixed(money.MinorUnits(currency))
		out = append(out, ln)
	}
	return out, rows.Err()
}

// ValidateEntry runs the posting checks without writing anything (POST /ledger/validate).
func (s *Postgres) ValidateEntry(ctx context.Context, in PostInput) (domain.Result, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return domain.Result{}, err
	}
	var res domain.Result
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, false)
		if err != nil {
			return err
		}
		accounts, err := loadAccounts(ctx, tx, p.TenantID, ledger.ChartCode, accountNumbers(in.Lines))
		if err != nil {
			return err
		}
		res = domain.ValidateEntry(in.rules(ledger), accounts, in.PostingDate, in.Today, in.Lines)
		return nil
	})
	return res, err
}
