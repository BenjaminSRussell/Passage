package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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
}

func (c config) scimEnabled() bool { return c.scimIssuerBase != "" }

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
	if c.httpAddr == "" {
		c.httpAddr = defaultHTTPAddr
	}
	return c, nil
}

func loadCatalogue(path string) (*identity.Catalogue, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read duty catalogue: %w", err)
	}
	return identity.LoadCatalogue(raw)
}

func newRouter(pool *pgxpool.Pool, catalogue *identity.Catalogue, operatorTenantID string, scim *identity.SCIMAPI) chi.Router {
	router := httpx.NewRouterWithoutTenant()
	router.Group(func(admin chi.Router) {
		admin.Use(tenant.Middleware)
		identity.NewTenantsAPI(pool, operatorTenantID).Register(admin)
		identity.NewLegalEntitiesAPI(pool).Register(admin)
		identity.NewRoleAssignmentsAPI(pool, catalogue, operatorTenantID).Register(admin)
		identity.NewEffectivePermissionsAPI(pool, catalogue).Register(admin)
		identity.NewUsersAPI(pool).Register(admin)
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
	warnIfOperatorMismatch(ctx, pool, cfg.operatorTenantID)
	scim, err := newSCIM(pool, cfg)
	if err != nil {
		return err
	}
	return httpx.Serve(ctx, cfg.httpAddr, newRouter(pool, catalogue, cfg.operatorTenantID, scim))
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
