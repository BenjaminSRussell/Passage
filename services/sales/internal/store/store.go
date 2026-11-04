// Package store persists sales orders, holds and credit profiles in Postgres
// under row-level security and writes events to the outbox in the same
// transaction as each state change.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Stable problem codes (README lists the full catalogue).
const (
	CodeCreditHold         = "SLS-0005"
	CodeVersionConflict    = "SLS-0006"
	CodeDuplicateIntake    = "SLS-0007"
	CodeAgreementExhausted = "SLS-0008"
	CodeProductNotListed   = "SLS-0011"
	CodeNotConfirmable     = "SLS-0019"
	CodeHoldBlocks         = "SLS-0021"
	CodeHoldNotActive      = "SLS-0022"
	CodeOneTimeLimit       = "SLS-0016"
	CodeFieldCryptMissing  = "SLS-0054"
	CodeHoldNotApplicable  = "SLS-0028"
	CodeHoldNotRejectable  = "SLS-0062"
	CodeAgreementNotValid  = "SLS-0029"
	CodeAgreementClosed    = "SLS-0052"
	CodeCommitmentBelow    = "SLS-0053"
	CodeOrderNotFound      = "SLS-0100"
	CodeHoldNotFound       = "SLS-0101"
	CodeProfileNotFound    = "SLS-0102"
	CodeAgreementNotFound  = "SLS-0103"
	CodeListingNotFound    = "SLS-0104"
	CodeIdempotencyReuse   = "idempotency_key_reuse"

	uniqueViolation = "23505"
	eventSource     = "sales"
	dayLayout       = "2006-01-02"
)

// Line is one order line. LineNet is computed by the caller with domain.LineNet.
type Line struct {
	ID                string          `json:"id"`
	LineNo            int             `json:"lineNo"`
	ProductID         string          `json:"productId"`
	Quantity          decimal.Decimal `json:"quantity"`
	Unit              string          `json:"unit"`
	SiteID            string          `json:"siteId"`
	WarehouseID       string          `json:"warehouseId"`
	UnitPrice         decimal.Decimal `json:"unitPrice"`
	DiscPct           decimal.Decimal `json:"discPct"`
	LineNet           decimal.Decimal `json:"lineNet"`
	RequestedShipDate string          `json:"requestedShipDate"`
	ConfirmedShipDate *string         `json:"confirmedShipDate,omitempty"`
	LineStatus        string          `json:"lineStatus"`
	// DeliveredQty and InvoicedQty are what the packing slips and invoices
	// recorded through POST /orders/{id}:record-progress have covered (BR-27.5).
	DeliveredQty decimal.Decimal `json:"deliveredQty"`
	InvoicedQty  decimal.Decimal `json:"invoicedQty"`

	// DeliveryDateControl is the line's own delivery date control (FR-27.3);
	// absent, the legal entity's default applies at confirm. ConfirmedReceiptDate
	// is the confirmed ship date plus the transport days, rolled to the next
	// receiving day (BR-27.2), of the customer's delivery terms as they were when
	// the order was confirmed (Order.ConfirmedDeliveryTerms).
	DeliveryDateControl  *string `json:"deliveryDateControl,omitempty"`
	ConfirmedReceiptDate *string `json:"confirmedReceiptDate,omitempty"`

	// ConfigurationID is the saved configuration the line is sold to; ProductionOrderID
	// the production order planned for it (absent until :plan-production ran).
	ConfigurationID   *string `json:"configurationId,omitempty"`
	ProductionOrderID *string `json:"productionOrderId,omitempty"`

	// SupplementaryOf is the id of the parent line a MANDATORY supplementary
	// line was added for at commit, ParentLineNo that line's number and
	// SupplementaryRuleID the rule that added it (FR-27.14, BR-27.14). All
	// three are absent on ordinary lines.
	SupplementaryOf     *string `json:"supplementaryOf,omitempty"`
	ParentLineNo        *int    `json:"parentLineNo,omitempty"`
	SupplementaryRuleID *string `json:"supplementaryRuleId,omitempty"`
	// suppBase and suppPerBase are the adding rule's base quantity and
	// quantity per base, kept so a parent change recomputes the line.
	suppBase    decimal.Decimal
	suppPerBase decimal.Decimal
	// added marks a line a change adds in memory, still to be inserted.
	added bool
}

// Notice is a non-blocking message returned with a result, such as the
// SLS-0012 warning for each mandatory supplementary line an order received.
type Notice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	LineNo  int    `json:"lineNo,omitempty"`
}

// Hold is an order hold. OrderAmount is the net total when the hold was applied.
type Hold struct {
	ID            string          `json:"id"`
	HoldType      string          `json:"holdType"`
	ReasonCode    string          `json:"reasonCode"`
	Status        string          `json:"status"`
	OrderAmount   decimal.Decimal `json:"orderAmount"`
	AppliedBy     string          `json:"appliedBy"`
	ReleasedBy    *string         `json:"releasedBy,omitempty"`
	ReleasedUntil *time.Time      `json:"releasedUntil,omitempty"`
	AppliedAt     time.Time       `json:"appliedAt"`
	// RejectedBy, RejectedAt and RejectionComment are set when a credit
	// manager rejected the hold (status REJECTED, slice 26).
	RejectedBy       *string    `json:"rejectedBy,omitempty"`
	RejectedAt       *time.Time `json:"rejectedAt,omitempty"`
	RejectionComment *string    `json:"rejectionComment,omitempty"`
}

// Order is a sales order header with its lines and holds.
type Order struct {
	ID                string            `json:"id"`
	LegalEntityID     string            `json:"legalEntityId"`
	OrderNumber       string            `json:"orderNumber"`
	CustomerID        string            `json:"customerId"`
	InvoiceAccountID  string            `json:"invoiceAccountId"`
	OrderType         string            `json:"orderType"`
	Status            string            `json:"status"`
	Currency          string            `json:"currency"`
	RequestedShipDate string            `json:"requestedShipDate"`
	Channel           string            `json:"channel"`
	ExternalRef       *string           `json:"externalRef,omitempty"`
	CustomerGroup     *string           `json:"customerGroup,omitempty"`
	NetTotal          decimal.Decimal   `json:"netTotal"`
	Version           int               `json:"version"`
	CreditCheck       json.RawMessage   `json:"creditCheck,omitempty"`
	Lines             []Line            `json:"lines"`
	Holds             []Hold            `json:"holds"`
	ListingOverrides  []ListingOverride `json:"listingOverrides"`
	// OneTime is the walk-in buyer of a one-time order (FR-27.22), opened from
	// its sealed column for the response; absent on an ordinary order.
	OneTime *domain.OneTimeParty `json:"oneTime,omitempty"`
	oneTime *sealedParty
	// ConfirmedDeliveryTerms are the customer's delivery terms copied onto the
	// order at confirm (BR-27.2); they date every line's confirmed receipt, so a
	// later change of the customer's terms does not move a confirmed promise.
	// Absent until the order is confirmed (and on orders confirmed before the
	// terms existed, whose receipt date is their ship date).
	ConfirmedDeliveryTerms *domain.DeliveryTerms `json:"confirmedDeliveryTerms,omitempty"`
	// dates, when set by a store flow, makes ApplyChange give a re-promised
	// line the confirmed ship date its delivery date control decides (BR-27.2)
	// instead of the requested date.
	dates *dateRule
	// Warnings is set only on the result of a create (including a replay),
	// a quotation conversion or an agreement release.
	Warnings []Notice `json:"warnings,omitempty"`
	// PendingChange is set only on the result of a PATCH that was stored as a
	// change request awaiting approval instead of being applied.
	PendingChange *OrderChangeRequest `json:"pendingChange,omitempty"`
	RequestHash   string              `json:"-"`
	CreatedAt     time.Time           `json:"createdAt"`
	UpdatedAt     time.Time           `json:"updatedAt"`
}

// NewOrder is an intake request after validation.
type NewOrder struct {
	LegalEntityID     string
	CustomerID        string
	InvoiceAccountID  string
	OrderType         string
	Currency          string
	RequestedShipDate string
	Channel           string
	ExternalRef       *string
	IdempotencyKey    *string
	RequestHash       string
	Lines             []Line

	// CustomerGroup stands in for the w7 customer group until the party lookup
	// exists; GROUP listing entries apply only when it is given (BR-27.13).
	CustomerGroup *string
	// OneTime makes the order a one-time customer order (FR-27.22): the party
	// is sealed on the header and the order is held to one_time_order_limit.
	OneTime *domain.OneTimeParty
	// oneTimeSealed and sameRequest are set by sealOneTime: the envelope that
	// is stored, and the replay check against a keyed request hash.
	oneTimeSealed []byte
	sameRequest   func(stored string) bool
	// OrderDate is the day listing validity is evaluated on (BR-27.13).
	OrderDate time.Time
	// Actor is the caller; CanOverrideListing and ListingOverrideReason come from
	// the caller's duties (sales.order.approve) and the request body (FR-27.15).
	Actor                 string
	CanOverrideListing    bool
	ListingOverrideReason string
}

