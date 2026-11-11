package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"time"
)

// ExecRunner starts real child processes. Their stdout and stderr go to Logs.
type ExecRunner struct {
	Logs func(name string) io.Writer
}

type execHandle struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Start launches p with its environment on top of the parent's.
func (r ExecRunner) Start(ctx context.Context, p Process) (Handle, error) {
	cmd := exec.Command(p.Exe, p.Args...) //nolint:gosec // the executable comes from the installed bin directory or the admin-owned config
	env := os.Environ()
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+p.Env[k])
	}
	cmd.Env = env
	if r.Logs != nil {
		w := r.Logs(p.Name)
		cmd.Stdout, cmd.Stderr = w, w
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", p.Name, err)
	}
	attachToJob(cmd)
	h := &execHandle{cmd: cmd, done: make(chan struct{})}
	go func() { h.err = cmd.Wait(); close(h.done) }()
	return h, nil
}

func (h *execHandle) Wait() error { <-h.done; return h.err }

// Stop interrupts the child (a no-op error on Windows, where the job object and Kill apply) and kills it after grace.
func (h *execHandle) Stop(grace time.Duration) error {
	_ = h.cmd.Process.Signal(os.Interrupt)
	select {
	case <-h.done:
		return nil
	case <-time.After(grace):
	}
	if err := h.cmd.Process.Kill(); err != nil && !isDone(err) {
		return err
	}
	<-h.done
	return nil
}

func isDone(err error) bool { return err == os.ErrProcessDone }

// ReadyProbe checks a child's admin /readyz.
func ReadyProbe(client *http.Client) Prober {
	return func(ctx context.Context, p Process) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/readyz", p.AdminPort), nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("readyz returned %d", resp.StatusCode)
		}
		return nil
	}
}
