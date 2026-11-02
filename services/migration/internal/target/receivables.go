package target

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// maxOpenItems is the most rows POST /open-items:batch takes (receivables domain.MaxOpeningRows) and maxImportSource
// the longest importSource it accepts (receivables domain.MaxImportSource); a custom_<name> source system can be longer.
const (
	maxOpenItems    = 1000
	maxImportSource = 40
)

var openItemFields = []string{"documentNumber", "kind", "side", "documentDate", "dueDate", "currency", "originalAmount", "openAmount", "cashDiscountDate", "cashDiscountAmount"}

type openingResult struct {
	Results []struct {
		Index      int    `json:"index"`
		Outcome    string `json:"outcome"`
		OpenItemID string `json:"openItemId"`
		Code       string `json:"code"`
		Detail     string `json:"detail"`
	} `json:"results"`
}

// openItems loads a cutover snapshot of open AR items through POST /v1/receivables/open-items:batch, one request per
// legal entity. The batch is keyed by (importSource, legacyKey), so a retry or a rerun updates instead of duplicating;
// the target answers per row and a refused row keeps the target's AR-OPENING-* code. The customer is the legacy
// customer key of an earlier customer load (customerKey) or a Passage customer id (customerId).
func (l *Loader) openItems(ctx context.Context, tenantID string, items []domain.LoadItem) ([]domain.LoadResult, error) {
	if l.cfg.ReceivablesURL == "" {
		return all(items, domain.LoadResult{Code: CodeNotAvailable, Detail: "the receivables service address is not configured for the migration service"}), nil
	}
	if len(l.run.SourceSystem) > maxImportSource {
		return all(items, domain.LoadResult{Code: CodeRefused, Detail: fmt.Sprintf("receivables names an import source in at most %d characters; source system %s is longer", maxImportSource, l.run.SourceSystem)}), nil
	}
	results := make([]domain.LoadResult, len(items))
	byEntity := map[string][]int{}
	rows := make([]map[string]any, len(items))
	for i, it := range items {
		le, bad := l.legalEntity(it.Fields)
		if bad != nil {
			results[i] = *bad
			continue
		}
		if missing := required(it.Fields, openItemFields[:8]...); len(missing) > 0 {
			results[i] = missingResult(missing)
			continue
		}
		customer, bad := l.ref(ctx, it.Fields, "customerId", "customerKey", "customer")
		if bad != nil {
			results[i] = *bad
			continue
		}
		row := map[string]any{"legacyKey": it.SourceKey, "customerId": customer}
		pick(row, it.Fields, openItemFields...)
		rows[i] = row
		byEntity[le] = append(byEntity[le], i)
	}
	entities := make([]string, 0, len(byEntity))
	for le := range byEntity {
		entities = append(entities, le)
	}
	sort.Strings(entities)
	c := &call{l: l, tenantID: tenantID, aud: audReceivables, base: l.cfg.ReceivablesURL}
	for _, le := range entities {
		idx := byEntity[le]
		for start := 0; start < len(idx); start += maxOpenItems {
			chunk := idx[start:min(start+maxOpenItems, len(idx))]
			if l.down(audReceivables) {
				for _, i := range chunk {
					results[i] = domain.LoadResult{Code: CodeUnavailable, Detail: "the receivables service stopped answering earlier in this batch"}
				}
				continue
			}
			batch := make([]map[string]any, len(chunk))
			for j, i := range chunk {
				batch[j] = rows[i]
			}
			r, err := c.do(ctx, http.MethodPost, "/v1/receivables/open-items:batch", le, nil,
				map[string]any{"legalEntityId": le, "importSource": l.run.SourceSystem, "atomic": false, "items": batch})
			if err != nil {
				return nil, err
			}
			if !r.ok() {
				res := r.refusal()
				for _, i := range chunk {
					results[i] = res
				}
				continue
			}
			var out openingResult
			if json.Unmarshal(r.body, &out) != nil || len(out.Results) != len(chunk) {
				for _, i := range chunk {
					results[i] = domain.LoadResult{Code: CodeRefused, Detail: "the receivables answer did not match the batch sent"}
				}
				continue
			}
			// A row the answer does not account for is refused, never taken as loaded.
			for _, i := range chunk {
				results[i] = domain.LoadResult{Code: CodeRefused, Detail: "the receivables answer has no result for this row"}
			}
			for _, res := range out.Results {
				if res.Index < 0 || res.Index >= len(chunk) {
					continue
				}
				i := chunk[res.Index]
				switch res.Outcome {
				case "CREATED", "UPDATED", "UNCHANGED":
					results[i] = domain.LoadResult{TargetID: res.OpenItemID}
				default:
					code := res.Code
					if code == "" {
						code = CodeRefused
					}
					results[i] = domain.LoadResult{Code: code, Detail: clip(res.Detail)}
				}
			}
		}
	}
	return results, nil
}
