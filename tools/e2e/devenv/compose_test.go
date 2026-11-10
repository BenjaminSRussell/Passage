package devenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func composePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "deploy", "local", "compose.yaml")
}

func TestComposeDeclaresTheLocalInfrastructure(t *testing.T) {
	raw, err := os.ReadFile(composePath(t))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	serviceName := regexp.MustCompile(`(?m)^  ([a-z]+):$`)
	section := text[strings.Index(text, "\nservices:"):strings.Index(text, "\nvolumes:")]
	got := map[string]bool{}
	for _, m := range serviceName.FindAllStringSubmatch(section, -1) {
		got[m[1]] = true
	}
	for _, want := range []string{"postgres", "kafka", "keycloak", "opa"} {
		if !got[want] {
			t.Errorf("compose does not declare %s", want)
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^\s+image: (\S+)$`).FindAllStringSubmatch(text, -1) {
		if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(m[1]) {
			t.Errorf("image %s is not pinned by digest", m[1])
		}
	}
	if !strings.Contains(text, "--import-realm") {
		t.Error("keycloak does not import the rendered realm")
	}
}

func TestComposeMountsExistingRepoFiles(t *testing.T) {
	raw, err := os.ReadFile(composePath(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(composePath(t))
	for _, m := range regexp.MustCompile(`(?m)^\s+- (\.\./\.\./[^:$]+):`).FindAllStringSubmatch(string(raw), -1) {
		if _, err := os.Stat(filepath.Join(dir, m[1])); err != nil {
			t.Errorf("compose mounts %s: %v", m[1], err)
		}
	}
}

func TestComposeConfigValidatesWhenDockerComposeIsInstalled(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose is not installed")
	}
	cmd := exec.Command("docker", "compose", "-f", composePath(t), "config", "-q")
	cmd.Env = append(os.Environ(), "PASSAGE_DEV_DIR="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
}

func TestMakeDevTargetsParse(t *testing.T) {
	for _, target := range []string{"dev", "dev-down", "dev-render"} {
		cmd := exec.Command("make", "-n", "-C", repoRoot(t), target)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("make -n %s: %v\n%s", target, err, out)
		}
	}
}
