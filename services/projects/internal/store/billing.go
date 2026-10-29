package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// Default names of the UNIQUE (tenant_id, idempotency_key) constraints.
const (
	txnKeyConstraint      = "project_transaction_tenant_id_idempotency_key_key"
	proposalKeyConstraint = "project_invoice_proposal_tenant_id_idempotency_key_key"
)

// ProposalFilter narrows the proposals of one project. ProjectID is required.
// AfterID and Limit page the list (keyset; AfterID is the last id of the
// previous page, Limit at most MaxPageSize).
type ProposalFilter struct {
	ProjectID string
	Status    domain.ProposalStatus
	AfterID   string
	Limit     int
}

// TransactionPage is one page of a project's transactions; NextID is set when more remain.
type TransactionPage struct {
	Items  []domain.Transaction
	NextID string
}

// ProposalPage is one page of a project's proposals; NextID is set when more remain.
type ProposalPage struct {
	Items  []domain.InvoiceProposal
	NextID string
}

// pageLimit bounds a requested page size to 1..MaxPageSize.
func pageLimit(n int) int {
	if n < 1 || n > MaxPageSize {
		return MaxPageSize
	}
	return n
}

// TransactionBilling is a transaction with the candidate view the BR-41.3
// selection reads, for the "why not billed" explanation.
type TransactionBilling struct {
	Transaction domain.Transaction
	Candidate   domain.Candidate
	// ProjectType decides which transaction types the project bills and
	// whether only due milestones are selected (domain.BillableTypes).
	ProjectType domain.ProjectType
}

// milestoneJoin resolves a transaction's WBS code in its project's WORKING plan
// (node ids change when that plan's WBS is replaced; codes do not) and yields
// m.is_billable_milestone and m.early_finish, both null without a node.
const milestoneJoin = ` left join lateral (select n.is_billable_milestone, n.early_finish from wbs_node n
		join plan_version v on v.tenant_id = n.tenant_id and v.id = n.plan_version_id
		where n.tenant_id = t.tenant_id and v.project_id = t.project_id and v.kind = 'WORKING' and n.wbs_code = t.wbs_code) m on true`

// ---- Categories ----

const categoryColumns = `id, name, txn_type, version, created_at, updated_at`

func scanCategory(row pgx.Row) (domain.Category, error) {
	var c domain.Category
	var t string
	err := row.Scan(&c.ID, &c.Name, &t, &c.Version, &c.CreatedAt, &c.UpdatedAt)
	c.TxnType = domain.TxnType(t)
	return c, err
}

// UpsertCategory creates or replaces a project category under CheckExpected.
// Its transaction type cannot change once set (PROJ-CAT-001).
func (s *Postgres) UpsertCategory(ctx context.Context, c domain.Category, expected int) (domain.Category, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Category
	var created bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanCategory(tx.QueryRow(ctx, `select `+categoryColumns+` from project_category where tenant_id = $1 and id = $2 for update`, p.TenantID, c.ID))
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := CheckExpected(expected, exists, cur.Version); err != nil {
			return err
		}
		if exists {
			if err := domain.CheckCategoryType(cur.TxnType, c.TxnType, c.ID); err != nil {
				return err
			}
			out, err = scanCategory(tx.QueryRow(ctx, `update project_category set name = $3, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2 returning `+categoryColumns, p.TenantID, c.ID, c.Name))
			return err
		}
		out, err = scanCategory(tx.QueryRow(ctx, `insert into project_category (tenant_id, id, name, txn_type)
			values ($1, $2, $3, $4) returning `+categoryColumns, p.TenantID, c.ID, c.Name, string(c.TxnType)))
		created = err == nil
		return err
	})
	return out, created, concurrentCreate(err)
}

// ListCategories returns every category of the caller's tenant, by id.
func (s *Postgres) ListCategories(ctx context.Context) ([]domain.Category, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Category{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+categoryColumns+` from project_category where tenant_id = $1 order by id`, p.TenantID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Category, error) { return scanCategory(r) })
		return err
	})
	return out, err
}

