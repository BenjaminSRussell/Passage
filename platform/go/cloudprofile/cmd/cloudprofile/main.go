// Command cloudprofile prints what a cloud profile resolves to.
//
//	go run ./cloudprofile/cmd/cloudprofile entra-idp -profile gcc-high -entra-tenant <guid> -client-id <id> -tenant-id <passage tenant>
//	go run ./cloudprofile/cmd/cloudprofile describe -profile gcc+hipaa
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
		fmt.Fprintln(os.Stderr, "usage: cloudprofile entra-idp|describe [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	sel := fs.String("profile", "commercial", "profile selector, for example gcc or gcc+hipaa")
	entra := fs.String("entra-tenant", cloudprofile.PlaceholderEntraTenant, "customer Entra tenant GUID")
	client := fs.String("client-id", cloudprofile.PlaceholderClientID, "app registration client id")
	tid := fs.String("tenant-id", cloudprofile.PlaceholderPassageTID, "Passage tenant id bound to the realm")
	_ = fs.Parse(os.Args[2:])
	base, overlays := cloudprofile.ParseSelector(*sel)
	p, err := cloudprofile.Resolve(base, overlays...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	var out any = p
	if os.Args[1] == "entra-idp" {
		if out, err = cloudprofile.EntraOIDC(p, *entra, *client, *tid); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
