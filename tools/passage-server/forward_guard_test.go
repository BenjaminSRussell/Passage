package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Under passage-server every service shares PASSAGE_GATEWAY_SECRET and refuses identity headers that do not carry it. A service that forwards
// the caller's identity to a peer in X-Passage-* headers (instead of a signed service token) must therefore call tenant.ForwardGatewaySecret
// wherever it sets tenant.HeaderTenant on an outbound request. This test finds such files under services/ and fails when one lacks the call.
func TestOutboundIdentityForwardingSendsTheGatewaySecret(t *testing.T) {
	root := filepath.Join("..", "..", "services")
	if _, err := os.Stat(root); err != nil {
		t.Skip("services directory not found")
	}
	var missing []string
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "ledgertest" { // ledgertest is a test helper that talks to a test server directly
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		if !strings.Contains(src, "Header.Set(tenant.HeaderTenant") {
			return nil
		}
		checked++
		if !strings.Contains(src, "ForwardGatewaySecret(") {
			missing = append(missing, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("found no file that forwards identity headers: the guard's pattern is stale")
	}
	if len(missing) > 0 {
		t.Fatalf("these files set tenant.HeaderTenant on an outbound request without tenant.ForwardGatewaySecret:\n%s", strings.Join(missing, "\n"))
	}
}