// ---- Transactions ----

const txnColumns = `t.id::text, t.project_id, t.wbs_node_id::text, t.wbs_code, t.txn_type, t.category_id, t.line_property,
	t.txn_date::text, t.quantity::text, t.cost_price::text, t.sales_price::text, t.currency, t.cost_amount::text, t.sales_amount::text,
	t.worker_id::text, t.item_id::text, t.vendor_party_id::text, t.source_doc_type, t.source_doc_id::text, t.description,
	t.journal_id::text, t.proposal_id::text, t.created_by, t.created_at`

func scanTxn(row pgx.Row) (domain.Transaction, error) { return scanTxnWith(row) }

// scanTxnWith scans txnColumns followed by extra destinations.
func scanTxnWith(row pgx.Row, extra ...any) (domain.Transaction, error) {
	var t domain.Transaction
	var txnType string
	dest := []any{&t.ID, &t.ProjectID, &t.WBSNodeID, &t.WBSCode, &txnType, &t.CategoryID, &t.LineProperty,
		&t.TxnDate, &t.Quantity, &t.CostPrice, &t.SalesPrice, &t.Currency, &t.CostAmount, &t.SalesAmount,
		&t.WorkerID, &t.ItemID, &t.VendorPartyID, &t.SourceDocType, &t.SourceDocID, &t.Description,
		&t.JournalID, &t.ProposalID, &t.CreatedBy, &t.CreatedAt}
	err := row.Scan(append(dest, extra...)...)
	if err != nil {
		return t, err
	}
	t.TxnType = domain.TxnType(txnType)
	if t.CostAmount, err = amount(t.CostAmount, t.Currency); err != nil {
		return t, err
	}
	t.SalesAmount, err = amount(t.SalesAmount, t.Currency)
	return t, err
}

// SameTransaction reports whether a replayed Idempotency-Key carries the same
// request as the stored transaction. Both sides are priced (domain.PriceLine),
// so decimal strings compare exactly.
func SameTransaction(stored, in domain.Transaction) bool {
	return stored.ProjectID == in.ProjectID && stored.TxnType == in.TxnType && stored.CategoryID == in.CategoryID &&
		stored.LineProperty == in.LineProperty && stored.TxnDate == in.TxnDate && stored.Currency == in.Currency &&
		stored.Quantity == in.Quantity && stored.CostPrice == in.CostPrice && stored.SalesPrice == in.SalesPrice &&
		eqPtr(stored.WBSNodeID, in.WBSNodeID) && eqPtr(stored.WorkerID, in.WorkerID) && eqPtr(stored.ItemID, in.ItemID) &&
		eqPtr(stored.VendorPartyID, in.VendorPartyID) && stored.SourceDocType == in.SourceDocType &&
		eqPtr(stored.SourceDocID, in.SourceDocID) && stored.Description == in.Description
}

