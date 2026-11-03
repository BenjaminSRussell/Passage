package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	assetevents "github.com/BenjaminSRussell/passage/services/assets/internal/events"
)

// MaxLeaseGLDocs bounds the journals one lease posting request builds; more is reported so the caller asks again.
const MaxLeaseGLDocs = 200

const (
	leaseSource    = "asset-lease"
	leaseBookCode  = "LEASE"
	leaseKindRecog = "Recognition"
	leaseKindPer   = "Period"
)

// LeasePostingProfile maps a legal entity to the accounts of its lease journals. Placeholder marks the built-in default.
type LeasePostingProfile struct {
	LegalEntityID       string    `json:"legalEntityId"`
	ROUAsset            string    `json:"rouAssetAccount"`
	ROUAccumulated      string    `json:"rouAccumulatedDepreciationAccount"`
	Liability           string    `json:"liabilityAccount"`
	InterestExpense     string    `json:"interestExpenseAccount"`
	DepreciationExpense string    `json:"depreciationExpenseAccount"`
	PaymentClearing     string    `json:"paymentClearingAccount"`
	RemeasurementGain   string    `json:"remeasurementGainAccount"`
	Version             int       `json:"version"`
	UpdatedBy           string    `json:"updatedBy,omitempty"`
	UpdatedAt           time.Time `json:"updatedAt,omitempty"`
	Placeholder         bool      `json:"placeholder,omitempty"`
}

// PlaceholderLeaseProfile applies when a legal entity has no lease profile; the accountant must replace these accounts (services/assets/README.md).
var PlaceholderLeaseProfile = LeasePostingProfile{ROUAsset: "1700", ROUAccumulated: "1790", Liability: "2400", InterestExpense: "7200",
	DepreciationExpense: "6820", PaymentClearing: "2110", RemeasurementGain: "7120", Placeholder: true}

func (p LeasePostingProfile) accounts() domain.LeaseAccounts {
	return domain.LeaseAccounts{ROUAsset: p.ROUAsset, ROUAccumulated: p.ROUAccumulated, Liability: p.Liability, InterestExpense: p.InterestExpense,
		DepreciationExpense: p.DepreciationExpense, PaymentClearing: p.PaymentClearing, RemeasurementGain: p.RemeasurementGain}
}

const leaseProfileColumns = `legal_entity_id::text, rou_asset_account, rou_accumulated_account, liability_account, interest_expense_account,
	depreciation_expense_account, payment_clearing_account, remeasurement_gain_account, version, updated_by, updated_at`

func (p *LeasePostingProfile) targets() []any {
	return []any{&p.LegalEntityID, &p.ROUAsset, &p.ROUAccumulated, &p.Liability, &p.InterestExpense, &p.DepreciationExpense, &p.PaymentClearing,
		&p.RemeasurementGain, &p.Version, &p.UpdatedBy, &p.UpdatedAt}
}

// loadLeaseProfile reads the profile of a legal entity; with none it returns the placeholder for that entity.
func loadLeaseProfile(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (LeasePostingProfile, error) {
	var p LeasePostingProfile
	err := tx.QueryRow(ctx, `select `+leaseProfileColumns+` from lease_posting_profile where tenant_id = $1 and legal_entity_id = $2`, tenantID, legalEntityID).Scan(p.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		p = PlaceholderLeaseProfile
		p.LegalEntityID = legalEntityID
		return p, nil
	}
	return p, err
}

// GetLeasePostingProfile returns the lease posting profile of a legal entity, or the placeholder when it has none.
func (s *Postgres) GetLeasePostingProfile(ctx context.Context, legalEntityID string) (LeasePostingProfile, error) {
	p := principal(ctx)
	var out LeasePostingProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadLeaseProfile(ctx, tx, p.TenantID, legalEntityID)
		return err
	})
	return out, err
}

