// Command migration-factory serves the migration factory API: open-items pack runs with
// control totals, the legacy cross-reference and the cutover runbook (PSG-49).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/api"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("migration-factory stopped", "err", err)
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
	return slog.New(handler).With("service", "migration-factory")
}

// receivablesTarget reads RECEIVABLES_URL and the service signing key the factory's calls are signed with. The key must
// belong to the service named "migration-factory" (PASSAGE_SERVICE_NAME), the caller the service grants are written
// for. With neither set the service starts and the load step answers 503 MIG-1520; an address without a key, or a key
// for another service, stops it, because every call would be refused.
func receivablesTarget(getenv func(string) string) (*target.Receivables, error) {
	base := getenv("RECEIVABLES_URL")
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if base != "" {
		if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("RECEIVABLES_URL must be an http or https base address such as http://receivables:8080")
		}
	}
	switch {
	case base != "" && signer == nil:
		return nil, errors.New("RECEIVABLES_URL is set but no service signing key is (PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	case signer != nil && getenv(tenant.EnvServiceName) != target.ServiceName:
		return nil, fmt.Errorf("%s must be %q for the factory's service grants", tenant.EnvServiceName, target.ServiceName)
	case base == "" || signer == nil:
		return target.NewReceivables(target.Config{}), nil
	}
	return target.NewReceivables(target.Config{BaseURL: base, Signer: signer}), nil
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	receivables, err := receivablesTarget(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	return httpx.Serve(ctx, addr, api.New(store.New(pool)).WithTarget(receivables).Routes())
}
