package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/identity"
	"github.com/BenjaminSRussell/passage/services/identity/migrations"
)

const (
	defaultHTTPAddr = ":8080"
	uuidTextLength  = 36

	scimRealmCacheTTL = 30 * time.Second

	defaultKeycloakClientID       = "passage-identity-deprovision"
	defaultCustomerPortalClientID = "passage-portal-customer"
	minDeprovisionInterval        = 5 * time.Second
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("identity stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "identity")
}

type config struct {
	databaseURL      string
	migrateURL       string
	migrateOnStart   bool
	cataloguePath    string
	operatorTenantID string
	httpAddr         string
	scimIssuerBase   string
	scimAudience     string
	opaURL           string
	bundleToken      string
	bundleKeyPath    string
	bundleKeyID      string
	bundleRegoPath   string
	bundleAddr       string
	bundleTLSCert    string
	bundleTLSKey     string

	keycloakAdminURL    string
	keycloakClientID    string
	keycloakSecretsDir  string
	deprovisionInterval time.Duration

	// The customer portal's Keycloak client and redirect: where the link in a contact invitation leads once the required actions
	// (verify email, set a password, enrol an authenticator) are done. Optional; without a redirect Keycloak shows its own page.
	customerPortalClientID    string
	customerPortalRedirectURI string
}

func (c config) scimEnabled() bool { return c.scimIssuerBase != "" }

func (c config) bundleEnabled() bool { return c.bundleToken != "" }

// keycloakDeprovisionEnabled reports whether SCIM deprovisionings are carried to Keycloak.
func (c config) keycloakDeprovisionEnabled() bool { return c.keycloakAdminURL != "" }

func configFrom(getenv func(string) string) (config, error) {
	c := config{
		databaseURL:      getenv("DATABASE_URL"),
		migrateURL:       getenv("MIGRATE_DATABASE_URL"),
		migrateOnStart:   getenv("MIGRATE_ON_START") == "true",
		cataloguePath:    getenv("IDENTITY_DUTY_CATALOGUE_PATH"),
		operatorTenantID: getenv("IDENTITY_OPERATOR_TENANT_ID"),
		httpAddr:         getenv("HTTP_ADDR"),
		scimIssuerBase:   getenv("IDENTITY_SCIM_ISSUER_BASE"),
		scimAudience:     getenv("IDENTITY_SCIM_AUDIENCE"),
		opaURL:           getenv("IDENTITY_OPA_URL"),
		bundleToken:      getenv("IDENTITY_BUNDLE_TOKEN"),
		bundleKeyPath:    getenv("IDENTITY_BUNDLE_SIGNING_KEY_PATH"),
		bundleKeyID:      getenv("IDENTITY_BUNDLE_KEY_ID"),
		bundleRegoPath:   getenv("IDENTITY_BUNDLE_REGO_PATH"),
		bundleAddr:       getenv("IDENTITY_BUNDLE_ADDR"),
		bundleTLSCert:    getenv("IDENTITY_BUNDLE_TLS_CERT_PATH"),
		bundleTLSKey:     getenv("IDENTITY_BUNDLE_TLS_KEY_PATH"),

		keycloakAdminURL:   getenv("IDENTITY_KEYCLOAK_ADMIN_URL"),
		keycloakClientID:   getenv("IDENTITY_KEYCLOAK_CLIENT_ID"),
		keycloakSecretsDir: getenv("IDENTITY_KEYCLOAK_SECRETS_DIR"),

		customerPortalClientID:    getenv("IDENTITY_CUSTOMER_PORTAL_CLIENT_ID"),
		customerPortalRedirectURI: getenv("IDENTITY_CUSTOMER_PORTAL_REDIRECT_URI"),
	}
	if c.customerPortalClientID == "" {
		c.customerPortalClientID = defaultCustomerPortalClientID
	}
	if c.databaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if c.cataloguePath == "" {
		return c, errors.New("IDENTITY_DUTY_CATALOGUE_PATH is required (the policies/authz/data.json the OPA bundle is built from)")
	}
	if c.operatorTenantID != "" {
		canonical, ok := ids.Canonical(c.operatorTenantID)
		if !ok || len(c.operatorTenantID) != uuidTextLength {
			return c, errors.New("IDENTITY_OPERATOR_TENANT_ID must be a UUID")
		}
		c.operatorTenantID = canonical
	}
	if c.scimIssuerBase != "" && (c.scimAudience == "" || c.opaURL == "") {
		return c, errors.New("IDENTITY_SCIM_ISSUER_BASE needs IDENTITY_SCIM_AUDIENCE and IDENTITY_OPA_URL")
	}
	if c.scimIssuerBase == "" && (c.scimAudience != "" || c.opaURL != "") {
		return c, errors.New("IDENTITY_SCIM_AUDIENCE and IDENTITY_OPA_URL need IDENTITY_SCIM_ISSUER_BASE")
	}
	if c.bundleEnabled() && (c.operatorTenantID == "" || c.bundleKeyPath == "" || c.bundleKeyID == "" || c.bundleAddr == "" || c.bundleTLSCert == "" || c.bundleTLSKey == "") {
		return c, errors.New("IDENTITY_BUNDLE_TOKEN needs IDENTITY_OPERATOR_TENANT_ID, IDENTITY_BUNDLE_SIGNING_KEY_PATH, IDENTITY_BUNDLE_KEY_ID, IDENTITY_BUNDLE_ADDR, IDENTITY_BUNDLE_TLS_CERT_PATH and IDENTITY_BUNDLE_TLS_KEY_PATH")
	}
	if !c.bundleEnabled() && (c.bundleKeyPath != "" || c.bundleKeyID != "" || c.bundleRegoPath != "" || c.bundleAddr != "" || c.bundleTLSCert != "" || c.bundleTLSKey != "") {
		return c, errors.New("IDENTITY_BUNDLE_* settings need IDENTITY_BUNDLE_TOKEN")
	}
	if c.bundleEnabled() && (c.bundleAddr == c.httpAddr || (c.httpAddr == "" && c.bundleAddr == defaultHTTPAddr)) {
		return c, errors.New("IDENTITY_BUNDLE_ADDR must differ from HTTP_ADDR so ingress never reaches the bundle")
	}
	if err := c.keycloakFrom(getenv); err != nil {
		return c, err
	}
	if c.bundleRegoPath == "" {
		c.bundleRegoPath = filepath.Join(filepath.Dir(c.cataloguePath), "authz.rego")
	}
	if c.httpAddr == "" {
		c.httpAddr = defaultHTTPAddr
	}
	return c, nil
}

func (c *config) keycloakFrom(getenv func(string) string) error {
	interval := getenv("IDENTITY_KEYCLOAK_DEPROVISION_INTERVAL")
	if !c.keycloakDeprovisionEnabled() {
		if c.keycloakClientID != "" || c.keycloakSecretsDir != "" || interval != "" {
			return errors.New("IDENTITY_KEYCLOAK_* settings need IDENTITY_KEYCLOAK_ADMIN_URL")
		}
		return nil
	}
	if c.keycloakSecretsDir == "" || c.operatorTenantID == "" {
		return errors.New("IDENTITY_KEYCLOAK_ADMIN_URL needs IDENTITY_KEYCLOAK_SECRETS_DIR and IDENTITY_OPERATOR_TENANT_ID (the deprovisioning worker lists tenants as the operator)")
	}
	if c.keycloakClientID == "" {
		c.keycloakClientID = defaultKeycloakClientID
	}
	c.deprovisionInterval = identity.DefaultDeprovisionInterval
	if interval != "" {
		d, err := time.ParseDuration(interval)
		if err != nil || d < minDeprovisionInterval {
			return errors.New("IDENTITY_KEYCLOAK_DEPROVISION_INTERVAL must be a duration of at least 5s, got " + interval)
		}
		c.deprovisionInterval = d
	}
	return nil
}

func loadCatalogue(path string) (*identity.Catalogue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read duty catalogue: %w", err)
	}
	return identity.LoadCatalogue(raw)
}

