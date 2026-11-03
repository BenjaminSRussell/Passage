package tenant

import (
	"container/heap"
	"context"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	HeaderServiceToken = "X-Passage-Service-Token"

	ServiceTokenMaxTTL = 60 * time.Second

	EnvServiceName        = "PASSAGE_SERVICE_NAME"
	EnvServicePublicKeys  = "PASSAGE_SERVICE_PUBLIC_KEYS"
	EnvServiceSigningKey  = "PASSAGE_SERVICE_SIGNING_KEY"
	EnvServiceSigningFile = "PASSAGE_SERVICE_SIGNING_KEY_FILE"
	EnvServiceKeyID       = "PASSAGE_SERVICE_KEY_ID"
	EnvServiceReplaySize  = "PASSAGE_SERVICE_REPLAY_CACHE_SIZE"

	serviceTokenContext  = "passage.service-token.v1."
	serviceClockSkew     = 5 * time.Second
	defaultReplayEntries = 100_000
	maxServiceTokenSize  = 4096
	sharedReplayTimeout  = 2 * time.Second
)

var (
	ErrServiceToken   = errors.New("invalid service token")
	ErrNoServiceGrant = errors.New("no service grant")

	ErrReplayStoreUnavailable = errors.New("service token replay store unavailable")
)

// serviceGrants is the static per-service duty table: caller -> target -> duties.
// Each grant holds only the narrow duties its caller signs (#333, #341).
// policies/authz/data.json "service_grants" mirrors it and a test keeps them equal.
var serviceGrants = map[string]map[string][]string{
	"billing": {
		"ledger": {"ledger.entry.post", "ledger.ledger.lookup"},
		"revrec": {"revrec.allocation.read"},
		"sales":  {"sales.order.record-invoice", "sales.order.record-delivery"},
	},
	"fulfillment": {
		"sales":     {"sales.order.record-delivery"},
		"inventory": {"inventory.read"},
	},
	"batch": {
		"product": {"product.variants.explode", "product.released.read", "product.jobs.write"},
	},
	"receiving": {
		"ledger":     {"ledger.entry.post", "ledger.journal.reverse"},
		"close":      {"period.check"},
		"purchasing": {"procurement.invoicing-plan.settle", "procurement.purchase-order.lookup"},
		"inventory":  {"inventory.ownership-change.read"},
		"party":      {"party.read"},
		"compliance": {"compliance.legal_hold.check"},
	},
	"tax": {
		"ledger": {"ledger.balance.read"},
	},
	"payables": {
		"ledger": {"ledger.balance.read", "ledger.ledger.lookup", "ledger.entry.lookup", "ledger.entry.post"},
		"fx":     {"fx.rate.lookup"},
		"tax":    {"Calculate withholding tax"},
		"party":  {"party.vendor_bank.reveal"},
	},
	"close": {
		"ledger": {"ledger.ledger.lookup", "ledger.entry.post", "ledger.journal.reverse", "ledger.balance.read", "ledger.account.lookup", "ledger.period.postclosed"},
	},
	"production": {
		"ledger":    {"ledger.entry.post", "ledger.balance.read"},
		"inventory": {"inventory.read"},
	},
	"productcost": {
		"ledger":      {"ledger.entry.post", "ledger.journal.reverse"},
		"inventory":   {"inventory.read"},
		"engineering": {"engineering.read"},
	},
	"costing": {
		"ledger": {"ledger.entry.post", "ledger.balance.read", "ledger.ledger.lookup"},
	},
	"eam": {
		"ledger": {"ledger.entry.post", "ledger.balance.read"},
	},
	"projects": {
		"ledger": {"ledger.entry.post", "ledger.balance.read"},
	},
	"revrec": {
		"ledger": {"ledger.entry.post", "ledger.balance.read"},
	},
	"costacct": {
		"ledger": {"ledger.balance.read", "ledger.ledger.lookup"},
	},
	"assets": {
		"ledger": {"ledger.entry.post", "ledger.balance.read"},
	},
	"payroll": {
		"ledger": {"ledger.entry.post", "ledger.balance.read", "ledger.ledger.lookup"},
	},
	"sales": {
		"product":    {"product.configuration.read"},
		"production": {"production.order.create-from-sales"},
	},
	"ledger": {
		"close": {"period.check"},
		"fx":    {"fx.rate.lookup"},
	},
	"analytics": {
		"ledger": {"ledger.entry.lookup", "ledger.ledger.lookup"},
	},
	"workflow-ts": {
		"payables":    {"payment-run.execute"},
		"engineering": {"engineering.eco.workflow"},
		"quality":     {"quality.capa.execute"},
		"transport":   {"transport.tender.execute"},
	},
	"party": {
		"numbering": {"numbering.sequence.allocate"},
	},
	"quality": {
		"inventory": {"inventory.hold.apply"},
	},
	"warehouse": {
		"inventory": {"inventory.read"},
	},
	"hr": {
		"compliance": {"compliance.legal_hold.check"},
		"numbering":  {"numbering.sequence.allocate"},
	},
	"product": {
		"org": {"org.read"},
	},
	"migration": {
		"party":       {"party.import.write"},
		"product":     {"product.import.write"},
		"receivables": {"receivables.import.write"},
	},
	"migration-factory": {
		"receivables": {"receivables.import.write", "ARView"},
	},
	"integration-hub": {
		"migration": {"migration.outbound.render", "migration.xref.record"},
	},
	"edi": {
		"fulfillment": {"fulfillment.shipment.read"},
		"billing":     {"billing.invoice.read"},
		"sales":       {"sales.order.lookup"},
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
	Kid    string   `json:"kid,omitempty"`
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
	kid  string
	key  ed25519.PrivateKey
	now  func() time.Time
}

func NewServiceSigner(name string, key ed25519.PrivateKey) (*ServiceSigner, error) {
	if name == "" || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("service signer needs a name and an Ed25519 private key")
	}
	return &ServiceSigner{name: name, key: key, now: time.Now}, nil
}

