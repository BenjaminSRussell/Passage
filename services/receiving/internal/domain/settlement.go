package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	InvoicingPlansPathTmpl   = "/v1/procurement/purchase-orders/%s/invoicing-plans"
	InventoryJournalsPath    = "/v1/inventory/journals"
	InventoryJournalPathTmpl = "/v1/inventory/journals/%s"
	SettlementPlanRow        = "PLAN_ROW"
	SettlementConsignment    = "CONSIGNMENT"
	PlanRowOpen              = "OPEN"
	PlanRowInvoiced          = "INVOICED"
	CodeOverCredit           = "AP-CRD-001"
	CodePlanSettled          = "AP-PLN-001"
	settlementKeyFormat      = "post:receiving:settlement:%s:%s"
)

var (
	ErrOwnershipChangeNotFound = errors.New("ownership change journal not found")
	ErrInventoryForbidden      = errors.New("inventory journals are not accessible to the caller")
)

type PlanRow struct {
	Seq     int
	DueDate time.Time
	Amount  decimal.Decimal
	Status  string
}

type InvoicingPlan struct {
	PurchaseOrderID      string
	LineNo               int
	SettleWithoutReceipt bool
	Currency             string
	Rows                 []PlanRow
}

type InvoicingPlans interface {
	List(ctx context.Context, purchaseOrderID string) ([]InvoicingPlan, error)
}

type OwnershipChange struct {
	JournalID      string
	LegalEntityID  string
	VendorID       string
	Currency       string
	AccrualAccount string
	PostingDate    time.Time
	Amount         decimal.Decimal
}

type OwnershipChanges interface {
	List(ctx context.Context, legalEntityID string) (journalIDs []string, truncated bool, err error)
	Get(ctx context.Context, journalID string) (OwnershipChange, error)
}

type FakeInvoicingPlans struct {
	Plans map[string][]InvoicingPlan
	Err   error
}

func (f FakeInvoicingPlans) List(_ context.Context, purchaseOrderID string) ([]InvoicingPlan, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if plans, ok := f.Plans[purchaseOrderID]; ok {
		return plans, nil
	}
	return nil, ErrPurchaseOrderNotFound
}

type FakeOwnershipChanges struct {
	Changes   []OwnershipChange
	Truncated bool
	Err       error
}

func (f FakeOwnershipChanges) List(_ context.Context, legalEntityID string) ([]string, bool, error) {
	if f.Err != nil {
		return nil, false, f.Err
	}
	var out []string
	for _, c := range f.Changes {
		if c.LegalEntityID == legalEntityID {
			out = append(out, c.JournalID)
		}
	}
	return out, f.Truncated, nil
}

func (f FakeOwnershipChanges) Get(_ context.Context, journalID string) (OwnershipChange, error) {
	if f.Err != nil {
		return OwnershipChange{}, f.Err
	}
	for _, c := range f.Changes {
		if c.JournalID == journalID {
			return c, nil
		}
	}
	return OwnershipChange{}, ErrOwnershipChangeNotFound
}

type Settlement struct {
	ID             string
	Kind           string
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	PostingDate    time.Time
	DocumentDate   time.Time
	Currency       string
	DebitAccount   string
	Amount         decimal.Decimal
}

func checkAPPeriod(ctx context.Context, periods PeriodControl, legalEntityID, ledgerID, fiscalPeriodID string, date time.Time, documentID string) error {
	status, err := periods.AP(ctx, PeriodQuery{LegalEntityID: legalEntityID, LedgerID: ledgerID, FiscalPeriodID: fiscalPeriodID, PostingDate: date})
	if err != nil {
		slog.ErrorContext(ctx, "period control unavailable", "err", err, "documentId", documentID)
		return fmt.Errorf("check AP period for document %s: %w", documentID, err)
	}
	if status != PeriodOpen {
		return fail(CodePeriodClosed, "AP period for %s is %s", date.Format(time.DateOnly), status)
	}
	return nil
}

func postEntry(ctx context.Context, ledger LedgerPoster, entry JournalEntry, documentID string) (string, error) {
	entryID, err := ledger.Post(ctx, entry)
	switch {
	case err == nil:
		return entryID, nil
	case errors.Is(err, ErrPeriodClosed):
		return "", fail(CodePeriodClosed, "ledger rejected document %s: period closed", documentID)
	case errors.Is(err, ErrLedgerConflict):
		return "", fail(CodePostingPending, "ledger holds a different posting under the key for document %s", documentID)
	}
	slog.ErrorContext(ctx, "document ledger post failed", "err", err, "documentId", documentID)
	return "", fmt.Errorf("post document %s: %w", documentID, err)
}

func PostSettlement(ctx context.Context, s Settlement, periods PeriodControl, ledger LedgerPoster, profile PostingProfile) (JournalEntry, error) {
	switch {
	case s.PostingDate.IsZero() || s.LedgerID == "" || s.FiscalPeriodID == "":
		return JournalEntry{}, fail(CodeInvalidReceipt, "settlement %s needs a posting date, a ledger and a fiscal period", s.ID)
	case s.DebitAccount == "" || len(s.Currency) != 3:
		return JournalEntry{}, fail(CodeInvalidReceipt, "settlement %s needs a debit account and a currency", s.ID)
	case !s.Amount.IsPositive() || !s.Amount.Equal(roundedMoney(s.Amount, s.Currency).Value):
		return JournalEntry{}, fail(CodeInvalidReceipt, "settlement %s amount %s must be positive with at most the currency's decimals", s.ID, s.Amount)
	}
	if err := checkAPPeriod(ctx, periods, s.LegalEntityID, s.LedgerID, s.FiscalPeriodID, s.PostingDate, s.ID); err != nil {
		return JournalEntry{}, err
	}
	documentDate := s.DocumentDate
	if documentDate.IsZero() {
		documentDate = s.PostingDate
	}
	amount := roundedMoney(s.Amount, s.Currency)
	entry := JournalEntry{
		IdempotencyKey: fmt.Sprintf(settlementKeyFormat, s.ID, strings.ToLower(s.Kind)),
		LegalEntityID:  s.LegalEntityID,
		LedgerID:       s.LedgerID,
		PostingDate:    s.PostingDate,
		DocumentDate:   documentDate,
		Source:         DocumentSource{Context: "receiving", DocumentType: "settlement", DocumentID: s.ID, Purpose: strings.ToLower(s.Kind)},
		RateType:       "spot",
		Lines:          []JournalLine{{Account: s.DebitAccount, Amount: amount}, {Account: profile.VendorControl, Amount: amount.Neg()}},
	}
	entryID, err := postEntry(ctx, ledger, entry, s.ID)
	if err != nil {
		return JournalEntry{}, err
	}
	entry.LedgerEntryID = entryID
	return entry, nil
}