// CreateTransaction records a priced transaction (domain.PriceLine) on a
// project. The project row is locked, so a concurrent request with the same
// key waits and replays, and the stage cannot change underneath. The project
// must be IN_PROCESS and in the transaction's currency (PROJ-TXN-001/002), the
// category must exist with the transaction's type (PROJ-TXN-003), and a
// wbsNodeId must name a node of one of the project's plan versions; its WBS
// code is stored with it. A replayed Idempotency-Key returns the stored
// transaction with created=false; a different body under it is idempotency_key_reuse.
func (s *Postgres) CreateTransaction(ctx context.Context, in domain.Transaction, idempotencyKey string) (domain.Transaction, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Transaction
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stage, currency string
		err := tx.QueryRow(ctx, `select stage, currency from project where tenant_id = $1 and id = $2 for update`, p.TenantID, in.ProjectID).Scan(&stage, &currency)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("project")
		}
		if err != nil {
			return err
		}
		stored, err := scanTxn(tx.QueryRow(ctx, `select `+txnColumns+` from project_transaction t where t.tenant_id = $1 and t.idempotency_key = $2`, p.TenantID, idempotencyKey))
		switch {
		case err == nil:
			if !SameTransaction(stored, in) {
				return idempotencyReuse()
			}
			out = stored
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if err := domain.CheckTransactionProject(domain.Stage(stage), currency, in); err != nil {
			return err
		}
		var catType string
		err = tx.QueryRow(ctx, `select txn_type from project_category where tenant_id = $1 and id = $2`, p.TenantID, in.CategoryID).Scan(&catType)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := domain.CheckCategory(err == nil, domain.TxnType(catType), in); err != nil {
			return err
		}
		if in.WBSNodeID != nil {
			var code string
			err := tx.QueryRow(ctx, `select n.wbs_code from wbs_node n join plan_version v on v.tenant_id = n.tenant_id and v.id = n.plan_version_id
				where n.tenant_id = $1 and n.id = $2::uuid and v.project_id = $3`, p.TenantID, *in.WBSNodeID, in.ProjectID).Scan(&code)
			if errors.Is(err, pgx.ErrNoRows) {
				prob := httpx.NewProblem(http.StatusUnprocessableEntity, CodeValidation, "the transaction names an unknown WBS node")
				prob.Errors = map[string]string{"wbsNodeId": "is not a WBS node of this project"}
				return prob
			}
			if err != nil {
				return err
			}
			in.WBSCode = &code
		}
		out, err = scanTxn(tx.QueryRow(ctx, `insert into project_transaction as t (tenant_id, id, project_id, wbs_node_id, wbs_code,
				txn_type, category_id, line_property, txn_date, quantity, cost_price, sales_price, currency, cost_amount, sales_amount,
				worker_id, item_id, vendor_party_id, source_doc_type, source_doc_id, description, created_by, idempotency_key)
			values ($1, $2, $3, $4::text::uuid, $5, $6, $7, $8, $9::text::date, $10::text::numeric, $11::text::numeric, $12::text::numeric,
				$13, $14::text::numeric, $15::text::numeric, $16::text::uuid, $17::text::uuid, $18::text::uuid, $19, $20::text::uuid, $21, $22, $23)
			returning `+txnColumns,
			p.TenantID, ids.New(), in.ProjectID, in.WBSNodeID, in.WBSCode, string(in.TxnType), in.CategoryID, in.LineProperty,
			in.TxnDate, in.Quantity, in.CostPrice, in.SalesPrice, in.Currency, in.CostAmount, in.SalesAmount,
			in.WorkerID, in.ItemID, in.VendorPartyID, in.SourceDocType, in.SourceDocID, in.Description, p.UserID, idempotencyKey))
		if err != nil {
			return err
		}
		created = true
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == txnKeyConstraint {
		// A request for another project took the key concurrently.
		return domain.Transaction{}, false, idempotencyReuse()
	}
	return out, created, err
}

// ListTransactions returns one page of a project's transactions, newest date
// first (then newest entry, then id), after the transaction afterID when it is
// not empty (keyset on (txn_date, created_at, id), D3e). A project that is not
// visible is 404; a cursor that is not a transaction of the project gives an
// empty page.
func (s *Postgres) ListTransactions(ctx context.Context, projectID, afterID string, limit int) (TransactionPage, error) {
	p, _ := tenant.FromContext(ctx)
	limit = pageLimit(limit)
	page := TransactionPage{Items: []domain.Transaction{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, projectID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+txnColumns+` from project_transaction t
			where t.tenant_id = $1 and t.project_id = $2
			and ($3::text = '' or (t.txn_date, t.created_at, t.id) < (select c.txn_date, c.created_at, c.id from project_transaction c
				where c.tenant_id = $1 and c.project_id = $2 and c.id = nullif($3, '')::uuid))
			order by t.txn_date desc, t.created_at desc, t.id desc limit $4`,
			p.TenantID, projectID, afterID, limit+1)
		if err != nil {
			return err
		}
		page.Items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Transaction, error) { return scanTxn(r) })
		if err != nil {
			return err
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextID = page.Items[limit-1].ID
		}
		return nil
	})
	return page, err
}

