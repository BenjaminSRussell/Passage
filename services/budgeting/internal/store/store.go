// Package store persists budget control rules, register entries, balances and
// encumbrances. Every command runs in one tenant transaction with its outbox
// rows and its Idempotency-Key record.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	budgetevents "github.com/BenjaminSRussell/passage/services/budgeting/internal/events"
)

const (
	CodeRuleNotFound          = "BUD-0001"
	CodeEncumbranceNotFound   = "BUD-0002"
	CodeLineMoved             = "BUD-0003"
	CodeCheckNotFound         = "BUD-0004"
	CodeIdempotencyReuse      = "idempotency_key_reuse"
	CodeIdempotencyInProgress = "idempotency_in_progress"
	uniqueViolation           = "23505"
	idempotencyPrimaryKey     = "idempotency_keys_pkey"
	eventSource               = "budgeting"
	listLimit                 = 500
)

// Rule is one version of a budget control rule (FR-38.3).
type Rule struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	Code          string          `json:"code"`
	Version       int             `json:"version"`
	WarnPct       decimal.Decimal `json:"warnPct"`
	BlockPct      decimal.Decimal `json:"blockPct"`
	Currency      string          `json:"currency"`
	EffectiveFrom string          `json:"effectiveFrom"`
}

// NewRule is the input for a new rule version; the store assigns id and version.
type NewRule struct {
	LegalEntityID string
	Code          string
	WarnPct       decimal.Decimal
	BlockPct      decimal.Decimal
	Currency      string
	EffectiveFrom string
}

// CheckLine is one document line presented for a budget check. The line's
// prior reservation (FR-38.4 step 4) is read from its stored encumbrance, never
// taken from the caller.
type CheckLine struct {
	LineID       string
	DimensionKey string
	SpanKey      string
	Amount       decimal.Decimal
}

// CheckRequest is a budget check of one document against one rule version.
type CheckRequest struct {
	RuleVersionID string
	DocumentID    string
	DocumentType  string
	Lines         []CheckLine
}

// LineResult is the per-line outcome with the trace of steps observed.
type LineResult struct {
	LineID        string          `json:"lineId"`
	DimensionKey  string          `json:"dimensionKey"`
	SpanKey       string          `json:"spanKey"`
	Amount        decimal.Decimal `json:"amount"`
	Prior         decimal.Decimal `json:"prior"`
	Result        domain.Result   `json:"result"`
	Available     decimal.Decimal `json:"available"`
	Net           decimal.Decimal `json:"net"`
	OverPct       decimal.Decimal `json:"overPct"`
	Trace         []string        `json:"trace"`
	EncumbranceID string          `json:"encumbranceId,omitempty"`
}

// CheckResponse is the recorded document-level outcome. The worst line decides.
type CheckResponse struct {
	ID            string        `json:"id"`
	RuleVersionID string        `json:"ruleVersionId"`
	DocumentID    string        `json:"documentId"`
	DocumentType  string        `json:"documentType"`
	Result        domain.Result `json:"result"`
	Overridden    bool          `json:"overridden"`
	CheckedAt     string        `json:"checkedAt"`
	Lines         []LineResult  `json:"lines"`
}

// RelieveRequest relieves an encumbrance from a matched invoice line (BR-38.4).
type RelieveRequest struct {
	EncumbranceID string
	InvoiceID     string
	InvoiceAmount decimal.Decimal
	MatchedShare  decimal.Decimal
}

// RelieveResponse reports the relief and the excess to be checked as new.
type RelieveResponse struct {
	EncumbranceID string          `json:"encumbranceId"`
	Relieved      decimal.Decimal `json:"relieved"`
	Remaining     decimal.Decimal `json:"remaining"`
	Excess        decimal.Decimal `json:"excess"`
	Status        string          `json:"status"`
}

// RegisterRequest posts a budget register entry directly (workflow is a later slice).
type RegisterRequest struct {
	RuleVersionID string
	EntryType     domain.EntryType
	Lines         []domain.RegisterLine
}

