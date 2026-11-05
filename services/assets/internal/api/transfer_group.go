package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// DutyConfigure covers group assets (PRD 7.37.17 assets.asset.configure: classes, book
// definitions, profiles, posting profiles, group assets).
const DutyConfigure = "assets.asset.configure"

type transferPartRequest struct {
	AssetNumber   string  `json:"assetNumber"`
	Name          string  `json:"name"`
	Percentage    string  `json:"percentage"`
	LegalEntityID *string `json:"legalEntityId"`
	ClassCode     *string `json:"classCode"`
	Barcode       *string `json:"barcode"`
	Quantity      *string `json:"quantity"`
}

type transferRequest struct {
	Kind         string                `json:"kind"`
	TransferDate *string               `json:"transferDate"`
	Parts        []transferPartRequest `json:"parts"`
}

// transfer moves the whole asset into one new asset or splits it into several
// (FR-37.7). It needs Idempotency-Key and If-Match like a disposal, because it
// empties every book of the source.
func (a *API) transfer(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req transferRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	kind := domain.TransferKind(req.Kind)
	if kind != domain.TransferKindTransfer && kind != domain.TransferKindSplit {
		fields["kind"] = "must be transfer or split"
	}
	date := store.NewDate(a.now())
	if req.TransferDate != nil {
		var err error
		if date, err = store.ParseDate(*req.TransferDate); err != nil {
			fields["transferDate"] = "must be a YYYY-MM-DD date"
		}
	}
	if len(req.Parts) == 0 || len(req.Parts) > domain.MaxTransferParts {
		fields["parts"] = "must list 1 to " + strconv.Itoa(domain.MaxTransferParts) + " target assets"
	}
	parts := make([]store.TransferPart, 0, len(req.Parts))
	numbers := map[string]bool{}
	for i, p := range req.Parts {
		at := "parts[" + strconv.Itoa(i) + "]."
		part := store.TransferPart{AssetNumber: strings.TrimSpace(p.AssetNumber), Name: strings.TrimSpace(p.Name)}
		if n := part.AssetNumber; n == "" || len(n) > 40 {
			fields[at+"assetNumber"] = "must be 1 to 40 characters"
		} else if numbers[n] {
			fields[at+"assetNumber"] = "is used by another target"
		}
		numbers[part.AssetNumber] = true
		if n := part.Name; n == "" || len(n) > 120 {
			fields[at+"name"] = "must be 1 to 120 characters"
		}
		pct, err := decimal.NewFromString(p.Percentage)
		if err != nil {
			fields[at+"percentage"] = "must be a decimal such as 60 or 33.33"
		}
		part.Percentage = pct
		if p.LegalEntityID != nil {
			if !ids.Valid(*p.LegalEntityID) {
				fields[at+"legalEntityId"] = "must be a UUID"
			}
			part.LegalEntityID = *p.LegalEntityID
		}
		if p.ClassCode != nil {
			if !classCode.MatchString(*p.ClassCode) {
				fields[at+"classCode"] = "must be upper-case letters, digits, hyphen or underscore"
			}
			part.ClassCode = *p.ClassCode
		}
		if p.Barcode != nil {
			if b := strings.TrimSpace(*p.Barcode); b == "" || len(b) > 64 {
				fields[at+"barcode"] = "must be 1 to 64 characters, or omitted"
			} else {
				part.Barcode = &b
			}
		}
		if p.Quantity != nil {
			q, err := decimal.NewFromString(*p.Quantity)
			if err != nil || !q.IsPositive() || q.Exponent() < -4 {
				fields[at+"quantity"] = "must be a positive decimal with at most 4 places"
			}
			part.Quantity = &q
		}
		parts = append(parts, part)
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	for _, p := range parts {
		if p.LegalEntityID != "" && !inScope(w, r, p.LegalEntityID) {
			return
		}
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset with id "+id))
		return
	}
	res, err := a.store.Transfer(r.Context(), id, store.TransferInput{Version: version, Kind: kind, Date: date, Parts: parts}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.transferred.Add(1)
	if res.Source != nil {
		setETag(w, res.Source.Version)
	}
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) listTransfers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset with id "+id))
		return
	}
	items, err := a.store.ListTransfers(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type groupAssetRequest struct {
	LegalEntityID        string   `json:"legalEntityId"`
	GroupCode            string   `json:"groupCode"`
	Name                 string   `json:"name"`
	ClassCode            string   `json:"classCode"`
	BookCode             string   `json:"bookCode"`
	TaxYear              int      `json:"taxYear"`
	Method               string   `json:"method"`
	Factor               *string  `json:"factor"`
	SwitchToStraightLine bool     `json:"switchToStraightLine"`
	ServiceLifePeriods   int      `json:"serviceLifePeriods"`
	Convention           string   `json:"convention"`
	MemberAssetIDs       []string `json:"memberAssetIds"`
}

// createGroupAsset pools member assets into a group asset on one tax book
// (FR-37.16, duty assets.asset.configure).
func (a *API) createGroupAsset(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req groupAssetRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !classCode.MatchString(req.GroupCode) {
		fields["groupCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if !classCode.MatchString(req.ClassCode) {
		fields["classCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	if !bookCode.MatchString(req.BookCode) {
		fields["bookCode"] = "must be a book code such as TAX"
	}
	if req.TaxYear < minTaxYear || req.TaxYear > maxTaxYear {
		fields["taxYear"] = "must be a four-digit year"
	}
	if !validMeth[req.Method] {
		fields["method"] = "must be StraightLine, DecliningBalance or MACRS"
	}
	if !validConv[req.Convention] {
		fields["convention"] = "must be None, FullMonth, MidMonth15, HalfYear or MidQuarter"
	}
	if req.ServiceLifePeriods < 1 || req.ServiceLifePeriods > 1200 {
		fields["serviceLifePeriods"] = "must be 1 to 1200"
	}
	factor := decimal.Zero
	if req.Factor != nil {
		var err error
		if factor, err = decimal.NewFromString(*req.Factor); err != nil || factor.IsNegative() {
			fields["factor"] = "must be a non-negative decimal such as 2 for 200%"
		}
	}
	if len(req.MemberAssetIDs) == 0 || len(req.MemberAssetIDs) > store.MaxGroupMembers {
		fields["memberAssetIds"] = "must list 1 to " + strconv.Itoa(store.MaxGroupMembers) + " assets"
	}
	seen := map[string]bool{}
	for _, id := range req.MemberAssetIDs {
		switch {
		case !ids.Valid(id):
			fields["memberAssetIds"] = "must be asset UUIDs"
		case seen[id]:
			fields["memberAssetIds"] = "lists asset " + id + " twice"
		}
		seen[id] = true
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	g, err := a.store.CreateGroupAsset(r.Context(), store.GroupAssetInput{LegalEntityID: req.LegalEntityID, GroupCode: req.GroupCode, Name: name,
		ClassCode: req.ClassCode, BookCode: req.BookCode, TaxYear: req.TaxYear, Method: req.Method, Factor: factor,
		SwitchToStraightLine: req.SwitchToStraightLine, ServiceLifePeriods: req.ServiceLifePeriods, Convention: req.Convention,
		MemberAssetIDs: req.MemberAssetIDs}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.grouped.Add(1)
	setETag(w, g.Version)
	httpx.JSON(w, http.StatusCreated, g)
}

func (a *API) listGroupAssets(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	legalEntity := r.URL.Query().Get("legalEntityId")
	if legalEntity != "" && !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := a.store.ListGroupAssets(r.Context(), store.GroupFilter{LegalEntityID: legalEntity, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getGroupAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeGroupNotFound, "no group asset with id "+id))
		return
	}
	g, err := a.store.GetGroupAsset(r.Context(), id)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	setETag(w, g.Version)
	httpx.JSON(w, http.StatusOK, g)
}