// WithKeyID returns a copy of the signer that stamps kid on its tokens so a
// target holding several keys for this service can pick the right one.
func (s *ServiceSigner) WithKeyID(kid string) *ServiceSigner {
	c := *s
	c.kid = kid
	return &c
}

// WithClock returns a copy of the signer reading time from now.
func (s *ServiceSigner) WithClock(now func() time.Time) *ServiceSigner {
	c := *s
	c.now = now
	return &c
}

// ServiceSignerFromEnv builds the signer from PASSAGE_SERVICE_NAME and the
// base64 32-byte seed in PASSAGE_SERVICE_SIGNING_KEY or the file named by
// PASSAGE_SERVICE_SIGNING_KEY_FILE (a mounted secret that wins over the
// variable, so a stale variable never shadows a rotated mount). It returns nil, nil when
// no signing key is configured so callers keep forwarding the end user.
func ServiceSignerFromEnv(getenv func(string) string) (*ServiceSigner, error) {
	raw := strings.TrimSpace(getenv(EnvServiceSigningKey))
	if file := getenv(EnvServiceSigningFile); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", EnvServiceSigningFile, err)
		}
		raw = strings.TrimSpace(string(b))
		if raw == "" {
			return nil, fmt.Errorf("%s names an empty file; refusing to run unsigned", EnvServiceSigningFile)
		}
	}
	if raw == "" {
		return nil, nil
	}
	seed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s must be a base64 %d-byte Ed25519 seed", EnvServiceSigningKey, ed25519.SeedSize)
	}
	signer, err := NewServiceSigner(getenv(EnvServiceName), ed25519.NewKeyFromSeed(seed))
	if err != nil {
		return nil, err
	}
	return signer.WithKeyID(getenv(EnvServiceKeyID)), nil
}

