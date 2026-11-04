package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	MaxGLGroupsPerRun = 200
	CodeGLFailed      = "PRODUCTION_GL_FAILED"

	glJournalSource    = "production-journal"
	glSettlementSource = "production-settlement"
)

type GLBooks interface {
	PostToGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error)
	ReconcileWIP(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (WIPReconciliation, error)
}

var _ GLBooks = (*Postgres)(nil)

type GLPostRequest struct {
	LegalEntityID string
	LedgerID      string
	OrderID       string
}

type GLBooked struct {
	Document string `json:"document"`
	EntryID  string `json:"entryId"`
	Lines    int    `json:"lines"`
}

type GLFailed struct {
	Document string `json:"document"`
	Code     string `json:"code"`
}

type GLPostResult struct {
	Booked    []GLBooked `json:"booked"`
	Failed    []GLFailed `json:"failed"`
	Remaining int        `json:"remaining"`
}

type glRow struct {
	id      string
	account string
	dr, cr  decimal.Decimal
}

type glGroup struct {
	orderID     string
	journalID   string
	document    string
	key         string
	sourceType  string
	postingDate string
	currency    string
	rows        []glRow
}

func (g *glGroup) entry(in GLPostRequest) ledgerclient.Entry {
	// The ledger takes a UUID document id: the journal, or the order for a settlement. The journal or order number
	// rides on each line's text so the GL still shows it.
	doc := g.journalID
	if doc == "" {
		doc = g.orderID
	}
	e := ledgerclient.Entry{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, DocumentID: doc, PostingDate: g.postingDate,
		Currency: g.currency, IdempotencyKey: g.key, SourceType: g.sourceType}
	for _, r := range g.rows {
		if r.cr.IsPositive() {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: r.account, Credit: true, Amount: r.cr, Text: g.document})
		} else {
			e.Lines = append(e.Lines, ledgerclient.Line{Account: r.account, Amount: r.dr, Text: g.document})
		}
	}
	return e
}

