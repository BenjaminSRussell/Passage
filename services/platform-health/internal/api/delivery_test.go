package api_test

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		{"services/platform-health/NOT-IMPLEMENTED.md", []string{"Deliverable 1", "built (Kyverno)", "Root `make deploy-local`"}},
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
			// The stage URLs URL-encode the placeholder: {{ .password | urlquery | ... }}@.
			if strings.Contains(line, "postgres://") && !regexp.MustCompile(`\{\{ \.password( \|[^}]*)? \}\}@`).MatchString(line) {
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

// Stage (FR-1.3, FR-1.4, B-09 part b): every stage Application reads this repository at the release tag of
// deploy/envs/stage/release.yaml, never a branch, and pins each service image exactly once, to a signed digest or, until
// one is recorded, to the release version tag. No file under deploy/envs/stage carries the all-zero placeholder digest.
// The manifests are parsed, not scanned line by line, so a comment cannot hide a source from the check.
func TestStageApplicationsArePinnedToTheRelease(t *testing.T) {
	var release struct {
		Revision string            `yaml:"revision"`
		Digests  map[string]string `yaml:"digests"`
	}
	if err := yaml.Unmarshal([]byte(readRepoFile(t, "deploy/envs/stage/release.yaml")), &release); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`^v(\d+\.\d+\.\d+)$`).FindStringSubmatch(release.Revision)
	if m == nil {
		t.Fatalf("deploy/envs/stage/release.yaml: revision %q is not a release tag vX.Y.Z", release.Revision)
	}
	version := m[1]
	zero := "sha256:" + strings.Repeat("0", 64)
	digest := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	semver := regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)
	for name, d := range release.Digests {
		if !digest.MatchString(d) || d == zero {
			t.Errorf("release.yaml digests.%s = %q is not a signed digest", name, d)
		}
	}
	repoSources, services := 0, 0
	checkSource := func(where string, src map[string]any, elements []map[string]any) {
		// This repository with or without ".git", in any letter case (GitHub accepts every form).
		repo, _ := src["repoURL"].(string)
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git"), "https://github.com/BenjaminSRussell/Depot") {
			repoSources++
			if rev, _ := src["targetRevision"].(string); rev != release.Revision {
				t.Errorf("%s: this repository at %q, want the release tag %s", where, rev, release.Revision)
			}
		} else if rev := fmt.Sprint(src["targetRevision"]); !semver.MatchString(rev) {
			t.Errorf("%s: chart %v at %q, want an exact version", where, src["chart"], rev)
		}
		path, _ := src["path"].(string)
		if !strings.HasPrefix(path, "services/") && !strings.HasPrefix(path, "tools/") && path != "{{ .path }}" {
			return
		}
		services++
		helm, _ := src["helm"].(map[string]any)
		var pins [][2]string
		params, _ := helm["parameters"].([]any)
		for _, raw := range params {
			p, _ := raw.(map[string]any)
			name, value := fmt.Sprint(p["name"]), fmt.Sprint(p["value"])
			if name == "{{ .imageKey }}" {
				for _, e := range elements {
					pins = append(pins, [2]string{fmt.Sprint(e["imageKey"]), fmt.Sprint(e["image"])})
				}
				continue
			}
			if name == "image.tag" || name == "image.digest" {
				pins = append(pins, [2]string{name, value})
			}
		}
		values, _ := helm["valuesObject"].(map[string]any)
		image, _ := values["image"].(map[string]any)
		for _, k := range []string{"tag", "digest"} {
			if v, ok := image[k]; ok {
				pins = append(pins, [2]string{"image." + k, fmt.Sprint(v)})
			}
		}
		if len(pins) == 0 {
			t.Errorf("%s: a service source with no image pin", where)
		}
		for _, pin := range pins {
			switch pin[0] {
			case "image.tag":
				if pin[1] != version {
					t.Errorf("%s: image.tag %q is not the release version %s", where, pin[1], version)
				}
			case "image.digest":
				if !digest.MatchString(pin[1]) || pin[1] == zero {
					t.Errorf("%s: image.digest %q is not a signed digest", where, pin[1])
				}
			default:
				t.Errorf("%s: unknown image pin %q", where, pin[0])
			}
		}
	}
	err := filepath.WalkDir(filepath.Join(repoRoot, "deploy/envs/stage"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(b), zero) {
			t.Errorf("%s pins the all-zero placeholder digest", path)
		}
		if !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		for {
			var doc map[string]any
			if err := dec.Decode(&doc); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return fmt.Errorf("%s: %w", path, err)
			}
			kind, _ := doc["kind"].(string)
			spec, _ := doc["spec"].(map[string]any)
			var elements []map[string]any
			if kind == "ApplicationSet" {
				gens, _ := spec["generators"].([]any)
				for _, g := range gens {
					list, _ := g.(map[string]any)["list"].(map[string]any)
					els, _ := list["elements"].([]any)
					for _, e := range els {
						em, _ := e.(map[string]any)
						elements = append(elements, em)
					}
				}
				tpl, _ := spec["template"].(map[string]any)
				spec, _ = tpl["spec"].(map[string]any)
			} else if kind != "Application" {
				continue
			}
			var srcs []any
			if one, ok := spec["source"]; ok {
				srcs = append(srcs, one)
			}
			many, _ := spec["sources"].([]any)
			srcs = append(srcs, many...)
			for i, raw := range srcs {
				src, _ := raw.(map[string]any)
				checkSource(fmt.Sprintf("%s: %s source %d", path, kind, i), src, elements)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if repoSources < 20 || services < 5 {
		t.Errorf("found %d stage sources of this repository and %d service sources; the walk no longer sees the Applications", repoSources, services)
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