// Mint signs a token for aud carrying the end user from p as act. The legal
// entity must be explicit: legalEntity when non-empty, else p's, and it must be
// exactly one legal entity.
func (s *ServiceSigner) Mint(aud string, p Principal, legalEntity string) (string, error) {
	return s.MintDuties(aud, p, legalEntity, nil)
}

// MintDuties is Mint restricted to the given duties: the token carries the
// intersection of duties and the service's grant for aud, never more than the
// grant. A nil duties means the whole grant.
func (s *ServiceSigner) MintDuties(aud string, p Principal, legalEntity string, only []string) (string, error) {
	duties := serviceGrant(s.name, aud)
	if only != nil {
		duties = slices.DeleteFunc(slices.Clone(duties), func(d string) bool { return !slices.Contains(only, d) })
	}
	if len(duties) == 0 {
		return "", fmt.Errorf("%w: %s -> %s", ErrNoServiceGrant, s.name, aud)
	}
	le, ok := s.legalEntityFor(p, legalEntity, duties)
	tid, tok := canonicalUUID(p.TenantID)
	if !ok || !tok || tid == ids.Nil {
		return "", errors.New("service token needs a tenant and one legal entity within the end user's scope")
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
		Iss: s.name, Kid: s.kid, Aud: aud, Tid: tid, Le: le, Act: p.UserID, Duties: duties,
		Iat: now.Unix(), Exp: now.Add(ServiceTokenMaxTTL).Unix(), Jti: hex.EncodeToString(jti),
	})
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(s.key, []byte(serviceTokenContext+body))
	return body + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// legalEntityFor resolves the token's legal entity: the explicit one must lie
// in the end user's legal-entity scope (or the user has no entity and holds the all-scope
// duty, which only survives on explicit ALL-scope reads, and the grant
// holds only read duties); with none given the user
// must be scoped to exactly one.
func (s *ServiceSigner) legalEntityFor(p Principal, explicit string, duties []string) (string, bool) {
	scopes := LegalEntityScopeList(p)
	if explicit == "" {
		if len(scopes) != 1 {
			return "", false
		}
		return scopes[0], true
	}
	le, ok := canonicalUUID(explicit)
	if !ok || le == ids.Nil {
		return "", false
	}
	return le, (p.LegalEntityID == "" && p.HasDuty(DutyAllScope) && onlyReads(duties)) || slices.Contains(scopes, le)
}

var readDuties = map[string]struct{}{
	"ledger.balance.read": {}, "ledger.ledger.lookup": {}, "ledger.account.lookup": {}, "ledger.entry.lookup": {}, "procurement.purchase-order.lookup": {},
	"inventory.ownership-change.read": {}, "inventory.read": {}, "fx.rate.lookup": {}, "compliance.legal_hold.check": {},
}

func onlyReads(duties []string) bool {
	return !slices.ContainsFunc(duties, func(d string) bool { _, ok := readDuties[d]; return !ok })
}

// Apply signs a token for aud and sets it on req together with the request id.
// A nil signer reports signed=false and touches nothing, so the caller can fall
// back to forwarding the end user's own headers.
func (s *ServiceSigner) Apply(req *http.Request, p Principal, aud, legalEntity string) (signed bool, err error) {
	return s.ApplyDuties(req, p, aud, legalEntity, nil)
}

// ApplyDuties is Apply with the token restricted to the given duties (see MintDuties).
func (s *ServiceSigner) ApplyDuties(req *http.Request, p Principal, aud, legalEntity string, only []string) (signed bool, err error) {
	if s == nil {
		return false, nil
	}
	token, err := s.MintDuties(aud, p, legalEntity, only)
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
	self     string
	keys     map[string]map[string]ed25519.PublicKey
	now      func() time.Time
	capacity int
	mu       sync.Mutex
	seen     map[string]struct{}
	expiries expiryHeap
	shared   ReplayStore
}

type expiry struct {
	exp int64
	jti string
}

