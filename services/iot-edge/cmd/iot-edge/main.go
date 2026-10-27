// Command iot-edge is the plant edge connector. This slice validates its
// 12-factor configuration and serves the loopback admin endpoint. OPC-UA and MQTT
// ingestion, Kafka publishing and enrollment are later slices (NOT-IMPLEMENTED.md).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

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
	if err := httpx.Serve(context.Background(), cfg.AdminAddr, httpx.NewRouter()); err != nil {
		logger.Error("iot-edge stopped", "err", err)
		os.Exit(1)
	}
}
