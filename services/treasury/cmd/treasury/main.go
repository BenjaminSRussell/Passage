// Command treasury runs the treasury service: bank accounts, statement import,
// and cash forecast (wave 36, PSG-36 slice 1).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/api"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
	"github.com/BenjaminSRussell/passage/services/treasury/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("treasury exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	maxMB := 50
	if raw := os.Getenv("TREASURY_MAX_FILE_MB"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("TREASURY_MAX_FILE_MB: %w", err)
		}
		maxMB = n
	}
	vaultCfg, err := bankconnect.LoadVaultConfig(os.Getenv)
	if err != nil {
		return err
	}
	var vault *bankconnect.Vault
	if vaultCfg.Enabled() {
		vault = bankconnect.NewVault(vaultCfg)
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := vault.Close(closeCtx); err != nil {
				slog.Error("vault token revoke failed", "err", err)
			}
		}()
		slog.Info("bank connectivity keys stay in vault transit", "addr", vaultCfg.Addr, "signingKeys", vaultCfg.SigningKeyNames, "sftpKey", vaultCfg.SFTPKeyName, "channels", vaultCfg.BankChannels)
	}
	pool, err := dbx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	pg := store.New(pool)
	a, err := api.New(pg).WithMaxFileMB(maxMB)
	if err != nil {
		return err
	}
	if err := wireEBICS(ctx, a, pg, vault, vaultCfg); err != nil {
		return err
	}

	router := httpx.NewRouter()
	a.Register(router)

	// The kernel router serves a static /readyz; the exact-match route below
	// takes precedence and checks Postgres.
	root := http.NewServeMux()
	root.Handle("/readyz", a.Ready())
	root.Handle("/", router)

	admin := &http.Server{Addr: envOr("ADMIN_ADDR", ":9090"), Handler: a.Metrics(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("admin listener stopped", "err", err)
		}
	}()
	defer func() { _ = admin.Shutdown(context.Background()) }()

	slog.Info("treasury listening", "addr", envOr("HTTP_ADDR", ":8080"))
	return httpx.Serve(ctx, envOr("HTTP_ADDR", ":8080"), root)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
