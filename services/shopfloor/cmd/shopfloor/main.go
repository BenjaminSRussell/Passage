package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/api"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/pki"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv, os.Args); err != nil {
		slog.Error("shopfloor stopped", "err", err)
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
	return slog.New(handler).With("service", "shopfloor")
}

func run(ctx context.Context, getenv func(string) string, args []string) error {
	consume := len(args) > 1 && args[1] == "consume"
	sweep := len(args) > 1 && args[1] == "sweep"
	if len(args) > 1 && !consume && !sweep {
		return fmt.Errorf("unknown command %q (use no argument to serve, \"consume\" to run the production operation status consumer or \"sweep\" to run the scheduled jobs)", args[1])
	}
	if sweep {
		// Validate the schedule before connecting so a bad setting fails fast.
		if _, err := sweepConfig(getenv); err != nil {
			return err
		}
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" && !consume && !sweep {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	st := store.New(pool)
	if consume {
		return runConsumer(ctx, getenv, st, pool)
	}
	if sweep {
		return runSweeper(ctx, getenv, st)
	}
	a := api.New(st)
	issuer, err := edgeIssuer(getenv)
	if err != nil {
		return err
	}
	if issuer != nil {
		a.WithIssuer(issuer)
	}
	// The enrollment redeem route carries no identity (its token is the credential), so the root router has
	// no tenant middleware; every other route sits behind it on a sub-router.
	router := httpx.NewRouterWithoutTenant()
	a.RegisterPublic(router)
	tenantRouter := chi.NewRouter()
	// A connector's certificate, forwarded by the edge gateway, becomes its identity before the tenant middleware.
	tenantRouter.Use(api.EdgeCertIdentity(st), tenant.Middleware)
	a.Register(tenantRouter)
	router.Mount("/", tenantRouter)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

// edgeIssuer builds the issuer of edge client certificates. SHOPFLOOR_EDGE_ISSUER=cert-manager is the platform
// issuer: cert-manager signs through a CertificateRequest the service creates with its service account (the
// issuer's name in SHOPFLOOR_EDGE_ISSUER_NAME, its kind in SHOPFLOOR_EDGE_ISSUER_KIND, the namespace in
// SHOPFLOOR_EDGE_ISSUER_NAMESPACE or the pod's own). Otherwise the dev and demo issuer loads the CA from
// SHOPFLOOR_EDGE_CA_CERT_FILE and SHOPFLOOR_EDGE_CA_KEY_FILE. With neither set enrollment answers SF-0173;
// with only one CA file set the service does not start.
func edgeIssuer(getenv func(string) string) (pki.Issuer, error) {
	if getenv("SHOPFLOOR_EDGE_ISSUER") == "cert-manager" {
		return certManagerIssuer(getenv)
	}
	certFile, keyFile := getenv("SHOPFLOOR_EDGE_CA_CERT_FILE"), getenv("SHOPFLOOR_EDGE_CA_KEY_FILE")
	switch {
	case certFile == "" && keyFile == "":
		return nil, nil
	case certFile == "" || keyFile == "":
		return nil, errors.New("SHOPFLOOR_EDGE_CA_CERT_FILE and SHOPFLOOR_EDGE_CA_KEY_FILE must be set together")
	}
	ca, err := pki.LoadLocalCA(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("edge CA: %w", err)
	}
	return ca, nil
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

func certManagerIssuer(getenv func(string) string) (pki.Issuer, error) {
	name := getenv("SHOPFLOOR_EDGE_ISSUER_NAME")
	if name == "" {
		return nil, errors.New("SHOPFLOOR_EDGE_ISSUER=cert-manager needs SHOPFLOOR_EDGE_ISSUER_NAME")
	}
	if getenv("SHOPFLOOR_EDGE_CA_CERT_FILE") != "" || getenv("SHOPFLOOR_EDGE_CA_KEY_FILE") != "" {
		return nil, errors.New("SHOPFLOOR_EDGE_ISSUER=cert-manager and the SHOPFLOOR_EDGE_CA_*_FILE variables are two issuers; set one")
	}
	kind := getenv("SHOPFLOOR_EDGE_ISSUER_KIND")
	if kind != "" && kind != "Issuer" && kind != "ClusterIssuer" {
		return nil, errors.New("SHOPFLOOR_EDGE_ISSUER_KIND must be Issuer or ClusterIssuer")
	}
	ns := getenv("SHOPFLOOR_EDGE_ISSUER_NAMESPACE")
	if ns == "" {
		b, err := os.ReadFile(saDir + "/namespace")
		if err != nil {
			return nil, errors.New("SHOPFLOOR_EDGE_ISSUER_NAMESPACE is required outside a pod: " + err.Error())
		}
		ns = strings.TrimSpace(string(b))
	}
	api := getenv("KUBERNETES_API_URL")
	if api == "" {
		host, port := getenv("KUBERNETES_SERVICE_HOST"), getenv("KUBERNETES_SERVICE_PORT")
		if host == "" || port == "" {
			return nil, errors.New("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are not set; is this a pod?")
		}
		api = "https://" + net.JoinHostPort(host, port)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	if ca, err := os.ReadFile(saDir + "/ca.crt"); err == nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(ca) {
			client.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}
		}
	}
	return &pki.CertManager{APIServer: api, Namespace: ns, IssuerName: name, IssuerKind: kind, Token: pki.ServiceAccountToken, Client: client}, nil
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