// OrderSummary is one row of the order list: the header without lines, plus
// the types of the order's ACTIVE holds.
type OrderSummary struct {
	ID                string          `json:"id"`
	LegalEntityID     string          `json:"legalEntityId"`
	OrderNumber       string          `json:"orderNumber"`
	CustomerID        string          `json:"customerId"`
	OrderType         string          `json:"orderType"`
	Status            string          `json:"status"`
	Currency          string          `json:"currency"`
	RequestedShipDate string          `json:"requestedShipDate"`
	Channel           string          `json:"channel"`
	ExternalRef       *string         `json:"externalRef,omitempty"`
	NetTotal          decimal.Decimal `json:"netTotal"`
	LineCount         int             `json:"lineCount"`
	ActiveHolds       []string        `json:"activeHolds"`
	Version           int             `json:"version"`
	CreatedAt         time.Time       `json:"createdAt"`
}

// OrderFilter narrows the order list. Empty fields do not filter. HoldType
// keeps orders with an ACTIVE hold of that type (the credit release queue).
// Before is the id of the last order on the previous page.
type OrderFilter struct {
	Status     string
	CustomerID string
	HoldType   string
	Before     string
	Limit      int
}

// OrderPage is one page of orders, newest first. NextCursor is set when more
// rows follow.
type OrderPage struct {
	Items      []OrderSummary `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// ConfirmResult is the outcome of :confirm. Confirmed is false when the credit
// check placed a CREDIT hold; the order then stays open with that hold.
type ConfirmResult struct {
	Order      Order
	Evaluation domain.CreditEvaluation
	Confirmed  bool
}

// CreditProfile is the stored profile for one customer in one legal entity.
type CreditProfile struct {
	LegalEntityID string `json:"legalEntityId"`
	CustomerID    string `json:"customerId"`
	domain.CreditProfile
	Exposure  domain.Exposure `json:"exposure"`
	Version   int             `json:"version"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

// Store is the persistence contract the API depends on.
type Store interface {
	CreateOrder(ctx context.Context, in NewOrder) (Order, bool, error)
	GetOrder(ctx context.Context, id string) (Order, error)
	ListOrders(ctx context.Context, f OrderFilter) (OrderPage, error)
	ConfirmOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ConfirmResult, error)
	ReleaseHold(ctx context.Context, orderID, holdID string, version int, actor string, now time.Time) (ConfirmResult, error)
	RejectHold(ctx context.Context, orderID, holdID string, version int, comment, actor string, now time.Time) (Order, error)
	ChangeOrder(ctx context.Context, id string, version int, req ChangeRequest, actor string, now time.Time) (Order, error)
	CancelOrderAs(ctx context.Context, id string, version int, reason, actor string, approver bool, now time.Time) (Order, error)
	CancelRemainder(ctx context.Context, id string, version int, reason, actor string, now time.Time) (Order, error)
	CancelLineRemainder(ctx context.Context, id string, lineNo, version int, reason, actor string, now time.Time) (Order, error)
	ListOrderVersions(ctx context.Context, id string) ([]OrderVersion, error)
	GetCreditProfile(ctx context.Context, legalEntityID, customerID string) (CreditProfile, error)
	ListCreditProfiles(ctx context.Context, legalEntityID string, limit int) ([]CreditProfile, error)
	ListCreditProfilesReviewDue(ctx context.Context, legalEntityID, dueBy string, limit int) ([]CreditProfile, error)
	PutCreditProfile(ctx context.Context, in CreditProfile, version int) (CreditProfile, error)
	ApplyHold(ctx context.Context, orderID string, version int, in HoldRequest, actor string) (Order, error)
	CreateListing(ctx context.Context, in ProductListing) (ProductListing, error)
	ListListings(ctx context.Context, f ListingFilter) ([]ProductListing, error)
	DeleteListing(ctx context.Context, id string) error
	CreateAgreement(ctx context.Context, in NewAgreement) (Agreement, bool, error)
	ListAgreements(ctx context.Context, f AgreementFilter) ([]Agreement, error)
	GetAgreement(ctx context.Context, id string) (Agreement, error)
	ReleaseAgreement(ctx context.Context, id string, in AgreementRelease, now time.Time) (ReleaseResult, bool, error)
	ReviseAgreement(ctx context.Context, id string, version int, in AgreementRevision, actor string, now time.Time) (Agreement, error)
	CloseAgreement(ctx context.Context, id string, version int, reason, actor string, now time.Time) (Agreement, error)
	ListAgreementVersions(ctx context.Context, id string) ([]AgreementVersion, error)
	CreateScheduleRelease(ctx context.Context, in NewScheduleRelease, now time.Time) (ScheduleRelease, bool, error)
	ListScheduleReleases(ctx context.Context, agreementID string, limit int) ([]ScheduleRelease, error)
	GetScheduleRelease(ctx context.Context, agreementID, id string) (ScheduleRelease, error)
	ResolveScheduleRelease(ctx context.Context, agreementID, id string, version int, decision, reason, actor string, now time.Time) (ScheduleRelease, error)
	RecordShipped(ctx context.Context, agreementID string, version int, cumShipped decimal.Decimal, resetDate *string, actor string, now time.Time) (Agreement, error)
	CreateQuotation(ctx context.Context, in NewQuotation) (Quotation, bool, error)
	ListQuotations(ctx context.Context, f QuotationFilter) ([]Quotation, error)
	GetQuotation(ctx context.Context, id string) (Quotation, error)
	SendQuotation(ctx context.Context, id string, version int, now time.Time) (Quotation, error)
	LoseQuotation(ctx context.Context, id string, version int, reason string) (Quotation, error)
	ConvertQuotation(ctx context.Context, id string, in ConvertRequest, now time.Time) (QuotationConversion, error)
	ExpireQuotations(ctx context.Context, legalEntityID string, today time.Time, actor string) (ExpireResult, error)
	Summary(ctx context.Context, f SummaryFilter) (Summary, error)
	ListHolds(ctx context.Context, f HoldQueueFilter) ([]HoldQueueItem, error)
	CreateSupplementaryRule(ctx context.Context, in domain.SupplementaryRule, actor string) (domain.SupplementaryRule, error)
	ListSupplementaryRules(ctx context.Context, f SupplementaryRuleFilter) ([]domain.SupplementaryRule, error)
	DeleteSupplementaryRule(ctx context.Context, id string) error
	CreateProposalRule(ctx context.Context, in domain.ProposalRule, actor string) (domain.ProposalRule, error)
	ListProposalRules(ctx context.Context, f ProposalRuleFilter) ([]domain.ProposalRule, error)
	DeleteProposalRule(ctx context.Context, id string) error
	RecordProposalResponse(ctx context.Context, in NewProposalResponse) (ProposalResponse, bool, error)
	CreateAlternativeRule(ctx context.Context, in domain.AlternativeRule, actor string) (domain.AlternativeRule, error)
	ListAlternativeRules(ctx context.Context, f AlternativeRuleFilter) ([]domain.AlternativeRule, error)
	DeleteAlternativeRule(ctx context.Context, id string) error
	CreateAllocationQuota(ctx context.Context, in NewAllocationQuota) (AllocationQuota, bool, error)
	ListAllocationQuotas(ctx context.Context, f AllocationFilter) ([]AllocationQuota, error)
	GetAllocationQuota(ctx context.Context, id string) (AllocationQuota, error)
	ChangeAllocationQuota(ctx context.Context, id string, version int, qty decimal.Decimal, actor string, now time.Time) (AllocationQuota, error)
	CreateDeliveryTerms(ctx context.Context, in NewDeliveryTerms) (DeliveryTerms, bool, error)
	ListDeliveryTerms(ctx context.Context, f DeliveryTermsFilter) ([]DeliveryTerms, error)
	GetDeliveryTerms(ctx context.Context, id string) (DeliveryTerms, error)
	ReplaceDeliveryTerms(ctx context.Context, id string, version int, terms domain.DeliveryTerms, actor string, now time.Time) (DeliveryTerms, error)
	RecordProgress(ctx context.Context, in NewProgress, now time.Time) (OrderProgress, bool, error)
	ListProgress(ctx context.Context, f ProgressFilter) ([]OrderProgress, error)
	PromiseLine(ctx context.Context, orderID string, version int, req PromiseRequest, actor string, now time.Time) (PromiseResult, error)
	ListScheduleLines(ctx context.Context, orderID string) ([]ScheduleLine, error)
	ListBackorders(ctx context.Context, f BackorderFilter) ([]BackorderItem, error)
	CreateBackorderRun(ctx context.Context, in NewBackorderRun, now time.Time) (BackorderRun, bool, error)
	ListBackorderRuns(ctx context.Context, f BackorderRunFilter) ([]BackorderRun, error)
	GetBackorderRun(ctx context.Context, id string) (BackorderRun, error)
	CommitBackorderRun(ctx context.Context, id string, version int, actor string, now time.Time) (BackorderRun, error)
	GetChangePolicy(ctx context.Context, legalEntityID, customerID string) (ChangePolicy, error)
	PutChangePolicy(ctx context.Context, in ChangePolicy, version int, actor string) (ChangePolicy, error)
	ListChangeRequests(ctx context.Context, f ChangeRequestFilter) ([]OrderChangeRequest, error)
	GetChangeRequest(ctx context.Context, id string) (OrderChangeRequest, error)
	ApproveChangeRequest(ctx context.Context, id string, version int, comment, actor string, now time.Time) (ChangeDecision, error)
	RejectChangeRequest(ctx context.Context, id string, version int, comment, actor string, now time.Time) (OrderChangeRequest, error)
	WithdrawChangeRequest(ctx context.Context, id string, version int, comment, actor string, now time.Time) (OrderChangeRequest, error)
	RecordLineProduction(ctx context.Context, in LineProduction) error
	RecordIntakeError(ctx context.Context, in NewIntakeError, now time.Time) (IntakeError, error)
	ListIntakeErrors(ctx context.Context, f IntakeErrorFilter) ([]IntakeError, error)
	GetIntakeError(ctx context.Context, id string) (IntakeError, error)
	ResolveIntakeError(ctx context.Context, id string, version int, note, actor string, now time.Time) (IntakeError, error)
	GetIntakeReplay(ctx context.Context, id string) (IntakeReplay, error)
	ListIntakeReplays(ctx context.Context, f IntakeErrorFilter) ([]IntakeReplay, int, error)
	AutoCloseOrders(ctx context.Context, f AutoCloseFilter, actor string, now time.Time) (AutoCloseResult, error)
	RecordReplayFailure(ctx context.Context, id, code, message string, now time.Time) (IntakeError, error)
	ResolveReplayed(ctx context.Context, id, orderID, orderNumber, actor string, now time.Time) (IntakeError, error)
	GetParameters(ctx context.Context, legalEntityID string) (SalesParameters, error)
	PutParameters(ctx context.Context, in SalesParameters, version int, actor string) (SalesParameters, error)
	CreateReturnOrder(ctx context.Context, in NewReturnOrder) (ReturnOrder, bool, error)
	GetReturnOrder(ctx context.Context, id string) (ReturnOrder, error)
	ListReturnOrders(ctx context.Context, f ReturnOrderFilter) ([]ReturnOrder, error)
	AuthorizeReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error)
	ReceiveReturnOrder(ctx context.Context, id string, version int, lines []ReceiveLine, actor string, now time.Time) (ReturnOrder, error)
	InspectReturnOrder(ctx context.Context, id string, version int, lines []InspectLine, actor string, now time.Time) (ReturnOrder, error)
	DispositionReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error)
	CreditReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error)
	RejectReturnOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pool whose role is subject to RLS.
