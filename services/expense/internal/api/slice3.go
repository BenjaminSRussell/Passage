package api

import (
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
)

const (
	// maxMatchItems covers the AC-43.7 batch (1,000 card transactions, 800 receipts).
	maxMatchItems         = 1000
	maxRequisitionCats    = 50
	maxLodgingNights      = 366
	maxToleranceDaysOrPct = 10
)

// requisitionCategories validates the categories that need a requisition.
func requisitionCategories(fields map[string]string, cats []string) {
	if len(cats) > maxRequisitionCats {
		fields["requisitionCategories"] = fmt.Sprintf("at most %d categories", maxRequisitionCats)
		return
	}
	for i, c := range cats {
		if c == "" || len(c) > 32 {
			fields[fmt.Sprintf("requisitionCategories[%d]", i)] = "is required and at most 32 characters"
		}
	}
}

type lodgingRequest struct {
	LineID        string `json:"lineId"`
	Amount        string `json:"amount"`
	Nights        int    `json:"nights"`
	LodgingRate   string `json:"lodgingRate"`
	Justification string `json:"justification"`
}

type lodgingResponse struct {
	CapPerNight string         `json:"capPerNight"`
	Cap         string         `json:"cap"`
	OverCap     bool           `json:"overCap"`
	Satisfied   bool           `json:"satisfied"`
	Result      *domain.Result `json:"result,omitempty"`
}

// lodgingCheck is the BR-43.5 lodging cap: a stay above the per-diem lodging
// rate × 1.5 per night needs a justification. The rate is caller input until
// the pack's per-diem tables are loaded.
func (a *API) lodgingCheck(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req lodgingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.LineID) > 64 {
		fields["lineId"] = "at most 64 characters"
	}
	amount := money(fields, "amount", req.Amount)
	rate := money(fields, "lodgingRate", req.LodgingRate)
	if req.Nights < 1 || req.Nights > maxLodgingNights {
		fields["nights"] = fmt.Sprintf("must be 1 to %d", maxLodgingNights)
	}
	if len(req.Justification) > 500 {
		fields["justification"] = "at most 500 characters"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	c := domain.CheckLodging(req.LineID, amount, req.Nights, rate, req.Justification)
	resp := lodgingResponse{CapPerNight: c.CapPerNight.StringFixed(2), Cap: c.Cap.StringFixed(2), OverCap: c.OverCap, Satisfied: true, Result: c.Result}
	if c.Result != nil {
		resp.Satisfied = c.Result.Satisfied
	}
	a.lodgings.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}

type mileageRequest struct {
	Distance string `json:"distance"`
	Rate     string `json:"rate"`
}

type mileageResponse struct {
	Amount string `json:"amount"`
}

