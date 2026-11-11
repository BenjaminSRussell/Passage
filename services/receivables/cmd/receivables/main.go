// Command receivables runs the Passage receivables service: cash application,
// open items and aging (wave 30 slice 1).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/api"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/fxclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/taxclient"
	"github.com/BenjaminSRussell/passage/services/receivables/migrations"
)

func main() {
	start, err := command(os.Args[1:])
	if err == nil {
		err = start(context.Background(), os.Getenv)
	}
	if err != nil {
		slog.Error("receivables exited", "err", err)
		os.Exit(1)
	}
}

// command picks what the process runs: no argument serves the API, "consume" runs the billing consumer.
func command(args []string) (func(context.Context, func(string) string) error, error) {
	switch {
	case len(args) == 0:
		return func(context.Context, func(string) string) error { return run() }, nil
	case args[0] == "consume" && len(args) == 1:
		return consume, nil
	}
	return nil, fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the billing consumer)", args)
}

// parseRMALedgers reads RECEIVABLES_RMA_LEDGERS: a JSON object from legal entity id to the ledger id its RMA credit journals post to.
func parseRMALedgers(raw string) (map[string]string, error) {
	return parseLedgers("RECEIVABLES_RMA_LEDGERS", raw)
}

// parseLedgers reads a JSON object from legal entity id to ledger id; name is the variable, for the errors.
// RECEIVABLES_GL_LEDGERS pins the ledger of the payment, fee, interest, down payment and deduction journals (30-2);
// a legal entity without a pin posts them to its RMA ledger.
func parseLedgers(name, raw string) (map[string]string, error) {
	out := map[string]string{}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object of legal entity id to ledger id: %w", name, err)
	}
	for le, ledger := range out {
		if !ids.Valid(le) || !ids.Valid(ledger) {
			return nil, fmt.Errorf("%s entry %q needs UUID legal entity and ledger ids", name, le)
		}
	}
	return out, nil
}

func parseRMATaxAccounts(raw string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("RECEIVABLES_RMA_TAX_ACCOUNTS must be a JSON object of legal entity id to an object of tax code to GL account: %w", err)
	}
	for le, byCode := range out {
		if !ids.Valid(le) {
			return nil, fmt.Errorf("RECEIVABLES_RMA_TAX_ACCOUNTS entry %q needs a UUID legal entity id", le)
		}
		for code, account := range byCode {
			if code == "" || account == "" {
				return nil, fmt.Errorf("RECEIVABLES_RMA_TAX_ACCOUNTS entry %q needs a tax code and a GL account", le)
			}
		}
	}
	return out, nil
}

func run() error {
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	adminAddr := os.Getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = ":9090"
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	s := store.New(pool)
	if gl, err := ledgerclient.FromEnv(os.Getenv, "receivables"); err != nil {
		return err
	} else if gl != nil {
		s.WithLedger(gl)
	}
	rmaLedgers, err := parseRMALedgers(os.Getenv("RECEIVABLES_RMA_LEDGERS"))
	if err != nil {
		return err
	}
	s.WithRMALedgers(rmaLedgers)
	glLedgers, err := parseLedgers("RECEIVABLES_GL_LEDGERS", os.Getenv("RECEIVABLES_GL_LEDGERS"))
	if err != nil {
		return err
	}
	s.WithGLLedgers(glLedgers)
	rmaTaxAccounts, err := parseRMATaxAccounts(os.Getenv("RECEIVABLES_RMA_TAX_ACCOUNTS"))
	if err != nil {
		return err
	}
	var taxRecorder taxclient.Recorder
	if tc, err := taxclient.FromEnv(os.Getenv); err != nil {
		return err
	} else if tc != nil {
		taxRecorder = tc
	}
	s.WithRMATax(rmaTaxAccounts, taxRecorder)
	if bc, err := billingclient.FromEnv(os.Getenv); err != nil {
		return err
	} else if bc != nil {
		s.WithBilling(bc).WithReservations(bc)
	}
	if fc, err := fxclient.FromEnv(os.Getenv); err != nil {
		return err
	} else if fc != nil {
		s.WithRates(fc)
	}
	h := api.New(s)
	r := httpx.NewRouter()
	h.Register(r)
	admin := http.NewServeMux()
	admin.Handle("/metrics", h.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sweep, err := newRMASweep(os.Getenv, s)
	if err != nil {
		return err
	}
	if sweep != nil {
		go sweep.loop(ctx)
	}
	autoApply, err := newAutoApplyJob(os.Getenv, s)
	if err != nil {
		return err
	}
	if autoApply != nil {
		go autoApply.loop(ctx)
	}
	sftpPoll, err := newSFTPPollJob(os.Getenv, s)
	if err != nil {
		return err
	}
	if sftpPoll != nil {
		go sftpPoll.loop(ctx)
	}
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr, r) }()
	go func() { errs <- httpx.Serve(ctx, adminAddr, admin) }()
	return <-errs
}
