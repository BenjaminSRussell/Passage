package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	tevents "github.com/BenjaminSRussell/passage/services/treasury/internal/events"
)

// Problem codes of reconciliation (see README). TRS-0411 is the PRD's concurrent-match code.
const (
	CodeLineMatched       = "TRS-0411"
	CodeNoRules           = "TRS-0480"
	CodeMatchUnbalanced   = "TRS-0481"
	CodeRuleDuplicate     = "TRS-0482"
	CodeRuleNotFound      = "TRS-0483"
	CodeMatchNotFound     = "TRS-0484"
	CodeMatchState        = "TRS-0485"
	CodeMatchLine         = "TRS-0486"
	CodeRuleState         = "TRS-0487"
	CodeLegalEntity       = "TRS-0488"
	MaxRunLines           = 2000
	MaxRunItems           = 2000
	MaxMatchesListed      = 500
	foreignKeyViolation   = "23503"
	reconciliationAggType = "reconciliation"
)

const ruleColumns = `id, legal_entity_id, bank_account_id, name, priority, weight_amount, weight_date, weight_reference,
	weight_counterparty, amount_tolerance::text, date_window_days, strict, active, version, created_at, updated_at`

// ReconRule is a stored matching rule (FR-36.3) for one bank account, or for every account of the legal entity.
type ReconRule struct {
	ID              string         `json:"id"`
	LegalEntityID   string         `json:"legalEntityId"`
	BankAccountID   *string        `json:"bankAccountId,omitempty"`
	Name            string         `json:"name"`
	Priority        int            `json:"priority"`
	Weights         domain.Weights `json:"weights"`
	AmountTolerance string         `json:"amountTolerance"`
	DateWindowDays  int            `json:"dateWindowDays"`
	Strict          bool           `json:"strict"`
	Active          bool           `json:"active"`
	Version         int            `json:"version"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// NewReconRule is a validated create request.
type NewReconRule struct {
	LegalEntityID string
	BankAccountID *string
	Rule          domain.Rule
}

func scanRule(row pgx.Row) (ReconRule, error) {
	var r ReconRule
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.BankAccountID, &r.Name, &r.Priority, &r.Weights.Amount, &r.Weights.Date, &r.Weights.Reference,
		&r.Weights.Counterparty, &r.AmountTolerance, &r.DateWindowDays, &r.Strict, &r.Active, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (r ReconRule) domainRule() (domain.Rule, error) {
	tolerance, err := decimal.NewFromString(r.AmountTolerance)
	if err != nil {
		return domain.Rule{}, err
	}
	return domain.Rule{Name: r.Name, Priority: r.Priority, Weights: r.Weights,
		Tolerance: domain.Tolerance{Amount: tolerance, DateWindowDays: r.DateWindowDays}, Strict: r.Strict}, nil
}

// MatchLine is a statement line inside a match with the amount it contributes.
type MatchLine struct {
	LineNo int    `json:"lineNo"`
	Amount string `json:"amount"`
}

// MatchItem is a ledger, AR or AP item inside a match, in the statement's sign convention.
type MatchItem struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

// Match groups statement lines with the items they settle: many items to one
// line (batch receipt) or one item to many lines.
type Match struct {
	ID            string      `json:"id"`
	LegalEntityID string      `json:"legalEntityId"`
	BankAccountID string      `json:"bankAccountId"`
	StatementID   string      `json:"statementId"`
	Kind          string      `json:"kind"`
	Status        string      `json:"status"`
	RuleName      string      `json:"ruleName,omitempty"`
	Score         *int        `json:"score,omitempty"`
	Difference    string      `json:"difference"`
	Reason        string      `json:"reason,omitempty"`
	UnmatchReason *string     `json:"unmatchReason,omitempty"`
	UnmatchedBy   *string     `json:"unmatchedBy,omitempty"`
	CreatedBy     string      `json:"createdBy"`
	Version       int         `json:"version"`
	Lines         []MatchLine `json:"lines"`
	Items         []MatchItem `json:"items"`
	CreatedAt     time.Time   `json:"createdAt"`
	UpdatedAt     time.Time   `json:"updatedAt"`
}

// NewMatch is a validated manual match request.
type NewMatch struct {
	StatementID   string
	LegalEntityID string
	LineNos       []int
	Items         []MatchItem
	Reason        string
}

// RunItem is an open item offered to a reconciliation run.
type RunItem struct {
	Type         string `json:"type"`
	ID           string `json:"id"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	ValueDate    string `json:"valueDate"`
	Reference    string `json:"reference"`
	Counterparty string `json:"counterparty"`
	Batch        string `json:"batch"`
}

