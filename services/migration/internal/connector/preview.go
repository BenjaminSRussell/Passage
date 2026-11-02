package connector

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// MaxPreviewRows is the most rows one preview reads.
const MaxPreviewRows = 100

// PreviewTimeout bounds one preview end to end (connecting, any token request, the read and the mapping), on top of
// the read's own page timeout, so a slow or stalled source cannot hold the request open.
var PreviewTimeout = 60 * time.Second

// maxPreviewText bounds one source or target text value in a preview, so a wide blob column cannot fill a response.
const maxPreviewText = 500

// PreviewRow is one sampled source row and what the mapping made of it. Nothing is loaded.
type PreviewRow struct {
	Index     int              `json:"index"`
	SourceKey string           `json:"sourceKey,omitempty"`
	Source    map[string]any   `json:"source"`
	Target    map[string]any   `json:"target,omitempty"`
	Warnings  []domain.Warning `json:"warnings,omitempty"`
	Rejects   []domain.Reject  `json:"rejects,omitempty"`
}

// PreviewResult is the outcome of extracting a few rows and running the mapping and rules over them.
type PreviewResult struct {
	Rows []PreviewRow `json:"rows"`
	// Columns are the source column or field names seen in the sample, sorted.
	Columns []string `json:"columns"`
	// More is true when the source holds more rows than the sample.
	More bool `json:"more"`
	// Mapped is the count of sampled rows with no reject; Rejected the rest. Both are 0 without a mapping.
	Mapped   int `json:"mapped"`
	Rejected int `json:"rejected"`
	// DuplicateKeys are source keys that repeat inside the sample.
	DuplicateKeys []string `json:"duplicateKeys"`
	// Watermark is the largest cursor value in the sample ("" without a cursor field).
	Watermark string `json:"watermark,omitempty"`
}

// Preview reads up to limit rows from the source, derives each row's key, and, when mapping is not nil, runs it with
// its rules, showing the target record and any rejects or warnings. It never writes to the source or to a target; a
// row without a usable key is shown with a reject instead of failing the preview.
func Preview(ctx context.Context, env Env, spec Spec, mapping *domain.Mapping, limit int) (PreviewResult, error) {
	if limit < 1 || limit > MaxPreviewRows {
		limit = 10
	}
	ctx, cancel := context.WithTimeout(ctx, PreviewTimeout)
	defer cancel()
	src, err := env.Open(spec)
	if err != nil {
		return PreviewResult{}, err
	}
	defer src.Close()
	// One row more than asked shows whether the source holds more than the sample.
	page, err := src.Read(ctx, Query{Limit: limit + 1})
	if err != nil {
		return PreviewResult{}, scrubErr(err, spec.Secret)
	}
	res := PreviewResult{Rows: []PreviewRow{}, DuplicateKeys: []string{}, Watermark: page.Watermark}
	if len(page.Rows) > limit {
		page.Rows, res.More = page.Rows[:limit], true
	}
	if page.Next != "" {
		res.More = true
	}
	cols := map[string]bool{}
	seen := map[string]bool{}
	dup := map[string]bool{}
	for i, row := range page.Rows {
		for c := range row {
			cols[c] = true
		}
		pr := PreviewRow{Index: i + 1, Source: clip(row)}
		key, kerr := domain.SourceKey(row, spec.KeyFields)
		if kerr != nil {
			pr.Rejects = append(pr.Rejects, domain.Reject{Code: "MIG-ROW-NO-KEY", Detail: kerr.Error()})
		} else {
			pr.SourceKey = key
			if seen[key] {
				dup[key] = true
			}
			seen[key] = true
		}
		if mapping != nil && kerr == nil {
			out, warnings, rejects, _ := mapping.ApplyTemplate(row, nil)
			pr.Warnings, pr.Rejects = warnings, append(pr.Rejects, rejects...)
			if len(rejects) == 0 {
				pr.Target = clip(out)
			}
		}
		if mapping != nil {
			if len(pr.Rejects) > 0 {
				res.Rejected++
			} else {
				res.Mapped++
			}
		}
		res.Rows = append(res.Rows, pr)
	}
	res.Columns = make([]string, 0, len(cols))
	for c := range cols {
		res.Columns = append(res.Columns, c)
	}
	sort.Strings(res.Columns)
	for k := range dup {
		res.DuplicateKeys = append(res.DuplicateKeys, k)
	}
	slices.Sort(res.DuplicateKeys)
	return res, nil
}

// clip returns a copy of a row whose long text values are cut, so one wide column cannot dominate a response.
func clip(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		if s, ok := v.(string); ok && len(s) > maxPreviewText {
			cut := maxPreviewText
			for cut > 0 && !utf8.RuneStart(s[cut]) {
				cut--
			}
			v = s[:cut] + fmt.Sprintf("... (%d bytes)", len(s))
		}
		out[k] = v
	}
	return out
}