// mileage is distance × rate, rounded to the cent. The rate is the pack's
// mileage table value for the vehicle type and date, supplied by the caller.
func (a *API) mileage(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req mileageRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	distance := money(fields, "distance", req.Distance)
	rate := money(fields, "rate", req.Rate)
	if _, bad := fields["distance"]; !bad && distance.GreaterThan(decimal.NewFromInt(100_000)) {
		fields["distance"] = "must be at most 100000"
	}
	if _, bad := fields["rate"]; !bad && rate.GreaterThan(decimal.NewFromInt(100)) {
		fields["rate"] = "must be at most 100 per unit"
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	a.mileages.Add(1)
	httpx.JSON(w, http.StatusOK, mileageResponse{Amount: domain.MileageAmount(distance, rate).StringFixed(2)})
}

type matchItemDTO struct {
	ID       string `json:"id"`
	WorkerID string `json:"workerId"`
	Date     string `json:"date"`
	Merchant string `json:"merchant"`
	Amount   string `json:"amount"`
}

type cardMatchRequest struct {
	Cards         []matchItemDTO `json:"cards"`
	Receipts      []matchItemDTO `json:"receipts"`
	TolerancePct  string         `json:"tolerancePct"`
	ToleranceDays *int           `json:"toleranceDays"`
}

type pairDTO struct {
	CardID    string `json:"cardId"`
	ReceiptID string `json:"receiptId"`
}

type cardMatchResponse struct {
	Matches           []pairDTO `json:"matches"`
	UnmatchedCards    []string  `json:"unmatchedCards"`
	UnmatchedReceipts []string  `json:"unmatchedReceipts"`
	AutoMatchRate     string    `json:"autoMatchRate"`
}

// cardMatch matches a batch of card transactions to receipts one to one,
// never across workers (FR-43.13). The tolerance defaults to ±1 % and 2 days;
// the tenant parameter range is 0–10 % and 0–10 days.
func (a *API) cardMatch(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req cardMatchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	tol := domain.DefaultTolerance()
	if req.TolerancePct != "" {
		pct, err := decimal.NewFromString(req.TolerancePct)
		if err != nil || pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(maxToleranceDaysOrPct)) {
			fields["tolerancePct"] = "must be a percentage from 0 to 10"
		} else {
			tol.Pct = pct.Div(decimal.NewFromInt(100))
		}
	}
	if req.ToleranceDays != nil {
		if *req.ToleranceDays < 0 || *req.ToleranceDays > maxToleranceDaysOrPct {
			fields["toleranceDays"] = "must be 0 to 10"
		} else {
			tol.Days = *req.ToleranceDays
		}
	}
	if len(req.Cards) > maxMatchItems {
		fields["cards"] = fmt.Sprintf("at most %d card transactions", maxMatchItems)
	}
	if len(req.Receipts) > maxMatchItems {
		fields["receipts"] = fmt.Sprintf("at most %d receipts", maxMatchItems)
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	cards := make([]domain.CardTxn, 0, len(req.Cards))
	seen := map[string]bool{}
	for i, c := range req.Cards {
		cards = append(cards, toCardTxn(fields, fmt.Sprintf("cards[%d].", i), c, seen))
	}
	receipts := make([]domain.Receipt, 0, len(req.Receipts))
	seen = map[string]bool{}
	for i, rc := range req.Receipts {
		c := toCardTxn(fields, fmt.Sprintf("receipts[%d].", i), rc, seen)
		receipts = append(receipts, domain.Receipt{ID: c.ID, WorkerID: c.WorkerID, Date: c.Date, Merchant: c.Merchant, Amount: c.Amount})
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	res := domain.MatchAll(cards, receipts, tol)
	resp := cardMatchResponse{
		Matches:           make([]pairDTO, 0, len(res.Pairs)),
		UnmatchedCards:    res.UnmatchedCards,
		UnmatchedReceipts: res.UnmatchedReceipts,
		AutoMatchRate:     res.Rate.StringFixed(4),
	}
	for _, p := range res.Pairs {
		resp.Matches = append(resp.Matches, pairDTO{CardID: p.CardID, ReceiptID: p.ReceiptID})
	}
	a.cardMatches.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}

// toCardTxn validates one card transaction or receipt; IDs are unique within
// their list.
func toCardTxn(fields map[string]string, prefix string, d matchItemDTO, seen map[string]bool) domain.CardTxn {
	switch {
	case d.ID == "" || len(d.ID) > 64:
		fields[prefix+"id"] = "is required and at most 64 characters"
	case seen[d.ID]:
		fields[prefix+"id"] = "must be unique within its list"
	}
	seen[d.ID] = true
	if d.WorkerID == "" || len(d.WorkerID) > 64 {
		fields[prefix+"workerId"] = "is required and at most 64 characters"
	}
	if d.Merchant == "" || len(d.Merchant) > 200 {
		fields[prefix+"merchant"] = "is required and at most 200 characters"
	}
	date := parseDate(fields, prefix+"date", d.Date)
	amount := money(fields, prefix+"amount", d.Amount)
	return domain.CardTxn{ID: d.ID, WorkerID: d.WorkerID, Date: date, Merchant: d.Merchant, Amount: amount}
}
