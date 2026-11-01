package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// Problem codes of the journal workspace (README error table).
const (
	CodeBatchNotFound          = "LEDGER_BATCH_NOT_FOUND"
	CodeBatchState             = "LEDGER_BATCH_STATE"
	CodeBatchApprovalRequired  = "LEDGER_BATCH_APPROVAL_REQUIRED"
	CodeSelfApproval           = "LEDGER_SELF_APPROVAL"
	CodeJournalNameInvalid     = "LEDGER_JOURNAL_NAME_INVALID"
	CodeLineLimit              = "LEDGER_LINE_LIMIT"
	CodeVersionConflict        = "LEDGER_VERSION_CONFLICT"
	batchAggregate             = "journal_batch"
	batchListLimit             = 200
	maxLastErrorLength         = 1000
	manualJournalSourceURIRoot = "passage://ledger/journal-batches/"
)

// BatchLine is one line of a manual journal batch (FR-11.9). Amount is a
// decimal string at the currency's minor-unit scale.
type BatchLine struct {
	LineNumber    int               `json:"lineNumber"`
	Account       string            `json:"account"`
	Dimensions    map[string]string `json:"dimensions,omitempty"`
	DebitCredit   string            `json:"debitCredit"`
	Amount        string            `json:"amount"`
	Currency      string            `json:"currency"`
	Text          string            `json:"text,omitempty"`
	SourceLineRef string            `json:"sourceLineRef,omitempty"`
}

// Batch is a manual journal batch with its side totals; Lines is set on
// single-batch reads only. RequiresApproval and SelfApprovalAllowed are the
// current settings of its journal name. Version is the optimistic concurrency
// token (ETag); every change of the batch increments it.
type Batch struct {
	ID                  string      `json:"id"`
	LedgerID            string      `json:"ledgerId"`
	LegalEntityID       string      `json:"legalEntityId"`
	JournalName         string      `json:"journalName"`
	Description         string      `json:"description"`
	PostingDate         string      `json:"postingDate"`
	DocumentDate        *string     `json:"documentDate,omitempty"`
	Currency            string      `json:"currency"`
	Status              string      `json:"status"`
	Version             int         `json:"version"`
	RequiresApproval    bool        `json:"requiresApproval"`
	SelfApprovalAllowed bool        `json:"selfApprovalAllowed"`
	SubmittedBy         string      `json:"submittedBy,omitempty"`
	DecidedBy           string      `json:"decidedBy,omitempty"`
	PostedBy            string      `json:"postedBy,omitempty"`
	PostedAt            *time.Time  `json:"postedAt,omitempty"`
	PostedEntryID       string      `json:"postedEntryId,omitempty"`
	LastError           string      `json:"lastError,omitempty"`
	TotalDebit          string      `json:"totalDebit"`
	TotalCredit         string      `json:"totalCredit"`
	LineCount           int         `json:"lineCount"`
	CreatedBy           string      `json:"createdBy"`
	CreatedAt           time.Time   `json:"createdAt"`
	UpdatedAt           time.Time   `json:"updatedAt"`
	Lines               []BatchLine `json:"lines,omitempty"`
	postKey             string
}

// BatchInput creates a batch.
type BatchInput struct {
	LedgerID     string
	JournalName  string
	Description  string
	PostingDate  time.Time
	DocumentDate *time.Time
	Lines        []BatchLine
}

// BatchPatch edits a batch header and, when Lines is set, replaces its lines.
// ClearDocumentDate removes the document date (posting then uses the posting date).
type BatchPatch struct {
	Description       *string
	PostingDate       *time.Time
	DocumentDate      *time.Time
	ClearDocumentDate bool
	Lines             *[]BatchLine
}

// BatchFilter narrows the batch list; empty fields match everything.
type BatchFilter struct {
	LedgerID    string
	Status      string
	JournalName string
}