// GetTransactionBilling returns a transaction and its candidate view (with its
// project's current stage) for the "why not billed" explanation (BR-41.3).
func (s *Postgres) GetTransactionBilling(ctx context.Context, id string) (TransactionBilling, error) {
	p, _ := tenant.FromContext(ctx)
	var out TransactionBilling
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		var milestone *bool
		var milestoneDate *string
		var stage, ptype string
		row := tx.QueryRow(ctx, `select `+txnColumns+`, m.is_billable_milestone, m.early_finish::text, p.stage, p.project_type
			from project_transaction t join project p on p.tenant_id = t.tenant_id and p.id = t.project_id`+milestoneJoin+`
			where t.tenant_id = $1 and t.id = $2::uuid`, p.TenantID, id)
		out.Transaction, err = scanTxnWith(row, &milestone, &milestoneDate, &stage, &ptype)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("transaction")
		}
		if err != nil {
			return err
		}
		out.ProjectType = domain.ProjectType(ptype)
		t := out.Transaction
		out.Candidate = domain.Candidate{
			ID: t.ID, ProjectID: t.ProjectID, TxnType: string(t.TxnType), LineProperty: t.LineProperty,
			ProjectStage: domain.Stage(stage), Proposed: t.ProposalID != nil,
		}
		if out.Candidate.TxnDate, err = domain.ParseDate(t.TxnDate); err != nil {
			return err
		}
		return setMilestone(&out.Candidate, milestone, milestoneDate)
	})
	return out, err
}

// setMilestone copies the milestoneJoin columns onto a candidate.
func setMilestone(c *domain.Candidate, milestone *bool, date *string) error {
	c.Milestone = milestone != nil && *milestone
	if date == nil {
		return nil
	}
	d, err := domain.ParseDate(*date)
	c.MilestoneDate = d
	return err
}

// ---- Invoice proposals ----

const proposalColumns = `pr.id::text, pr.project_id, pr.customer_party_id::text, pr.status, pr.currency, pr.from_date::text, pr.to_date::text,
	pr.txn_types, pr.total_cost::text, pr.total_sales::text,
	(select count(*) from project_invoice_proposal_line l where l.tenant_id = pr.tenant_id and l.proposal_id = pr.id)::int,
	pr.created_by, pr.approved_by, pr.version, pr.created_at, pr.updated_at`

func scanProposal(row pgx.Row) (domain.InvoiceProposal, error) {
	var pr domain.InvoiceProposal
	var status string
	var types []string
	err := row.Scan(&pr.ID, &pr.ProjectID, &pr.CustomerPartyID, &status, &pr.Currency, &pr.FromDate, &pr.ToDate,
		&types, &pr.TotalCost, &pr.TotalSales, &pr.LineCount, &pr.CreatedBy, &pr.ApprovedBy, &pr.Version, &pr.CreatedAt, &pr.UpdatedAt)
	if err != nil {
		return pr, err
	}
	pr.Status = domain.ProposalStatus(status)
	pr.Lines = []domain.ProposalLine{}
	pr.TxnTypes = make([]domain.TxnType, len(types))
	for i, t := range types {
		pr.TxnTypes[i] = domain.TxnType(t)
	}
	if pr.TotalCost, err = amount(pr.TotalCost, pr.Currency); err != nil {
		return pr, err
	}
	pr.TotalSales, err = amount(pr.TotalSales, pr.Currency)
	return pr, err
}