// RunInput is a validated reconciliation run request.
type RunInput struct {
	StatementID   string
	LegalEntityID string
	AfterLineNo   int
	Items         []RunItem
}

// RunSuggestion is the best candidate of a line that did not auto-match.
type RunSuggestion struct {
	LineNo   int    `json:"lineNo"`
	ItemType string `json:"itemType"`
	ItemID   string `json:"itemId"`
	Score    int    `json:"score"`
}

// RunResult reports what a run matched, suggested and left alone.
type RunResult struct {
	Matches         []Match         `json:"matches"`
	Suggestions     []RunSuggestion `json:"suggestions"`
	LinesConsidered int             `json:"linesConsidered"`
	ItemsConsidered int             `json:"itemsConsidered"`
	// RemainingLines counts open lines after this page; run again with
	// afterLineNo = NextAfterLineNo to evaluate them.
	RemainingLines  int `json:"remainingLines"`
	NextAfterLineNo int `json:"nextAfterLineNo"`
}

func itemKey(kind, id string) string { return kind + ":" + id }

// CreateReconRule stores a rule. A repeated Idempotency-Key replays the stored rule.
func (s *Postgres) CreateReconRule(ctx context.Context, in NewReconRule, key string) (ReconRule, error) {
	p, _ := tenant.FromContext(ctx)
	hash, err := requestHash("create-recon-rule", in)
	if err != nil {
		return ReconRule{}, err
	}
	var out ReconRule
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, hash, func() (ReconRule, error) {
			r := in.Rule
			rule, err := scanRule(tx.QueryRow(ctx, `insert into reconciliation_rule (tenant_id, id, legal_entity_id, bank_account_id, name, priority,
				weight_amount, weight_date, weight_reference, weight_counterparty, amount_tolerance, date_window_days, strict)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) returning `+ruleColumns,
				p.TenantID, ids.New(), in.LegalEntityID, in.BankAccountID, r.Name, r.Priority, r.Weights.Amount, r.Weights.Date,
				r.Weights.Reference, r.Weights.Counterparty, r.Tolerance.Amount.String(), r.Tolerance.DateWindowDays, r.Strict))
			var pg *pgconn.PgError
			switch {
			case errors.As(err, &pg) && pg.Code == uniqueViolation:
				return ReconRule{}, httpx.NewProblem(http.StatusConflict, CodeRuleDuplicate, "a rule with this name exists for the legal entity")
			case errors.As(err, &pg) && pg.Code == foreignKeyViolation:
				return ReconRule{}, notFound(CodeAccountNotFound, "bank account not found for this legal entity")
			}
			return rule, err
		})
		return err
	})
	return out, err
}

// ListReconRules returns the rules of a legal entity, optionally those that apply to one bank account
// (the account's own and the entity-wide ones), in the order a run applies them.
func (s *Postgres) ListReconRules(ctx context.Context, legalEntityID, bankAccountID string) ([]ReconRule, error) {
	p, _ := tenant.FromContext(ctx)
	items := []ReconRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+ruleColumns+` from reconciliation_rule
			where tenant_id = $1 and legal_entity_id = $2 and ($3 = '' or bank_account_id is null or bank_account_id::text = $3)
			order by priority, name limit 500`, p.TenantID, legalEntityID, bankAccountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			items = append(items, r)
		}
		return rows.Err()
	})
	return items, err
}

