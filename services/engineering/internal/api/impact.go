package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/shopspring/decimal"
)

type lineChangeBody struct {
	ComponentProductID string           `json:"componentProductId"`
	Change             string           `json:"change"`
	OldQuantity        *decimal.Decimal `json:"oldQuantity,omitempty"`
	NewQuantity        *decimal.Decimal `json:"newQuantity,omitempty"`
}

type impactReplacementBody struct {
	OldVersionID string           `json:"oldVersionId"`
	NewVersionID string           `json:"newVersionId"`
	ProductID    string           `json:"productId"`
	Changes      []lineChangeBody `json:"changes"`
}

type impactProductBody struct {
	ProductID  string   `json:"productId"`
	Reason     string   `json:"reason"`
	Level      int      `json:"level"`
	TopLevel   bool     `json:"topLevel"`
	SiteIDs    []string `json:"siteIds"`
	VersionIDs []string `json:"versionIds"`
}

type impactBody struct {
	AsOf                 string                  `json:"asOf"`
	Replacements         []impactReplacementBody `json:"replacements"`
	Products             []impactProductBody     `json:"products"`
	SiteIDs              []string                `json:"siteIds"`
	OpenProductionOrders int                     `json:"openProductionOrders"`
	OpenPlannedOrders    int                     `json:"openPlannedOrders"`
}

func bodyFromImpact(in domain.Impact) impactBody {
	out := impactBody{
		AsOf: formatDate(in.AsOf), SiteIDs: in.SiteIDs, OpenProductionOrders: in.OpenProductionOrders, OpenPlannedOrders: in.OpenPlannedOrders,
		Replacements: make([]impactReplacementBody, len(in.Replacements)), Products: make([]impactProductBody, len(in.Products)),
	}
	for i, r := range in.Replacements {
		b := impactReplacementBody{OldVersionID: r.OldVersionID, NewVersionID: r.NewVersionID, ProductID: r.ProductID, Changes: make([]lineChangeBody, len(r.Changes))}
		for j, c := range r.Changes {
			lc := lineChangeBody{ComponentProductID: c.ComponentProductID, Change: c.Change}
			if c.Change != domain.ChangeAdded {
				q := c.OldQty
				lc.OldQuantity = &q
			}
			if c.Change != domain.ChangeRemoved {
				q := c.NewQty
				lc.NewQuantity = &q
			}
			b.Changes[j] = lc
		}
		out.Replacements[i] = b
	}
	for i, p := range in.Products {
		out.Products[i] = impactProductBody{ProductID: p.ProductID, Reason: p.Reason, Level: p.Level, TopLevel: p.TopLevel, SiteIDs: p.SiteIDs, VersionIDs: p.VersionIDs}
	}
	return out
}

// ecoImpact answers FR-9.8: the products, sites and active versions an ECO
// touches, found by multi-level where-used of each replaced product.
func (a *API) ecoImpact(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	asOf := a.today()
	explicit := false
	if raw := r.URL.Query().Get("asOf"); raw != "" {
		var valid bool
		if asOf, valid = parseDate(raw); !valid {
			invalid(w, r, map[string]string{"asOf": "must be a date as YYYY-MM-DD"})
			return
		}
		explicit = true
	}
	record, err := a.store.GetEco(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get eco for impact", err, &a.ecosFailed)
		return
	}
	if !explicit && !record.Eco.ReleaseDate.IsZero() && record.Eco.ReleaseDate.After(asOf) {
		asOf = record.Eco.ReleaseDate
	}
	roots := make([]string, 0, len(record.Versions))
	for _, v := range record.Versions {
		roots = append(roots, v.ProductID)
	}
	snapshot, err := a.store.Catalog(r.Context(), roots, true)
	if err != nil {
		a.fail(w, r, "load impact catalog", err, &a.ecosFailed)
		return
	}
	impact, err := domain.BuildImpact(snapshot, record.Eco, record.Versions, asOf, nil)
	if err != nil {
		a.fail(w, r, "build eco impact", err, &a.ecosFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromImpact(impact))
}
