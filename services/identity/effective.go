package identity

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const recursiveSuffix = "{,/**}"

type compiledPermission struct {
	pattern *regexp.Regexp
	methods []string
}

type DutyPermission struct {
	Resource string   `json:"resource"`
	Methods  []string `json:"methods"`
}

type catalogueDuty struct {
	permissions []compiledPermission
	declared    []DutyPermission
	size        int
	serviceOnly bool
}

func (d *catalogueDuty) grants(method, resource string) bool {
	for _, p := range d.permissions {
		if slices.Contains(p.methods, method) && p.pattern.MatchString(resource) {
			return true
		}
	}
	return false
}

// Route is one resource pattern and HTTP method declared by a seeded duty.
type Route struct {
	Resource string
	Method   string
}

// Grant is the engine's decision for one route. Duty names the lexically first
// held duty that grants it; DutyMissing mirrors the policy's least-privileged
// duty that would grant a denied route.
type Grant struct {
	Resource    string
	Method      string
	Allow       bool
	Duty        string
	DutyMissing string
}

// Catalogue is the duty and route catalogue loaded from policies/authz/data.json.
// It mirrors authz.rego: a duty grants a request when one of its permissions
// lists the method and its glob (delimiters "/" and ":") matches the resource.
type Catalogue struct {
	duties map[string]*catalogueDuty
	names  []string
	routes []Route
}

type catalogueFile struct {
	Duties map[string]struct {
		ServiceOnly bool `json:"service_only"`
		Permissions []struct {
			Resource string   `json:"resource"`
			Methods  []string `json:"methods"`
		} `json:"permissions"`
	} `json:"duties"`
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	base, recursive := strings.CutSuffix(pattern, recursiveSuffix)
	if strings.ContainsAny(base, "?[]{}\\") || strings.Contains(base, "**") {
		return nil, fmt.Errorf("unsupported glob %q", pattern)
	}
	parts := strings.Split(base, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	expr := "(?s)^" + strings.Join(parts, `[^/:]*`)
	if recursive {
		expr += `(/.*)?`
	}
	return regexp.Compile(expr + "$")
}

// LoadCatalogue parses the policy data file. A glob outside the supported
// subset (literal text, "*", and a trailing "{,/**}") is an error so the engine
// can never disagree with the policy silently.
func LoadCatalogue(raw []byte) (*Catalogue, error) {
	var file catalogueFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse duty catalogue: %w", err)
	}
	c := &Catalogue{duties: map[string]*catalogueDuty{}}
	seen := map[Route]bool{}
	for name, spec := range file.Duties {
		duty := &catalogueDuty{serviceOnly: spec.ServiceOnly}
		for _, p := range spec.Permissions {
			pattern, err := compileGlob(p.Resource)
			if err != nil {
				return nil, fmt.Errorf("duty %q: %w", name, err)
			}
			duty.permissions = append(duty.permissions, compiledPermission{pattern: pattern, methods: p.Methods})
			duty.size += len(p.Methods)
			duty.declared = append(duty.declared, DutyPermission{Resource: p.Resource, Methods: slices.Clone(p.Methods)})
			for _, method := range p.Methods {
				if route := (Route{p.Resource, method}); !seen[route] {
					seen[route] = true
					c.routes = append(c.routes, route)
				}
			}
		}
		c.duties[name] = duty
		c.names = append(c.names, name)
	}
	sort.Strings(c.names)
	sort.Slice(c.routes, func(i, j int) bool {
		if c.routes[i].Resource != c.routes[j].Resource {
			return c.routes[i].Resource < c.routes[j].Resource
		}
		return c.routes[i].Method < c.routes[j].Method
	})
	return c, nil
}

// HasDuty reports whether name is a seeded duty.
func (c *Catalogue) HasDuty(name string) bool { _, ok := c.duties[name]; return ok }

// ServiceOnly reports whether the duty is held only through a service grant
// and must never be assigned to a person.
func (c *Catalogue) ServiceOnly(name string) bool {
	d, ok := c.duties[name]
	return ok && d.serviceOnly
}

// DutyInfo describes one seeded duty and the routes it grants.
type DutyInfo struct {
	Name        string
	ServiceOnly bool
	Permissions []DutyPermission
}

// Duties lists every seeded duty sorted by name.
func (c *Catalogue) Duties() []DutyInfo {
	out := make([]DutyInfo, len(c.names))
	for i, name := range c.names {
		out[i], _ = c.Duty(name)
	}
	return out
}

// Duty returns the named duty and whether it is seeded.
func (c *Catalogue) Duty(name string) (DutyInfo, bool) {
	d, ok := c.duties[name]
	if !ok {
		return DutyInfo{}, false
	}
	return DutyInfo{Name: name, ServiceOnly: d.serviceOnly, Permissions: slices.Clone(d.declared)}, true
}

// Routes lists every distinct resource pattern and method, sorted.
func (c *Catalogue) Routes() []Route { return slices.Clone(c.routes) }

// SampleResource turns a catalogue pattern into a concrete request path.
func SampleResource(pattern string) string {
	if base, ok := strings.CutSuffix(pattern, recursiveSuffix); ok {
		return base + "/item-1"
	}
	return strings.ReplaceAll(pattern, "*", "item-1")
}

// Evaluate decides one concrete request for a principal holding duties.
func (c *Catalogue) Evaluate(duties []string, method, resource string) Grant {
	grant := Grant{Resource: resource, Method: method}
	for _, name := range slices.Sorted(slices.Values(duties)) {
		if duty, ok := c.duties[name]; ok && duty.grants(method, resource) {
			grant.Allow, grant.Duty = true, name
			return grant
		}
	}
	grant.DutyMissing = c.leastPrivileged(method, resource, false)
	if grant.DutyMissing == "" {
		grant.DutyMissing = c.leastPrivileged(method, resource, true)
	}
	return grant
}

// leastPrivileged names the smallest duty granting the request among duties of
// the given kind; a person is only pointed at service-only duties when no
// person duty grants the route.
func (c *Catalogue) leastPrivileged(method, resource string, serviceOnly bool) string {
	smallest, best := -1, ""
	for _, name := range c.names {
		duty := c.duties[name]
		if duty.serviceOnly == serviceOnly && duty.grants(method, resource) && (smallest < 0 || duty.size < smallest) {
			smallest, best = duty.size, name
		}
	}
	return best
}

// Effective evaluates every catalogue route, at its sample resource, for a
// principal holding duties. Duties absent from the catalogue grant nothing.
func (c *Catalogue) Effective(duties []string) []Grant {
	grants := make([]Grant, len(c.routes))
	for i, route := range c.routes {
		grants[i] = c.Evaluate(duties, route.Method, SampleResource(route.Resource))
		grants[i].Resource = route.Resource
	}
	return grants
}
