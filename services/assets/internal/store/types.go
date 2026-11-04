package store

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
)

// DateLayout is the wire format of every calendar date the service exchanges.
const DateLayout = "2006-01-02"

// Date is a calendar date. It travels as "YYYY-MM-DD" in JSON and as a
// Postgres date, so the OpenAPI `format: date` fields hold exactly that.
type Date struct{ time.Time }

// NewDate truncates t to its UTC calendar day.
func NewDate(t time.Time) Date {
	y, m, d := t.UTC().Date()
	return Date{time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// ParseDate reads "YYYY-MM-DD".
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return Date{}, err
	}
	return Date{t}, nil
}

// String renders "YYYY-MM-DD".
func (d Date) String() string { return d.Format(DateLayout) }

// MarshalJSON implements json.Marshaler.
func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON implements json.Unmarshaler.
func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := ParseDate(s)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// ScanDate implements pgtype.DateScanner.
func (d *Date) ScanDate(v pgtype.Date) error {
	if !v.Valid {
		return errors.New("store: NULL date scanned into Date")
	}
	*d = NewDate(v.Time)
	return nil
}

// DateValue implements pgtype.DateValuer.
func (d Date) DateValue() (pgtype.Date, error) {
	return pgtype.Date{Time: d.Time, Valid: true}, nil
}

// Asset is the register entry. Status follows domain.AssetStatus.
type Asset struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	AssetNumber   string          `json:"assetNumber"`
	Name          string          `json:"name"`
	ClassCode     string          `json:"classCode"`
	Barcode       *string         `json:"barcode"`
	Quantity      decimal.Decimal `json:"quantity"`
	Status        string          `json:"status"`
	AcquiredDate  *Date           `json:"acquiredDate"`
	BookCount     int             `json:"bookCount"`
	Version       int             `json:"version"`
	CreatedAt     time.Time       `json:"createdAt"`
}

// AssetDetail is an asset with every one of its books.
type AssetDetail struct {
	Asset
	Books []Book `json:"books"`
}

// AssetFilter narrows ListAssets. After is the id of the last asset of the
// previous page; Limit is 1..200.
type AssetFilter struct {
	LegalEntityID string
	Status        string
	After         string
	Limit         int
}