// PostBatchInput posts a batch. Version, when > 0, must equal the batch's
// version (If-Match). CanPostDirect is true when the caller holds the duty
// "Post ledger journals without approval", needed to post a validated batch of
// a journal name without workflow (§2.11.17).
type PostBatchInput struct {
	ID                    string
	Version               int
	Key                   string
	Today                 time.Time
	TraceID               string
	CanPostDirect         bool
	CanPostLedgerSpecific bool
}

// batchFingerprint is what the posting idempotency hash covers: the batch
// identity and its content, so the key of one batch cannot replay another.
type batchFingerprint struct {
	BatchID      string      `json:"batchId"`
	PostingDate  string      `json:"postingDate"`
	DocumentDate *string     `json:"documentDate"`
	Description  string      `json:"description"`
	Lines        []BatchLine `json:"lines"`
}

const batchSelect = `select b.id::text, b.ledger_id::text, b.legal_entity_id::text, b.journal_name, b.description,
		b.posting_date::text, b.document_date::text, l.currency::text, b.status, b.version, n.requires_approval,
		n.self_approval_allowed, coalesce(b.submitted_by, ''), coalesce(b.decided_by, ''), coalesce(b.posted_by, ''),
		b.posted_at, coalesce(b.posted_entry_id::text, ''), coalesce(b.last_error, ''), coalesce(b.post_key, ''),
		b.created_by, b.created_at, b.updated_at, t.debit::text, t.credit::text, t.lines
	from journal_batch b
	join ledger l on l.tenant_id = b.tenant_id and l.id = b.ledger_id
	join journal_name n on n.tenant_id = b.tenant_id and n.code = b.journal_name
	cross join lateral (select coalesce(sum(x.amount) filter (where x.debit_credit = 'D'), 0) as debit,
			coalesce(sum(x.amount) filter (where x.debit_credit = 'C'), 0) as credit, count(*)::int as lines
		from journal_batch_line x where x.tenant_id = b.tenant_id and x.batch_id = b.id) t`

func scanBatch(row pgx.Row) (Batch, error) {
	var b Batch
	err := row.Scan(&b.ID, &b.LedgerID, &b.LegalEntityID, &b.JournalName, &b.Description, &b.PostingDate, &b.DocumentDate,
		&b.Currency, &b.Status, &b.Version, &b.RequiresApproval, &b.SelfApprovalAllowed, &b.SubmittedBy, &b.DecidedBy,
		&b.PostedBy, &b.PostedAt, &b.PostedEntryID, &b.LastError, &b.postKey, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt,
		&b.TotalDebit, &b.TotalCredit, &b.LineCount)
	if err != nil {
		return b, err
	}
	b.TotalDebit = fmtMoney(b.TotalDebit, b.Currency)
	b.TotalCredit = fmtMoney(b.TotalCredit, b.Currency)
	return b, nil
}

// fmtMoney renders a decimal string at the currency's minor-unit scale.
func fmtMoney(value, currency string) string {
	d, err := decimal.NewFromString(value)
	if err != nil {
		return value
	}
	return d.StringFixed(money.MinorUnits(currency))
}

func batchStateProblem(err error) error {
	return httpx.NewProblem(http.StatusConflict, CodeBatchState, err.Error())
}

// checkBatchVersion enforces If-Match: want 0 means the caller sent none.
func checkBatchVersion(b Batch, want int) error {
	if want > 0 && b.Version != want {
		return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
			"the journal batch changed since it was read; reload it and try again")
	}
	return nil
}

// toLineInputs converts stored lines into posting input.
func toLineInputs(lines []BatchLine) ([]domain.LineInput, error) {
	out := make([]domain.LineInput, 0, len(lines))
	for _, l := range lines {
		amount, err := decimal.NewFromString(l.Amount)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.LineInput{Account: l.Account, Dimensions: l.Dimensions, Side: l.DebitCredit,
			Amount: amount, Currency: l.Currency, Text: l.Text, SourceLineRef: l.SourceLineRef})
	}
	return out, nil
}

