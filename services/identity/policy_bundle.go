package identity

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const (
	PolicyBundlePath = "/v1/identity/policy/bundle"

	bundleDataFile      = "data.json"
	bundleRegoFile      = "authz.rego"
	bundleManifestFile  = ".manifest"
	bundleSignatureFile = ".signatures.json"
	bundleCacheTTL      = 15 * time.Second
	bundleRevisionLen   = 16
)

var errOperatorNotRegistered = errors.New("operator tenant is not registered in identity_operator; refusing to publish an empty policy")

var bundleRoots = []string{"authz", "assignments", "duties", "service_grants"}

const compiledAssignmentsQuery = `select a.tenant_id::text, u.idp_subject, a.role
from identity_role_assignments a
join identity_users u on u.tenant_id = a.tenant_id and u.user_id = a.user_id
join identity_tenants t on t.tenant_id = a.tenant_id
where a.legal_entity_id is null
  and u.status = 'active' and t.status = 'active'
  and a.valid_from <= now() and (a.valid_to is null or a.valid_to > now())
order by 1, 2, 3`

type BundleSigner struct {
	key   *rsa.PrivateKey
	keyID string
}

const minBundleKeyBits = 2048

func NewBundleSigner(privateKeyPEM []byte, keyID string) (*BundleSigner, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil || keyID == "" {
		return nil, errors.New("bundle signing key must be a PEM RSA private key with a key id")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, pkcs8Err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if pkcs8Err != nil {
			return nil, fmt.Errorf("parse bundle signing key: %w", pkcs8Err)
		}
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("bundle signing key must be RSA")
		}
		key = rsaKey
	}
	if key.N.BitLen() < minBundleKeyBits {
		return nil, fmt.Errorf("bundle signing key must be at least %d bits", minBundleKeyBits)
	}
	return &BundleSigner{key: key, keyID: keyID}, nil
}

type signedFile struct {
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	Algorithm string `json:"algorithm"`
}

func (s *BundleSigner) sign(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	listed := make([]signedFile, len(names))
	for i, name := range names {
		sum := sha256.Sum256(files[name])
		listed[i] = signedFile{Name: name, Hash: hex.EncodeToString(sum[:]), Algorithm: "SHA-256"}
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": s.keyID, "typ": "JWT"})
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"files": listed, "keyid": s.keyID})
	if err != nil {
		return nil, err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string][]string{"signatures": {signingInput + "." + enc.EncodeToString(signature)}})
}

type PolicyBundle struct {
	Revision string
	Archive  []byte
}

type PolicyBundleCompiler struct {
	pool             *pgxpool.Pool
	catalogue        *Catalogue
	catalogueRaw     []byte
	rego             []byte
	operatorTenantID string
	signer           *BundleSigner
}

func NewPolicyBundleCompiler(pool *pgxpool.Pool, catalogue *Catalogue, catalogueRaw, rego []byte, operatorTenantID string, signer *BundleSigner) (*PolicyBundleCompiler, error) {
	if pool == nil || catalogue == nil || len(rego) == 0 || signer == nil || !validUUID(operatorTenantID) {
		return nil, errors.New("policy bundle compiler needs a pool, catalogue, rego source, signer and operator tenant id")
	}
	return &PolicyBundleCompiler{pool: pool, catalogue: catalogue, catalogueRaw: catalogueRaw, rego: rego, operatorTenantID: operatorTenantID, signer: signer}, nil
}

func (c *PolicyBundleCompiler) assignments(ctx context.Context) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	err := pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true), set_config('passage.policy_compile', 'on', true)`, c.operatorTenantID); err != nil {
			return err
		}
		var operators int
		if err := tx.QueryRow(ctx, `select count(*) from identity_operator where tenant_id = $1`, c.operatorTenantID).Scan(&operators); err != nil {
			return err
		}
		if operators != 1 {
			return errOperatorNotRegistered
		}
		rows, err := tx.Query(ctx, compiledAssignmentsQuery)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var tenantID, subject, role string
			if err := rows.Scan(&tenantID, &subject, &role); err != nil {
				return err
			}
			if !c.catalogue.PersonAssignable(role) {
				continue
			}
			if out[tenantID] == nil {
				out[tenantID] = map[string][]string{}
			}
			out[tenantID][subject] = append(out[tenantID][subject], role)
		}
		return rows.Err()
	})
	return out, err
}

func (c *PolicyBundleCompiler) Compile(ctx context.Context) (PolicyBundle, error) {
	assignments, err := c.assignments(ctx)
	if err != nil {
		return PolicyBundle{}, fmt.Errorf("read role assignments: %w", err)
	}
	data, err := c.dataFile(assignments)
	if err != nil {
		return PolicyBundle{}, err
	}
	digest := sha256.New()
	digest.Write(data)
	digest.Write(c.rego)
	revision := hex.EncodeToString(digest.Sum(nil))[:bundleRevisionLen]
	manifest, err := json.Marshal(map[string]any{"revision": revision, "roots": bundleRoots, "rego_version": 1})
	if err != nil {
		return PolicyBundle{}, err
	}
	files := map[string][]byte{bundleDataFile: data, bundleRegoFile: c.rego, bundleManifestFile: manifest}
	signatures, err := c.signer.sign(files)
	if err != nil {
		return PolicyBundle{}, fmt.Errorf("sign bundle: %w", err)
	}
	files[bundleSignatureFile] = signatures
	archive, err := tarGzip(files)
	if err != nil {
		return PolicyBundle{}, err
	}
	return PolicyBundle{Revision: revision, Archive: archive}, nil
}

func (c *PolicyBundleCompiler) dataFile(assignments map[string]map[string][]string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(c.catalogueRaw))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse duty catalogue: %w", err)
	}
	doc["assignments"] = assignments
	return json.Marshal(doc)
}

func tarGzip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name]))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type PolicyBundleAPI struct {
	compiler *PolicyBundleCompiler
	token    [sha256.Size]byte
	now      func() time.Time

	mu       sync.Mutex
	cached   PolicyBundle
	cachedAt time.Time
}

func NewPolicyBundleAPI(compiler *PolicyBundleCompiler, token string) (*PolicyBundleAPI, error) {
	if compiler == nil || len(token) < 32 {
		return nil, errors.New("policy bundle API needs a compiler and a bearer token of at least 32 characters")
	}
	return &PolicyBundleAPI{compiler: compiler, token: sha256.Sum256([]byte(token)), now: time.Now}, nil
}

func (a *PolicyBundleAPI) Register(r chi.Router) { r.Get(PolicyBundlePath, a.get) }

func (a *PolicyBundleAPI) authorized(r *http.Request) bool {
	presented, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(presented))
	return found && subtle.ConstantTimeCompare(sum[:], a.token[:]) == 1
}

func (a *PolicyBundleAPI) bundle(ctx context.Context) (PolicyBundle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cached.Revision != "" && a.now().Sub(a.cachedAt) < bundleCacheTTL {
		return a.cached, nil
	}
	compiled, err := a.compiler.Compile(ctx)
	if err != nil {
		return PolicyBundle{}, err
	}
	a.cached, a.cachedAt = compiled, a.now()
	return compiled, nil
}

func (a *PolicyBundleAPI) get(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "invalid bundle credentials"))
		return
	}
	compiled, err := a.bundle(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "compile policy bundle", "err", err)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "IDN-BUNDLE-UNAVAILABLE", "policy bundle is unavailable"))
		return
	}
	etag := `"` + compiled.Revision + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(compiled.Archive)
}