// AssetPage is one page of the register ordered by asset number.
type AssetPage struct {
	Items      []Asset `json:"items"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

// Book is one depreciation book of an asset with its projected balances.
type Book struct {
	ID                      string          `json:"id"`
	AssetID                 string          `json:"assetId"`
	LegalEntityID           string          `json:"legalEntityId"`
	BookCode                string          `json:"bookCode"`
	Currency                string          `json:"currency"`
	Method                  string          `json:"method"`
	Factor                  decimal.Decimal `json:"factor"`
	SwitchToStraightLine    bool            `json:"switchToStraightLine"`
	ServiceLifePeriods      int             `json:"serviceLifePeriods"`
	SalvageValue            decimal.Decimal `json:"salvageValue"`
	Convention              string          `json:"convention"`
	InServiceDate           Date            `json:"inServiceDate"`
	Cost                    decimal.Decimal `json:"cost"`
	AccumulatedDepreciation decimal.Decimal `json:"accumulatedDepreciation"`
	NBV                     decimal.Decimal `json:"nbv"`
	Status                  string          `json:"status"`
	// LastDepreciatedPeriod is the last period a posted run charged ("2026-03"
	// or "2026"), nil before the first run.
	LastDepreciatedPeriod *string `json:"lastDepreciatedPeriod"`
	// AcquisitionCost is the schedule basis a depreciation run spreads its
	// charges by (the cost at acquisition); adjustments change Cost only.
	AcquisitionCost decimal.Decimal `json:"-"`
	// GroupAssetID and GroupCode name the group asset that depreciates this
	// book (FR-37.16, depreciatedAtGroup); they are set on the asset read.
	GroupAssetID *string `json:"groupAssetId,omitempty"`
	GroupCode    *string `json:"groupCode,omitempty"`
}

// Profile maps the book onto the domain depreciation input.
func (b Book) Profile() domain.Input {
	return domain.Input{
		Cost: b.Cost, Salvage: b.SalvageValue, Life: b.ServiceLifePeriods, Method: domain.Method(b.Method), Factor: b.Factor,
		SwitchToStraightLine: b.SwitchToStraightLine, InService: b.InServiceDate.Time, Convention: domain.Convention(b.Convention), Currency: b.Currency,
	}
}

// RunProfile is the book's profile on its acquisition cost: the schedule whose
// proportions a depreciation run follows (domain.RunAmount).
func (b Book) RunProfile() domain.Input {
	in := b.Profile()
	in.Cost = b.AcquisitionCost
	if in.Salvage.GreaterThan(in.Cost) {
		in.Salvage = in.Cost
	}
	return in
}

// Transaction is one row of the append-only AssetTransaction log. Amounts are
// signed: disposals carry negative cost and accumulated amounts.
// ImpairmentAmount is the impairment share of AccumulatedAmount.
type Transaction struct {
	ID                string           `json:"id"`
	AssetBookID       string           `json:"assetBookId"`
	BookCode          string           `json:"bookCode"`
	Currency          string           `json:"currency"`
	Type              string           `json:"type"`
	TransactionDate   Date             `json:"transactionDate"`
	CostAmount        decimal.Decimal  `json:"costAmount"`
	AccumulatedAmount decimal.Decimal  `json:"accumulatedAmount"`
	ProceedsAmount    *decimal.Decimal `json:"proceedsAmount"`
	GainLoss          *decimal.Decimal `json:"gainLoss"`
	ImpairmentAmount  decimal.Decimal  `json:"impairmentAmount"`
	SourceType        string           `json:"sourceType"`
	SourceID          *string          `json:"sourceId"`
	RunID             *string          `json:"runId"`
	CreatedAt         time.Time        `json:"createdAt"`
}

// DisposalInput is a validated disposal request. Version is the asset version
// the caller read (If-Match); Date is the disposal date.
type DisposalInput struct {
	Version    int                 `json:"version"`
	Kind       domain.DisposalKind `json:"kind"`
	Percentage decimal.Decimal     `json:"percentage"`
	Proceeds   decimal.Decimal     `json:"proceeds"`
	Date       Date                `json:"date"`
}

// Disposal is the asset after a disposal, every book it touched and the
// transaction recorded on each book.
type Disposal struct {
	Asset        Asset         `json:"asset"`
	Books        []Book        `json:"books"`
	Transactions []Transaction `json:"transactions"`
}

// Lease is an IFRS 16 lease with its recognized balances and schedule.
// Liability and ROUAsset are the amounts at initial recognition; every
// remeasurement is listed in Modifications and reflected in the schedule
// (BR-37.8). PaymentAmount, TermMonths and IBRRate are the terms in force.
// RateMethod is Nominal or Effective (lease.periodicRateMethod).
type Lease struct {
	ID               string              `json:"id"`
	LegalEntityID    string              `json:"legalEntityId"`
	LeaseNumber      string              `json:"leaseNumber"`
	CommencementDate Date                `json:"commencementDate"`
	TermMonths       int                 `json:"termMonths"`
	PaymentsPerYear  int                 `json:"paymentsPerYear"`
	PaymentAmount    decimal.Decimal     `json:"paymentAmount"`
	PaymentTiming    string              `json:"paymentTiming"`
	IBRRate          decimal.Decimal     `json:"ibrRate"`
	RateMethod       string              `json:"rateMethod"`
	Currency         string              `json:"currency"`
	Liability        decimal.Decimal     `json:"liability"`
	ROUAsset         decimal.Decimal     `json:"rouAsset"`
	Status           string              `json:"status"`
	Version          int                 `json:"version"`
	CreatedAt        time.Time           `json:"createdAt"`
	Schedule         []LeaseLine         `json:"schedule,omitempty"`
	Modifications    []LeaseModification `json:"modifications,omitempty"`
	// Termination is set once the lease was terminated early (D4c).
	Termination *LeaseTermination `json:"termination,omitempty"`
}

// LeaseFilter narrows ListLeases. After is the id of the last lease of the
// previous page; Limit is 1..200.
type LeaseFilter struct {
	LegalEntityID string
	After         string
	Limit         int
}

// LeasePage is one page of leases ordered by lease number (no schedules).
type LeasePage struct {
	Items      []Lease `json:"items"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

// LeaseLine is one row of lease_schedule_line. Remeasurement is the
// modification adjustment added to Opening before the period's payment and
// interest (zero except on the first period after a modification).
type LeaseLine struct {
	PeriodNo        int             `json:"periodNo"`
	Opening         decimal.Decimal `json:"opening"`
	Remeasurement   decimal.Decimal `json:"remeasurement"`
	Interest        decimal.Decimal `json:"interest"`
	Payment         decimal.Decimal `json:"payment"`
	Closing         decimal.Decimal `json:"closing"`
	ROUDepreciation decimal.Decimal `json:"rouDepreciation"`
}