// SameProposalRequest reports whether a replayed Idempotency-Key carries the
// same selection as the stored proposal (types after domain.BillableTypes).
func SameProposalRequest(stored domain.InvoiceProposal, in domain.ProposalRequest, types []domain.TxnType) bool {
	if stored.ProjectID != in.ProjectID || stored.FromDate != in.From || stored.ToDate != in.To || len(stored.TxnTypes) != len(types) {
		return false
	}
	for i := range types {
		if stored.TxnTypes[i] != types[i] {
			return false
		}
	}
	return true
}

func readProposal(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (domain.InvoiceProposal, error) {
	q := `select ` + proposalColumns + ` from project_invoice_proposal pr where pr.tenant_id = $1 and pr.id = $2::uuid`
	if forUpdate {
		q += ` for update of pr`
	}
	pr, err := scanProposal(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return pr, notFound("invoice proposal")
	}
	return pr, err
}

func withProposalLines(ctx context.Context, tx pgx.Tx, tenantID string, pr domain.InvoiceProposal) (domain.InvoiceProposal, error) {
	rows, err := tx.Query(ctx, `select l.line_no, l.transaction_id::text, t.txn_type, t.txn_date::text, t.category_id, t.description,
			l.quantity::text, l.unit_price::text, l.cost_amount::text, l.amount::text
		from project_invoice_proposal_line l
		join project_transaction t on t.tenant_id = l.tenant_id and t.id = l.transaction_id
		where l.tenant_id = $1 and l.proposal_id = $2::uuid order by l.line_no`, tenantID, pr.ID)
	if err != nil {
		return pr, err
	}
	pr.Lines, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.ProposalLine, error) {
		var l domain.ProposalLine
		var t string
		err := r.Scan(&l.LineNo, &l.TransactionID, &t, &l.TxnDate, &l.CategoryID, &l.Description, &l.Quantity, &l.UnitPrice, &l.CostAmount, &l.Amount)
		l.TxnType = domain.TxnType(t)
		if err == nil {
			l.CostAmount, err = amount(l.CostAmount, pr.Currency)
		}
		if err == nil {
			l.Amount, err = amount(l.Amount, pr.Currency)
		}
		return l, err
	})
	return pr, err
}

// CreateProposal builds a DRAFT invoice proposal for one project (FR-41.6). The
// project must bill the requested types (domain.BillableTypes, PROJ-PROP-004).
// It selects, in date order, the project's transactions that are CHARGEABLE,
// not on a live proposal, dated in [From, To] and of a selected type (BR-41.3),
// with SELECT … FOR UPDATE SKIP LOCKED (BR-41.8): a concurrent build skips the
// rows another build holds, so two builds never select the same transaction.
// Every selected row is re-checked with domain.Select. One line per
// transaction carries its quantity, unit sales price and sales amount; the
// totals are their sums. Nothing selected is PROJ-PROP-002. A replayed
// Idempotency-Key returns the stored proposal with created=false (also when
// the replay races the first request); a different selection under the key is
// idempotency_key_reuse.
func (s *Postgres) CreateProposal(ctx context.Context, in domain.ProposalRequest, idempotencyKey string) (domain.InvoiceProposal, bool, error) {
	out, created, err := s.createProposal(ctx, in, idempotencyKey)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == proposalKeyConstraint {
		// A concurrent build with the same key committed first: replay it.
		return s.replayProposal(ctx, in, idempotencyKey)
	}
	return out, created, err
}

func (s *Postgres) replayProposal(ctx context.Context, in domain.ProposalRequest, idempotencyKey string) (domain.InvoiceProposal, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.InvoiceProposal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var ptype string
		if err := tx.QueryRow(ctx, `select project_type from project where tenant_id = $1 and id = $2`, p.TenantID, in.ProjectID).Scan(&ptype); err != nil {
			return err
		}
		types, err := domain.BillableTypes(domain.ProjectType(ptype), in.TxnTypes)
		if err != nil {
			return err
		}
		stored, err := scanProposal(tx.QueryRow(ctx, `select `+proposalColumns+` from project_invoice_proposal pr
			where pr.tenant_id = $1 and pr.idempotency_key = $2`, p.TenantID, idempotencyKey))
		if err != nil {
			return err
		}
		if !SameProposalRequest(stored, in, types) {
			return idempotencyReuse()
		}
		out, err = withProposalLines(ctx, tx, p.TenantID, stored)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, idempotencyReuse()
	}
	return out, false, err
}

