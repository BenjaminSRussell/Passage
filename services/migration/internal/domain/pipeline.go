package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
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
	// Existing is true when an earlier run already loaded this key (the cross-reference holds its content hash), so
	// the target record is updated instead of created. TargetVersion is the version the target answered then.
	Existing      bool
	TargetVersion int
}

// LoadResult is the target's answer for one row. Code is "" when the row was accepted; otherwise it is the target's
// refusal code and Detail says why. A target that assigns its own ids returns TargetID (and the version of the record
// it holds in TargetVersion) so the cross-reference records the real record, not the id the pipeline reserved.
type LoadResult struct {
	Code          string
	Detail        string
	TargetID      string
	TargetVersion int
}

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

type XrefEntry struct {
	TargetID, ContentHash string
	// TargetVersion is the version of the target record after the last load (0 when the target keeps none).
	TargetVersion int
	// Rebind, on a put, replaces the stored target id with TargetID. It is set when the target assigned its own id
	// to a record the pipeline had reserved another for; every other put keeps the id first reserved.
	Rebind bool
}

type XrefItem struct {
	SourceKey string
	Entry     XrefEntry
}

type XrefStore interface {
	Get(ctx context.Context, k XrefKey) (XrefEntry, bool, error)
	Put(ctx context.Context, k XrefKey, e XrefEntry) error
	GetMany(ctx context.Context, sourceSystem, entity string, sourceKeys []string) (map[string]XrefEntry, error)
	PutMany(ctx context.Context, sourceSystem, entity string, items []XrefItem) error
	Reserve(ctx context.Context, sourceSystem, entity string, items []XrefItem) (map[string]string, error)
	Keys(ctx context.Context, sourceSystem, entity string) ([]string, error)
}

type RunSpec struct {
	SourceSystem string
	Entity       string
	Mapping      Mapping
	DryRun       bool
	OnlyKeys     map[string]bool
	// Passthrough loads extracted fields unchanged (copy projects); Mapping is then ignored.
	Passthrough bool
	// Template supplies default values for absent source fields (FR-10.18); nil applies none.
	Template *DataTemplate
	// Since is the watermark of the last committed delta run (integration track S4): an extractor that can read a
	// delta returns only rows changed after it. "" reads everything, and so does an extractor without a cursor.
	Since string
	// HoldExisting is set when Passage is the source of truth of the binding: a row whose record an earlier run
	// loaded and whose mapped content now differs is reported as a conflict (MIG-TARGET-DRIFT) and not applied.
	HoldExisting bool
	// Acknowledged holds, per source key, the content hash of a legacy row an operator chose to leave out
	// ("keep Passage"); the same row is not reported again, a row that changed again is.
	Acknowledged map[string]string
}

// CodeSourceOfTruth is the reject code of a held row. It is the code the loader uses for a record changed in Passage
// since the last load, because to the operator both mean the same thing: Passage and the legacy system disagree.
const CodeSourceOfTruth = "MIG-TARGET-DRIFT"

// CodeTargetUnavailable is the loader's reject code for a target service that did not answer (retries exhausted).
const CodeTargetUnavailable = "MIG-TARGET-UNAVAILABLE"

// Conflict is a legacy row held back because Passage is the source of truth.
type Conflict struct {
	SourceKey   string
	TargetID    string
	ContentHash string
	Fields      map[string]any
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
	// TemplateFields are the targets filled from the data template (lineage default_from_template).
	TemplateFields []string
}

