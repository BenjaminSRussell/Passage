// Package logpseudo pseudonymizes personal identifiers in log records before they are exported to Loki (issue 605).
//
// Microsoft pseudonymizes the personal data in its own system logs inside the EU Data Boundary; Passage owes the same
// for its logs and traces. Handler wraps a slog.Handler. An attribute whose key names a person (user, email, name,
// person, subject, ip, ...), and any email address inside any string value or the message, is replaced by a stable
// pseudonym: the HMAC of the value under the tenant's MAC key (fieldcrypt.Keys.MAC), so the same value maps to the
// same pseudonym inside one tenant and to a different one in another, and the pseudonym cannot be reversed without the
// key. When no key is configured, or the tenant is not known, the value is replaced by a fixed marker and never
// passed through.
//
// The tenant comes from the request context (tenant.FromContext) when the call carries one (slog.InfoContext), else
// from a tenant attribute on the record (tenant, tenantId, tenant_id). httpx.Serve calls Install, so a service needs
// nothing else.
//
// Limits: a person's name in free text (a message, a field not named like a person) cannot be recognised and is not
// changed; log a name under a key such as name, displayName or user instead. A value that is a struct or map is
// checked as text (fmt %+v) for email addresses only.
package logpseudo

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Env turns pseudonymization on when "true" (it is always on under the eu-data-boundary overlay).
const Env = "LOG_PSEUDONYMIZE"

// Redacted replaces a value that could not be pseudonymized (no key, or no tenant).
const Redacted = "[redacted]"

// EmailRedacted replaces an email address that could not be pseudonymized.
const EmailRedacted = "[redacted-email]"

// Prefix starts every pseudonym.
const Prefix = "pn_"

const macDomain = "passage.log.pseudonym.v1\x00"

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+'-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)

// MACer is fieldcrypt.Keys' MAC.
type MACer interface {
	MAC(tenantID string, message []byte) ([]byte, error)
}

// Enabled reports whether logs are pseudonymized: always under the eu-data-boundary overlay of the cloud profile,
// and when LOG_PSEUDONYMIZE=true.
func Enabled(getenv func(string) string) (bool, error) {
	p, err := cloudprofile.FromEnv(getenv)
	if err != nil {
		return false, err
	}
	for _, o := range p.Overlays {
		if o == "eu-data-boundary" {
			return true, nil
		}
	}
	return strings.EqualFold(strings.TrimSpace(getenv(Env)), "true"), nil
}

// Install wraps slog's default handler when pseudonymization is enabled (see Enabled) and reports whether it did. The
// key is fieldcrypt's (FIELDCRYPT_KEYS); with none configured every personal value is replaced by Redacted. A bad
// profile or key configuration is an error: logs are never left raw because the configuration could not be read.
func Install(getenv func(string) string) (bool, error) {
	on, err := Enabled(getenv)
	if err != nil || !on {
		return false, err
	}
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil {
		return false, fmt.Errorf("logpseudo: %w", err)
	}
	var mac MACer
	if keys != nil {
		mac = keys
	}
	return InstallWith(mac), nil
}

// installMu makes InstallWith's check and set one step: a service that runs several httpx.Serve listeners installs from
// each of them concurrently, and two unsynchronised installs would wrap the handler twice.
var installMu sync.Mutex

// InstallWith wraps slog's default handler unconditionally, unless it is already wrapped. A nil mac redacts. It is safe
// for concurrent use.
func InstallWith(mac MACer) bool {
	installMu.Lock()
	defer installMu.Unlock()
	if _, done := slog.Default().Handler().(*Handler); done {
		return false
	}
	slog.SetDefault(slog.New(New(slog.Default().Handler(), mac)))
	return true
}

// part is one WithAttrs or WithGroup call, kept so the attributes are rewritten when the record, and with it the
// tenant, is known.
type part struct {
	group string
	attrs []slog.Attr
}

// Handler is a slog.Handler that pseudonymizes personal data before it reaches next.
type Handler struct {
	next  slog.Handler
	mac   MACer
	parts []part
}

