package tenant

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	HeaderServiceToken = "X-Passage-Service-Token"

	ServiceTokenMaxTTL = 60 * time.Second

	EnvServiceName        = "PASSAGE_SERVICE_NAME"
	EnvServicePublicKeys  = "PASSAGE_SERVICE_PUBLIC_KEYS"
	EnvServiceSigningKey  = "PASSAGE_SERVICE_SIGNING_KEY"
	EnvServiceSigningFile = "PASSAGE_SERVICE_SIGNING_KEY_FILE"

	serviceTokenContext = "passage.service-token.v1."
	serviceClockSkew    = 5 * time.Second
	maxReplayEntries    = 100_000
	maxServiceTokenSize = 4096
)

var (
	ErrServiceToken   = errors.New("invalid service token")
	ErrNoServiceGrant = errors.New("no service grant")
)

// serviceGrants is the static per-service duty table: caller -> target -> duties.
// policies/authz/data.json "service_grants" mirrors it and a test keeps them equal.
var serviceGrants = map[string]map[string][]string{
	"receiving": {
		"ledger": {"ledger.journal.post", "ledger.journal.reverse"},
		"close":  {"period.check"},
	},
	"payables": {
		"ledger": {"ledger.balance.read", "ledger.journal.read"},
	},
	"party": {
		"numbering": {"numbering.write"},
	},
}

// ServiceGrantTable returns a copy of the static grant table.
func ServiceGrantTable() map[string]map[string][]string {
	out := make(map[string]map[string][]string, len(serviceGrants))
	for iss, targets := range serviceGrants {
		out[iss] = make(map[string][]string, len(targets))
		for aud, duties := range targets {
			out[iss][aud] = slices.Clone(duties)
		}
	}
	return out
}

func serviceGrant(iss, aud string) []string { return serviceGrants[iss][aud] }

// ServiceClaims is the signed payload: iss calls aud for tenant tid in legal
// entity le on behalf of end user act, holding only the service's own duties.
type ServiceClaims struct {
	Iss    string   `json:"iss"`
	Aud    string   `json:"aud"`
	Tid    string   `json:"tid"`
	Le     string   `json:"le"`
	Act    string   `json:"act"`
	Duties []string `json:"duties"`
	Iat    int64    `json:"iat"`
	Exp    int64    `json:"exp"`
	Jti    string   `json:"jti"`
}

// ServiceSigner mints short-lived Ed25519 service tokens for one calling service.
type ServiceSigner struct {
	name string
	key  ed25519.PrivateKey
	now  func() time.Time
}

func NewServiceSigner(name string, key ed25519.PrivateKey) (*ServiceSigner, error) {
	if name == "" || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("service signer needs a name and an Ed25519 private key")
	}
	return &ServiceSigner{name: name, key: key, now: time.Now}, nil
}

// WithClock returns a copy of the signer reading time from now.
func (s *ServiceSigner) WithClock(now func() time.Time) *ServiceSigner {
	c := *s
	c.now = now
	return &c
}

// ServiceSignerFromEnv builds the signer from PASSAGE_SERVICE_NAME and the
// base64 32-byte seed in PASSAGE_SERVICE_SIGNING_KEY or the file named by
// PASSAGE_SERVICE_SIGNING_KEY_FILE (a mounted secret). It returns nil, nil when
// no signing key is configured so callers keep forwarding the end user.
func ServiceSignerFromEnv(getenv func(string) string) (*ServiceSigner, error) {
	raw := strings.TrimSpace(getenv(EnvServiceSigningKey))
	if file := getenv(EnvServiceSigningFile); raw == "" && file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", EnvServiceSigningFile, err)
		}
		raw = strings.TrimSpace(string(b))
	}
	if raw == "" {
		return nil, nil
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s must be a base64 %d-byte Ed25519 seed", EnvServiceSigningKey, ed25519.SeedSize)
	}
	return NewServiceSigner(getenv(EnvServiceName), ed25519.NewKeyFromSeed(seed))
}

// Mint signs a token for aud carrying the end user from p as act. The legal
// entity must be explicit: legalEntity when non-empty, else p's, and it must be
// exactly one legal entity.
func (s *ServiceSigner) Mint(aud string, p Principal, legalEntity string) (string, error) {
	duties := serviceGrant(s.name, aud)
	if len(duties) == 0 {
		return "", fmt.Errorf("%w: %s -> %s", ErrNoServiceGrant, s.name, aud)
	}
	if legalEntity == "" {
		legalEntity = p.LegalEntityID
	}
	le, ok := canonicalUUID(legalEntity)
	tid, tok := canonicalUUID(p.TenantID)
	if !ok || le == ids.Nil || !tok || tid == ids.Nil {
		return "", errors.New("service token needs one explicit legal entity and a tenant")
	}
	if p.UserID == "" {
		return "", errors.New("service token needs the end user as act")
	}
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	now := s.now()
	payload, err := json.Marshal(ServiceClaims{
		Iss: s.name, Aud: aud, Tid: tid, Le: le, Act: p.UserID, Duties: duties,
		Iat: now.Unix(), Exp: now.Add(ServiceTokenMaxTTL).Unix(), Jti: hex.EncodeToString(jti),
	})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(s.key, []byte(serviceTokenContext+body))
	return body + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Apply signs a token for aud and sets it on req together with the request id.
// A nil signer reports signed=false and touches nothing, so the caller can fall
// back to forwarding the end user's own headers.
func (s *ServiceSigner) Apply(req *http.Request, p Principal, aud, legalEntity string) (signed bool, err error) {
	if s == nil {
		return false, nil
	}
	token, err := s.Mint(aud, p, legalEntity)
	if err != nil {
		return false, err
	}
	req.Header.Set(HeaderServiceToken, token)
	if id := middleware.GetReqID(req.Context()); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}
	return true, nil
}

