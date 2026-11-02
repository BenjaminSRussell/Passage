package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
)

const activationSourceType = "StandardCostActivation"

var activationNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("passage:productcost:standard-cost-activation"))

type PostedLine struct {
	Account string          `json:"account"`
	Credit  bool            `json:"credit"`
	Amount  decimal.Decimal `json:"amount"`
}

type Activation struct {
	ID                       string                    `json:"id"`
	VersionID                string                    `json:"costingVersionId"`
	LegalEntityID            string                    `json:"legalEntityId"`
	ReversesActivationID     *string                   `json:"reversesActivationId,omitempty"`
	LedgerID                 string                    `json:"ledgerId"`
	PostingDate              string                    `json:"postingDate"`
	Currency                 string                    `json:"currency"`
	ItemCount                int                       `json:"itemCount"`
	Total                    decimal.Decimal           `json:"total"`
	JournalEntryID           string                    `json:"journalEntryId,omitempty"`
	InventoryAccount         string                    `json:"inventoryAccount,omitempty"`
	CostChangeAccount        string                    `json:"costChangeAccount,omitempty"`
	JournalLines             []PostedLine              `json:"journalLines"`
	NewStandardForOpenOrders bool                      `json:"newStandardForOpenOrders"`
	Lines                    []domain.RevalPreviewLine `json:"lines"`
	ActivatedBy              string                    `json:"activatedBy"`
	CreatedAt                time.Time                 `json:"createdAt"`
}

type ActivationResult struct {
	Version    Version    `json:"version"`
	Activation Activation `json:"activation"`
}

type LedgerRefusal struct{ Err error }

func (e *LedgerRefusal) Error() string {
	return "ledger refused the activation journal: " + e.Err.Error()
}
func (e *LedgerRefusal) Unwrap() error { return e.Err }

type ActivationInput struct {
	VersionID                string
	Expected                 int
	Key                      string
	Fingerprint              any
	ReversesID               string
	LedgerID                 string
	PostingDate              string
	Currency                 string
	NewStandardForOpenOrders bool
	OnHand                   map[inventory.Position]decimal.Decimal
	Ledger                   ledgerclient.Ledger
}

const activationColumns = `id::text, costing_version_id::text, legal_entity_id::text, reverses_activation_id::text, ledger_id::text,
  to_char(posting_date, 'YYYY-MM-DD'), currency, item_count, total, coalesce(journal_entry_id, ''), inventory_account, cost_change_account,
  journal_lines, new_standard_for_open_orders, lines, activated_by, created_at`

func scanActivation(row pgx.Row) (Activation, error) {
	var a Activation
	var lines, journal []byte
	err := row.Scan(&a.ID, &a.VersionID, &a.LegalEntityID, &a.ReversesActivationID, &a.LedgerID, &a.PostingDate, &a.Currency,
		&a.ItemCount, &a.Total, &a.JournalEntryID, &a.InventoryAccount, &a.CostChangeAccount, &journal, &a.NewStandardForOpenOrders, &lines,
		&a.ActivatedBy, &a.CreatedAt)
	if err != nil {
		return a, err
	}
	return a, errors.Join(json.Unmarshal(lines, &a.Lines), json.Unmarshal(journal, &a.JournalLines))
}

func activationNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeReversalTarget, "activation "+id+" not found")
}

