package domain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Outbound mappings (integration track S5): the mapping DSL of the inbound pipeline, read in the other direction. The
// "row" a mapping reads is the Passage CloudEvent (id, type, subject, time, data.*, and any extension attribute), plus
// the legacy keys the mapping's xref lookups resolved, under "xref.<name>". The output is the request body the
// integration hub pushes to the legacy ERP.

const (
	CodeOutboundInvalid    = "MIG-OUTBOUND-INVALID"
	CodeOutboundEvent      = "MIG-OUTBOUND-EVENT-INVALID"
	CodeOutboundMismatch   = "MIG-OUTBOUND-EVENT-MISMATCH"
	CodeOutboundRejected   = "MIG-OUTBOUND-REJECTED"
	CodeXrefUnresolved     = "MIG-XREF-UNRESOLVED"
	maxOutboundXrefs       = 8
	maxEventBytes          = 1 << 20
	maxEventIDLength       = 256
	idempotencyKeyPrefix   = "mig-ob-v1-"
	selfXrefName           = "self"
	outboundMaxPatternSegs = 12
)

var (
	xrefNameRE   = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,39}$`)
	xrefEntityRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.\-]{0,63}$`)
	typeSegRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]*$`)
	eventPathRE  = regexp.MustCompile(`^[A-Za-z0-9_\-]+(\.[A-Za-z0-9_\-]+)*$`)
)

// OutboundSubject names the Passage record an event is about. Its legacy key, once the legacy ERP has assigned one, is
// recorded against it (xref:record) so a later event about the same record can update instead of create.
type OutboundSubject struct {
	Entity string `json:"entity"`
	IDPath string `json:"idPath"`
}

// OutboundXref resolves one Passage id carried by the event (a customer id, a product id) to the legacy key the S3 loads
// recorded. The key reaches the mapping as the row field xref.<Name>.
type OutboundXref struct {
	Name   string `json:"name"`
	Entity string `json:"entity"`
	IDPath string `json:"idPath"`
	// Optional lookups that find nothing leave xref.<Name> absent instead of failing the render.
	Optional bool `json:"optional,omitempty"`
}

// OutboundDefinition is one version of an outbound mapping.
type OutboundDefinition struct {
	// EventType is an exact CloudEvents type or a pattern in the hub's syntax: `*` stands for one dotted segment and a
	// trailing `**` for the rest ("dev.passage.sales.order.*.v1", "dev.passage.billing.invoice.**").
	EventType string           `json:"eventType"`
	Subject   *OutboundSubject `json:"subject,omitempty"`
	Xrefs     []OutboundXref   `json:"xrefs,omitempty"`
	// Lookups read lookup table entries (enum codes, unit conversion factors, prices) for the event; the mapping
	// reads them as lookup.<name>.
	Lookups []OutboundLookup `json:"lookups,omitempty"`
	// Collections map event arrays (an order's lines) to body arrays, element by element, so one push creates a
	// header and all its lines in the legacy ERP.
	Collections []OutboundCollection `json:"collections,omitempty"`
	// DecimalsAsNumbers writes decimal fields as exact JSON numbers instead of strings.
	DecimalsAsNumbers bool `json:"decimalsAsNumbers,omitempty"`
	// Mapping is the inbound mapping DSL: Entity is the legacy entity written, Fields read the event row. A target
	// containing dots ("header.customerNo") nests. Its version is the mapping version and is set by the service.
	Mapping Mapping `json:"mapping"`
}

// ParseOutboundDefinition decodes and validates a definition, pinning the mapping DSL's version to version.
func ParseOutboundDefinition(raw []byte, version int) (OutboundDefinition, error) {
	var d OutboundDefinition
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, &Error{CodeOutboundInvalid, "definition JSON: " + err.Error()}
	}
	d.Mapping.Version = version
	// Validating through ParseMapping also builds the time zone and checks every rule, test and reject condition.
	canonical, err := json.Marshal(d.Mapping)
	if err != nil {
		return d, &Error{CodeOutboundInvalid, err.Error()}
	}
	m, err := ParseMapping(canonical)
	if err != nil {
		return d, err
	}
	d.Mapping = m
	if err := d.check(); err != nil {
		return d, err
	}
	return d, nil
}

func (d OutboundDefinition) check() error {
	bad := func(format string, a ...any) error {
		return &Error{CodeOutboundInvalid, fmt.Sprintf(format, a...)}
	}
	if err := ValidateEventPattern(d.EventType); err != nil {
		return bad("eventType %s", err.Error())
	}
	if d.Subject != nil {
		if !xrefEntityRE.MatchString(d.Subject.Entity) {
			return bad("subject.entity must start with a letter and use letters, digits, _ . -")
		}
		if !eventPathRE.MatchString(d.Subject.IDPath) {
			return bad("subject.idPath must be a dotted path into the event, such as data.order_id")
		}
	}
	targets := fieldTargets(d.Mapping.Fields)
	for _, c := range d.Collections {
		targets = append(targets, c.Name)
	}
	if err := checkTargets(targets); err != nil {
		return err
	}
	if err := d.checkCollections(); err != nil {
		return err
	}
	if len(d.Xrefs) > maxOutboundXrefs {
		return bad("at most %d xrefs", maxOutboundXrefs)
	}
	seen := map[string]bool{}
	for _, x := range d.Xrefs {
		switch {
		case !xrefNameRE.MatchString(x.Name):
			return bad("xref name %q must start with a lower-case letter and use letters, digits and _", x.Name)
		case x.Name == selfXrefName:
			return bad("xref name %q is reserved for the subject's own legacy key", selfXrefName)
		case seen[x.Name]:
			return bad("duplicate xref name %q", x.Name)
		case !xrefEntityRE.MatchString(x.Entity):
			return bad("xref %s: entity must start with a letter and use letters, digits, _ . -", x.Name)
		case !eventPathRE.MatchString(x.IDPath):
			return bad("xref %s: idPath must be a dotted path into the event", x.Name)
		}
		seen[x.Name] = true
	}
	if len(d.Lookups) > maxOutboundLookups {
		return bad("at most %d lookups", maxOutboundLookups)
	}
	seenLookup := map[string]bool{}
	for _, l := range d.Lookups {
		if err := l.check(); err != nil {
			return err
		}
		if seenLookup[l.Name] {
			return bad("duplicate lookup name %q", l.Name)
		}
		seenLookup[l.Name] = true
	}
	return nil
}

// checkTargets refuses targets that cannot nest into one body: an empty dotted segment, or one target that is a
// prefix of another ("Header" and "Header.No"). ParseMapping already refuses a repeated target.
func checkTargets(targets []string) error {
	leaf := map[string]bool{}
	for _, t := range targets {
		if slices.Contains(strings.Split(t, "."), "") {
			return &Error{CodeOutboundInvalid, "target " + t + " has an empty segment"}
		}
		if leaf[t] {
			return &Error{CodeOutboundInvalid, "target " + t + " is used twice"}
		}
		leaf[t] = true
	}
	for t := range leaf {
		segs := strings.Split(t, ".")
		for i := 1; i < len(segs); i++ {
			if prefix := strings.Join(segs[:i], "."); leaf[prefix] {
				return &Error{CodeOutboundInvalid, "target " + t + " collides with target " + prefix}
			}
		}
	}
	return nil
}

// ValidateEventPattern checks an event type pattern.
func ValidateEventPattern(p string) error {
	if p == "" || len(p) > 200 {
		return fmt.Errorf("is required (at most 200 characters)")
	}
	segs := strings.Split(p, ".")
	if len(segs) > outboundMaxPatternSegs {
		return fmt.Errorf("has too many segments")
	}
	for i, s := range segs {
		switch {
		case s == "*":
		case s == "**" && i == len(segs)-1:
		case typeSegRE.MatchString(s):
		default:
			return fmt.Errorf("segment %q is not a name, * or a trailing **", s)
		}
	}
	return nil
}

// MatchesEventType reports whether a CloudEvents type fits a pattern.
func MatchesEventType(pattern, eventType string) bool {
	ps, ts := strings.Split(pattern, "."), strings.Split(eventType, ".")
	for i, p := range ps {
		if p == "**" && i == len(ps)-1 {
			return len(ts) > i
		}
		if i >= len(ts) || (p != "*" && p != ts[i]) {
			return false
		}
	}
	return len(ps) == len(ts)
}

// OutboundRef identifies a Passage record: the entity name used in the cross-reference and its id.
type OutboundRef struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
}

// UnresolvedRef is a lookup that found no legacy key: which xref of the mapping, which Passage record, and why.
type UnresolvedRef struct {
	Name   string `json:"name"`
	Entity string `json:"entity"`
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason"`
}

