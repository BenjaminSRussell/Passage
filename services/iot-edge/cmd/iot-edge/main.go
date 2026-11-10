// Command iot-edge is the plant edge connector. This slice validates its
// 12-factor configuration and serves the loopback admin endpoint. OPC-UA and MQTT
// ingestion, Kafka publishing and enrollment are later slices (NOT-IMPLEMENTED.md).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/runtime"
)

// version is the connector version heartbeats report; the build sets it with -ldflags.
var version = "0.2.0-dev"

const (
	defaultEnv        = "dev"
	defaultService    = "iot-edge"
	defaultAdminAddr  = "127.0.0.1:8081"
	defaultBufferHrs  = 24
	minBufferHrs      = 1
	maxBufferHrs      = 168
	serviceNameLogKey = "service"
)

// config is the validated connector configuration.
type config struct {
	Env         string
	Service     string
	Site        string
	EdgeID      string
	AdminAddr   string
	BufferHours int
	// BufferCapacity is how many events the in-memory ring holds (the persistent ring is open).
	BufferCapacity int
	// ShopfloorURL switches the runtime on: the connector ships events and heartbeats to it.
	ShopfloorURL string
	NodeID       string // the shopfloor edge node id (UUID)
	TokenFile    string // bearer token file, for dev until enrollment issues certificates
	SeqStateFile string
	Source       string // simulator is the only source built
	SimMachines  int
	// CertDir holds the mTLS client certificate (25-4). With it the connector enrolls on first start
	// with the token in EnrollTokenFile and renews the certificate 30 days before it expires.
	CertDir         string
	EnrollTokenFile string
	ServerCAFile    string // PEM roots for the shopfloor server; the system roots when empty
	// Writer switches control recipe execution on (25-5). simulator is the only writer built: an OPC-UA
	// client is not. RecipeAutoComplete reports a recipe completed after its writes (the demo plant).
	Writer             string
	RecipeAutoComplete bool
}

// loadConfig reads and validates the environment. Invalid configuration is an
// error so the process exits non-zero before it touches the plant network.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		Env:       orDefault(getenv("PASSAGE_ENV"), defaultEnv),
		Service:   orDefault(getenv("SERVICE_NAME"), defaultService),
		Site:      getenv("EDGE_SITE"),
		EdgeID:    getenv("EDGE_ID"),
		AdminAddr: orDefault(getenv("EDGE_ADMIN_ADDR"), defaultAdminAddr),
	}
	var problems []string
	if cfg.Site == "" {
		problems = append(problems, "EDGE_SITE is required")
	}
	if cfg.EdgeID == "" {
		problems = append(problems, "EDGE_ID is required")
	}
	if !loopback(cfg.AdminAddr) {
		problems = append(problems, "EDGE_ADMIN_ADDR must bind to loopback (127.0.0.1 or localhost)")
	}
	cfg.BufferHours = defaultBufferHrs
	if v := getenv("IOT_BUFFER_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minBufferHrs || n > maxBufferHrs {
			problems = append(problems, fmt.Sprintf("IOT_BUFFER_HOURS must be an integer from %d to %d", minBufferHrs, maxBufferHrs))
		} else {
			cfg.BufferHours = n
		}
	}
	cfg.BufferCapacity = 200_000
	if v := getenv("IOT_BUFFER_CAPACITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1000 || n > 5_000_000 {
			problems = append(problems, "IOT_BUFFER_CAPACITY must be an integer from 1000 to 5000000")
		} else {
			cfg.BufferCapacity = n
		}
	}
	cfg.ShopfloorURL = strings.TrimRight(getenv("EDGE_SHOPFLOOR_URL"), "/")
	cfg.NodeID, cfg.TokenFile = getenv("EDGE_NODE_ID"), getenv("EDGE_TOKEN_FILE")
	cfg.SeqStateFile, cfg.Source = getenv("EDGE_SEQ_STATE_FILE"), getenv("EDGE_SOURCE")
	cfg.SimMachines = 2
	if cfg.ShopfloorURL != "" {
		switch {
		case !strings.HasPrefix(cfg.ShopfloorURL, "https://") && !(cfg.Env == "dev" && strings.HasPrefix(cfg.ShopfloorURL, "http://")):
			problems = append(problems, "EDGE_SHOPFLOOR_URL must be https (http only when PASSAGE_ENV is dev)")
		case !ids.Valid(cfg.NodeID):
			problems = append(problems, "EDGE_NODE_ID must be the edge node's UUID when EDGE_SHOPFLOOR_URL is set")
		}
		cfg.CertDir, cfg.EnrollTokenFile, cfg.ServerCAFile = getenv("EDGE_CERT_DIR"), getenv("EDGE_ENROLLMENT_TOKEN_FILE"), getenv("EDGE_SERVER_CA_FILE")
		if cfg.CertDir != "" && !strings.HasPrefix(cfg.ShopfloorURL, "https://") {
			problems = append(problems, "EDGE_CERT_DIR needs an https EDGE_SHOPFLOOR_URL")
		}
		if cfg.CertDir == "" && (cfg.EnrollTokenFile != "" || cfg.ServerCAFile != "") {
			problems = append(problems, "EDGE_ENROLLMENT_TOKEN_FILE and EDGE_SERVER_CA_FILE need EDGE_CERT_DIR")
		}
		cfg.Writer = getenv("EDGE_WRITER")
		if cfg.Writer != "" && cfg.Writer != "simulator" {
			problems = append(problems, "EDGE_WRITER must be simulator (an OPC-UA writer is not built)")
		}
		cfg.RecipeAutoComplete = getenv("EDGE_RECIPE_AUTO_COMPLETE") == "true"
		if cfg.Source != "simulator" {
			problems = append(problems, "EDGE_SOURCE must be simulator when EDGE_SHOPFLOOR_URL is set (OPC-UA and MQTT adapters are not built)")
		}
		if v := getenv("EDGE_SIM_MACHINES"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 50 {
				problems = append(problems, "EDGE_SIM_MACHINES must be an integer from 1 to 50")
			} else {
				cfg.SimMachines = n
			}
		}
	}
	if len(problems) > 0 {
		return config{}, errors.New(strings.Join(problems, "; "))
	}
	return cfg, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// loopback reports whether addr binds only the local host.