func (s *Postgres) GetActivation(ctx context.Context, id string) (Activation, error) {
	var out Activation
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		a, err := scanActivation(tx.QueryRow(ctx, `select `+activationColumns+` from standard_cost_activation where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return activationNotFound(id)
		}
		out = a
		return err
	})
	return out, err
}

func (s *Postgres) ActivationPositions(ctx context.Context, versionID string) ([]inventory.Position, error) {
	var out []inventory.Position
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, err = versionPositions(ctx, tx, p.TenantID, versionID)
		return err
	})
	return out, err
}

func versionPositions(ctx context.Context, tx pgx.Tx, tenantID, versionID string) ([]inventory.Position, error) {
	rows, err := tx.Query(ctx, `select distinct item_id::text, site_id::text from cost_calculation
		where tenant_id = $1 and costing_version_id = $2 and valuation_view = 'Legal' and status in ('Pending', 'Active') order by 1, 2`, tenantID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inventory.Position
	for rows.Next() {
		var pos inventory.Position
		if err := rows.Scan(&pos.ItemID, &pos.SiteID); err != nil {
			return nil, err
		}
		out = append(out, pos)
	}
	return out, rows.Err()
}

type activationPlan struct {
	preview  domain.RevalPreview
	accounts PostingAccounts
	reverses *Activation
}

func (s *Postgres) PreviewActivation(ctx context.Context, in ActivationInput) (domain.RevalPreview, error) {
	var out domain.RevalPreview
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		cur, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2`, p.TenantID, in.VersionID))
		if errors.Is(err, pgx.ErrNoRows) {
			return versionNotFound(in.VersionID)
		}
		if err != nil {
			return err
		}
		plan, err := planActivation(ctx, tx, p.TenantID, cur, in, false)
		out = plan.preview
		return err
	})
	return out, err
}

const activateScope = "version.activate"

type pendingActivation struct {
	Activation      Activation `json:"activation"`
	ReversesEntryID string     `json:"reversesEntryId,omitempty"`
}

type preparedActivation struct {
	cur       Version
	pending   pendingActivation
	post      bool
	ambiguous bool
}

func attemptKey(versionID string, in ActivationInput) string {
	if in.ReversesID != "" {
		return "reverse:" + in.ReversesID
	}
	return "activate:" + versionID
}

// ActivateVersion settles in three steps. The first transaction checks
// everything and commits the pending attempt (the exact journal to post); the
// ledger is then posted with that attempt's own ids and values; the second
// transaction records the activation. A crash or failed commit after the post
// leaves the attempt, and the next call posts it verbatim so the ledger replays
// the entry instead of booking a second one.
func (s *Postgres) ActivateVersion(ctx context.Context, in ActivationInput) (ActivationResult, bool, error) {
	var replay ActivationResult
	var found bool
	var prepared preparedActivation
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		if replay, found, err = storedReplay[ActivationResult](ctx, tx, p.TenantID, in.Key, activateScope, in.Fingerprint); err != nil || found {
			return err
		}
		prepared, err = prepareActivation(ctx, tx, p, in)
		return err
	})
	if err != nil {
		return ActivationResult{}, false, err
	}
	if found {
		return replay, true, nil
	}
	settle := context.WithoutCancel(ctx)
	if prepared.post {
		entryID, err := postPending(settle, in.Ledger, prepared)
		if err != nil {
			s.recordFailedAttempt(settle, in, prepared, err)
			return ActivationResult{}, false, &LedgerRefusal{Err: err}
		}
		prepared.pending.Activation.JournalEntryID = entryID
	}
	var out ActivationResult
	var replayed bool
	err = s.tenantTx(settle, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(settle, tx, p.TenantID, in.Key, activateScope, in.Fingerprint, func() (ActivationResult, error) {
			return finalizeActivation(settle, tx, p, in, prepared)
		})
		return err
	})
	return out, replayed, err
}