// New wraps next. A nil mac replaces personal values with Redacted.
func New(next slog.Handler, mac MACer) *Handler { return &Handler{next: next, mac: mac} }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.parts = append(append([]part(nil), h.parts...), part{attrs: append([]slog.Attr(nil), attrs...)})
	return &c
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.parts = append(append([]part(nil), h.parts...), part{group: name})
	return &c
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	for i := len(h.parts) - 1; i >= 0; i-- {
		if p := h.parts[i]; p.group != "" {
			attrs = []slog.Attr{{Key: p.group, Value: slog.GroupValue(attrs...)}}
		} else {
			attrs = append(append([]slog.Attr(nil), p.attrs...), attrs...)
		}
	}
	tenantID := ""
	if p, ok := tenant.FromContext(ctx); ok {
		tenantID = p.TenantID
	}
	if tenantID == "" {
		tenantID = findTenant(attrs)
	}
	s := scrubber{mac: h.mac, tenantID: tenantID}
	out := slog.NewRecord(r.Time, r.Level, s.text(r.Message), r.PC)
	for _, a := range attrs {
		out.AddAttrs(s.attr(a, false))
	}
	return h.next.Handle(ctx, out)
}

type scrubber struct {
	mac      MACer
	tenantID string
}

// PseudonymFor is the pseudonym of value for a tenant under mac, as logged. The TypeScript and Python loggers compute the
// same value; their tests hold known answers taken from this function.
func PseudonymFor(mac MACer, tenantID, value string) string {
	p, _ := scrubber{mac: mac, tenantID: tenantID}.pseudonym(value)
	return p
}

func (s scrubber) pseudonym(value string) (string, bool) {
	if s.mac == nil || s.tenantID == "" {
		return "", false
	}
	sum, err := s.mac.MAC(s.tenantID, []byte(macDomain+strings.ToLower(strings.TrimSpace(value))))
	if err != nil {
		return "", false
	}
	return Prefix + hex.EncodeToString(sum[:8]), true
}

// text replaces every email address in text by its pseudonym (or EmailRedacted).
func (s scrubber) text(text string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	return emailRE.ReplaceAllStringFunc(text, func(m string) string {
		if p, ok := s.pseudonym(m); ok {
			return p
		}
		return EmailRedacted
	})
}

// attr rewrites one attribute. Everything inside a group whose own key names a person (a "user" or "person" group) is
// personal data, whatever its keys are.
func (s scrubber) attr(a slog.Attr, inPersonal bool) slog.Attr {
	v := a.Value.Resolve()
	inPersonal = inPersonal || personal(a.Key)
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		out := make([]any, 0, len(group))
		for _, g := range group {
			out = append(out, s.attr(g, inPersonal))
		}
		return slog.Group(a.Key, out...)
	}
	if inPersonal {
		if v.Kind() == slog.KindString && v.String() == "" {
			return a
		}
		str := fmt.Sprint(v.Any())
		if p, ok := s.pseudonym(str); ok {
			return slog.String(a.Key, p)
		}
		return slog.String(a.Key, Redacted)
	}
	switch v.Kind() {
	case slog.KindString:
		if t := s.text(v.String()); t != v.String() {
			return slog.String(a.Key, t)
		}
	case slog.KindAny:
		if str := fmt.Sprintf("%+v", v.Any()); strings.Contains(str, "@") {
			if t := s.text(str); t != str {
				return slog.String(a.Key, t)
			}
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}

func findTenant(attrs []slog.Attr) string {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			if id := findTenant(v.Group()); id != "" {
				return id
			}
			continue
		}
		switch strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(a.Key)) {
		case "tenant", "tenantid":
			if v.Kind() == slog.KindString {
				return v.String()
			}
		}
	}
	return ""
}

var (
	personalWords = map[string]bool{
		"user": true, "username": true, "userid": true, "email": true, "mail": true, "person": true, "subject": true, "sub": true,
		"ip": true, "phone": true, "actor": true, "principal": true, "approver": true, "requester": true, "caller": true,
		"employee": true, "contact": true, "firstname": true, "lastname": true, "fullname": true, "displayname": true,
		"givenname": true, "surname": true, "upn": true,
	}
	nameQualifiers = map[string]bool{
		"first": true, "last": true, "full": true, "display": true, "given": true, "family": true, "person": true,
		"user": true, "customer": true, "employee": true, "contact": true, "author": true,
	}
)

// personal reports whether an attribute key names a person: user, email, name, person, subject, ip and the like,
// in snake_case, kebab-case, dotted or camelCase.
func personal(key string) bool {
	words := splitWords(key)
	hasName := false
	for _, w := range words {
		if personalWords[w] {
			return true
		}
		if w == "name" {
			hasName = true
		}
	}
	if !hasName {
		return false
	}
	if len(words) == 1 {
		return true
	}
	for _, w := range words {
		if nameQualifiers[w] {
			return true
		}
	}
	return false
}

func splitWords(key string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}
