package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	MaxProposalItems = 5000
	uniqueViolation  = "23505"
)

var (
	ProposalCreatedType   = events.Type("payables", "paymentproposal", "created", 1)
	ProposalApprovedType  = events.Type("payables", "paymentproposal", "approved", 1)
	ProposalCancelledType = events.Type("payables", "paymentproposal", "cancelled", 1)
)

type proposalEvent struct {
	ProposalID    string `json:"proposal_id"`
	LegalEntityID string `json:"legal_entity_id"`
	RunDate       string `json:"run_date"`
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	Actor         string `json:"actor"`
	Currency      string `json:"currency,omitempty"`
	TotalValued   string `json:"total_valued,omitempty"`
	TotalWithheld string `json:"total_withheld,omitempty"`
}

var (
	ErrProposalNotFound = errors.New("payment proposal not found")
	ErrNotDraft         = errors.New("payment proposal is not a draft")
	ErrSelfApproval     = errors.New("a proposal cannot be approved by its creator")
	ErrItemsChanged     = errors.New("open items changed, were held or were reserved while the proposal was built")
	ErrTooManyItems     = errors.New("more open items are due than one proposal can hold")
	ErrRunDateElapsed   = errors.New("the proposal's run date has passed")
	ErrNoFieldCrypt     = errors.New("field encryption keys are not configured; protected run fields cannot be stored or read")
)

type LinesChangedError struct{ OpenItemIDs []string }

func (e *LinesChangedError) Error() string {
	return "open items changed, were held, settled or blocked since the proposal was created: " + strings.Join(e.OpenItemIDs, ", ")
}

type Proposal struct {
	ID                  string                 `json:"id"`
	LegalEntityID       string                 `json:"legalEntityId"`
	RunDate             string                 `json:"runDate"`
	RunID               string                 `json:"runId"`
	DueBy               string                 `json:"dueBy"`
	RateDate            string                 `json:"rateDate"`
	LegalEntityCurrency string                 `json:"legalEntityCurrency"`
	PaymentMethod       string                 `json:"paymentMethod"`
	Status              string                 `json:"status"`
	CreatedBy           string                 `json:"createdBy"`
	CreatedAt           time.Time              `json:"createdAt"`
	ApprovedBy          *string                `json:"approvedBy,omitempty"`
	ApprovedAt          *time.Time             `json:"approvedAt,omitempty"`
	CancelledBy         *string                `json:"cancelledBy,omitempty"`
	CancelledAt         *time.Time             `json:"cancelledAt,omitempty"`
	ExecutedAt          *time.Time             `json:"executedAt,omitempty"`
	FailedAt            *time.Time             `json:"failedAt,omitempty"`
	Groups              []domain.ProposalGroup `json:"groups"`
	TotalValued         decimal.Decimal        `json:"totalValued"`
	TotalWithheld       decimal.Decimal        `json:"totalWithheld"`
	RequestHash         string                 `json:"-"`
}

type NewProposal struct {
	LegalEntityID       string
	RunDate             string
	RunID               string
	DueBy               string
	RateDate            string
	LegalEntityCurrency string
	PaymentMethod       string
	RequestHash         string
	CreatedBy           string
	Groups              []domain.ProposalGroup
}

