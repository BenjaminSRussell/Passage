// Package httpx is the HTTP kernel every Passage service uses: router with
// standard middleware, JSON helpers, RFC 9457 problem details and graceful serve.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/platform/logpseudo"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const RequestTimeout = 30 * time.Second

// Problem is an RFC 9457 problem-details body. Code is the stable, documented
// Passage error code (e.g. "LED-0001"); Errors carries field-level detail.
type Problem struct {
	Type     string            `json:"type"`
	Title    string            `json:"title"`
	Status   int               `json:"status"`
	Detail   string            `json:"detail,omitempty"`
	Instance string            `json:"instance,omitempty"`
	Code     string            `json:"code,omitempty"`
	TraceID  string            `json:"traceId,omitempty"`
	Errors   map[string]string `json:"errors,omitempty"`
}

func (p *Problem) Error() string { return fmt.Sprintf("%s (%d): %s", p.Code, p.Status, p.Detail) }

// NewProblem builds a problem with the given status and code.
func NewProblem(status int, code, detail string) *Problem {
	return &Problem{Type: "https://passage.dev/problems/" + code, Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

// NewRouter returns a chi router with request id, trusted-proxy client IP ($PASSAGE_TRUSTED_PROXIES), structured logging,
// panic recovery, a 30s timeout and tenant-context extraction installed.
func NewRouter() *chi.Mux {
	return newRouter(tenant.Middleware)
}

// NewRouterWithoutTenant is NewRouter without tenant-context extraction, for services that authenticate some
// routes themselves (a bearer token) and mount the header-based tenant middleware only on the others.
func NewRouterWithoutTenant() *chi.Mux {
	return newRouter()
}

func newRouter(extra ...func(http.Handler) http.Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, recordMetrics, TrustedRealIP(trustedProxiesFromEnv()), middleware.Recoverer, middleware.Timeout(RequestTimeout))
	r.Use(logRequests)
	r.Use(extra...)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	return r
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(), "bytes", ww.BytesWritten(),
			"durationMs", time.Since(start).Milliseconds(), "requestId", middleware.GetReqID(r.Context()))
	})
}

// JSON writes v as JSON with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Decode parses a JSON body into v, rejecting unknown fields and bodies over 4 MiB.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return NewProblem(http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error())
	}
	return nil
}

// DecodeLimit parses JSON like Decode with a caller-selected body limit.
func DecodeLimit(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return NewProblem(http.StatusRequestEntityTooLarge, "COM-0413", "request body exceeds the size limit")
		}
		return NewProblem(http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error())
	}
	return nil
}

// Fail writes err as a problem. *Problem values are written as-is; anything
// else becomes a 500 with code COM-0500 and the detail hidden from the client.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *Problem
	if !errors.As(err, &p) {
		slog.Error("unhandled error", "err", err, "path", r.URL.Path)
		p = NewProblem(http.StatusInternalServerError, "COM-0500", "internal error")
	}
	p.Instance = r.URL.Path
	p.TraceID = middleware.GetReqID(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// Serve runs the handler on addr until SIGINT/SIGTERM or ctx cancellation,
// then drains in-flight requests for up to 15s.
//
// In a FIPS cloud profile (PASSAGE_CLOUD_PROFILE, #599), or in a binary built with GOFIPS140, Serve
// refuses to start unless crypto/fips140.Enabled() is true, so every service that serves through it
// fails closed.
//
// Under the eu-data-boundary overlay, or with LOG_PSEUDONYMIZE=true, Serve also wraps the default slog handler so
// user and person identifiers and email addresses are replaced by tenant-keyed pseudonyms before logs are exported
// (platform/go/logpseudo, #605); a bad profile or key configuration stops the service rather than log raw values.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	if err := cloudprofile.CheckFIPS(os.Getenv); err != nil {
		return err
	}
	if _, err := logpseudo.Install(os.Getenv); err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", addr)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