func prepareActivation(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ActivationInput) (preparedActivation, error) {
	var out preparedActivation
	cur, err := lockVersion(ctx, tx, p.TenantID, in)
	if err != nil {
		return out, err
	}
	var free bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtextextended($1, 0))`, domain.RollLockKey(p.TenantID, in.VersionID)).Scan(&free); err != nil {
		return out, err
	}
	if !free {
		return out, httpx.NewProblem(http.StatusConflict, domain.CodeVersionRolling, "version "+cur.Code+" is being rolled; activation blocked")
	}
	if in.ReversesID == "" {
		if _, err := domain.Transition(cur.Lifecycle(), domain.ActionActivate); err != nil {
			return out, err
		}
		if cur.Type != domain.VersionStandard {
			return out, &domain.CodedError{Code: domain.CodeNotStandard, Detail: "only a Standard version revalues the inventory; version " + cur.Code + " is " + cur.Type}
		}
	}
	out.cur = cur
	key := attemptKey(cur.ID, in)
	var raw []byte
	err = tx.QueryRow(ctx, `select payload, attempted_ambiguous from standard_cost_activation_attempt where tenant_id = $1 and attempt_key = $2`, p.TenantID, key).Scan(&raw, &out.ambiguous)
	if err == nil {
		out.post = true
		return out, json.Unmarshal(raw, &out.pending)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	plan, err := planActivation(ctx, tx, p.TenantID, cur, in, true)
	if err != nil {
		return out, err
	}
	out.pending.Activation = newActivation(p, cur, in, plan)
	out.post = !plan.preview.Total.IsZero()
	if plan.reverses != nil {
		out.pending.ReversesEntryID = plan.reverses.JournalEntryID
	}
	if !out.post {
		return out, nil
	}
	payload, err := json.Marshal(out.pending)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `insert into standard_cost_activation_attempt (tenant_id, attempt_key, legal_entity_id, costing_version_id, payload) values ($1, $2, $3, $4, $5)`,
		p.TenantID, key, cur.LegalEntityID, cur.ID, payload)
	return out, err
}

func lockVersion(ctx context.Context, tx pgx.Tx, tenantID string, in ActivationInput) (Version, error) {
	cur, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2 for update`, tenantID, in.VersionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return cur, versionNotFound(in.VersionID)
	}
	if err != nil {
		return cur, err
	}
	if cur.Version != in.Expected {
		return cur, httpx.NewProblem(http.StatusConflict, domain.CodeVersionConflict, "current version is "+strconv.Itoa(cur.Version))
	}
	return cur, nil
}

func finalizeActivation(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ActivationInput, prepared preparedActivation) (ActivationResult, error) {
	var res ActivationResult
	if _, err := lockVersion(ctx, tx, p.TenantID, in); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `delete from standard_cost_activation_attempt where tenant_id = $1 and attempt_key = $2`, p.TenantID, attemptKey(in.VersionID, in)); err != nil {
		return res, err
	}
	reversing := in.ReversesID != ""
	next, err := scanVersion(tx.QueryRow(ctx, `update costing_version
		set status = case when $3 then status else 'Active' end, version = version + 1,
		    activated_at = case when $3 then activated_at else now() end
		where tenant_id = $1 and id = $2 returning `+versionColumns, p.TenantID, in.VersionID, reversing))
	if err != nil {
		return res, err
	}
	a, err := insertActivation(ctx, tx, p, prepared.pending.Activation)
	if err != nil {
		return res, err
	}
	if !reversing {
		if err := enqueue(ctx, tx, "costingversion", next.ID, costevents.VersionTransitioned, p, costevents.VersionTransitionedData{
			ID: next.ID, LegalEntityID: next.LegalEntityID, Code: next.Code, Type: next.Type, Action: domain.ActionActivate,
			FromStatus: prepared.cur.Status, ToStatus: next.Status, Version: next.Version, ActivatedAt: next.ActivatedAt, Actor: p.UserID,
		}); err != nil {
			return res, err
		}
	}
	err = enqueue(ctx, tx, "standardcostactivation", a.ID, costevents.StandardCostActivated, p, costevents.StandardCostActivatedData{
		ActivationID: a.ID, CostingVersionID: a.VersionID, LegalEntityID: a.LegalEntityID, ReversesActivationID: in.ReversesID, ItemCount: a.ItemCount,
		RevaluationJournalID: a.JournalEntryID, TotalRevaluation: a.Total.String(), Currency: a.Currency, PostingDate: a.PostingDate,
		NewStandardForOpenOrders: a.NewStandardForOpenOrders, Actor: p.UserID,
	})
	return ActivationResult{Version: next, Activation: a}, err
}

// recordFailedAttempt keeps the pending attempt after a failed post so the next
// call resends it verbatim. A definite refusal (ledger 400, 404, 422 or a
// locally invalid entry) on an attempt never sent ambiguously drops it, so a
// corrected request can post afresh; any other failure may have reached the
// ledger (a 401, 403 or 429 is decided before its idempotency replay), so the
// attempt is flagged ambiguous and no later refusal drops it.
func (s *Postgres) recordFailedAttempt(ctx context.Context, in ActivationInput, prepared preparedActivation, cause error) {
	definite := definiteRefusal(cause)
	if definite && prepared.ambiguous {
		return
	}
	statement := `update standard_cost_activation_attempt set attempted_ambiguous = true where tenant_id = $1 and attempt_key = $2`
	if definite {
		statement = `delete from standard_cost_activation_attempt where tenant_id = $1 and attempt_key = $2`
	} else if prepared.ambiguous {
		return
	}
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		_, err := tx.Exec(ctx, statement, p.TenantID, attemptKey(prepared.cur.ID, in))
		return err
	})
	if err != nil {
		slog.ErrorContext(ctx, "pending activation attempt not updated after a ledger failure", "versionId", prepared.cur.ID, "err", err)
	}
}