// RegisterEntry is a posted register entry.
type RegisterEntry struct {
	ID            string                `json:"id"`
	RuleVersionID string                `json:"ruleVersionId"`
	EntryType     domain.EntryType      `json:"entryType"`
	Status        string                `json:"status"`
	Total         decimal.Decimal       `json:"total"`
	PostedAt      string                `json:"postedAt"`
	Lines         []domain.RegisterLine `json:"lines"`
}

// BalanceView is one control combination with its available funds (FR-38.4 step 5).
type BalanceView struct {
	ID                  string          `json:"id"`
	RuleVersionID       string          `json:"ruleVersionId"`
	ControlDimensionKey string          `json:"controlDimensionKey"`
	TimeSpanKey         string          `json:"timeSpanKey"`
	BudgetTotal         decimal.Decimal `json:"budgetTotal"`
	Actuals             decimal.Decimal `json:"actuals"`
	Encumbrances        decimal.Decimal `json:"encumbrances"`
	PreEncumbrances     decimal.Decimal `json:"preEncumbrances"`
	Available           decimal.Decimal `json:"available"`
	Version             int64           `json:"version"`
}

// EncumbranceView is one reservation with its relief history (FR-38.5).
type EncumbranceView struct {
	ID                  string          `json:"id"`
	RuleVersionID       string          `json:"ruleVersionId"`
	ControlDimensionKey string          `json:"controlDimensionKey"`
	TimeSpanKey         string          `json:"timeSpanKey"`
	SourceType          string          `json:"sourceType"`
	SourceDocumentID    string          `json:"sourceDocumentId"`
	SourceLineID        string          `json:"sourceLineId"`
	Reserved            decimal.Decimal `json:"reserved"`
	Relieved            decimal.Decimal `json:"relieved"`
	Remaining           decimal.Decimal `json:"remaining"`
	Status              string          `json:"status"`
	History             []historyEntry  `json:"history"`
	CreatedAt           string          `json:"createdAt"`
}

// Postgres is the budgeting store.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type command func(ctx context.Context, tx pgx.Tx) (int, any, error)

// run executes fn once per Idempotency-Key. A replay returns the stored body
// with the stored status; a reused key with a different body hash is 422.
// Only 2xx results reach this point stored, because fn errors roll back.
func (s *Postgres) run(ctx context.Context, key, hash string, fn command) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	var status int
	var raw []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var storedHash string
		var storedBody []byte
		err := tx.QueryRow(ctx, `select request_hash, response_status, response_body from idempotency_keys where tenant_id = $1 and key = $2`,
			p.TenantID, key).Scan(&storedHash, &status, &storedBody)
		if err == nil {
			if storedHash != hash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			raw = storedBody
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		st, body, err := fn(ctx, tx)
		if err != nil {
			return err
		}
		if raw, err = json.Marshal(body); err != nil {
			return err
		}
		status = st
		_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
			p.TenantID, key, hash, st, raw)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == idempotencyPrimaryKey {
		return 0, nil, httpx.NewProblem(http.StatusConflict, CodeIdempotencyInProgress, "a request with this Idempotency-Key is still in progress")
	}
	if err != nil {
		return 0, nil, err
	}
	return status, raw, nil
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func parseDec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ruleColumns reads numerics as text so no driver-specific scanning is needed.
const ruleColumns = `id::text, legal_entity_id::text, code, version, warn_pct::text, block_pct::text, currency, effective_from::text`

func scanRule(row pgx.Row) (Rule, error) {
	var r Rule
	var warn, block string
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.Code, &r.Version, &warn, &block, &r.Currency, &r.EffectiveFrom)
	r.WarnPct, r.BlockPct = parseDec(warn), parseDec(block)
	return r, err
}

