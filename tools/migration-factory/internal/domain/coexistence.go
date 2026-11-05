package domain

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Coexistence (FR-49.7, D6): while a legacy company is cut over entity by entity, each entity kind
// is mastered on one side. Until its cutover the legacy system masters it and the coexistence
// adapter brings its changes into Passage; from its cutover Passage masters it and a legacy change
// is refused (BR-49.10), so the two sides never both write one record. A rollback flips it back.

// CoexistMode says which side masters an entity kind.
type CoexistMode string

// Coexistence modes.
const (
	CoexistLegacy  CoexistMode = "LEGACY"
	CoexistPassage CoexistMode = "PASSAGE"
)

// CoexistEntities are the entity kinds an adapter carries, one flag each per (system, company).
var CoexistEntities = []string{"customer", "vendor", "product", "open_items", "open_orders", "onhand", "gl_journal", "fixed_asset", "worker"}

// ParseCoexistEntity validates an entity kind.
func ParseCoexistEntity(s string) (string, error) {
	for _, e := range CoexistEntities {
		if e == s {
			return e, nil
		}
	}
	return "", fmt.Errorf("%w: entity %q is not one of %s", ErrInvalid, s, strings.Join(CoexistEntities, ", "))
}

// ParseCoexistMode validates a wire mode.
func ParseCoexistMode(s string) (CoexistMode, error) {
	switch m := CoexistMode(strings.ToUpper(strings.TrimSpace(s))); m {
	case CoexistLegacy, CoexistPassage:
		return m, nil
	}
	return "", fmt.Errorf("%w: mode %q is not LEGACY or PASSAGE", ErrInvalid, s)
}

// CoexistFlag is the cutover flag of one entity kind of a legacy company. A kind without a stored
// flag is LEGACY at version 0. CutoverAt is when Passage took it over.
type CoexistFlag struct {
	SourceSystem SourceSystem
	Company      string
	Entity       string
	Mode         CoexistMode
	CutoverAt    *time.Time
	Version      int
	ChangedBy    string
	ChangedAt    time.Time
}

// SetMode changes the flag to mode at now by a signed-in user. Cutting over records CutoverAt; a
// flip back to LEGACY (a rollback) clears it. Setting the mode it has is refused, so a stale
// client's repeat is visible.
func (f *CoexistFlag) SetMode(mode CoexistMode, by string, now time.Time) error {
	if strings.TrimSpace(by) == "" {
		return fmt.Errorf("%w: a coexistence change needs a signed-in user", ErrSoD)
	}
	if f.Mode == mode || (f.Mode == "" && mode == CoexistLegacy) {
		return fmt.Errorf("%w: %s of %s is already %s", ErrTransition, f.Entity, f.Company, mode)
	}
	f.Mode, f.ChangedBy, f.ChangedAt = mode, by, now
	if mode == CoexistPassage {
		t := now
		f.CutoverAt = &t
	} else {
		f.CutoverAt = nil
	}
	f.Version++
	return nil
}

// InboundRecord is one change an adapter read from the legacy system: the entity kind, the legacy
// table and id, and when the legacy system changed it.
type InboundRecord struct {
	Entity      string
	SourceTable string
	LegacyID    string
	ChangedAt   time.Time
	Payload     []byte
}

// OutboundRecord is one Passage change an adapter writes back to the legacy system for an entity
// Passage masters while the legacy system still reads it (re-keying during coexistence).
type OutboundRecord struct {
	Entity    string
	PassageID string
	LegacyID  string
	Payload   []byte
}

// CoexistenceAdapter is a legacy system's coexistence adapter (AX OData/DMF, SAP IDoc/BAPI). The
// implementations live in the integration hub (wave 35); the factory routes their records by the
// entity flags.
type CoexistenceAdapter interface {
	System() SourceSystem
	// Pull returns the legacy changes of one entity kind of company since the given time.
	Pull(ctx context.Context, company, entity string, since time.Time) ([]InboundRecord, error)
	// Push writes Passage changes of an entity Passage masters back to the legacy system.
	Push(ctx context.Context, company, entity string, records []OutboundRecord) error
	// Suspend stops and Resume restarts the adapter's flow for an entity kind (cutover, rollback).
	Suspend(ctx context.Context, company, entity string) error
	Resume(ctx context.Context, company, entity string) error
}

// Route decisions for an inbound legacy record.
const (
	RouteCreate   = "CREATE"
	RouteMatch    = "MATCH"
	RouteRefused  = "REFUSED"
	RouteDupBatch = "DUPLICATE"
	// MsgCoexistCutOver is the BR-49.10 refusal: the entity is mastered in Passage.
	MsgCoexistCutOver = "MIG-1510"
)

// RoutedRecord is the decision for one inbound record: CREATE (no cross-reference yet, Passage takes
// it as a new record), MATCH (a legacy_xref row exists, the change updates PassageID), REFUSED (the
// entity is cut over and the change is not older than the cutover) or DUPLICATE (the batch has the
// same record twice; the latest change wins).
type RoutedRecord struct {
	Record    InboundRecord
	Decision  string
	PassageID string
	Reason    string
}

// RouteInbound decides what happens to an adapter's inbound records under the entity's flag and the
// cross-reference (BR-49.10). known maps "table|legacy id" to the Passage id legacy_xref holds.
// Records are deduplicated by (table, legacy id), keeping the latest change; a record of a cut-over
// entity changed at or after its cutover is refused, while one changed before it (a late delivery of
// a pre-cutover change) is still applied.
func RouteInbound(flag CoexistFlag, in []InboundRecord, known map[string]string) []RoutedRecord {
	latest := map[string]int{}
	for i, r := range in {
		k := r.SourceTable + "|" + r.LegacyID
		if j, ok := latest[k]; !ok || r.ChangedAt.After(in[j].ChangedAt) {
			latest[k] = i
		}
	}
	out := make([]RoutedRecord, 0, len(in))
	for i, r := range in {
		k := r.SourceTable + "|" + r.LegacyID
		switch {
		case r.Entity != flag.Entity:
			out = append(out, RoutedRecord{Record: r, Decision: RouteRefused, Reason: fmt.Sprintf("record is a %s, the flag is for %s", r.Entity, flag.Entity)})
		case latest[k] != i:
			out = append(out, RoutedRecord{Record: r, Decision: RouteDupBatch, Reason: "a later change of the same record is in the batch"})
		case flag.Mode == CoexistPassage && flag.CutoverAt != nil && !r.ChangedAt.Before(*flag.CutoverAt):
			out = append(out, RoutedRecord{Record: r, Decision: RouteRefused, PassageID: known[k],
				Reason: fmt.Sprintf("%s %s of %s is mastered in Passage since %s; the legacy change is not applied", MsgCoexistCutOver, flag.Entity, flag.Company, flag.CutoverAt.UTC().Format(time.RFC3339))})
		case known[k] != "":
			out = append(out, RoutedRecord{Record: r, Decision: RouteMatch, PassageID: known[k]})
		default:
			out = append(out, RoutedRecord{Record: r, Decision: RouteCreate})
		}
	}
	return out
}
