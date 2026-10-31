package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

var (
	ErrRunNotFound        = errors.New("payment run not found")
	ErrNotApproved        = errors.New("payment proposal is not approved")
	ErrExecutorConflict   = errors.New("a payment run must be started by a user who neither created nor approved the proposal")
	ErrRunState           = errors.New("payment run is no longer running")
	ErrRunOutOfOrder      = errors.New("payment run step needs an earlier step to finish first")
	ErrJournalRequested   = errors.New("the payment journal was requested; the run can only complete")
	ErrUnsupportedMethod  = errors.New("payment run supports TRANSFER and WIRE groups only")
	ErrDiscountAccount    = errors.New("the proposal takes cash discounts; the run needs a discountAccount")
	ErrWithholdingAccount = errors.New("the proposal withholds tax; the run needs a withholdingAccount")
)

type PaymentRun struct {
	ID                 string              `json:"id"`
	LegalEntityID      string              `json:"legalEntityId"`
	ProposalID         string              `json:"proposalId"`
	Status             string              `json:"status"`
	RequestedBy        string              `json:"requestedBy"`
	LedgerID           string              `json:"ledgerId"`
	BankAccount        string              `json:"bankAccount"`
	ControlAccount     string              `json:"controlAccount"`
	DiscountAccount    *string             `json:"discountAccount,omitempty"`
	WithholdingAccount *string             `json:"withholdingAccount,omitempty"`
	FileNumber         *string             `json:"fileNumber,omitempty"`
	FileSHA256         *string             `json:"fileSha256,omitempty"`
	PaymentCount       *int                `json:"paymentCount,omitempty"`
	FileGeneratedAt    *time.Time          `json:"fileGeneratedAt,omitempty"`
	JournalRequested   *time.Time          `json:"journalRequestedAt,omitempty"`
	JournalRejected    *time.Time          `json:"journalRejectedAt,omitempty"`
	JournalEntryID     *string             `json:"journalEntryId,omitempty"`
	JournalPostedAt    *time.Time          `json:"journalPostedAt,omitempty"`
	SettledAt          *time.Time          `json:"settledAt,omitempty"`
	FailureReason      *string             `json:"failureReason,omitempty"`
	CreatedAt          time.Time           `json:"createdAt"`
	CompletedAt        *time.Time          `json:"completedAt,omitempty"`
	Payments           []domain.RunPayment `json:"payments"`
}

type NewRun struct {
	FileConfig         []byte
	LegalEntityID      string
	ProposalID         string
	RequestedBy        string
	Today              string
	LedgerID           string
	BankAccount        string
	ControlAccount     string
	DiscountAccount    string
	WithholdingAccount string
}

type RunFile struct {
	Number       string
	SHA256       string
	Content      string
	PaymentCount int
	Payments     []domain.RunPayment
}

const runColumns = `id::text, legal_entity_id::text, proposal_id::text, status, requested_by, ledger_id::text, bank_account, control_account,
	discount_account, withholding_account, file_number, file_sha256, payment_count, file_generated_at, journal_requested_at, journal_rejected_at, journal_entry_id, journal_posted_at, settled_at,
	failure_reason, created_at, completed_at`

func scanRun(row pgx.Row) (PaymentRun, error) {
	var r PaymentRun
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.ProposalID, &r.Status, &r.RequestedBy, &r.LedgerID, &r.BankAccount, &r.ControlAccount,
		&r.DiscountAccount, &r.WithholdingAccount, &r.FileNumber, &r.FileSHA256, &r.PaymentCount, &r.FileGeneratedAt, &r.JournalRequested, &r.JournalRejected, &r.JournalEntryID, &r.JournalPostedAt, &r.SettledAt,
		&r.FailureReason, &r.CreatedAt, &r.CompletedAt)
	return r, err
}

