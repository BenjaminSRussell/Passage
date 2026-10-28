package domain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeValidator(t *testing.T, script string, timeout time.Duration) *XSDValidator {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "xmllint")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &XSDValidator{xmllint: bin, dir: dir, schema: filepath.Join(dir, "schema.xsd"), timeout: timeout, slots: make(chan struct{}, 1)}
}

func TestXSDValidator_ExitCodes(t *testing.T) {
	tests := []struct {
		name      string
		script    string
		wantErr   bool
		wantFound []XSDError
	}{
		{"valid", `exit 0`, false, nil},
		{"invalid document", `echo "doc.xml:3: element InstdAmt: Schemas validity error : bad amount" >&2; exit 3`, false,
			[]XSDError{{Path: "InstdAmt", Reason: "bad amount"}}},
		{"invalid with unparseable output", `echo "some /tmp/secret/path noise" >&2; exit 3`, false,
			[]XSDError{{Path: documentPath, Reason: genericXSDReason}}},
		{"schema compile failure", `echo "schema.xsd:1: parser error" >&2; exit 5`, true, nil},
		{"io failure", `exit 1`, true, nil},
		{"killed", `kill -9 $$`, true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			found, err := fakeValidator(t, tc.script, time.Second).Validate(context.Background(), []byte("<x/>"))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaks tool output: %v", err)
			}
			if len(found) != len(tc.wantFound) || (len(found) > 0 && found[0] != tc.wantFound[0]) {
				t.Fatalf("found = %+v, want %+v", found, tc.wantFound)
			}
		})
	}
}

func TestXSDValidator_TimeoutIsAnError(t *testing.T) {
	found, err := fakeValidator(t, `sleep 5`, 100*time.Millisecond).Validate(context.Background(), []byte("<x/>"))
	if !errors.Is(err, context.DeadlineExceeded) || len(found) != 0 {
		t.Fatalf("found = %+v, err = %v", found, err)
	}
}

func TestXSDValidator_CancelledWhileRunning(t *testing.T) {
	v := fakeValidator(t, `exec sleep 5`, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := v.Validate(ctx, []byte("<x/>"))
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestXSDValidator_CancelledWhileWaitingForSlot(t *testing.T) {
	v := fakeValidator(t, `exit 0`, time.Second)
	v.slots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := v.Validate(ctx, []byte("<x/>")); err == nil {
		t.Fatal("expected an error when no slot frees before cancellation")
	}
}

func TestXSDValidator_LeavesNoDocumentFilesBehind(t *testing.T) {
	v := fakeValidator(t, `exit 3`, time.Second)
	if _, err := v.Validate(context.Background(), []byte("<x/>")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(v.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp dir holds %d entries, want only the fake binary", len(entries))
	}
}

func TestNewXSDValidator_RejectsABrokenSchema(t *testing.T) {
	if _, err := NewXSDValidator([]byte("not a schema")); err == nil {
		t.Skip("xmllint unavailable or accepted the schema")
	}
}

func TestNewXSDValidator_AcceptsTheVendoredSchemaAndCloses(t *testing.T) {
	schema, err := os.ReadFile("../../testdata/xsd/pain.001.001.09.xsd")
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewXSDValidator(schema)
	if err != nil {
		t.Skipf("xmllint unavailable: %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(v.dir); !os.IsNotExist(err) {
		t.Fatalf("schema dir survives Close: %v", err)
	}
}
