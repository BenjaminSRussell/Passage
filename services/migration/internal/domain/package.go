package domain

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// Configuration data package codes (FR-10.15, BR-10.14).
const (
	CodePkgSignature   = "MIG-PKG-SIGNATURE"
	CodePkgSchemaNewer = "MIG-PKG-SCHEMA-NEWER"
	CodePkgInvalid     = "MIG-PKG-INVALID"
)

// SupportedPackageSchema is the newest package schema this service can apply.
const SupportedPackageSchema = 1

type PackageRecord struct {
	Key  string          `json:"key"`
	Body json.RawMessage `json:"body"`
}

type PackageFile struct {
	Name    string          `json:"name"`
	Entity  string          `json:"entity"`
	Records []PackageRecord `json:"records"`
}

type ManifestFile struct {
	Name      string   `json:"name"`
	SHA256    string   `json:"sha256"`
	DependsOn []string `json:"dependsOn"`
}

type Manifest struct {
	Files []ManifestFile `json:"files"`
}

type Package struct {
	Version       string        `json:"version"`
	SchemaVersion int           `json:"schemaVersion"`
	Manifest      Manifest      `json:"manifest"`
	Files         []PackageFile `json:"files"`
}

// Signer signs and checks the canonical manifest. The tenant key from Vault is a handoff; HMACSigner serves tests and dev.
type Signer interface {
	Sign(payload []byte) []byte
	Valid(payload, signature []byte) bool
}

type HMACSigner struct{ Key []byte }

func (s HMACSigner) Sign(payload []byte) []byte {
	m := hmac.New(sha256.New, s.Key)
	m.Write(payload)
	return m.Sum(nil)
}

func (s HMACSigner) Valid(payload, signature []byte) bool {
	return hmac.Equal(s.Sign(payload), signature)
}

// canonicalValue re-encodes any JSON value with sorted object keys and numbers normalised to their shortest decimal form,
// so key order, whitespace and 1 versus 1.0 never change a hash.
func canonicalValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = canonicalValue(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = canonicalValue(x)
		}
		return t
	case json.Number:
		if d, err := decimal.NewFromString(t.String()); err == nil {
			return json.Number(d.String())
		}
		return t
	}
	return v
}

// CanonicalJSON is the canonical encoding used for package file hashes, manifests and record comparison.
func CanonicalJSON(raw []byte) ([]byte, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return json.Marshal(canonicalValue(v))
}

func canonicalFields(fields map[string]any) ([]byte, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return CanonicalJSON(raw)
}