func loadRule(ctx context.Context, tx pgx.Tx, tenantID, id string) (Rule, error) {
	r, err := scanRule(tx.QueryRow(ctx, `select `+ruleColumns+` from budget_control_rule where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, httpx.NewProblem(http.StatusNotFound, CodeRuleNotFound, "budget control rule version not found")
	}
	return r, err
}

// CreateRule stores the next version of a rule code in a legal entity.
func (s *Postgres) CreateRule(ctx context.Context, key, hash string, in NewRule) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		// Serialize version numbering per (legal entity, code) so two creates cannot pick the same number.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1 || ':' || $2 || ':' || $3, 0))`,
			p.TenantID, in.LegalEntityID, in.Code); err != nil {
			return 0, nil, err
		}
		r, err := scanRule(tx.QueryRow(ctx, `insert into budget_control_rule (tenant_id, id, legal_entity_id, code, version, warn_pct, block_pct, currency, effective_from)
			select $1, $2, $3, $4, coalesce(max(version), 0) + 1, $5, $6, $7, $8::date from budget_control_rule
			where tenant_id = $1 and legal_entity_id = $3 and code = $4
			returning `+ruleColumns, p.TenantID, ids.New(), in.LegalEntityID, in.Code, in.WarnPct, in.BlockPct, in.Currency, in.EffectiveFrom))
		return http.StatusCreated, r, err
	})
}

// balance is one locked control combination in one time span.
type balance struct {
	id  string
	bal domain.Balance
}

func balanceKey(dim, span string) string { return dim + "\x00" + span }

// lockBalance creates the combination's row if needed and locks it (FR-38.4 step 8).
func lockBalance(ctx context.Context, tx pgx.Tx, tenantID string, rule Rule, dim, span string) (*balance, error) {
	if _, err := tx.Exec(ctx, `insert into budget_control_balance (tenant_id, id, legal_entity_id, rule_version_id, control_dimension_key, time_span_key)
		values ($1, $2, $3, $4, $5, $6) on conflict (tenant_id, rule_version_id, control_dimension_key, time_span_key) do nothing`,
		tenantID, ids.New(), rule.LegalEntityID, rule.ID, dim, span); err != nil {
		return nil, err
	}
	var b balance
	var budget, actuals, enc, pre string
	err := tx.QueryRow(ctx, `select id::text, budget_total::text, actuals::text, encumbrances::text, pre_encumbrances::text from budget_control_balance
		where tenant_id = $1 and rule_version_id = $2 and control_dimension_key = $3 and time_span_key = $4 for update`,
		tenantID, rule.ID, dim, span).Scan(&b.id, &budget, &actuals, &enc, &pre)
	b.bal = domain.Balance{Budget: parseDec(budget), Actuals: parseDec(actuals), Encumbrances: parseDec(enc), PreEncumbrances: parseDec(pre)}
	return &b, err
}