func (s *Postgres) Candidates(ctx context.Context, legalEntityID, dueBy string) ([]domain.ProposalCandidate, error) {
	var out []domain.ProposalCandidate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select i.id::text, i.vendor_id::text, i.currency, i.version, i.open_amount::text, i.discount_amount::text, i.discount_date, i.status = 'PARTIALLY_SETTLED'
			from ap_open_item i
			where i.legal_entity_id = $1::uuid and i.due_date <= $2::date and i.status in ('OPEN', 'PARTIALLY_SETTLED') and not i.payment_hold
			and not exists (select 1 from ap_vendor_block b where b.vendor_id = i.vendor_id and b.legal_entity_id = i.legal_entity_id)
			and not exists (select 1 from ap_payment_proposal_line l where l.open_item_id = i.id and l.reserved)
			order by i.due_date, i.id limit $3`, legalEntityID, dueBy, MaxProposalItems+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.ProposalCandidate
			var open, discount string
			if err := rows.Scan(&c.ID, &c.VendorID, &c.Currency, &c.Version, &open, &discount, &c.DiscountDate, &c.PartiallySettled); err != nil {
				return err
			}
			if c.OpenAmount, err = decimal.NewFromString(open); err != nil {
				return err
			}
			if c.DiscountAmount, err = decimal.NewFromString(discount); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err == nil && len(out) > MaxProposalItems {
		return nil, ErrTooManyItems
	}
	return out, err
}

const proposalColumns = `id::text, legal_entity_id::text, run_date::text, run_id, due_by::text, rate_date::text, legal_entity_currency, payment_method, status,
	created_by, created_at, approved_by, approved_at, cancelled_by, cancelled_at, executed_at, failed_at, request_hash`

func scanProposal(row pgx.Row) (Proposal, error) {
	var p Proposal
	err := row.Scan(&p.ID, &p.LegalEntityID, &p.RunDate, &p.RunID, &p.DueBy, &p.RateDate, &p.LegalEntityCurrency, &p.PaymentMethod, &p.Status,
		&p.CreatedBy, &p.CreatedAt, &p.ApprovedBy, &p.ApprovedAt, &p.CancelledBy, &p.CancelledAt, &p.ExecutedAt, &p.FailedAt, &p.RequestHash)
	return p, err
}

func (s *Postgres) FindProposal(ctx context.Context, legalEntityID, runDate, runID string) (Proposal, error) {
	var id string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text from ap_payment_proposal where legal_entity_id = $1::uuid and run_date = $2::date and run_id = $3`,
			legalEntityID, runDate, runID).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Proposal{}, ErrProposalNotFound
	}
	if err != nil {
		return Proposal{}, err
	}
	return s.GetProposal(ctx, id)
}

func (s *Postgres) CreateProposal(ctx context.Context, in NewProposal) (Proposal, bool, error) {
	p, _ := tenant.FromContext(ctx)
	id := ""
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `insert into ap_payment_proposal (tenant_id, id, legal_entity_id, run_date, run_id, due_by, rate_date, legal_entity_currency,
			payment_method, request_hash, status, created_by)
			values ($1, $11::uuid, $2::uuid, $3::date, $4, $5::date, $6::date, $7, $8, $9, 'DRAFT', $10)
			on conflict (tenant_id, legal_entity_id, run_date, run_id) do nothing returning id::text`,
			p.TenantID, in.LegalEntityID, in.RunDate, in.RunID, in.DueBy, in.RateDate, in.LegalEntityCurrency, in.PaymentMethod, in.RequestHash, in.CreatedBy, ids.New()).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			id = ""
			return nil
		}
		if err != nil {
			return err
		}
		if err := insertGroups(ctx, tx, p.TenantID, id, in); err != nil {
			return err
		}
		total, withheld := decimal.Zero, decimal.Zero
		for _, g := range in.Groups {
			total = total.Add(g.Valued)
			withheld = withheld.Add(g.Withheld)
		}
		return enqueueEvent(ctx, tx, ProposalCreatedType, "paymentproposal", id, p.TenantID, proposalEvent{
			ProposalID: id, LegalEntityID: in.LegalEntityID, RunDate: in.RunDate, RunID: in.RunID, Status: domain.ProposalDraft, Actor: in.CreatedBy,
			Currency: in.LegalEntityCurrency, TotalValued: total.String(), TotalWithheld: withheld.String(),
		})
	})
	if isUnique(err) {
		return Proposal{}, false, ErrItemsChanged
	}
	if err != nil {
		return Proposal{}, false, err
	}
	if id == "" {
		existing, err := s.FindProposal(ctx, in.LegalEntityID, in.RunDate, in.RunID)
		return existing, false, err
	}
	created, err := s.GetProposal(ctx, id)
	return created, true, err
}