func definiteRefusal(err error) bool {
	if errors.Is(err, ledgerclient.ErrUnbalanced) || errors.Is(err, ledgerclient.ErrEmptyEntry) {
		return true
	}
	var le *ledgerclient.Error
	if !errors.As(err, &le) {
		return false
	}
	return le.Status == http.StatusBadRequest || le.Status == http.StatusNotFound || le.Status == http.StatusUnprocessableEntity
}

func activationID(tenantID string, cur Version, in ActivationInput) string {
	name := tenantID + "|activate|" + cur.ID + "|" + strconv.Itoa(in.Expected)
	if in.ReversesID != "" {
		name = tenantID + "|reverse|" + in.ReversesID
	}
	return uuid.NewSHA1(activationNamespace, []byte(name)).String()
}

func newActivation(p tenant.Principal, cur Version, in ActivationInput, plan activationPlan) Activation {
	a := Activation{ID: activationID(p.TenantID, cur, in), VersionID: cur.ID, LegalEntityID: cur.LegalEntityID, ReversesActivationID: nullable(in.ReversesID),
		LedgerID: in.LedgerID, PostingDate: in.PostingDate, Currency: in.Currency, ItemCount: len(plan.preview.Lines), Total: plan.preview.Total,
		InventoryAccount: plan.accounts.InventoryAccount, CostChangeAccount: plan.accounts.CostChangeAccount, JournalLines: plan.journalLines(),
		NewStandardForOpenOrders: in.NewStandardForOpenOrders, Lines: plan.preview.Lines, ActivatedBy: p.UserID}
	if plan.reverses != nil {
		a.LedgerID, a.Currency = plan.reverses.LedgerID, plan.reverses.Currency
	}
	return a
}

func insertActivation(ctx context.Context, tx pgx.Tx, p tenant.Principal, a Activation) (Activation, error) {
	lines, err := json.Marshal(a.Lines)
	if err != nil {
		return a, err
	}
	journal, err := json.Marshal(a.JournalLines)
	if err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `insert into standard_cost_activation
		(tenant_id, id, legal_entity_id, costing_version_id, reverses_activation_id, ledger_id, posting_date, currency, item_count, total, journal_entry_id,
		 inventory_account, cost_change_account, journal_lines, new_standard_for_open_orders, lines, activated_by)
		values ($1, $2, $3, $4, $5::uuid, $6, $7::date, $8, $9, $10, nullif($11, ''), $12, $13, $14, $15, $16, $17) returning created_at`,
		p.TenantID, a.ID, a.LegalEntityID, a.VersionID, a.ReversesActivationID, a.LedgerID, a.PostingDate, a.Currency, a.ItemCount, a.Total, a.JournalEntryID,
		a.InventoryAccount, a.CostChangeAccount, journal, a.NewStandardForOpenOrders, lines, a.ActivatedBy).Scan(&a.CreatedAt)
	if pgCode(err) == uniqueViolation {
		return a, httpx.NewProblem(http.StatusConflict, domain.CodeReversalTarget, "activation "+a.ID+" was already recorded or its target already reversed")
	}
	return a, err
}

