package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

const (
	MaxVouchersPerRun  = 200
	CodeGLFailed       = "CST-GL-001"
	CodeNoProfile      = "CST-GL-002"
	glSourceType       = "costing-voucher"
	invoicePPVPrefix   = "ppv:INVOICE:"
	CodeVoucherParked  = "CST-GL-004"
	CodeLedgerForeign  = "CST-GL-005"
	CodeUnbalanced     = "CST-GL-006"
	MaxVoucherAttempts = 5
	voucherPosted      = "POSTED"
	voucherParked      = "PARKED"
)

// PostingProfile is the stored configuration of a legal entity's vouchers: the general ledger they post in
// and the accounts of inventory, GR/IR, accounts payable, PPV and the close adjustment.
type PostingProfile struct {
	LegalEntityID     string    `json:"legalEntityId"`
	GLLedgerID        string    `json:"ledgerId"`
	InventoryAccount  string    `json:"inventoryAccount"`
	GRIRAccount       string    `json:"grirAccount"`
	APAccount         string    `json:"apAccount"`
	PPVAccount        string    `json:"ppvAccount"`
	AdjustmentAccount string    `json:"adjustmentAccount"`
	Version           int       `json:"version"`
	UpdatedBy         string    `json:"updatedBy,omitempty"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func (p PostingProfile) accounts() domain.PostingAccounts {
	return domain.PostingAccounts{Inventory: p.InventoryAccount, GRIR: p.GRIRAccount, AP: p.APAccount, PPV: p.PPVAccount, Adjustment: p.AdjustmentAccount}
}

const profileColumns = `legal_entity_id::text, gl_ledger_id::text, inventory_account, grir_account, ap_account, ppv_account, adjustment_account, version, updated_by, updated_at`

func scanProfile(row pgx.Row) (PostingProfile, error) {
	var p PostingProfile
	err := row.Scan(&p.LegalEntityID, &p.GLLedgerID, &p.InventoryAccount, &p.GRIRAccount, &p.APAccount, &p.PPVAccount, &p.AdjustmentAccount, &p.Version, &p.UpdatedBy, &p.UpdatedAt)
	return p, err
}

func loadProfile(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (PostingProfile, error) {
	p, err := scanProfile(tx.QueryRow(ctx, `select `+profileColumns+` from costing_posting_profile where tenant_id = $1 and legal_entity_id = $2`, tenantID, legalEntityID))
	if errors.Is(err, pgx.ErrNoRows) {
		return PostingProfile{}, httpx.NewProblem(http.StatusConflict, CodeNoProfile, "the legal entity has no costing posting profile")
	}
	return p, err
}

func (s *Postgres) GetPostingProfile(ctx context.Context, legalEntityID string) (PostingProfile, error) {
	t, _ := tenant.FromContext(ctx)
	var out PostingProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadProfile(ctx, tx, t.TenantID, legalEntityID)
		return err
	})
	return out, err
}

func (s *Postgres) SavePostingProfile(ctx context.Context, in PostingProfile, actor string) (PostingProfile, error) {
	t, _ := tenant.FromContext(ctx)
	var out PostingProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanProfile(tx.QueryRow(ctx, `insert into costing_posting_profile (tenant_id, legal_entity_id, gl_ledger_id, inventory_account, grir_account, ap_account,
				ppv_account, adjustment_account, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			on conflict (tenant_id, legal_entity_id) do update set gl_ledger_id = excluded.gl_ledger_id, inventory_account = excluded.inventory_account,
				grir_account = excluded.grir_account, ap_account = excluded.ap_account, ppv_account = excluded.ppv_account,
				adjustment_account = excluded.adjustment_account, updated_by = excluded.updated_by, updated_at = now(),
				version = costing_posting_profile.version + 1
			returning `+profileColumns,
			t.TenantID, in.LegalEntityID, in.GLLedgerID, in.InventoryAccount, in.GRIRAccount, in.APAccount, in.PPVAccount, in.AdjustmentAccount, actor))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update costing_gl_voucher set status = 'PENDING', attempt_count = 0, next_attempt_at = now(), parked_reason = null, rebuild = true
			where tenant_id = $1 and legal_entity_id = $2 and status = 'PARKED'`, t.TenantID, in.LegalEntityID)
		return err
	})
	return out, err
}

