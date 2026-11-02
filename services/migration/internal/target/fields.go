package target

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
)

// text reads a mapped field as trimmed text ("" when absent or null). Numbers and booleans print as they are.
func text(f map[string]any, k string) string {
	v, ok := f[k]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// pick copies the named fields that have a value into out, as text.
func pick(out map[string]any, f map[string]any, names ...string) {
	for _, n := range names {
		if v := text(f, n); v != "" {
			out[n] = v
		}
	}
}

// required lists the names with no value, for a MIG-LOAD-FIELD-REQUIRED reject.
func required(f map[string]any, names ...string) []string {
	var missing []string
	for _, n := range names {
		if text(f, n) == "" {
			missing = append(missing, n)
		}
	}
	return missing
}

func missingResult(missing []string) domain.LoadResult {
	return domain.LoadResult{Code: CodeFieldRequired, Detail: "the mapped record has no " + strings.Join(missing, ", ") + "; map them in the mapping spec"}
}

// legalEntity resolves the legal entity a row loads into: its own legalEntityId, else the run's. A run under a project
// with a target legal entity loads into that entity only: a row naming another one is refused, so a mapping cannot
// move a project's rows into a legal entity the project was not set up for.
func (l *Loader) legalEntity(f map[string]any) (string, *domain.LoadResult) {
	own := text(f, "legalEntityId")
	if own != "" && !ids.Valid(own) {
		return "", &domain.LoadResult{Code: CodeLegalEntity, Detail: "legalEntityId is not a UUID"}
	}
	run := strings.ToLower(l.run.LegalEntityID)
	switch {
	case own == "" && run == "":
		return "", &domain.LoadResult{Code: CodeLegalEntity, Detail: "no legal entity: start the run under a project with a target legal entity or map legalEntityId (a UUID) on each row"}
	case own == "":
		return run, nil
	case run != "" && !strings.EqualFold(own, run):
		return "", &domain.LoadResult{Code: CodeLegalEntity, Detail: "legalEntityId is not the project's target legal entity; a project run loads into that legal entity only"}
	}
	return strings.ToLower(own), nil
}

// localPrefix marks a mapped field that stays in the migration service: it feeds reject rules and the change hash and
// is never sent to a target.
const localPrefix = "_"

// contract is the field names each entity's loader sends or reads, beyond legalEntityId.
var contract = map[string][]string{
	"party":        slices.Concat([]string{"partyType", "status"}, partyFields),
	"customer":     slices.Concat([]string{"partyId", "partyKey", "invoiceAccountPartyKey", "creditHold"}, customerNeed, customerOptional),
	"vendor":       slices.Concat([]string{"partyId", "partyKey"}, vendorNeed, vendorOptional),
	"product":      productFields,
	"item":         productFields,
	"ar_open_item": slices.Concat([]string{"customerId", "customerKey"}, openItemFields),
}

// UnknownTargets lists the mapping targets of entity that its loader would not send anywhere: a misspelt or
// unsupported field would otherwise be dropped while its value still counts in the change hash. legalEntityId,
// attr.<code> for products and names starting with "_" (kept for rules and the hash) are always accepted. An entity
// with no target API has no contract; its rows are rejected MIG-TARGET-NOT-AVAILABLE instead.
func UnknownTargets(entity string, targets []string) []string {
	names, ok := contract[entity]
	if !ok {
		return nil
	}
	var unknown []string
	for _, t := range targets {
		switch {
		case t == "legalEntityId", strings.HasPrefix(t, localPrefix), slices.Contains(names, t):
		case (entity == "product" || entity == "item") && strings.HasPrefix(t, attrPrefix) && len(t) > len(attrPrefix):
		default:
			unknown = append(unknown, t)
		}
	}
	return unknown
}

// UnknownTargets is the package function, so the API can check a run's mapping against the loader it was given.
func (l *Loader) UnknownTargets(entity string, targets []string) []string {
	return UnknownTargets(entity, targets)
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ref resolves a reference to another loaded entity: the field idField holds a Passage id directly, keyField the
// legacy key of a row that an earlier run of entity loaded. An unresolved key is a reject that says which run to do first.
func (l *Loader) ref(ctx context.Context, f map[string]any, idField, keyField, entity string) (string, *domain.LoadResult) {
	if id := text(f, idField); id != "" {
		if !uuidShape.MatchString(id) {
			return "", &domain.LoadResult{Code: CodeRefUnresolved, Detail: idField + " is not a UUID"}
		}
		return strings.ToLower(id), nil
	}
	key := text(f, keyField)
	if key == "" {
		return "", &domain.LoadResult{Code: CodeFieldRequired, Detail: "the mapped record has no " + keyField + " (or " + idField + ")"}
	}
	if l.run.Xref == nil {
		return "", &domain.LoadResult{Code: CodeRefUnresolved, Detail: "no cross-reference to resolve " + keyField}
	}
	e, found, err := l.run.Xref.Get(ctx, domain.XrefKey{SourceSystem: l.run.SourceSystem, Entity: entity, SourceKey: key})
	if err != nil {
		return "", &domain.LoadResult{Code: CodeRefUnresolved, Detail: "cross-reference lookup failed for " + keyField}
	}
	if !found || e.ContentHash == "" {
		return "", &domain.LoadResult{Code: CodeRefUnresolved, Detail: fmt.Sprintf("%s %q is not loaded yet; run the %s load first", keyField, key, entity)}
	}
	return e.TargetID, nil
}