func postPending(ctx context.Context, ledger ledgerclient.Ledger, prepared preparedActivation) (string, error) {
	a := prepared.pending.Activation
	if a.ReversesActivationID != nil {
		return ledger.Reverse(ctx, ledgerclient.Reversal{LegalEntityID: a.LegalEntityID, EntryID: prepared.pending.ReversesEntryID, PostingDate: a.PostingDate,
			Description: "Reversal of standard cost activation " + *a.ReversesActivationID, IdempotencyKey: "productcost.activation.reversal:" + *a.ReversesActivationID})
	}
	e := ledgerclient.Entry{LegalEntityID: a.LegalEntityID, LedgerID: a.LedgerID, DocumentID: a.ID, PostingDate: a.PostingDate, Currency: a.Currency,
		IdempotencyKey: "productcost.activation:" + a.ID, SourceType: activationSourceType}
	for _, l := range a.JournalLines {
		e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: l.Amount, Text: "Standard cost revaluation " + prepared.cur.Code})
	}
	return ledger.Post(ctx, e)
}

func (p activationPlan) journalLines() []PostedLine {
	out := []PostedLine{}
	if p.reverses != nil {
		for _, l := range p.reverses.JournalLines {
			l.Credit = !l.Credit
			out = append(out, l)
		}
		return out
	}
	accounts := map[string]string{domain.AccountInventory: p.accounts.InventoryAccount, domain.AccountCostChangeVar: p.accounts.CostChangeAccount}
	for _, jl := range p.preview.Journal {
		l := PostedLine{Account: accounts[jl.Account], Amount: jl.Debit}
		if !jl.Credit.IsZero() {
			l.Credit, l.Amount = true, jl.Credit
		}
		out = append(out, l)
	}
	return out
}

func planActivation(ctx context.Context, tx pgx.Tx, tenantID string, cur Version, in ActivationInput, withAccounts bool) (activationPlan, error) {
	var plan activationPlan
	if in.ReversesID != "" {
		orig, err := loadReversible(ctx, tx, tenantID, cur, in.ReversesID)
		if err != nil {
			return plan, err
		}
		plan.reverses = &orig
		plan.preview = domain.Revalue(domain.ReversalLines(orig.Lines), orig.Currency)
		plan.accounts = PostingAccounts{LegalEntityID: orig.LegalEntityID, InventoryAccount: orig.InventoryAccount, CostChangeAccount: orig.CostChangeAccount}
		return plan, nil
	}
	positions, err := versionPositions(ctx, tx, tenantID, cur.ID)
	if err != nil {
		return plan, err
	}
	if len(positions) == 0 {
		return plan, &domain.CodedError{Code: domain.CodeNoStoredStandard, Detail: "version " + cur.Code + " has no stored calculations to revalue"}
	}
	std, err := storedStandards(ctx, tx, tenantID, cur, positions)
	if err != nil {
		return plan, err
	}
	lines := make([]domain.RevalLine, len(positions))
	for i, pos := range positions {
		qty, ok := in.OnHand[pos]
		if !ok {
			return plan, &domain.CodedError{Code: domain.CodeNoOnHand, Detail: "the inventory reports no on-hand for item " + pos.ItemID + " at site " + pos.SiteID +
				"; inventory must carry the site's id in its site dimension"}
		}
		s := std[pos]
		lines[i] = domain.RevalLine{ItemID: pos.ItemID, SiteID: pos.SiteID, Qty: qty, OldStd: s.old, NewStd: s.new}
	}
	plan.preview = domain.Revalue(lines, in.Currency)
	if withAccounts && !plan.preview.Total.IsZero() {
		plan.accounts, err = postingAccountsIn(ctx, tx, tenantID, cur.LegalEntityID)
	}
	return plan, err
}

