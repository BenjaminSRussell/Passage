// Package cloudprofile is the Go side of the Passage cloud profile (#599).
//
// One deployment runs in one Microsoft cloud environment, named by a base profile (commercial, gcc, gcc-high,
// dod, china) and optional overlays (eu-data-boundary, hipaa), written "gcc+hipaa" in PASSAGE_CLOUD_PROFILE.
// The data is deploy/cloud-profiles/profiles.json, embedded here as profiles.json (a generated copy; a test
// and `python3 deploy/cloud-profiles/cloudprofile.py sync --check` fail when it drifts). No Azure hostname
// belongs anywhere else in Go code: read it from the resolved profile.
package cloudprofile

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
)

// EnvProfile names the profile a process runs under, for example "gcc" or "gcc+hipaa". Helm sets it from
// global.cloudProfile and global.cloudOverlays. Unset means commercial.
const EnvProfile = "PASSAGE_CLOUD_PROFILE"

//go:embed profiles.json
var profilesJSON []byte

// Endpoint is one row of the endpoint table.
type Endpoint struct {
	Value   string   `json:"value"`
	Source  string   `json:"source"`
	Status  string   `json:"status,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

type registry struct {
	Default       string `json:"default"`
	RequireMirror bool   `json:"requireMirror"`
}

type egress struct {
	LLMProviders     []string `json:"llmProviders"`
	EnforceProviders bool     `json:"enforceProviders"`
	PagerDuty        bool     `json:"pagerDuty"`
	ExtraAllow       []string `json:"extraAllow"`
}

// Keycloak holds the realm settings a profile requires.
type Keycloak struct {
	PasswordHash       string `json:"passwordHash"`
	PasswordMinLength  int    `json:"passwordMinLength"`
	EventRetentionDays int    `json:"eventRetentionDays"`
}

type profile struct {
	Description    string              `json:"description"`
	Cloud          string              `json:"cloud"`
	FIPS           bool                `json:"fips"`
	AllowedRegions []string            `json:"allowedRegions"`
	Endpoints      map[string]Endpoint `json:"endpoints"`
	Registry       registry            `json:"registry"`
	Egress         egress              `json:"egress"`
	Keycloak       Keycloak            `json:"keycloak"`
}

type overlay struct {
	Description        string   `json:"description"`
	AllowedRegions     []string `json:"allowedRegions"`
	EgressRemove       []string `json:"egressRemove"`
	RequireFIPS        bool     `json:"requireFips"`
	EventRetentionDays int      `json:"eventRetentionDays"`
}

// Doc is the whole profile table.
type Doc struct {
	SchemaVersion int                `json:"schemaVersion"`
	EndpointKeys  []string           `json:"endpointKeys"`
	Profiles      map[string]profile `json:"profiles"`
	Overlays      map[string]overlay `json:"overlays"`
}

var (
	docOnce sync.Once
	doc     *Doc
	docErr  error
)

// Table returns the parsed profile table.
func Table() (*Doc, error) {
	docOnce.Do(func() {
		doc = &Doc{}
		docErr = json.Unmarshal(profilesJSON, doc)
	})
	return doc, docErr
}

// ProfileNames lists the base profiles, sorted.
func ProfileNames() []string {
	d, err := Table()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(d.Profiles))
	for n := range d.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Profile is a base profile with its overlays applied.
type Profile struct {
	Name                  string
	Overlays              []string
	Cloud                 string
	FIPS                  bool
	AllowedRegions        []string // empty means no region restriction
	Endpoints             map[string]string
	EntraAuthorityAliases []string
	RequireMirror         bool
	RegistryDefault       string
	LLMProviders          []string
	EnforceProviders      bool
	PagerDutyAllowed      bool
	EgressAllowlist       []string
	Keycloak              Keycloak
}

// ParseSelector splits "gcc+hipaa" into the base profile and its overlays; empty means commercial.
func ParseSelector(selector string) (string, []string) {
	parts := strings.Split(strings.TrimSpace(selector), "+")
	base := strings.TrimSpace(parts[0])
	if base == "" {
		base = "commercial"
	}
	var overlays []string
	for _, p := range parts[1:] {
		if p = strings.TrimSpace(p); p != "" {
			overlays = append(overlays, p)
		}
	}
	return base, overlays
}

// FromEnv resolves the profile named by PASSAGE_CLOUD_PROFILE. A name it does not know is an error, so a
// typo fails closed instead of falling back to commercial.
func FromEnv(getenv func(string) string) (*Profile, error) {
	base, overlays := ParseSelector(getenv(EnvProfile))
	return Resolve(base, overlays...)
}

// Resolve applies overlays to a base profile. An overlay whose regions share nothing with the profile's is an error.
func Resolve(base string, overlays ...string) (*Profile, error) {
	d, err := Table()
	if err != nil {
		return nil, fmt.Errorf("cloudprofile: embedded table: %w", err)
	}
	p, ok := d.Profiles[base]
	if !ok {
		return nil, fmt.Errorf("cloudprofile: unknown cloud profile %q (known: %s)", base, strings.Join(ProfileNames(), ", "))
	}
	r := &Profile{
		Name: base, Overlays: slices.Clone(overlays), Cloud: p.Cloud, FIPS: p.FIPS,
		AllowedRegions: slices.Clone(p.AllowedRegions), Endpoints: map[string]string{},
		EntraAuthorityAliases: slices.Clone(p.Endpoints["entraAuthority"].Aliases),
		RequireMirror:         p.Registry.RequireMirror, RegistryDefault: p.Registry.Default,
		LLMProviders: slices.Clone(p.Egress.LLMProviders), EnforceProviders: p.Egress.EnforceProviders,
		PagerDutyAllowed: p.Egress.PagerDuty, Keycloak: p.Keycloak,
	}
	for k, e := range p.Endpoints {
		r.Endpoints[k] = e.Value
	}
	extra := slices.Clone(p.Egress.ExtraAllow)
	for _, name := range overlays {
		o, ok := d.Overlays[name]
		if !ok {
			return nil, fmt.Errorf("cloudprofile: unknown overlay %q", name)
		}
		if len(o.AllowedRegions) > 0 {
			if len(r.AllowedRegions) == 0 {
				r.AllowedRegions = slices.Clone(o.AllowedRegions)
			} else {
				r.AllowedRegions = slices.DeleteFunc(r.AllowedRegions, func(region string) bool { return !slices.Contains(o.AllowedRegions, region) })
			}
			if len(r.AllowedRegions) == 0 {
				return nil, fmt.Errorf("cloudprofile: overlay %s cannot be combined with %s: no region is allowed by both", name, base)
			}
		}
		r.FIPS = r.FIPS || o.RequireFIPS
		r.Keycloak.EventRetentionDays = max(r.Keycloak.EventRetentionDays, o.EventRetentionDays)
		for _, item := range o.EgressRemove {
			if item == "pagerDuty" {
				r.PagerDutyAllowed = false
			}
			r.LLMProviders = slices.DeleteFunc(r.LLMProviders, func(s string) bool { return s == item })
		}
	}
	r.EgressAllowlist = buildAllowlist(r, extra)
	return r, nil
}

var llmHosts = map[string]string{
	"anthropic": "api.anthropic.com",
	"bedrock":   "bedrock-runtime.*.amazonaws.com",
	"openai":    "api.openai.com",
}

func buildAllowlist(r *Profile, extra []string) []string {
	ep := r.Endpoints
	hosts := []string{HostOf(ep["entraAuthority"])}
	for _, a := range r.EntraAuthorityAliases {
		hosts = append(hosts, HostOf(a))
	}
	hosts = append(hosts, HostOf(ep["graphRoot"]), HostOf(ep["armEndpoint"]),
		"*."+ep["keyVaultSuffix"], "*."+ep["blobSuffix"], "*."+ep["containerRegistrySuffix"], "*."+ep["postgresSuffix"])
	for _, name := range r.LLMProviders {
		if name == "azure-openai" {
			hosts = append(hosts, "*."+ep["azureOpenAISuffix"])
		} else {
			hosts = append(hosts, llmHosts[name])
		}
	}
	if r.PagerDutyAllowed {
		hosts = append(hosts, "events.pagerduty.com")
	}
	if !r.RequireMirror {
		hosts = append(hosts, "ghcr.io")
	}
	hosts = append(hosts, extra...)
	var out []string
	for _, h := range hosts {
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// HostOf returns the host of a URL, or the input when it is already a bare host.
func HostOf(v string) string {
	if strings.Contains(v, "://") {
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return v
}

// HostMatches reports whether host matches an allowlist pattern: an exact host, "*.suffix" (one or more
// labels), or a host with "*" for a whole label ("bedrock-runtime.*.amazonaws.com").
func HostMatches(host, pattern string) bool {
	host, pattern = strings.ToLower(host), strings.ToLower(pattern)
	if host == pattern {
		return true
	}
	labels, plabels := strings.Split(host, "."), strings.Split(pattern, ".")
	if plabels[0] == "*" {
		rest := plabels[1:]
		return len(labels) > len(rest) && slices.Equal(labels[len(labels)-len(rest):], rest)
	}
	if len(labels) != len(plabels) {
		return false
	}
	for i := range labels {
		if plabels[i] != "*" && plabels[i] != labels[i] {
			return false
		}
	}
	return true
}

// AllowsHost reports whether the profile's egress allowlist covers host.
func (p *Profile) AllowsHost(host string) bool {
	return slices.ContainsFunc(p.EgressAllowlist, func(pattern string) bool { return HostMatches(host, pattern) })
}

// AllowsURL reports whether the URL's host is on the egress allowlist.
func (p *Profile) AllowsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Hostname() != "" && p.AllowsHost(u.Hostname())
}

// AllowsRegion reports whether a deployment may use region; a profile with no region list allows any.
func (p *Profile) AllowsRegion(region string) bool {
	return len(p.AllowedRegions) == 0 || slices.Contains(p.AllowedRegions, region)
}

// Endpoint returns one endpoint value, for example "graphRoot".
func (p *Profile) Endpoint(key string) string { return p.Endpoints[key] }

// Selector is the PASSAGE_CLOUD_PROFILE form of the profile, "gcc+hipaa".
func (p *Profile) Selector() string {
	return strings.Join(append([]string{p.Name}, p.Overlays...), "+")
}
