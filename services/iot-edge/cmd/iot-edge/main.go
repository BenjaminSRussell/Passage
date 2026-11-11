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
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mfs"
	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/mqtt"
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
	Source       string // simulator, opcua or mqtt
	SimMachines  int
	// BufferFile keeps unsent events across a restart (the persistent ring). Empty keeps them in memory.
	BufferFile string
	// OPC-UA (EDGE_SOURCE=opcua, or EDGE_WRITER containing opcua).
	OPCUA runtime.OPCUAConfig
	// MQTT (EDGE_SOURCE=mqtt, or EDGE_WRITER containing mqtt).
	MQTTBroker, MQTTClientID, MQTTUser, MQTTCAFile string
	MQTTPassword                                   string
	MQTTTLS                                        bool
	// PLC automation (wave 32): the connector dials PLCAddr (or runs the simulator when it is "simulator"),
	// sends the warehouse's transport orders as MOVE telegrams and forwards the PLC's events.
	PLCAddr, PLCStation              string
	WarehouseURL, WarehouseTokenFile string
	// CertDir holds the mTLS client certificate (25-4). With it the connector enrolls on first start
	// with the token in EnrollTokenFile and renews the certificate 30 days before it expires.
	CertDir         string
	EnrollTokenFile string
	ServerCAFile    string // PEM roots for the shopfloor server; the system roots when empty
	// Writer switches control recipe execution on (25-5): a comma-separated list of simulator, opcua (for
	// pcs-opcua recipes) and mqtt (for pcs-mqtt recipes). RecipeAutoComplete reports a recipe completed after
	// its writes (the demo plant).
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
	cfg.BufferFile = getenv("EDGE_BUFFER_FILE")
	cfg.PLCAddr, cfg.PLCStation = getenv("EDGE_PLC_ADDR"), orDefault(getenv("EDGE_PLC_STATION"), "PLC")
	cfg.WarehouseURL, cfg.WarehouseTokenFile = strings.TrimRight(getenv("EDGE_WAREHOUSE_URL"), "/"), getenv("EDGE_WAREHOUSE_TOKEN_FILE")
	if cfg.PLCAddr != "" {
		switch {
		case cfg.PLCAddr != "simulator" && !strings.Contains(cfg.PLCAddr, ":"):
			problems = append(problems, "EDGE_PLC_ADDR must be host:port, or simulator")
		case len(cfg.PLCStation) > 8:
			problems = append(problems, "EDGE_PLC_STATION must be at most 8 characters (the telegram's station name)")
		case !strings.HasPrefix(cfg.WarehouseURL, "https://") && !(cfg.Env == "dev" && strings.HasPrefix(cfg.WarehouseURL, "http://")):
			problems = append(problems, "EDGE_WAREHOUSE_URL must be https (http only when PASSAGE_ENV is dev) when EDGE_PLC_ADDR is set")
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
		for _, w := range writerList(cfg.Writer) {
			if w != "simulator" && w != "opcua" && w != "mqtt" {
				problems = append(problems, "EDGE_WRITER must list simulator, opcua and mqtt, separated by commas")
				break
			}
		}
		cfg.RecipeAutoComplete = getenv("EDGE_RECIPE_AUTO_COMPLETE") == "true"
		if cfg.Source != "simulator" && cfg.Source != "opcua" && cfg.Source != "mqtt" {
			problems = append(problems, "EDGE_SOURCE must be simulator, opcua or mqtt when EDGE_SHOPFLOOR_URL is set")
		}
		wantOPCUA := cfg.Source == "opcua" || hasWriter(cfg.Writer, "opcua")
		wantMQTT := cfg.Source == "mqtt" || hasWriter(cfg.Writer, "mqtt")
		if wantOPCUA {
			cfg.OPCUA = runtime.OPCUAConfig{Endpoint: getenv("EDGE_OPCUA_ENDPOINT"), SecurityPolicy: getenv("EDGE_OPCUA_SECURITY_POLICY"),
				SecurityMode: getenv("EDGE_OPCUA_SECURITY_MODE"), CertFile: getenv("EDGE_OPCUA_CERT_FILE"), KeyFile: getenv("EDGE_OPCUA_KEY_FILE"),
				Username: getenv("EDGE_OPCUA_USER")}
			if f := getenv("EDGE_OPCUA_PASSWORD_FILE"); f != "" {
				b, err := os.ReadFile(f)
				if err != nil {
					problems = append(problems, "EDGE_OPCUA_PASSWORD_FILE: "+err.Error())
				}
				cfg.OPCUA.Password = strings.TrimSpace(string(b))
			}
			if v := getenv("EDGE_OPCUA_INTERVAL_MS"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 50 || n > 60_000 {
					problems = append(problems, "EDGE_OPCUA_INTERVAL_MS must be an integer from 50 to 60000")
				} else {
					cfg.OPCUA.Interval = time.Duration(n) * time.Millisecond
				}
			}
			if err := cfg.OPCUA.Validate(); err != nil {
				problems = append(problems, "EDGE_OPCUA_*: "+err.Error())
			}
		}
		if wantMQTT {
			cfg.MQTTBroker, cfg.MQTTClientID, cfg.MQTTUser, cfg.MQTTCAFile = getenv("EDGE_MQTT_BROKER"), orDefault(getenv("EDGE_MQTT_CLIENT_ID"), "passage-"+cfg.EdgeID), getenv("EDGE_MQTT_USER"), getenv("EDGE_MQTT_CA_FILE")
			cfg.MQTTTLS = getenv("EDGE_MQTT_TLS") == "true"
			if f := getenv("EDGE_MQTT_PASSWORD_FILE"); f != "" {
				b, err := os.ReadFile(f)
				if err != nil {
					problems = append(problems, "EDGE_MQTT_PASSWORD_FILE: "+err.Error())
				}
				cfg.MQTTPassword = strings.TrimSpace(string(b))
			}
			switch {
			case cfg.MQTTBroker == "" || !strings.Contains(cfg.MQTTBroker, ":"):
				problems = append(problems, "EDGE_MQTT_BROKER must be host:port")
			case !cfg.MQTTTLS && cfg.Env != "dev":
				problems = append(problems, "EDGE_MQTT_TLS must be true outside dev")
			case cfg.MQTTCAFile != "" && !cfg.MQTTTLS:
				problems = append(problems, "EDGE_MQTT_CA_FILE needs EDGE_MQTT_TLS=true")
			}
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

func writerList(s string) []string {
	var out []string
	for _, w := range strings.Split(s, ",") {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

func hasWriter(list, name string) bool {
	for _, w := range writerList(list) {
		if w == name {
			return true
		}
	}
	return false
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
	var rt *runtime.Runtime
	var hc *http.Client
	if cfg.ShopfloorURL != "" {
		var err error
		rt, hc, err = newRuntime(ctx, cfg)
		if err != nil {
			logger.Error("iot-edge runtime not started", "err", err)
			os.Exit(2)
		}
		go func() {
			if err := rt.Run(ctx); err != nil {
				logger.Error("iot-edge runtime stopped", "err", err)
				os.Exit(1)
			}
		}()
	}
	var auto *runtime.Automation
	if cfg.PLCAddr != "" {
		var err error
		if auto, err = newAutomation(ctx, cfg, hc); err != nil {
			logger.Error("iot-edge PLC link not started", "err", err)
			os.Exit(2)
		}
		go func() { _ = auto.Run(ctx) }()
	}
	if rt != nil || auto != nil {
		mux.HandleFunc("/metrics", metricsHandler(rt, auto))
	}
	if err := httpx.Serve(ctx, cfg.AdminAddr, mux); err != nil {
		logger.Error("iot-edge stopped", "err", err)
		os.Exit(1)
	}
}

// newRuntime builds the runtime for a connector that reports to a shopfloor service. With EDGE_CERT_DIR
// it enrolls when no certificate is installed (redeeming the token once), then talks mTLS and renews.
// The returned HTTP client carries the connector's identity; the PLC automation reuses it.
func newRuntime(ctx context.Context, cfg config) (*runtime.Runtime, *http.Client, error) {
	token := ""
	if cfg.TokenFile != "" {
		b, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read EDGE_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	client := &runtime.Client{BaseURL: cfg.ShopfloorURL, EdgeID: cfg.NodeID, Token: token}
	o := runtime.Options{Site: cfg.Site, EdgeID: cfg.EdgeID, ConnectorVersion: version, Transport: client, Control: client,
		BufferAge: time.Duration(cfg.BufferHours) * time.Hour, BufferCapacity: cfg.BufferCapacity}
	if cfg.BufferFile != "" {
		fb, err := runtime.OpenFileBuffer(cfg.BufferFile, o.BufferAge, o.BufferCapacity, time.Now())
		if err != nil {
			return nil, nil, err
		}
		fb.SyncEvery = time.Second
		o.Buffer = fb
		slog.Info("iot-edge event buffer is persistent", "file", cfg.BufferFile, "restored", fb.Len())
	}
	if cfg.CertDir != "" {
		store, err := enrollIfNeeded(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		roots, err := serverRoots(cfg.ServerCAFile)
		if err != nil {
			return nil, nil, err
		}
		client.HTTP = store.HTTPClient(roots)
		o.Certs = store
		o.Renew = func(ctx context.Context) error { return client.Renew(ctx, store) }
	}
	if cfg.SeqStateFile != "" {
		o.State = runtime.FileSeqState{Path: cfg.SeqStateFile}
	}

	// One OPC-UA connection and one MQTT session are shared by the source and the writers that need them.
	var opc *runtime.OPCUAConn
	if cfg.Source == "opcua" || hasWriter(cfg.Writer, "opcua") {
		opc = &runtime.OPCUAConn{Config: cfg.OPCUA}
	}
	var mq *mqtt.Session
	if cfg.Source == "mqtt" || hasWriter(cfg.Writer, "mqtt") {
		tlsCfg, err := mqttTLS(cfg)
		if err != nil {
			return nil, nil, err
		}
		mq = &mqtt.Session{Options: mqtt.Options{Addr: cfg.MQTTBroker, ClientID: cfg.MQTTClientID, Username: cfg.MQTTUser, Password: cfg.MQTTPassword, TLS: tlsCfg},
			OnError: func(err error) { slog.Warn("MQTT connection", "err", err) }}
	}
	switch cfg.Source {
	case "opcua":
		o.Source = &runtime.OPCUASource{Conn: opc}
	case "mqtt":
		o.Source = &runtime.MQTTSource{Session: mq}
	default:
		// The simulator reads the tags the published configuration maps (sim/<n>/count and sim/<n>/state).
		_, sims := runtime.SimConfig(1, cfg.SimMachines, func(int) string { return "" })
		o.Source = &runtime.Simulator{Machines: sims, Tick: time.Second}
	}
	if list := writerList(cfg.Writer); len(list) > 0 {
		o.Recipes, o.AutoComplete = client, cfg.RecipeAutoComplete
		o.Writers = map[string]runtime.Writer{}
		for _, w := range list {
			switch w {
			case "simulator":
				sim := &runtime.SimWriter{}
				o.Writers[runtime.DestOPCUA], o.Writers[runtime.DestMQTT] = sim, sim
			case "opcua":
				o.Writers[runtime.DestOPCUA] = opc
			case "mqtt":
				o.Writers[runtime.DestMQTT] = &runtime.MQTTWriter{Session: mq}
			}
		}
	}
	rt, err := runtime.New(o)
	if err != nil {
		return nil, nil, err
	}
	// A writer-only MQTT session (the source is something else) has nobody to run it.
	if mq != nil && cfg.Source != "mqtt" {
		go func() { _ = mq.Run(ctx) }()
	}
	return rt, client.HTTP, nil
}

func mqttTLS(cfg config) (*tls.Config, error) {
	if !cfg.MQTTTLS {
		return nil, nil
	}
	roots, err := serverRoots(cfg.MQTTCAFile)
	if err != nil {
		return nil, fmt.Errorf("EDGE_MQTT_CA_FILE: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, nil
}

// newAutomation builds the PLC link: the warehouse client (bearer token for dev, the connector's mTLS client
// when it has one) and the dial function, which for "simulator" starts a simulated PLC on loopback.
func newAutomation(ctx context.Context, cfg config, hc *http.Client) (*runtime.Automation, error) {
	token := ""
	if cfg.WarehouseTokenFile != "" {
		b, err := os.ReadFile(cfg.WarehouseTokenFile)
		if err != nil {
			return nil, fmt.Errorf("read EDGE_WAREHOUSE_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	plane := &runtime.Client{BaseURL: cfg.WarehouseURL, Token: token, HTTP: hc}
	addr := cfg.PLCAddr
	if addr == "simulator" {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		go func() { _ = (&mfs.SimPLC{Name: cfg.PLCStation}).Serve(ctx, ln) }()
		addr = ln.Addr().String()
	}
	return &runtime.Automation{Plane: plane, Station: cfg.PLCStation,
		Dial: func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", addr)
		}}, nil
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
func metricsHandler(rt *runtime.Runtime, auto *runtime.Automation) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		var all []metric
		if auto != nil {
			a := auto.Stats()
			all = append(all,
				metric{"iotedge_plc_orders_sent_total", float64(a.OrdersSent), "Transport orders the PLC accepted as MOVE telegrams."},
				metric{"iotedge_plc_orders_refused_total", float64(a.OrdersRefused), "Transport orders refused by the PLC or not valid telegrams."},
				metric{"iotedge_plc_events_forwarded_total", float64(a.EventsForwarded), "PLC events delivered to the warehouse."},
				metric{"iotedge_plc_link_drops_total", float64(a.LinkDrops), "Times the PLC link ended and was dialled again."})
		}
		if rt == nil {
			writeMetrics(w, all)
			return
		}
		s := rt.Stats()
		for _, m := range []metric{
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
			all = append(all, m)
		}
		writeMetrics(w, all)
	}
}

type metric struct {
	name string
	v    float64
	help string
}

func writeMetrics(w io.Writer, ms []metric) {
	for _, m := range ms {
		kind := "gauge"
		if strings.HasSuffix(m.name, "_total") {
			kind = "counter"
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %g\n", m.name, m.help, m.name, kind, m.name, m.v)
	}
}