// insertBatchLines writes lines numbered 1..n in input order.
func insertBatchLines(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, batchID string, lines []BatchLine) error {
	batch := &pgx.Batch{}
	for i, l := range lines {
		dims := l.Dimensions
		if dims == nil {
			dims = map[string]string{}
		}
		batch.Queue(`insert into journal_batch_line (tenant_id, batch_id, line_number, legal_entity_id, account, dimensions,
				debit_credit, amount, currency, line_text, source_line_ref)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, nullif($10, ''), nullif($11, ''))`,
			tenantID, batchID, i+1, legalEntityID, l.Account, dims, l.DebitCredit, l.Amount, l.Currency, l.Text, l.SourceLineRef)
	}
	return execBatch(ctx, tx, batch)
}

func loadBatchLines(ctx context.Context, tx pgx.Tx, tenantID, batchID string) ([]BatchLine, error) {
	rows, err := tx.Query(ctx, `select line_number, account, dimensions, debit_credit::text, amount::text, currency::text,
			coalesce(line_text, ''), coalesce(source_line_ref, '')
		from journal_batch_line where tenant_id = $1 and batch_id = $2 order by line_number`, tenantID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BatchLine{}
	for rows.Next() {
		var l BatchLine
		if err := rows.Scan(&l.LineNumber, &l.Account, &l.Dimensions, &l.DebitCredit, &l.Amount, &l.Currency, &l.Text, &l.SourceLineRef); err != nil {
			return nil, err
		}
		if len(l.Dimensions) == 0 {
			l.Dimensions = nil
		}
		l.Amount = fmtMoney(l.Amount, l.Currency)
		out = append(out, l)
	}
	return out, rows.Err()
}

// getBatchTx loads a batch with its lines inside tx; lock takes the row lock
// that serializes every change of the batch.
func getBatchTx(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Batch, error) {
	q := batchSelect + ` where b.tenant_id = $1 and b.id = $2`
	if lock {
		q += ` for update of b`
	}
	b, err := scanBatch(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return b, httpx.NewProblem(http.StatusNotFound, CodeBatchNotFound, "journal batch not found")
	}
	if err != nil {
		return b, err
	}
	b.Lines, err = loadBatchLines(ctx, tx, tenantID, id)
	return b, err
}

// moveBatch sets the batch status, bumps its version and enqueues
// dev.passage.ledger.batch.statuschanged.v1 in the same transaction.
func moveBatch(ctx context.Context, tx pgx.Tx, tenantID string, b *Batch, to, actor string) error {
	if _, err := tx.Exec(ctx, `update journal_batch set status = $3, version = version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, tenantID, b.ID, to); err != nil {
		return err
	}
	from := *b
	b.Status = to
	return batchEvent(ctx, tx, tenantID, from, to, actor, "")
}

// batchEvent enqueues dev.passage.ledger.batch.statuschanged.v1 for b moving to to.
func batchEvent(ctx context.Context, tx pgx.Tx, tenantID string, b Batch, to, actor, postedEntryID string) error {
	return enqueueFor(ctx, tx, batchAggregate, b.ID, ledgerevents.BatchStatusChanged, tenantID, ledgerevents.BatchStatusChangedData{
		BatchID: b.ID, LedgerID: b.LedgerID, JournalName: b.JournalName, From: b.Status, To: to, Actor: actor, PostedEntryID: postedEntryID})
}

// usableJournalName loads the journal name of a new or submitted batch: it
// must exist and be active.
func usableJournalName(ctx context.Context, tx pgx.Tx, tenantID, code string) (*JournalName, error) {
	name, err := loadJournalNameTx(ctx, tx, tenantID, code)
	if err != nil {
		return nil, err
	}
	if name == nil || !name.Active {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeJournalNameInvalid,
			"journal name "+code+" does not exist or is inactive")
	}
	return name, nil
}

func checkLinesLimit(name *JournalName, n int) error {
	if n > name.LinesLimit {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeLineLimit,
			"journal name "+name.Code+" allows at most "+strconv.Itoa(name.LinesLimit)+" lines per batch")
	}
	return nil
}

// withBatchTx runs fn on the locked batch after the If-Match check, then
// returns the batch as committed.
func (s *Postgres) withBatchTx(ctx context.Context, id string, version int,
	fn func(tx pgx.Tx, p tenant.Principal, cur *Batch) error) (Batch, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Batch{}, err
	}
	var out Batch
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getBatchTx(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if err := checkBatchVersion(cur, version); err != nil {
			return err
		}
		if err := fn(tx, p, &cur); err != nil {
			return err
		}
		out, err = getBatchTx(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// CreateBatch stores a new draft batch with its lines. The journal name must be
// active and its lines limit holds. Rule checks run at submit and post.
func (s *Postgres) CreateBatch(ctx context.Context, in BatchInput) (Batch, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Batch{}, err
	}
	var out Batch
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = createBatchTx(ctx, tx, p, in)
		return err
	})
	return out, err
}

// createBatchTx stores a draft batch inside tx; recurring journal generation
// shares it so the batch and its run row commit together.
func createBatchTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in BatchInput) (Batch, error) {
	ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, false)
	if err != nil {
		return Batch{}, err
	}
	name, err := usableJournalName(ctx, tx, p.TenantID, in.JournalName)
	if err != nil {
		return Batch{}, err
	}
	if err := checkLinesLimit(name, len(in.Lines)); err != nil {
		return Batch{}, err
	}
	var docDate *string
	if in.DocumentDate != nil {
		d := in.DocumentDate.Format(time.DateOnly)
		docDate = &d
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into journal_batch (tenant_id, id, ledger_id, legal_entity_id, journal_name, description,
			posting_date, document_date, status, created_by)
		values ($1, $2, $3, $4, $5, $6, $7::text::date, $8::text::date, 'draft', $9)`,
		p.TenantID, id, ledger.ID, ledger.LegalEntityID, name.Code, in.Description, in.PostingDate.Format(time.DateOnly),
		docDate, p.UserID); err != nil {
		return Batch{}, err
	}
	if err := insertBatchLines(ctx, tx, p.TenantID, ledger.LegalEntityID, id, in.Lines); err != nil {
		return Batch{}, err
	}
	return getBatchTx(ctx, tx, p.TenantID, id, false)
}

// GetBatch returns one batch with its lines.
func (s *Postgres) GetBatch(ctx context.Context, id string) (Batch, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return Batch{}, err
	}
	var out Batch
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = getBatchTx(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ListBatches returns the newest batches (headers with totals, no lines).
func (s *Postgres) ListBatches(ctx context.Context, f BatchFilter) ([]Batch, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []Batch{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, batchSelect+`
			where b.tenant_id = $1 and ($2 = '' or b.ledger_id::text = $2) and ($3 = '' or b.status = $3)
				and ($4 = '' or b.journal_name = $4)
			order by b.created_at desc, b.id
			limit $5`, p.TenantID, f.LedgerID, f.Status, f.JournalName, batchListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBatch(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateBatch edits a batch. Any edit returns it to draft (BR-11.10) and clears
// the submit and approval, so the batch is validated (and approved) again.
func (s *Postgres) UpdateBatch(ctx context.Context, id string, version int, in BatchPatch) (Batch, error) {
	return s.withBatchTx(ctx, id, version, func(tx pgx.Tx, p tenant.Principal, cur *Batch) error {
		if !domain.BatchEditable(cur.Status) {
			return batchStateProblem(&domain.StateError{From: cur.Status, To: domain.BatchDraft})
		}
		description, postingDate, documentDate := cur.Description, cur.PostingDate, cur.DocumentDate
		if in.Description != nil {
			description = *in.Description
		}
		if in.PostingDate != nil {
			postingDate = in.PostingDate.Format(time.DateOnly)
		}
		if in.DocumentDate != nil {
			d := in.DocumentDate.Format(time.DateOnly)
			documentDate = &d
		}
		if in.ClearDocumentDate {
			documentDate = nil
		}
		if in.Lines != nil {
			name, err := loadJournalNameTx(ctx, tx, p.TenantID, cur.JournalName)
			if err != nil {
				return err
			}
			if err := checkLinesLimit(name, len(*in.Lines)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from journal_batch_line where tenant_id = $1 and batch_id = $2`, p.TenantID, cur.ID); err != nil {
				return err
			}
			if err := insertBatchLines(ctx, tx, p.TenantID, cur.LegalEntityID, cur.ID, *in.Lines); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update journal_batch set description = $3, posting_date = $4::text::date,
				document_date = $5::text::date, submitted_by = null, decided_by = null, last_error = null,
				version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, cur.ID, description, postingDate, documentDate); err != nil {
			return err
		}
		if cur.Status != domain.BatchDraft {
			return moveBatch(ctx, tx, p.TenantID, cur, domain.BatchDraft, p.UserID)
		}
		return nil
	})
}

// SubmitBatch validates every line against the ledger's posting rules (the
// same checks as posting, with the journal name's reconciliation override). A
// batch with violations stays where it is and the violations are returned
// (422). A valid batch moves to validated, and on to in_approval when its
// journal name has a workflow.
func (s *Postgres) SubmitBatch(ctx context.Context, id string, version int, today time.Time) (Batch, error) {
	return s.withBatchTx(ctx, id, version, func(tx pgx.Tx, p tenant.Principal, cur *Batch) error {
		name, err := usableJournalName(ctx, tx, p.TenantID, cur.JournalName)
		if err != nil {
			return err
		}
		if err := domain.CheckSubmit(cur.Status, name.RequiresApproval); err != nil {
			return batchStateProblem(err)
		}
		in, err := batchPostInput(*cur, name)
		if err != nil {
			return err
		}
		ledger, err := loadLedger(ctx, tx, p.TenantID, cur.LedgerID, false)
		if err != nil {
			return err
		}
		accounts, err := loadAccounts(ctx, tx, p.TenantID, ledger.ChartCode, accountNumbers(in.Lines))
		if err != nil {
			return err
		}
		// The same stages as posting: derivation (FR-11.22) runs before structure validation,
		// so a dimension the active strategy fills is not reported missing.
		lines, _, err := applyDerivation(ctx, tx, p.TenantID, ledger, accounts, in.PostingDate, in.Lines)
		if err != nil {
			return err
		}
		lines, _, splitViol, err := applySplitting(ctx, tx, p.TenantID, ledger, accounts, in.Source.Type, lines)
		if err != nil {
			return err
		}
		rules, err := withStructures(ctx, tx, p.TenantID, ledger.ID, in.rules(ledger), lines)
		if err != nil {
			return err
		}
		res := domain.ValidateEntry(rules, accounts, in.PostingDate, today, lines)
		res.Violations = append(res.Violations, splitViol...)
		outcome, err := applyValidationRules(ctx, tx, p.TenantID, ledger, in, accounts, lines)
		if err != nil {
			return err
		}
		if err := rejectWithPeriod(ctx, ledger, in.PostingDate, in.Source.System, append(res.Violations, outcome.Errors...)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update journal_batch set submitted_by = $3, decided_by = null, last_error = null
			where tenant_id = $1 and id = $2`, p.TenantID, cur.ID, p.UserID); err != nil {
			return err
		}
		if cur.Status == domain.BatchDraft {
			if err := moveBatch(ctx, tx, p.TenantID, cur, domain.BatchValidated, p.UserID); err != nil {
				return err
			}
		}
		if name.RequiresApproval {
			return moveBatch(ctx, tx, p.TenantID, cur, domain.BatchInApproval, p.UserID)
		}
		return nil
	})
}

// DecideBatch approves (in_approval -> approved) or rejects (in_approval ->
// rejected) a batch. The approver may not be the submitter unless the journal
// name allows self-approval (403 LEDGER_SELF_APPROVAL).
func (s *Postgres) DecideBatch(ctx context.Context, id string, version int, approve bool) (Batch, error) {
	return s.withBatchTx(ctx, id, version, func(tx pgx.Tx, p tenant.Principal, cur *Batch) error {
		to := domain.BatchRejected
		if approve {
			to = domain.BatchApproved
		}
		if err := domain.CheckTransition(cur.Status, to); err != nil {
			return batchStateProblem(err)
		}
		if approve {
			if err := domain.CheckApproval(cur.SubmittedBy, p.UserID, cur.SelfApprovalAllowed); err != nil {
				return httpx.NewProblem(http.StatusForbidden, CodeSelfApproval,
					"the user who submitted the batch may not approve it under journal name "+cur.JournalName)
			}
		}
		if _, err := tx.Exec(ctx, `update journal_batch set decided_by = $3 where tenant_id = $1 and id = $2`,
			p.TenantID, cur.ID, p.UserID); err != nil {
			return err
		}
		return moveBatch(ctx, tx, p.TenantID, cur, to, p.UserID)
	})
}

// CancelBatch cancels a draft, validated or rejected batch (terminal).
func (s *Postgres) CancelBatch(ctx context.Context, id string, version int) (Batch, error) {
	return s.withBatchTx(ctx, id, version, func(tx pgx.Tx, p tenant.Principal, cur *Batch) error {
		if err := domain.CheckTransition(cur.Status, domain.BatchCancelled); err != nil {
			return batchStateProblem(err)
		}
		return moveBatch(ctx, tx, p.TenantID, cur, domain.BatchCancelled, p.UserID)
	})
}

// batchPostInput is the posting request a batch makes: one manual journal
// entry whose document is the batch (FR-11.12 source object).
func batchPostInput(b Batch, name *JournalName) (PostInput, error) {
	postingDate, err := time.Parse(time.DateOnly, b.PostingDate)
	if err != nil {
		return PostInput{}, err
	}
	docDate := postingDate
	if b.DocumentDate != nil {
		if docDate, err = time.Parse(time.DateOnly, *b.DocumentDate); err != nil {
			return PostInput{}, err
		}
	}
	lines, err := toLineInputs(b.Lines)
	if err != nil {
		return PostInput{}, err
	}
	return PostInput{LedgerID: b.LedgerID, DocumentID: b.ID, PostingDate: postingDate, DocumentDate: &docDate,
		Description: b.Description, Lines: lines, AllowReconAccounts: name.AllowReconAccounts,
		Source: ledgerevents.Source{System: domain.ManualSourceSystem, Type: domain.ManualSourceType, ID: b.ID,
			URI: manualJournalSourceURIRoot + b.ID}}, nil
}

func batchHash(b Batch) ([]byte, error) {
	return domain.HashPayload(batchFingerprint{BatchID: b.ID, PostingDate: b.PostingDate, DocumentDate: b.DocumentDate,
		Description: b.Description, Lines: b.Lines})
}

// PostBatch posts a batch as one journal entry (BR-11.10). The batch row lock
// is the posting lock: the entry is written through the posting pipeline and
// the batch marked posted in one transaction, so a batch is posted exactly when
// its entry exists. On failure the transaction rolls back, the batch stays
// approved (or validated) and the cause is kept in lastError. A repeat with the
// same Idempotency-Key on the posted batch replays the stored response.
func (s *Postgres) PostBatch(ctx context.Context, in PostBatchInput) (EntryResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return EntryResult{}, err
	}
	var out EntryResult
	attempted := false
	s.warmPeriods(ctx, warmBatchQuery, in.ID, domain.ManualSourceSystem)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := getBatchTx(ctx, tx, p.TenantID, in.ID, true)
		if err != nil {
			return err
		}
		hash, err := batchHash(cur)
		if err != nil {
			return err
		}
		if cur.Status == domain.BatchPosted {
			if cur.postKey != in.Key {
				return batchStateProblem(errors.New("journal batch is already posted"))
			}
			replayed, err := replayIdempotent(ctx, tx, p.TenantID, cur.LegalEntityID, in.Key, hash, &out)
			if err != nil {
				return err
			}
			if !replayed { // the stored response expired; answer from the journal
				out = EntryResult{Idempotency: Idempotency{Key: in.Key, Replayed: true}, TraceID: in.TraceID}
				entry, err := loadEntryTx(ctx, tx, p.TenantID, cur.PostedEntryID)
				if err != nil {
					return err
				}
				out.Entries = []Entry{entry}
			}
			return nil
		}
		if err := checkBatchVersion(cur, in.Version); err != nil {
			return err
		}
		name, err := loadJournalNameTx(ctx, tx, p.TenantID, cur.JournalName)
		if err != nil {
			return err
		}
		if err := domain.CheckPost(cur.Status, name.RequiresApproval); err != nil {
			if errors.Is(err, domain.ErrApprovalRequired) {
				return httpx.NewProblem(http.StatusConflict, CodeBatchApprovalRequired,
					"journal name "+cur.JournalName+" requires approval; submit the batch for approval")
			}
			return batchStateProblem(err)
		}
		if cur.Status == domain.BatchValidated && !in.CanPostDirect {
			return httpx.NewProblem(http.StatusForbidden, "AUTH-0003",
				"posting a batch without approval needs duty "+DutyPostDirect)
		}
		post, err := batchPostInput(cur, name)
		if err != nil {
			return err
		}
		post.Key, post.Hash, post.Actor, post.TraceID, post.Today = in.Key, hash, p.UserID, in.TraceID, in.Today
		attempted = true
		post.CanPostLedgerSpecific = in.CanPostLedgerSpecific
		res, err := postEntryTx(ctx, tx, p.TenantID, post)
		if err != nil {
			return err
		}
		entryID := res.Entries[0].ID
		if _, err := tx.Exec(ctx, `update journal_batch set status = $3, posted_entry_id = $4, post_key = $5, posted_by = $6,
				posted_at = now(), last_error = null, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, cur.ID, domain.BatchPosted, entryID, in.Key, p.UserID); err != nil {
			return err
		}
		out = res
		return batchEvent(ctx, tx, p.TenantID, cur, domain.BatchPosted, p.UserID, entryID)
	})
	err = idempotencyRace(err)
	if err != nil && attempted {
		s.recordPostFailure(ctx, p.TenantID, in.ID, err)
	}
	return out, err
}

// DutyPostDirect is "Post ledger journals without approval" (§2.11.17).
const DutyPostDirect = "ledger.journal.postdirect"

// DutyPostLedgerSpecific is "Post to a ledger-specific ledger" (FB50L): posting to one
// non-leading ledger of a ledger set instead of through the set.
const DutyPostLedgerSpecific = "ledger.journal.postledger"

// recordPostFailure keeps why posting failed on the batch, which stays in the
// state it was posted from (BR-11.10: back to approved with error). It is
// best-effort: the caller already returns the posting error itself.
func (s *Postgres) recordPostFailure(ctx context.Context, tenantID, id string, cause error) {
	msg := cause.Error()
	var rej *domain.RejectedError
	if errors.As(cause, &rej) {
		parts := make([]string, 0, len(rej.Violations))
		for _, v := range rej.Violations {
			parts = append(parts, v.Code+": "+v.Message)
		}
		msg = strings.Join(parts, "; ")
	}
	if r := []rune(msg); len(r) > maxLastErrorLength {
		msg = string(r[:maxLastErrorLength])
	}
	_ = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update journal_batch set last_error = $3 where tenant_id = $1 and id = $2`, tenantID, id, msg)
		return err
	})
}
