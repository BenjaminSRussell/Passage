// Command cloudprofile prints what a cloud profile resolves to.
//
//	go run ./cloudprofile/cmd/cloudprofile entra-idp -profile gcc-high -entra-tenant <guid> -client-id <id> -tenant-id <passage tenant>
//	go run ./cloudprofile/cmd/cloudprofile describe -profile gcc+hipaa
//	go run ./cloudprofile/cmd/cloudprofile realm-patch -profile gcc-high+hipaa [-trust-idp-mfa] [-amr-regex <regex>] [-realm deploy/keycloak/realm-passage.json]
//
// realm-patch prints what the profile changes in the Keycloak realm (password policy, event retention, idle session
// timeout, and with -trust-idp-mfa the post-broker flow that skips the second factor when the IdP's amr claim shows MFA);
// with -realm it prints the realm export with the patch applied. entra-idp -trust-idp-mfa adds the amr and acr mappers.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cloudprofile entra-idp|describe|realm-patch [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	sel := fs.String("profile", "commercial", "profile selector, for example gcc or gcc+hipaa")
	entra := fs.String("entra-tenant", cloudprofile.PlaceholderEntraTenant, "customer Entra tenant GUID")
	client := fs.String("client-id", cloudprofile.PlaceholderClientID, "app registration client id")
	tid := fs.String("tenant-id", cloudprofile.PlaceholderPassageTID, "Passage tenant id bound to the realm")
	trust := fs.Bool("trust-idp-mfa", false, "trust the identity provider's amr claim for phishing-resistant MFA instead of asking again")
	amr := fs.String("amr-regex", "", "regex matched against the idp_amr attribute (default: mfa or hwk)")
	realmFile := fs.String("realm", "", "realm export to apply the patch to (realm-patch only)")
	_ = fs.Parse(os.Args[2:])
	base, overlays := cloudprofile.ParseSelector(*sel)
	p, err := cloudprofile.Resolve(base, overlays...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	var out any = p
	switch os.Args[1] {
	case "entra-idp":
		idp, err := cloudprofile.EntraOIDC(p, *entra, *client, *tid)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		if *trust {
			idp.Mappers = append(idp.Mappers, cloudprofile.TrustIdPMFAMappers()...)
		}
		out = idp
	case "realm-patch":
		patch, err := cloudprofile.RenderRealmPatch(p, cloudprofile.RealmOptions{TrustIdPMFA: *trust, AMRRegex: *amr})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
		out = patch
		if *realmFile != "" {
			raw, err := os.ReadFile(*realmFile)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(2)
			}
			var realm map[string]any
			if err := json.Unmarshal(raw, &realm); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(2)
			}
			cloudprofile.ApplyRealmPatch(realm, patch)
			out = realm
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
