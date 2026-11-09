package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/api"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/ebics"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

// ebicsService is what the onboarding routes drive: the subscriber store and the client.
type ebicsService struct {
	*store.Postgres
	*ebics.Client
}

// wireEBICS enables the EBICS subscriber routes, the "ebics" bank channel and,
// with TREASURY_EBICS_POLL_WORKERS=true, the statement poller, when
// TREASURY_BANK_CHANNELS lists ebics. Everything is off otherwise.
func wireEBICS(ctx context.Context, a *api.API, pg *store.Postgres, vault *bankconnect.Vault, cfg bankconnect.VaultConfig) error {
	poll, pollOn, err := ebics.LoadPollConfig(os.Getenv)
	if err != nil {
		return err
	}
	if !slices.Contains(cfg.BankChannels, bankconnect.ProtocolEBICS) {
		if pollOn {
			return errors.New("TREASURY_EBICS_POLL_WORKERS needs ebics in TREASURY_BANK_CHANNELS")
		}
		return nil
	}
	if vault == nil {
		return errors.New("the ebics bank channel needs VAULT_ADDR")
	}
	client := &ebics.Client{Store: pg, Keys: func(s ebics.Subscriber) (ebics.Keys, error) {
		k, err := vault.EBICSKeys(bankconnect.EBICSKeyRefs{A006: s.A006Key, X002: s.X002Key, E002KVMount: cfg.EBICSKVMount, E002KVPath: s.E002KVPath})
		if err != nil {
			return nil, err
		}
		return k, nil
	}}
	a.WithEBICS(ebicsService{Postgres: pg, Client: client})
	a.WithChannels(bankconnect.Channels{bankconnect.ProtocolEBICS: ebics.AccountSender{Client: client, Accounts: pg, Audit: pg}})
	if pollOn {
		go (&ebics.Poller{Client: client, Lister: pg, Sink: pg.ImportStatementFile, Config: poll}).Loop(ctx)
		slog.Info("ebics statement polling on", "order", poll.OrderType, "interval", poll.Interval, "tenants", len(poll.Tenants))
	}
	slog.Info("ebics bank channel on")
	return nil
}
