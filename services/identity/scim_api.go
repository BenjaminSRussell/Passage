package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	mediaSCIM    = "application/scim+json"
	scimBase     = "/scim/v2"
	schemaUser   = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup  = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaList   = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaError  = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaPatch  = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaConfig = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"

	scimDefaultCount = 100
	scimMaxCount     = 200
	scimMaxBody      = 1 << 20
	scimMaxAttr      = 255
	scimMaxEmail     = 320
)

// SCIMAPI serves SCIM 2.0 provisioning (RFC 7643/7644) for Users and Groups under /scim/v2. Every request carries
// a bearer JWT verified against the token's tenant, then the policy decides on the request method and path. SCIM
// groups are provisioning records only: they are not mapped to roles, because role assignments are dated,
// scoped and audited, and a group sync must not grant access.
type SCIMAPI struct {
	pool      *pgxpool.Pool
	verifiers *TenantVerifiers
	opa       *OPAClient
}

func NewSCIMAPI(pool *pgxpool.Pool, verifiers *TenantVerifiers, opa *OPAClient) *SCIMAPI {
	return &SCIMAPI{pool: pool, verifiers: verifiers, opa: opa}
}

func (a *SCIMAPI) Register(r chi.Router) {
	r.Route(scimBase, func(r chi.Router) {
		r.Use(scimProblems, a.verifiers.Middleware, RequireOPAForRequest(a.opa))
		r.Get("/ServiceProviderConfig", func(w http.ResponseWriter, _ *http.Request) { writeSCIM(w, http.StatusOK, serviceProviderConfig) })
		r.Get("/Schemas", listResources(schemaDefinitions))
		r.Get("/Schemas/{id}", getResource(schemaDefinitions))
		r.Get("/ResourceTypes", listResources(resourceTypes))
		r.Get("/ResourceTypes/{id}", getResource(resourceTypes))
		r.Get("/Users", a.listUsers)
		r.Post("/Users", a.createUser)
		r.Get("/Users/{id}", a.getUser)
		r.Put("/Users/{id}", a.replaceUser)
		r.Patch("/Users/{id}", a.patchUser)
		r.Delete("/Users/{id}", a.deleteUser)
		r.Get("/Groups", a.listGroups)
		r.Post("/Groups", a.createGroup)
		r.Get("/Groups/{id}", a.getGroup)
		r.Put("/Groups/{id}", a.replaceGroup)
		r.Patch("/Groups/{id}", a.patchGroup)
		r.Delete("/Groups/{id}", a.deleteGroup)
	})
}

type scimError struct {
	status   int
	scimType string
	detail   string
}

func (e *scimError) Error() string {
	return strconv.Itoa(e.status) + " " + e.scimType + ": " + e.detail
}

func badRequest(scimType, detail string) *scimError {
	return &scimError{status: http.StatusBadRequest, scimType: scimType, detail: detail}
}

func scimNotFound(what string) *scimError {
	return &scimError{status: http.StatusNotFound, detail: what + " not found"}
}

func scimConflict(detail string) *scimError {
	return &scimError{status: http.StatusConflict, scimType: "uniqueness", detail: detail}
}

func writeSCIM(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", mediaSCIM)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func scimFail(w http.ResponseWriter, r *http.Request, err error) {
	var e *scimError
	if !errors.As(err, &e) {
		slog.Error("scim request failed", "err", err, "path", r.URL.Path)
		e = &scimError{status: http.StatusInternalServerError, detail: "internal error"}
	}
	if e.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	body := map[string]any{"schemas": []string{schemaError}, "status": strconv.Itoa(e.status), "detail": e.detail}
	if e.scimType != "" {
		body["scimType"] = e.scimType
	}
	writeSCIM(w, e.status, body)
}

// inTx runs fn in a transaction for the verified caller's tenant with an explicit tenant-wide ('*') legal-entity
// scope. Only the /scim/v2 routes reach it, behind the bearer and policy middleware; the SCIM principal is never
// given the tenant.administer duty the admin APIs require.
func (a *SCIMAPI) inTx(r *http.Request, fn func(tx pgx.Tx) error) error {
	tenantID := scimPrincipal(r).TenantID
	return pgx.BeginFunc(r.Context(), a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func scimPrincipal(r *http.Request) tenant.Principal {
	p, _ := tenant.FromContext(r.Context())
	return p
}

func decodeSCIMObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, scimMaxBody)).Decode(&body); err != nil || body == nil {
		return nil, bodyError(err, "the request body must be a JSON object")
	}
	return body, nil
}

func bodyError(err error, detail string) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &scimError{status: http.StatusRequestEntityTooLarge, scimType: "tooLarge", detail: "the request body exceeds 1 MiB"}
	}
	return badRequest("invalidSyntax", detail)
}

type listResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    any      `json:"Resources"`
}