// SaveLeasePostingProfile creates the profile of a legal entity (ifMatch 0) or replaces it under its version.
func (s *Postgres) SaveLeasePostingProfile(ctx context.Context, in LeasePostingProfile, ifMatch int, actor string) (LeasePostingProfile, bool, error) {
	p := principal(ctx)
	var out LeasePostingProfile
	created := false
	args := func() []any {
		return []any{p.TenantID, in.LegalEntityID, in.ROUAsset, in.ROUAccumulated, in.Liability, in.InterestExpense, in.DepreciationExpense, in.PaymentClearing,
			in.RemeasurementGain, actor}
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var version int
		err := tx.QueryRow(ctx, `select version from lease_posting_profile where tenant_id = $1 and legal_entity_id = $2 for update`, p.TenantID, in.LegalEntityID).Scan(&version)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if ifMatch != 0 {
				return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the lease posting profile does not exist; create it without If-Match")
			}
			created = true
			_, err = tx.Exec(ctx, `insert into lease_posting_profile (tenant_id, legal_entity_id, rou_asset_account, rou_accumulated_account, liability_account,
				interest_expense_account, depreciation_expense_account, payment_clearing_account, remeasurement_gain_account, updated_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, args()...)
		case err != nil:
			return err
		case ifMatch == 0:
			return httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the profile version is required to replace it")
		case ifMatch != version:
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("the lease posting profile was changed by another user; current version is %d", version))
		default:
			_, err = tx.Exec(ctx, `update lease_posting_profile set rou_asset_account = $3, rou_accumulated_account = $4, liability_account = $5, interest_expense_account = $6,
				depreciation_expense_account = $7, payment_clearing_account = $8, remeasurement_gain_account = $9, updated_by = $10, updated_at = now(), version = version + 1
				where tenant_id = $1 and legal_entity_id = $2`, args()...)
		}
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select `+leaseProfileColumns+` from lease_posting_profile where tenant_id = $1 and legal_entity_id = $2`, p.TenantID, in.LegalEntityID).Scan(out.targets()...)
	})
	return out, created, err
}

// LeaseGLPostRequest asks to book the unbooked journals of the leases of a legal entity (or one lease) dated on or before PostThrough.
type LeaseGLPostRequest struct {
	LegalEntityID string
	LedgerID      string
	LeaseID       string
	PostThrough   Date
}

type LeaseGLBooked struct {
	LeaseID     string `json:"leaseId"`
	LeaseNumber string `json:"leaseNumber"`
	Period      int    `json:"period"`
	EntryID     string `json:"entryId"`
	Lines       int    `json:"lines"`
	Placeholder bool   `json:"placeholderProfile,omitempty"`
}

type LeaseGLFailed struct {
	LeaseID     string `json:"leaseId"`
	LeaseNumber string `json:"leaseNumber"`
	Period      int    `json:"period"`
	Code        string `json:"code"`
}

type LeaseGLPostResult struct {
	Booked []LeaseGLBooked `json:"booked"`
	Failed []LeaseGLFailed `json:"failed"`
	More   bool            `json:"more"`
}

// leaseDoc is one journal to book: period 0 is the recognition at commencement.
type leaseDoc struct {
	leaseID, number string
	period          int
	entry           ledgerclient.Entry
	placeholder     bool
	event           assetevents.LeasePostedData
	fresh           bool
}

type leaseHead struct {
	id, number, commencement, currency string
	perYear                            int
	liability, rou                     decimal.Decimal
	recognized                         bool
}

type leaseModSum struct{ adjustment, rou, excess decimal.Decimal }

func netEntry(d *leaseDoc, currency string, net map[string]decimal.Decimal) {
	mu := money.MinorUnits(currency)
	accounts := make([]string, 0, len(net))
	for a := range net {
		accounts = append(accounts, a)
	}
	slices.Sort(accounts)
	for _, a := range accounts {
		if v := net[a].Round(mu); !v.IsZero() {
			d.entry.Lines = append(d.entry.Lines, ledgerclient.Line{Account: a, Credit: v.IsNegative(), Amount: v.Abs()})
		}
	}
}