func loadRun(ctx context.Context, tx pgx.Tx, query string, args ...any) (PaymentRun, error) {
	run, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from ap_payment_run where `+query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return PaymentRun{}, ErrRunNotFound
	}
	if err != nil {
		return PaymentRun{}, err
	}
	run.Payments = []domain.RunPayment{}
	rows, err := tx.Query(ctx, `select id::text, group_id::text, vendor_id::text, currency, payment_method, end_to_end_id, amount::text, valued_amount::text
		from ap_payment where run_id = $1::uuid order by end_to_end_id`, run.ID)
	if err != nil {
		return PaymentRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p domain.RunPayment
		var amount, valued string
		if err := rows.Scan(&p.ID, &p.GroupID, &p.VendorID, &p.Currency, &p.Method, &p.EndToEndID, &amount, &valued); err != nil {
			return PaymentRun{}, err
		}
		if err := parseDecimals([]*decimal.Decimal{&p.Amount, &p.Valued}, amount, valued); err != nil {
			return PaymentRun{}, err
		}
		run.Payments = append(run.Payments, p)
	}
	return run, rows.Err()
}

func (s *Postgres) GetRun(ctx context.Context, id string) (PaymentRun, error) {
	var run PaymentRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = loadRun(ctx, tx, `id = $1::uuid`, id)
		return err
	})
	return run, err
}

func runField(kind, runID string) string { return "ap_payment_run." + kind + ":" + runID }

func (s *Postgres) openRunField(ctx context.Context, id, column, kind string) ([]byte, error) {
	p, _ := tenant.FromContext(ctx)
	var sealed []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select `+column+` from ap_payment_run where id = $1::uuid`, id).Scan(&sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	})
	if err != nil || sealed == nil {
		return nil, err
	}
	if s.crypt == nil {
		return nil, ErrNoFieldCrypt
	}
	return s.crypt.Open(p.TenantID, runField(kind, id), sealed)
}

func (s *Postgres) RunFileContent(ctx context.Context, id string) (string, error) {
	plain, err := s.openRunField(ctx, id, "file_sealed", "file")
	return string(plain), err
}

func (s *Postgres) RunFileConfig(ctx context.Context, id string) ([]byte, error) {
	return s.openRunField(ctx, id, "file_config_sealed", "config")
}

func canonicalSubject(user string) string { return strings.ToLower(strings.TrimSpace(user)) }

func (s *Postgres) InvoiceNumbers(ctx context.Context, proposalID string) (map[string]string, error) {
	out := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.open_item_id::text, i.invoice_number from ap_payment_proposal_line l
			join ap_open_item i on i.tenant_id = l.tenant_id and i.id = l.open_item_id where l.proposal_id = $1::uuid`, proposalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, number string
			if err := rows.Scan(&id, &number); err != nil {
				return err
			}
			out[id] = number
		}
		return rows.Err()
	})
	return out, err
}

type ProposalParties struct {
	CreatedBy  string
	ApprovedBy string
}

func (s *Postgres) ProposalParties(ctx context.Context, id string) (ProposalParties, error) {
	var out ProposalParties
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select created_by, coalesce(approved_by, '') from ap_payment_proposal where id = $1::uuid`, id).Scan(&out.CreatedBy, &out.ApprovedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProposalNotFound
		}
		return err
	})
	return out, err
}

func (s *Postgres) StartRun(ctx context.Context, in NewRun) (PaymentRun, bool, error) {
	p, _ := tenant.FromContext(ctx)
	runID, created := "", false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, createdBy, approvedBy string
		var elapsed bool
		err := tx.QueryRow(ctx, `select status, created_by, coalesce(approved_by, ''), run_date < $3::date from ap_payment_proposal where id = $1::uuid and legal_entity_id = $2::uuid for update`,
			in.ProposalID, in.LegalEntityID, in.Today).Scan(&status, &createdBy, &approvedBy, &elapsed)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProposalNotFound
		}
		if err != nil {
			return err
		}
		executor := canonicalSubject(in.RequestedBy)
		executorConflict := executor == canonicalSubject(createdBy) || executor == canonicalSubject(approvedBy)
		existing, err := loadRun(ctx, tx, `legal_entity_id = $1::uuid and proposal_id = $2::uuid`, in.LegalEntityID, in.ProposalID)
		if err == nil {
			if executorConflict {
				return ErrExecutorConflict
			}
			runID = existing.ID
			return nil
		}
		if !errors.Is(err, ErrRunNotFound) {
			return err
		}
		if status != domain.ProposalApproved {
			return ErrNotApproved
		}
		if executorConflict {
			return ErrExecutorConflict
		}
		if elapsed {
			return ErrRunDateElapsed
		}
		if err := checkRunnable(ctx, tx, in); err != nil {
			return err
		}
		if s.crypt == nil {
			return ErrNoFieldCrypt
		}
		runID, created = ids.New(), true
		config, err := s.crypt.Seal(p.TenantID, runField("config", runID), in.FileConfig)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into ap_payment_run (tenant_id, id, legal_entity_id, proposal_id, status, requested_by, ledger_id, bank_account,
			control_account, discount_account, withholding_account, file_config_sealed) values ($1, $2::uuid, $3::uuid, $4::uuid, 'RUNNING', $5, $6::uuid, $7, $8, $9, $10, $11)`,
			p.TenantID, runID, in.LegalEntityID, in.ProposalID, in.RequestedBy, in.LedgerID, in.BankAccount, in.ControlAccount, nullable(in.DiscountAccount), nullable(in.WithholdingAccount), config); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update ap_payment_proposal set status = 'EXECUTING' where id = $1::uuid`, in.ProposalID); err != nil {
			return err
		}
		var changed *LinesChangedError
		if err := verifyLines(ctx, tx, in.ProposalID); errors.As(err, &changed) {
			return failRun(ctx, tx, runID, in.ProposalID, changed.Error())
		} else if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return PaymentRun{}, false, err
	}
	run, err := s.GetRun(ctx, runID)
	return run, created, err
}