// lockBalances locks every combination the lines touch in a fixed (sorted)
// order, so two documents that share combinations cannot deadlock.
func lockBalances(ctx context.Context, tx pgx.Tx, tenantID string, rule Rule, lines []CheckLine) (map[string]*balance, error) {
	keys := make([]string, 0, len(lines))
	parts := map[string][2]string{}
	for _, l := range lines {
		k := balanceKey(l.DimensionKey, l.SpanKey)
		if _, ok := parts[k]; !ok {
			parts[k] = [2]string{l.DimensionKey, l.SpanKey}
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make(map[string]*balance, len(keys))
	for _, k := range keys {
		b, err := lockBalance(ctx, tx, tenantID, rule, parts[k][0], parts[k][1])
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return out, nil
}

type historyEntry struct {
	Type       string `json:"type"`
	Amount     string `json:"amount"`
	DocumentID string `json:"documentId"`
	At         string `json:"at"`
}

// prior is a line's stored reservation, locked for the rest of the check.
type prior struct {
	id        string
	balanceID string
	reserved  decimal.Decimal
	relieved  decimal.Decimal
}

func lockPrior(ctx context.Context, tx pgx.Tx, tenantID, lineID string) (*prior, error) {
	var p prior
	var reserved, relieved string
	err := tx.QueryRow(ctx, `select id::text, balance_id::text, reserved::text, relieved::text from encumbrance
		where tenant_id = $1 and source_line_id = $2 for update`, tenantID, lineID).Scan(&p.id, &p.balanceID, &reserved, &relieved)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.reserved, p.relieved = parseDec(reserved), parseDec(relieved)
	return &p, nil
}

// CheckBudget runs FR-38.4 for every line. Nothing is reserved unless the
// document as a whole passes or warns; the result is recorded either way.
func (s *Postgres) CheckBudget(ctx context.Context, key, hash string, req CheckRequest, override bool) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		rule, err := loadRule(ctx, tx, p.TenantID, req.RuleVersionID)
		if err != nil {
			return 0, nil, err
		}
		balances, err := lockBalances(ctx, tx, p.TenantID, rule, req.Lines)
		if err != nil {
			return 0, nil, err
		}
		th := domain.Thresholds{WarnPct: rule.WarnPct, BlockPct: rule.BlockPct}
		resp := CheckResponse{ID: ids.New(), RuleVersionID: rule.ID, DocumentID: req.DocumentID, DocumentType: req.DocumentType,
			Result: domain.Pass, CheckedAt: now(), Lines: make([]LineResult, 0, len(req.Lines))}
		priors := make([]*prior, len(req.Lines))
		outcomes := make([]domain.CheckOutcome, len(req.Lines))
		for i, l := range req.Lines {
			b := balances[balanceKey(l.DimensionKey, l.SpanKey)]
			pr, err := lockPrior(ctx, tx, p.TenantID, l.LineID)
			if err != nil {
				return 0, nil, err
			}
			in := domain.CheckInput{Amount: l.Amount, Override: override}
			line := LineResult{LineID: l.LineID, DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount}
			if pr != nil {
				if pr.balanceID != b.id {
					return 0, nil, httpx.NewProblem(http.StatusConflict, CodeLineMoved,
						fmt.Sprintf("line %s is already reserved against another control combination or rule version", l.LineID))
				}
				in.Prior, in.Relieved = pr.reserved, pr.relieved
				line.Prior, line.EncumbranceID = pr.reserved, pr.id
			}
			out := domain.Check(b.bal, th, in)
			// Later lines on the same combination see this line's reservation.
			b.bal.Encumbrances = b.bal.Encumbrances.Add(out.Reserve)
			priors[i], outcomes[i] = pr, out
			line.Result, line.Available, line.Net, line.OverPct, line.Trace = out.Result, out.Available, out.Net, out.OverPct, out.Trace
			resp.Lines = append(resp.Lines, line)
			if slices.Contains(out.Trace, "override") {
				resp.Overridden = true
			}
			switch {
			case out.Result == domain.Fail:
				resp.Result = domain.Fail
			case out.Result == domain.Warn && resp.Result == domain.Pass:
				resp.Result = domain.Warn
			}
		}
		if resp.Result != domain.Fail {
			for i, l := range req.Lines {
				b := balances[balanceKey(l.DimensionKey, l.SpanKey)]
				id, err := reserve(ctx, tx, p.TenantID, rule, b.id, l, req, priors[i], outcomes[i].Reserve, resp.CheckedAt)
				if err != nil {
					return 0, nil, err
				}
				resp.Lines[i].EncumbranceID = id
			}
		}
		return http.StatusOK, resp, recordCheck(ctx, tx, p.TenantID, rule, resp)
	})
}

