package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	platformmoney "github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
)

const (
	maxPerDiemDays    = 60
	maxAgingAdvances  = 500
	maxTravelDates    = 366
	defaultAdvanceLim = 30
	// maxFeedBytes keeps a feed inside httpx.Decode's 4 MiB body cap even when
	// JSON escaping doubles its size; 10,000 card rows are about 1 MiB.
	maxFeedBytes = 2 << 20
	// codeAdvancesBlocked is returned in the aging body when new advances are blocked.
	codeAdvancesBlocked = "EXP-ADV-002"
)

// currencyCode validates a three-letter upper-case ISO 4217 code.
func currencyCode(fields map[string]string, field, raw string) string {
	ok := len(raw) == 3
	for _, c := range raw {
		ok = ok && c >= 'A' && c <= 'Z'
	}
	if !ok {
		fields[field] = "must be a three-letter upper-case currency code"
	}
	return raw
}

// fixed formats an amount at the currency's minor unit (JPY 0, KWD 3, else 2).
func fixed(d decimal.Decimal, currency string) string {
	return d.StringFixed(platformmoney.MinorUnits(currency))
}

type perDiemRateDTO struct {
	Country          string `json:"country"`
	City             string `json:"city"`
	Currency         string `json:"currency"`
	Lodging          string `json:"lodging"`
	MealsIncidentals string `json:"mealsIncidentals"`
}

type perDiemDayDTO struct {
	Date              string `json:"date"`
	Lodging           bool   `json:"lodging"`
	BreakfastProvided bool   `json:"breakfastProvided"`
	LunchProvided     bool   `json:"lunchProvided"`
	DinnerProvided    bool   `json:"dinnerProvided"`
}

type perDiemRequest struct {
	Rate perDiemRateDTO  `json:"rate"`
	Days []perDiemDayDTO `json:"days"`
}

type perDiemLineDTO struct {
	Date    string `json:"date"`
	Partial bool   `json:"partial"`
	Lodging string `json:"lodging"`
	MIE     string `json:"mie"`
	Total   string `json:"total"`
}

type perDiemResponse struct {
	Currency string           `json:"currency"`
	Lines    []perDiemLineDTO `json:"lines"`
	Totals   totalsAmounts    `json:"totals"`
}

type totalsAmounts struct {
	Lodging string `json:"lodging"`
	MIE     string `json:"mie"`
	Total   string `json:"total"`
}