// voucher is the exact payload of one ledger entry, stored before the ledger is called.
type voucher struct {
	legalEntityID, sourceKey, glLedgerID, currency, date, key string
	lines                                                     []domain.VoucherLine
}

func (v voucher) entry() ledgerclient.Entry {
	e := ledgerclient.Entry{LegalEntityID: v.legalEntityID, LedgerID: v.glLedgerID, DocumentID: v.sourceKey, PostingDate: v.date,
		Currency: v.currency, IdempotencyKey: v.key, SourceType: glSourceType}
	for _, l := range v.lines {
		e.Lines = append(e.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: l.Amount})
	}
	return e
}

// stageVoucher stores v as PENDING unless its source key already has a voucher, and returns the stored one with its
// status and journal entry id: a retry resends what the first attempt stored.
func (s *Postgres) stageVoucher(ctx context.Context, v voucher) (stored voucher, status, entryID string, err error) {
	t, _ := tenant.FromContext(ctx)
	raw, err := json.Marshal(v.lines)
	if err != nil {
		return voucher{}, "", "", err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into costing_gl_voucher (tenant_id, id, legal_entity_id, source_key, gl_ledger_id, currency, posting_date, idempotency_key, lines)
			values ($1, $2, $3, $4, $5, $6, $7::date, $8, $9)
			on conflict (tenant_id, source_key) do update set lines = excluded.lines, gl_ledger_id = excluded.gl_ledger_id, currency = excluded.currency,
				posting_date = excluded.posting_date, idempotency_key = excluded.idempotency_key || '-' || costing_gl_voucher.attempt_count, rebuild = false
				where costing_gl_voucher.rebuild and costing_gl_voucher.status = 'PENDING'`,
			t.TenantID, ids.New(), v.legalEntityID, v.sourceKey, v.glLedgerID, v.currency, v.date, v.key, raw); err != nil {
			return err
		}
		stored = voucher{legalEntityID: v.legalEntityID, sourceKey: v.sourceKey}
		var lines []byte
		if err := tx.QueryRow(ctx, `select gl_ledger_id::text, currency, posting_date::text, idempotency_key, lines, status, coalesce(journal_entry_id, '')
			from costing_gl_voucher where tenant_id = $1 and source_key = $2`, t.TenantID, v.sourceKey).
			Scan(&stored.glLedgerID, &stored.currency, &stored.date, &stored.key, &lines, &status, &entryID); err != nil {
			return err
		}
		return json.Unmarshal(lines, &stored.lines)
	})
	return stored, status, entryID, err
}

func (s *Postgres) markVoucherPosted(ctx context.Context, sourceKey, entryID string) error {
	t, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update costing_gl_voucher set status = 'POSTED', journal_entry_id = $3, posted_at = now() where tenant_id = $1 and source_key = $2`,
			t.TenantID, sourceKey, entryID)
		return err
	})
}