// GetReconRule returns one rule.
func (s *Postgres) GetReconRule(ctx context.Context, id string) (ReconRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReconRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRule(tx.QueryRow(ctx, `select `+ruleColumns+` from reconciliation_rule where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeRuleNotFound, "reconciliation rule not found")
		}
		return err
	})
	return out, err
}

// DeactivateReconRule stops a rule from running; its past matches stay.
func (s *Postgres) DeactivateReconRule(ctx context.Context, id string, version int) (ReconRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out ReconRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanRule(tx.QueryRow(ctx, `select `+ruleColumns+` from reconciliation_rule where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeRuleNotFound, "reconciliation rule not found")
		}
		if err != nil {
			return err
		}
		if cur.Version != version {
			return versionMismatch()
		}
		if !cur.Active {
			return httpx.NewProblem(http.StatusConflict, CodeRuleState, "the rule is already inactive")
		}
		out, err = scanRule(tx.QueryRow(ctx, `update reconciliation_rule set active = false, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+ruleColumns, p.TenantID, id))
		return err
	})
	return out, err
}

type statementScope struct {
	legalEntityID, bankAccountID, currency string
}

func loadStatementScope(ctx context.Context, tx pgx.Tx, tenantID, statementID string) (statementScope, error) {
	var st statementScope
	err := tx.QueryRow(ctx, `select legal_entity_id, bank_account_id, currency from bank_statement where tenant_id = $1 and id = $2`,
		tenantID, statementID).Scan(&st.legalEntityID, &st.bankAccountID, &st.currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, notFound(CodeStatementNotFound, "statement not found")
	}
	return st, err
}

func createdBy(p tenant.Principal) string {
	if p.UserID != "" {
		return p.UserID
	}
	return "system"
}

// RunReconciliation applies the active rules of the statement's account to its
// unmatched and suggested lines against the offered open items (FR-36.3). Auto
// matches are stored and published; the best candidate of other lines is
// returned and their status becomes suggested. Items already in an active match
// are left out. The account row is locked, so two runs on one account queue.
func (s *Postgres) RunReconciliation(ctx context.Context, in RunInput, key string) (RunResult, error) {
	p, _ := tenant.FromContext(ctx)
	hash, err := requestHash("reconcile-run", in)
	if err != nil {
		return RunResult{}, err
	}
	var out RunResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, hash, func() (RunResult, error) { return runReconciliation(ctx, tx, p, in) })
		return err
	})
	return out, err
}

func runReconciliation(ctx context.Context, tx pgx.Tx, p tenant.Principal, in RunInput) (RunResult, error) {
	st, err := loadStatementScope(ctx, tx, p.TenantID, in.StatementID)
	if err != nil {
		return RunResult{}, err
	}
	if in.LegalEntityID != st.legalEntityID {
		return RunResult{}, unprocessable(CodeLegalEntity, "the items belong to a different legal entity than the statement")
	}
	if _, err := tx.Exec(ctx, `select 1 from bank_account where tenant_id = $1 and id = $2 for update`, p.TenantID, st.bankAccountID); err != nil {
		return RunResult{}, err
	}
	rules, err := activeRules(ctx, tx, p.TenantID, st)
	if err != nil {
		return RunResult{}, err
	}
	lines, amounts, remaining, err := openLines(ctx, tx, p.TenantID, in.StatementID, in.AfterLineNo)
	if err != nil {
		return RunResult{}, err
	}
	items, byKey, err := openItems(ctx, tx, p.TenantID, st, in.Items)
	if err != nil {
		return RunResult{}, err
	}
	outcome := domain.ReconcileStatement(lines, items, rules, domain.DefaultThresholds)
	matches := make([]Match, 0, len(outcome.Matches))
	for _, am := range outcome.Matches {
		m := Match{ID: ids.New(), LegalEntityID: st.legalEntityID, BankAccountID: st.bankAccountID, StatementID: in.StatementID,
			Kind: "auto", Status: "matched", RuleName: am.Rule, Score: &am.Score, Difference: am.Difference.String(), CreatedBy: createdBy(p), Version: 1}
		for _, no := range am.LineNos {
			m.Lines = append(m.Lines, MatchLine{LineNo: no, Amount: amounts[no]})
		}
		for _, id := range am.ItemIDs {
			m.Items = append(m.Items, byKey[id])
		}
		matches = append(matches, m)
	}
	if matches, err = writeMatches(ctx, tx, p.TenantID, in.StatementID, matches); err != nil {
		return RunResult{}, err
	}
	result := RunResult{Matches: matches, Suggestions: []RunSuggestion{}, LinesConsidered: len(lines), ItemsConsidered: len(items), RemainingLines: remaining}
	considered := make([]int32, len(lines))
	for i, l := range lines {
		considered[i] = int32(l.No)
	}
	if len(lines) > 0 && remaining > 0 {
		result.NextAfterLineNo = lines[len(lines)-1].No
	}
	suggested := []int32{}
	for _, sg := range outcome.Suggestions {
		it := byKey[sg.ItemID]
		result.Suggestions = append(result.Suggestions, RunSuggestion{LineNo: sg.LineNo, ItemType: it.Type, ItemID: it.ID, Score: sg.Score})
		suggested = append(suggested, int32(sg.LineNo))
	}
	if err := markSuggested(ctx, tx, p.TenantID, in.StatementID, suggested, considered); err != nil {
		return RunResult{}, err
	}
	return result, nil
}

func activeRules(ctx context.Context, tx pgx.Tx, tenantID string, st statementScope) ([]domain.Rule, error) {
	rows, err := tx.Query(ctx, `select `+ruleColumns+` from reconciliation_rule where tenant_id = $1 and legal_entity_id = $2 and active
		and (bank_account_id is null or bank_account_id = $3) order by priority, name`, tenantID, st.legalEntityID, st.bankAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []domain.Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		rule, err := r.domainRule()
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, unprocessable(CodeNoRules, "the bank account has no active reconciliation rule")
	}
	return rules, nil
}

func openLines(ctx context.Context, tx pgx.Tx, tenantID, statementID string, after int) ([]domain.RecLine, map[int]string, int, error) {
	rows, err := tx.Query(ctx, `select line_no, value_date::text, amount::text, end_to_end_ref, reference, counterparty_name
		from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no > $3 and status in ('unmatched', 'suggested')
		order by line_no limit $4`, tenantID, statementID, after, MaxRunLines)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	var lines []domain.RecLine
	amounts := map[int]string{}
	for rows.Next() {
		var l domain.RecLine
		var amount string
		if err := rows.Scan(&l.No, &l.ValueDate, &amount, &l.Reference, &l.AltReference, &l.Counterparty); err != nil {
			return nil, nil, 0, err
		}
		if l.Amount, err = decimal.NewFromString(amount); err != nil {
			return nil, nil, 0, err
		}
		amounts[l.No] = amount
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(lines) == 0 {
		return lines, amounts, 0, err
	}
	var remaining int
	err = tx.QueryRow(ctx, `select count(*) from bank_statement_line where tenant_id = $1 and statement_id = $2 and line_no > $3
		and status in ('unmatched', 'suggested')`, tenantID, statementID, lines[len(lines)-1].No).Scan(&remaining)
	return lines, amounts, remaining, err
}

func openItems(ctx context.Context, tx pgx.Tx, tenantID string, st statementScope, offered []RunItem) ([]domain.Item, map[string]MatchItem, error) {
	keys := make([]string, 0, len(offered))
	for _, it := range offered {
		keys = append(keys, itemKey(it.Type, it.ID))
	}
	taken := map[string]bool{}
	rows, err := tx.Query(ctx, `select item_type || ':' || item_id from reconciliation_match_item
		where tenant_id = $1 and legal_entity_id = $2 and active and item_type || ':' || item_id = any($3)`, tenantID, st.legalEntityID, keys)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, nil, err
		}
		taken[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var items []domain.Item
	byKey := map[string]MatchItem{}
	for _, it := range offered {
		k := itemKey(it.Type, it.ID)
		if taken[k] || it.Currency != st.currency {
			continue
		}
		amount, err := decimal.NewFromString(it.Amount)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, domain.Item{ID: k, Batch: it.Batch, Facts: domain.Facts{
			Amount: amount, ValueDate: it.ValueDate, Reference: it.Reference, Counterparty: it.Counterparty}})
		byKey[k] = MatchItem{Type: it.Type, ID: it.ID, Currency: it.Currency, Amount: it.Amount}
	}
	return items, byKey, nil
}

// markSuggested sets suggested lines to suggested and puts lines of this page
// that were suggested by an earlier run but found no candidate now back to
// unmatched. Lines outside the page keep their status.
func markSuggested(ctx context.Context, tx pgx.Tx, tenantID, statementID string, suggested, considered []int32) error {
	if _, err := tx.Exec(ctx, `update bank_statement_line set status = 'suggested', version = version + 1
		where tenant_id = $1 and statement_id = $2 and line_no = any($3) and status = 'unmatched'`, tenantID, statementID, suggested); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update bank_statement_line set status = 'unmatched', version = version + 1
		where tenant_id = $1 and statement_id = $2 and status = 'suggested' and line_no = any($3) and not (line_no = any($4))`,
		tenantID, statementID, considered, suggested)
	return err
}

// writeMatches stores matches with their lines and items, marks the lines
// matched and queues one event per match, all in the caller's transaction. A
// line or item that is already in an active match is refused (TRS-0411).
func writeMatches(ctx context.Context, tx pgx.Tx, tenantID, statementID string, matches []Match) ([]Match, error) {
	if len(matches) == 0 {
		return matches, nil
	}
	batch := &pgx.Batch{}
	var lineNos []int32
	for _, m := range matches {
		batch.Queue(`insert into reconciliation_match (tenant_id, id, legal_entity_id, bank_account_id, statement_id, kind, rule_name, score, difference, reason, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, tenantID, m.ID, m.LegalEntityID, m.BankAccountID, statementID, m.Kind, m.RuleName, m.Score, m.Difference, m.Reason, m.CreatedBy)
		for _, l := range m.Lines {
			batch.Queue(`insert into reconciliation_match_line (tenant_id, match_id, statement_id, line_no, legal_entity_id, amount) values ($1, $2, $3, $4, $5, $6)`,
				tenantID, m.ID, statementID, l.LineNo, m.LegalEntityID, l.Amount)
			lineNos = append(lineNos, int32(l.LineNo))
		}
		for _, it := range m.Items {
			batch.Queue(`insert into reconciliation_match_item (tenant_id, match_id, legal_entity_id, item_type, item_id, currency, amount) values ($1, $2, $3, $4, $5, $6, $7)`,
				tenantID, m.ID, m.LegalEntityID, it.Type, it.ID, it.Currency, it.Amount)
		}
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == uniqueViolation {
				return nil, httpx.NewProblem(http.StatusConflict, CodeLineMatched, "a statement line or item is already in an active match")
			}
			return nil, err
		}
	}
	if err := br.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `update bank_statement_line set status = 'matched', version = version + 1
		where tenant_id = $1 and statement_id = $2 and line_no = any($3)`, tenantID, statementID, lineNos); err != nil {
		return nil, err
	}
	stamps := map[string][2]time.Time{}
	matchIDs := make([]string, len(matches))
	for i, m := range matches {
		matchIDs[i] = m.ID
	}
	rows, err := tx.Query(ctx, `select id, created_at, updated_at from reconciliation_match where tenant_id = $1 and id = any($2)`, tenantID, matchIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var at [2]time.Time
		if err := rows.Scan(&id, &at[0], &at[1]); err != nil {
			rows.Close()
			return nil, err
		}
		stamps[id] = at
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range matches {
		matches[i].CreatedAt, matches[i].UpdatedAt = stamps[matches[i].ID][0], stamps[matches[i].ID][1]
		if err := enqueueMatched(ctx, tx, tenantID, matches[i]); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func enqueueMatched(ctx context.Context, tx pgx.Tx, tenantID string, m Match) error {
	evt, err := events.New(tevents.ReconciliationMatched, eventSource, m.ID, tenantID, tevents.ReconciliationMatchedData{
		MatchID: m.ID, StatementID: m.StatementID, BankAccountID: m.BankAccountID, LegalEntityID: m.LegalEntityID,
		Kind: m.Kind, LineCount: len(m.Lines), ItemCount: len(m.Items), Score: m.Score,
	})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, reconciliationAggType, m.ID, evt)
}

// CreateManualMatch matches the chosen lines to the chosen items. The lines'
// amounts must add up to the items' exactly (TRS-0481); every line and item
// must share the statement currency and be free of another active match.
func (s *Postgres) CreateManualMatch(ctx context.Context, in NewMatch, key string) (Match, error) {
	p, _ := tenant.FromContext(ctx)
	hash, err := requestHash("manual-match", in)
	if err != nil {
		return Match{}, err
	}
	var out Match
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, key, hash, func() (Match, error) { return manualMatch(ctx, tx, p, in) })
		return err
	})
	return out, err
}

func manualMatch(ctx context.Context, tx pgx.Tx, p tenant.Principal, in NewMatch) (Match, error) {
	st, err := loadStatementScope(ctx, tx, p.TenantID, in.StatementID)
	if err != nil {
		return Match{}, err
	}
	if in.LegalEntityID != st.legalEntityID {
		return Match{}, unprocessable(CodeLegalEntity, "the items belong to a different legal entity than the statement")
	}
	nos := make([]int32, len(in.LineNos))
	for i, n := range in.LineNos {
		nos[i] = int32(n)
	}
	rows, err := tx.Query(ctx, `select line_no, amount::text, currency, status from bank_statement_line
		where tenant_id = $1 and statement_id = $2 and line_no = any($3) order by line_no`, p.TenantID, in.StatementID, nos)
	if err != nil {
		return Match{}, err
	}
	m := Match{ID: ids.New(), LegalEntityID: st.legalEntityID, BankAccountID: st.bankAccountID, StatementID: in.StatementID,
		Kind: "manual", Status: "matched", Reason: in.Reason, CreatedBy: createdBy(p), Version: 1, Items: in.Items}
	lineTotal, itemTotal := decimal.Zero, decimal.Zero
	for rows.Next() {
		var l MatchLine
		var currency, status string
		if err := rows.Scan(&l.LineNo, &l.Amount, &currency, &status); err != nil {
			rows.Close()
			return Match{}, err
		}
		if currency != st.currency {
			rows.Close()
			return Match{}, unprocessable(CodeMatchLine, "statement line currency differs from the statement currency")
		}
		if status == "matched" {
			rows.Close()
			return Match{}, httpx.NewProblem(http.StatusConflict, CodeLineMatched, "statement line is already matched")
		}
		lineTotal = lineTotal.Add(decimal.RequireFromString(l.Amount))
		m.Lines = append(m.Lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Match{}, err
	}
	if len(m.Lines) != len(in.LineNos) {
		return Match{}, unprocessable(CodeMatchLine, "a statement line is not on the statement")
	}
	for _, it := range in.Items {
		if it.Currency != st.currency {
			return Match{}, unprocessable(CodeMatchLine, "item currency differs from the statement currency")
		}
		itemTotal = itemTotal.Add(decimal.RequireFromString(it.Amount))
	}
	if !lineTotal.Equal(itemTotal) {
		return Match{}, unprocessable(CodeMatchUnbalanced, "lines add up to "+lineTotal.String()+" but items to "+itemTotal.String())
	}
	m.Difference = "0"
	matched, err := writeMatches(ctx, tx, p.TenantID, in.StatementID, []Match{m})
	if err != nil {
		return Match{}, err
	}
	return matched[0], nil
}

const matchColumns = `id, legal_entity_id, bank_account_id, statement_id, kind, status, rule_name, score, difference::text, reason,
	unmatch_reason, unmatched_by, created_by, version, created_at, updated_at`

func loadMatches(ctx context.Context, tx pgx.Tx, tenantID, where string, limit, offset int, args ...any) ([]Match, error) {
	n := strconv.Itoa(2 + len(args))
	rows, err := tx.Query(ctx, `select `+matchColumns+` from reconciliation_match where tenant_id = $1 and `+where+` order by created_at, id limit $`+
		n+` offset $`+strconv.Itoa(3+len(args)), append(append([]any{tenantID}, args...), limit, offset)...)
	if err != nil {
		return nil, err
	}
	matches := []Match{}
	index := map[string]int{}
	var matchIDs []string
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.ID, &m.LegalEntityID, &m.BankAccountID, &m.StatementID, &m.Kind, &m.Status, &m.RuleName, &m.Score, &m.Difference,
			&m.Reason, &m.UnmatchReason, &m.UnmatchedBy, &m.CreatedBy, &m.Version, &m.CreatedAt, &m.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.Lines, m.Items = []MatchLine{}, []MatchItem{}
		index[m.ID] = len(matches)
		matchIDs = append(matchIDs, m.ID)
		matches = append(matches, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(matches) == 0 {
		return matches, err
	}
	lines, err := tx.Query(ctx, `select match_id, line_no, amount::text from reconciliation_match_line where tenant_id = $1 and match_id = any($2) order by match_id, line_no`, tenantID, matchIDs)
	if err != nil {
		return nil, err
	}
	for lines.Next() {
		var id string
		var l MatchLine
		if err := lines.Scan(&id, &l.LineNo, &l.Amount); err != nil {
			lines.Close()
			return nil, err
		}
		matches[index[id]].Lines = append(matches[index[id]].Lines, l)
	}
	lines.Close()
	if err := lines.Err(); err != nil {
		return nil, err
	}
	its, err := tx.Query(ctx, `select match_id, item_type, item_id, currency, amount::text from reconciliation_match_item where tenant_id = $1 and match_id = any($2) order by match_id, item_type, item_id`, tenantID, matchIDs)
	if err != nil {
		return nil, err
	}
	defer its.Close()
	for its.Next() {
		var id string
		var it MatchItem
		if err := its.Scan(&id, &it.Type, &it.ID, &it.Currency, &it.Amount); err != nil {
			return nil, err
		}
		matches[index[id]].Items = append(matches[index[id]].Items, it)
	}
	return matches, its.Err()
}

// ListMatches returns a page of the matches of a statement, oldest first, unmatched ones included.
func (s *Postgres) ListMatches(ctx context.Context, statementID string, limit, offset int) ([]Match, error) {
	p, _ := tenant.FromContext(ctx)
	if limit <= 0 || limit > MaxMatchesListed {
		limit = MaxMatchesListed
	}
	var out []Match
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadMatches(ctx, tx, p.TenantID, `statement_id = $2`, limit, offset, statementID)
		return err
	})
	return out, err
}

// GetMatch returns one match with its lines and items.
func (s *Postgres) GetMatch(ctx context.Context, id string) (Match, error) {
	p, _ := tenant.FromContext(ctx)
	var out Match
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := loadMatches(ctx, tx, p.TenantID, `id = $2`, 1, 0, id)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return notFound(CodeMatchNotFound, "reconciliation match not found")
		}
		out = found[0]
		return nil
	})
	return out, err
}

// Unmatch dissolves a match under the optimistic version check: its lines and
// items become free again, its lines return to unmatched, and the reason is
// kept. The row stays as history.
func (s *Postgres) Unmatch(ctx context.Context, id string, version int, reason string) (Match, error) {
	p, _ := tenant.FromContext(ctx)
	var out Match
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var curVersion int
		var status, statementID, accountID, legalEntityID string
		err := tx.QueryRow(ctx, `select version, status, statement_id, bank_account_id, legal_entity_id from reconciliation_match
			where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&curVersion, &status, &statementID, &accountID, &legalEntityID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeMatchNotFound, "reconciliation match not found")
		}
		if err != nil {
			return err
		}
		if curVersion != version {
			return versionMismatch()
		}
		if status != "matched" {
			return unprocessable(CodeMatchState, "the match is already unmatched")
		}
		if _, err := tx.Exec(ctx, `update reconciliation_match set status = 'unmatched', unmatch_reason = $3, unmatched_by = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, id, reason, createdBy(p)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update bank_statement_line l set status = 'unmatched', version = l.version + 1
			from reconciliation_match_line ml where ml.tenant_id = $1 and ml.match_id = $2 and l.tenant_id = ml.tenant_id
			and l.statement_id = ml.statement_id and l.line_no = ml.line_no`, p.TenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update reconciliation_match_line set active = false where tenant_id = $1 and match_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update reconciliation_match_item set active = false where tenant_id = $1 and match_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		evt, err := events.New(tevents.ReconciliationUnmatched, eventSource, id, p.TenantID, tevents.ReconciliationUnmatchedData{
			MatchID: id, StatementID: statementID, BankAccountID: accountID, LegalEntityID: legalEntityID, Reason: reason, UnmatchedBy: createdBy(p),
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, reconciliationAggType, id, evt); err != nil {
			return err
		}
		found, err := loadMatches(ctx, tx, p.TenantID, `id = $2`, 1, 0, id)
		if err != nil {
			return err
		}
		out = found[0]
		return nil
	})
	return out, err
}
