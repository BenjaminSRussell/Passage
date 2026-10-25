package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const (
	CodeLoadFailed  = "MIG-LOAD-FAILED"
	CodeDuplicate   = "MIG-ROW-DUPLICATE-KEY"
	MaxBatch        = 1000
	StatusSucceeded = "succeeded"
	StatusPartial   = "partial"
	StatusFailed    = "failed"
	ActionCreated   = "created"
	ActionUpdated   = "updated"
	ActionUnchanged = "unchanged"
	reconcileSample = 100
	reconcileSeed   = 1
)

type Record struct {
	Key    string
	Fields map[string]any
}

type LoadItem struct {
	TargetID       string
	SourceKey      string
	IdempotencyKey string
	Fields         map[string]any
}

type LoadResult struct{ Code string }

type Extractor interface {
	Extract(ctx context.Context, entity string) ([]Record, error)
}

type TargetLoader interface {
	LoadBatch(ctx context.Context, entity string, items []LoadItem) ([]LoadResult, error)
}

type TargetReader interface {
	Read(ctx context.Context, entity string) ([]Record, error)
}

type XrefKey struct{ SourceSystem, Entity, SourceKey string }

type XrefEntry struct{ TargetID, ContentHash string }

type XrefStore interface {
	Get(ctx context.Context, k XrefKey) (XrefEntry, bool, error)
	Put(ctx context.Context, k XrefKey, e XrefEntry) error
	Keys(ctx context.Context, sourceSystem, entity string) ([]string, error)
}

type RunSpec struct {
	SourceSystem string
	Entity       string
	Mapping      Mapping
	DryRun       bool
	OnlyKeys     map[string]bool
}

type RowReject struct {
	SourceKey string   `json:"sourceKey"`
	Rejects   []Reject `json:"rejects"`
}

type LedgerEntry struct {
	SourceKey      string
	TargetID       string
	IdempotencyKey string
	ContentHash    string
	Action         string
}

type Report struct {
	Status         string           `json:"status"`
	DryRun         bool             `json:"dryRun"`
	Extracted      int              `json:"extracted"`
	Mapped         int              `json:"mapped"`
	Created        int              `json:"created"`
	Updated        int              `json:"updated"`
	Unchanged      int              `json:"unchanged"`
	Rejects        []RowReject      `json:"rejects"`
	Ledger         []LedgerEntry    `json:"-"`
	Absent         []string         `json:"absent"`
	Reconciliation *ReconcileReport `json:"reconciliation,omitempty"`
}

type Pipeline struct {
	Extractor Extractor
	Loader    TargetLoader
	Reader    TargetReader
	Xref      XrefStore
	IDGen     func() string
}

type pending struct {
	entry  LedgerEntry
	item   LoadItem
	key    XrefKey
	action string
}

func ContentHash(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%T:%v\x00", k, fields[k], fields[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func IdempotencyKey(sourceSystem, entity, sourceKey, contentHash string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{sourceSystem, entity, sourceKey, contentHash}, "|")))
	return hex.EncodeToString(sum[:])
}

func (p *Pipeline) Run(ctx context.Context, spec RunSpec) (Report, error) {
	rep := Report{DryRun: spec.DryRun, Status: StatusFailed}
	rows, err := p.Extractor.Extract(ctx, spec.Entity)
	if err != nil {
		return rep, fmt.Errorf("extract %s: %w", spec.Entity, err)
	}
	rep.Extracted = len(rows)
	seen := make(map[string]bool, len(rows))
	var expected []Record
	var todo []pending
	for _, r := range rows {
		if spec.OnlyKeys != nil && !spec.OnlyKeys[r.Key] {
			continue
		}
		if seen[r.Key] {
			rep.Rejects = append(rep.Rejects, RowReject{r.Key, []Reject{{"", CodeDuplicate, "source key repeated in extract"}}})
			continue
		}
		seen[r.Key] = true
		out, rejects := spec.Mapping.Apply(r.Fields)
		if len(rejects) > 0 {
			rep.Rejects = append(rep.Rejects, RowReject{r.Key, rejects})
			continue
		}
		rep.Mapped++
		expected = append(expected, Record{r.Key, out})
		hash := ContentHash(out)
		xk := XrefKey{spec.SourceSystem, spec.Entity, r.Key}
		x, found, err := p.Xref.Get(ctx, xk)
		if err != nil {
			return rep, fmt.Errorf("xref get %s: %w", spec.Entity, err)
		}
		idemKey := IdempotencyKey(spec.SourceSystem, spec.Entity, r.Key, hash)
		if found && x.ContentHash == hash {
			rep.Unchanged++
			rep.Ledger = append(rep.Ledger, LedgerEntry{r.Key, x.TargetID, idemKey, hash, ActionUnchanged})
			continue
		}
		action := ActionUpdated
		if !found || x.ContentHash == "" {
			action = ActionCreated
		}
		if !found && !spec.DryRun {
			x.TargetID = p.IDGen()
			if err := p.Xref.Put(ctx, xk, XrefEntry{TargetID: x.TargetID}); err != nil {
				return rep, fmt.Errorf("xref put %s: %w", spec.Entity, err)
			}
		}
		entry := LedgerEntry{r.Key, x.TargetID, idemKey, hash, action}
		if spec.DryRun {
			rep.count(action)
			rep.Ledger = append(rep.Ledger, entry)
			continue
		}
		todo = append(todo, pending{entry, LoadItem{x.TargetID, r.Key, idemKey, out}, xk, action})
	}
	failed, err := p.load(ctx, spec, &rep, todo)
	if err != nil {
		return rep, err
	}
	if spec.OnlyKeys == nil {
		if rep.Absent, err = p.absent(ctx, spec, seen); err != nil {
			return rep, err
		}
	}
	if p.Reader != nil && !spec.DryRun && spec.OnlyKeys == nil {
		target, err := p.Reader.Read(ctx, spec.Entity)
		if err != nil {
			return rep, fmt.Errorf("read target %s: %w", spec.Entity, err)
		}
		rec := Reconcile(expected, target, reconcileSample, reconcileSeed)
		rep.Reconciliation = &rec
		failed = failed || !rec.OK()
	}
	switch {
	case failed:
		rep.Status = StatusFailed
	case len(rep.Rejects) > 0:
		rep.Status = StatusPartial
	default:
		rep.Status = StatusSucceeded
	}
	return rep, nil
}

