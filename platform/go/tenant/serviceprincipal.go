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
	"finreport-ledger":   {"ledger.entry.lookup"},
	"purchasing-inbound": {"purchasing.inbound.apply"},
	"fulfillment-order":  {"fulfillment.order.sync"},
	// The reporter of posted packing slips to sales (#496): it signs :record-progress calls, and its token holds only
	// fulfillment's grant on sales (sales.order.record-delivery).
	"fulfillment-sales-report": {"sales.order.record-delivery"},
	"integration-hub":          {"migration.outbound.render", "migration.xref.record"},
	"inventory-postings":       {"inventory.transaction.apply"},
	"pricing-cache":            {"pricing.cache.invalidate"},
	"migration-loader":         {"party.import.write", "party.import.read", "product.import.write", "product.import.read", "receivables.import.write", "receivables.import.read", "engineering.import.write", "engineering.import.read"},
	"production-planning":      {"production.planned.intake"},
	"production-shopfloor":     {"production.jobcard.shopfloor"},
	"receiving-erasure":        {"compliance.legal-hold.check"},
	"receiving-ers":            {"receiving.vendor-invoice.post"},
	"scheduling-downtime":      {"scheduling.downtime.apply"},
	"scheduling-order-import":  {"scheduling.order.import"},
	"shopfloor-production":     {"shopfloor.job.sync"},
}

// servicePrincipalIssuers names the services whose signing key may present each
// service principal as a token's actor. Without it any trusted issuer could act
// as any registered principal (iss "integration-hub" with act
// "service:migration-loader"), borrowing duties its own grant lacks.
var servicePrincipalIssuers = map[string][]string{
	"finreport-ledger":         {"finreport"},
	"purchasing-inbound":       {"purchasing"},
	"fulfillment-order":        {"fulfillment"},
	"fulfillment-sales-report": {"fulfillment"},
	"integration-hub":          {"integration-hub"},
	"inventory-postings":       {"inventory"},
	"pricing-cache":            {"pricing"},
	// party forwards the loader as act on the one onward call a party create makes: the party number from numbering.
	// Its token holds only party's own grant (numbering.sequence.allocate), so nothing is borrowed (#475).
	"migration-loader":        {"migration", "migration-factory", "party"},
	"production-planning":     {"production"},
	"production-shopfloor":    {"production"},
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
