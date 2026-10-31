package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// Automatic clearing error codes (FR-11.20).
const (
	CodeAutoRuleNotFound   = "LEDGER_AUTO_CLEARING_RULE_NOT_FOUND"
	CodeAutoRunNotFound    = "LEDGER_AUTO_CLEARING_RUN_NOT_FOUND"
	CodeAutoRuleInactive   = "LEDGER_AUTO_CLEARING_RULE_INACTIVE"
	CodeAutoRuleIDConflict = "LEDGER_AUTO_CLEARING_RULE_ID_CONFLICT"

	// MaxRunGroupsShown caps the group log returned with one run or simulation.
	MaxRunGroupsShown = 1000
)

// AutoClearingRule groups the open items of an account range by ordered criteria
// (OB74 semantics). With AllowTolerance the named tolerance group lets a group
// with a small net clear too.
type AutoClearingRule struct {
	ID             string    `json:"id"`
	LedgerID       string    `json:"ledgerId"`
	Name           string    `json:"name"`
	AccountFrom    string    `json:"accountFrom"`
	AccountTo      string    `json:"accountTo"`
	Criteria       []string  `json:"criteria"`
	AllowTolerance bool      `json:"allowTolerance"`
	ToleranceGroup string    `json:"toleranceGroup,omitempty"`
	Sequence       int       `json:"sequence"`
	Active         bool      `json:"active"`
	Version        int       `json:"version"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

const autoRuleSelect = `select id::text, ledger_id::text, name, account_from, account_to, criteria, allow_tolerance, coalesce(tolerance_group_code, ''),
		sequence, active, version, created_by, created_at, updated_at from auto_clearing_rule`

func scanAutoRule(row pgx.Row) (AutoClearingRule, error) {
	var r AutoClearingRule
	err := row.Scan(&r.ID, &r.LedgerID, &r.Name, &r.AccountFrom, &r.AccountTo, &r.Criteria, &r.AllowTolerance, &r.ToleranceGroup,
		&r.Sequence, &r.Active, &r.Version, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func autoRuleNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeAutoRuleNotFound, "automatic clearing rule not found")
}

// ListAutoClearingRules returns the rules of a ledger in run order.
func (s *Postgres) ListAutoClearingRules(ctx context.Context, ledgerID string) ([]AutoClearingRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []AutoClearingRule{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, autoRuleSelect+` where tenant_id = $1 and ledger_id = $2 order by sequence, name, id`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanAutoRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetAutoClearingRule returns one rule or 404 LEDGER_AUTO_CLEARING_RULE_NOT_FOUND.
func (s *Postgres) GetAutoClearingRule(ctx context.Context, ledgerID, id string) (AutoClearingRule, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return AutoClearingRule{}, err
	}
	var out AutoClearingRule
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		out, err = scanAutoRule(tx.QueryRow(ctx, autoRuleSelect+` where tenant_id = $1 and ledger_id = $2 and id = $3`, p.TenantID, ledgerID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return autoRuleNotFound()
		}
		return err
	})
	return out, err
}

// PutAutoClearingRule creates or replaces a rule under a client-chosen id. A replace
// checks version when it is above zero (412 LEDGER_VERSION_CONFLICT). A rule that
// allows tolerance must name an active tolerance group of the ledger.
func (s *Postgres) PutAutoClearingRule(ctx context.Context, ledgerID, id string, version int, in AutoClearingRule) (AutoClearingRule, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return AutoClearingRule{}, false, err
	}
	var out AutoClearingRule
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		ledger, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false)
		if err != nil {
			return err
		}
		var tol *string
		if in.AllowTolerance {
			var active bool
			err := tx.QueryRow(ctx, `select active from tolerance_group where tenant_id = $1 and ledger_id = $2 and code = $3`,
				p.TenantID, ledgerID, in.ToleranceGroup).Scan(&active)
			if errors.Is(err, pgx.ErrNoRows) {
				return oiViolation(CodeToleranceNotFound, "toleranceGroup", "tolerance group "+in.ToleranceGroup+" does not exist in this ledger")
			}
			if err != nil {
				return err
			}
			if !active {
				return oiViolation(CodeToleranceInactive, "toleranceGroup", "tolerance group "+in.ToleranceGroup+" is not active")
			}
			tol = &in.ToleranceGroup
		}
		var current int
		var owner string
		err = tx.QueryRow(ctx, `select version, ledger_id::text from auto_clearing_rule where tenant_id = $1 and id = $2 for update`,
			p.TenantID, id).Scan(&current, &owner)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			created = true
			_, err = tx.Exec(ctx, `insert into auto_clearing_rule (tenant_id, id, legal_entity_id, ledger_id, name, account_from, account_to,
					criteria, allow_tolerance, tolerance_group_code, sequence, active, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				p.TenantID, id, ledger.LegalEntityID, ledgerID, in.Name, in.AccountFrom, in.AccountTo, in.Criteria, in.AllowTolerance, tol,
				in.Sequence, in.Active, p.UserID)
		case err != nil:
			return err
		default:
			if owner != ledgerID {
				return autoRuleNotFound()
			}
			if version > 0 && version != current {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
					"the automatic clearing rule changed since it was read; reload and retry")
			}
			_, err = tx.Exec(ctx, `update auto_clearing_rule set name = $3, account_from = $4, account_to = $5, criteria = $6, allow_tolerance = $7,
					tolerance_group_code = $8, sequence = $9, active = $10, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`,
				p.TenantID, id, in.Name, in.AccountFrom, in.AccountTo, in.Criteria, in.AllowTolerance, tol, in.Sequence, in.Active)
		}
		if err != nil {
			return err
		}
		out, err = scanAutoRule(tx.QueryRow(ctx, autoRuleSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if isUnique(err) {
		// A concurrent create of the same id, or an id held by a rule outside the caller's legal entities.
		return AutoClearingRule{}, false, httpx.NewProblem(http.StatusConflict, CodeAutoRuleIDConflict,
			"this id is already used by another automatic clearing rule; reload, or create with a new id")
	}
	return out, created, err
}

// AutoClearingInput runs the active rules of a ledger, or one rule, on the open
// items now. Mode is simulate (nothing is written) or clear (Key and Hash are the
// Idempotency-Key and request hash). A group clears on ClearingDate, or on its
// latest item's posting date when that is later.
type AutoClearingInput struct {
	LedgerID     string
	RuleID       string
	Mode         string
	ClearingDate time.Time
	Key          string
	Hash         []byte
	TraceID      string
	Today        time.Time
}

// AutoClearingGroup is one group of two or more items a rule found. Status is
// proposed (simulation), cleared or skipped; skipped groups carry the reason.
type AutoClearingGroup struct {
	No                 int      `json:"no"`
	RuleID             string   `json:"ruleId"`
	Account            string   `json:"account"`
	ItemCount          int      `json:"itemCount"`
	Net                string   `json:"net"`
	Status             string   `json:"status"`
	ReasonCode         string   `json:"reasonCode,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	ClearingDocumentID *string  `json:"clearingDocumentId,omitempty"`
	ItemIDs            []string `json:"itemIds,omitempty"`
}

// AutoClearingRun is the result of a run: a stored one (clear) or a simulation.
type AutoClearingRun struct {
	ID             string              `json:"id,omitempty"`
	LedgerID       string              `json:"ledgerId"`
	RuleID         *string             `json:"ruleId,omitempty"`
	Mode           string              `json:"mode"`
	ClearingDate   string              `json:"clearingDate"`
	GroupsFound    int                 `json:"groupsFound"`
	GroupsCleared  int                 `json:"groupsCleared"`
	GroupsSkipped  int                 `json:"groupsSkipped"`
	ItemsCleared   int                 `json:"itemsCleared"`
	UnmatchedItems int                 `json:"unmatchedItems"`
	Truncated      bool                `json:"truncated"`
	CreatedBy      string              `json:"createdBy,omitempty"`
	CreatedAt      *time.Time          `json:"createdAt,omitempty"`
	Groups         []AutoClearingGroup `json:"groups,omitempty"`

	hash []byte
}

type plannedGroup struct {
	rule    AutoClearingRule
	group   domain.AutoGroup
	tolCode string
	reason  string
	message string
	clear   bool
}

func (s *Postgres) loadRunRules(ctx context.Context, tx pgxQuerier, tenantID string, in AutoClearingInput) ([]AutoClearingRule, error) {
	if in.RuleID != "" {
		r, err := scanAutoRule(tx.QueryRow(ctx, autoRuleSelect+` where tenant_id = $1 and ledger_id = $2 and id = $3`, tenantID, in.LedgerID, in.RuleID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, autoRuleNotFound()
		}
		if err != nil {
			return nil, err
		}
		if !r.Active {
			return nil, oiViolation(CodeAutoRuleInactive, "ruleId", "the automatic clearing rule is not active")
		}
		return []AutoClearingRule{r}, nil
	}
	rows, err := tx.Query(ctx, autoRuleSelect+` where tenant_id = $1 and ledger_id = $2 and active order by sequence, name, id`, tenantID, in.LedgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AutoClearingRule
	for rows.Next() {
		r, err := scanAutoRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pgxQuerier is the read side of a transaction.
type pgxQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const autoItemSelect = `select i.id::text, a.account_number, i.amount::text, i.posting_date::text, coalesce(i.assignment, ''), dc.dimensions,
		coalesce(jl.source_line_ref, ''), coalesce(je.document_id::text, '')
	from open_item i
	join main_account a on a.tenant_id = i.tenant_id and a.id = i.main_account_id
	join open_item_setting s on s.tenant_id = i.tenant_id and s.ledger_id = i.ledger_id and s.main_account_id = i.main_account_id and s.enabled
	join dimension_combination dc on dc.tenant_id = i.tenant_id and dc.id = i.dimension_combination_id
	left join journal_line jl on jl.tenant_id = i.tenant_id and jl.id = i.journal_line_id
	left join journal_entry je on je.tenant_id = jl.tenant_id and je.id = jl.entry_id
	where i.tenant_id = $1 and i.ledger_id = $2 and i.status in ('open', 'reset_open')
	  and a.account_number >= $3::text and a.account_number <= $4::text
	order by i.seq limit $5`

// plan reads the open items of each rule's range and decides every group; items a
// group of an earlier rule already took are left to that rule.
func plan(ctx context.Context, tx pgxQuerier, tenantID string, rules []AutoClearingRule) (out []plannedGroup, unmatched int, truncated bool, err error) {
	taken := map[string]bool{}
	for _, rule := range rules {
		rows, err := tx.Query(ctx, autoItemSelect, tenantID, rule.LedgerID, rule.AccountFrom, rule.AccountTo, domain.MaxAutoItems+1)
		if err != nil {
			return nil, 0, false, err
		}
		var items []domain.AutoItem
		read := 0
		for rows.Next() {
			read++
			if read > domain.MaxAutoItems {
				// More items wait than one run reads; they are left for the next run.
				truncated = true
				break
			}
			var it domain.AutoItem
			var amount string
			if err := rows.Scan(&it.ID, &it.Account, &amount, &it.PostingDate, &it.Assignment, &it.Dimensions, &it.SourceLineRef, &it.DocumentID); err != nil {
				rows.Close()
				return nil, 0, false, err
			}
			if it.Amount, err = decimal.NewFromString(amount); err != nil {
				rows.Close()
				return nil, 0, false, err
			}
			if !taken[it.ID] {
				items = append(items, it)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, 0, false, err
		}
		var tol *domain.Tolerance
		var tolProblem, tolMessage string
		if rule.AllowTolerance {
			var debit, credit, pct string
			var active, managed bool
			var diff string
			err := tx.QueryRow(ctx, `select t.debit_limit::text, t.credit_limit::text, t.percent_limit::text, t.active, a.account_number,
					coalesce((select s.enabled from open_item_setting s where s.tenant_id = t.tenant_id and s.ledger_id = t.ledger_id
						and s.main_account_id = t.difference_account_id), false)
				from tolerance_group t join main_account a on a.tenant_id = t.tenant_id and a.id = t.difference_account_id
				where t.tenant_id = $1 and t.ledger_id = $2 and t.code = $3`, tenantID, rule.LedgerID, rule.ToleranceGroup).
				Scan(&debit, &credit, &pct, &active, &diff, &managed)
			switch {
			case err != nil && !errors.Is(err, pgx.ErrNoRows):
				return nil, 0, false, err
			case err != nil:
				tolProblem, tolMessage = CodeToleranceNotFound, "tolerance group "+rule.ToleranceGroup+" does not exist"
			case !active:
				tolProblem, tolMessage = CodeToleranceInactive, "tolerance group "+rule.ToleranceGroup+" is not active"
			case managed:
				tolProblem, tolMessage = CodeClearingDifferenceAccount, "the difference account "+diff+" of tolerance group "+rule.ToleranceGroup+" is open-item managed"
			default:
				tol = &domain.Tolerance{Debit: decimal.RequireFromString(debit), Credit: decimal.RequireFromString(credit), Percent: decimal.RequireFromString(pct)}
			}
		}
		groups, miss := domain.GroupItems(items, rule.Criteria)
		unmatched += miss
		for _, g := range groups {
			pg := plannedGroup{rule: rule, group: g}
			d := domain.DecideGroup(g, tol)
			pg.clear, pg.reason, pg.message = d.Clear, d.Reason, d.Message
			if !d.Clear && tolProblem != "" && d.Reason == "LEDGER_CLEARING_UNBALANCED" {
				pg.reason, pg.message = tolProblem, tolMessage
			}
			if pg.clear && !g.Net.IsZero() {
				pg.tolCode = rule.ToleranceGroup
			}
			if pg.clear {
				for _, it := range g.Items {
					taken[it.ID] = true
				}
			}
			out = append(out, pg)
		}
	}
	return out, unmatched, truncated, nil
}

func (pg plannedGroup) view(no int, minor int32, status string, withItems bool) AutoClearingGroup {
	v := AutoClearingGroup{No: no, RuleID: pg.rule.ID, Account: pg.group.Account, ItemCount: len(pg.group.Items),
		Net: pg.group.Net.StringFixed(minor), Status: status, ReasonCode: pg.reason, Reason: pg.message}
	if withItems {
		for _, it := range pg.group.Items {
			v.ItemIDs = append(v.ItemIDs, it.ID)
		}
	}
	return v
}

func (in AutoClearingInput) base(ledger Ledger) AutoClearingRun {
	run := AutoClearingRun{LedgerID: ledger.ID, Mode: in.Mode, ClearingDate: dateText(in.ClearingDate)}
	if in.RuleID != "" {
		id := in.RuleID
		run.RuleID = &id
	}
	return run
}

// RunAutoClearing evaluates the rules (FR-11.20). Simulation reads only and returns
// the proposed and skipped groups. Clear mode clears each group that nets to zero
// (or within the rule's tolerance) as its own clearing document in its own
// transaction, with the idempotency key auto:{rule}:{group hash}; a group that
// fails is logged as skipped and the run continues. It never makes a residual
// item. The run and its group log are stored under the request's Idempotency-Key:
// a repeat with the same body returns the stored run (created = false), another
// body is 409 LEDGER_IDEMPOTENCY_CONFLICT.
func (s *Postgres) RunAutoClearing(ctx context.Context, in AutoClearingInput) (AutoClearingRun, bool, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return AutoClearingRun{}, false, err
	}
	var ledger Ledger
	var rules []AutoClearingRule
	var planned []plannedGroup
	var unmatched int
	var truncated bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if ledger, err = loadLedger(ctx, tx, p.TenantID, in.LedgerID, false); err != nil {
			return err
		}
		if in.Mode == "clear" {
			existing, err := loadAutoRunByKey(ctx, tx, p.TenantID, ledger.ID, in.Key)
			if err == nil {
				if !bytes.Equal(existing.hash, in.Hash) {
					return httpx.NewProblem(http.StatusConflict, CodeIdempotencyConflict, "Idempotency-Key was already used with a different request body")
				}
				return errReplay{existing}
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if rules, err = s.loadRunRules(ctx, tx, p.TenantID, in); err != nil {
			return err
		}
		planned, unmatched, truncated, err = plan(ctx, tx, p.TenantID, rules)
		return err
	})
	var replay errReplay
	if errors.As(err, &replay) {
		return replay.run, false, nil
	}
	if err != nil {
		return AutoClearingRun{}, false, err
	}
	minor := money.MinorUnits(ledger.Currency)
	run := in.base(ledger)
	run.GroupsFound, run.UnmatchedItems, run.Truncated = len(planned), unmatched, truncated

	if in.Mode != "clear" {
		for i, pg := range planned {
			status := "skipped"
			if pg.clear {
				status = "proposed"
				run.GroupsCleared++
				run.ItemsCleared += len(pg.group.Items)
			} else {
				run.GroupsSkipped++
			}
			if i < MaxRunGroupsShown {
				run.Groups = append(run.Groups, pg.view(i+1, minor, status, true))
			}
		}
		return run, false, nil
	}

	results := make([]AutoClearingGroup, len(planned))
	for i, pg := range planned {
		g := pg.view(i+1, minor, "skipped", false)
		if pg.clear {
			date := in.ClearingDate
			if latest, perr := time.Parse(time.DateOnly, pg.group.Latest); perr == nil && latest.After(date) {
				date = latest
			}
			ci := ClearingInput{LedgerID: ledger.ID, ClearingDate: date, ToleranceGroup: pg.tolCode, TraceID: in.TraceID, Today: in.Today}
			for _, it := range pg.group.Items {
				ci.ItemIDs = append(ci.ItemIDs, it.ID)
			}
			var doc ClearingDocument
			cerr := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
				key, err := autoGroupKey(ctx, tx, p.TenantID, ledger.ID, "auto:"+pg.rule.ID+":"+pg.group.Hash)
				if err != nil {
					return err
				}
				hash := sha256.Sum256([]byte(key))
				ci.Key, ci.Hash = key, hash[:]
				doc, _, err = clearInTx(ctx, tx, p, ci, "auto")
				return err
			})
			if cerr == nil {
				g.Status = "cleared"
				g.ClearingDocumentID = &doc.ID
				run.GroupsCleared++
				run.ItemsCleared += len(pg.group.Items)
			} else if code, msg, ok := groupFailure(cerr); ok {
				g.ReasonCode, g.Reason = code, msg
				run.GroupsSkipped++
			} else {
				return AutoClearingRun{}, false, cerr
			}
		} else {
			run.GroupsSkipped++
		}
		results[i] = g
	}

	var stored AutoClearingRun
	var created bool
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run.ID = ids.New()
		_, err := tx.Exec(ctx, `insert into auto_clearing_run (tenant_id, id, legal_entity_id, ledger_id, rule_id, clearing_date, groups_found,
				groups_cleared, groups_skipped, items_cleared, unmatched_items, truncated, idempotency_key, payload_hash, created_by)
			values ($1, $2, $3, $4, $5, $6::text::date, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
			p.TenantID, run.ID, ledger.LegalEntityID, ledger.ID, run.RuleID, run.ClearingDate, run.GroupsFound, run.GroupsCleared,
			run.GroupsSkipped, run.ItemsCleared, run.UnmatchedItems, run.Truncated, in.Key, in.Hash, p.UserID)
		if err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, g := range results {
			batch.Queue(`insert into auto_clearing_run_group (tenant_id, legal_entity_id, run_id, group_no, rule_id, account, group_hash, item_count,
					net, status, reason_code, reason, clearing_document_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9::text::numeric, $10, nullif($11, ''), nullif($12, ''), $13)`,
				p.TenantID, ledger.LegalEntityID, run.ID, g.No, g.RuleID, g.Account, planned[g.No-1].group.Hash, g.ItemCount, g.Net, g.Status,
				g.ReasonCode, g.Reason, g.ClearingDocumentID)
		}
		br := tx.SendBatch(ctx, batch)
		for range results {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return err
			}
		}
		if err := br.Close(); err != nil {
			return err
		}
		stored, err = loadAutoRun(ctx, tx, p.TenantID, run.ID, true)
		created = true
		return err
	})
	if isUnique(err) {
		// The same key raced in: the other request's run is the answer.
		err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			var err error
			stored, err = loadAutoRunByKey(ctx, tx, p.TenantID, ledger.ID, in.Key)
			if err == nil && !bytes.Equal(stored.hash, in.Hash) {
				err = httpx.NewProblem(http.StatusConflict, CodeIdempotencyConflict, "Idempotency-Key was already used with a different request body")
			}
			if err == nil {
				stored.Groups, err = loadAutoRunGroups(ctx, tx, p.TenantID, stored.ID)
			}
			created = false
			return err
		})
	}
	return stored, created, err
}