type Report struct {
	Status         string           `json:"status"`
	DryRun         bool             `json:"dryRun"`
	Extracted      int              `json:"extracted"`
	Mapped         int              `json:"mapped"`
	Created        int              `json:"created"`
	Updated        int              `json:"updated"`
	Unchanged      int              `json:"unchanged"`
	Templated      int              `json:"templated"`
	Rejects        []RowReject      `json:"rejects"`
	Warnings       []RowWarning     `json:"warnings"`
	Ledger         []LedgerEntry    `json:"-"`
	Absent         []string         `json:"absent"`
	Reconciliation *ReconcileReport `json:"reconciliation,omitempty"`
	// Watermark is the largest change cursor the extractor read ("" without a cursor); the sync scheduler stores it
	// only after the run ends without failing.
	Watermark string `json:"watermark,omitempty"`
	// Conflicted counts held rows (each also has a MIG-TARGET-DRIFT reject); Conflicts carries them for storage.
	Conflicted int `json:"conflicted,omitempty"`
	// Transient counts rows refused for a reason a later run can cure (a target service that did not answer); the
	// rows were not loaded, so a delta run that has any must not move its watermark.
	Transient int        `json:"transient,omitempty"`
	Conflicts []Conflict `json:"-"`
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
	key    string
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
	var rows []Record
	var err error
	if d, ok := p.Extractor.(DeltaExtractor); ok {
		rows, rep.Watermark, err = d.ExtractSince(ctx, spec.Entity, spec.Since)
	} else {
		rows, err = p.Extractor.Extract(ctx, spec.Entity)
	}
	if err != nil {
		return rep, fmt.Errorf("extract %s: %w", spec.Entity, err)
	}
	rep.Extracted = len(rows)
	seen := make(map[string]bool, len(rows))
	var mapped []Record
	templated := map[string][]string{}
	conflicting := conflictingKeys(rows, spec.OnlyKeys)
	for _, r := range rows {
		if spec.OnlyKeys != nil && !spec.OnlyKeys[r.Key] {
			continue
		}
		if conflicting[r.Key] {
			seen[r.Key] = true
			rep.Rejects = append(rep.Rejects, RowReject{r.Key, []Reject{{"", CodeDuplicate, "source key repeated in extract with different values"}}})
			continue
		}
		if seen[r.Key] {
			rep.Rejects = append(rep.Rejects, RowReject{r.Key, []Reject{{"", CodeDuplicate, "source key repeated in extract"}}})
			continue
		}
		seen[r.Key] = true
		out, warnings, rejects := r.Fields, []Warning(nil), []Reject(nil)
		var fromTemplate []string
		if !spec.Passthrough {
			out, warnings, rejects, fromTemplate = spec.Mapping.ApplyTemplate(r.Fields, spec.Template)
		}
		if len(rejects) > 0 {
			rep.Rejects = append(rep.Rejects, RowReject{r.Key, rejects})
			continue
		}
		if len(warnings) > 0 {
			rep.Warnings = append(rep.Warnings, RowWarning{r.Key, warnings})
		}
		rep.Mapped++
		if len(fromTemplate) > 0 {
			templated[r.Key] = fromTemplate
			rep.Templated++
		}
		mapped = append(mapped, Record{r.Key, out})
	}
	known, err := p.knownXrefs(ctx, spec, mapped)
	if err != nil {
		return rep, err
	}
	var todo []pending
	var reserve []XrefItem
	for _, r := range mapped {
		hash := ContentHash(r.Fields)
		x, found := known[r.Key]
		if spec.HoldExisting && found && x.ContentHash != "" && x.ContentHash != hash {
			if spec.Acknowledged[r.Key] == hash {
				rep.Unchanged++
				continue
			}
			rep.Conflicted++
			rep.Conflicts = append(rep.Conflicts, Conflict{SourceKey: r.Key, TargetID: x.TargetID, ContentHash: hash, Fields: r.Fields})
			rep.Rejects = append(rep.Rejects, RowReject{r.Key, []Reject{{"", CodeSourceOfTruth, "Passage is the source of truth for this binding; the legacy row differs from the loaded record and was not applied"}}})
			continue
		}
		idemKey := IdempotencyKey(spec.SourceSystem, spec.Entity, r.Key, hash)
		if found && x.ContentHash == hash {
			rep.Unchanged++
			rep.Ledger = append(rep.Ledger, LedgerEntry{r.Key, x.TargetID, idemKey, hash, ActionUnchanged, templated[r.Key]})
			continue
		}
		action := ActionUpdated
		if !found || x.ContentHash == "" {
			action = ActionCreated
		}
		if !found && !spec.DryRun {
			x.TargetID = p.IDGen()
			reserve = append(reserve, XrefItem{r.Key, XrefEntry{TargetID: x.TargetID}})
		}
		entry := LedgerEntry{r.Key, x.TargetID, idemKey, hash, action, templated[r.Key]}
		if spec.DryRun {
			rep.count(action)
			rep.Ledger = append(rep.Ledger, entry)
			continue
		}
		item := LoadItem{TargetID: x.TargetID, SourceKey: r.Key, IdempotencyKey: idemKey, Fields: r.Fields, Existing: found && x.ContentHash != "", TargetVersion: x.TargetVersion}
		todo = append(todo, pending{entry, item, r.Key, action})
	}
	won, err := p.reserveXrefs(ctx, spec, reserve)
	if err != nil {
		return rep, err
	}
	for i := range todo {
		if id, ok := won[todo[i].key]; ok {
			todo[i].item.TargetID, todo[i].entry.TargetID = id, id
		}
	}
	failed, err := p.load(ctx, spec, &rep, todo)
	if err != nil {
		return rep, err
	}
	if spec.OnlyKeys == nil && spec.Since == "" {
		if rep.Absent, err = p.absent(ctx, spec, seen); err != nil {
			return rep, err
		}
	}
	// A delta or a run that holds rows back covers only part of the source, so it is not tied out to the target.
	if p.Reader != nil && !spec.DryRun && spec.OnlyKeys == nil && spec.Since == "" && !spec.HoldExisting {
		target, err := p.Reader.Read(ctx, spec.Entity)
		if err != nil {
			return rep, fmt.Errorf("read target %s: %w", spec.Entity, err)
		}
		rec := Reconcile(mapped, onlyKeys(target, mapped), reconcileSample, reconcileSeed)
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

// conflictingKeys returns the source keys that repeat in the extract with different field values. No occurrence of
// such a key loads: keeping the first would make the target depend on extract order, so re-running the same rows in
// another order would change it. Exact repeats still load once and only the extra occurrences are rejected.
func conflictingKeys(rows []Record, only map[string]bool) map[string]bool {
	count := make(map[string]int, len(rows))
	for _, r := range rows {
		if only == nil || only[r.Key] {
			count[r.Key]++
		}
	}
	first := map[string]string{}
	conflicting := map[string]bool{}
	for _, r := range rows {
		if count[r.Key] < 2 {
			continue
		}
		h := ContentHash(r.Fields)
		if prev, ok := first[r.Key]; !ok {
			first[r.Key] = h
		} else if prev != h {
			conflicting[r.Key] = true
		}
	}
	return conflicting
}

func (p *Pipeline) knownXrefs(ctx context.Context, spec RunSpec, rows []Record) (map[string]XrefEntry, error) {
	known := make(map[string]XrefEntry, len(rows))
	for start := 0; start < len(rows); start += MaxBatch {
		batch := rows[start:min(start+MaxBatch, len(rows))]
		keys := make([]string, len(batch))
		for i, r := range batch {
			keys[i] = r.Key
		}
		got, err := p.Xref.GetMany(ctx, spec.SourceSystem, spec.Entity, keys)
		if err != nil {
			return nil, fmt.Errorf("xref get %s: %w", spec.Entity, err)
		}
		for k, e := range got {
			known[k] = e
		}
	}
	return known, nil
}

func (p *Pipeline) putXrefs(ctx context.Context, spec RunSpec, items []XrefItem) error {
	for start := 0; start < len(items); start += MaxBatch {
		if err := p.Xref.PutMany(ctx, spec.SourceSystem, spec.Entity, items[start:min(start+MaxBatch, len(items))]); err != nil {
			return fmt.Errorf("xref put %s: %w", spec.Entity, err)
		}
	}
	return nil
}

func (p *Pipeline) reserveXrefs(ctx context.Context, spec RunSpec, items []XrefItem) (map[string]string, error) {
	won := make(map[string]string, len(items))
	for start := 0; start < len(items); start += MaxBatch {
		got, err := p.Xref.Reserve(ctx, spec.SourceSystem, spec.Entity, items[start:min(start+MaxBatch, len(items))])
		if err != nil {
			return nil, fmt.Errorf("xref reserve %s: %w", spec.Entity, err)
		}
		for k, id := range got {
			won[k] = id
		}
	}
	return won, nil
}

func onlyKeys(target, expected []Record) []Record {
	want := make(map[string]bool, len(expected))
	for _, r := range expected {
		want[r.Key] = true
	}
	scoped := make([]Record, 0, len(expected))
	for _, r := range target {
		if want[r.Key] {
			scoped = append(scoped, r)
		}
	}
	return scoped
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
			slog.ErrorContext(ctx, "target load batch failed", "err", err, "entity", spec.Entity, "sourceSystem", spec.SourceSystem,
				"batchStart", start, "batchSize", len(batch), "resultCount", len(results), "resultCountMismatch", err == nil)
			failed = true
			results = make([]LoadResult, len(batch))
			for i := range results {
				results[i].Code = CodeLoadFailed
			}
		}
		var loaded []XrefItem
		for i, b := range batch {
			if code := results[i].Code; code != "" {
				detail := results[i].Detail
				if detail == "" {
					detail = "target did not accept the row"
				}
				rep.Rejects = append(rep.Rejects, RowReject{b.entry.SourceKey, []Reject{{"", code, detail}}})
				if code == CodeLoadFailed || code == CodeTargetUnavailable {
					rep.Transient++
				}
				continue
			}
			rebind := false
			if id := results[i].TargetID; id != "" {
				rebind, b.entry.TargetID = id != b.entry.TargetID, id
			}
			loaded = append(loaded, XrefItem{b.key, XrefEntry{TargetID: b.entry.TargetID, ContentHash: b.entry.ContentHash, TargetVersion: results[i].TargetVersion, Rebind: rebind}})
			rep.count(b.action)
			rep.Ledger = append(rep.Ledger, b.entry)
		}
		if err := p.putXrefs(ctx, spec, loaded); err != nil {
			return failed, err
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
	e.Rebind = false
	m.Entries[k] = e
	return nil
}

func (m *MemXref) GetMany(_ context.Context, sourceSystem, entity string, sourceKeys []string) (map[string]XrefEntry, error) {
	out := make(map[string]XrefEntry, len(sourceKeys))
	for _, k := range sourceKeys {
		if e, ok := m.Entries[XrefKey{sourceSystem, entity, k}]; ok {
			out[k] = e
		}
	}
	return out, nil
}

func (m *MemXref) PutMany(ctx context.Context, sourceSystem, entity string, items []XrefItem) error {
	for _, it := range items {
		if err := m.Put(ctx, XrefKey{sourceSystem, entity, it.SourceKey}, it.Entry); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemXref) Reserve(_ context.Context, sourceSystem, entity string, items []XrefItem) (map[string]string, error) {
	won := make(map[string]string, len(items))
	for _, it := range items {
		k := XrefKey{sourceSystem, entity, it.SourceKey}
		e, ok := m.Entries[k]
		if !ok {
			m.Puts++
			e = it.Entry
			m.Entries[k] = e
		}
		won[it.SourceKey] = e.TargetID
	}
	return won, nil
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