func (s *Postgres) createProposal(ctx context.Context, in domain.ProposalRequest, idempotencyKey string) (domain.InvoiceProposal, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.InvoiceProposal
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// FOR SHARE: builds of one project run in parallel (SKIP LOCKED keeps
		// them disjoint) while a stage move waits for them (BR-41.7 counts them).
		var stage, ptype, currency string
		var customer *string
		err := tx.QueryRow(ctx, `select stage, project_type, currency, customer_party_id::text from project
			where tenant_id = $1 and id = $2 for share`, p.TenantID, in.ProjectID).Scan(&stage, &ptype, &currency, &customer)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("project")
		}
		if err != nil {
			return err
		}
		types, err := domain.BillableTypes(domain.ProjectType(ptype), in.TxnTypes)
		if err != nil {
			return err
		}
		// Serialize requests with one key: a retry racing the first request
		// would otherwise skip the rows the first one holds (SKIP LOCKED), find
		// nothing and answer PROJ-PROP-002 instead of replaying.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('projects:proposal:' || $1 || ':' || $2, 0))`,
			p.TenantID, idempotencyKey); err != nil {
			return err
		}
		stored, err := scanProposal(tx.QueryRow(ctx, `select `+proposalColumns+` from project_invoice_proposal pr
			where pr.tenant_id = $1 and pr.idempotency_key = $2`, p.TenantID, idempotencyKey))
		switch {
		case err == nil:
			if !SameProposalRequest(stored, in, types) {
				return idempotencyReuse()
			}
			out, err = withProposalLines(ctx, tx, p.TenantID, stored)
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		selected, err := selectBillable(ctx, tx, p.TenantID, in, domain.Stage(stage), domain.ProjectType(ptype), types)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			return domain.EmptySelection(in.ProjectID, domain.Stage(stage), domain.ProjectType(ptype), types)
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into project_invoice_proposal (tenant_id, id, project_id, customer_party_id, currency,
				from_date, to_date, txn_types, total_cost, total_sales, created_by, idempotency_key)
			select $1, $2, $3, $4::text::uuid, $5, $6::text::date, $7::text::date, $8, sum(t.cost_amount), sum(t.sales_amount), $9, $10
			from project_transaction t where t.tenant_id = $1 and t.id = any($11::uuid[])`,
			p.TenantID, id, in.ProjectID, customer, currency, in.From, in.To, domain.TypeNames(types), p.UserID, idempotencyKey, selected); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into project_invoice_proposal_line (tenant_id, proposal_id, line_no, transaction_id,
				quantity, unit_price, cost_amount, amount)
			select t.tenant_id, $2::uuid, s.ord::int, t.id, t.quantity, t.sales_price, t.cost_amount, t.sales_amount
			from unnest($3::uuid[]) with ordinality as s(id, ord)
			join project_transaction t on t.tenant_id = $1 and t.id = s.id`, p.TenantID, id, selected); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update project_transaction set proposal_id = $2::uuid where tenant_id = $1 and id = any($3::uuid[])`,
			p.TenantID, id, selected); err != nil {
			return err
		}
		created = true
		if out, err = readProposal(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		out, err = withProposalLines(ctx, tx, p.TenantID, out)
		return err
	})
	return out, created, err
}

// selectBillable locks and returns, in date order, the ids of the project's
// transactions that the BR-41.3 predicate selects. The SQL narrows to the
// unbilled chargeable rows of the window and types (the partial index);
// domain.Select re-checks every row, including the project stage.
func selectBillable(ctx context.Context, tx pgx.Tx, tenantID string, in domain.ProposalRequest, stage domain.Stage, ptype domain.ProjectType, types []domain.TxnType) ([]string, error) {
	from, err := domain.ParseDate(in.From)
	if err != nil {
		return nil, err
	}
	to, err := domain.ParseDate(in.To)
	if err != nil {
		return nil, err
	}
	milestones := domain.BillsMilestones(ptype)
	rows, err := tx.Query(ctx, `select t.id::text, t.txn_type, t.line_property, t.txn_date::text, m.is_billable_milestone, m.early_finish::text
		from project_transaction t`+milestoneJoin+`
		where t.tenant_id = $1 and t.project_id = $2 and t.proposal_id is null and t.line_property = 'CHARGEABLE'
			and t.txn_date between $3::text::date and $4::text::date and t.txn_type = any($5::text[])
			and (not $6::bool or (m.is_billable_milestone and m.early_finish <= $4::text::date))
		order by t.txn_date, t.created_at, t.id
		for update of t skip locked`, tenantID, in.ProjectID, in.From, in.To, domain.TypeNames(types), milestones)
	if err != nil {
		return nil, err
	}
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.Candidate, error) {
		var c domain.Candidate
		var date string
		var milestone *bool
		var milestoneDate *string
		if err := r.Scan(&c.ID, &c.TxnType, &c.LineProperty, &date, &milestone, &milestoneDate); err != nil {
			return c, err
		}
		c.ProjectID, c.ProjectStage = in.ProjectID, stage
		if c.TxnDate, err = domain.ParseDate(date); err != nil {
			return c, err
		}
		return c, setMilestone(&c, milestone, milestoneDate)
	})
	if err != nil {
		return nil, err
	}
	selected, _ := domain.Select(domain.Criteria{From: from, To: to, TxnTypes: domain.TypeNames(types),
		ProjectIDs: []string{in.ProjectID}, MilestonesOnly: milestones}, cands)
	return selected, nil
}

// refreshTotals recomputes a proposal's totals from its lines (exact numeric
// sums), sets its status and bumps its version.
func refreshTotals(ctx context.Context, tx pgx.Tx, tenantID, id string, status domain.ProposalStatus) error {
	_, err := tx.Exec(ctx, `update project_invoice_proposal pr set
			total_cost = coalesce((select sum(l.cost_amount) from project_invoice_proposal_line l where l.tenant_id = pr.tenant_id and l.proposal_id = pr.id), 0),
			total_sales = coalesce((select sum(l.amount) from project_invoice_proposal_line l where l.tenant_id = pr.tenant_id and l.proposal_id = pr.id), 0),
			status = $3, approved_by = case when $3 = 'DRAFT' then null else approved_by end,
			version = version + 1, updated_at = now()
		where pr.tenant_id = $1 and pr.id = $2::uuid`, tenantID, id, string(status))
	return err
}

// GetProposal returns one proposal with its lines.
func (s *Postgres) GetProposal(ctx context.Context, id string) (domain.InvoiceProposal, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.InvoiceProposal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pr, err := readProposal(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		out, err = withProposalLines(ctx, tx, p.TenantID, pr)
		return err
	})
	return out, err
}

// ListProposals returns one page of a project's proposals, newest first, with
// their totals and line counts but without lines (keyset on (created_at, id)
// after f.AfterID, D3e). A project that is not visible is 404.
func (s *Postgres) ListProposals(ctx context.Context, f ProposalFilter) (ProposalPage, error) {
	p, _ := tenant.FromContext(ctx)
	limit := pageLimit(f.Limit)
	page := ProposalPage{Items: []domain.InvoiceProposal{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockProject(ctx, tx, p.TenantID, f.ProjectID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+proposalColumns+` from project_invoice_proposal pr
			where pr.tenant_id = $1 and pr.project_id = $2 and ($3::text = '' or pr.status = $3)
			and ($4::text = '' or (pr.created_at, pr.id) < (select c.created_at, c.id from project_invoice_proposal c
				where c.tenant_id = $1 and c.project_id = $2 and c.id = nullif($4, '')::uuid))
			order by pr.created_at desc, pr.id desc limit $5`, p.TenantID, f.ProjectID, string(f.Status), f.AfterID, limit+1)
		if err != nil {
			return err
		}
		page.Items, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (domain.InvoiceProposal, error) { return scanProposal(r) })
		if err != nil {
			return err
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextID = page.Items[limit-1].ID
		}
		return nil
	})
	return page, err
}

// lockProposalAt locks a proposal and checks the caller's If-Match version.
func lockProposalAt(ctx context.Context, tx pgx.Tx, tenantID, id string, expected int) (domain.InvoiceProposal, error) {
	cur, err := readProposal(ctx, tx, tenantID, id, true)
	if err != nil {
		return cur, err
	}
	if cur.Version != expected {
		return cur, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the proposal changed since it was read; re-read and retry")
	}
	return cur, nil
}

// DecideProposal approves (DRAFT with lines) or cancels (DRAFT or APPROVED) a
// proposal when expected matches its version. Cancelling returns every line's
// transaction to the pool (FR-41.6); the canceled proposal keeps its lines.
func (s *Postgres) DecideProposal(ctx context.Context, id string, expected int, to domain.ProposalStatus) (domain.InvoiceProposal, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.InvoiceProposal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockProposalAt(ctx, tx, p.TenantID, id, expected)
		if err != nil {
			return err
		}
		if err := domain.CheckProposalMove(cur.Status, to, cur.LineCount); err != nil {
			return err
		}
		if to == domain.ProposalCanceled {
			if _, err := tx.Exec(ctx, `update project_transaction set proposal_id = null where tenant_id = $1 and proposal_id = $2::uuid`, p.TenantID, id); err != nil {
				return err
			}
		}
		var approver *string
		if to == domain.ProposalApproved {
			approver = &p.UserID
		}
		if _, err := tx.Exec(ctx, `update project_invoice_proposal set status = $3, approved_by = coalesce($4, approved_by),
				version = version + 1, updated_at = now() where tenant_id = $1 and id = $2::uuid`,
			p.TenantID, id, string(to), approver); err != nil {
			return err
		}
		if out, err = readProposal(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		out, err = withProposalLines(ctx, tx, p.TenantID, out)
		return err
	})
	return out, err
}

// RemoveProposalLine takes the line of transaction txnID off a DRAFT or
// APPROVED proposal when expected matches its version, returns the transaction
// to the pool and recomputes the totals; an APPROVED proposal goes back to
// DRAFT (domain.CheckLineRemoval).
func (s *Postgres) RemoveProposalLine(ctx context.Context, id string, expected int, txnID string) (domain.InvoiceProposal, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.InvoiceProposal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockProposalAt(ctx, tx, p.TenantID, id, expected)
		if err != nil {
			return err
		}
		next, err := domain.CheckLineRemoval(cur.Status)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from project_invoice_proposal_line where tenant_id = $1 and proposal_id = $2::uuid and transaction_id = $3::uuid`,
			p.TenantID, id, txnID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return notFound("proposal line")
		}
		if _, err := tx.Exec(ctx, `update project_transaction set proposal_id = null where tenant_id = $1 and id = $2::uuid and proposal_id = $3::uuid`,
			p.TenantID, txnID, id); err != nil {
			return err
		}
		if err := refreshTotals(ctx, tx, p.TenantID, id, next); err != nil {
			return err
		}
		if out, err = readProposal(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		out, err = withProposalLines(ctx, tx, p.TenantID, out)
		return err
	})
	return out, err
}

// openProposals counts the DRAFT and APPROVED proposals of a project (BR-41.7).
func openProposals(ctx context.Context, tx pgx.Tx, tenantID, projectID string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `select count(*) from project_invoice_proposal where tenant_id = $1 and project_id = $2 and status in ('DRAFT', 'APPROVED')`,
		tenantID, projectID).Scan(&n)
	return n, err
}
