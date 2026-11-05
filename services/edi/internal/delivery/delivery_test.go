package delivery

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

func TestBackoffDoublesFromAMinuteToAnHour(t *testing.T) {
	want := []time.Duration{time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour, time.Hour}
	for n, w := range want {
		if got := Backoff(n); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, w)
		}
	}
}

func TestInboxFileSkipsPartialsDirectoriesAndOurFolders(t *testing.T) {
	cfg := domain.SFTPSettings{ArchiveDir: "processed", FailedDir: "failed"}
	for name, want := range map[string]bool{
		"po.x12": true, "po.x12.part": false, "processed": false, "failed": false, "sub/": false, ".hidden": false, "": false, "a/b": false,
	} {
		if got := inboxFile(name, cfg); got != want {
			t.Errorf("%q: %v", name, got)
		}
	}
}
