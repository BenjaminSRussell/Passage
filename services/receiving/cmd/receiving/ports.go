package main

import (
	"fmt"
	"log/slog"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
)

func buildPorts(getenv func(string) string) (api.Ports, error) {
	allowFakes := getenv("PASSAGE_ALLOW_FAKES") == "1"
	var ports api.Ports
	switch url := getenv("LEDGER_URL"); {
	case url != "":
		ports.Ledger = ledger.New(url, nil)
	case allowFakes:
		slog.Warn("LEDGER_URL is empty: journal entries go to an in-memory fake and are lost on restart", "flag", "PASSAGE_ALLOW_FAKES")
		ports.Ledger = &domain.FakeLedger{}
	default:
		return api.Ports{}, fmt.Errorf("LEDGER_URL is required (set PASSAGE_ALLOW_FAKES=1 only for local development)")
	}
	switch url := getenv("CLOSE_URL"); {
	case url != "":
		ports.Periods = periods.New(url, nil)
	case allowFakes:
		slog.Warn("CLOSE_URL is empty: every AP period reads as open", "flag", "PASSAGE_ALLOW_FAKES")
		ports.Periods = domain.FakePeriods{Status: domain.PeriodOpen}
	default:
		return api.Ports{}, fmt.Errorf("CLOSE_URL is required (set PASSAGE_ALLOW_FAKES=1 only for local development)")
	}
	return ports, nil
}
