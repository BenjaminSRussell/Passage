package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	maxRatesPerRequest = 5000
	maxConvertAmounts  = 100
	maxAmountMagnitude = 1e15
	maxAmountScale     = 10
	sourceManual       = "manual"
)

type rateBody struct {
	RateType  string `json:"rateType"`
	From      string `json:"from"`
	To        string `json:"to"`
	ValidFrom string `json:"validFrom"`
	Rate      string `json:"rate"`
}

type publishRequest struct {
	Rates []rateBody `json:"rates"`
}

type postedBody struct {
	ID         string `json:"id"`
	Version    int    `json:"version"`
	Superseded bool   `json:"superseded"`
	Unchanged  bool   `json:"unchanged"`
}

type publishResponse struct {
	Rates []postedBody `json:"rates"`
}

type hopBody struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Rate      string `json:"rate"`
	ValidFrom string `json:"validFrom"`
}

type resolutionBody struct {
	RateType string    `json:"rateType"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Date     string    `json:"date"`
	Path     string    `json:"path"`
	Rate     string    `json:"rate"`
	Hops     []hopBody `json:"hops"`
}

type historyBody struct {
	ID                string `json:"id"`
	Version           int    `json:"version"`
	ValidFrom         string `json:"validFrom"`
	Rate              string `json:"rate"`
	Source            string `json:"source"`
	ProviderTimestamp string `json:"providerTimestamp,omitempty"`
	PayloadHash       string `json:"payloadHash,omitempty"`
	SupersededBy      string `json:"supersededBy,omitempty"`
	CreatedAt         string `json:"createdAt"`
	CreatedBy         string `json:"createdBy,omitempty"`
}

type amountBody struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type convertRequest struct {
	RateType string       `json:"rateType"`
	To       string       `json:"to"`
	Date     string       `json:"date"`
	Via      string       `json:"via"`
	Amounts  []amountBody `json:"amounts"`
}

type convertedBody struct {
	Amount    amountBody `json:"amount"`
	Converted amountBody `json:"converted"`
	Path      string     `json:"path"`
	Rate      string     `json:"rate"`
}

type convertResponse struct {
	RateType string          `json:"rateType"`
	Date     string          `json:"date"`
	To       string          `json:"to"`
	Results  []convertedBody `json:"results"`
}

func badRequest(fields map[string]string) *httpx.Problem {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	p.Errors = fields
	return p
}

func today() time.Time {
	y, m, d := time.Now().UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func parseDate(fields map[string]string, name, raw string) time.Time {
	if raw == "" {
		return today()
	}
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		fields[name] = "must be a date as YYYY-MM-DD"
	}
	return d
}

func parseRate(fields map[string]string, name, raw string) decimal.Decimal {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		fields[name] = "must be a decimal number in a string"
	}
	return v
}

func (a *API) publishRates(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Rates) == 0 || len(req.Rates) > maxRatesPerRequest {
		fields["rates"] = fmt.Sprintf("must hold 1 to %d rates", maxRatesPerRequest)
	}
	inputs := make([]store.RateInput, 0, len(req.Rates))
	for i, item := range req.Rates {
		key := func(name string) string { return fmt.Sprintf("rates[%d].%s", i, name) }
		validFrom := time.Time{}
		if item.ValidFrom == "" {
			fields[key("validFrom")] = "is required"
		} else {
			validFrom = parseDate(fields, key("validFrom"), item.ValidFrom)
		}
		inputs = append(inputs, store.RateInput{
			RateType: item.RateType, From: item.From, To: item.To, ValidFrom: validFrom,
			Rate: parseRate(fields, key("rate"), item.Rate), Source: sourceManual,
		})
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, badRequest(fields))
		return
	}
	posted, err := a.store.PostRates(r.Context(), inputs)
	if err != nil {
		a.fail(w, r, "publish rates failed", err)
		return
	}
	out := publishResponse{Rates: make([]postedBody, len(posted))}
	for i, p := range posted {
		out.Rates[i] = postedBody{ID: p.ID, Version: p.Version, Superseded: p.Superseded, Unchanged: p.Unchanged}
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) bookAt(r *http.Request, date time.Time) (*domain.Book, error) {
	if date.After(today()) {
		return nil, fmt.Errorf("%w: %s", domain.ErrFutureRate, date.Format(time.DateOnly))
	}
	return a.store.CurrentBook(r.Context(), date)
}

func rateOf(res domain.Resolution) decimal.Decimal {
	v := decimal.NewFromInt(1)
	for _, f := range res.Factors {
		v = v.Mul(f)
	}
	return v.Round(domain.MaxRateScale)
}

func toResolutionBody(typ, from, to string, date time.Time, res domain.Resolution) resolutionBody {
	out := resolutionBody{RateType: typ, From: from, To: to, Date: date.Format(time.DateOnly), Path: string(res.Path), Rate: rateOf(res).String(), Hops: make([]hopBody, len(res.Hops))}
	for i, h := range res.Hops {
		out.Hops[i] = hopBody{From: h.From, To: h.To, Rate: h.Rate.Round(domain.MaxRateScale).String(), ValidFrom: h.ValidFrom.Format(time.DateOnly)}
	}
	return out
}

func (a *API) lookupRate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	for _, name := range []string{"type", "from", "to"} {
		if q.Get(name) == "" {
			fields[name] = "is required"
		}
	}
	date := parseDate(fields, "date", q.Get("date"))
	if len(fields) > 0 {
		httpx.Fail(w, r, badRequest(fields))
		return
	}
	book, err := a.bookAt(r, date)
	if err != nil {
		a.fail(w, r, "load rate book failed", err)
		return
	}
	res, err := book.Resolve(q.Get("type"), q.Get("from"), q.Get("to"), date, q.Get("via"))
	if err != nil {
		a.fail(w, r, "resolve rate failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResolutionBody(q.Get("type"), q.Get("from"), q.Get("to"), date, res))
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (a *API) rateHistory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	for _, name := range []string{"type", "from", "to"} {
		if q.Get(name) == "" {
			fields[name] = "is required"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, badRequest(fields))
		return
	}
	entries, err := a.store.History(r.Context(), q.Get("type"), q.Get("from"), q.Get("to"))
	if err != nil {
		a.fail(w, r, "rate history failed", err)
		return
	}
	out := make([]historyBody, len(entries))
	for i, e := range entries {
		created := e.CreatedAt
		out[i] = historyBody{
			ID: e.ID, Version: e.Version, ValidFrom: e.ValidFrom.Format(time.DateOnly), Rate: e.Rate.String(), Source: e.Source,
			ProviderTimestamp: formatTime(e.ProviderTimestamp), PayloadHash: e.PayloadHash, SupersededBy: e.SupersededBy,
			CreatedAt: formatTime(&created), CreatedBy: e.CreatedBy,
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"entries": out})
}

func parseAmount(fields map[string]string, i int, in amountBody) money.Amount {
	key := func(name string) string { return fmt.Sprintf("amounts[%d].%s", i, name) }
	v, err := decimal.NewFromString(in.Value)
	if err != nil || v.Exponent() < -maxAmountScale || v.Abs().GreaterThan(decimal.NewFromFloat(maxAmountMagnitude)) {
		fields[key("value")] = "must be a decimal number in a string, at most 10 decimals and 1e15 in magnitude"
	}
	if len(in.Currency) != 3 {
		fields[key("currency")] = "must be a 3-letter upper-case ISO 4217 code"
	}
	return money.Amount{Value: v, Currency: in.Currency}
}

func (a *API) convert(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req convertRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.RateType == "" {
		fields["rateType"] = "is required"
	}
	if len(req.To) != 3 {
		fields["to"] = "must be a 3-letter upper-case ISO 4217 code"
	}
	if len(req.Amounts) == 0 || len(req.Amounts) > maxConvertAmounts {
		fields["amounts"] = fmt.Sprintf("must hold 1 to %d amounts", maxConvertAmounts)
	}
	date := parseDate(fields, "date", req.Date)
	amounts := make([]money.Amount, len(req.Amounts))
	for i, in := range req.Amounts {
		amounts[i] = parseAmount(fields, i, in)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, badRequest(fields))
		return
	}
	book, err := a.bookAt(r, date)
	if err != nil {
		a.fail(w, r, "load rate book failed", err)
		return
	}
	out := convertResponse{RateType: req.RateType, Date: date.Format(time.DateOnly), To: req.To, Results: make([]convertedBody, len(amounts))}
	resolved := map[string]domain.Resolution{}
	for i, amount := range amounts {
		res, seen := resolved[amount.Currency]
		if !seen {
			if res, err = book.Resolve(req.RateType, amount.Currency, req.To, date, req.Via); err != nil {
				a.fail(w, r, "resolve rate failed", err)
				return
			}
			resolved[amount.Currency] = res
		}
		converted, err := domain.Convert(amount, req.To, res)
		if err != nil {
			a.fail(w, r, "convert failed", err)
			return
		}
		out.Results[i] = convertedBody{
			Amount:    amountBody{Value: amount.Value.String(), Currency: amount.Currency},
			Converted: amountBody{Value: converted.Value.StringFixed(money.MinorUnits(req.To)), Currency: req.To},
			Path:      string(res.Path), Rate: rateOf(res).String(),
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}