// buildLeaseDocs builds the journals of one lease up to PostThrough: the recognition if it is not booked, then every unbooked period in order.
func buildLeaseDocs(ctx context.Context, tx pgx.Tx, tenantID string, in LeaseGLPostRequest, h leaseHead, profile LeasePostingProfile, room int) (docs []*leaseDoc, failed []LeaseGLFailed, more bool, err error) {
	commencement, err := time.Parse(DateLayout, h.commencement)
	if err != nil {
		return nil, nil, false, err
	}
	through := in.PostThrough.Time
	mu := money.MinorUnits(h.currency)
	newDoc := func(period int, date time.Time, kind string) *leaseDoc {
		return &leaseDoc{leaseID: h.id, number: h.number, period: period, placeholder: profile.Placeholder,
			entry: ledgerclient.Entry{LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, DocumentID: h.id, PostingDate: date.Format(DateLayout), Currency: h.currency,
				IdempotencyKey: fmt.Sprintf("%s:%s:%d:%s", leaseSource, h.id, period, h.currency), SourceType: leaseSource},
			event: assetevents.LeasePostedData{LeaseID: h.id, LegalEntityID: in.LegalEntityID, Kind: kind, Period: period, PostingDate: date.Format(DateLayout), Currency: h.currency}}
	}
	if !h.recognized && !commencement.After(through) {
		if len(docs) >= room {
			return docs, failed, true, nil
		}
		d := newDoc(0, commencement, leaseKindRecog)
		netEntry(d, h.currency, domain.LeaseRecognitionNet(profile.accounts(), h.liability.Round(mu), h.rou.Round(mu)))
		d.event.Liability, d.event.ROUAsset = h.liability.Round(mu).String(), h.rou.Round(mu).String()
		docs = append(docs, d)
	}
	mods := map[int]leaseModSum{}
	// A modification that a later one superseded (by taking effect in an earlier period, markSuperseded) is not part of the schedule any more.
	mrows, err := tx.Query(ctx, `select m.effective_period, sum(m.adjustment), sum(m.rou_adjustment), sum(m.profit_loss_excess) from lease_modification m
		where m.tenant_id = $1 and m.lease_id = $2 and not exists (select 1 from lease_modification n where n.tenant_id = m.tenant_id and n.lease_id = m.lease_id
		and n.effective_period < m.effective_period and (n.created_at, n.id) > (m.created_at, m.id)) group by m.effective_period`, tenantID, h.id)
	if err != nil {
		return nil, nil, false, err
	}
	for mrows.Next() {
		var p int
		var m leaseModSum
		if err := mrows.Scan(&p, &m.adjustment, &m.rou, &m.excess); err != nil {
			mrows.Close()
			return nil, nil, false, err
		}
		mods[p] = m
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return nil, nil, false, err
	}
	type line struct {
		period                        int
		interest, payment, dep, remsr decimal.Decimal
	}
	var lines []line
	lrows, err := tx.Query(ctx, `select period_no, interest, payment, rou_depreciation, remeasurement from lease_schedule_line
		where tenant_id = $1 and lease_id = $2 and posted_journal_id is null order by period_no`, tenantID, h.id)
	if err != nil {
		return nil, nil, false, err
	}
	for lrows.Next() {
		var l line
		if err := lrows.Scan(&l.period, &l.interest, &l.payment, &l.dep, &l.remsr); err != nil {
			lrows.Close()
			return nil, nil, false, err
		}
		lines = append(lines, l)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return nil, nil, false, err
	}
	for _, l := range lines {
		end := domain.LeasePeriodEnd(commencement, h.perYear, l.period)
		if end.After(through) {
			break
		}
		if len(docs) >= room {
			return docs, failed, true, nil
		}
		m := mods[l.period]
		net, nerr := domain.LeasePeriodNet(profile.accounts(), domain.LeasePeriodAmounts{Interest: l.interest.Round(mu), Payment: l.payment.Round(mu), ROUDepreciation: l.dep.Round(mu),
			Remeasurement: l.remsr.Round(mu), ROUAdjustment: m.rou.Round(mu), PLExcess: m.excess.Round(mu)})
		if nerr != nil || !m.adjustment.Round(mu).Equal(l.remsr.Round(mu)) {
			failed = append(failed, LeaseGLFailed{LeaseID: h.id, LeaseNumber: h.number, Period: l.period, Code: domain.CodeLeaseRemeasurementMismatch})
			break
		}
		d := newDoc(l.period, end, leaseKindPer)
		netEntry(d, h.currency, net)
		d.event.Interest, d.event.Payment, d.event.ROUDepreciation, d.event.Remeasurement = l.interest.Round(mu).String(), l.payment.Round(mu).String(), l.dep.Round(mu).String(), l.remsr.Round(mu).String()
		docs = append(docs, d)
	}
	return docs, failed, false, nil
}