// perDiem computes BR-43.6 per diem for a trip of consecutive days (stateless;
// the rate table is supplied). The first and last day are partial.
func (a *API) perDiem(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req perDiemRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	currency := currencyCode(fields, "rate.currency", req.Rate.Currency)
	if req.Rate.Country != "" && (len(req.Rate.Country) != 2 || strings.ToUpper(req.Rate.Country) != req.Rate.Country) {
		fields["rate.country"] = "must be a two-letter upper-case country code"
	}
	if len(req.Rate.City) > 64 {
		fields["rate.city"] = "must be at most 64 characters"
	}
	lodging := money(fields, "rate.lodging", req.Rate.Lodging)
	mie := money(fields, "rate.mealsIncidentals", req.Rate.MealsIncidentals)
	if len(req.Days) == 0 || len(req.Days) > maxPerDiemDays {
		fields["days"] = fmt.Sprintf("must hold 1 to %d days", maxPerDiemDays)
	}
	days := make([]domain.PerDiemDay, 0, len(req.Days))
	for i, d := range req.Days {
		date := parseDate(fields, fmt.Sprintf("days[%d].date", i), d.Date)
		days = append(days, domain.PerDiemDay{
			Date: date, Lodging: d.Lodging,
			Meals: domain.MealsProvided{Breakfast: d.BreakfastProvided, Lunch: d.LunchProvided, Dinner: d.DinnerProvided},
		})
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	rate := domain.PerDiemRate{Country: req.Rate.Country, City: req.Rate.City, Currency: currency,
		Lodging: lodging, MealsIncidentals: mie}
	res, err := domain.ComputePerDiem(rate, days)
	if errors.Is(err, domain.ErrPerDiemDaysNotConsecutive) {
		refuse(w, r, map[string]string{"days": "must be consecutive calendar days in date order, each once"})
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lines := make([]perDiemLineDTO, 0, len(res.Lines))
	for _, l := range res.Lines {
		lines = append(lines, perDiemLineDTO{Date: l.Date.Format(dateLayout), Partial: l.Partial,
			Lodging: fixed(l.Lodging, currency), MIE: fixed(l.MIE, currency), Total: fixed(l.Total, currency)})
	}
	a.perDiems.Add(1)
	httpx.JSON(w, http.StatusOK, perDiemResponse{
		Currency: res.Currency, Lines: lines,
		Totals: totalsAmounts{Lodging: fixed(res.Lodging, currency), MIE: fixed(res.MIE, currency), Total: fixed(res.Total, currency)},
	})
}

type agingAdvanceDTO struct {
	ID         string `json:"id"`
	TripEnd    string `json:"tripEnd"`
	Paid       string `json:"paid"`
	Settled    string `json:"settled"`
	Returned   string `json:"returned"`
	Recovered  string `json:"recovered"`
	WrittenOff string `json:"writtenOff"`
}

type agingRequest struct {
	AsOf      string            `json:"asOf"`
	LimitDays int               `json:"limitDays"`
	Advances  []agingAdvanceDTO `json:"advances"`
}

// optionalMoney treats an absent amount as zero and otherwise validates like money.
func optionalMoney(fields map[string]string, field, raw string) decimal.Decimal {
	if raw == "" {
		return decimal.Zero
	}
	return money(fields, field, raw)
}

type overdueDTO struct {
	ID          string `json:"id"`
	DueDate     string `json:"dueDate"`
	DaysPastDue int    `json:"daysPastDue"`
	Open        string `json:"open"`
}

type agingResponse struct {
	NewAdvancesBlocked bool         `json:"newAdvancesBlocked"`
	Overdue            []overdueDTO `json:"overdue"`
	Code               string       `json:"code,omitempty"`
}

// advanceAging applies FR-43.15 aging: which advances are past their settlement
// due date and whether new advances are blocked (EXP-ADV-002).
func (a *API) advanceAging(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req agingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	asOf := parseDate(fields, "asOf", req.AsOf)
	limit := req.LimitDays
	if limit == 0 {
		limit = defaultAdvanceLim
	}
	if limit < 1 || limit > 180 {
		fields["limitDays"] = "must be 1 to 180 days"
	}
	if len(req.Advances) > maxAgingAdvances {
		fields["advances"] = fmt.Sprintf("must hold at most %d advances", maxAgingAdvances)
	}
	advances := make([]domain.CashAdvance, 0, len(req.Advances))
	seen := map[string]bool{}
	for i, adv := range req.Advances {
		prefix := fmt.Sprintf("advances[%d].", i)
		if adv.ID == "" || len(adv.ID) > 64 || seen[adv.ID] {
			fields[prefix+"id"] = "is required, unique and at most 64 characters"
		}
		seen[adv.ID] = true
		c := domain.CashAdvance{
			ID: adv.ID, TripEnd: parseDate(fields, prefix+"tripEnd", adv.TripEnd),
			Paid:       money(fields, prefix+"paid", adv.Paid),
			Settled:    optionalMoney(fields, prefix+"settled", adv.Settled),
			Returned:   optionalMoney(fields, prefix+"returned", adv.Returned),
			Recovered:  optionalMoney(fields, prefix+"recovered", adv.Recovered),
			WrittenOff: optionalMoney(fields, prefix+"writtenOff", adv.WrittenOff),
		}
		if c.Cleared().GreaterThan(c.Paid) {
			fields[prefix+"paid"] = "settled, returned, recovered and written off together must not exceed paid"
		}
		advances = append(advances, c)
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	overdue := []overdueDTO{}
	for _, adv := range advances {
		if domain.Overdue(adv, asOf, limit) {
			due := domain.DueDate(adv.TripEnd, limit)
			overdue = append(overdue, overdueDTO{ID: adv.ID, DueDate: due.Format(dateLayout),
				DaysPastDue: int(asOf.Sub(due).Hours() / 24), Open: adv.Open().StringFixed(2)})
		}
	}
	resp := agingResponse{NewAdvancesBlocked: domain.NewAdvancesBlocked(advances, asOf, limit), Overdue: overdue}
	if resp.NewAdvancesBlocked {
		resp.Code = codeAdvancesBlocked
	}
	a.agings.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}

type windowDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type requisitionCheckRequest struct {
	Trip           windowDTO `json:"trip"`
	TravelDates    []string  `json:"travelDates"`
	LastTravelDate string    `json:"lastTravelDate"`
	AsOf           string    `json:"asOf"`
	ReportPosted   bool      `json:"reportPosted"`
}

type requisitionCheckResponse struct {
	OutOfRangeDates []string `json:"outOfRangeDates"`
	CloseDate       string   `json:"closeDate"`
	CloseDue        bool     `json:"closeDue"`
}

// requisitionCheck applies BR-43.13: travel dates outside the approved window are
// WARN, and the requisition closes once its report posts or 60 days after the
// last travel date (default: the end of the approved window).
func (a *API) requisitionCheck(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req requisitionCheckRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	from := parseDate(fields, "trip.from", req.Trip.From)
	to := parseDate(fields, "trip.to", req.Trip.To)
	if len(fields) == 0 && to.Before(from) {
		fields["trip.to"] = "must not be before trip.from"
	}
	if len(req.TravelDates) > maxTravelDates {
		fields["travelDates"] = fmt.Sprintf("must hold at most %d dates", maxTravelDates)
	}
	dates := make([]time.Time, 0, len(req.TravelDates))
	for i, raw := range req.TravelDates {
		dates = append(dates, parseDate(fields, fmt.Sprintf("travelDates[%d]", i), raw))
	}
	last := to
	if req.LastTravelDate != "" {
		last = parseDate(fields, "lastTravelDate", req.LastTravelDate)
	}
	asOf := parseDate(fields, "asOf", req.AsOf)
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	out := []string{}
	for _, d := range domain.OutOfRangeDates(domain.TripWindow{From: from, To: to}, dates) {
		out = append(out, d.Format(dateLayout))
	}
	a.requisitionChecks.Add(1)
	httpx.JSON(w, http.StatusOK, requisitionCheckResponse{
		OutOfRangeDates: out,
		CloseDate:       domain.CloseDate(last).Format(dateLayout),
		CloseDue:        domain.CloseDue(last, asOf, req.ReportPosted),
	})
}

type cardFeedRequest struct {
	Format  string `json:"format"`
	Content string `json:"content"`
}

type importedTxnDTO struct {
	Line      int    `json:"line"`
	CardLast4 string `json:"cardLast4"`
	TxnDate   string `json:"txnDate"`
	PostDate  string `json:"postDate"`
	Merchant  string `json:"merchant"`
	MCC       string `json:"mcc"`
	Amount    string `json:"amount"`
	Currency  string `json:"currency"`
}

type rowErrorDTO struct {
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

type cardFeedResponse struct {
	Format       string           `json:"format"`
	FileHash     string           `json:"fileHash"`
	Transactions []importedTxnDTO `json:"transactions"`
	Rejected     []rowErrorDTO    `json:"rejected"`
}

// cardFeedParse parses a CSV or OFX card statement and returns its file hash, so the
// caller can de-duplicate imports (EXP-CARD-002). Nothing is stored. Card import is
// an ExpenseAdmin task (8.43.16), so the route takes the expense.card.import duty.
func (a *API) cardFeedParse(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req cardFeedRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Content == "" || len(req.Content) > maxFeedBytes {
		fields["content"] = fmt.Sprintf("must hold 1 to %d bytes", maxFeedBytes)
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	feed, err := domain.ParseCardFeed(req.Format, []byte(req.Content))
	switch {
	case errors.Is(err, domain.ErrUnsupportedFormat):
		fields["format"] = "must be CSV or OFX"
		refuse(w, r, fields)
		return
	case errors.Is(err, domain.ErrMissingColumns):
		fields["content"] = "CSV header needs card_last4, txn_date, post_date, merchant, amount and currency"
		refuse(w, r, fields)
		return
	case errors.Is(err, domain.ErrMalformedFeed):
		fields["content"] = "is not an OFX statement"
		refuse(w, r, fields)
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	}
	txns := make([]importedTxnDTO, 0, len(feed.Transactions))
	for _, t := range feed.Transactions {
		txns = append(txns, importedTxnDTO{Line: t.Line, CardLast4: t.CardLast4, TxnDate: t.TxnDate.Format(dateLayout),
			PostDate: t.PostDate.Format(dateLayout), Merchant: t.Merchant, MCC: t.MCC,
			Amount: fixed(t.Amount, t.Currency), Currency: t.Currency})
	}
	rejected := make([]rowErrorDTO, 0, len(feed.Rejected))
	for _, e := range feed.Rejected {
		rejected = append(rejected, rowErrorDTO{Line: e.Line, Reason: e.Reason})
	}
	a.feeds.Add(1)
	httpx.JSON(w, http.StatusOK, cardFeedResponse{Format: feed.Format, FileHash: feed.FileHash, Transactions: txns, Rejected: rejected})
}
