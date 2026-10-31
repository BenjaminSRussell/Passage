// Package golden loads the BOM explosion fixtures under testdata/ that
// testdata/gen_bom_golden.py writes; it is imported only by tests.
package golden

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
)

// Fixture is a catalog of BOM versions with the explosion request it is
// exploded for and the oracle's summary of the result.
type Fixture struct {
	Root     string           `json:"root"`
	SiteID   string           `json:"siteId"`
	AsOf     string           `json:"asOf"`
	Quantity decimal.Decimal  `json:"quantity"`
	Levels   int              `json:"levels"`
	Expected Expected         `json:"expected"`
	Versions []fixtureVersion `json:"versions"`
}

type Expected struct {
	TreeNodes     int             `json:"treeNodes"`
	FlatCount     int             `json:"flatCount"`
	TotalQuantity decimal.Decimal `json:"totalQuantity"`
}

type fixtureVersion struct {
	ID            string           `json:"id"`
	ProductID     string           `json:"productId"`
	SiteID        string           `json:"siteId"`
	EffectiveFrom string           `json:"effectiveFrom"`
	EffectiveTo   string           `json:"effectiveTo"`
	FromQty       *decimal.Decimal `json:"fromQty"`
	ToQty         *decimal.Decimal `json:"toQty"`
	Status        string           `json:"status"`
	Lines         []fixtureLine    `json:"lines"`
}

type fixtureLine struct {
	LineNo        int              `json:"lineNo"`
	Component     string           `json:"component"`
	Qty           decimal.Decimal  `json:"qty"`
	Series        *decimal.Decimal `json:"series"`
	Type          string           `json:"type"`
	ScrapConst    decimal.Decimal  `json:"scrapConst"`
	ScrapVarPct   decimal.Decimal  `json:"scrapVarPct"`
	RoundUp       string           `json:"roundUp"`
	EffectiveFrom string           `json:"effectiveFrom"`
	EffectiveTo   string           `json:"effectiveTo"`
}

func day(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.DateOnly, s)
}

// Load reads a fixture file.
func Load(path string) (Fixture, error) {
	var f Fixture
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(raw, &f)
}

// AsOfDate is the explosion date.
func (f Fixture) AsOfDate() time.Time {
	t, _ := day(f.AsOf)
	return t
}

// BomVersions converts the fixture to domain versions, ids as in the file.
func (f Fixture) BomVersions() ([]domain.BomVersion, error) {
	out := make([]domain.BomVersion, 0, len(f.Versions))
	for _, fv := range f.Versions {
		v := domain.BomVersion{ID: fv.ID, ProductID: fv.ProductID, SiteID: fv.SiteID, FromQty: fv.FromQty, ToQty: fv.ToQty, Status: fv.Status}
		var errFrom, errTo error
		v.EffectiveFrom, errFrom = day(fv.EffectiveFrom)
		v.EffectiveTo, errTo = day(fv.EffectiveTo)
		if errFrom != nil || errTo != nil {
			return nil, fmt.Errorf("version %s: bad date", fv.ID)
		}
		for _, fl := range fv.Lines {
			l := domain.BomLine{LineNo: fl.LineNo, ComponentProductID: fl.Component, Quantity: fl.Qty, Series: decimal.NewFromInt(1),
				LineType: fl.Type, ScrapConst: fl.ScrapConst, ScrapVarPct: fl.ScrapVarPct, RoundUp: fl.RoundUp}
			if fl.Series != nil {
				l.Series = *fl.Series
			}
			l.EffectiveFrom, errFrom = day(fl.EffectiveFrom)
			l.EffectiveTo, errTo = day(fl.EffectiveTo)
			if errFrom != nil || errTo != nil {
				return nil, fmt.Errorf("version %s line %d: bad date", fv.ID, fl.LineNo)
			}
			v.Lines = append(v.Lines, l)
		}
		out = append(out, v)
	}
	return out, nil
}

// FlatCSV reads an expected flat list (productId,quantity with a header row).
func FlatCSV(path string) (map[string]decimal.Decimal, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		return nil, err
	}
	out := make(map[string]decimal.Decimal, len(rows))
	for _, row := range rows[1:] {
		q, err := decimal.NewFromString(row[1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", row[0], err)
		}
		out[row[0]] = q
	}
	return out, nil
}

// FlatDiff lists every product whose exploded quantity differs from want,
// including products missing on either side; empty means equal.
func FlatDiff(got []domain.FlatLine, want map[string]decimal.Decimal) []string {
	var diffs []string
	seen := make(map[string]bool, len(got))
	for _, f := range got {
		seen[f.ProductID] = true
		if w, ok := want[f.ProductID]; !ok || !w.Equal(f.Quantity) {
			diffs = append(diffs, fmt.Sprintf("%s: got %s, want %s", f.ProductID, f.Quantity, w))
		}
	}
	for p, w := range want {
		if !seen[p] {
			diffs = append(diffs, fmt.Sprintf("%s: missing, want %s", p, w))
		}
	}
	return diffs
}
