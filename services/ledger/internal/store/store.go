// Package store is the ledger's Postgres layer. Every method runs inside
// dbx.WithTenantTx so row-level security applies; posting serializes per ledger
// by locking the ledger row, which also assigns the gap-free posting sequence.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// MaxPageSize bounds keyset pages (FR-11.14).
const MaxPageSize = 1000

const uniqueViolation = "23505"

// Store is the persistence contract the HTTP layer depends on.
type Store interface {
	PutAccount(ctx context.Context, chartCode, number string, in Account) (Account, bool, error)
	ListAccounts(ctx context.Context, chartCode, afterNumber string, limit int) ([]Account, error)
	CreateLedger(ctx context.Context, in Ledger) (Ledger, error)
	GetLedger(ctx context.Context, id string) (Ledger, error)
	PostEntry(ctx context.Context, in PostInput) (EntryResult, error)
	ReverseEntry(ctx context.Context, in ReverseInput) (EntryResult, error)
	GetEntry(ctx context.Context, id string) (Entry, error)
	ValidateEntry(ctx context.Context, in PostInput) (domain.Result, error)
	TrialBalance(ctx context.Context, f BalanceFilter) (TrialBalance, error)
	RebuildBalances(ctx context.Context, ledgerID string) (RebuildResult, error)
	ListReasonCodes(ctx context.Context) ([]ReasonCode, error)
	GetReasonCode(ctx context.Context, code string) (ReasonCode, error)
	PutReasonCode(ctx context.Context, code string, in ReasonCode) (ReasonCode, bool, error)
	ListDescriptionTemplates(ctx context.Context) ([]DescriptionTemplate, error)
	GetDescriptionTemplate(ctx context.Context, id string) (DescriptionTemplate, error)
	PutDescriptionTemplate(ctx context.Context, id string, in DescriptionTemplate) (DescriptionTemplate, bool, error)
	ListStructures(ctx context.Context, ledgerID string) ([]domain.Structure, error)
	GetStructure(ctx context.Context, ledgerID, id string) (domain.Structure, error)
	PutStructure(ctx context.Context, ledgerID, id string, in domain.Structure) (domain.Structure, bool, error)
	ListDimensionValues(ctx context.Context, ledgerID string) ([]domain.DimensionValue, error)
	PutDimensionValue(ctx context.Context, ledgerID string, in domain.DimensionValue) (domain.DimensionValue, bool, error)
	ListJournalNames(ctx context.Context) ([]JournalName, error)
	GetJournalName(ctx context.Context, code string) (JournalName, error)
	PutJournalName(ctx context.Context, code string, in JournalName) (JournalName, bool, error)
	CreateBatch(ctx context.Context, in BatchInput) (Batch, error)
	GetBatch(ctx context.Context, id string) (Batch, error)
	ListBatches(ctx context.Context, f BatchFilter) ([]Batch, error)
	UpdateBatch(ctx context.Context, id string, version int, in BatchPatch) (Batch, error)
	SubmitBatch(ctx context.Context, id string, version int, today time.Time) (Batch, error)
	DecideBatch(ctx context.Context, id string, version int, approve bool) (Batch, error)
	CancelBatch(ctx context.Context, id string, version int) (Batch, error)
	PostBatch(ctx context.Context, in PostBatchInput) (EntryResult, error)
	ListLines(ctx context.Context, f LineFilter) (LinePage, error)
	ListRecurring(ctx context.Context, status string) ([]RecurringJournal, error)
	GetRecurring(ctx context.Context, id string) (RecurringJournal, error)
	PutRecurring(ctx context.Context, id string, version int, in RecurringInput) (RecurringJournal, bool, error)
	SetRecurringStatus(ctx context.Context, id string, version int, status string, today time.Time) (RecurringJournal, error)
	GenerateRecurring(ctx context.Context, in GenerateRecurringInput) (RecurringRun, bool, error)
	ListRecurringRuns(ctx context.Context, id string) ([]RecurringRun, error)
	CreateLedgerSet(ctx context.Context, in LedgerSetInput) (LedgerSet, error)
	GetLedgerSet(ctx context.Context, id string) (LedgerSet, error)
	CreateAccountMapping(ctx context.Context, in AccountMapping) (AccountMapping, error)
	PostSetEntry(ctx context.Context, setID string, in PostInput) (EntryResult, error)
	ListOpenItemAccounts(ctx context.Context, ledgerID string) ([]OpenItemAccount, error)
	PutOpenItemAccount(ctx context.Context, ledgerID, number string, enabled bool) (OpenItemAccount, bool, error)
	ListToleranceGroups(ctx context.Context, ledgerID string) ([]ToleranceGroup, error)
	GetToleranceGroup(ctx context.Context, ledgerID, code string) (ToleranceGroup, error)
	PutToleranceGroup(ctx context.Context, ledgerID, code string, version int, in ToleranceGroup) (ToleranceGroup, bool, error)
	ListOpenItems(ctx context.Context, f OpenItemFilter) (OpenItemPage, error)
	SimulateClearing(ctx context.Context, in ClearingInput) (ClearingPreview, error)
	ClearItems(ctx context.Context, in ClearingInput) (ClearingDocument, bool, error)
	ListClearings(ctx context.Context, ledgerID, status string) ([]ClearingDocument, error)
	GetClearing(ctx context.Context, id string) (ClearingDocument, error)
	ResetClearing(ctx context.Context, in ResetInput) (ClearingDocument, error)
	ListAutoClearingRules(ctx context.Context, ledgerID string) ([]AutoClearingRule, error)
	GetAutoClearingRule(ctx context.Context, ledgerID, id string) (AutoClearingRule, error)
	PutAutoClearingRule(ctx context.Context, ledgerID, id string, version int, in AutoClearingRule) (AutoClearingRule, bool, error)
	RunAutoClearing(ctx context.Context, in AutoClearingInput) (AutoClearingRun, bool, error)
	ListAutoClearingRuns(ctx context.Context, ledgerID string) ([]AutoClearingRun, error)
	GetAutoClearingRun(ctx context.Context, id string) (AutoClearingRun, error)
	ListLookupTables(ctx context.Context, ledgerID string) ([]domain.LookupTable, error)
	GetLookupTable(ctx context.Context, ledgerID, id string) (domain.LookupTable, error)
	PutLookupTable(ctx context.Context, ledgerID, id string, version int, in domain.LookupTable) (domain.LookupTable, bool, error)
	ListDerivationStrategies(ctx context.Context, ledgerID string) ([]domain.DerivationStrategy, error)
	GetDerivationStrategy(ctx context.Context, ledgerID string, version int) (domain.DerivationStrategy, error)
	PutDerivationStrategy(ctx context.Context, ledgerID string, version, lockVersion int, in domain.DerivationStrategy) (domain.DerivationStrategy, bool, error)
	ActivateDerivationStrategy(ctx context.Context, ledgerID string, version, lockVersion int) (domain.DerivationStrategy, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a Postgres-backed store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for the readiness probe.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Account is a main account in a chart.
type Account struct {
	ID            string  `json:"id"`
	ChartCode     string  `json:"chartCode"`
	Number        string  `json:"accountNumber"`
	Name          string  `json:"name"`
	Type          string  `json:"accountType"`
	Postable      bool    `json:"isPostable"`
	SuspendedFrom *string `json:"suspendedFrom,omitempty"`
	ReconType     string  `json:"reconciliationAccountType"`
}

// Ledger is one accounting ledger of a legal entity.
// ReversalReasonRequired makes a reason code mandatory on reversal (FR-11.15);
// NegativePostingEnabled permits reason codes that allow storno postings (BR-11.16).
type Ledger struct {
	ID                     string    `json:"id"`
	LegalEntityID          string    `json:"legalEntityId"`
	Code                   string    `json:"code"`
	Name                   string    `json:"name"`
	ChartCode              string    `json:"chartCode"`
	Currency               string    `json:"currency"`
	DimensionAttributes    []string  `json:"dimensionAttributes"`
	FirstOpenDate          *string   `json:"firstOpenDate,omitempty"`
	FuturePostingDays      int       `json:"futurePostingDays"`
	ReversalReasonRequired bool      `json:"reversalReasonRequired"`
	NegativePostingEnabled bool      `json:"negativePostingEnabled"`
	NextSequence           int64     `json:"-"`
	CreatedAt              time.Time `json:"createdAt"`
}

// Rules returns the posting rules this ledger imposes.
func (l Ledger) Rules() domain.Rules {
	r := domain.Rules{ChartCode: l.ChartCode, Currency: l.Currency, DimensionAttributes: l.DimensionAttributes, FutureDays: l.FuturePostingDays}
	if l.FirstOpenDate != nil {
		if d, err := time.Parse(time.DateOnly, *l.FirstOpenDate); err == nil {
			r.FirstOpenDate = &d
		}
	}
	return r
}

// ReversalPolicy returns the reversal parameters this ledger imposes (FR-11.15, BR-11.16).
func (l Ledger) ReversalPolicy() domain.ReversalPolicy {
	return domain.ReversalPolicy{ReasonRequired: l.ReversalReasonRequired, NegativeEnabled: l.NegativePostingEnabled}
}

// PostInput is one entry to post or validate.
type PostInput struct {
	LedgerID     string
	DocumentID   string
	PostingDate  time.Time
	DocumentDate *time.Time
	Description  string
	Source       ledgerevents.Source
	Lines        []domain.LineInput
	// LedgerLines are extra lines per ledger id, posted by PostSetEntry to that ledger
	// only (no account mapping) on top of the common Lines (BR-12.6 ledger_lines).
	LedgerLines map[string][]domain.LineInput
	Key         string
	Hash        []byte
	Actor       string
	TraceID     string
	Today       time.Time
	// AllowReconAccounts lifts the BR-11.8 protection for a manual journal whose
	// journal name sets allow_recon_accounts (migration and correction use).
	AllowReconAccounts bool
	// CanPostLedgerSpecific is true when the caller holds the duty "Post to a
	// ledger-specific ledger" (FB50L), needed to post to one non-leading ledger of a set.
	CanPostLedgerSpecific bool
}

// rules returns the ledger's posting rules for this input: a manual journal
// source additionally protects reconciliation accounts (BR-11.8), unless its
// journal name allows them.
func (in PostInput) rules(l Ledger) domain.Rules {
	r := l.Rules()
	r.Manual = domain.IsManualSource(in.Source.System, in.Source.Type) && !in.AllowReconAccounts
	return r
}

// ReverseInput is a reversal request for one posted entry.
type ReverseInput struct {
	EntryID       string
	PostingDate   *time.Time
	Description   string
	ReasonCode    string
	ReasonComment string
	Key           string
	Hash          []byte
	Actor         string
	TraceID       string
	Today         time.Time

	CanPostLedgerSpecific bool

	// clearingReset is set by ResetClearing, which reverses its own difference
	// entry while that entry's open item is still cleared by the document.
	clearingReset bool
}

// Idempotency reports the replay state of a keyed request.
type Idempotency struct {
	Key      string `json:"key"`
	Replayed bool   `json:"replayed"`
}

// EntryResult is the response of a posting or reversal.
type EntryResult struct {
	Entries     []Entry     `json:"entries"`
	Idempotency Idempotency `json:"idempotency"`
	TraceID     string      `json:"traceId"`
	Notices     []Notice    `json:"notices,omitempty"`
}

// Notice is an info-severity message on an accepted posting, such as
// LEDGER_DESCRIPTION_TRUNCATED (LEDGER-0031).
type Notice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Entry is a posted journal entry with its lines.
type Entry struct {
	ID                string               `json:"id"`
	LedgerID          string               `json:"ledgerId"`
	DocumentID        string               `json:"documentId"`
	Voucher           string               `json:"voucher"`
	PostingSequence   int64                `json:"postingSequence"`
	PostingDate       string               `json:"postingDate"`
	PeriodKey         string               `json:"periodKey"`
	DocumentDate      *string              `json:"documentDate,omitempty"`
	Description       string               `json:"description"`
	Source            ledgerevents.Source  `json:"source"`
	ReversalOfEntryID *string              `json:"reversalOfEntryId,omitempty"`
	ReasonCode        *string              `json:"reasonCode,omitempty"`
	ReasonComment     *string              `json:"reasonComment,omitempty"`
	PostedAt          time.Time            `json:"postedAt"`
	PostedBy          string               `json:"postedBy"`
	TraceID           string               `json:"traceId"`
	Totals            ledgerevents.Amounts `json:"totals"`
	Lines             []Line               `json:"lines"`
}

// Line is one journal line.
type Line struct {
	ID                     string            `json:"id"`
	LineNumber             int               `json:"lineNumber"`
	Account                string            `json:"account"`
	DimensionCombinationID string            `json:"dimensionCombinationId"`
	DisplayName            string            `json:"displayName"`
	Dimensions             map[string]string `json:"dimensions,omitempty"`
	DebitCredit            string            `json:"debitCredit"`
	Amount                 string            `json:"amount"`
	Text                   string            `json:"text,omitempty"`
	SourceLineRef          string            `json:"sourceLineRef,omitempty"`
	ReversalOfLineID       *string           `json:"reversalOfLineId,omitempty"`
	NegativePosting        bool              `json:"negativePosting,omitempty"`
	// DerivedByStepIDs names the derivation steps that filled dimensions on the line (FR-11.22).
	DerivedByStepIDs []string `json:"derivedByStepIds,omitempty"`
}

// BalanceFilter selects balances for a trial balance.
type BalanceFilter struct {
	LedgerID, PeriodFrom, PeriodTo, AccountFrom, AccountTo string
}

// BalanceRow is one account's period-to-date turnover in the filter.
type BalanceRow struct {
	Account   string `json:"account"`
	Name      string `json:"name"`
	Debit     string `json:"debit"`
	Credit    string `json:"credit"`
	Net       string `json:"net"`
	LineCount int64  `json:"lineCount"`
}

// TrialBalance is the account-level summary with totals.
type TrialBalance struct {
	LedgerID string       `json:"ledgerId"`
	Currency string       `json:"currency"`
	Rows     []BalanceRow `json:"rows"`
	Debit    string       `json:"debit"`
	Credit   string       `json:"credit"`
	Net      string       `json:"net"`
	Balanced bool         `json:"balanced"`
}

// RebuildResult reports a balance rebuild (BR-11.7, simplified: no shadow table).
type RebuildResult struct {
	LedgerID          string `json:"ledgerId"`
	HighWaterSequence int64  `json:"highWaterSequence"`
	RowsWritten       int64  `json:"rowsWritten"`
	RowsDiffering     int64  `json:"rowsDiffering"`
	DurationMs        int64  `json:"durationMs"`
}

func principalOf(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return tenant.Principal{}, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing tenant")
	}
	return p, nil
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateID, eventType, tenantID string, data any) error {
	return enqueueFor(ctx, tx, "journal", aggregateID, eventType, tenantID, data)
}

// enqueueFor writes an event to the outbox under the named aggregate type.
func enqueueFor(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, ledgerevents.EventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// CodeIdempotencyInProgress is the 409 returned while another request holding
// the same Idempotency-Key is still being committed (FR-11.5). The API adds
// Retry-After: 1.
const CodeIdempotencyInProgress = "LEDGER_IDEMPOTENCY_IN_PROGRESS"

// idempotencyRace maps a unique violation on an idempotency key to 409
// LEDGER_IDEMPOTENCY_IN_PROGRESS. Requests on the same ledger serialize on the
// ledger row lock and replay instead; this covers the same key racing on two
// ledgers of one legal entity, where the loser hits the key's primary key.
func idempotencyRace(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && strings.Contains(pgErr.ConstraintName, "idempotency") {
		return httpx.NewProblem(http.StatusConflict, CodeIdempotencyInProgress, "a request with this Idempotency-Key is still in progress; retry shortly")
	}
	return err
}

// replayIdempotent returns true with the stored response when the key was used
// with the same payload; a different payload is a 409 (BR-11.6). Expired keys
// are removed first so the request is treated as new (FR-11.5).
func replayIdempotent(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, key string, hash []byte, out *EntryResult) (bool, error) {
	if _, err := tx.Exec(ctx, `delete from idempotency_keys where tenant_id = $1 and legal_entity_id = $2 and key = $3 and expires_at <= now()`,
		tenantID, legalEntityID, key); err != nil {
		return false, err
	}
	var stored []byte
	var body []byte
	err := tx.QueryRow(ctx, `select payload_hash, response_body from idempotency_keys where tenant_id = $1 and legal_entity_id = $2 and key = $3`,
		tenantID, legalEntityID, key).Scan(&stored, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(stored, hash) {
		return false, httpx.NewProblem(http.StatusConflict, "LEDGER_IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different request body")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return false, err
	}
	out.Idempotency = Idempotency{Key: key, Replayed: true}
	return true, nil
}

// storeIdempotent records a successful response under the key (2xx only).
func storeIdempotent(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, key string, hash []byte, result EntryResult) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, legal_entity_id, key, payload_hash, response_status, response_body)
		values ($1, $2, $3, $4, 201, $5)`, tenantID, legalEntityID, key, hash, string(body))
	return err
}

func loadLedger(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Ledger, error) {
	q := ledgerColumns + ` from ledger where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	l, err := scanLedger(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Ledger{}, httpx.NewProblem(http.StatusNotFound, "LEDGER_NOT_FOUND", "ledger not found")
	}
	return l, err
}

// loadAccounts returns the accounts of chart that appear in numbers, keyed by number.
func loadAccounts(ctx context.Context, tx pgx.Tx, tenantID, chart string, numbers []string) (map[string]domain.Account, error) {
	out := make(map[string]domain.Account, len(numbers))
	if len(numbers) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select id, account_number, is_postable, suspended_from::text, reconciliation_account_type, account_type
		from main_account where tenant_id = $1 and chart_code = $2 and account_number = any($3)`, tenantID, chart, numbers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a domain.Account
		var susp *string
		if err := rows.Scan(&a.ID, &a.Number, &a.Postable, &susp, &a.ReconType, &a.Type); err != nil {
			return nil, err
		}
		if susp != nil {
			if d, err := time.Parse(time.DateOnly, *susp); err == nil {
				a.SuspendedFrom = &d
			}
		}
		out[a.Number] = a
	}
	return out, rows.Err()
}

func accountNumbers(lines []domain.LineInput) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lines {
		if !seen[l.Account] {
			seen[l.Account] = true
			out = append(out, l.Account)
		}
	}
	return out
}

const ledgerFields = `id, legal_entity_id, code, name, chart_code, currency, dimension_attributes, first_open_date::text, future_posting_days, next_sequence, created_at, reversal_reason_required, negative_posting_enabled`

const ledgerColumns = `select ` + ledgerFields

func scanLedger(row pgx.Row) (Ledger, error) {
	var l Ledger
	err := row.Scan(&l.ID, &l.LegalEntityID, &l.Code, &l.Name, &l.ChartCode, &l.Currency, &l.DimensionAttributes,
		&l.FirstOpenDate, &l.FuturePostingDays, &l.NextSequence, &l.CreatedAt, &l.ReversalReasonRequired, &l.NegativePostingEnabled)
	if l.DimensionAttributes == nil {
		l.DimensionAttributes = []string{}
	}
	return l, err
}
