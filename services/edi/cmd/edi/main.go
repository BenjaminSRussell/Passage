// Command edi runs the EDI engine HTTP API: partner profiles and transports,
// the map tester, inbound X12 receipt with 997 generation and 997 matching,
// outbound 855/856/810/820/214 and EDIFACT ORDERS/DESADV/INVOIC generation
// with 997 and CONTRL matching, and the interchange list; the AS2
// listener for partners (EDI_AS2_ADDR); and, with EDI_TRANSPORT_WORKERS=true,
// the delivery dispatcher and the SFTP poller.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/api"
	"github.com/BenjaminSRussell/passage/services/edi/internal/certtrust"
	"github.com/BenjaminSRussell/passage/services/edi/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/poster"
	"github.com/BenjaminSRussell/passage/services/edi/internal/secrets"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = run()
	case len(os.Args) == 2 && os.Args[1] == "consume":
		err = consume(context.Background(), os.Getenv)
	default:
		err = fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the outbound consumer)", os.Args[1:])
	}
	if err != nil {
		slog.Error("edi exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	dupDays, err := envInt("EDI_DUP_WINDOW_DAYS", 30)
	if err != nil {
		return err
	}
	if dupDays < 7 || dupDays > 365 {
		// dup_window_days allowed values (PRD §6.35.13).
		return errors.New("EDI_DUP_WINDOW_DAYS must be 7 to 365")
	}
	sender := api.DefaultSender
	if q, id := os.Getenv("EDI_ISA_QUALIFIER"), os.Getenv("EDI_ISA_ID"); q != "" || id != "" {
		sender = domain.Party{Qualifier: q, ID: id}
	}
	// Our identity follows the same ISA05/ISA06 rules as a partner profile.
	probe := domain.PartnerProfile{Name: "self", ISA: sender, Environment: "PROD"}
	if fields := probe.Validate(); len(fields) > 0 {
		return errors.New("EDI_ISA_QUALIFIER must be two upper-case letters or digits and EDI_ISA_ID 1 to 15 letters or digits")
	}
	// Our UNB S002 identity on outbound EDIFACT, held to the partner rules.
	edifactSender := api.DefaultEdifactSender
	if id, q := os.Getenv("EDI_EDIFACT_ID"), os.Getenv("EDI_EDIFACT_QUALIFIER"); id != "" || q != "" {
		edifactSender = domain.EdifactParty{ID: id, Qualifier: q}
	}
	probe.EDIFACT = &domain.EdifactIdentity{ID: edifactSender.ID, Qualifier: edifactSender.Qualifier}
	if fields := probe.Validate(); len(fields) > 0 {
		return errors.New("EDI_EDIFACT_ID must be 1 to 35 letters, digits, '.', '_' or '-' and EDI_EDIFACT_QUALIFIER 1 to 4 letters or digits (or empty)")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	signer, err := tenant.ServiceSignerFromEnv(os.Getenv)
	if err != nil {
		return err
	}
	st := store.New(pool, dupDays)
	a := api.New(st)
	a.Sender, a.EdifactSender = sender, edifactSender
	a.AllowBodyOutbound = os.Getenv("EDI_ALLOW_BODY_OUTBOUND") == "true"
	fulfillmentURL, billingURL, salesURL := os.Getenv("EDI_FULFILLMENT_URL"), os.Getenv("EDI_BILLING_URL"), os.Getenv("EDI_SALES_URL")
	if signer != nil && fulfillmentURL != "" && billingURL != "" && salesURL != "" {
		src := sources.New(signer, fulfillmentURL, billingURL, salesURL, nil)
		a.Sources, a.Intake = src, src
		// A returned punchout cart becomes a requisition in purchasing when its URL is set (35-3).
		if purchasingURL := os.Getenv("EDI_PURCHASING_URL"); purchasingURL != "" {
			a.Requisitions = src.WithPurchasing(purchasingURL)
		}
	}
	tc, err := loadTransportConfig(os.Getenv, addr)
	if err != nil {
		return err
	}
	a.Secrets, a.AS2PublicURL, a.AllowInsecureAS2, a.AS2Trust = tc.secrets, tc.publicURL, tc.allowHTTP, tc.trust
	a.AS2Limit = api.NewAS2Limiter(tc.ratePerMinute, tc.rateBurst, 0)

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 2)
	if tc.as2Addr != "" {
		go func() { errCh <- httpx.Serve(ctx, tc.as2Addr, a.AS2Routes()) }()
	}
	if tc.workers {
		d := &delivery.Dispatcher{Store: st, Secrets: tc.secrets, Trust: tc.trust, HTTP: tc.http, AS2PublicURL: tc.publicURL, MDNTimeout: tc.mdnTimeout}
		p := &delivery.Poller{Store: st, Secrets: tc.secrets, Receive: a.ReceiveFromTransport}
		go delivery.Run(ctx, "delivery dispatcher", tc.interval, d.RunOnce)
		go delivery.Run(ctx, "sftp poller", tc.interval, p.RunOnce)
		if a.Intake != nil {
			sp := &poster.Poster{Store: st, Intake: a.Intake, PlaceAs: poster.AsServicePrincipal}
			go delivery.Run(ctx, "sales poster", tc.interval, sp.RunOnce)
		}
	}
	go func() { errCh <- httpx.ServeWithMetrics(ctx, addr, a.Routes()) }()
	err = <-errCh
	stop()
	return err
}

// transportConfig is the AS2 and SFTP setup from the environment.
type transportConfig struct {
	secrets   secrets.Store
	publicURL string
	allowHTTP bool
	as2Addr   string
	workers   bool
	interval  time.Duration
	http      *http.Client
	// ratePerMinute and rateBurst are the AS2 listener's limit per endpoint
	// (0 per minute: off); mdnTimeout bounds the wait for an asynchronous MDN.
	ratePerMinute int
	rateBurst     int
	mdnTimeout    time.Duration
	// trust checks partner AS2 certificates against EDI_AS2_TRUST_ROOTS and
	// EDI_AS2_CRL_DIR; nil pins them only.
	trust *certtrust.Checker
}

// loadTransportConfig reads EDI_SECRETS_DIR (the mounted directory of
// transport secrets), EDI_AS2_ADDR (the AS2 listener, off when empty; it must
// differ from HTTP_ADDR because it serves partners without a Passage
// identity), EDI_AS2_PUBLIC_URL (https base URL partners reach it on),
// EDI_AS2_ALLOW_HTTP (http:// partner URLs, development only),
// EDI_AS2_RATE_PER_MINUTE and EDI_AS2_RATE_BURST (requests per AS2 endpoint,
// default 120 a minute with bursts of 60; 0 a minute turns the limit off),
// EDI_AS2_MDN_TIMEOUT_MINUTES (how long a delivery waits for its
// asynchronous MDN, default 1440), EDI_TRANSPORT_WORKERS (run the dispatcher
// and the SFTP poller), EDI_TRANSPORT_INTERVAL_SECONDS (how often they
// look for work, default 15), EDI_AS2_TRUST_ROOTS (a PEM bundle of the CAs
// partner AS2 certificates must chain to; unset pins certificates only) and
// EDI_AS2_CRL_DIR (a directory of CRLs from those CAs; with it every
// certificate of a chain must be covered by a current CRL, fail closed).
func loadTransportConfig(getenv func(string) string, httpAddr string) (transportConfig, error) {
	c := transportConfig{as2Addr: getenv("EDI_AS2_ADDR"), allowHTTP: getenv("EDI_AS2_ALLOW_HTTP") == "true",
		workers: getenv("EDI_TRANSPORT_WORKERS") == "true", publicURL: strings.TrimRight(getenv("EDI_AS2_PUBLIC_URL"), "/")}
	if dir := getenv("EDI_SECRETS_DIR"); dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return c, errors.New("EDI_SECRETS_DIR must be a directory")
		}
		c.secrets = secrets.Dir(dir)
	}
	trust, err := certtrust.New(getenv("EDI_AS2_TRUST_ROOTS"), getenv("EDI_AS2_CRL_DIR"))
	if err != nil {
		return c, fmt.Errorf("EDI_AS2_TRUST_ROOTS / EDI_AS2_CRL_DIR: %w", err)
	}
	c.trust = trust
	if c.as2Addr != "" && c.as2Addr == httpAddr {
		return c, errors.New("EDI_AS2_ADDR must differ from HTTP_ADDR: the AS2 listener serves partners without a Passage identity")
	}
	if c.publicURL != "" {
		u, err := url.Parse(c.publicURL)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || (u.Scheme != "https" && !(c.allowHTTP && u.Scheme == "http")) {
			return c, errors.New("EDI_AS2_PUBLIC_URL must be an https URL without credentials or query")
		}
	}
	seconds, err := envIntFrom(getenv, "EDI_TRANSPORT_INTERVAL_SECONDS", 15)
	if err != nil {
		return c, err
	}
	if seconds < 1 || seconds > 3600 {
		return c, errors.New("EDI_TRANSPORT_INTERVAL_SECONDS must be 1 to 3600")
	}
	c.interval = time.Duration(seconds) * time.Second
	if c.ratePerMinute, err = envIntFrom(getenv, "EDI_AS2_RATE_PER_MINUTE", 120); err != nil {
		return c, err
	}
	if c.ratePerMinute < 0 || c.ratePerMinute > 100000 {
		return c, errors.New("EDI_AS2_RATE_PER_MINUTE must be 0 (off) to 100000")
	}
	if c.rateBurst, err = envIntFrom(getenv, "EDI_AS2_RATE_BURST", 60); err != nil {
		return c, err
	}
	if c.rateBurst < 1 || c.rateBurst > 10000 {
		return c, errors.New("EDI_AS2_RATE_BURST must be 1 to 10000")
	}
	minutes, err := envIntFrom(getenv, "EDI_AS2_MDN_TIMEOUT_MINUTES", 1440)
	if err != nil {
		return c, err
	}
	if minutes < 5 || minutes > 10080 {
		return c, errors.New("EDI_AS2_MDN_TIMEOUT_MINUTES must be 5 to 10080 (a week)")
	}
	c.mdnTimeout = time.Duration(minutes) * time.Minute
	// Partner URLs are configured by an administrator; redirects are not
	// followed so a partner endpoint cannot bounce the service elsewhere.
	c.http = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return c, nil
}

func envInt(name string, def int) (int, error) {
	return envIntFrom(os.Getenv, name, def)
}

func envIntFrom(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.New(name + " must be an integer")
	}
	return n, nil
}