// stageLeaseDocs builds the journals and stores each as a pending booking before the ledger is called. A document with a pending booking
// from an earlier attempt resends it unchanged (the ledger may have accepted it without our seeing the answer).
func (s *Postgres) stageLeaseDocs(ctx context.Context, tenantID string, in LeaseGLPostRequest) (out []*leaseDoc, failed []LeaseGLFailed, more bool, err error) {
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		profile, err := loadLeaseProfile(ctx, tx, tenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		// The leases are locked until their pending bookings are written, the lock ModifyLease takes: a remeasurement cannot replace the schedule
		// between reading a period and staging its journal (it sees the pending booking and refuses), and two requests cannot stage the same journal.
		rows, err := tx.Query(ctx, `select id::text, lease_number, commencement_date::text, payments_per_year, currency, liability, rou_asset, recognition_journal_id is not null
			from lease where tenant_id = $1 and legal_entity_id = $2 and ($3 = '' or id = nullif($3, '')::uuid) and status <> 'Draft' order by lease_number, id for update`,
			tenantID, in.LegalEntityID, in.LeaseID)
		if err != nil {
			return err
		}
		var heads []leaseHead
		for rows.Next() {
			var h leaseHead
			if err := rows.Scan(&h.id, &h.number, &h.commencement, &h.perYear, &h.currency, &h.liability, &h.rou, &h.recognized); err != nil {
				rows.Close()
				return err
			}
			heads = append(heads, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, h := range heads {
			docs, f, m, err := buildLeaseDocs(ctx, tx, tenantID, in, h, profile, MaxLeaseGLDocs-len(out))
			if err != nil {
				return err
			}
			failed = append(failed, f...)
			for _, d := range docs {
				var ledgerID, date string
				var raw []byte
				perr := tx.QueryRow(ctx, `select ledger_id::text, posting_date::text, lines from asset_pending_booking where tenant_id = $1 and idempotency_key = $2`,
					tenantID, d.entry.IdempotencyKey).Scan(&ledgerID, &date, &raw)
				switch {
				case perr == nil:
					var pending []pendingLine
					if err := json.Unmarshal(raw, &pending); err != nil {
						return err
					}
					d.entry.LedgerID, d.entry.PostingDate, d.entry.Lines = ledgerID, date, nil
					for _, l := range pending {
						d.entry.Lines = append(d.entry.Lines, ledgerclient.Line{Account: l.Account, Credit: l.Credit, Amount: decimal.RequireFromString(l.Amount)})
					}
				case !errors.Is(perr, pgx.ErrNoRows):
					return perr
				default:
					raw, err := json.Marshal(linesOf(d.entry))
					if err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `insert into asset_pending_booking (tenant_id, idempotency_key, legal_entity_id, ledger_id, book_code, currency, source_type, document_id, posting_date, lines, transaction_ids)
						values ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10, '{}'::uuid[])`, tenantID, d.entry.IdempotencyKey, in.LegalEntityID, in.LedgerID, leaseBookCode, h.currency,
						leaseSource, d.entry.DocumentID, d.entry.PostingDate, raw); err != nil {
						return err
					}
				}
				out = append(out, d)
			}
			if m {
				more = true
				break
			}
		}
		return nil
	})
	return out, failed, more, err
}

// confirmLeaseDoc records the ledger entry on the lease or schedule line, drops the pending booking and writes the lease.posted event, in one transaction.
func (s *Postgres) confirmLeaseDoc(ctx context.Context, tenantID string, d *leaseDoc, entryID string) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `delete from asset_pending_booking where tenant_id = $1 and idempotency_key = $2`, tenantID, d.entry.IdempotencyKey); err != nil {
			return err
		}
		var tag int64
		if d.period == 0 {
			ct, err := tx.Exec(ctx, `update lease set recognition_journal_id = $3::uuid where tenant_id = $1 and id = $2 and recognition_journal_id is null`, tenantID, d.leaseID, entryID)
			if err != nil {
				return err
			}
			tag = ct.RowsAffected()
		} else {
			ct, err := tx.Exec(ctx, `update lease_schedule_line set posted_journal_id = $4::uuid where tenant_id = $1 and lease_id = $2 and period_no = $3 and posted_journal_id is null`,
				tenantID, d.leaseID, d.period, entryID)
			if err != nil {
				return err
			}
			tag = ct.RowsAffected()
		}
		if tag != 1 {
			// A concurrent request that resent the same pending booking may have confirmed it first: the ledger answered both with the same
			// entry, which is then already recorded (and its event written) and is not an error.
			var booked *string
			q := `select recognition_journal_id::text from lease where tenant_id = $1 and id = $2`
			args := []any{tenantID, d.leaseID}
			if d.period != 0 {
				q = `select posted_journal_id::text from lease_schedule_line where tenant_id = $1 and lease_id = $2 and period_no = $3`
				args = append(args, d.period)
			}
			if err := tx.QueryRow(ctx, q, args...).Scan(&booked); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if booked != nil && *booked == entryID {
				return nil
			}
			return fmt.Errorf("lease %s period %d is not open for booking any more", d.leaseID, d.period)
		}
		d.event.JournalEntryID, d.event.LedgerID = entryID, d.entry.LedgerID
		return enqueue(ctx, tx, "lease", d.leaseID, assetevents.LeasePosted, tenantID, d.event)
	})
}

