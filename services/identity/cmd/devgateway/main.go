package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	defaultAddr     = "127.0.0.1:8088"
	defaultAudience = "passage-api"
	upstreamPrefix  = "PASSAGE_SVC_"
	upstreamSuffix  = "_URL"
)

func main() {
	if err := run(os.Getenv, os.Environ()); err != nil {
		slog.Error("devgateway stopped", "err", err)
		os.Exit(1)
	}
}

func run(getenv func(string) string, environ []string) error {
	handler, err := buildHandler(getenv, environ)
	if err != nil {
		return err
	}
	addr := getenv("DEVGATEWAY_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("devgateway listening", "addr", addr)
	return server.ListenAndServe()
}

func buildHandler(getenv func(string) string, environ []string) (http.Handler, error) {
	audience := getenv("DEVGATEWAY_AUDIENCE")
	if audience == "" {
		audience = defaultAudience
	}
	verifier, err := identity.NewJWTVerifier(getenv("DEVGATEWAY_ISSUER"), audience, getenv("DEVGATEWAY_TENANT_ID"), getenv("DEVGATEWAY_JWKS_URL"), nil)
	if err != nil {
		return nil, fmt.Errorf("jwt verifier: %w", err)
	}
	opa, err := identity.NewOPAClient(getenv("DEVGATEWAY_OPA_URL"), nil)
	if err != nil {
		return nil, fmt.Errorf("opa client: %w", err)
	}
	upstreams, err := upstreamsFrom(environ)
	if err != nil {
		return nil, err
	}
	return newGateway(verifier, opa, upstreams), nil
}

func upstreamsFrom(environ []string) (map[string]*url.URL, error) {
	out := map[string]*url.URL{}
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		name, ok := strings.CutPrefix(key, upstreamPrefix)
		if !ok {
			continue
		}
		name, ok = strings.CutSuffix(name, upstreamSuffix)
		if !ok || name == "" {
			continue
		}
		target, err := url.Parse(value)
		if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
			return nil, fmt.Errorf("%s is not an http(s) URL", key)
		}
		out[strings.ToLower(name)] = target
	}
	if len(out) == 0 {
		return nil, errors.New("no upstream configured: set PASSAGE_SVC_<NAME>_URL")
	}
	return out, nil
}
