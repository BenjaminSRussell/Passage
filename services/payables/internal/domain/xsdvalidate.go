package domain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	xmllintTimeout       = 10 * time.Second
	maxConcurrentXmllint = 3
	xmllintInvalidExit   = 3
	documentPath         = "Document"
	genericXSDReason     = "document does not conform to the schema"
)

type XSDValidator struct {
	xmllint string
	dir     string
	schema  string
	timeout time.Duration
	slots   chan struct{}
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
		os.RemoveAll(dir)
		return nil, fmt.Errorf("write schema: %w", err)
	}
	v := &XSDValidator{xmllint: xmllint, dir: dir, schema: path, timeout: xmllintTimeout, slots: make(chan struct{}, maxConcurrentXmllint)}
	if err := v.selfTest(); err != nil {
		v.Close()
		return nil, err
	}
	return v, nil
}

func (v *XSDValidator) Close() error {
	return os.RemoveAll(v.dir)
}

func (v *XSDValidator) selfTest() error {
	profile, err := ParseBankProfile(BankProfileDTO{Variant: VariantSEPA})
	if err != nil {
		return fmt.Errorf("xsd self-test profile: %w", err)
	}
	method, err := ParsePaymentMethod(PaymentMethodDTO{Type: MethodTransfer, Format: FormatPain001})
	if err != nil {
		return fmt.Errorf("xsd self-test method: %w", err)
	}
	party := Pain001Party{Name: "Self Test", Address: PostalAddress{Country: "DE"}}
	file, err := GeneratePain001(Pain001Input{
		FileNumber: "SELFTEST", Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Debtor: party,
		DebtorAccount: Pain001Account{IBAN: "DE89370400440532013000"}, DebtorBIC: "COBADEFFXXX", Profile: profile,
		Payments: []Pain001Payment{{
			Method: method, ExecutionDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Number: "SELFTEST-1",
			Amount: decimal.NewFromInt(1), Currency: "EUR",
			Creditor: Pain001Creditor{Party: party, Account: Pain001Account{IBAN: "DE63111111111111111111"}},
		}},
	})
	if err != nil {
		return fmt.Errorf("xsd self-test document: %w", err)
	}
	found, err := v.Validate(context.Background(), file.Content)
	if err != nil {
		return fmt.Errorf("xsd self-test: %w", err)
	}
	if len(found) > 0 {
		return fmt.Errorf("xsd self-test: known-good pain.001 rejected: %s", found[0].Error())
	}
	return nil
}

func (v *XSDValidator) Validate(ctx context.Context, content []byte) ([]XSDError, error) {
	select {
	case v.slots <- struct{}{}:
		defer func() { <-v.slots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for xmllint slot: %w", ctx.Err())
	}
	file, err := os.CreateTemp(v.dir, "doc-*.xml")
	if err != nil {
		return nil, fmt.Errorf("create document file: %w", err)
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(content)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return nil, fmt.Errorf("write document file: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.xmllint, "--noout", "--nonet", "--schema", v.schema, file.Name())
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil, nil
	}
	var exit *exec.ExitError
	if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != xmllintInvalidExit {
		slog.Error("xmllint failed", "err", err, "output", strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("run xmllint: %w", err)
	}
	return parseXmllintOutput(string(out), file.Name()), nil
}

func parseXmllintOutput(out, documentFile string) []XSDError {
	var found []XSDError
	for _, line := range strings.Split(out, "\n") {
		if e, ok := ParseXmllintLine(strings.ReplaceAll(line, documentFile, "document")); ok {
			found = append(found, e)
		}
	}
	if len(found) == 0 {
		found = append(found, XSDError{Path: documentPath, Reason: genericXSDReason})
	}
	return found
}
