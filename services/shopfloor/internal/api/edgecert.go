package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// The connector's calls arrive through the edge gateway, which terminates its mTLS with the edge CA and forwards the
// certificate in X-Forwarded-Client-Cert (the gateway sets the header itself and removes any the client sent). The
// certificate names a node, edge:<node id>, and no tenant or duty, so EdgeCertIdentity turns it into the headers the
// tenant middleware reads: the node's tenant (found by the node id), the common name as the user, and the one duty a
// connector has. The service then matches the user to the node's bound connector subject like any connector call,
// so a certificate of one node never acts for another. A request that already carries a tenant (a JWT call through
// the API gateway) is left as it is.

// EdgeTenantLookup finds the tenant of an edge node by its id; it returns "" for an unknown node.
type EdgeTenantLookup interface {
	EdgeNodeTenant(ctx context.Context, id string) (string, error)
}

const (
	headerXFCC    = "X-Forwarded-Client-Cert"
	headerTenant  = "X-Passage-Tenant"
	headerUser    = "X-Passage-User"
	headerDuties  = "X-Passage-Duties"
	connectorDuty = DutyEdgeConnect
)

// EdgeCertIdentity returns the middleware; put it before the tenant middleware.
func EdgeCertIdentity(lookup EdgeTenantLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			xfcc := r.Header.Get(headerXFCC)
			if xfcc == "" || r.Header.Get(headerTenant) != "" {
				next.ServeHTTP(w, r)
				return
			}
			cn, ok := certCommonName(xfcc)
			nodeID, isEdge := strings.CutPrefix(cn, "edge:")
			if !ok || !isEdge || !ids.Valid(nodeID) {
				next.ServeHTTP(w, r) // not a connector certificate: no identity, the tenant middleware refuses it
				return
			}
			tenantID, err := lookup.EdgeNodeTenant(r.Context(), strings.ToLower(nodeID))
			if err != nil || tenantID == "" {
				next.ServeHTTP(w, r)
				return
			}
			r2 := r.Clone(r.Context())
			r2.Header.Set(headerTenant, tenantID)
			r2.Header.Set(headerUser, cn)
			r2.Header.Set(headerDuties, connectorDuty)
			next.ServeHTTP(w, r2)
		})
	}
}

// certCommonName reads the common name out of a single-element X-Forwarded-Client-Cert value, such as
// By=spiffe://x;Hash=abc;Subject="CN=edge:0195...,O=Passage". A value with more than one element (a chain of proxies)
// is ambiguous and gives no name.
func certCommonName(xfcc string) (string, bool) {
	parts := splitOutsideQuotes(xfcc, ',')
	if len(parts) != 1 {
		return "", false
	}
	for _, kv := range splitOutsideQuotes(parts[0], ';') {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok || !strings.EqualFold(k, "Subject") {
			continue
		}
		v = strings.Trim(v, `"`)
		for _, rdn := range splitOutsideQuotes(v, ',') {
			if name, found := strings.CutPrefix(strings.TrimSpace(rdn), "CN="); found && name != "" {
				return name, true
			}
		}
	}
	return "", false
}

func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	inQuote, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == sep && !inQuote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
