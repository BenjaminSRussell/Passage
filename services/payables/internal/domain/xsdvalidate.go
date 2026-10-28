package domain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	xmllintTimeout = 10 * time.Second
	documentPath   = "Document"
)

type XSDValidator struct {
	xmllint string
	schema  string
}

func NewXSDValidator(schema []byte) (*XSDValidator, error) {
	xmllint, err := exec.LookPath("xmllint")
	if err != nil {
		return nil, fmt.Errorf("xmllint is required for XSD validation: %w", err)
	}
	dir, err := os.MkdirTemp("", "payables-xsd-")
	if err != nil {
		return nil, fmt.Errorf("create schema dir: %w", err)
	}
	path := filepath.Join(dir, "schema.xsd")
	if err := os.WriteFile(path, schema, 0o600); err != nil {
		return nil, fmt.Errorf("write schema: %w", err)
	}
	return &XSDValidator{xmllint: xmllint, schema: path}, nil
}

func (v *XSDValidator) Validate(ctx context.Context, content []byte) ([]XSDError, error) {
	file, err := os.CreateTemp("", "payables-doc-*.xml")
	if err != nil {
		return nil, fmt.Errorf("create document file: %w", err)
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(content)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return nil, fmt.Errorf("write document file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, xmllintTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, v.xmllint, "--noout", "--schema", v.schema, file.Name()).CombinedOutput()
	if err == nil {
		return nil, nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || ctx.Err() != nil {
		return nil, fmt.Errorf("run xmllint: %w", err)
	}
	var found []XSDError
	for _, line := range strings.Split(string(out), "\n") {
		if e, ok := ParseXmllintLine(line); ok {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		found = append(found, XSDError{Path: documentPath, Reason: strings.TrimSpace(string(out))})
	}
	return found, nil
}