func isUnique(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == uniqueViolation
}

func insertGroups(ctx context.Context, tx pgx.Tx, tenantID, proposalID string, in NewProposal) error {
	batch := &pgx.Batch{}
	lines := 0
	for _, g := range in.Groups {
		batch.Queue(`insert into ap_payment_proposal_group (tenant_id, id, proposal_id, legal_entity_id, vendor_id, currency, payment_method,
			gross_amount, discount_amount, net_amount, valued_amount, withheld_amount) values ($1, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9, $10, $11, $12)`,
			tenantID, g.ID, proposalID, in.LegalEntityID, g.VendorID, g.Currency, g.Method, g.Gross.String(), g.Discount.String(), g.Net.String(), g.Valued.String(), g.Withheld.String())
	}
	for _, g := range in.Groups {
		for _, l := range g.Lines {
			lines++
			batch.Queue(`insert into ap_payment_proposal_line (tenant_id, open_item_id, proposal_id, group_id, legal_entity_id, item_version, open_amount, discount_amount, pay_amount, rate, valued_amount, withheld_amount, withholding_code)
				select i.tenant_id, i.id, $2::uuid, $3::uuid, i.legal_entity_id, i.version, $5::numeric, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, nullif($11, '') from ap_open_item i
				where i.id = $1::uuid and i.version = $4 and i.open_amount = $5::numeric and not i.payment_hold
				and not exists (select 1 from ap_vendor_block b where b.vendor_id = i.vendor_id and b.legal_entity_id = i.legal_entity_id)`,
				l.OpenItemID, proposalID, g.ID, l.OpenItemVersion, l.OpenAmount.String(), l.Discount.String(), l.Pay.String(), l.Rate.String(), l.Valued.String(), l.Withheld.String(), l.WithholdingCode)
		}
	}
	results := tx.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()
	for range in.Groups {
		if _, err := results.Exec(); err != nil {
			return err
		}
	}
	for range lines {
		tag, err := results.Exec()
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrItemsChanged
		}
	}
	return results.Close()
}

func (s *Postgres) GetProposal(ctx context.Context, id string) (Proposal, error) {
	var out Proposal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanProposal(tx.QueryRow(ctx, `select `+proposalColumns+` from ap_payment_proposal where id = $1::uuid`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProposalNotFound
		}
		if err != nil {
			return err
		}
		out.Groups, err = loadGroups(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, g := range out.Groups {
			out.TotalValued = out.TotalValued.Add(g.Valued)
			out.TotalWithheld = out.TotalWithheld.Add(g.Withheld)
		}
		return nil
	})
	return out, err
}