func newRouter(pool *pgxpool.Pool, catalogue *identity.Catalogue, operatorTenantID string, scim *identity.SCIMAPI, contacts *identity.CustomerContactsAPI) chi.Router {
	router := httpx.NewRouterWithoutTenant()
	router.Group(func(admin chi.Router) {
		admin.Use(tenant.Middleware)
		identity.NewTenantsAPI(pool, operatorTenantID).Register(admin)
		identity.NewLegalEntitiesAPI(pool).Register(admin)
		identity.NewTenantFeaturesAPI(pool).Register(admin)
		identity.NewTenantSetupAPI(pool).Register(admin)
		identity.NewRoleAssignmentsAPI(pool, catalogue, operatorTenantID).Register(admin)
		identity.NewEffectivePermissionsAPI(pool, catalogue).WithOperatorTenant(operatorTenantID).Register(admin)
		identity.NewUsersAPI(pool).Register(admin)
		identity.NewKeycloakDeprovisionsAPI(pool).Register(admin)
		if contacts != nil {
			contacts.Register(admin)
		}
		identity.NewCatalogueAPI(catalogue).Register(admin)
	})
	if scim != nil {
		scim.Register(router)
	}
	return router
}

func newSCIM(pool *pgxpool.Pool, cfg config) (*identity.SCIMAPI, error) {
	if !cfg.scimEnabled() {
		slog.Info("SCIM provisioning is disabled: IDENTITY_SCIM_ISSUER_BASE is not set")
		return nil, nil
	}
	verifiers, err := identity.NewTenantVerifiers(pool, cfg.scimIssuerBase, cfg.scimAudience, nil, scimRealmCacheTTL)
	if err != nil {
		return nil, err
	}
	opa, err := identity.NewOPAClient(cfg.opaURL, nil)
	if err != nil {
		return nil, err
	}
	return identity.NewSCIMAPI(pool, verifiers, opa), nil
}

