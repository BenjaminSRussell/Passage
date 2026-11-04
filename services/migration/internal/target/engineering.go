package target

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// The engineering service keeps BOM and route versions per tenant (no legal entity) and identifies a product by its
// Passage product id. The loader creates draft versions through POST /v1/engineering/bom-versions and
// /v1/engineering/route-versions (engineering.import.write); activating one stays an engineering decision. A service
// token always names one legal entity, so the call carries the row's (or the run's), as product calls do.
var (
	bomFields   = []string{"siteId", "effectiveFrom", "effectiveTo", "fromQty", "toQty", "usage", "category"}
	routeFields = []string{"siteId", "routeCode", "effectiveFrom", "effectiveTo"}
	// bomLineFields are the line fields sent as they are; componentProductId comes from the line or its componentKey.
	bomLineFields = []string{"lineNo", "quantity", "series", "lineType", "scrapConst", "scrapVarPct", "roundUp"}
)

// productRef resolves the product a BOM, a route or a BOM line is for: a Passage product id in idField, else the legacy
// key in keyField of an item load, else of a product load.
func (l *Loader) productRef(ctx context.Context, f map[string]any, idField, keyField string) (string, *domain.LoadResult) {
	if id := text(f, idField); id != "" {
		return id, nil
	}
	if text(f, keyField) == "" {
		return "", &domain.LoadResult{Code: CodeFieldRequired, Detail: "the mapped record has no " + keyField + " (or " + idField + ")"}
	}
	id, bad := l.ref(ctx, f, idField, keyField, "item")
	if bad != nil && bad.Code == CodeRefUnresolved {
		if alt, altBad := l.ref(ctx, f, idField, keyField, "product"); altBad == nil {
			return alt, nil
		}
	}
	return id, bad
}

// list reads a mapped field that carries a list of objects: a JSON array as text (a flat extract column) or a decoded
// array (a JSON source).
func list(f map[string]any, k string) ([]map[string]any, error) {
	var raw []any
	switch v := f[k].(type) {
	case nil:
		return nil, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		dec := json.NewDecoder(strings.NewReader(v))
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%s is not a JSON array of objects", k)
		}
	case []any:
		raw = v
	case []map[string]any:
		return v, nil
	default:
		return nil, fmt.Errorf("%s is not a list", k)
	}
	out := make([]map[string]any, len(raw))
	for i, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] is not an object", k, i)
		}
		out[i] = m
	}
	return out, nil
}

// loadBom creates a draft BOM version. A row an earlier run loaded is not changed: engineering edits a draft only under
// engineering.bom.write, so a changed legacy BOM is a reject, not a silent skip.
func (l *Loader) loadBom(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error) {
	if it.Existing {
		return domain.LoadResult{Code: CodeUpdateUnavailable, Detail: "the engineering service opens no import route to change a BOM version after it is created; edit the draft in Passage or load it as a new key"}, nil
	}
	le, bad := l.legalEntity(it.Fields)
	if bad != nil {
		return *bad, nil
	}
	if missing := required(it.Fields, "effectiveFrom", "lines"); len(missing) > 0 {
		return missingResult(missing), nil
	}
	productID, bad := l.productRef(ctx, it.Fields, "productId", "productKey")
	if bad != nil {
		return *bad, nil
	}
	lines, err := list(it.Fields, "lines")
	if err != nil {
		return domain.LoadResult{Code: CodeFieldRequired, Detail: err.Error()}, nil
	}
	body := map[string]any{"productId": productID, "status": "draft"}
	pick(body, it.Fields, bomFields...)
	sent := make([]map[string]any, len(lines))
	for i, line := range lines {
		component, bad := l.productRef(ctx, line, "componentProductId", "componentKey")
		if bad != nil {
			bad.Detail = fmt.Sprintf("lines[%d]: %s", i, bad.Detail)
			return *bad, nil
		}
		out := map[string]any{"componentProductId": component}
		for _, k := range bomLineFields {
			if v, ok := line[k]; ok && v != nil && text(line, k) != "" {
				out[k] = v
			}
		}
		sent[i] = out
	}
	body["lines"] = sent
	r, err := c.do(ctx, http.MethodPost, "/v1/engineering/bom-versions", le, idemHeader(it), body)
	if err != nil {
		return domain.LoadResult{}, err
	}
	if !r.ok() {
		return r.refusal(), nil
	}
	return accepted(r, ""), nil
}

// loadRoute creates a draft route version; the operations go as the mapping gives them (engineering validates them).
func (l *Loader) loadRoute(ctx context.Context, c *call, it domain.LoadItem) (domain.LoadResult, error) {
	if it.Existing {
		return domain.LoadResult{Code: CodeUpdateUnavailable, Detail: "the engineering service opens no import route to change a route version after it is created; edit the draft in Passage or load it as a new key"}, nil
	}
	le, bad := l.legalEntity(it.Fields)
	if bad != nil {
		return *bad, nil
	}
	if missing := required(it.Fields, "siteId", "routeCode", "effectiveFrom", "operations"); len(missing) > 0 {
		return missingResult(missing), nil
	}
	productID, bad := l.productRef(ctx, it.Fields, "productId", "productKey")
	if bad != nil {
		return *bad, nil
	}
	ops, err := list(it.Fields, "operations")
	if err != nil {
		return domain.LoadResult{Code: CodeFieldRequired, Detail: err.Error()}, nil
	}
	body := map[string]any{"productId": productID, "operations": ops}
	pick(body, it.Fields, routeFields...)
	if raw := text(it.Fields, "versionNo"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return domain.LoadResult{Code: CodeFieldRequired, Detail: "versionNo must be a whole number from 1"}, nil
		}
		body["versionNo"] = n
	}
	r, err := c.do(ctx, http.MethodPost, "/v1/engineering/route-versions", le, idemHeader(it), body)
	if err != nil {
		return domain.LoadResult{}, err
	}
	if !r.ok() {
		return r.refusal(), nil
	}
	return accepted(r, ""), nil
}