func (s *Postgres) unbookedGroups(ctx context.Context, tenantID string, in GLPostRequest) ([]*glGroup, error) {
	var out []*glGroup
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select w.id::text, w.order_id::text, coalesce(w.journal_id::text, ''), w.ledger_account, w.amount_dr::text, w.amount_cr::text,
				w.currency::text, w.posting_date::text, coalesce(j.journal_number, o.order_number)
			from production_wip_posting w
			join production_order o on o.tenant_id = w.tenant_id and o.id = w.order_id
			left join production_journal j on j.tenant_id = w.tenant_id and j.id = w.journal_id
			where w.tenant_id = $1 and w.legal_entity_id = $2 and w.journal_entry_id is null and ($3 = '' or w.order_id = $3::uuid)
			order by w.created_at, w.id`, tenantID, in.LegalEntityID, in.OrderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		byKey := map[string]*glGroup{}
		for rows.Next() {
			var r glRow
			var dr, cr string
			g := glGroup{}
			if err := rows.Scan(&r.id, &g.orderID, &g.journalID, &r.account, &dr, &cr, &g.currency, &g.postingDate, &g.document); err != nil {
				return err
			}
			r.dr, r.cr = dec(dr), dec(cr)
			g.key, g.sourceType = "production-journal:"+g.journalID, glJournalSource
			if g.journalID == "" {
				g.key, g.sourceType = "production-settlement:"+g.orderID, glSettlementSource
			}
			existing, ok := byKey[g.key]
			if !ok {
				existing = &g
				byKey[g.key] = existing
				out = append(out, existing)
			}
			existing.rows = append(existing.rows, r)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) markBooked(ctx context.Context, tenantID string, g *glGroup, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rowIDs := make([]string, len(g.rows))
		for i, r := range g.rows {
			rowIDs[i] = r.id
		}
		if _, err := tx.Exec(ctx, `update production_wip_posting set journal_entry_id = $3::uuid
			where tenant_id = $1 and id = any($2::uuid[]) and journal_entry_id is null`, tenantID, rowIDs, entryID); err != nil {
			return err
		}
		if g.journalID != "" {
			_, err := tx.Exec(ctx, `update production_journal set journal_entry_id = $3::uuid
				where tenant_id = $1 and id = $2::uuid and journal_entry_id is null`, tenantID, g.journalID, entryID)
			return err
		}
		_, err := tx.Exec(ctx, `update production_variance set journal_entry_id = $3::uuid
			where tenant_id = $1 and order_id = $2::uuid and journal_entry_id is null`, tenantID, g.orderID, entryID)
		return err
	})
}

// PostToGL books every WIP subledger posting not yet in the general ledger: one balanced entry per posted journal and one per
// settlement, keyed by the journal or order so a retry returns the same entry. A group the ledger refuses is reported and the rest go on.
func (s *Postgres) PostToGL(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return GLPostResult{}, errors.New("no principal in context")
	}
	groups, err := s.unbookedGroups(ctx, p.TenantID, in)
	if err != nil {
		return GLPostResult{}, err
	}
	res := GLPostResult{Booked: []GLBooked{}, Failed: []GLFailed{}}
	if len(groups) > MaxGLGroupsPerRun {
		res.Remaining, groups = len(groups)-MaxGLGroupsPerRun, groups[:MaxGLGroupsPerRun]
	}
	for _, g := range groups {
		entry := g.entry(in)
		entryID, err := ledger.Post(ctx, entry)
		if err == nil {
			err = s.markBooked(ctx, p.TenantID, g, entryID)
		}
		if err != nil {
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = CodeGLFailed
			}
			slog.ErrorContext(ctx, "production GL booking failed", "document", g.document, "key", g.key, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, GLFailed{Document: g.document, Code: code})
			continue
		}
		res.Booked = append(res.Booked, GLBooked{Document: g.document, EntryID: entryID, Lines: len(entry.Lines)})
	}
	return res, nil
}

type GLReconcileRequest struct {
	LegalEntityID string
	LedgerID      string
	Currency      string
	PeriodTo      string
}

type WIPAccountRecon struct {
	Account    string          `json:"account"`
	Subledger  decimal.Decimal `json:"subledger"`
	Unbooked   decimal.Decimal `json:"unbooked"`
	GL         decimal.Decimal `json:"gl"`
	Difference decimal.Decimal `json:"difference"`
}

type WIPReconciliation struct {
	Currency   string            `json:"currency"`
	Accounts   []WIPAccountRecon `json:"accounts"`
	Reconciled bool              `json:"reconciled"`
}

// ReconcileWIP compares, per WIP account, the net the subledger holds on the account (all open and ended orders of the legal entity,
// in one currency, up to the period) with the general ledger's balance of that account. The difference is GL minus subledger;
// unbooked is the part of the subledger the GL has not received yet.
func (s *Postgres) ReconcileWIP(ctx context.Context, ledger ledgerclient.Ledger, in GLReconcileRequest) (WIPReconciliation, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return WIPReconciliation{}, errors.New("no principal in context")
	}
	out := WIPReconciliation{Currency: in.Currency, Accounts: []WIPAccountRecon{}, Reconciled: true}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select ledger_account, sum(amount_dr - amount_cr)::text,
				coalesce(sum(amount_dr - amount_cr) filter (where journal_entry_id is null), 0)::text
			from production_wip_posting
			where tenant_id = $1 and legal_entity_id = $2 and wip and currency = $3 and ($4 = '' or to_char(posting_date, 'YYYY-MM') <= $4)
			group by ledger_account order by ledger_account`, p.TenantID, in.LegalEntityID, in.Currency, in.PeriodTo)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a WIPAccountRecon
			var sub, unbooked string
			if err := rows.Scan(&a.Account, &sub, &unbooked); err != nil {
				return err
			}
			a.Subledger, a.Unbooked = dec(sub), dec(unbooked)
			out.Accounts = append(out.Accounts, a)
		}
		return rows.Err()
	})
	if err != nil {
		return WIPReconciliation{}, err
	}
	for i := range out.Accounts {
		a := &out.Accounts[i]
		bal, err := ledger.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, Account: a.Account, PeriodTo: in.PeriodTo})
		if err != nil {
			return WIPReconciliation{}, fmt.Errorf("general ledger balance of %s: %w", a.Account, err)
		}
		if bal.Currency != in.Currency && !bal.Net().IsZero() {
			return WIPReconciliation{}, fmt.Errorf("general ledger balance of %s is in %s, not %s", a.Account, bal.Currency, in.Currency)
		}
		a.GL = bal.Net()
		a.Difference = a.GL.Sub(a.Subledger)
		out.Reconciled = out.Reconciled && a.Difference.IsZero()
	}
	return out, nil
}
