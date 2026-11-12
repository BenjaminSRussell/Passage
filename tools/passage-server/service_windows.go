//go:build windows

package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

func inServiceMode() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

type handler struct {
	run runFunc
	log *slog.Logger
}

// Execute is the Windows service control loop: it runs the program and maps Stop, Shutdown and PreShutdown to context cancellation.
// While stopping it keeps reporting StopPending with a rising checkpoint, so the SCM does not give up on a slow shutdown. A failure
// returns a non-zero service-specific exit code; the MSI sets the failure actions to run on a non-zero exit (FailureActionsWhen).
func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown
	const waitHint = 30000 // ms
	status <- svc.Status{State: svc.StartPending, WaitHint: waitHint}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx, h.log) }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				h.log.Error("service stopped with an error", "err", err)
				return true, 1
			}
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				cancel()
				tick := time.NewTicker(time.Second)
				var check uint32 = 1
				status <- svc.Status{State: svc.StopPending, WaitHint: waitHint, CheckPoint: check}
			stopping:
				for {
					select {
					case err := <-done:
						if err != nil && !errors.Is(err, context.Canceled) {
							h.log.Error("service stopped with an error while stopping", "err", err)
						}
						tick.Stop()
						break stopping
					case <-tick.C:
						check++
						status <- svc.Status{State: svc.StopPending, WaitHint: waitHint, CheckPoint: check}
					}
				}
				return false, 0
			}
		}
	}
}

func runAsService(name, logDir string, run runFunc) error {
	file := &RotatingFile{Path: filepath.Join(logDir, name+"-service.log"), MaxBytes: logMaxBytes, Keep: 5}
	defer func() { _ = file.Close() }()
	base := slog.NewJSONHandler(file, nil)
	var h slog.Handler = base
	if el, err := eventlog.Open(name); err == nil {
		defer func() { _ = el.Close() }()
		h = eventLogHandler{Handler: base, el: el}
	} else if _, werr := file.Write([]byte("event log source unavailable: " + err.Error() + "\n")); werr != nil {
		return werr
	}
	return svc.Run(name, &handler{run: run, log: slog.New(h)})
}

// eventLogHandler copies warnings and errors to the Windows Event Log (source "Passage"); the full log is in the rotating file.
type eventLogHandler struct {
	slog.Handler
	el *eventlog.Log
}

func (e eventLogHandler) Handle(ctx context.Context, r slog.Record) error {
	switch {
	case r.Level >= slog.LevelError:
		_ = e.el.Error(1, r.Message)
	case r.Level >= slog.LevelWarn:
		_ = e.el.Warning(2, r.Message)
	}
	return e.Handler.Handle(ctx, r)
}

func (e eventLogHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return eventLogHandler{Handler: e.Handler.WithAttrs(a), el: e.el}
}

func (e eventLogHandler) WithGroup(n string) slog.Handler {
	return eventLogHandler{Handler: e.Handler.WithGroup(n), el: e.el}
}

// serviceRunning reports whether the Passage service is in the Running state.
func serviceRunning() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService("Passage")
	if err != nil {
		return false
	}
	defer func() { _ = s.Close() }()
	st, err := s.Query()
	return err == nil && st.State == svc.Running
}