func (s *Postgres) discardLeaseDoc(ctx context.Context, tenantID string, d *leaseDoc) {
	if err := s.discard(ctx, tenantID, d.entry.IdempotencyKey); err != nil {
		slog.ErrorContext(ctx, "assets pending lease booking not dropped", "key", d.entry.IdempotencyKey, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

// PostLeasesToGL books the leases of a legal entity (or one lease) in the general ledger up to PostThrough: the recognition at commencement
// (Dr ROU asset, Cr lease liability) and one journal per schedule period dated on its last day (interest, payment, ROU depreciation and the
// remeasurement of a modification). Periods go in order and a lease whose journal the ledger refuses (a closed period, for one) stops there
// and is retried by the next request; other leases go on. Each journal is stored as a pending booking before the ledger is called and resent
// unchanged until the ledger answers, so a retry cannot book twice or book different amounts. At most MaxLeaseGLDocs journals per request.
func (s *Postgres) PostLeasesToGL(ctx context.Context, ledger ledgerclient.Ledger, in LeaseGLPostRequest) (LeaseGLPostResult, error) {
	p := principal(ctx)
	docs, failed, more, err := s.stageLeaseDocs(ctx, p.TenantID, in)
	if err != nil {
		return LeaseGLPostResult{}, err
	}
	res := LeaseGLPostResult{Booked: []LeaseGLBooked{}, Failed: failed, More: more}
	if res.Failed == nil {
		res.Failed = []LeaseGLFailed{}
	}
	stopped := map[string]bool{}
	for _, d := range docs {
		if stopped[d.leaseID] {
			s.discardLeaseDoc(ctx, p.TenantID, d)
			continue
		}
		entryID, err := ledger.Post(ctx, d.entry)
		if err != nil {
			code := ledgerclient.CodeOf(err)
			if code == "" {
				code = CodeGLFailed
			}
			if ledgerclient.Rejected(err) {
				s.discardLeaseDoc(ctx, p.TenantID, d)
			}
			slog.ErrorContext(ctx, "assets lease GL booking failed", "lease", d.leaseID, "period", d.period, "code", code, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, LeaseGLFailed{LeaseID: d.leaseID, LeaseNumber: d.number, Period: d.period, Code: code})
			stopped[d.leaseID] = true
			continue
		}
		if err := s.confirmLeaseDoc(ctx, p.TenantID, d, entryID); err != nil {
			slog.ErrorContext(ctx, "assets lease GL booking not recorded", "lease", d.leaseID, "period", d.period, "entryId", entryID, "err", err, "correlationId", middleware.GetReqID(ctx))
			res.Failed = append(res.Failed, LeaseGLFailed{LeaseID: d.leaseID, LeaseNumber: d.number, Period: d.period, Code: CodeGLFailed})
			stopped[d.leaseID] = true
			continue
		}
		res.Booked = append(res.Booked, LeaseGLBooked{LeaseID: d.leaseID, LeaseNumber: d.number, Period: d.period, EntryID: entryID, Lines: len(d.entry.Lines), Placeholder: d.placeholder})
	}
	return res, nil
}

// LeasePostedPeriod is the booking state of one schedule period.
type LeasePostedPeriod struct {
	PeriodNo        int     `json:"periodNo"`
	PeriodEnd       string  `json:"periodEnd"`
	PostedJournalID *string `json:"postedJournalId"`
}

// LeasePostings is the booking state of a lease: its recognition journal and every period of the schedule in force.
type LeasePostings struct {
	LeaseID              string              `json:"leaseId"`
	RecognitionJournalID *string             `json:"recognitionJournalId"`
	Periods              []LeasePostedPeriod `json:"periods"`
}

// GetLeasePostings reads which journals of a lease are in the general ledger.
func (s *Postgres) GetLeasePostings(ctx context.Context, leaseID string) (LeasePostings, error) {
	p := principal(ctx)
	out := LeasePostings{LeaseID: leaseID, Periods: []LeasePostedPeriod{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var commencement time.Time
		var perYear int
		err := tx.QueryRow(ctx, `select commencement_date, payments_per_year, recognition_journal_id::text from lease where tenant_id = $1 and id = $2`, p.TenantID, leaseID).
			Scan(&commencement, &perYear, &out.RecognitionJournalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeLeaseNotFound, "no lease with id "+leaseID)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select period_no, posted_journal_id::text from lease_schedule_line where tenant_id = $1 and lease_id = $2 order by period_no`, p.TenantID, leaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x LeasePostedPeriod
			if err := rows.Scan(&x.PeriodNo, &x.PostedJournalID); err != nil {
				return err
			}
			x.PeriodEnd = domain.LeasePeriodEnd(commencement, perYear, x.PeriodNo).Format(DateLayout)
			out.Periods = append(out.Periods, x)
		}
		return rows.Err()
	})
	return out, err
}