// newKeycloakAdmin builds the Admin REST client shared by the deprovisioning worker and the customer contact routes, or nil when
// IDENTITY_KEYCLOAK_ADMIN_URL is not set.
func newKeycloakAdmin(cfg config) (*identity.KeycloakAdmin, error) {
	if !cfg.keycloakDeprovisionEnabled() {
		return nil, nil
	}
	return identity.NewKeycloakAdmin(cfg.keycloakAdminURL, cfg.keycloakClientID, identity.SecretsDir(cfg.keycloakSecretsDir), nil)
}

// newDeprovisioner builds the worker that carries SCIM deprovisionings to Keycloak, or nil when it is off. The
// rows are queued either way, so turning it on later processes what SCIM deprovisioned in the meantime.
func newDeprovisioner(pool *pgxpool.Pool, cfg config, admin *identity.KeycloakAdmin) (*identity.KeycloakDeprovisioner, error) {
	if admin == nil {
		slog.Info("keycloak deprovisioning is disabled: IDENTITY_KEYCLOAK_ADMIN_URL is not set; SCIM deprovisionings stay queued")
		return nil, nil
	}
	return identity.NewKeycloakDeprovisioner(pool, admin, cfg.operatorTenantID, cfg.deprovisionInterval)
}

// newContactsAPI builds the customer contact routes. Without Keycloak administration they serve the directory but create and change
// nothing (503 IDN-CONTACT-KEYCLOAK-OFF).
func newContactsAPI(pool *pgxpool.Pool, cfg config, admin *identity.KeycloakAdmin) *identity.CustomerContactsAPI {
	var provisioner identity.ContactProvisioner
	if admin != nil {
		provisioner = admin
	} else {
		slog.Info("customer contact invitations are disabled: IDENTITY_KEYCLOAK_ADMIN_URL is not set")
	}
	return identity.NewCustomerContactsAPI(pool, provisioner, identity.Invitation{ClientID: cfg.customerPortalClientID, RedirectURI: cfg.customerPortalRedirectURI})
}