func (p *Pipeline) Rerun(ctx context.Context, spec RunSpec, prev Report) (Report, error) {
	spec.OnlyKeys = make(map[string]bool, len(prev.Rejects))
	for _, r := range prev.Rejects {
		spec.OnlyKeys[r.SourceKey] = true
	}
	return p.Run(ctx, spec)
}

func (r *Report) count(action string) {
	if action == ActionCreated {
		r.Created++
	} else {
		r.Updated++
	}
}

func (p *Pipeline) load(ctx context.Context, spec RunSpec, rep *Report, todo []pending) (bool, error) {
	failed := false
	for start := 0; start < len(todo); start += MaxBatch {
		if err := ctx.Err(); err != nil {
			return failed, err
		}
		batch := todo[start:min(start+MaxBatch, len(todo))]
		items := make([]LoadItem, len(batch))
		for i, b := range batch {
			items[i] = b.item
		}
		results, err := p.Loader.LoadBatch(ctx, spec.Entity, items)
		if err != nil || len(results) != len(batch) {
			failed = true
			results = make([]LoadResult, len(batch))
			for i := range results {
				results[i].Code = CodeLoadFailed
			}
		}
		for i, b := range batch {
			if code := results[i].Code; code != "" {
				rep.Rejects = append(rep.Rejects, RowReject{b.entry.SourceKey, []Reject{{"", code, "target did not accept the row"}}})
				continue
			}
			if err := p.Xref.Put(ctx, b.key, XrefEntry{b.entry.TargetID, b.entry.ContentHash}); err != nil {
				return failed, fmt.Errorf("xref put %s: %w", spec.Entity, err)
			}
			rep.count(b.action)
			rep.Ledger = append(rep.Ledger, b.entry)
		}
	}
	return failed, nil
}

func (p *Pipeline) absent(ctx context.Context, spec RunSpec, seen map[string]bool) ([]string, error) {
	keys, err := p.Xref.Keys(ctx, spec.SourceSystem, spec.Entity)
	if err != nil {
		return nil, fmt.Errorf("xref keys %s: %w", spec.Entity, err)
	}
	var gone []string
	for _, k := range keys {
		if !seen[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	return gone, nil
}

type MemXref struct {
	Entries map[XrefKey]XrefEntry
	Puts    int
}

func NewMemXref() *MemXref { return &MemXref{Entries: map[XrefKey]XrefEntry{}} }

func (m *MemXref) Get(_ context.Context, k XrefKey) (XrefEntry, bool, error) {
	e, ok := m.Entries[k]
	return e, ok, nil
}

func (m *MemXref) Put(_ context.Context, k XrefKey, e XrefEntry) error {
	m.Puts++
	m.Entries[k] = e
	return nil
}

func (m *MemXref) Keys(_ context.Context, sourceSystem, entity string) ([]string, error) {
	var keys []string
	for k := range m.Entries {
		if k.SourceSystem == sourceSystem && k.Entity == entity {
			keys = append(keys, k.SourceKey)
		}
	}
	return keys, nil
}

type MemLoader struct {
	Rows       map[string]LoadItem
	Calls      int
	Err        error
	RejectKeys map[string]string
}

func NewMemLoader() *MemLoader { return &MemLoader{Rows: map[string]LoadItem{}} }

func (m *MemLoader) LoadBatch(_ context.Context, _ string, items []LoadItem) ([]LoadResult, error) {
	m.Calls++
	if m.Err != nil {
		return nil, m.Err
	}
	results := make([]LoadResult, len(items))
	for i, it := range items {
		if code := m.RejectKeys[it.SourceKey]; code != "" {
			results[i].Code = code
			continue
		}
		m.Rows[it.TargetID] = it
	}
	return results, nil
}

func (m *MemLoader) Read(_ context.Context, _ string) ([]Record, error) {
	out := make([]Record, 0, len(m.Rows))
	for _, it := range m.Rows {
		out = append(out, Record{it.SourceKey, it.Fields})
	}
	return out, nil
}