type Postgres struct {
	pool      *pgxpool.Pool
	crypt     *fieldcrypt.Keys
	inventory InventoryReceiver
	ar        ARCreditor
	invoices  InvoiceReader
	costs     CostReader
}

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func problem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

func versionConflict() error {
	return problem(http.StatusPreconditionFailed, CodeVersionConflict, "the resource was changed by another user; reload and reapply")
}

// CreateOrder stores a new DRAFT order. A repeat of the same Idempotency-Key or
// (channel, external_ref) with an identical body returns the stored order with
// created=false; a different body is refused (BR-27.10).
func (s *Postgres) CreateOrder(ctx context.Context, in NewOrder) (Order, bool, error) {
	p, _ := tenant.FromContext(ctx)
	if err := s.sealOneTime(p.TenantID, &in); err != nil {
		return Order{}, false, err
	}
	var out Order
	created := false
	err := s.orderTx(ctx, p.TenantID, &out, func(tx pgx.Tx) error {
		params, err := loadParameters(ctx, tx, p.TenantID, in.LegalEntityID)
		if err != nil {
			return err
		}
		existing, found, err := findIntake(ctx, tx, p.TenantID, in, params.IntakeDedupWindowDays)
		if err != nil {
			return err
		}
		if found {
			out = existing
			out.Warnings = supplementaryWarnings(out)
			return nil
		}
		if in.OrderType == "" {
			in.OrderType = params.DefaultOrderType
		}
		out, err = createOrderTx(ctx, tx, p.TenantID, in)
		created = err == nil
		if err != nil {
			return err
		}
		return checkAtEntryTx(ctx, tx, p.TenantID, &out)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Order{}, false, ConcurrentIntake{httpx.NewProblem(http.StatusConflict, CodeDuplicateIntake, "a concurrent request stored this order first; retry to receive it")}
	}
	if err != nil {
		return Order{}, false, err
	}
	return out, created, nil
}

// checkAtEntryTx runs the credit rules on a new order when the legal entity's
// credit_check_points include entry (FR-27.7). The check is advisory: the
// evaluation is stored on the order and written as creditcheck.evaluated, but
// no hold is placed before confirmation. JOURNAL orders reserve and invoice
// nothing against the customer (BR-27.7), so they are not checked.
func checkAtEntryTx(ctx context.Context, tx pgx.Tx, tenantID string, o *Order) error {
	if o.OrderType == domain.OrderTypeJournal {
		return nil
	}
	checked, err := checksAtTx(ctx, tx, tenantID, o.LegalEntityID, domain.CheckPointEntry)
	if err != nil || !checked {
		return err
	}
	if _, err := evaluateCreditTx(ctx, tx, tenantID, *o, domain.CheckPointEntry, time.Now().UTC()); err != nil {
		return err
	}
	*o, err = loadOrder(ctx, tx, tenantID, o.ID, false)
	o.Warnings = supplementaryWarnings(*o)
	return err
}

// createOrderTx checks the product listings (BR-27.13) and inserts a DRAFT
// order with its lines in the caller's transaction.
func createOrderTx(ctx context.Context, tx pgx.Tx, tenantID string, in NewOrder) (Order, error) {
	overrides, err := checkListings(ctx, tx, tenantID, in)
	if err != nil {
		return Order{}, err
	}
	var oneTimeCountry *string
	if in.OneTime != nil {
		if in.oneTimeSealed == nil {
			return Order{}, errors.New("one-time party is not sealed")
		}
		oneTimeCountry = &in.OneTime.Country
	}
	overrideJSON, err := json.Marshal(overrides)
	if err != nil {
		return Order{}, err
	}
	supplementary, err := mandatorySupplementaryTx(ctx, tx, tenantID, in)
	if err != nil {
		return Order{}, err
	}
	seq, err := nextOrderSequence(ctx, tx, tenantID, in.LegalEntityID)
	if err != nil {
		return Order{}, err
	}
	id := ids.New()
	net := decimal.Zero
	for _, l := range in.Lines {
		net = net.Add(l.LineNet)
	}
	for _, l := range supplementary {
		net = net.Add(l.LineNet)
	}
	if in.OneTime != nil {
		if err := checkOneTimeLimitTx(ctx, tx, tenantID, in.LegalEntityID, net, in.Currency); err != nil {
			return Order{}, err
		}
	}
	if _, err := tx.Exec(ctx, `insert into sales_order (tenant_id, id, legal_entity_id, order_number, customer_id, invoice_account_id, order_type, status, currency,
			requested_ship_date, net_total, channel, external_ref, idempotency_key, request_hash, listing_overrides, customer_group,
			one_time_party_sealed, one_time_country)
		values ($1, $2, $3, $4, $5, $6, $7, 'DRAFT', $8, $9::date, $10::numeric, $11, $12, $13, $14, $15::jsonb, $16, $17, $18)`,
		tenantID, id, in.LegalEntityID, domain.FormatOrderNumber(seq), in.CustomerID, in.InvoiceAccountID, in.OrderType, in.Currency,
		in.RequestedShipDate, net.String(), in.Channel, in.ExternalRef, in.IdempotencyKey, in.RequestHash, string(overrideJSON), in.CustomerGroup,
		in.oneTimeSealed, oneTimeCountry); err != nil {
		return Order{}, err
	}
	lineIDs := make(map[int]string, len(in.Lines))
	for _, l := range in.Lines {
		lineIDs[l.LineNo] = ids.New()
		if _, err := tx.Exec(ctx, `insert into sales_order_line (tenant_id, id, order_id, line_no, product_id, quantity, unit, site_id, warehouse_id,
				unit_price, disc_pct, line_net, requested_ship_date, line_status, configuration_id, delivery_date_control)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10::numeric, $11::numeric, $12::numeric, $13::date, 'OPEN', $14, $15)`,
			tenantID, lineIDs[l.LineNo], id, l.LineNo, l.ProductID, l.Quantity.String(), l.Unit, l.SiteID, l.WarehouseID,
			l.UnitPrice.String(), l.DiscPct.String(), l.LineNet.String(), l.RequestedShipDate, l.ConfigurationID, l.DeliveryDateControl); err != nil {
			return Order{}, err
		}
	}
	for _, l := range supplementary {
		if _, err := tx.Exec(ctx, `insert into sales_order_line (tenant_id, id, order_id, line_no, product_id, quantity, unit, site_id, warehouse_id,
				unit_price, disc_pct, line_net, requested_ship_date, line_status,
				supplementary_of, supplementary_rule_id, supplementary_base_qty, supplementary_qty_per_base)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9, $10::numeric, 0, $11::numeric, $12::date, 'OPEN', $13, $14, $15::numeric, $16::numeric)`,
			tenantID, ids.New(), id, l.LineNo, l.ProductID, l.Quantity.String(), l.Unit, l.SiteID, l.WarehouseID,
			l.UnitPrice.String(), l.LineNet.String(), l.RequestedShipDate, lineIDs[*l.ParentLineNo], l.SupplementaryRuleID,
			l.suppBase.String(), l.suppPerBase.String()); err != nil {
			return Order{}, err
		}
	}
	o, err := loadOrder(ctx, tx, tenantID, id, false)
	o.Warnings = supplementaryWarnings(o)
	return o, err
}

// findIntake finds the order a repeated intake refers to: the same
// Idempotency-Key (never expires), or the same (channel, externalRef) of the
// same legal entity created less than the legal entity's
// intake_dedup_window_days ago (BR-27.10). The reference is matched within the
// legal entity, not across the tenant, so the result does not depend on which
// legal entities row-level security lets the caller see. Intakes of one
// reference are serialised by an advisory lock held to the end of the
// transaction (read committed, so the lookup after the lock sees an order the
// previous holder committed), so two concurrent first intakes cannot both
// create an order; after the window the reference opens a new order.
func findIntake(ctx context.Context, tx pgx.Tx, tenantID string, in NewOrder, windowDays int) (Order, bool, error) {
	var id, hash string
	var byKey bool
	if in.ExternalRef != nil {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('sales.intake:' || $1 || ':' || $2 || ':' || $3 || ':' || $4, 0))`,
			tenantID, in.LegalEntityID, in.Channel, *in.ExternalRef); err != nil {
			return Order{}, false, err
		}
	}
	err := tx.QueryRow(ctx, `select id::text, request_hash, coalesce(idempotency_key = $2::text, false)
		from sales_order
		where tenant_id = $1 and (($2::text is not null and idempotency_key = $2::text)
			or ($3::text is not null and legal_entity_id = $6::uuid and channel = $4 and external_ref = $3::text
				and created_at > now() - make_interval(days => $5::int)))
		order by (idempotency_key = $2::text) desc nulls last, created_at desc
		limit 1`, tenantID, in.IdempotencyKey, in.ExternalRef, in.Channel, windowDays, in.LegalEntityID).Scan(&id, &hash, &byKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, err
	}
	if hash == in.RequestHash || (in.sameRequest != nil && in.sameRequest(hash)) {
		order, err := loadOrder(ctx, tx, tenantID, id, false)
		return order, true, err
	}
	if byKey {
		return Order{}, false, problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return Order{}, false, problem(http.StatusConflict, CodeDuplicateIntake, "channel and externalRef were already used with a different payload")
}

func nextOrderSequence(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (int64, error) {
	return nextSequence(ctx, tx, tenantID, legalEntityID, "SO")
}

// nextSequence draws the next number of a per-legal-entity sequence (SO-, SA-, SQ-).
func nextSequence(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, sequenceID string) (int64, error) {
	var seq int64
	err := tx.QueryRow(ctx, `insert into sales_number_sequence (tenant_id, legal_entity_id, sequence_id, next_value) values ($1, $2, $3, 2)
		on conflict (tenant_id, legal_entity_id, sequence_id) do update set next_value = sales_number_sequence.next_value + 1
		returning next_value - 1`, tenantID, legalEntityID, sequenceID).Scan(&seq)
	return seq, err
}

// GetOrder returns one order with its lines and holds.
func (s *Postgres) GetOrder(ctx context.Context, id string) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var o Order
	err := s.orderTx(ctx, p.TenantID, &o, func(tx pgx.Tx) error {
		var err error
		o, err = loadOrder(ctx, tx, p.TenantID, id, false)
		return err
	})
	return o, err
}

func orderNotFound() error { return problem(http.StatusNotFound, CodeOrderNotFound, "order not found") }

func loadOrder(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Order, error) {
	q := `select id::text, legal_entity_id::text, order_number, customer_id::text, invoice_account_id::text, order_type, status, currency::text,
			requested_ship_date::text, channel, external_ref, customer_group, net_total::text, version, request_hash, credit_check_result::text,
			listing_overrides::text, one_time_party_sealed, one_time_country::text, created_at, updated_at,
			confirmed_transport_days, confirmed_receiving_days
		from sales_order where tenant_id = $1 and id = $2`
	if lock {
		q += " for update"
	}
	var o Order
	var net, overrides string
	var creditCheck, oneTimeCountry *string
	var oneTimeSealed []byte
	var termsTransport *int
	var termsDays []int16
	err := tx.QueryRow(ctx, q, tenantID, id).Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.CustomerID, &o.InvoiceAccountID, &o.OrderType,
		&o.Status, &o.Currency, &o.RequestedShipDate, &o.Channel, &o.ExternalRef, &o.CustomerGroup, &net, &o.Version, &o.RequestHash, &creditCheck, &overrides,
		&oneTimeSealed, &oneTimeCountry, &o.CreatedAt, &o.UpdatedAt, &termsTransport, &termsDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, orderNotFound()
	}
	if err != nil {
		return Order{}, err
	}
	o.NetTotal = decimal.RequireFromString(net)
	if creditCheck != nil {
		o.CreditCheck = json.RawMessage(*creditCheck)
	}
	o.ListingOverrides = []ListingOverride{}
	if err := json.Unmarshal([]byte(overrides), &o.ListingOverrides); err != nil {
		return Order{}, err
	}
	if oneTimeSealed != nil && oneTimeCountry != nil {
		o.oneTime = &sealedParty{envelope: oneTimeSealed, country: *oneTimeCountry}
	}
	if termsTransport != nil {
		o.ConfirmedDeliveryTerms = &domain.DeliveryTerms{TransportDays: *termsTransport, ReceivingDays: ints(termsDays)}
	}
	lineRows, err := tx.Query(ctx, `select id::text, line_no, product_id::text, quantity::text, unit, site_id::text, warehouse_id::text,
			unit_price::text, disc_pct::text, line_net::text, requested_ship_date::text, confirmed_ship_date::text, line_status,
			supplementary_of::text, supplementary_rule_id::text, coalesce(supplementary_base_qty, 0)::text, coalesce(supplementary_qty_per_base, 0)::text,
			configuration_id::text, delivery_date_control, delivered_qty::text, invoiced_qty::text,
			(select p.production_order_id::text from order_line_production p where p.tenant_id = l.tenant_id and p.line_id = l.id)
		from sales_order_line l where l.tenant_id = $1 and l.order_id = $2 order by l.line_no`, tenantID, id)
	if err != nil {
		return Order{}, err
	}
	defer lineRows.Close()
	o.Lines = []Line{}
	for lineRows.Next() {
		var l Line
		var qty, price, disc, net, base, perBase, delivered, invoiced string
		if err := lineRows.Scan(&l.ID, &l.LineNo, &l.ProductID, &qty, &l.Unit, &l.SiteID, &l.WarehouseID, &price, &disc, &net,
			&l.RequestedShipDate, &l.ConfirmedShipDate, &l.LineStatus, &l.SupplementaryOf, &l.SupplementaryRuleID, &base, &perBase, &l.ConfigurationID, &l.DeliveryDateControl, &delivered, &invoiced, &l.ProductionOrderID); err != nil {
			return Order{}, err
		}
		l.Quantity, l.UnitPrice = decimal.RequireFromString(qty), decimal.RequireFromString(price)
		l.DeliveredQty, l.InvoicedQty = decimal.RequireFromString(delivered), decimal.RequireFromString(invoiced)
		l.DiscPct, l.LineNet = decimal.RequireFromString(disc), decimal.RequireFromString(net)
		l.suppBase, l.suppPerBase = decimal.RequireFromString(base), decimal.RequireFromString(perBase)
		o.Lines = append(o.Lines, l)
	}
	if err := lineRows.Err(); err != nil {
		return Order{}, err
	}
	linkSupplementaryParents(o.Lines)
	holdRows, err := tx.Query(ctx, `select id::text, hold_type, reason_code, status, order_amount::text, applied_by, released_by, released_until, applied_at,
			rejected_by, rejected_at, rejection_comment
		from order_hold where tenant_id = $1 and order_id = $2 order by applied_at, id`, tenantID, id)
	if err != nil {
		return Order{}, err
	}
	defer holdRows.Close()
	o.Holds = []Hold{}
	for holdRows.Next() {
		var h Hold
		var amount string
		if err := holdRows.Scan(&h.ID, &h.HoldType, &h.ReasonCode, &h.Status, &amount, &h.AppliedBy, &h.ReleasedBy, &h.ReleasedUntil, &h.AppliedAt,
			&h.RejectedBy, &h.RejectedAt, &h.RejectionComment); err != nil {
			return Order{}, err
		}
		h.OrderAmount = decimal.RequireFromString(amount)
		o.Holds = append(o.Holds, h)
	}
	if err := holdRows.Err(); err != nil {
		return Order{}, err
	}
	addReceiptDates(&o)
	return o, nil
}

func setOrderState(ctx context.Context, tx pgx.Tx, tenantID, id, status string) error {
	_, err := tx.Exec(ctx, `update sales_order set status = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`, tenantID, id, status)
	return err
}

// coveredByRelease reports whether a still-valid CREDIT release covers the
// order's current amount, so the credit check need not hold it again (BR-27.4).
func coveredByRelease(o Order, now time.Time) bool {
	for _, h := range o.Holds {
		if h.HoldType == domain.HoldCredit && h.Status == domain.HoldReleased && h.ReleasedUntil != nil &&
			now.Before(*h.ReleasedUntil) && domain.ReleaseCovers(h.OrderAmount, o.NetTotal) {
			return true
		}
	}
	return false
}

func firstFailedRule(ev domain.CreditEvaluation) string {
	for _, r := range ev.Rules {
		if r.Result == domain.RuleFail {
			return r.ID
		}
	}
	return "CREDIT"
}

// ConfirmOrder locks the order, refuses if a blocking hold is active, evaluates
// credit and either places a CREDIT hold (order stays open) or confirms it.
func (s *Postgres) ConfirmOrder(ctx context.Context, id string, version int, actor string, now time.Time) (ConfirmResult, error) {
	p, _ := tenant.FromContext(ctx)
	var res ConfirmResult
	err := s.orderTx(ctx, p.TenantID, &res.Order, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if !domain.CanConfirm(o.Status) {
			return problem(http.StatusUnprocessableEntity, CodeNotConfirmable, fmt.Sprintf("order is %s and cannot be confirmed", o.Status))
		}
		for _, h := range o.Holds {
			if h.Status == domain.HoldActive && domain.BlocksConfirm(h.HoldType) {
				code := CodeHoldBlocks
				if h.HoldType == domain.HoldCredit {
					code = CodeCreditHold
				}
				return problem(http.StatusUnprocessableEntity, code, "an active "+h.HoldType+" hold blocks confirmation")
			}
		}
		// Checked before credit, so an order over the limit is refused rather
		// than held; confirmOrderTx checks again for the release path.
		if o.isOneTime() {
			if err := checkOneTimeLimitTx(ctx, tx, p.TenantID, o.LegalEntityID, o.NetTotal, o.Currency); err != nil {
				return err
			}
		}
		// A JOURNAL order bills and reserves nothing against the customer
		// (BR-27.7), so it takes no credit check.
		checked, err := checksAtTx(ctx, tx, p.TenantID, o.LegalEntityID, domain.CheckPointConfirm)
		if err != nil {
			return err
		}
		checked = checked && o.OrderType != domain.OrderTypeJournal
		eval := domain.CreditEvaluation{Outcome: domain.RulePass}
		if checked {
			if eval, err = evaluateCreditTx(ctx, tx, p.TenantID, o, domain.CheckPointConfirm, now); err != nil {
				return err
			}
		}
		if eval.Outcome == domain.RuleFail && !coveredByRelease(o, now) {
			if err := applyCreditHoldTx(ctx, tx, p.TenantID, o, eval, actor, o.Version+1); err != nil {
				return err
			}
			if err := setOrderState(ctx, tx, p.TenantID, o.ID, o.Status); err != nil {
				return err
			}
			res = ConfirmResult{Evaluation: eval}
			res.Order, err = loadOrder(ctx, tx, p.TenantID, o.ID, false)
			return err
		}
		confirmed, err := confirmOrderTx(ctx, tx, p.TenantID, o, now)
		if err != nil {
			return err
		}
		res = ConfirmResult{Order: confirmed, Evaluation: eval, Confirmed: true}
		return nil
	})
	return res, err
}

// checksAtTx reports whether the legal entity's credit_check_points include
// point (FR-27.7).
func checksAtTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, point string) (bool, error) {
	params, err := loadParameters(ctx, tx, tenantID, legalEntityID)
	if err != nil {
		return false, err
	}
	return params.ChecksAt(point), nil
}

// undeliveredNetSQL is the value of what a line (alias l) has not delivered:
// its net less the net of the delivered quantity at the same price and
// discount. It is read only for lines with delivered_qty < quantity. SQL round
// is half away from zero where domain.LineNet rounds half to even, so the
// figure can differ from a Go recomputation by at most 0.0001 on an exact tie.
// It assumes line_net is quantity x price less the discount; a line priced
// another way (such as by catch weight) needs its own delivered net here.
const undeliveredNetSQL = `(l.line_net - round(l.delivered_qty * l.unit_price * (100 - l.disc_pct) / 100, 4))`

// openOrdersTx is the customer's exposure from orders in this service: the net
// total of the customer's other CONFIRMED ORDER and SUBSCRIPTION orders, and
// only the undelivered part of a PARTIALLY_SHIPPED one, in the credit profile
// currency (BR-27.3 open orders; what was delivered is open AR, owned by the
// receivables wave).
// Orders in another currency cannot be converted without an FX rate (w12):
// they are returned per currency so the evaluation and the profile flag them
// instead of understating the exposure silently. The sum reads the partial
// index sales_order_open_exposure (migration 0022).
func openOrdersTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID, currency, exceptOrderID string) (decimal.Decimal, []domain.CurrencyAmount, error) {
	rows, err := tx.Query(ctx, `select currency::text, sum(amount)::text from (
		select currency, net_total as amount from sales_order
		where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3
		  and status = 'CONFIRMED' and order_type in ('ORDER', 'SUBSCRIPTION')
		  and ($4::uuid is null or id <> $4::uuid)
		union all
		select o.currency, `+undeliveredNetSQL+` as amount
		from sales_order o join sales_order_line l on l.tenant_id = o.tenant_id and l.order_id = o.id
		where o.tenant_id = $1 and o.legal_entity_id = $2 and o.customer_id = $3
		  and o.status = 'PARTIALLY_SHIPPED' and o.order_type in ('ORDER', 'SUBSCRIPTION')
		  and ($4::uuid is null or o.id <> $4::uuid)
		  and l.line_status <> 'CANCELLED' and l.delivered_qty < l.quantity
	) open_amounts group by currency order by currency`,
		tenantID, legalEntityID, customerID, nullIfEmpty(exceptOrderID))
	if err != nil {
		return decimal.Zero, nil, err
	}
	defer rows.Close()
	total := decimal.Zero
	var other []domain.CurrencyAmount
	for rows.Next() {
		var cur, sum string
		if err := rows.Scan(&cur, &sum); err != nil {
			return decimal.Zero, nil, err
		}
		amount, err := decimal.NewFromString(sum)
		if err != nil {
			return decimal.Zero, nil, err
		}
		if cur == currency {
			total = amount
		} else {
			other = append(other, domain.CurrencyAmount{Currency: cur, Amount: amount})
		}
	}
	return total, other, rows.Err()
}

// deliveredNotInvoicedSQL is the value of what a line (alias l) has delivered
// and not invoiced: the delivered less the invoiced quantity at the line's
// price and discount. It is read only for lines with delivered_qty >
// invoiced_qty. Like undeliveredNetSQL it assumes a quantity x price line.
const deliveredNotInvoicedSQL = `round((l.delivered_qty - l.invoiced_qty) * l.unit_price * (100 - l.disc_pct) / 100, 4)`

// deliveredNotInvoicedTx is the customer's delivered, not yet invoiced goods
// (BR-27.3 deliveredNotInvoiced) from the lines of the customer's other ORDER
// orders that are PARTIALLY_SHIPPED or SHIPPED, in the profile currency; other
// currencies are returned per currency, as for open orders. SUBSCRIPTION
// orders are invoiced per period by billing, not per line quantity, and JOURNAL
// orders post no slip or invoice, so neither counts. The order being checked is
// left out: its whole net total is the amount under check.
func deliveredNotInvoicedTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID, currency, exceptOrderID string) (decimal.Decimal, []domain.CurrencyAmount, error) {
	rows, err := tx.Query(ctx, `select o.currency::text, sum(`+deliveredNotInvoicedSQL+`)::text
		from sales_order o join sales_order_line l on l.tenant_id = o.tenant_id and l.order_id = o.id
		where o.tenant_id = $1 and o.legal_entity_id = $2 and o.customer_id = $3
		  and o.status in ('PARTIALLY_SHIPPED', 'SHIPPED') and o.order_type = 'ORDER'
		  and ($4::uuid is null or o.id <> $4::uuid)
		  and l.delivered_qty > l.invoiced_qty
		group by o.currency order by o.currency`,
		tenantID, legalEntityID, customerID, nullIfEmpty(exceptOrderID))
	if err != nil {
		return decimal.Zero, nil, err
	}
	defer rows.Close()
	total := decimal.Zero
	var other []domain.CurrencyAmount
	for rows.Next() {
		var cur, sum string
		if err := rows.Scan(&cur, &sum); err != nil {
			return decimal.Zero, nil, err
		}
		amount, err := decimal.NewFromString(sum)
		if err != nil {
			return decimal.Zero, nil, err
		}
		if cur == currency {
			total = amount
		} else {
			other = append(other, domain.CurrencyAmount{Currency: cur, Amount: amount})
		}
	}
	return total, other, rows.Err()
}

// withOpenOrders replaces the stored open-orders and delivered-not-invoiced
// figures of c with the ones computed from the orders (a value sent with the
// profile is never used).
func withOpenOrders(ctx context.Context, tx pgx.Tx, tenantID string, c *CreditProfile, exceptOrderID string) error {
	var err error
	c.Exposure.OpenOrders, c.Exposure.OpenOrdersOtherCurrencies, err = openOrdersTx(ctx, tx, tenantID, c.LegalEntityID, c.CustomerID, c.Currency, exceptOrderID)
	if err != nil {
		return err
	}
	c.Exposure.DeliveredNotInvoiced, c.Exposure.DeliveredNotInvoicedOtherCurrencies, err = deliveredNotInvoicedTx(ctx, tx, tenantID, c.LegalEntityID, c.CustomerID, c.Currency, exceptOrderID)
	return err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// evaluateCreditTx runs the BR-27.3 rules for o at its current net total,
// stores the evaluation on the order and writes creditcheck.evaluated, tagged
// with the check point, in the caller's transaction.
func evaluateCreditTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, point string, now time.Time) (domain.CreditEvaluation, error) {
	// The profile row is locked for the rest of the transaction, so two orders
	// of one customer are checked one after the other: the second sees the
	// first's confirmation in its open orders instead of both passing on an
	// exposure that misses the other.
	profile, found, err := loadProfileForUpdate(ctx, tx, tenantID, o.LegalEntityID, o.CustomerID)
	if err != nil {
		return domain.CreditEvaluation{}, err
	}
	input := domain.CreditInput{Order: o.NetTotal, OrderCurrency: o.Currency, AsOf: now}
	if found {
		input.Profile = &profile.CreditProfile
		if err := withOpenOrders(ctx, tx, tenantID, &profile, o.ID); err != nil {
			return domain.CreditEvaluation{}, err
		}
		input.Exposure = profile.Exposure
	}
	eval := domain.EvaluateCredit(input)
	if err := enqueue(ctx, tx, "order", o.ID, salesevents.CreditCheckEvaluated, tenantID, salesevents.CreditCheckData{
		CustomerID: o.CustomerID, OrderID: o.ID, Rules: eval.Rules, Outcome: eval.Outcome, Point: point}); err != nil {
		return domain.CreditEvaluation{}, err
	}
	evalJSON, err := json.Marshal(eval)
	if err != nil {
		return domain.CreditEvaluation{}, err
	}
	_, err = tx.Exec(ctx, `update sales_order set credit_check_result = $3::jsonb where tenant_id = $1 and id = $2`, tenantID, o.ID, string(evalJSON))
	return eval, err
}

// applyCreditHoldTx places a CREDIT hold for o's current net total and writes
// orderhold.applied carrying version, the order version the caller moves to.
func applyCreditHoldTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, eval domain.CreditEvaluation, actor string, version int) error {
	holdID := ids.New()
	reason := firstFailedRule(eval)
	if _, err := tx.Exec(ctx, `insert into order_hold (tenant_id, id, legal_entity_id, order_id, hold_type, reason_code, order_amount, applied_by)
		values ($1, $2, $3, $4, 'CREDIT', $5, $6::numeric, $7)`,
		tenantID, holdID, o.LegalEntityID, o.ID, reason, o.NetTotal.String(), actor); err != nil {
		return err
	}
	return enqueue(ctx, tx, "order", o.ID, salesevents.OrderHoldApplied, tenantID, salesevents.OrderHoldData{
		OrderID: o.ID, HoldID: holdID, HoldType: domain.HoldCredit, ReasonCode: reason, Actor: actor, Version: version})
}

// confirmOrderTx confirms every line at the ship date its delivery date
// control gives (FR-27.3, BR-27.2: the requested date, today when that is
// past, or the sales lead time when later; ATP and CTP wait for their
// sources) unless allocation quota moves it to a later period (FR-27.16),
// moves the order to CONFIRMED and writes order.confirmed in the same
// transaction.
func confirmOrderTx(ctx context.Context, tx pgx.Tx, tenantID string, o Order, now time.Time) (Order, error) {
	if o.isOneTime() {
		if err := checkOneTimeLimitTx(ctx, tx, tenantID, o.LegalEntityID, o.NetTotal, o.Currency); err != nil {
			return Order{}, err
		}
	}
	rule, err := loadDateRule(ctx, tx, tenantID, o.LegalEntityID, now)
	if err != nil {
		return Order{}, err
	}
	dateNotices, err := confirmDatesTx(ctx, tx, tenantID, o, rule)
	if err != nil {
		return Order{}, err
	}
	if err := copyDeliveryTermsTx(ctx, tx, tenantID, o); err != nil {
		return Order{}, err
	}
	alloc, err := allocateOrderTx(ctx, tx, tenantID, o, nil)
	if err != nil {
		return Order{}, err
	}
	if err := setOrderState(ctx, tx, tenantID, o.ID, domain.OrderConfirmed); err != nil {
		return Order{}, err
	}
	if err := syncScheduleTx(ctx, tx, tenantID, o.ID); err != nil {
		return Order{}, err
	}
	confirmed, err := loadOrder(ctx, tx, tenantID, o.ID, false)
	if err != nil {
		return Order{}, err
	}
	confirmed.Warnings = append(dateNotices, alloc.Notices...)
	sched, err := activeSchedule(ctx, tx, tenantID, o.ID)
	if err != nil {
		return Order{}, err
	}
	lines := make([]salesevents.ConfirmedLine, 0, len(confirmed.Lines))
	for _, l := range confirmed.Lines {
		if l.ConfirmedShipDate == nil {
			continue
		}
		atp := decimal.Zero
		for _, sl := range sched[l.ID] {
			if sl.Status == domain.SchedConfirmed {
				atp = atp.Add(sl.Quantity)
			}
		}
		line := salesevents.ConfirmedLine{LineID: l.ID, ProductID: l.ProductID, Quantity: l.Quantity.String(), AtpQty: atp.String(),
			SiteID: l.SiteID, WarehouseID: l.WarehouseID, ConfirmedShipDate: *l.ConfirmedShipDate, ConfigurationID: l.ConfigurationID}
		if l.ConfirmedReceiptDate != nil {
			line.ConfirmedReceiptDate = *l.ConfirmedReceiptDate
		}
		lines = append(lines, line)
	}
	data := salesevents.OrderConfirmedData{
		OrderID: o.ID, CustomerID: o.CustomerID, OrderType: o.OrderType, Currency: o.Currency, Lines: lines, NetTotal: o.NetTotal.String(), Version: confirmed.Version}
	if confirmed.isOneTime() {
		// The buyer's name, address and tax id stay in sales (sealed); the
		// event marks the order and gives the country only.
		data.OneTime = &salesevents.OneTimeRef{Country: confirmed.oneTime.country}
	}
	err = enqueue(ctx, tx, "order", o.ID, salesevents.OrderConfirmed, tenantID, data)
	return confirmed, err
}

// ReleaseHold releases an active hold. A CREDIT release is valid for the
// legal entity's credit_release_validity_days. The user who applied a hold may not release it (SoD).
// Releasing the CREDIT hold that a confirm placed confirms the order in the
// same transaction when no other hold blocks confirmation (AC-27.3).
func (s *Postgres) ReleaseHold(ctx context.Context, orderID, holdID string, version int, actor string, now time.Time) (ConfirmResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out ConfirmResult
	err := s.orderTx(ctx, p.TenantID, &out.Order, func(tx pgx.Tx) error {
		o, err := loadOrder(ctx, tx, p.TenantID, orderID, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		var hold *Hold
		for i := range o.Holds {
			if o.Holds[i].ID == holdID {
				hold = &o.Holds[i]
			}
		}
		if hold == nil {
			return problem(http.StatusNotFound, CodeHoldNotFound, "hold not found")
		}
		if hold.Status != domain.HoldActive {
			return problem(http.StatusUnprocessableEntity, CodeHoldNotActive, "hold is not active")
		}
		if hold.AppliedBy == actor {
			return problem(http.StatusForbidden, "AUTH-0003", "the user who applied a hold may not release it")
		}
		params, err := loadParameters(ctx, tx, p.TenantID, o.LegalEntityID)
		if err != nil {
			return err
		}
		until := domain.ReleaseValidUntil(now, params.CreditReleaseValidityDays)
		if _, err := tx.Exec(ctx, `update order_hold set status = 'RELEASED', released_by = $3, released_at = $4, released_until = $5
			where tenant_id = $1 and id = $2`, p.TenantID, holdID, actor, now, until); err != nil {
			return err
		}
		if err := setOrderState(ctx, tx, p.TenantID, o.ID, o.Status); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "order", o.ID, salesevents.OrderHoldReleased, p.TenantID, salesevents.OrderHoldData{
			OrderID: o.ID, HoldID: holdID, HoldType: hold.HoldType, ReasonCode: hold.ReasonCode, Actor: actor, Version: o.Version + 1}); err != nil {
			return err
		}
		if hold.HoldType == domain.HoldCredit && params.ChecksAt(domain.CheckPointRelease) {
			// The release stands whatever the rules say now: the check records
			// the position at the moment of release for the credit manager.
			if _, err := evaluateCreditTx(ctx, tx, p.TenantID, o, domain.CheckPointRelease, now); err != nil {
				return err
			}
		}
		released, err := loadOrder(ctx, tx, p.TenantID, o.ID, false)
		if err != nil {
			return err
		}
		if hold.HoldType != domain.HoldCredit || !domain.CanConfirm(released.Status) || blockedForConfirm(released) {
			out = ConfirmResult{Order: released}
			return nil
		}
		// The credit decision stands even when allocation quota cannot cover the
		// order: the confirmation runs under a savepoint, and an SLS-0013 refusal
		// rolls back only the confirmation, so the hold is released and the order
		// stays unconfirmed with the refusal as a warning (FR-27.16). A one-time
		// order over its limit (SLS-0016, the limit lowered since the hold was
		// placed) is treated the same way.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		confirmed, err := confirmOrderTx(ctx, sp, p.TenantID, released, now)
		var refused *httpx.Problem
		if errors.As(err, &refused) && (refused.Code == CodeAllocationExhausted || refused.Code == CodeOneTimeLimit) {
			if err := sp.Rollback(ctx); err != nil {
				return err
			}
			released.Warnings = []Notice{{Code: refused.Code, Message: refused.Detail}}
			out = ConfirmResult{Order: released}
			return nil
		}
		if err != nil {
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		out = ConfirmResult{Order: confirmed, Confirmed: true}
		return nil
	})
	return out, err
}

// blockedForConfirm reports whether any ACTIVE hold blocks confirmation.
func blockedForConfirm(o Order) bool {
	for _, h := range o.Holds {
		if h.Status == domain.HoldActive && domain.BlocksConfirm(h.HoldType) {
			return true
		}
	}
	return false
}

func parseDay(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := time.Parse(dayLayout, *s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func dayString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(dayLayout)
	return &s
}

const profileColumns = `legal_entity_id::text, customer_id::text, credit_limit::text, currency::text, limit_type, tolerance_pct::text,
	tolerance_amount::text, oldest_item_days_max, max_dunning_level, review_date::text, customer_since::text, mandatory_check,
	exposure::text, version, updated_at`

func scanProfile(row pgx.Row) (CreditProfile, error) {
	var c CreditProfile
	var limit, tolPct, tolAmt, exposure string
	var review, since *string
	if err := row.Scan(&c.LegalEntityID, &c.CustomerID, &limit, &c.Currency, &c.LimitType, &tolPct, &tolAmt, &c.OldestItemDaysMax,
		&c.MaxDunningLevel, &review, &since, &c.MandatoryCheck, &exposure, &c.Version, &c.UpdatedAt); err != nil {
		return CreditProfile{}, err
	}
	c.Limit = decimal.RequireFromString(limit)
	c.TolerancePct = decimal.RequireFromString(tolPct)
	c.ToleranceAmount = decimal.RequireFromString(tolAmt)
	var err error
	if c.ReviewDate, err = parseDay(review); err != nil {
		return CreditProfile{}, err
	}
	if c.CustomerSince, err = parseDay(since); err != nil {
		return CreditProfile{}, err
	}
	if err := json.Unmarshal([]byte(exposure), &c.Exposure); err != nil {
		return CreditProfile{}, err
	}
	return c, nil
}

func loadProfile(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID string) (CreditProfile, bool, error) {
	return queryProfile(ctx, tx, "", tenantID, legalEntityID, customerID)
}

// loadProfileForUpdate is loadProfile with the row locked until the
// transaction ends.
func loadProfileForUpdate(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, customerID string) (CreditProfile, bool, error) {
	return queryProfile(ctx, tx, " for update", tenantID, legalEntityID, customerID)
}

func queryProfile(ctx context.Context, tx pgx.Tx, lock, tenantID, legalEntityID, customerID string) (CreditProfile, bool, error) {
	c, err := scanProfile(tx.QueryRow(ctx, `select `+profileColumns+`
		from credit_profile where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3`+lock, tenantID, legalEntityID, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return CreditProfile{}, false, nil
	}
	if err != nil {
		return CreditProfile{}, false, err
	}
	return c, true, nil
}

// GetCreditProfile returns the profile of a customer in a legal entity.
func (s *Postgres) GetCreditProfile(ctx context.Context, legalEntityID, customerID string) (CreditProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var c CreditProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var found bool
		var err error
		c, found, err = loadProfile(ctx, tx, p.TenantID, legalEntityID, customerID)
		if err != nil {
			return err
		}
		if !found {
			return problem(http.StatusNotFound, CodeProfileNotFound, "credit profile not found")
		}
		return withOpenOrders(ctx, tx, p.TenantID, &c, "")
	})
	return c, err
}

// PutCreditProfile creates a profile when version is 0 and otherwise replaces
// it when version matches the stored version (optimistic concurrency).
func (s *Postgres) PutCreditProfile(ctx context.Context, in CreditProfile, version int) (CreditProfile, error) {
	p, _ := tenant.FromContext(ctx)
	// Open orders and delivered-not-invoiced goods are computed from the
	// orders on every read; a figure sent with the profile is not stored.
	in.Exposure.OpenOrders, in.Exposure.OpenOrdersOtherCurrencies = decimal.Zero, nil
	in.Exposure.DeliveredNotInvoiced, in.Exposure.DeliveredNotInvoicedOtherCurrencies = decimal.Zero, nil
	exposure, err := json.Marshal(in.Exposure)
	if err != nil {
		return CreditProfile{}, err
	}
	review, since := dayString(in.ReviewDate), dayString(in.CustomerSince)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var stored int
		if version == 0 {
			err := tx.QueryRow(ctx, `insert into credit_profile (tenant_id, legal_entity_id, customer_id, credit_limit, currency, limit_type, tolerance_pct,
					tolerance_amount, oldest_item_days_max, max_dunning_level, review_date, customer_since, mandatory_check, exposure)
				values ($1, $2, $3, $4::numeric, $5, $6, $7::numeric, $8::numeric, $9, $10, $11::date, $12::date, $13, $14::jsonb)
				on conflict do nothing returning version`,
				p.TenantID, in.LegalEntityID, in.CustomerID, in.Limit.String(), in.Currency, in.LimitType, in.TolerancePct.String(),
				in.ToleranceAmount.String(), in.OldestItemDaysMax, in.MaxDunningLevel, review, since, in.MandatoryCheck, string(exposure)).Scan(&stored)
			if errors.Is(err, pgx.ErrNoRows) {
				return versionConflict()
			}
			return err
		}
		err := tx.QueryRow(ctx, `update credit_profile set credit_limit = $4::numeric, currency = $5, limit_type = $6, tolerance_pct = $7::numeric,
				tolerance_amount = $8::numeric, oldest_item_days_max = $9, max_dunning_level = $10, review_date = $11::date, customer_since = $12::date,
				mandatory_check = $13, exposure = $14::jsonb, version = version + 1, updated_at = now()
			where tenant_id = $1 and legal_entity_id = $2 and customer_id = $3 and version = $15 returning version`,
			p.TenantID, in.LegalEntityID, in.CustomerID, in.Limit.String(), in.Currency, in.LimitType, in.TolerancePct.String(),
			in.ToleranceAmount.String(), in.OldestItemDaysMax, in.MaxDunningLevel, review, since, in.MandatoryCheck, string(exposure), version).Scan(&stored)
		if errors.Is(err, pgx.ErrNoRows) {
			return versionConflict()
		}
		return err
	})
	if err != nil {
		return CreditProfile{}, err
	}
	return s.GetCreditProfile(ctx, in.LegalEntityID, in.CustomerID)
}

// ListOrders returns one page of orders, newest first, inside the caller's
// legal-entity scope (row-level security).
func (s *Postgres) ListOrders(ctx context.Context, f OrderFilter) (OrderPage, error) {
	p, _ := tenant.FromContext(ctx)
	page := OrderPage{Items: []OrderSummary{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.legal_entity_id::text, o.order_number, o.customer_id::text, o.order_type, o.status,
				o.currency::text, o.requested_ship_date::text, o.channel, o.external_ref, o.net_total::text, o.version, o.created_at,
				(select count(*) from sales_order_line l where l.tenant_id = o.tenant_id and l.order_id = o.id),
				coalesce((select array_agg(h.hold_type order by h.applied_at) from order_hold h
					where h.tenant_id = o.tenant_id and h.order_id = o.id and h.status = 'ACTIVE'), '{}')
			from sales_order o
			where o.tenant_id = $1
				and ($2 = '' or o.status = $2)
				and ($3 = '' or o.customer_id::text = $3)
				and ($4 = '' or exists (select 1 from order_hold h where h.tenant_id = o.tenant_id and h.order_id = o.id
					and h.status = 'ACTIVE' and h.hold_type = $4))
				and ($5 = '' or (o.created_at, o.id) < (select c.created_at, c.id from sales_order c where c.tenant_id = $1 and c.id::text = $5))
			order by o.created_at desc, o.id desc
			limit $6`, p.TenantID, f.Status, f.CustomerID, f.HoldType, f.Before, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o OrderSummary
			var net string
			if err := rows.Scan(&o.ID, &o.LegalEntityID, &o.OrderNumber, &o.CustomerID, &o.OrderType, &o.Status, &o.Currency,
				&o.RequestedShipDate, &o.Channel, &o.ExternalRef, &net, &o.Version, &o.CreatedAt, &o.LineCount, &o.ActiveHolds); err != nil {
				return err
			}
			o.NetTotal = decimal.RequireFromString(net)
			page.Items = append(page.Items, o)
		}
		return rows.Err()
	})
	if err != nil {
		return OrderPage{}, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		page.NextCursor = page.Items[f.Limit-1].ID
	}
	return page, nil
}

