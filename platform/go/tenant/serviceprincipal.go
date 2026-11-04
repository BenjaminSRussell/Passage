package tenant

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

const ServiceActorPrefix = "service:"

var ErrUnknownServicePrincipal = errors.New("unknown service principal")

// servicePrincipals is the static duty table of background actors: a consumer
// acts in-process as one of these, never as a user and never with more than the
// duties listed. policies/authz/data.json "service_principals" mirrors it and a
// test keeps them equal.
var servicePrincipals = map[string][]string{
	"finreport-ledger":        {"ledger.entry.lookup"},
	"purchasing-inbound":      {"purchasing.inbound.apply"},
	"fulfillment-order":       {"fulfillment.order.sync"},
	"integration-hub":         {"migration.outbound.render", "migration.xref.record"},
	"pricing-cache":           {"pricing.cache.invalidate"},
	"migration-loader":        {"party.import.write", "product.import.write", "receivables.import.write"},
	"production-planning":     {"production.planned.intake"},
	"receiving-erasure":       {"compliance.legal_hold.check"},
	"receiving-ers":           {"VendorInvoicePost"},
	"scheduling-downtime":     {"scheduling.downtime.apply"},
	"scheduling-order-import": {"scheduling.order.import"},
	"shopfloor-production":    {"shopfloor.job.sync"},
}

// servicePrincipalIssuers names the services whose signing key may present each
// service principal as a token's actor. Without it any trusted issuer could act
// as any registered principal (iss "integration-hub" with act
// "service:migration-loader"), borrowing duties its own grant lacks.
var servicePrincipalIssuers = map[string][]string{
	"finreport-ledger":        {"finreport"},
	"purchasing-inbound":      {"purchasing"},
	"fulfillment-order":       {"fulfillment"},
	"integration-hub":         {"integration-hub"},
	"pricing-cache":           {"pricing"},
	"migration-loader":        {"migration", "migration-factory"},
	"production-planning":     {"production"},
	"receiving-erasure":       {"receiving"},
	"receiving-ers":           {"receiving"},
	"scheduling-downtime":     {"scheduling"},
	"scheduling-order-import": {"scheduling"},
	"shopfloor-production":    {"shopfloor"},
}

func ServicePrincipalTable() map[string][]string {
	out := make(map[string][]string, len(servicePrincipals))
	for name, duties := range servicePrincipals {
		out[name] = slices.Clone(duties)
	}
	return out
}

// ServiceActor is the recorded actor id of a service principal, e.g. "service:receiving-ers".
func ServiceActor(name string) string { return ServiceActorPrefix + name }

// ServicePrincipal builds the principal a background consumer runs as: the
// tenant comes from the event, the legal entity is one UUID or empty (tenant
// bookkeeping only), the actor is "service:<name>" and the duties are exactly
// the table's. It never carries DutyAllScope or an administrator duty.
func ServicePrincipal(name, tenantID, legalEntityID string) (Principal, error) {
	duties, ok := servicePrincipals[name]
	if !ok {
		return Principal{}, fmt.Errorf("%w: %q", ErrUnknownServicePrincipal, name)
	}
	tid, ok := canonicalUUID(tenantID)
	if !ok || tid == ids.Nil {
		return Principal{}, errors.New("service principal needs a tenant id that is a UUID")
	}
	le := ""
	if legalEntityID != "" {
		if le, ok = canonicalUUID(legalEntityID); !ok || le == ids.Nil {
			return Principal{}, errors.New("service principal legal entity must be a UUID")
		}
	}
	held := make(map[string]struct{}, len(duties))
	for _, d := range duties {
		held[d] = struct{}{}
	}
	return Principal{TenantID: tid, LegalEntityID: le, UserID: ServiceActor(name), Duties: held}, nil
}

func IsServiceActor(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), ServiceActorPrefix)
}

func isKnownServiceActor(id string) bool {
	name, ok := strings.CutPrefix(id, ServiceActorPrefix)
	_, known := servicePrincipals[name]
	return ok && known
}

// serviceActorIssuedBy reports whether issuer may sign tokens acting as the
// service principal named by id.
func serviceActorIssuedBy(id, issuer string) bool {
	name, ok := strings.CutPrefix(id, ServiceActorPrefix)
	return ok && slices.Contains(servicePrincipalIssuers[name], issuer)
}
