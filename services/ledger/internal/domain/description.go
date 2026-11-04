package domain

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Default description templates (FR-11.17). A template holds placeholders %1 to
// %9; each placeholder is bound to one named field. Expansion is pure: the
// caller supplies the field values.

// MaxDescriptionLength is the entry description and line text limit (FR-11.17,
// LEDGER-0031), matching the journal_entry.description and journal_line.line_text checks.
const MaxDescriptionLength = 255

// DefaultDescriptionLanguage is the language templates are looked up in until
// journal names (journal_name.description_language) and the legal entity's
// language exist (see NOT-IMPLEMENTED.md).
const DefaultDescriptionLanguage = "en"

// CodeDescriptionTruncated is the info notice LEDGER-0031: an expanded default
// description was cut to MaxDescriptionLength characters.
const CodeDescriptionTruncated = "LEDGER_DESCRIPTION_TRUNCATED"

// DescriptionFields are the named fields a placeholder may bind to (FR-11.17).
var DescriptionFields = []string{"voucher", "party_name", "document_date", "amount_txn", "currency_txn", "source_id", "period"}

var (
	placeholderRe = regexp.MustCompile(`%([1-9])`)
	sourceTypeRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,59}$`)
	languageRe    = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
)

// Journal name settings (PRD 2.11): who may post a name's batches and where its
// default description language comes from.
const (
	PostingNone             = "none"
	PostingOwnJournalsOnly  = "own_journals_only"
	DescriptionLanguageLE   = "legal_entity"
	DescriptionLanguageUser = "user"
)

// DescriptionLanguages lists the languages to try for a default description,
// most specific first: the requested tag, its base language, then the default.
// An empty or malformed request gives the default only.
func DescriptionLanguages(requested string) []string {
	out := []string{}
	if ValidLanguage(requested) {
		out = append(out, requested)
		if base := requested[:2]; base != requested {
			out = append(out, base)
		}
	}
	if len(out) == 0 || out[len(out)-1] != DefaultDescriptionLanguage {
		out = append(out, DefaultDescriptionLanguage)
	}
	return out
}

// ValidSourceType reports whether s is a usable source type key.
func ValidSourceType(s string) bool { return sourceTypeRe.MatchString(s) }

// ValidLanguage reports whether s is a language tag such as "en" or "en-US".
func ValidLanguage(s string) bool { return languageRe.MatchString(s) }

func isDescriptionField(f string) bool {
	for _, known := range DescriptionFields {
		if f == known {
			return true
		}
	}
	return false
}

// ValidateDescriptionTemplate returns field errors keyed by request field name
// (template or fieldBindings); an empty map means the template is valid. Every
// placeholder in the text must be bound to a known field and every binding must
// appear in the text, so no value is silently dropped. Messages are
// deterministic: the lowest offending placeholder is reported.
func ValidateDescriptionTemplate(template string, bindings map[string]string) map[string]string {
	errs := map[string]string{}
	switch {
	case strings.TrimSpace(template) == "":
		errs["template"] = "template is required"
	case utf8.RuneCountInString(template) > MaxDescriptionLength:
		errs["template"] = "template must be at most 255 characters"
	}
	keys := make([]string, 0, len(bindings))
	for k := range bindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(k) != 1 || k[0] < '1' || k[0] > '9' {
			errs["fieldBindings"] = "placeholder keys must be 1 to 9"
			return errs
		}
		if !isDescriptionField(bindings[k]) {
			errs["fieldBindings"] = "unknown field " + bindings[k] + " for %" + k
			return errs
		}
	}
	if len(errs) > 0 {
		return errs
	}
	used := map[string]bool{}
	var unbound []string
	for _, m := range placeholderRe.FindAllStringSubmatch(template, -1) {
		if !used[m[1]] {
			used[m[1]] = true
			if _, bound := bindings[m[1]]; !bound {
				unbound = append(unbound, m[1])
			}
		}
	}
	if len(unbound) > 0 {
		sort.Strings(unbound)
		errs["fieldBindings"] = "placeholder %" + unbound[0] + " has no binding"
	}
	for _, k := range keys {
		if !used[k] {
			errs["template"] = "binding %" + k + " does not appear in the template"
			break
		}
	}
	return errs
}

// ExpandDescription substitutes bound fields into template. Unbound
// placeholders expand to empty text. The result is cut to
// MaxDescriptionLength runes, ending in an ellipsis; the boolean reports the
// truncation (LEDGER-0031).
func ExpandDescription(template string, bindings, values map[string]string) (string, bool) {
	out := placeholderRe.ReplaceAllStringFunc(template, func(m string) string {
		field, ok := bindings[m[1:]]
		if !ok {
			return ""
		}
		return values[field]
	})
	if utf8.RuneCountInString(out) <= MaxDescriptionLength {
		return out, false
	}
	runes := []rune(out)
	return string(runes[:MaxDescriptionLength-1]) + "…", true
}
