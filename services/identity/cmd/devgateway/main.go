package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
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

	allowNonLocalEnv = "DEVGATEWAY_ALLOW_NONLOCAL"
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
	if !loopback(addr) && getenv(allowNonLocalEnv) != "1" {
		return fmt.Errorf("refusing to bind %s: the dev gateway is for one machine; set %s=1 to override", addr, allowNonLocalEnv)
	}
	if getenv(allowNonLocalEnv) == "1" {
		slog.Warn("devgateway non-local guard is off", "env", allowNonLocalEnv, "addr", addr)
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
	if getenv(allowNonLocalEnv) != "1" {
		for name, target := range upstreams {
			if !loopback(target.Host) {
				return nil, fmt.Errorf("upstream %s is %s, which is not loopback; set %s=1 to override", name, target.Host, allowNonLocalEnv)
			}
		}
	}
	return newGateway(verifier, opa, upstreams), nil
}

func loopback(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
