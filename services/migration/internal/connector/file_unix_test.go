//go:build unix

package connector_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
)

func TestFile_RejectsAFIFOInTheDropDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "acme", "cust.csv"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	// A symlink inside the root that resolves to the FIFO is refused too.
	if err := os.Symlink("cust.csv", filepath.Join(root, "acme", "link.csv")); err != nil {
		t.Fatal(err)
	}
	env := connector.Env{DropRoot: root}
	for file, want := range map[string]string{"cust.csv": "no file in the drop directory matches", "link.csv": "is not a regular file"} {
		done := make(chan error, 1)
		go func() {
			o, err := env.Open(fileSpec(map[string]any{"dropDir": "acme", "format": "csv"}, map[string]any{"file": file}))
			if err == nil {
				_, err = o.Read(context.Background(), connector.Query{Limit: 10})
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: %v", file, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: reading the drop directory blocked on a FIFO", file)
		}
	}
}