func (s *Postgres) recordVoucherFailure(ctx context.Context, sourceKey, code string, refusedForGood, park bool) error {
	t, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update costing_gl_voucher set attempt_count = attempt_count + 1, last_error = $3, rebuild = rebuild or $4,
				next_attempt_at = now() + make_interval(secs => least(3600, 60 * power(2, attempt_count))::double precision),
				status = case when $5 or attempt_count + 1 >= $6 then 'PARKED' else status end,
				parked_reason = case when $5 or attempt_count + 1 >= $6 then $3 else parked_reason end
			where tenant_id = $1 and source_key = $2 and status = 'PENDING'`, t.TenantID, sourceKey, code, refusedForGood, park, MaxVoucherAttempts)
		return err
	})
}

// CheckLedgerOwner refuses (409) a general ledger that does not belong to the legal entity.
func CheckLedgerOwner(ctx context.Context, ledger ledgerclient.Ledger, legalEntityID, ledgerID string) error {
	owner, err := ledger.LegalEntity(ctx, legalEntityID, ledgerID)
	if err != nil {
		slog.ErrorContext(ctx, "costing cannot read the ledger", "ledger", ledgerID, "err", err, "correlationId", middleware.GetReqID(ctx))
		var le *ledgerclient.Error
		if errors.As(err, &le) && (le.Status == http.StatusNotFound || le.Status == http.StatusUnprocessableEntity) {
			return httpx.NewProblem(http.StatusConflict, CodeLedgerForeign, "the ledger is unknown or not readable for this legal entity")
		}
		return httpx.NewProblem(http.StatusBadGateway, CodeGLFailed, "the general ledger did not answer the ledger lookup")
	}
	if !strings.EqualFold(owner, legalEntityID) {
		return httpx.NewProblem(http.StatusConflict, CodeLedgerForeign, "the ledger belongs to another legal entity")
	}
	return nil
}

// postVoucher books v once: a voucher the ledger already confirmed returns its entry id, a pending one is resent unchanged, and a
// failure counts an attempt with backoff. A refusal other than a closed period marks the voucher to be derived again from the
// current profile; one that cannot balance, or the fifth failure, parks it.
func (s *Postgres) postVoucher(ctx context.Context, ledger ledgerclient.Ledger, v voucher) (string, error) {
	stored, status, entryID, err := s.stageVoucher(ctx, v)
	if err != nil {
		return "", err
	}
	switch status {
	case voucherPosted:
		return entryID, nil
	case voucherParked:
		return "", httpx.NewProblem(http.StatusConflict, CodeVoucherParked, "the voucher is parked; save the posting profile to retry it")
	}
	if err := stored.entry().Validate(); err != nil {
		if rerr := s.recordVoucherFailure(ctx, v.sourceKey, CodeUnbalanced, false, true); rerr != nil {
			slog.ErrorContext(ctx, "costing voucher failure not recorded", "sourceKey", v.sourceKey, "err", rerr, "correlationId", middleware.GetReqID(ctx))
		}
		return "", err
	}
	entryID, err = ledger.Post(ctx, stored.entry())
	if err != nil {
		code := glCode(err)
		if rerr := s.recordVoucherFailure(ctx, v.sourceKey, code, ledgerclient.Rejected(err) && code != ledgerclient.CodePeriodClosed, false); rerr != nil {
			slog.ErrorContext(ctx, "costing voucher failure not recorded", "sourceKey", v.sourceKey, "err", rerr, "correlationId", middleware.GetReqID(ctx))
		}
		return "", err
	}
	return entryID, s.markVoucherPosted(ctx, v.sourceKey, entryID)
}

func glCode(err error) string {
	if code := ledgerclient.CodeOf(err); code != "" {
		return code
	}
	return CodeGLFailed
}

type GLPostRequest struct {
	LegalEntityID string
}

type GLBooked struct {
	CostTransactionID string `json:"costTransactionId"`
	EntryID           string `json:"entryId"`
	Lines             int    `json:"lines"`
}

type GLFailed struct {
	CostTransactionID string `json:"costTransactionId"`
	Code              string `json:"code"`
}

type GLPostResult struct {
	Booked []GLBooked `json:"booked"`
	Failed []GLFailed `json:"failed"`
	More   bool       `json:"more"`
	Parked int        `json:"parked"`
}

type ppvCandidate struct {
	id, sourceKey, currency, date string
	ppv, grir                     decimal.Decimal
}

func (c ppvCandidate) voucher(p PostingProfile) voucher {
	lines := domain.ReceiptVoucher(p.accounts(), c.grir, c.ppv)
	if strings.HasPrefix(c.sourceKey, invoicePPVPrefix) {
		lines = domain.InvoiceVoucher(p.accounts(), c.grir, c.ppv)
	}
	key := "ctxn:" + c.id
	return voucher{legalEntityID: "", sourceKey: key, glLedgerID: p.GLLedgerID, currency: c.currency, date: c.date, key: "costing-" + key, lines: lines}
}

func (s *Postgres) unpostedPPV(ctx context.Context, legalEntityID string) (PostingProfile, []ppvCandidate, int, error) {
	t, _ := tenant.FromContext(ctx)
	var profile PostingProfile
	var out []ppvCandidate
	parked := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if profile, err = loadProfile(ctx, tx, t.TenantID, legalEntityID); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `select count(*) from costing_gl_voucher where tenant_id = $1 and legal_entity_id = $2 and status = 'PARKED'`, t.TenantID, legalEntityID).Scan(&parked); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select ct.id::text, ct.source_key, ct.currency, ct.financial_date::text, ct.amount_acct::text, ct.gr_ir_amount::text
			from cost_transaction ct
			left join costing_gl_voucher v on v.tenant_id = ct.tenant_id and v.source_key = 'ctxn:' || ct.id::text
			where ct.tenant_id = $1 and ct.legal_entity_id = $2 and ct.valuation_ledger_id is null and ct.kind = 'PPV'
				and ct.gr_ir_amount is not null and ct.gr_ir_amount <> 0
				and (v.id is null or (v.status = 'PENDING' and v.next_attempt_at <= now()))
			order by ct.financial_date, ct.id limit $3`, t.TenantID, legalEntityID, MaxVouchersPerRun+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c ppvCandidate
			var ppv, grir string
			if err := rows.Scan(&c.id, &c.sourceKey, &c.currency, &c.date, &ppv, &grir); err != nil {
				return err
			}
			if c.ppv, err = decimal.NewFromString(ppv); err != nil {
				return err
			}
			if c.grir, err = decimal.NewFromString(grir); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return profile, out, parked, err
}

