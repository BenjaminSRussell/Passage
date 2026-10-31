package domain

import (
	"context"
	"fmt"
	"strings"
)

// Copy project codes (FR-10.14, BR-10.13).
const (
	CodeCopyInvalid   = "MIG-COPY-INVALID"
	CodeCopyKeyExists = "COPY-KEY-EXISTS"
	// KeyField names the record's own natural key in a KeyRewrite rule.
	KeyField = "@key"
)

// KeyRewrite maps a legal-entity-scoped natural key to the target entity: a value of Field that starts with From
// (a dataAreaId such as USMF, or an item-number prefix) has that prefix replaced by To.
type KeyRewrite struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func rewriteValue(v string, field string, rules []KeyRewrite) string {
	for _, r := range rules {
		if r.Field == field && r.From != "" && strings.HasPrefix(v, r.From) {
			return r.To + v[len(r.From):]
		}
	}
	return v
}

// RewriteKeys returns rows with every rule applied to the original value (the first matching rule per field wins),
// so one pass is deterministic, and idempotent whenever no target value starts with a source value. Rows are not
// modified; non-string values and unmatched values pass through unchanged.
func RewriteKeys(rows []Record, rules []KeyRewrite) []Record {
	out := make([]Record, len(rows))
	for i, row := range rows {
		fields := make(map[string]any, len(row.Fields))
		for k, v := range row.Fields {
			if s, ok := v.(string); ok {
				v = rewriteValue(s, k, rules)
			}
			fields[k] = v
		}
		out[i] = Record{Key: rewriteValue(row.Key, KeyField, rules), Fields: fields}
	}
	return out
}

type LegalEntityRef struct{ TenantID, ID string }

type leKey struct{}

// WithLegalEntity tags a context with the legal entity an extractor reads from or a loader writes to.
func WithLegalEntity(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, leKey{}, id)
}

func LegalEntityFrom(ctx context.Context) string {
	id, _ := ctx.Value(leKey{}).(string)
	return id
}

type CopySpec struct {
	Project string
	Entity  string
	Source  LegalEntityRef
	Target  LegalEntityRef
	Rules   []KeyRewrite
	// Upsert lets the copy overwrite a rewritten key that already exists in the target and was not created by this project.
	Upsert bool
	DryRun bool
}

type CopyRun struct {
	Extractor Extractor
	Loader    TargetLoader
	Reader    TargetReader
	Xref      XrefStore
	IDGen     func() string
}

func (s CopySpec) validate() error {
	switch {
	case s.Project == "" || s.Entity == "" || s.Source.ID == "" || s.Target.ID == "" || s.Source.TenantID == "" || s.Target.TenantID == "":
		return &Error{CodeCopyInvalid, "project, entity, source and target legal entities and their tenants are required"}
	case s.Source.TenantID != s.Target.TenantID:
		return &Error{CodeCopyInvalid, "source and target legal entities must belong to the same tenant"}
	case s.Source.ID == s.Target.ID:
		return &Error{CodeCopyInvalid, "source and target legal entities must differ"}
	}
	return nil
}

type copyExtractor struct {
	inner    Extractor
	spec     CopySpec
	reader   TargetReader
	xref     XrefStore
	rejected []RowReject
}

func (c *copyExtractor) Extract(ctx context.Context, entity string) ([]Record, error) {
	rows, err := c.inner.Extract(WithLegalEntity(ctx, c.spec.Source.ID), entity)
	if err != nil {
		return nil, err
	}
	rows = RewriteKeys(rows, c.spec.Rules)
	if c.spec.Upsert {
		return rows, nil
	}
	existing, err := c.reader.Read(WithLegalEntity(ctx, c.spec.Target.ID), entity)
	if err != nil {
		return nil, fmt.Errorf("read target %s: %w", entity, err)
	}
	inTarget := make(map[string]bool, len(existing))
	for _, e := range existing {
		inTarget[e.Key] = true
	}
	var clash []string
	for _, r := range rows {
		if inTarget[r.Key] {
			clash = append(clash, r.Key)
		}
	}
	created := map[string]XrefEntry{}
	for start := 0; start < len(clash); start += MaxBatch {
		got, err := c.xref.GetMany(ctx, c.spec.Project, entity, clash[start:min(start+MaxBatch, len(clash))])
		if err != nil {
			return nil, fmt.Errorf("xref get %s: %w", entity, err)
		}
		for k, v := range got {
			created[k] = v
		}
	}
	kept := rows[:0:0]
	for _, r := range rows {
		if _, ours := created[r.Key]; inTarget[r.Key] && !ours {
			c.rejected = append(c.rejected, RowReject{r.Key, []Reject{{"", CodeCopyKeyExists, "rewritten key already exists in the target legal entity"}}})
			continue
		}
		kept = append(kept, r)
	}
	return kept, nil
}

type targetLoader struct {
	inner TargetLoader
	le    string
}

func (t targetLoader) LoadBatch(ctx context.Context, entity string, items []LoadItem) ([]LoadResult, error) {
	return t.inner.LoadBatch(WithLegalEntity(ctx, t.le), entity, items)
}

type targetReader struct {
	inner TargetReader
	le    string
}

func (t targetReader) Read(ctx context.Context, entity string) ([]Record, error) {
	return t.inner.Read(WithLegalEntity(ctx, t.le), entity)
}

// Run copies one entity's rows from the source to the target legal entity through the migration pipeline. The
// project id is the xref source system, so a second run over unchanged source rows skips every row.
func (c *CopyRun) Run(ctx context.Context, spec CopySpec) (Report, error) {
	if err := spec.validate(); err != nil {
		return Report{Status: StatusFailed}, err
	}
	if c.Reader == nil && !spec.Upsert {
		// without a target read, existing keys cannot be detected and BR-10.13 could not hold
		return Report{Status: StatusFailed}, &Error{CodeCopyInvalid, "a copy needs a target reader to detect existing keys unless upsert is selected"}
	}
	ex := &copyExtractor{inner: c.Extractor, spec: spec, reader: c.Reader, xref: c.Xref}
	p := Pipeline{Extractor: ex, Loader: targetLoader{c.Loader, spec.Target.ID}, Xref: c.Xref, IDGen: c.IDGen}
	if c.Reader != nil {
		p.Reader = targetReader{c.Reader, spec.Target.ID}
	}
	rep, err := p.Run(ctx, RunSpec{SourceSystem: spec.Project, Entity: spec.Entity, DryRun: spec.DryRun, Passthrough: true})
	if err != nil {
		return rep, err
	}
	if len(ex.rejected) > 0 {
		rep.Rejects = append(rep.Rejects, ex.rejected...)
		rep.Extracted += len(ex.rejected)
		if rep.Status == StatusSucceeded {
			rep.Status = StatusPartial
		}
	}
	return rep, nil
}