// autoGroupKey is the idempotency key of one automatic group clearing:
// auto:{rule}:{group hash}, so a retried run finds the document it already made.
// When the same items were cleared under that key and the clearing was reset
// since, the key takes the next free :{n} suffix; otherwise the reset document
// would be replayed and the reopened items reported as cleared again.
func autoGroupKey(ctx context.Context, tx pgx.Tx, tenantID, ledgerID, base string) (string, error) {
	key := base
	for n := 1; ; n++ {
		var status string
		err := tx.QueryRow(ctx, `select status from clearing_document where tenant_id = $1 and ledger_id = $2 and idempotency_key = $3`,
			tenantID, ledgerID, key).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != "reset") {
			return key, nil
		}
		if err != nil {
			return "", err
		}
		key = base + ":" + strconv.Itoa(n)
	}
}

// errReplay carries a stored run out of a read-only transaction.
type errReplay struct{ run AutoClearingRun }

func (errReplay) Error() string { return "replay" }

// groupFailure turns a refused group clearing into a logged reason; any other
// error (a lost connection, say) aborts the run.
func groupFailure(err error) (code, msg string, ok bool) {
	var rej *domain.RejectedError
	if errors.As(err, &rej) && len(rej.Violations) > 0 {
		return rej.Violations[0].Code, rej.Violations[0].Message, true
	}
	var prob *httpx.Problem
	if errors.As(err, &prob) {
		return prob.Code, prob.Detail, true
	}
	return "", "", false
}