// PostCostVouchers books the receipt and invoice PPV cost transactions of the legal entity's primary valuation that no confirmed
// voucher covers: at receipt Dr inventory at standard, Dr or Cr PPV / Cr GR/IR; at invoice Dr GR/IR, Dr or Cr PPV / Cr accounts payable.
// Amounts come from the stored cost transaction and the accounts from the stored posting profile, one voucher per cost transaction.
// Only vouchers that are due are tried, so a voucher that keeps failing waits out its backoff and is parked after five attempts
// instead of holding back newer ones; a voucher the ledger refuses is reported and the rest go on.
func (s *Postgres) PostCostVouchers(ctx context.Context, ledger ledgerclient.Ledger, in GLPostRequest) (GLPostResult, error) {
	profile, candidates, parked, err := s.unpostedPPV(ctx, in.LegalEntityID)
	if err != nil {
		return GLPostResult{}, err
	}
	if err := CheckLedgerOwner(ctx, ledger, in.LegalEntityID, profile.GLLedgerID); err != nil {
		return GLPostResult{}, err
	}
	res := GLPostResult{Booked: []GLBooked{}, Failed: []GLFailed{}, Parked: parked}
	if len(candidates) > MaxVouchersPerRun {
		res.More, candidates = true, candidates[:MaxVouchersPerRun]
	}
	for _, c := range candidates {
		v := c.voucher(profile)
		v.legalEntityID = in.LegalEntityID
		entryID, err := s.postVoucher(ctx, ledger, v)
		if err != nil {
			slog.ErrorContext(ctx, "costing voucher failed", "costTransaction", c.id, "code", glCode(err), "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, GLFailed{CostTransactionID: c.id, Code: glCode(err)})
			continue
		}
		res.Booked = append(res.Booked, GLBooked{CostTransactionID: c.id, EntryID: entryID, Lines: len(v.lines)})
	}
	return res, nil
}
