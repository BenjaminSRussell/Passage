package tenant

import "testing"

func TestEveryServicePrincipalNamesItsIssuers(t *testing.T) {
	for name := range servicePrincipals {
		if len(servicePrincipalIssuers[name]) == 0 {
			t.Errorf("service principal %q has no issuer in servicePrincipalIssuers", name)
		}
	}
	for name := range servicePrincipalIssuers {
		if _, ok := servicePrincipals[name]; !ok {
			t.Errorf("servicePrincipalIssuers names %q, which is not a service principal", name)
		}
	}
}