// UnresolvedError is MIG-XREF-UNRESOLVED: the event refers to Passage records that have no legacy key yet.
type UnresolvedError struct{ Refs []UnresolvedRef }

func (e *UnresolvedError) Error() string {
	parts := make([]string, len(e.Refs))
	for i, r := range e.Refs {
		parts[i] = r.Name + " (" + r.Entity + " " + r.ID + "): " + r.Reason
	}
	return CodeXrefUnresolved + ": " + strings.Join(parts, "; ")
}

// RejectedError is MIG-OUTBOUND-REJECTED: the mapping could not build a body from this event.
type RejectedError struct{ Rejects []Reject }

func (e *RejectedError) Error() string {
	parts := make([]string, len(e.Rejects))
	for i, r := range e.Rejects {
		parts[i] = r.Field + ": " + r.Code
	}
	return CodeOutboundRejected + ": " + strings.Join(parts, "; ")
}

// LegacyKeyResolver finds legacy keys for Passage records of one entity in one legacy system. A record with several
// legacy keys resolves to the lowest (the first the cross-reference ever held in key order), so a render is repeatable.
type LegacyKeyResolver interface {
	LegacyKeys(ctx context.Context, sourceSystem, entity string, passageIDs []string) (map[string]string, error)
}

// Rendered is the answer of a render: the request body, the key that makes the same render repeatable, and the Passage
// records the legacy key of the response belongs to.
type Rendered struct {
	Body           map[string]any `json:"body"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Refs           []OutboundRef  `json:"refs"`
	Warnings       []Warning      `json:"warnings,omitempty"`
	// Lookups lists the lookup table versions the render read, for the audit of what was pushed.
	Lookups []LookupUse `json:"lookups,omitempty"`
}

// OutboundIdempotencyKey is the render's repeatable key: SHA-256 of the mapping id and the event id. The mapping
// version is deliberately left out: a push that is retried after the mapping was edited (the legacy ERP accepted it
// but xref:record failed, or an operator replays it) must reach the receiver under the same key, or it creates a
// second record. The hub sends the event id itself as Idempotency-Key, which has the same property. The mapping id is
// a fixed-length UUID, so the concatenation is unambiguous.
func OutboundIdempotencyKey(mappingID, eventID string) string {
	sum := sha256.Sum256([]byte(mappingID + "\x00" + eventID))
	return idempotencyKeyPrefix + hex.EncodeToString(sum[:])
}

// DecodeEvent parses a CloudEvent as the row an outbound mapping reads. Numbers stay exact (json.Number).
func DecodeEvent(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxEventBytes {
		return nil, &Error{CodeOutboundEvent, "event is required and at most 1 MiB"}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var ev map[string]any
	if err := dec.Decode(&ev); err != nil || ev == nil {
		return nil, &Error{CodeOutboundEvent, "event must be a JSON object"}
	}
	id, _ := ev["id"].(string)
	typ, _ := ev["type"].(string)
	if id == "" || len(id) > maxEventIDLength || typ == "" {
		return nil, &Error{CodeOutboundEvent, "event needs a string id (at most 256 characters) and a string type"}
	}
	return ev, nil
}

// Render builds the request body for event. It reads the cross-reference through resolver and does nothing else: no
// writes, no network, so it can be called any number of times. An unresolved lookup is *UnresolvedError and lists
// every one; a mapping that cannot produce a body is *RejectedError.
func (d OutboundDefinition) Render(ctx context.Context, sourceSystem, mappingID string, event map[string]any, resolver LegacyKeyResolver) (Rendered, error) {
	eventID, _ := event["id"].(string)
	eventType, _ := event["type"].(string)
	if !MatchesEventType(d.EventType, eventType) {
		return Rendered{}, &Error{CodeOutboundMismatch, "event type " + eventType + " does not match the mapping's " + d.EventType}
	}

	var unresolved []UnresolvedRef
	keys := map[string]any{}
	for _, x := range d.Xrefs {
		id, ok := idAt(event, x.IDPath)
		if !ok {
			if !x.Optional {
				unresolved = append(unresolved, UnresolvedRef{Name: x.Name, Entity: x.Entity, Reason: "the event has no id at " + x.IDPath})
			}
			continue
		}
		found, err := resolver.LegacyKeys(ctx, sourceSystem, x.Entity, []string{id})
		if err != nil {
			return Rendered{}, err
		}
		if key, ok := found[id]; ok {
			keys[x.Name] = key
		} else if !x.Optional {
			unresolved = append(unresolved, UnresolvedRef{Name: x.Name, Entity: x.Entity, ID: id, Reason: "no legacy key is recorded for this record yet"})
		}
	}

	refs := []OutboundRef{}
	if d.Subject != nil {
		if id, ok := idAt(event, d.Subject.IDPath); ok && !ids.Valid(id) {
			// xref:record ties the legacy key to a Passage id; anything else would be pushed and then not recordable.
			unresolved = append(unresolved, UnresolvedRef{Name: "subject", Entity: d.Subject.Entity, ID: id, Reason: "the id at " + d.Subject.IDPath + " is not a Passage id (UUID)"})
		} else if ok {
			refs = append(refs, OutboundRef{Entity: d.Subject.Entity, ID: id})
			// A subject that already has a legacy key is an update: the mapping may read xref.self.
			found, err := resolver.LegacyKeys(ctx, sourceSystem, d.Subject.Entity, []string{id})
			if err != nil {
				return Rendered{}, err
			}
			if key, ok := found[id]; ok {
				keys[selfXrefName] = key
			}
		} else {
			unresolved = append(unresolved, UnresolvedRef{Name: "subject", Entity: d.Subject.Entity, Reason: "the event has no id at " + d.Subject.IDPath})
		}
	}
	// Lookup tables are read once each for the whole render (the event's lookups and every element's), see lookupCache.
	prepared, shapeRejects := d.collectionRows(event)
	tables, cache := lookupSource(resolver)
	if cache != nil {
		if err := cache.prefetch(ctx, d.lookupKeys(event, prepared)); err != nil {
			return Rendered{}, err
		}
	}
	lookups, missing, used, err := d.resolveLookups(ctx, tables, event)
	if err != nil {
		return Rendered{}, err
	}
	unresolved = append(unresolved, missing...)
	itemUnresolved, itemLookups, err := d.resolveItems(ctx, resolver, tables, sourceSystem, prepared)
	if err != nil {
		return Rendered{}, err
	}
	unresolved = append(unresolved, itemUnresolved...)
	used = append(used, itemLookups...)
	if len(unresolved) > 0 {
		return Rendered{}, &UnresolvedError{Refs: unresolved}
	}

	row := make(map[string]any, len(event)+1)
	for k, v := range event {
		row[k] = v
	}
	row["xref"] = keys
	row["lookup"] = lookups
	out, warnings, rejects := d.Mapping.ApplyWithWarnings(row)
	rejects = append(rejects, shapeRejects...)
	body := map[string]any{}
	if len(rejects) == 0 {
		if body, err = shapeFields(d.Mapping.Fields, out, d.DecimalsAsNumbers); err != nil {
			return Rendered{}, err
		}
	}
	itemWarnings, itemRejects, err := d.mapCollections(prepared, body)
	if err != nil {
		return Rendered{}, err
	}
	rejects = append(rejects, itemRejects...)
	if len(rejects) > 0 {
		return Rendered{}, &RejectedError{Rejects: rejects}
	}
	warnings = append(warnings, itemWarnings...)
	return Rendered{Body: body, IdempotencyKey: OutboundIdempotencyKey(mappingID, eventID), Refs: refs, Warnings: warnings, Lookups: used}, nil
}

// shapeFields nests dotted targets and turns decimals into exact numbers when the definition asks for it.
func shapeFields(fields []FieldRule, out map[string]any, decimalsAsNumbers bool) (map[string]any, error) {
	decimals := map[string]bool{}
	for _, f := range fields {
		decimals[f.Target] = f.Type == TypeDecimal
	}
	body := make(map[string]any, len(out))
	for target, v := range out {
		if s, ok := v.(string); ok && decimalsAsNumbers && decimals[target] {
			v = json.Number(s)
		}
		if err := place(body, target, v); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// place puts v at the dotted target of body, creating the objects on the way.
func place(body map[string]any, target string, v any) error {
	segs := strings.Split(target, ".")
	cur := body
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			if _, taken := cur[seg]; taken {
				return &Error{CodeOutboundInvalid, "target " + target + " collides with another target"}
			}
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	last := segs[len(segs)-1]
	if _, taken := cur[last]; taken {
		return &Error{CodeOutboundInvalid, "target " + target + " collides with another target"}
	}
	cur[last] = v
	return nil
}

// idAt reads a string id (a UUID or any key) at a dotted path of the event.
func idAt(event map[string]any, path string) (string, bool) {
	switch v := Lookup(event, path).(type) {
	case string:
		s := strings.TrimSpace(v)
		return s, s != ""
	case json.Number:
		return v.String(), true
	}
	return "", false
}
