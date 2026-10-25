package domain_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	pain001Schema = "../../testdata/xsd/pain.001.001.09.xsd"
	pain002Schema = "../../testdata/xsd/pain.002.001.10.xsd"
)

func runXmllint(t *testing.T, schema string, content []byte) (parsed []domain.XSDError, stderr string) {
	t.Helper()
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skipf("xmllint not on PATH; %s XSD contract check skipped", schema)
	}
	file := filepath.Join(t.TempDir(), "document.xml")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(xmllint, "--noout", "--schema", schema, file).CombinedOutput()
	if err == nil {
		return nil, ""
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("xmllint did not run: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if xsdErr, ok := domain.ParseXmllintLine(line); ok {
			parsed = append(parsed, xsdErr)
		}
	}
	return parsed, strings.TrimSpace(string(out))
}

func validateXSD(t *testing.T, schema string, content []byte) {
	t.Helper()
	parsed, stderr := runXmllint(t, schema, content)
	if stderr == "" {
		return
	}
	if len(parsed) == 0 {
		t.Fatalf("%s: %s", domain.CodeFileXSD, stderr)
	}
	lines := make([]string, len(parsed))
	for i, e := range parsed {
		lines[i] = e.Error()
	}
	t.Fatal(strings.Join(lines, "\n"))
}
