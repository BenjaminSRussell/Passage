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
	cmd.WaitDelay = 5 * time.Second // a grandchild holding the pipes open must not block Wait forever
	setProcAttr(cmd)
	var logw io.Writer
	if r.Logs != nil {
		logw = r.Logs(p.Name)
		cmd.Stdout, cmd.Stderr = logw, logw
	}
	if err := cmd.Start(); err != nil {
		closeWriter(logw)
		return nil, fmt.Errorf("start %s: %w", p.Name, err)
	}
	attachToJob(cmd)
	h := &execHandle{cmd: cmd, done: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		closeWriter(logw) // each start opens its own log file handle; release it when the child is gone
		close(h.done)
	}()
	return h, nil
}

func (h *execHandle) Wait() error { <-h.done; return h.err }

func closeWriter(w io.Writer) {
	if c, ok := w.(io.Closer); ok {
		_ = c.Close()
	}
}

// Stop asks the child to stop with an interrupt and kills it after grace. Windows has no portable way to deliver a graceful signal to a
// child of a service (no console to send CTRL_BREAK on), so there the child is killed at once; the services commit their work in
// database transactions and replay from the outbox, so a hard stop loses nothing that was acknowledged.
func (h *execHandle) Stop(grace time.Duration) error {
	if err := interrupt(h.cmd.Process); err != nil {
		grace = 0
	}
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

// ReadyProbe checks a child's admin /readyz (or the ReadyPath its spec names).
func ReadyProbe(client *http.Client) Prober {
	return func(ctx context.Context, p Process) error {
		path := p.ReadyPath
		if path == "" {
			path = "/readyz"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", p.AdminPort, path), nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s returned %d", path, resp.StatusCode)
		}
		return nil
	}
}