type expiryHeap []expiry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].exp < h[j].exp }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(expiry)) }
func (h *expiryHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// NewServiceVerifier trusts one key (empty kid) per issuing service.
func NewServiceVerifier(self string, keys map[string]ed25519.PublicKey) *ServiceVerifier {
	byKid := make(map[string]map[string]ed25519.PublicKey, len(keys))
	for iss, key := range keys {
		byKid[iss] = map[string]ed25519.PublicKey{"": key}
	}
	return NewServiceVerifierKids(self, byKid)
}

// NewServiceVerifierKids trusts several keys per issuing service, selected by
// the token's kid, so a key can be rotated without an outage.
func NewServiceVerifierKids(self string, keys map[string]map[string]ed25519.PublicKey) *ServiceVerifier {
	return &ServiceVerifier{self: self, keys: keys, now: time.Now, capacity: defaultReplayEntries, seen: map[string]struct{}{}}
}

// WithReplayCapacity returns a verifier remembering at most n unexpired token ids.
func (v *ServiceVerifier) WithReplayCapacity(n int) *ServiceVerifier {
	c := NewServiceVerifierKids(v.self, v.keys)
	c.now, c.capacity, c.shared = v.now, n, v.shared
	return c
}

// WithClock returns a verifier sharing keys but with its own clock and replay cache.
func (v *ServiceVerifier) WithClock(now func() time.Time) *ServiceVerifier {
	c := NewServiceVerifierKids(v.self, v.keys)
	c.now, c.capacity, c.shared = now, v.capacity, v.shared
	return c
}

// WithReplayStore returns a verifier that also records every accepted token id
// in store, which all replicas of the target share, so a token replayed on
// another pod is refused too. The per-process cache stays in front of it.
func (v *ServiceVerifier) WithReplayStore(store ReplayStore) *ServiceVerifier {
	c := v.WithClock(v.now)
	c.shared = store
	return c
}

// ServiceVerifierFromEnv reads PASSAGE_SERVICE_NAME (this service, the expected
// aud) and PASSAGE_SERVICE_PUBLIC_KEYS (comma-separated name=base64 or
// name:kid=base64 Ed25519 public keys; a repeated name+kid is an error) and the
// optional PASSAGE_SERVICE_REPLAY_CACHE_SIZE. It returns nil, nil when the name
// or keys are unset.
func ServiceVerifierFromEnv(getenv func(string) string) (*ServiceVerifier, error) {
	self, raw := getenv(EnvServiceName), strings.TrimSpace(getenv(EnvServicePublicKeys))
	if self == "" || raw == "" {
		return nil, nil
	}
	keys := map[string]map[string]ed25519.PublicKey{}
	for _, entry := range strings.Split(raw, ",") {
		id, enc, ok := strings.Cut(strings.TrimSpace(entry), "=")
		name, kid, _ := strings.Cut(id, ":")
		pub, err := base64.StdEncoding.DecodeString(enc)
		if !ok || name == "" || err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s entry %q is not name[:kid]=base64 Ed25519 public key", EnvServicePublicKeys, id)
		}
		if _, dup := keys[name][kid]; dup {
			return nil, fmt.Errorf("%s repeats %s:%s", EnvServicePublicKeys, name, kid)
		}
		if keys[name] == nil {
			keys[name] = map[string]ed25519.PublicKey{}
		}
		keys[name][kid] = ed25519.PublicKey(pub)
	}
	v := NewServiceVerifierKids(self, keys)
	if size := getenv(EnvServiceReplaySize); size != "" {
		n, err := strconv.Atoi(size)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%s must be a positive integer", EnvServiceReplaySize)
		}
		v.capacity = n
	}
	return v, nil
}

// ConfigureServiceVerifier installs the verifier built from getenv and fails on
// a malformed configuration so a target service stops at startup instead of
// rejecting every service token.
func ConfigureServiceVerifier(getenv func(string) string) error {
	v, err := ServiceVerifierFromEnv(getenv)
	if err != nil {
		return err
	}
	SetServiceVerifier(v)
	return nil
}

