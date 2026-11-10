package stack

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStopKillsGroupedGrandchildThatIgnoresInterrupt(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{Root: dir, AdminDB: "postgres://127.0.0.1:1/x", KeepDBs: true})
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "grandchild.pid")
	script := `(trap '' INT; sleep 300 & echo $! > "` + pidFile + `"; wait) & wait`
	if err := s.SpawnGroup("grouped", dir, nil, "sh", "-c", script); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidFile)
		if v, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			pid = v
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild never started")
	}
	s.Stop()
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("grandchild %d survived Stop", pid)
}
