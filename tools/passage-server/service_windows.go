//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const serviceName = "Passage"

func inServiceMode() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

type handler struct {
	getenv func(string) string
	log    *slog.Logger
}

// Execute is the Windows service control loop: it runs runServe and maps Stop/Shutdown to context cancellation.
func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, h.getenv, h.log) }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				h.log.Error("passage-server stopped", "err", err)
				return false, 1 // non-zero exit code lets the SCM recovery actions restart the service
			}
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

func runAsService(getenv func(string) string) error {
	dataDir := DefaultDataDir(getenv)
	file := &RotatingFile{Path: filepath.Join(dataDir, "logs", "passage-server.log"), MaxBytes: logMaxBytes, Keep: 5}
	defer func() { _ = file.Close() }()
	var out io.Writer = file
	base := slog.NewJSONHandler(out, nil)
	var h slog.Handler = base
	if el, err := eventlog.Open(serviceName); err == nil {
		defer func() { _ = el.Close() }()
		h = eventLogHandler{Handler: base, el: el}
	} else {
		_, _ = file.Write([]byte("event log source unavailable: " + err.Error() + "\n"))
	}
	return svc.Run(serviceName, &handler{getenv: getenv, log: slog.New(h)})
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