func loopback(addr string) bool {
	return strings.HasPrefix(addr, "127.0.0.1:") || strings.HasPrefix(addr, "localhost:")
}

func newLogger(w io.Writer, service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		switch a.Key {
		case slog.TimeKey:
			a.Key = "timestamp"
		case slog.MessageKey:
			a.Key = "message"
		}
		return a
	}})).With(serviceNameLogKey, service)
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "iot-edge configuration:", err)
		os.Exit(2)
	}
	logger := newLogger(os.Stdout, cfg.Service)
	slog.SetDefault(logger)
	logger.Info("iot-edge starting", "site", cfg.Site, "edgeId", cfg.EdgeID, "bufferHours", cfg.BufferHours)
	ctx := context.Background()
	mux := http.NewServeMux()
	router := httpx.NewRouter()
	mux.Handle("/", router)
	if cfg.ShopfloorURL != "" {
		rt, err := newRuntime(ctx, cfg)
		if err != nil {
			logger.Error("iot-edge runtime not started", "err", err)
			os.Exit(2)
		}
		mux.HandleFunc("/metrics", metricsHandler(rt))
		go func() {
			if err := rt.Run(ctx); err != nil {
				logger.Error("iot-edge runtime stopped", "err", err)
				os.Exit(1)
			}
		}()
	}
	if err := httpx.Serve(ctx, cfg.AdminAddr, mux); err != nil {
		logger.Error("iot-edge stopped", "err", err)
		os.Exit(1)
	}
}

