package api_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../../../.."

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDeliveryFilesCarryRequiredContent(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"deploy/Makefile", []string{"deploy-local:", "tofu", "k3d image import", "root-local.yaml"}},
		{"deploy/tofu/cluster/main.tf", []string{"precondition", `helm_release" "argocd"`, `helm_release" "cert_manager"`}},
		{"services/platform-health/NOT-IMPLEMENTED.md", []string{"Deliverable 1", "slice 2 of PSG-1", "Root `make deploy-local`"}},
		{"services/platform-health/charts/hello-ledger/templates/deployment.yaml", []string{"secretKeyRef", "/healthz", "/readyz"}},
		{".github/workflows/gate.yml", []string{"gate:", "if: always()", "dorny/paths-filter"}},
		{".github/workflows/ci.yml", []string{"cosign sign", "cosign attest", "anchore/sbom-action", "anchore/scan-action", "aquasecurity/trivy-action"}},
	}
	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			body := readRepoFile(t, tc.file)
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("%s missing %q", tc.file, w)
				}
			}
		})
	}
}

func TestEnvOverlaysArePinnedByDigestAndHoldNoSecrets(t *testing.T) {
	err := filepath.WalkDir(filepath.Join(repoRoot, "deploy/envs"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, banned := range []string{"postgres://", "password:", "tag:"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s contains %q: overlays pin digests and hold no secret values", path, banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