func loadGroups(ctx context.Context, tx pgx.Tx, proposalID string) ([]domain.ProposalGroup, error) {
	rows, err := tx.Query(ctx, `select id::text, vendor_id::text, currency, payment_method, gross_amount::text, discount_amount::text, net_amount::text, valued_amount::text, withheld_amount::text
		from ap_payment_proposal_group where proposal_id = $1::uuid order by vendor_id, currency, payment_method`, proposalID)
	if err != nil {
		return nil, err
	}
	groups := []domain.ProposalGroup{}
	index := map[string]int{}
	for rows.Next() {
		var g domain.ProposalGroup
		var gross, discount, net, valued, withheld string
		if err := rows.Scan(&g.ID, &g.VendorID, &g.Currency, &g.Method, &gross, &discount, &net, &valued, &withheld); err != nil {
			rows.Close()
			return nil, err
		}
		if err := parseDecimals([]*decimal.Decimal{&g.Gross, &g.Discount, &g.Net, &g.Valued, &g.Withheld}, gross, discount, net, valued, withheld); err != nil {
			rows.Close()
			return nil, err
		}
		g.Lines = []domain.ProposalLine{}
		index[g.ID] = len(groups)
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	lines, err := tx.Query(ctx, `select l.group_id::text, l.open_item_id::text, l.open_amount::text, l.discount_amount::text, l.pay_amount::text, l.rate::text, l.valued_amount::text, l.withheld_amount::text, coalesce(l.withholding_code, '')
		from ap_payment_proposal_line l join ap_open_item i on i.tenant_id = l.tenant_id and i.id = l.open_item_id
		where l.proposal_id = $1::uuid order by i.due_date, l.open_item_id`, proposalID)
	if err != nil {
		return nil, err
	}
	defer lines.Close()
	for lines.Next() {
		var groupID, open, discount, pay, rate, valued, withheld string
		var l domain.ProposalLine
		if err := lines.Scan(&groupID, &l.OpenItemID, &open, &discount, &pay, &rate, &valued, &withheld, &l.WithholdingCode); err != nil {
			return nil, err
		}
		if err := parseDecimals([]*decimal.Decimal{&l.OpenAmount, &l.Discount, &l.Pay, &l.Rate, &l.Valued, &l.Withheld}, open, discount, pay, rate, valued, withheld); err != nil {
			return nil, err
		}
		g := &groups[index[groupID]]
		g.Lines = append(g.Lines, l)
	}
	return groups, lines.Err()
}

func parseDecimals(dst []*decimal.Decimal, raw ...string) error {
	for i, r := range raw {
		d, err := decimal.NewFromString(r)
		if err != nil {
			return err
		}
		*dst[i] = d
	}
	return nil
}

func verifyLines(ctx context.Context, tx pgx.Tx, proposalID string) error {
	rows, err := tx.Query(ctx, `select l.open_item_id::text,
			(i.version <> l.item_version or i.open_amount <> l.open_amount or i.payment_hold or i.status not in ('OPEN', 'PARTIALLY_SETTLED')
			 or exists (select 1 from ap_vendor_block b where b.vendor_id = i.vendor_id and b.legal_entity_id = i.legal_entity_id))
		from ap_payment_proposal_line l join ap_open_item i on i.tenant_id = l.tenant_id and i.id = l.open_item_id
		where l.proposal_id = $1::uuid order by l.open_item_id for update of i`, proposalID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var id string
		var changed bool
		if err := rows.Scan(&id, &changed); err != nil {
			return err
		}
		if changed {
			bad = append(bad, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bad) > 0 {
		return &LinesChangedError{OpenItemIDs: bad}
	}
	return nil
}

func (s *Postgres) ApproveProposal(ctx context.Context, id, approver, today string) (Proposal, error) {
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, createdBy, entity, runDate, runID string
		var elapsed bool
		err := tx.QueryRow(ctx, `select status, created_by, run_date < $2::date, legal_entity_id::text, run_date::text, run_id from ap_payment_proposal where id = $1::uuid for update`, id, today).Scan(&status, &createdBy, &elapsed, &entity, &runDate, &runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProposalNotFound
		}
		if err != nil {
			return err
		}
		if status != domain.ProposalDraft {
			return ErrNotDraft
		}
		if createdBy == approver {
			return ErrSelfApproval
		}
		if elapsed {
			return ErrRunDateElapsed
		}
		if err := verifyLines(ctx, tx, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update ap_payment_proposal set status = 'APPROVED', approved_by = $2, approved_at = now() where id = $1::uuid`, id, approver); err != nil {
			return err
		}
		return enqueueEvent(ctx, tx, ProposalApprovedType, "paymentproposal", id, p.TenantID, proposalEvent{
			ProposalID: id, LegalEntityID: entity, RunDate: runDate, RunID: runID, Status: domain.ProposalApproved, Actor: approver,
		})
	})
	if err != nil {
		return Proposal{}, err
	}
	return s.GetProposal(ctx, id)
}

func (s *Postgres) CancelProposal(ctx context.Context, id, user string) (Proposal, error) {
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, entity, runDate, runID string
		err := tx.QueryRow(ctx, `select status, legal_entity_id::text, run_date::text, run_id from ap_payment_proposal where id = $1::uuid for update`, id).Scan(&status, &entity, &runDate, &runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProposalNotFound
		}
		if err != nil {
			return err
		}
		if status != domain.ProposalDraft {
			return ErrNotDraft
		}
		if _, err = tx.Exec(ctx, `update ap_payment_proposal set status = 'CANCELLED', cancelled_by = $2, cancelled_at = now() where id = $1::uuid`, id, user); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update ap_payment_proposal_line set reserved = false where proposal_id = $1::uuid`, id); err != nil {
			return err
		}
		return enqueueEvent(ctx, tx, ProposalCancelledType, "paymentproposal", id, p.TenantID, proposalEvent{
			ProposalID: id, LegalEntityID: entity, RunDate: runDate, RunID: runID, Status: domain.ProposalCancelled, Actor: user,
		})
	})
	if err != nil {
		return Proposal{}, err
	}
	return s.GetProposal(ctx, id)
}

type ProposalFilter struct {
	LegalEntityID string
	Status        string
	RunFrom       string
	RunTo         string
	AfterRunDate  string
	AfterID       string
}

type ProposalSummary struct {
	ID                  string          `json:"id"`
	LegalEntityID       string          `json:"legalEntityId"`
	RunDate             string          `json:"runDate"`
	RunID               string          `json:"runId"`
	DueBy               string          `json:"dueBy"`
	LegalEntityCurrency string          `json:"legalEntityCurrency"`
	PaymentMethod       string          `json:"paymentMethod"`
	Status              string          `json:"status"`
	CreatedBy           string          `json:"createdBy"`
	CreatedAt           time.Time       `json:"createdAt"`
	ApprovedBy          *string         `json:"approvedBy,omitempty"`
	ApprovedAt          *time.Time      `json:"approvedAt,omitempty"`
	TotalValued         decimal.Decimal `json:"totalValued"`
}

func (s *Postgres) ListProposals(ctx context.Context, f ProposalFilter, limit int) ([]ProposalSummary, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	out := []ProposalSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select p.id::text, p.legal_entity_id::text, p.run_date::text, p.run_id, p.due_by::text, p.legal_entity_currency, p.payment_method, p.status,
				p.created_by, p.created_at, p.approved_by, p.approved_at,
				coalesce((select sum(g.valued_amount) from ap_payment_proposal_group g where g.tenant_id = p.tenant_id and g.proposal_id = p.id), 0)::text
			from ap_payment_proposal p
			where p.legal_entity_id = $1::uuid
			and ($2::text is null or p.status = $2::text)
			and ($3::date is null or p.run_date >= $3::date)
			and ($4::date is null or p.run_date <= $4::date)
			and ($5::date is null or (p.run_date, p.id) < ($5::date, $6::uuid))
			order by p.run_date desc, p.id desc limit $7`,
			f.LegalEntityID, nullable(f.Status), nullable(f.RunFrom), nullable(f.RunTo), nullable(f.AfterRunDate), nullable(f.AfterID), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ProposalSummary
			var total string
			if err := rows.Scan(&p.ID, &p.LegalEntityID, &p.RunDate, &p.RunID, &p.DueBy, &p.LegalEntityCurrency, &p.PaymentMethod, &p.Status,
				&p.CreatedBy, &p.CreatedAt, &p.ApprovedBy, &p.ApprovedAt, &total); err != nil {
				return err
			}
			if p.TotalValued, err = decimal.NewFromString(total); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) PaidToDate(ctx context.Context, legalEntityID, vendorID, currency string, year int) (decimal.Decimal, error) {
	var total string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select coalesce(sum(l.pay_amount), 0)::text
			from ap_payment_proposal_line l
			join ap_payment_proposal_group g on g.tenant_id = l.tenant_id and g.id = l.group_id
			join ap_payment_proposal p on p.tenant_id = l.tenant_id and p.id = l.proposal_id
			where p.legal_entity_id = $1::uuid and g.vendor_id = $2::uuid and g.currency = $3
			and p.status in ('APPROVED', 'EXECUTING', 'EXECUTED') and extract(year from p.run_date) = $4`, legalEntityID, vendorID, currency, year).Scan(&total)
	})
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(total)
}