func newBundleAPI(pool *pgxpool.Pool, catalogue *identity.Catalogue, cfg config) (*identity.PolicyBundleAPI, error) {
	if !cfg.bundleEnabled() {
		slog.Info("policy bundle distribution is disabled: IDENTITY_BUNDLE_TOKEN is not set")
		return nil, nil
	}
	catalogueRaw, err := os.ReadFile(cfg.cataloguePath)
	if err != nil {
		return nil, fmt.Errorf("read duty catalogue: %w", err)
	}
	rego, err := os.ReadFile(cfg.bundleRegoPath)
	if err != nil {
		return nil, fmt.Errorf("read authz policy: %w", err)
	}
	keyPEM, err := os.ReadFile(cfg.bundleKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read bundle signing key: %w", err)
	}
	signer, err := identity.NewBundleSigner(keyPEM, cfg.bundleKeyID)
	if err != nil {
		return nil, err
	}
	compiler, err := identity.NewPolicyBundleCompiler(pool, catalogue, catalogueRaw, rego, cfg.operatorTenantID, signer)
	if err != nil {
		return nil, err
	}
	return identity.NewPolicyBundleAPI(compiler, cfg.bundleToken)
}

func serveBundle(ctx context.Context, cfg config, bundle *identity.PolicyBundleAPI) error {
	router := chi.NewRouter()
	bundle.Register(router)
	srv := &http.Server{Addr: cfg.bundleAddr, Handler: router, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServeTLS(cfg.bundleTLSCert, cfg.bundleTLSKey) }()
	slog.Info("policy bundle listening", "addr", cfg.bundleAddr)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func operatorRegistered(ctx context.Context, pool *pgxpool.Pool, operatorTenantID string) (bool, error) {
	ctx = tenant.With(ctx, tenant.Principal{TenantID: operatorTenantID})
	var rows int
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from identity_operator where tenant_id = $1`, operatorTenantID).Scan(&rows)
	})
	return rows == 1, err
}

func warnIfOperatorMismatch(ctx context.Context, pool *pgxpool.Pool, operatorTenantID string) {
	if operatorTenantID == "" {
		return
	}
	registered, err := operatorRegistered(ctx, pool, operatorTenantID)
	switch {
	case err != nil:
		slog.Warn("could not verify IDENTITY_OPERATOR_TENANT_ID against identity_operator", "err", err)
	case !registered:
		slog.Warn("IDENTITY_OPERATOR_TENANT_ID is not the tenant registered in identity_operator; operator-only APIs will refuse every caller", "tenantId", operatorTenantID)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	cfg, err := configFrom(getenv)
	if err != nil {
		return err
	}
	catalogue, err := loadCatalogue(cfg.cataloguePath)
	if err != nil {
		return err
	}
	if cfg.migrateOnStart {
		if err := migrate(ctx, cfg); err != nil {
			return err
		}
	}
	pool, err := dbx.Connect(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	warnIfOperatorMismatch(ctx, pool, cfg.operatorTenantID)
	scim, err := newSCIM(pool, cfg)
	if err != nil {
		return err
	}
	bundle, err := newBundleAPI(pool, catalogue, cfg)
	if err != nil {
		return err
	}
	keycloakAdmin, err := newKeycloakAdmin(cfg)
	if err != nil {
		return err
	}
	deprovisioner, err := newDeprovisioner(pool, cfg, keycloakAdmin)
	if err != nil {
		return err
	}
	router := newRouter(pool, catalogue, cfg.operatorTenantID, scim, newContactsAPI(pool, cfg, keycloakAdmin))
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	if deprovisioner != nil {
		worker := make(chan struct{})
		go func() { defer close(worker); deprovisioner.Run(ctx) }()
		defer func() { stop(); <-worker }()
	}
	if bundle == nil {
		return httpx.Serve(ctx, cfg.httpAddr, router)
	}
	bundleErr := make(chan error, 1)
	go func() {
		err := serveBundle(ctx, cfg, bundle)
		bundleErr <- err
		stop()
	}()
	serveErr := httpx.Serve(ctx, cfg.httpAddr, router)
	stop()
	return errors.Join(serveErr, <-bundleErr)
}

func migrate(ctx context.Context, cfg config) error {
	url := cfg.migrateURL
	if url == "" {
		url = cfg.databaseURL
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect migration database: %w", err)
	}
	defer pool.Close()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