func newList(total, start, count int, resources any) listResponse {
	return listResponse{Schemas: []string{schemaList}, TotalResults: total, StartIndex: start, ItemsPerPage: count, Resources: resources}
}

func scimPaging(r *http.Request) (start, count int, err error) {
	start, count = 1, scimDefaultCount
	q := r.URL.Query()
	if raw := q.Get("startIndex"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, 0, badRequest("invalidValue", "startIndex must be an integer")
		}
		start = max(n, 1)
	}
	if raw := q.Get("count"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, 0, badRequest("invalidValue", "count must be an integer")
		}
		count = min(max(n, 0), scimMaxCount)
	}
	return start, count, nil
}

var eqFilter = regexp.MustCompile(`(?i)^\s*([a-z]+)\s+eq\s+("(?:[^"\\]|\\.)*")\s*$`)

func scimFilter(r *http.Request, allowed ...string) (attr, value string, err error) {
	raw := r.URL.Query().Get("filter")
	if raw == "" {
		return "", "", nil
	}
	m := eqFilter.FindStringSubmatch(raw)
	if m == nil || json.Unmarshal([]byte(m[2]), &value) != nil {
		return "", "", badRequest("invalidFilter", `only the form: attribute eq "value" is supported`)
	}
	attr = strings.ToLower(m[1])
	for _, name := range allowed {
		if attr == strings.ToLower(name) {
			return attr, value, nil
		}
	}
	return "", "", badRequest("invalidFilter", "filtering is supported on "+strings.Join(allowed, ", ")+" only")
}

type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func decodePatch(w http.ResponseWriter, r *http.Request) ([]patchOp, error) {
	var req struct {
		Operations []patchOp `json:"Operations"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, scimMaxBody)).Decode(&req); err != nil {
		return nil, bodyError(err, "the request body must be a SCIM PatchOp")
	}
	if len(req.Operations) == 0 {
		return nil, badRequest("invalidValue", "Operations must not be empty")
	}
	for i := range req.Operations {
		op := strings.ToLower(req.Operations[i].Op)
		if op != "add" && op != "replace" && op != "remove" {
			return nil, badRequest("invalidValue", "op must be add, replace or remove")
		}
		req.Operations[i].Op = op
	}
	return req.Operations, nil
}

var jsonNull = json.RawMessage("null")

func attrName(path string) string {
	if strings.HasPrefix(path, "urn:") {
		path = path[strings.LastIndexByte(path, ':')+1:]
	}
	if i := strings.IndexByte(path, '['); i >= 0 {
		if j := strings.IndexByte(path, ']'); j > i {
			path = path[:i] + path[j+1:]
		}
	}
	return strings.ToLower(path)
}

func applyOperations(ops []patchOp, set func(op, attr string, raw json.RawMessage) error) error {
	for _, op := range ops {
		value := op.Value
		if len(value) == 0 && op.Op == "remove" {
			value = jsonNull
		}
		if op.Path != "" {
			if err := set(op.Op, attrName(op.Path), value); err != nil {
				return err
			}
			continue
		}
		var attrs map[string]json.RawMessage
		if json.Unmarshal(value, &attrs) != nil || attrs == nil {
			return badRequest("invalidValue", "an operation without a path needs an object value")
		}
		for key, raw := range attrs {
			if err := set(op.Op, attrName(key), raw); err != nil {
				return err
			}
		}
	}
	return nil
}

func rawString(raw json.RawMessage) (string, error) {
	var s *string
	if json.Unmarshal(raw, &s) != nil {
		return "", badRequest("invalidValue", "a string value is required")
	}
	if s == nil {
		return "", nil
	}
	return strings.TrimSpace(*s), nil
}

func tooLong(name, value string, limit int) error {
	if utf8.RuneCountInString(value) > limit {
		return badRequest("invalidValue", name+" must be at most "+strconv.Itoa(limit)+" characters")
	}
	return nil
}

func scimLocation(kind, id string) string {
	return scimBase + "/" + kind + "/" + url.PathEscape(id)
}

type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	Location     string `json:"location"`
}

type scimRef struct {
	Value   string `json:"value"`
	Display string `json:"display,omitempty"`
	Ref     string `json:"$ref,omitempty"`
}

type problemCapture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *problemCapture) WriteHeader(status int) {
	if c.Header().Get("Content-Type") == "application/problem+json" {
		c.status = status
		return
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *problemCapture) Write(b []byte) (int, error) {
	if c.status != 0 {
		return c.body.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// scimProblems rewrites the problem+json bodies written by the shared bearer and policy middleware into
// the SCIM error format, so a SCIM client sees one error shape on every response.
func scimProblems(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture := &problemCapture{ResponseWriter: w}
		next.ServeHTTP(capture, r)
		if capture.status == 0 {
			return
		}
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(capture.body.Bytes(), &problem)
		w.Header().Del("Content-Type")
		scimFail(w, r, &scimError{status: capture.status, detail: problem.Detail})
	})
}