// reserve applies one line's reservation: the balance and the encumbrance move
// together. It returns the line's encumbrance id ("" when nothing is reserved).
func reserve(ctx context.Context, tx pgx.Tx, tenantID string, rule Rule, balanceID string, l CheckLine, req CheckRequest, pr *prior, amount decimal.Decimal, at string) (string, error) {
	if amount.IsZero() {
		if pr == nil {
			return "", nil
		}
		return pr.id, nil
	}
	if _, err := tx.Exec(ctx, `update budget_control_balance set encumbrances = encumbrances + $3, version = version + 1 where tenant_id = $1 and id = $2`,
		tenantID, balanceID, amount); err != nil {
		return "", err
	}
	kind := "Reserve"
	if pr != nil {
		kind = "Adjust"
	}
	hist, err := json.Marshal([]historyEntry{{Type: kind, Amount: amount.String(), DocumentID: req.DocumentID, At: at}})
	if err != nil {
		return "", err
	}
	if pr != nil {
		reserved := pr.reserved.Add(amount)
		_, err := tx.Exec(ctx, `update encumbrance set reserved = $3, status = $4, lines = lines || $5::jsonb where tenant_id = $1 and id = $2`,
			tenantID, pr.id, reserved, domain.EncumbranceStatus(reserved, pr.relieved), string(hist))
		return pr.id, err
	}
	id := ids.New()
	_, err = tx.Exec(ctx, `insert into encumbrance (tenant_id, id, legal_entity_id, balance_id, source_type, source_line_id, source_document_id, reserved, status, lines)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
		tenantID, id, rule.LegalEntityID, balanceID, req.DocumentType, l.LineID, req.DocumentID, amount, domain.StatusOpen, string(hist))
	return id, err
}

// recordCheck stores the BudgetCheckResult (step 8) and its outbox event.
func recordCheck(ctx context.Context, tx pgx.Tx, tenantID string, rule Rule, resp CheckResponse) error {
	lines, err := json.Marshal(resp.Lines)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into budget_check_result (tenant_id, id, legal_entity_id, rule_version_id, document_id, document_type, result, overridden, lines, checked_at)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::timestamptz)`,
		tenantID, resp.ID, rule.LegalEntityID, rule.ID, resp.DocumentID, resp.DocumentType, string(resp.Result), resp.Overridden, string(lines), resp.CheckedAt); err != nil {
		return err
	}
	data := budgetevents.CheckCompletedData{CheckID: resp.ID, SourceDocumentID: resp.DocumentID, Result: string(resp.Result)}
	for _, l := range resp.Lines {
		data.LineResults = append(data.LineResults, budgetevents.CheckLineData{LineID: l.LineID, Result: string(l.Result), Available: l.Available.String(), Amount: l.Amount.String()})
	}
	return enqueue(ctx, tx, "budgetcheck", resp.DocumentID, budgetevents.CheckCompleted, tenantID, data)
}