func loadReversible(ctx context.Context, tx pgx.Tx, tenantID string, cur Version, reversesID string) (Activation, error) {
	if cur.Status != domain.VersionActive {
		return Activation{}, &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "only an Active version's activation can be reversed"}
	}
	orig, err := scanActivation(tx.QueryRow(ctx, `select `+activationColumns+` from standard_cost_activation where tenant_id = $1 and id = $2`, tenantID, reversesID))
	if errors.Is(err, pgx.ErrNoRows) {
		return orig, activationNotFound(reversesID)
	}
	if err != nil {
		return orig, err
	}
	if orig.VersionID != cur.ID {
		return orig, &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "activation " + reversesID + " is not an activation of this version"}
	}
	if orig.ReversesActivationID != nil {
		return orig, &domain.CodedError{Code: domain.CodeReversalTarget, Detail: "activation " + reversesID + " is itself a reversal"}
	}
	var reversed bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from standard_cost_activation where tenant_id = $1 and reverses_activation_id = $2)`, tenantID, reversesID).Scan(&reversed); err != nil {
		return orig, err
	}
	if reversed {
		return orig, httpx.NewProblem(http.StatusConflict, domain.CodeReversalTarget, "activation "+reversesID+" was already reversed")
	}
	return orig, nil
}

type standard struct{ old, new decimal.Decimal }

// storedStandards pairs each position's new standard (this version's Legal
// calculation) with the standard the stock is carried at now: the new standard
// of the latest activation that booked the position and was not reversed, else
// the calculation of the latest activated Standard version whose activation was
// not reversed. A position without both is refused.
func storedStandards(ctx context.Context, tx pgx.Tx, tenantID string, cur Version, positions []inventory.Position) (map[inventory.Position]standard, error) {
	items, sites := make([]string, len(positions)), make([]string, len(positions))
	for i, pos := range positions {
		items[i], sites[i] = pos.ItemID, pos.SiteID
	}
	const pick = `select distinct on (c.item_id, c.site_id) c.item_id::text, c.site_id::text, c.unit_cost
		from cost_calculation c join costing_version v on v.tenant_id = c.tenant_id and v.id = c.costing_version_id
		where c.tenant_id = $1 and c.valuation_view = 'Legal' and c.status in ('Pending', 'Active') and %s
		  and (c.item_id, c.site_id) in (select * from unnest($3::uuid[], $4::uuid[]))
		order by c.item_id, c.site_id, %s c.created_at desc, c.id desc`
	newer, err := standardRows(ctx, tx, fmt.Sprintf(pick, `v.id = $2`, ``), tenantID, cur.ID, items, sites)
	if err != nil {
		return nil, err
	}
	const notReversed = `not exists (select 1 from standard_cost_activation r where r.tenant_id = a.tenant_id and r.reverses_activation_id = a.id)`
	booked, err := standardRows(ctx, tx, `select distinct on (l->>'itemId', l->>'siteId') l->>'itemId', l->>'siteId', (l->>'newStd')::numeric
		from standard_cost_activation a cross join lateral jsonb_array_elements(a.lines) l
		where a.tenant_id = $1 and a.legal_entity_id::text = $2 and a.reverses_activation_id is null and `+notReversed+`
		  and (l->>'itemId', l->>'siteId') in (select * from unnest($3::text[], $4::text[]))
		order by l->>'itemId', l->>'siteId', a.created_at desc, a.id desc`, tenantID, cur.LegalEntityID, items, sites)
	if err != nil {
		return nil, err
	}
	older, err := standardRows(ctx, tx, fmt.Sprintf(pick, `v.legal_entity_id::text = $5 and v.id <> $2 and v.status = 'Active' and v.type = 'Standard'
		and not exists (select 1 from standard_cost_activation a where a.tenant_id = v.tenant_id and a.costing_version_id = v.id and not `+notReversed+`)`,
		`v.activated_at desc,`), tenantID, cur.ID, items, sites, cur.LegalEntityID)
	if err != nil {
		return nil, err
	}
	out := make(map[inventory.Position]standard, len(positions))
	for _, pos := range positions {
		n, hasNew := newer[pos]
		o, hasOld := booked[pos]
		if !hasOld {
			o, hasOld = older[pos]
		}
		if !hasNew || !hasOld {
			return nil, &domain.CodedError{Code: domain.CodeNoStoredStandard, Detail: "item " + pos.ItemID + " at site " + pos.SiteID +
				" has no stored standard in this version or no earlier standard in an Active version; nothing is revalued without both"}
		}
		out[pos] = standard{old: o, new: n}
	}
	return out, nil
}

func standardRows(ctx context.Context, tx pgx.Tx, query string, args ...any) (map[inventory.Position]decimal.Decimal, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[inventory.Position]decimal.Decimal{}
	for rows.Next() {
		var pos inventory.Position
		var cost decimal.Decimal
		if err := rows.Scan(&pos.ItemID, &pos.SiteID, &cost); err != nil {
			return nil, err
		}
		out[pos] = cost
	}
	return out, rows.Err()
}