func hashFields(fields map[string]any) (string, error) {
	b, err := canonicalFields(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func fileHash(f PackageFile) (string, error) {
	records := make([]any, len(f.Records))
	for i, r := range f.Records {
		body := r.Body
		if len(body) == 0 {
			body = json.RawMessage("null")
		}
		records[i] = map[string]any{"key": r.Key, "body": body}
	}
	raw, err := json.Marshal(map[string]any{"name": f.Name, "entity": f.Entity, "records": records})
	if err != nil {
		return "", err
	}
	b, err := CanonicalJSON(raw)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func invalid(format string, args ...any) *Error {
	return &Error{CodePkgInvalid, fmt.Sprintf(format, args...)}
}

// signedPayload covers the version, schema version and manifest, so a package cannot be re-labelled as an older schema.
func signedPayload(p Package) ([]byte, error) {
	files := p.Manifest.Files
	if files == nil {
		files = []ManifestFile{}
	}
	out := make([]any, len(files))
	for i, f := range files {
		deps := f.DependsOn
		if deps == nil {
			deps = []string{}
		}
		out[i] = map[string]any{"name": f.Name, "sha256": f.SHA256, "dependsOn": deps}
	}
	raw, err := json.Marshal(map[string]any{"version": p.Version, "schemaVersion": p.SchemaVersion, "files": out})
	if err != nil {
		return nil, err
	}
	return CanonicalJSON(raw)
}

// Seal fills the manifest sha256 of every listed file from the package files. The manifest keeps the names and
// dependsOn the caller listed; used when exporting a package and in tests.
func Seal(p Package) (Package, error) {
	byName := map[string]PackageFile{}
	for _, f := range p.Files {
		byName[f.Name] = f
	}
	files := make([]ManifestFile, len(p.Manifest.Files))
	for i, m := range p.Manifest.Files {
		f, ok := byName[m.Name]
		if !ok {
			return p, invalid("manifest lists unknown file %q", m.Name)
		}
		h, err := fileHash(f)
		if err != nil {
			return p, invalid("file %q is not valid JSON", m.Name)
		}
		m.SHA256 = h
		files[i] = m
	}
	p.Manifest = Manifest{Files: files}
	return p, nil
}

// Sign signs the canonical manifest.
func Sign(p Package, s Signer) ([]byte, error) {
	payload, err := signedPayload(p)
	if err != nil {
		return nil, invalid("manifest is not valid JSON")
	}
	return s.Sign(payload), nil
}

// ApplyOrder validates the manifest structure and returns file names in dependency order (stable on manifest order).
func ApplyOrder(p Package) ([]string, error) {
	listed := map[string]ManifestFile{}
	for _, m := range p.Manifest.Files {
		if _, dup := listed[m.Name]; dup {
			return nil, invalid("manifest lists file %q twice", m.Name)
		}
		listed[m.Name] = m
	}
	have := map[string]bool{}
	for _, f := range p.Files {
		if have[f.Name] {
			return nil, invalid("package holds file %q twice", f.Name)
		}
		have[f.Name] = true
		if _, ok := listed[f.Name]; !ok {
			return nil, invalid("file %q is not listed in the manifest", f.Name)
		}
		seen := map[string]bool{}
		for _, r := range f.Records {
			if r.Key == "" || seen[r.Key] {
				return nil, invalid("file %q has an empty or repeated record key", f.Name)
			}
			seen[r.Key] = true
		}
	}
	for _, m := range p.Manifest.Files {
		if !have[m.Name] {
			return nil, invalid("manifest lists unknown file %q", m.Name)
		}
		for _, d := range m.DependsOn {
			if _, ok := listed[d]; !ok {
				return nil, invalid("file %q depends on unknown file %q", m.Name, d)
			}
		}
	}
	done := map[string]bool{}
	order := make([]string, 0, len(listed))
	for len(order) < len(listed) {
		progressed := false
		for _, m := range p.Manifest.Files {
			if done[m.Name] {
				continue
			}
			ready := true
			for _, d := range m.DependsOn {
				ready = ready && done[d]
			}
			if ready {
				done[m.Name] = true
				order = append(order, m.Name)
				progressed = true
			}
		}
		if !progressed {
			return nil, invalid("manifest dependencies are cyclic")
		}
	}
	return order, nil
}

// Verify refuses a package whose signature or any file hash does not match (MIG-PKG-SIGNATURE) or whose
// manifest is malformed (MIG-PKG-INVALID). Messages never include record bodies.
func Verify(p Package, signature []byte, s Signer) error {
	payload, err := signedPayload(p)
	if err != nil || !s.Valid(payload, signature) {
		return &Error{CodePkgSignature, "package signature does not match its manifest"}
	}
	if _, err := ApplyOrder(p); err != nil {
		return err
	}
	byName := map[string]PackageFile{}
	for _, f := range p.Files {
		byName[f.Name] = f
	}
	for _, m := range p.Manifest.Files {
		h, err := fileHash(byName[m.Name])
		if err != nil || h != m.SHA256 {
			return &Error{CodePkgSignature, fmt.Sprintf("file %q does not match its manifest sha256", m.Name)}
		}
	}
	return nil
}

// CheckSchema refuses a package written for a newer schema than the target supports.
func CheckSchema(p Package, supported int) error {
	if p.SchemaVersion > supported {
		return &Error{CodePkgSchemaNewer, fmt.Sprintf("package schema %d is newer than the supported schema %d", p.SchemaVersion, supported)}
	}
	if p.SchemaVersion < 1 {
		return invalid("schemaVersion must be at least 1")
	}
	return nil
}

// PackageTarget is the setup data a package is applied to.
type PackageTarget interface {
	Read(ctx context.Context, entity string) ([]Record, error)
	Upsert(ctx context.Context, entity string, records []Record) error
}

type FileDiff struct {
	Name      string   `json:"name"`
	Entity    string   `json:"entity"`
	Create    []string `json:"create"`
	Update    []string `json:"update"`
	Unchanged []string `json:"unchanged"`
}

type DiffResult struct {
	Files     []FileDiff `json:"files"`
	Create    int        `json:"create"`
	Update    int        `json:"update"`
	Unchanged int        `json:"unchanged"`
}

func (d DiffResult) Changes() int { return d.Create + d.Update }

func decodeBody(f PackageFile, r PackageRecord) (map[string]any, error) {
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(r.Body))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, invalid("record %q in file %q is not a JSON object", r.Key, f.Name)
	}
	return fields, nil
}

func diffFile(ctx context.Context, f PackageFile, target PackageTarget) (FileDiff, []Record, error) {
	d := FileDiff{Name: f.Name, Entity: f.Entity, Create: []string{}, Update: []string{}, Unchanged: []string{}}
	existing, err := target.Read(ctx, f.Entity)
	if err != nil {
		return d, nil, fmt.Errorf("read target %s: %w", f.Entity, err)
	}
	have := make(map[string]string, len(existing))
	for _, e := range existing {
		h, err := hashFields(e.Fields)
		if err != nil {
			return d, nil, fmt.Errorf("hash target %s record: %w", f.Entity, err)
		}
		have[e.Key] = h
	}
	var write []Record
	for _, r := range f.Records {
		fields, err := decodeBody(f, r)
		if err != nil {
			return d, nil, err
		}
		h, err := hashFields(fields)
		if err != nil {
			return d, nil, invalid("record %q in file %q cannot be encoded", r.Key, f.Name)
		}
		switch cur, ok := have[r.Key]; {
		case !ok:
			d.Create = append(d.Create, r.Key)
			write = append(write, Record{Key: r.Key, Fields: fields})
		case cur != h:
			d.Update = append(d.Update, r.Key)
			write = append(write, Record{Key: r.Key, Fields: fields})
		default:
			d.Unchanged = append(d.Unchanged, r.Key)
		}
	}
	return d, write, nil
}

type ApplyOptions struct {
	DryRun          bool
	SupportedSchema int
}

// Diff verifies the package and reports, per file in dependency order, which records Apply would create or update.
func Diff(ctx context.Context, p Package, signature []byte, s Signer, target PackageTarget, supported int) (DiffResult, error) {
	return run(ctx, p, signature, s, target, ApplyOptions{DryRun: true, SupportedSchema: supported})
}

// Apply verifies the package, then writes only created and updated records in manifest dependency order. Target
// records absent from the package are never deleted, and a second apply reports no changes.
func Apply(ctx context.Context, p Package, signature []byte, s Signer, target PackageTarget, o ApplyOptions) (DiffResult, error) {
	return run(ctx, p, signature, s, target, o)
}

func run(ctx context.Context, p Package, signature []byte, s Signer, target PackageTarget, o ApplyOptions) (DiffResult, error) {
	if err := Verify(p, signature, s); err != nil {
		return DiffResult{}, err
	}
	supported := o.SupportedSchema
	if supported == 0 {
		supported = SupportedPackageSchema
	}
	if err := CheckSchema(p, supported); err != nil {
		return DiffResult{}, err
	}
	order, err := ApplyOrder(p)
	if err != nil {
		return DiffResult{}, err
	}
	byName := map[string]PackageFile{}
	for _, f := range p.Files {
		byName[f.Name] = f
	}
	res := DiffResult{Files: []FileDiff{}}
	for _, name := range order {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		d, write, err := diffFile(ctx, byName[name], target)
		if err != nil {
			return res, err
		}
		if !o.DryRun && len(write) > 0 {
			if err := target.Upsert(ctx, d.Entity, write); err != nil {
				return res, fmt.Errorf("apply %s: %w", d.Entity, err)
			}
		}
		res.Files = append(res.Files, d)
		res.Create += len(d.Create)
		res.Update += len(d.Update)
		res.Unchanged += len(d.Unchanged)
	}
	return res, nil
}

// MemPackageTarget is the in-memory PackageTarget used by tests and by deployments without a setup backend.
type MemPackageTarget struct {
	Data    map[string]map[string]map[string]any
	Upserts []string
}

func NewMemPackageTarget() *MemPackageTarget {
	return &MemPackageTarget{Data: map[string]map[string]map[string]any{}}
}

func (m *MemPackageTarget) Read(_ context.Context, entity string) ([]Record, error) {
	keys := make([]string, 0, len(m.Data[entity]))
	for k := range m.Data[entity] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Record, len(keys))
	for i, k := range keys {
		out[i] = Record{Key: k, Fields: m.Data[entity][k]}
	}
	return out, nil
}

func (m *MemPackageTarget) Upsert(_ context.Context, entity string, records []Record) error {
	m.Upserts = append(m.Upserts, entity)
	if m.Data[entity] == nil {
		m.Data[entity] = map[string]map[string]any{}
	}
	for _, r := range records {
		m.Data[entity][r.Key] = r.Fields
	}
	return nil
}