// ListCreditProfiles returns the credit profiles of one legal entity, or of
// every legal entity in the caller's scope when legalEntityID is empty.
func (s *Postgres) ListCreditProfiles(ctx context.Context, legalEntityID string, limit int) ([]CreditProfile, error) {
	return s.listProfiles(ctx, legalEntityID, "", limit)
}

// ListCreditProfilesReviewDue returns the profiles whose review date is on or
// before dueBy (a YYYY-MM-DD day), oldest review date first: the list a credit
// manager works from before the CR-4 review-date rule holds orders (PRD 5.27.6).
func (s *Postgres) ListCreditProfilesReviewDue(ctx context.Context, legalEntityID, dueBy string, limit int) ([]CreditProfile, error) {
	return s.listProfiles(ctx, legalEntityID, dueBy, limit)
}

func (s *Postgres) listProfiles(ctx context.Context, legalEntityID, dueBy string, limit int) ([]CreditProfile, error) {
	p, _ := tenant.FromContext(ctx)
	var due *string // null lists every profile; a day lists the reviews due by it
	if dueBy != "" {
		due = &dueBy
	}
	out := []CreditProfile{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+profileColumns+` from credit_profile
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
				and ($4::date is null or review_date <= $4::date)
			order by case when $4::date is null then null else review_date end, legal_entity_id, customer_id limit $3`, p.TenantID, legalEntityID, limit, due)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanProfile(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		return listOpenOrdersTx(ctx, tx, p.TenantID, out)
	})
	return out, err
}