// newRuntime builds the runtime for a connector that reports to a shopfloor service. With EDGE_CERT_DIR
// it enrolls when no certificate is installed (redeeming the token once), then talks mTLS and renews.
func newRuntime(ctx context.Context, cfg config) (*runtime.Runtime, error) {
	token := ""
	if cfg.TokenFile != "" {
		b, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read EDGE_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	client := &runtime.Client{BaseURL: cfg.ShopfloorURL, EdgeID: cfg.NodeID, Token: token}
	o := runtime.Options{Site: cfg.Site, EdgeID: cfg.EdgeID, ConnectorVersion: version, Transport: client, Control: client,
		BufferAge: time.Duration(cfg.BufferHours) * time.Hour, BufferCapacity: cfg.BufferCapacity}
	if cfg.CertDir != "" {
		store, err := enrollIfNeeded(ctx, cfg)
		if err != nil {
			return nil, err
		}
		roots, err := serverRoots(cfg.ServerCAFile)
		if err != nil {
			return nil, err
		}
		client.HTTP = store.HTTPClient(roots)
		o.Certs = store
		o.Renew = func(ctx context.Context) error { return client.Renew(ctx, store) }
	}
	if cfg.Writer == "simulator" {
		o.Recipes, o.Writer, o.AutoComplete = client, &runtime.SimWriter{}, cfg.RecipeAutoComplete
	}
	if cfg.SeqStateFile != "" {
		o.State = runtime.FileSeqState{Path: cfg.SeqStateFile}
	}
	// The simulator reads the tags the published configuration maps (sim/<n>/count and sim/<n>/state).
	_, sims := runtime.SimConfig(1, cfg.SimMachines, func(int) string { return "" })
	o.Source = &runtime.Simulator{Machines: sims, Tick: time.Second}
	return runtime.New(o)
}

func serverRoots(file string) (*x509.CertPool, error) {
	if file == "" {
		return nil, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read EDGE_SERVER_CA_FILE: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("EDGE_SERVER_CA_FILE holds no PEM certificate")
	}
	return pool, nil
}

// enrollIfNeeded loads the installed certificate, or redeems the enrollment token for one. A connector
// with neither a certificate nor a token does not start.
func enrollIfNeeded(ctx context.Context, cfg config) (*runtime.CertStore, error) {
	store := &runtime.CertStore{Dir: cfg.CertDir}
	if err := store.Load(); err != nil {
		return nil, err
	}
	if store.Has() {
		return store, nil
	}
	if cfg.EnrollTokenFile == "" {
		return nil, errors.New("no client certificate in EDGE_CERT_DIR and no EDGE_ENROLLMENT_TOKEN_FILE to enroll with")
	}
	b, err := os.ReadFile(cfg.EnrollTokenFile)
	if err != nil {
		return nil, fmt.Errorf("read EDGE_ENROLLMENT_TOKEN_FILE: %w", err)
	}
	roots, err := serverRoots(cfg.ServerCAFile)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
	out, err := runtime.Enroll(ctx, hc, cfg.ShopfloorURL, cfg.NodeID, strings.TrimSpace(string(b)), store)
	if err != nil {
		return nil, err
	}
	slog.Info("iot-edge enrolled", "subject", out.Subject, "serial", out.Serial, "expiresAt", out.NotAfter)
	return store, nil
}

// metricsHandler serves the runtime's counters in Prometheus text format on the loopback admin port.
func metricsHandler(rt *runtime.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		s := rt.Stats()
		for _, m := range []struct {
			name string
			v    float64
			help string
		}{
			{"iotedge_events_accepted_total", float64(s.Accepted), "Machine events built from readings and buffered."},
			{"iotedge_events_dropped_duplicate_total", float64(s.DroppedDuplicate), "Events dropped as duplicate by seq (BR-25.4)."},
			{"iotedge_events_dropped_stale_total", float64(s.DroppedStale), "Events dropped as older than 7 days (BR-25.4)."},
			{"iotedge_readings_invalid_total", float64(s.Invalid), "Readings that could not become an event."},
			{"iotedge_buffer_evicted_total", float64(s.Evicted), "Events lost to buffer capacity."},
			{"iotedge_events_sent_total", float64(s.Sent), "Events acknowledged or rejected by the server and removed from the buffer."},
			{"iotedge_batches_rejected_events_total", float64(s.Rejected), "Events in batches the server rejected as invalid."},
			{"iotedge_send_failures_total", float64(s.SendFailures), "Sends that failed and left events buffered."},
			{"iotedge_seq_gaps_total", float64(s.SeqGaps), "Sequence gaps the server reported (SF-0008)."},
			{"iotedge_heartbeat_failures_total", float64(s.HeartbeatFailures), "Heartbeats that failed."},
			{"iotedge_recipes_executed_total", float64(s.RecipesExecuted), "Control recipes whose setpoints were written (BR-25.15)."},
			{"iotedge_readback_deviations_total", float64(s.ReadbackDeviations), "Setpoint writes whose readback differed from the value written."},
			{"iotedge_cert_renewals_total", float64(s.CertRenewals), "Client certificate renewals that succeeded."},
			{"iotedge_cert_renew_failures_total", float64(s.CertRenewFailures), "Client certificate renewals that failed."},
			{"iotedge_buffer_events", float64(s.BufferLen), "Events waiting in the buffer."},
			{"iotedge_buffer_fill_percent", s.BufferFillPct, "Buffer fill as a percentage of capacity."},
			{"iotedge_applied_config_version", float64(s.AppliedConfigVersion), "Applied configuration version."},
		} {
			kind := "gauge"
			if strings.HasSuffix(m.name, "_total") {
				kind = "counter"
			}
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %g\n", m.name, m.help, m.name, kind, m.name, m.v)
		}
	}
}
