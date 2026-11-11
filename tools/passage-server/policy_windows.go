//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// Install-time values are written by the MSI under HKLM\SOFTWARE\Passage\Config; a GPO or Intune policy under
// HKLM\SOFTWARE\Policies\Passage overrides them. Both are string values named like the MSI properties (PG_DSN, OIDC_ISSUER, ...).
var registryKeys = []string{`SOFTWARE\Passage\Config`, `SOFTWARE\Policies\Passage`}

var registryValueNames = []string{"PG_DSN", "PG_APP_DSN", "OIDC_ISSUER", "OIDC_AUDIENCE", "OIDC_JWKS_URL", "TENANT_ID", "OPA_URL", "KAFKA_BOOTSTRAP",
	"TEMPORAL_HOSTPORT", "LISTEN", "CLOUD_PROFILE", "TLS_CERT_FILE", "TLS_KEY_FILE", "REQUIRE_FIPS", "FEATURE_NODE", "FEATURE_PYTHON"}

func platformOverrides() map[string]string {
	out := map[string]string{}
	for _, path := range registryKeys {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		for _, name := range registryValueNames {
			if v, _, err := k.GetStringValue(name); err == nil && v != "" {
				out[name] = v
			}
		}
		_ = k.Close()
	}
	return out
}
