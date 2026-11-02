package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Positions                []inventory.Position
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

func (s *Postgres) ActivateVersion(ctx context.Context, in ActivationInput) (ActivationResult, bool, error) {
	var out ActivationResult
	var replayed bool
	err := s.tenantTx(ctx, func(tx pgx.Tx, p tenant.Principal) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, p.TenantID, in.Key, "version.activate", in.Fingerprint, func() (ActivationResult, error) {
			return activateInTx(ctx, tx, p, in)
		})
		return err
	})
	return out, replayed, err
}

func activateInTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in ActivationInput) (ActivationResult, error) {
	var res ActivationResult
	cur, err := scanVersion(tx.QueryRow(ctx, `select `+versionColumns+` from costing_version where tenant_id = $1 and id = $2 for update`, p.TenantID, in.VersionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return res, versionNotFound(in.VersionID)
	}
	if err != nil {
		return res, err
	}
	if cur.Version != in.Expected {
		return res, httpx.NewProblem(http.StatusConflict, domain.CodeVersionConflict, "current version is "+strconv.Itoa(cur.Version))
	}
	var free bool
	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtextextended($1, 0))`, domain.RollLockKey(p.TenantID, in.VersionID)).Scan(&free); err != nil {
		return res, err
	}
	if !free {
		return res, httpx.NewProblem(http.StatusConflict, domain.CodeVersionRolling, "version "+cur.Code+" is being rolled; activation blocked")
	}
	reversing := in.ReversesID != ""
	if !reversing {
		if _, err := domain.Transition(cur.Lifecycle(), domain.ActionActivate); err != nil {
			return res, err
		}
	}
	plan, err := planActivation(ctx, tx, p.TenantID, cur, in, true)
	if err != nil {
		return res, err
	}
	next, err := scanVersion(tx.QueryRow(ctx, `update costing_version
		set status = case when $3 then status else 'Active' end, version = version + 1,
		    activated_at = case when $3 then activated_at else now() end
		where tenant_id = $1 and id = $2 returning `+versionColumns, p.TenantID, in.VersionID, reversing))
	if err != nil {
		return res, err
	}
	a, err := insertActivation(ctx, tx, p, cur, in, plan)
	if err != nil {
		return res, err
	}
	if !reversing {
		if err := enqueue(ctx, tx, "costingversion", next.ID, costevents.VersionTransitioned, p, costevents.VersionTransitionedData{
			ID: next.ID, LegalEntityID: next.LegalEntityID, Code: next.Code, Type: next.Type, Action: domain.ActionActivate,
			FromStatus: cur.Status, ToStatus: next.Status, Version: next.Version, ActivatedAt: next.ActivatedAt, Actor: p.UserID,
		}); err != nil {
			return res, err
		}
	}
	if !plan.preview.Total.IsZero() {
		if a.JournalEntryID, err = postJournal(ctx, in, cur, a, plan); err != nil {
			return res, &LedgerRefusal{Err: err}
		}
		if _, err := tx.Exec(ctx, `update standard_cost_activation set journal_entry_id = $3 where tenant_id = $1 and id = $2`, p.TenantID, a.ID, a.JournalEntryID); err != nil {
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

func activationID(tenantID string, cur Version, in ActivationInput) string {
	name := tenantID + "|activate|" + cur.ID + "|" + strconv.Itoa(in.Expected)
	if in.ReversesID != "" {
		name = tenantID + "|reverse|" + in.ReversesID
	}
	return uuid.NewSHA1(activationNamespace, []byte(name)).String()
}

func insertActivation(ctx context.Context, tx pgx.Tx, p tenant.Principal, cur Version, in ActivationInput, plan activationPlan) (Activation, error) {
	a := Activation{ID: activationID(p.TenantID, cur, in), VersionID: cur.ID, LegalEntityID: cur.LegalEntityID, ReversesActivationID: nullable(in.ReversesID),
		LedgerID: in.LedgerID, PostingDate: in.PostingDate, Currency: in.Currency, ItemCount: len(plan.preview.Lines), Total: plan.preview.Total,
		InventoryAccount: plan.accounts.InventoryAccount, CostChangeAccount: plan.accounts.CostChangeAccount, JournalLines: plan.journalLines(),
		NewStandardForOpenOrders: in.NewStandardForOpenOrders, Lines: plan.preview.Lines, ActivatedBy: p.UserID}
	if plan.reverses != nil {
		a.LedgerID, a.Currency = plan.reverses.LedgerID, plan.reverses.Currency
	}
	lines, err := json.Marshal(a.Lines)
	if err != nil {
		return a, err
	}
	journal, err := json.Marshal(a.JournalLines)
	if err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `insert into standard_cost_activation
		(tenant_id, id, legal_entity_id, costing_version_id, reverses_activation_id, ledger_id, posting_date, currency, item_count, total,
		 inventory_account, cost_change_account, journal_lines, new_standard_for_open_orders, lines, activated_by)
		values ($1, $2, $3, $4, $5::uuid, $6, $7::date, $8, $9, $10, $11, $12, $13, $14, $15, $16) returning created_at`,
		p.TenantID, a.ID, a.LegalEntityID, a.VersionID, a.ReversesActivationID, a.LedgerID, a.PostingDate, a.Currency, a.ItemCount, a.Total,
		a.InventoryAccount, a.CostChangeAccount, journal, a.NewStandardForOpenOrders, lines, a.ActivatedBy).Scan(&a.CreatedAt)
	if pgCode(err) == uniqueViolation {
		return a, httpx.NewProblem(http.StatusConflict, domain.CodeReversalTarget, "activation "+in.ReversesID+" was already reversed")
	}
	return a, err
}

func postJournal(ctx context.Context, in ActivationInput, cur Version, a Activation, plan activationPlan) (string, error) {
	if plan.reverses != nil {
		return in.Ledger.Reverse(ctx, ledgerclient.Reversal{LegalEntityID: cur.LegalEntityID, EntryID: plan.reverses.JournalEntryID, PostingDate: in.PostingDate,
			Description: "Reversal of standard cost activation " + plan.reverses.ID, IdempotencyKey: "productcost.activation.reversal:" + plan.reverses.ID})
	}
	e := ledgerclient.Entry{LegalEntityID: cur.LegalEntityID, LedgerID: in.LedgerID, DocumentID: a.ID, PostingDate: in.PostingDate, Currency: in.Currency,
		IdempotencyKey: "productcost.activation:" + cur.ID + ":" + strconv.Itoa(in.Expected), SourceType: activationSourceType}
	for _, l := range a.JournalLines {
		e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: l.Amount, Text: "Standard cost revaluation " + cur.Code})
	}
	return in.Ledger.Post(ctx, e)
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
	std, err := storedStandards(ctx, tx, tenantID, cur, in.Positions)
	if err != nil {
		return plan, err
	}
	lines := make([]domain.RevalLine, len(in.Positions))
	for i, pos := range in.Positions {
		s := std[pos]
		lines[i] = domain.RevalLine{ItemID: pos.ItemID, SiteID: pos.SiteID, Qty: in.OnHand[pos], OldStd: s.old, NewStd: s.new}
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
	older, err := standardRows(ctx, tx, fmt.Sprintf(pick, `v.legal_entity_id::text = $5 and v.id <> $2 and v.status = 'Active' and v.type = 'Standard'`, `v.activated_at desc,`),
		tenantID, cur.ID, items, sites, cur.LegalEntityID)
	if err != nil {
		return nil, err
	}
	out := make(map[inventory.Position]standard, len(positions))
	for _, pos := range positions {
		n, hasNew := newer[pos]
		o, hasOld := older[pos]
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
