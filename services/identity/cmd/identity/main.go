package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/identity"
	"github.com/BenjaminSRussell/passage/services/identity/migrations"
)

const (
	defaultHTTPAddr = ":8080"
	uuidTextLength  = 36
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
}

func configFrom(getenv func(string) string) (config, error) {
	c := config{
		databaseURL:      getenv("DATABASE_URL"),
		migrateURL:       getenv("MIGRATE_DATABASE_URL"),
		migrateOnStart:   getenv("MIGRATE_ON_START") == "true",
		cataloguePath:    getenv("IDENTITY_DUTY_CATALOGUE_PATH"),
		operatorTenantID: getenv("IDENTITY_OPERATOR_TENANT_ID"),
		httpAddr:         getenv("HTTP_ADDR"),
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

func newRouter(pool *pgxpool.Pool, catalogue *identity.Catalogue, operatorTenantID string) chi.Router {
	router := httpx.NewRouter()
	identity.NewTenantsAPI(pool, operatorTenantID).Register(router)
	identity.NewLegalEntitiesAPI(pool).Register(router)
	identity.NewRoleAssignmentsAPI(pool, catalogue, operatorTenantID).Register(router)
	identity.NewEffectivePermissionsAPI(pool, catalogue).Register(router)
	identity.NewUsersAPI(pool).Register(router)
	identity.NewCatalogueAPI(catalogue).Register(router)
	return router
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
	return httpx.Serve(ctx, cfg.httpAddr, newRouter(pool, catalogue, cfg.operatorTenantID))
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
