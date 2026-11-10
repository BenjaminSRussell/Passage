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
		{"services/platform-health/Dockerfile", []string{"USER 65532:65532"}},
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
		if strings.Contains(string(b), "tag:") {
			t.Errorf("%s contains %q: overlays pin digests and hold no secret values", path, "tag:")
		}
		// A connection URL is allowed only when its password is an ExternalSecret template placeholder
		// (eam-database, transport-db); a literal DSN would carry the secret.
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "postgres://") && !strings.Contains(line, "{{ .password }}@") {
				t.Errorf("%s contains a literal postgres:// URL: overlays hold no secret values", path)
			}
		}
		if hasLiteralPassword(string(b)) {
			t.Errorf("%s contains a literal password value: overlays hold no secret values", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasLiteralPassword(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		_, value, found := strings.Cut(line, "password:")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if strings.HasPrefix(value, "{{") && strings.HasSuffix(value, "}}") {
			continue
		}
		return true
	}
	return false
}

func TestHasLiteralPassword(t *testing.T) {
	if hasLiteralPassword(`password: "{{ .password }}"`) {
		t.Error("pure template placeholder must be allowed")
	}
	if !hasLiteralPassword(`password: hunter2`) {
		t.Error("literal password must be rejected")
	}
}