// RelieveEncumbrance applies relief = min(matched share, remaining reservation)
// and decrements the balance in the same transaction (FR-38.5).
func (s *Postgres) RelieveEncumbrance(ctx context.Context, key, hash string, in RelieveRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		var balanceID string
		err := tx.QueryRow(ctx, `select balance_id::text from encumbrance where tenant_id = $1 and id = $2`, p.TenantID, in.EncumbranceID).Scan(&balanceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, httpx.NewProblem(http.StatusNotFound, CodeEncumbranceNotFound, "encumbrance not found")
		}
		if err != nil {
			return 0, nil, err
		}
		// Lock order matches CheckBudget: balance first, then the encumbrance.
		if _, err := tx.Exec(ctx, `select 1 from budget_control_balance where tenant_id = $1 and id = $2 for update`, p.TenantID, balanceID); err != nil {
			return 0, nil, err
		}
		var reservedText, relievedText string
		if err := tx.QueryRow(ctx, `select reserved::text, relieved::text from encumbrance where tenant_id = $1 and id = $2 for update`,
			p.TenantID, in.EncumbranceID).Scan(&reservedText, &relievedText); err != nil {
			return 0, nil, err
		}
		reserved, relieved := parseDec(reservedText), parseDec(relievedText)
		relief := domain.Relief(reserved, relieved, in.MatchedShare)
		next := relieved.Add(relief)
		status := domain.EncumbranceStatus(reserved, next)
		resp := RelieveResponse{EncumbranceID: in.EncumbranceID, Relieved: relief, Remaining: reserved.Sub(next), Excess: domain.Excess(in.InvoiceAmount, relief), Status: status}
		if relief.IsZero() {
			return http.StatusOK, resp, nil
		}
		hist, err := json.Marshal([]historyEntry{{Type: "Relieve", Amount: relief.String(), DocumentID: in.InvoiceID, At: now()}})
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update encumbrance set relieved = $3, status = $4, lines = lines || $5::jsonb where tenant_id = $1 and id = $2`,
			p.TenantID, in.EncumbranceID, next, status, string(hist)); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update budget_control_balance set encumbrances = encumbrances - $3, version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, balanceID, relief); err != nil {
			return 0, nil, err
		}
		if err := enqueue(ctx, tx, "encumbrance", in.EncumbranceID, budgetevents.EncumbranceRelieved, p.TenantID, budgetevents.EncumbranceRelievedData{
			EncumbranceID: in.EncumbranceID, InvoiceID: in.InvoiceID, Amount: relief.String(), Remaining: resp.Remaining.String(),
		}); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, resp, nil
	})
}

// PostRegisterEntry validates and posts a register entry: budget_total moves
// by each line, and the entry is stored immutable (BR-38.2).
func (s *Postgres) PostRegisterEntry(ctx context.Context, key, hash string, req RegisterRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		rule, err := loadRule(ctx, tx, p.TenantID, req.RuleVersionID)
		if err != nil {
			return 0, nil, err
		}
		if err := domain.ValidateEntry(req.EntryType, req.Lines); err != nil {
			var de *domain.Error
			if !errors.As(err, &de) {
				return 0, nil, err
			}
			status := http.StatusUnprocessableEntity
			if de.Code == "COM-0001" {
				status = http.StatusBadRequest
			}
			return 0, nil, httpx.NewProblem(status, de.Code, de.Detail)
		}
		// Sorted upserts take the balance row locks in the same order as checks.
		lines := slices.Clone(req.Lines)
		sort.SliceStable(lines, func(i, j int) bool {
			return balanceKey(lines[i].DimensionKey, lines[i].SpanKey) < balanceKey(lines[j].DimensionKey, lines[j].SpanKey)
		})
		total := decimal.Zero
		for _, l := range lines {
			total = total.Add(l.Amount)
			if _, err := tx.Exec(ctx, `insert into budget_control_balance (tenant_id, id, legal_entity_id, rule_version_id, control_dimension_key, time_span_key, budget_total)
				values ($1, $2, $3, $4, $5, $6, $7)
				on conflict (tenant_id, rule_version_id, control_dimension_key, time_span_key)
				do update set budget_total = budget_control_balance.budget_total + excluded.budget_total, version = budget_control_balance.version + 1`,
				p.TenantID, ids.New(), rule.LegalEntityID, rule.ID, l.DimensionKey, l.SpanKey, l.Amount); err != nil {
				return 0, nil, err
			}
		}
		entry := RegisterEntry{ID: ids.New(), RuleVersionID: rule.ID, EntryType: req.EntryType, Status: "Posted", Total: total, PostedAt: now(), Lines: req.Lines}
		raw, err := json.Marshal(entry.Lines)
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `insert into budget_register_entry (tenant_id, id, legal_entity_id, rule_version_id, entry_type, lines, total, posted_at)
			values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8::timestamptz)`,
			p.TenantID, entry.ID, rule.LegalEntityID, rule.ID, string(req.EntryType), string(raw), total, entry.PostedAt); err != nil {
			return 0, nil, err
		}
		data := budgetevents.RegisterPostedData{EntryID: entry.ID, RuleVersionID: rule.ID, EntryType: string(req.EntryType)}
		for _, l := range req.Lines {
			data.Lines = append(data.Lines, budgetevents.RegisterLineData{DimensionKey: l.DimensionKey, SpanKey: l.SpanKey, Amount: l.Amount.String()})
		}
		if err := enqueue(ctx, tx, "register", entry.ID, budgetevents.RegisterPosted, p.TenantID, data); err != nil {
			return 0, nil, fmt.Errorf("enqueue register posted: %w", err)
		}
		return http.StatusCreated, entry, nil
	})
}