const autoRunSelect = `select id::text, ledger_id::text, rule_id::text, clearing_date::text, groups_found, groups_cleared, groups_skipped,
		items_cleared, unmatched_items, truncated, created_by, created_at, payload_hash from auto_clearing_run`

func scanAutoRun(row pgx.Row) (AutoClearingRun, error) {
	var r AutoClearingRun
	var created time.Time
	err := row.Scan(&r.ID, &r.LedgerID, &r.RuleID, &r.ClearingDate, &r.GroupsFound, &r.GroupsCleared, &r.GroupsSkipped, &r.ItemsCleared,
		&r.UnmatchedItems, &r.Truncated, &r.CreatedBy, &created, &r.hash)
	r.Mode, r.CreatedAt = "clear", &created
	return r, err
}

func loadAutoRunByKey(ctx context.Context, tx pgx.Tx, tenantID, ledgerID, key string) (AutoClearingRun, error) {
	r, err := scanAutoRun(tx.QueryRow(ctx, autoRunSelect+` where tenant_id = $1 and ledger_id = $2 and idempotency_key = $3`, tenantID, ledgerID, key))
	if err != nil {
		return r, err
	}
	r.Groups, err = loadAutoRunGroups(ctx, tx, tenantID, r.ID)
	return r, err
}