// ServiceVerifier checks service tokens addressed to one service.
type ServiceVerifier struct {
	self string
	keys map[string]ed25519.PublicKey
	now  func() time.Time
	mu   sync.Mutex
	seen map[string]int64
}

func NewServiceVerifier(self string, keys map[string]ed25519.PublicKey) *ServiceVerifier {
	return &ServiceVerifier{self: self, keys: keys, now: time.Now, seen: map[string]int64{}}
}

// WithClock returns a verifier sharing keys but with its own clock and replay cache.
func (v *ServiceVerifier) WithClock(now func() time.Time) *ServiceVerifier {
	c := NewServiceVerifier(v.self, v.keys)
	c.now = now
	return c
}

// ServiceVerifierFromEnv reads PASSAGE_SERVICE_NAME (this service, the expected
// aud) and PASSAGE_SERVICE_PUBLIC_KEYS (comma-separated name=base64 Ed25519
// public keys). It returns nil, nil when either is unset.
func ServiceVerifierFromEnv(getenv func(string) string) (*ServiceVerifier, error) {
	self, raw := getenv(EnvServiceName), strings.TrimSpace(getenv(EnvServicePublicKeys))
	if self == "" || raw == "" {
		return nil, nil
	}
	keys := map[string]ed25519.PublicKey{}
	for _, entry := range strings.Split(raw, ",") {
		name, enc, ok := strings.Cut(strings.TrimSpace(entry), "=")
		pub, err := base64.StdEncoding.DecodeString(enc)
		if !ok || name == "" || err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s entry %q is not name=base64 Ed25519 public key", EnvServicePublicKeys, name)
		}
		keys[name] = ed25519.PublicKey(pub)
	}
	return NewServiceVerifier(self, keys), nil
}

// Verify returns the claims of a token that is signed by a known service,
// addressed to this service, fresh, within the issuer's grant and not replayed.
func (v *ServiceVerifier) Verify(token string) (ServiceClaims, error) {
	var c ServiceClaims
	if v == nil || len(token) > maxServiceTokenSize {
		return c, ErrServiceToken
	}
	body, sigPart, ok := strings.Cut(token, ".")
	sig, serr := base64.RawURLEncoding.DecodeString(sigPart)
	payload, perr := base64.RawURLEncoding.DecodeString(body)
	if !ok || serr != nil || perr != nil {
		return c, ErrServiceToken
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return ServiceClaims{}, ErrServiceToken
	}
	pub, known := v.keys[c.Iss]
	if !known || !ed25519.Verify(pub, []byte(serviceTokenContext+body), sig) {
		return ServiceClaims{}, ErrServiceToken
	}
	now := v.now()
	if c.Aud != v.self || c.Jti == "" || c.Act == "" ||
		c.Exp <= now.Unix() || c.Exp-c.Iat > int64(ServiceTokenMaxTTL/time.Second) || c.Iat > now.Add(serviceClockSkew).Unix() {
		return ServiceClaims{}, ErrServiceToken
	}
	if tid, ok := canonicalUUID(c.Tid); !ok || tid != c.Tid || tid == ids.Nil {
		return ServiceClaims{}, ErrServiceToken
	}
	if le, ok := canonicalUUID(c.Le); !ok || le != c.Le || le == ids.Nil {
		return ServiceClaims{}, ErrServiceToken
	}
	grant := serviceGrant(c.Iss, c.Aud)
	if len(c.Duties) == 0 || slices.ContainsFunc(c.Duties, func(d string) bool { return d == DutyAllScope || !slices.Contains(grant, d) }) {
		return ServiceClaims{}, ErrServiceToken
	}
	if !v.remember(c.Jti, c.Exp, now) {
		return ServiceClaims{}, ErrServiceToken
	}
	return c, nil
}

// ponytail: replay memory is per process, so a token replays at most once per replica within its 60s life; a shared store is the upgrade.
func (v *ServiceVerifier) remember(jti string, exp int64, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, replayed := v.seen[jti]; replayed {
		return false
	}
	if len(v.seen) >= maxReplayEntries {
		for k, e := range v.seen {
			if e <= now.Unix() {
				delete(v.seen, k)
			}
		}
		if len(v.seen) >= maxReplayEntries {
			return false
		}
	}
	v.seen[jti] = exp
	return true
}

// Principal is the caller identity a verified token stands for: the service's
// own duties, the token's legal entity, and the end user as the actor.
func (c ServiceClaims) Principal() Principal {
	duties := make(map[string]struct{}, len(c.Duties))
	for _, d := range c.Duties {
		duties[d] = struct{}{}
	}
	return Principal{TenantID: c.Tid, LegalEntityID: c.Le, UserID: c.Act, Duties: duties, Service: c.Iss}
}

var (
	activeVerifier  atomic.Pointer[ServiceVerifier]
	envVerifierOnce sync.Once
)

// SetServiceVerifier replaces the verifier Middleware uses; nil makes every
// service token fail. Tests and explicit wiring use it instead of the environment.
func SetServiceVerifier(v *ServiceVerifier) {
	envVerifierOnce.Do(func() {})
	activeVerifier.Store(v)
}

func currentServiceVerifier() *ServiceVerifier {
	envVerifierOnce.Do(func() {
		v, err := ServiceVerifierFromEnv(os.Getenv)
		if err != nil {
			slog.Error("service token verifier misconfigured; service tokens will be rejected", "err", err)
		}
		activeVerifier.Store(v)
	})
	return activeVerifier.Load()
}
