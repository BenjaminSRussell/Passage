package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

// MaxEvidenceRows bounds the lines of one transfer-pricing evidence export.
const MaxEvidenceRows = 10000

// ---- direct delivery chain (FR-40.3, AC-40.3) ----

type chainLegBody struct {
	DocID    string `json:"docId"`
	Entity   string `json:"entity"`
	Quantity string `json:"quantity"`
}

type chainPartnerBody struct {
	SellerEntity string `json:"sellerEntity"`
	BuyerEntity  string `json:"buyerEntity"`
	Active       bool   `json:"active"`
}

type chainRequest struct {
	ChainID         string           `json:"chainId"`
	Partner         chainPartnerBody `json:"partner"`
	CustomerOrder   chainLegBody     `json:"customerOrder"`
	IcPurchase      chainLegBody     `json:"icPurchase"`
	IcSales         chainLegBody     `json:"icSales"`
	ShippedQuantity string           `json:"shippedQuantity"`
}

type chainPostingBody struct {
	Kind     string `json:"kind"`
	Entity   string `json:"entity"`
	DocID    string `json:"docId"`
	Quantity string `json:"quantity"`
}

type chainResponse struct {
	ChainID  string             `json:"chainId"`
	Postings []chainPostingBody `json:"postings"`
}

func (f fieldParser) chainLeg(name string, in chainLegBody) domain.ChainLeg {
	return domain.ChainLeg{
		DocID:    f.text(name+".docId", in.DocID),
		Entity:   f.text(name+".entity", in.Entity),
		Quantity: f.positive(name+".quantity", in.Quantity),
	}
}

// previewChain validates a direct delivery chain against its pairing and
// returns the documents the seller's packing slip posts in the buyer entity in
// one saga (FR-40.3, AC-40.3). Nothing is posted.
func (a *API) previewChain(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req chainRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	partner := domain.Partner{
		SellerEntity: f.text("partner.sellerEntity", req.Partner.SellerEntity),
		BuyerEntity:  f.text("partner.buyerEntity", req.Partner.BuyerEntity),
		Active:       req.Partner.Active,
	}
	if partner.SellerEntity != "" && partner.SellerEntity == partner.BuyerEntity {
		f["partner.buyerEntity"] = "must differ from sellerEntity"
	}
	chain := domain.DeliveryChain{
		ChainID:       f.text("chainId", req.ChainID),
		CustomerOrder: f.chainLeg("customerOrder", req.CustomerOrder),
		IcPurchase:    f.chainLeg("icPurchase", req.IcPurchase),
		IcSales:       f.chainLeg("icSales", req.IcSales),
	}
	shipped := chain.IcSales.Quantity
	if req.ShippedQuantity != "" {
		shipped = f.positive("shippedQuantity", req.ShippedQuantity)
	}
	if !validation(w, r, f) {
		return
	}
	postings, err := domain.PackingSlipPostings(chain, partner, shipped)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.chainPreviews.Add(1)
	out := chainResponse{ChainID: chain.ChainID, Postings: make([]chainPostingBody, 0, len(postings))}
	for _, p := range postings {
		out.Postings = append(out.Postings, chainPostingBody{Kind: p.Kind, Entity: p.Entity, DocID: p.DocID, Quantity: p.Quantity.String()})
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- transfer-pricing evidence (FR-40.4) ----

type evidenceRowBody struct {
	Item     string `json:"item"`
	Quantity string `json:"quantity"`
	Price    string `json:"price"`
	Cost     string `json:"cost"`
	Basis    string `json:"basis"`
}

type evidenceRequest struct {
	SellerEntity string            `json:"sellerEntity"`
	BuyerEntity  string            `json:"buyerEntity"`
	Period       string            `json:"period"`
	Currency     string            `json:"currency"`
	Rows         []evidenceRowBody `json:"rows"`
}

// exportEvidence renders the transfer-pricing evidence file for one entity
// pair and period as CSV (FR-40.4). The caller supplies the priced lines; the
// stored links and charges feed it in a later slice.
func (a *API) exportEvidence(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req evidenceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := fieldParser{}
	file := domain.EvidenceFile{
		SellerEntity: f.text("sellerEntity", req.SellerEntity),
		BuyerEntity:  f.text("buyerEntity", req.BuyerEntity),
		Period:       req.Period,
	}
	if file.SellerEntity != "" && file.SellerEntity == file.BuyerEntity {
		f["buyerEntity"] = "must differ from sellerEntity"
	}
	if !periodKey.MatchString(req.Period) {
		f["period"] = "must be a period key such as 2026-11"
	}
	f.currency("currency", req.Currency)
	switch {
	case len(req.Rows) == 0:
		f["rows"] = "must contain at least one row"
	case len(req.Rows) > MaxEvidenceRows:
		f["rows"] = fmt.Sprintf("must contain at most %d rows", MaxEvidenceRows)
	}
	if _, bad := f["rows"]; !bad {
		for i, row := range req.Rows {
			at := func(name string) string { return fmt.Sprintf("rows[%d].%s", i, name) }
			file.Rows = append(file.Rows, domain.EvidenceRow{
				Item:     f.text(at("item"), row.Item),
				Quantity: f.positive(at("quantity"), row.Quantity),
				Price:    money.Amount{Value: f.amount(at("price"), row.Price, true), Currency: req.Currency},
				Cost:     money.Amount{Value: f.amount(at("cost"), row.Cost, true), Currency: req.Currency},
				Basis:    f.text(at("basis"), row.Basis),
			})
		}
	}
	if !validation(w, r, f) {
		return
	}
	out, err := domain.EvidenceCSV(file)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.evidenceExports.Add(1)
	name := safeFileName(fmt.Sprintf("tp-evidence-%s-%s-%s.csv", file.SellerEntity, file.BuyerEntity, file.Period))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, out)
}

// ---- centralized payment reversal (BR-40.11) ----

type reversalRequest struct {
	centralPaymentRequest
	PairID string `json:"pairId"`
}

// previewReversal derives the postings a returned centralized bank payment
// reverses in both entities under the original pair id (BR-40.11). The caller
// sends the original payment; the reversal is its legs with sides flipped.
// Nothing is posted.
func (a *API) previewReversal(w http.ResponseWriter, r *http.Request) {
	if !guard(w, r) {
		return
	}
	var req reversalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setup, amount, bank, owning, matrix, f := parseCentralPayment(req.centralPaymentRequest)
	if !ids.Valid(req.PairID) {
		f["pairId"] = "must be the pair id of the original payment"
	}
	if !validation(w, r, f) {
		return
	}
	j, err := domain.CentralPaymentJournal(matrix, req.PairID, setup, amount, bank, owning)
	if err == nil {
		j, err = domain.ReverseJournal(j)
	}
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.reversalPreviews.Add(1)
	httpx.JSON(w, http.StatusOK, previewResponse{PairID: j.PairID, Balanced: j.Balanced() == nil, Legs: legsOf(j)})
}

// safeFileName keeps letters, digits, dot, dash and underscore so an entity id
// cannot break out of the Content-Disposition header.
func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}
