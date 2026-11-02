package connector

import (
	"context"
	"fmt"

	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// Binding is one entity's source for an Extractor. Open is called when the entity is extracted and its result closed
// afterwards, so a database connection lives for one extraction.
type Binding struct {
	Open        func(ctx context.Context) (Opened, error)
	KeyFields   []string
	CursorField string
	CursorType  string
}

// Extractor implements domain.DeltaExtractor over the bindings of a project, keyed by entity.
type Extractor struct {
	Bindings map[string]Binding
	// MaxRows bounds one extraction (default domain.MaxExtractRows).
	MaxRows int
	// PageSize is the page read from the source (default domain.MaxBatch).
	PageSize int
	// MaxPages bounds the pages one extraction reads (default MaxExtractPages), so a source that keeps returning a
	// next page (empty pages, a cycle of links) ends the extraction instead of running it forever.
	MaxPages int
}

// MaxExtractPages is the default page bound of one extraction: 2,000,000 rows at 40 rows a page.
const MaxExtractPages = 50_000

var _ domain.DeltaExtractor = (*Extractor)(nil)

func (e *Extractor) Extract(ctx context.Context, entity string) ([]domain.Record, error) {
	recs, _, err := e.ExtractSince(ctx, entity, "")
	return recs, err
}

// ExtractSince reads every row changed after since and returns the new watermark. Without a cursor field the whole
// source is read every time and the watermark stays "".
func (e *Extractor) ExtractSince(ctx context.Context, entity, since string) ([]domain.Record, string, error) {
	b, ok := e.Bindings[entity]
	if !ok {
		return nil, since, fmt.Errorf("no entity binding for %s", entity)
	}
	if since != "" && b.CursorField == "" {
		since = "" // nothing to compare with: read it all
	}
	src, err := b.Open(ctx)
	if err != nil {
		return nil, since, err
	}
	defer src.Close()
	maxRows, size, maxPages := e.MaxRows, e.PageSize, e.MaxPages
	if maxRows < 1 {
		maxRows = domain.MaxExtractRows
	}
	if maxPages < 1 {
		maxPages = MaxExtractPages
	}
	if size < 1 || size > domain.MaxBatch {
		size = domain.MaxBatch
	}
	var out []domain.Record
	watermark := since
	q := Query{Since: since, Limit: size}
	for pages := 1; ; pages++ {
		page, err := src.Read(ctx, q)
		if err != nil {
			return nil, since, err
		}
		for i, row := range page.Rows {
			key, err := domain.SourceKey(row, b.KeyFields)
			if err != nil {
				return nil, since, fmt.Errorf("%s row %d: %w", entity, len(out)+i+1, err)
			}
			out = append(out, domain.Record{Key: key, Fields: row})
		}
		if len(out) > maxRows {
			return nil, since, fmt.Errorf("%s has more than %d rows to extract; extract a delta or split the source", entity, maxRows)
		}
		watermark = domain.MaxCursor(b.CursorType, watermark, page.Watermark)
		if page.Next == "" {
			return out, watermark, nil
		}
		if page.Next == q.Position {
			return nil, since, fmt.Errorf("%s: the source returned the same next page position twice, so paging would not end; check the paging settings", entity)
		}
		if pages >= maxPages {
			return nil, since, fmt.Errorf("%s needed more than %d pages; raise the page size, extract a delta or split the source", entity, maxPages)
		}
		q.Position = page.Next
	}
}