// Verify returns the claims of a token that is signed by a known service,
// addressed to this service, fresh, within the issuer's grant and not replayed.
func (v *ServiceVerifier) Verify(token string) (ServiceClaims, error) {
	return v.VerifyContext(context.Background(), token)
}

// VerifyContext is Verify with ctx bounding the shared replay store call.
func (v *ServiceVerifier) VerifyContext(ctx context.Context, token string) (ServiceClaims, error) {
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
	pub, known := v.keys[c.Iss][c.Kid]
	if !known || !ed25519.Verify(pub, []byte(serviceTokenContext+body), sig) {
		return ServiceClaims{}, ErrServiceToken
	}
	now := v.now()
	if IsServiceActor(c.Act) && (!isKnownServiceActor(c.Act) || !serviceActorIssuedBy(c.Act, c.Iss)) {
		return ServiceClaims{}, ErrServiceToken
	}
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
	if v.shared != nil {
		if err := v.rememberShared(ctx, c); err != nil {
			return ServiceClaims{}, err
		}
	}
	return c, nil
}

// rememberShared returns ErrServiceToken for a token id another replica already
// accepted, and fails closed: a store error returns ErrReplayStoreUnavailable
// (logged and counted, and answered 503 so callers retry), since
// the store is the service's own database and a token that cannot be recorded
// cannot be proven fresh.
func (v *ServiceVerifier) rememberShared(ctx context.Context, c ServiceClaims) error {
	ctx, cancel := context.WithTimeout(ctx, sharedReplayTimeout)
	defer cancel()
	fresh, err := v.shared.Remember(ctx, c.Tid, c.Iss+":"+c.Jti, time.Unix(c.Exp, 0))
	if err != nil {
		replayStoreFailures.Add(1)
		slog.ErrorContext(ctx, "shared service token replay store failed; token refused", "err", err, "service", c.Iss)
		return ErrReplayStoreUnavailable
	}
	if !fresh {
		replaysRefused.Add(1)
		return ErrServiceToken
	}
	return nil
}

// remember records jti until exp and reports false on a replay or a full cache.
// Eviction pops the expiry min-heap, so each token is evicted once (amortized O(log n)).
// ponytail: this heap is per process; a token can be replayed once per other replica inside its 60s life unless WithReplayStore adds the shared check.
func (v *ServiceVerifier) remember(jti string, exp int64, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	for len(v.expiries) > 0 && v.expiries[0].exp <= now.Unix() {
		delete(v.seen, heap.Pop(&v.expiries).(expiry).jti)
	}
	if _, replayed := v.seen[jti]; replayed || len(v.seen) >= v.capacity {
		return false
	}
	v.seen[jti] = struct{}{}
	heap.Push(&v.expiries, expiry{exp: exp, jti: jti})
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

// ShareReplayCache makes the process-wide verifier record accepted token ids in
// the Postgres table service_token_replay (dbx.ServiceTokenReplayDDL) of pool.
// It does nothing when no verifier is configured, and otherwise fails when pool
// is nil or the table or its purge function is missing, so a service never
// starts with service tokens enabled and no shared replay protection.
func ShareReplayCache(ctx context.Context, pool *pgxpool.Pool) error {
	v := currentServiceVerifier()
	if v == nil {
		return nil
	}
	if pool == nil {
		return errors.New("service tokens are enabled but there is no database for the shared replay cache")
	}
	var present bool
	if err := pool.QueryRow(ctx, `select to_regclass('service_token_replay') is not null and to_regprocedure('service_token_replay_purge()') is not null`).Scan(&present); err != nil {
		return fmt.Errorf("check service_token_replay: %w", err)
	}
	if !present {
		return errors.New("service tokens are enabled but table service_token_replay or function service_token_replay_purge() is missing; apply the service_token_replay migration first")
	}
	SetServiceVerifier(v.WithReplayStore(NewPostgresReplayStore(pool)))
	return nil
}

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