func loadAutoRun(ctx context.Context, tx pgx.Tx, tenantID, id string, withGroups bool) (AutoClearingRun, error) {
	r, err := scanAutoRun(tx.QueryRow(ctx, autoRunSelect+` where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, httpx.NewProblem(http.StatusNotFound, CodeAutoRunNotFound, "automatic clearing run not found")
	}
	if err != nil || !withGroups {
		return r, err
	}
	r.Groups, err = loadAutoRunGroups(ctx, tx, tenantID, id)
	return r, err
}

func loadAutoRunGroups(ctx context.Context, tx pgx.Tx, tenantID, runID string) ([]AutoClearingGroup, error) {
	rows, err := tx.Query(ctx, `select g.group_no, g.rule_id::text, g.account, g.item_count, g.net::text, g.status, coalesce(g.reason_code, ''),
			coalesce(g.reason, ''), g.clearing_document_id::text, l.currency::text
		from auto_clearing_run_group g
		join auto_clearing_run r on r.tenant_id = g.tenant_id and r.id = g.run_id
		join ledger l on l.tenant_id = r.tenant_id and l.id = r.ledger_id
		where g.tenant_id = $1 and g.run_id = $2 order by g.group_no limit $3`,
		tenantID, runID, MaxRunGroupsShown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutoClearingGroup{}
	for rows.Next() {
		var g AutoClearingGroup
		var currency string
		if err := rows.Scan(&g.No, &g.RuleID, &g.Account, &g.ItemCount, &g.Net, &g.Status, &g.ReasonCode, &g.Reason, &g.ClearingDocumentID, &currency); err != nil {
			return nil, err
		}
		net, err := decimal.NewFromString(g.Net)
		if err != nil {
			return nil, err
		}
		g.Net = net.StringFixed(money.MinorUnits(currency))
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListAutoClearingRuns returns the stored runs of a ledger, newest first, up to 100, without their group logs.
func (s *Postgres) ListAutoClearingRuns(ctx context.Context, ledgerID string) ([]AutoClearingRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []AutoClearingRun{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadLedger(ctx, tx, p.TenantID, ledgerID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, autoRunSelect+` where tenant_id = $1 and ledger_id = $2 order by created_at desc, id desc limit 100`, p.TenantID, ledgerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanAutoRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// GetAutoClearingRun returns a stored run with its group log (the first 1,000 groups).
func (s *Postgres) GetAutoClearingRun(ctx context.Context, id string) (AutoClearingRun, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return AutoClearingRun{}, err
	}
	var out AutoClearingRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = loadAutoRun(ctx, tx, p.TenantID, id, true)
		return err
	})
	return out, err
}