func checkRunnable(ctx context.Context, tx pgx.Tx, in NewRun) error {
	var unsupported, discounted, withheld bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from ap_payment_proposal_group where proposal_id = $1::uuid and payment_method not in ('TRANSFER', 'WIRE')),
		exists(select 1 from ap_payment_proposal_line where proposal_id = $1::uuid and discount_amount > 0),
		exists(select 1 from ap_payment_proposal_line where proposal_id = $1::uuid and withheld_amount > 0)`, in.ProposalID).Scan(&unsupported, &discounted, &withheld); err != nil {
		return err
	}
	if unsupported {
		return ErrUnsupportedMethod
	}
	if discounted && in.DiscountAccount == "" {
		return ErrDiscountAccount
	}
	if withheld && in.WithholdingAccount == "" {
		return ErrWithholdingAccount
	}
	return nil
}

func failRun(ctx context.Context, tx pgx.Tx, runID, proposalID, reason string) error {
	if _, err := tx.Exec(ctx, `update ap_payment_run set status = 'FAILED', failure_reason = $2 where id = $1::uuid`, runID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update ap_payment_proposal set status = 'FAILED', failed_at = now() where id = $1::uuid`, proposalID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update ap_payment_proposal_line set reserved = false where proposal_id = $1::uuid`, proposalID)
	return err
}

type runLock struct {
	status                                                               string
	proposalID                                                           string
	fileDone, journalRequested, journalRejected, journalDone, settleDone bool
}

func lockRun(ctx context.Context, tx pgx.Tx, id string) (runLock, error) {
	var l runLock
	err := tx.QueryRow(ctx, `select status, proposal_id::text, file_generated_at is not null, journal_requested_at is not null, journal_rejected_at is not null, journal_posted_at is not null, settled_at is not null
		from ap_payment_run where id = $1::uuid for update`, id).Scan(&l.status, &l.proposalID, &l.fileDone, &l.journalRequested, &l.journalRejected, &l.journalDone, &l.settleDone)
	if errors.Is(err, pgx.ErrNoRows) {
		return runLock{}, ErrRunNotFound
	}
	return l, err
}

func (s *Postgres) step(ctx context.Context, id string, fn func(tx pgx.Tx, l runLock) error) (PaymentRun, error) {
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := lockRun(ctx, tx, id)
		if err != nil {
			return err
		}
		return fn(tx, l)
	})
	if err != nil {
		return PaymentRun{}, err
	}
	return s.GetRun(ctx, id)
}

func (s *Postgres) SaveRunFile(ctx context.Context, id string, f RunFile) (PaymentRun, error) {
	p, _ := tenant.FromContext(ctx)
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.status == domain.RunFailed:
			return ErrRunState
		case l.fileDone:
			return nil
		case l.status != domain.RunRunning:
			return ErrRunState
		}
		if s.crypt == nil {
			return ErrNoFieldCrypt
		}
		if err := verifyLines(ctx, tx, l.proposalID); err != nil {
			return err
		}
		sealed, err := s.crypt.Seal(p.TenantID, runField("file", id), []byte(f.Content))
		if err != nil {
			return err
		}
		var le string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text from ap_payment_run where id = $1::uuid`, id).Scan(&le); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, pay := range f.Payments {
			batch.Queue(`insert into ap_payment (tenant_id, id, run_id, group_id, legal_entity_id, vendor_id, currency, payment_method, amount, valued_amount, end_to_end_id)
				values ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7, $8, $9, $10, $11)`,
				p.TenantID, ids.New(), id, pay.GroupID, le, pay.VendorID, pay.Currency, pay.Method, pay.Amount.String(), pay.Valued.String(), pay.EndToEndID)
		}
		results := tx.SendBatch(ctx, batch)
		for range f.Payments {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		if err := results.Close(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update ap_payment_run set file_number = $2, file_sha256 = $3, file_sealed = $4, payment_count = $5, file_generated_at = now() where id = $1::uuid`,
			id, f.Number, f.SHA256, sealed, f.PaymentCount)
		return err
	})
}

func (s *Postgres) BeginJournal(ctx context.Context, id string) (PaymentRun, error) {
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.journalRejected && !l.journalDone:
			return ErrRunState
		case l.journalRequested:
			return nil
		case l.status != domain.RunRunning:
			return ErrRunState
		case !l.fileDone:
			return ErrRunOutOfOrder
		}
		_, err := tx.Exec(ctx, `update ap_payment_run set journal_requested_at = now() where id = $1::uuid`, id)
		return err
	})
}

func (s *Postgres) RejectJournal(ctx context.Context, id string) (PaymentRun, error) {
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.journalDone || l.journalRejected:
			return nil
		case !l.journalRequested:
			return ErrRunOutOfOrder
		}
		_, err := tx.Exec(ctx, `update ap_payment_run set journal_rejected_at = now() where id = $1::uuid`, id)
		return err
	})
}

func (s *Postgres) VerifyRunLines(ctx context.Context, id string) (PaymentRun, error) {
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		if l.status != domain.RunRunning {
			return ErrRunState
		}
		if l.journalRequested {
			return nil
		}
		return verifyLines(ctx, tx, l.proposalID)
	})
}

func (s *Postgres) RecordJournal(ctx context.Context, id, entryID string) (PaymentRun, error) {
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.journalDone:
			return nil
		case l.status != domain.RunRunning:
			return ErrRunState
		case !l.journalRequested:
			return ErrRunOutOfOrder
		}
		_, err := tx.Exec(ctx, `update ap_payment_run set journal_entry_id = $2, journal_posted_at = now() where id = $1::uuid`, id, entryID)
		return err
	})
}

func (s *Postgres) SettleRun(ctx context.Context, id string) (PaymentRun, error) {
	p, _ := tenant.FromContext(ctx)
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.settleDone:
			return nil
		case l.status != domain.RunRunning:
			return ErrRunState
		case !l.journalDone:
			return ErrRunOutOfOrder
		}
		type pending struct {
			itemID  string
			version int
			amount  decimal.Decimal
		}
		var todo []pending
		rows, err := tx.Query(ctx, `select open_item_id::text, item_version, open_amount::text from ap_payment_proposal_line
			where proposal_id = $1::uuid and not settled order by open_item_id`, l.proposalID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var q pending
			var amount string
			if err := rows.Scan(&q.itemID, &q.version, &amount); err != nil {
				rows.Close()
				return err
			}
			if q.amount, err = decimal.NewFromString(amount); err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, q)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, q := range todo {
			if _, err := settleTx(ctx, tx, p.TenantID, q.itemID, q.version, q.amount, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update ap_payment_proposal_line set settled = true where proposal_id = $1::uuid and open_item_id = $2::uuid`, l.proposalID, q.itemID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `update ap_payment_run set settled_at = now() where id = $1::uuid`, id)
		return err
	})
}

func (s *Postgres) CompleteRun(ctx context.Context, id string) (PaymentRun, error) {
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.status == domain.RunCompleted:
			return nil
		case l.status != domain.RunRunning:
			return ErrRunState
		case !l.settleDone:
			return ErrRunOutOfOrder
		}
		if _, err := tx.Exec(ctx, `update ap_payment_run set status = 'COMPLETED', completed_at = now() where id = $1::uuid`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update ap_payment_proposal set status = 'EXECUTED', executed_at = now() where id = $1::uuid`, l.proposalID)
		return err
	})
}

func (s *Postgres) FailRun(ctx context.Context, id, reason string) (PaymentRun, error) {
	return s.step(ctx, id, func(tx pgx.Tx, l runLock) error {
		switch {
		case l.status == domain.RunFailed:
			return nil
		case l.status != domain.RunRunning:
			return ErrRunState
		case l.journalRequested && (!l.journalRejected || l.journalDone):
			return ErrJournalRequested
		}
		return failRun(ctx, tx, id, l.proposalID, reason)
	})
}