// listOpenOrdersTx fills the computed open-orders and delivered-not-invoiced
// figures of a page of profiles with one grouped query each rather than one
// per profile.
func listOpenOrdersTx(ctx context.Context, tx pgx.Tx, tenantID string, profiles []CreditProfile) error {
	if len(profiles) == 0 {
		return nil
	}
	les, customers := make([]string, len(profiles)), make([]string, len(profiles))
	at := map[[2]string]int{}
	for i, c := range profiles {
		les[i], customers[i] = c.LegalEntityID, c.CustomerID
		at[[2]string{c.LegalEntityID, c.CustomerID}] = i
		profiles[i].Exposure.OpenOrders, profiles[i].Exposure.OpenOrdersOtherCurrencies = decimal.Zero, nil
		profiles[i].Exposure.DeliveredNotInvoiced, profiles[i].Exposure.DeliveredNotInvoicedOtherCurrencies = decimal.Zero, nil
	}
	rows, err := tx.Query(ctx, `select legal_entity_id::text, customer_id::text, currency::text, sum(amount)::text from (
		select o.legal_entity_id, o.customer_id, o.currency, o.net_total as amount
		from sales_order o join unnest($2::uuid[], $3::uuid[]) as k(le, customer) on o.legal_entity_id = k.le and o.customer_id = k.customer
		where o.tenant_id = $1 and o.status = 'CONFIRMED' and o.order_type in ('ORDER', 'SUBSCRIPTION')
		union all
		select o.legal_entity_id, o.customer_id, o.currency, `+undeliveredNetSQL+`
		from sales_order o join unnest($2::uuid[], $3::uuid[]) as k(le, customer) on o.legal_entity_id = k.le and o.customer_id = k.customer
		join sales_order_line l on l.tenant_id = o.tenant_id and l.order_id = o.id
		where o.tenant_id = $1 and o.status = 'PARTIALLY_SHIPPED' and o.order_type in ('ORDER', 'SUBSCRIPTION')
		  and l.line_status <> 'CANCELLED' and l.delivered_qty < l.quantity
	) open_amounts group by legal_entity_id, customer_id, currency order by currency`, tenantID, les, customers)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var le, customer, cur, sum string
		if err := rows.Scan(&le, &customer, &cur, &sum); err != nil {
			return err
		}
		amount, err := decimal.NewFromString(sum)
		if err != nil {
			return err
		}
		c := &profiles[at[[2]string{le, customer}]]
		if cur == c.Currency {
			c.Exposure.OpenOrders = amount
		} else {
			c.Exposure.OpenOrdersOtherCurrencies = append(c.Exposure.OpenOrdersOtherCurrencies, domain.CurrencyAmount{Currency: cur, Amount: amount})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	// Delivered, not invoiced goods, grouped the same way.
	rows, err = tx.Query(ctx, `select o.legal_entity_id::text, o.customer_id::text, o.currency::text, sum(`+deliveredNotInvoicedSQL+`)::text
		from sales_order o join unnest($2::uuid[], $3::uuid[]) as k(le, customer) on o.legal_entity_id = k.le and o.customer_id = k.customer
		join sales_order_line l on l.tenant_id = o.tenant_id and l.order_id = o.id
		where o.tenant_id = $1 and o.status in ('PARTIALLY_SHIPPED', 'SHIPPED') and o.order_type = 'ORDER'
		  and l.delivered_qty > l.invoiced_qty
		group by o.legal_entity_id, o.customer_id, o.currency order by o.currency`, tenantID, les, customers)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var le, customer, cur, sum string
		if err := rows.Scan(&le, &customer, &cur, &sum); err != nil {
			return err
		}
		amount, err := decimal.NewFromString(sum)
		if err != nil {
			return err
		}
		c := &profiles[at[[2]string{le, customer}]]
		if cur == c.Currency {
			c.Exposure.DeliveredNotInvoiced = amount
		} else {
			c.Exposure.DeliveredNotInvoicedOtherCurrencies = append(c.Exposure.DeliveredNotInvoicedOtherCurrencies, domain.CurrencyAmount{Currency: cur, Amount: amount})
		}
	}
	return rows.Err()
}
