package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const periodLayout = "2006-01"

var ErrRead = errors.New("general ledger read failed")

type GL struct {
	Client        ledgerclient.Ledger
	LegalEntityID string
	LedgerID      string
	Accounts      map[string]string
}

func (g GL) Movements(ctx context.Context, from, to time.Time) (map[string]decimal.Decimal, error) {
	if from.Day() != 1 || !to.AddDate(0, 0, 1).Equal(time.Date(to.Year(), to.Month()+1, 1, 0, 0, 0, 0, time.UTC)) {
		return nil, &domain.Error{Code: domain.CodeValidation, Field: "period_from", Detail: "the settlement period must cover whole calendar months to reconcile to the GL"}
	}
	out := make(map[string]decimal.Decimal, len(g.Accounts))
	for key, account := range g.Accounts {
		balance, err := g.Client.Balance(ctx, ledgerclient.BalanceQuery{
			LegalEntityID: g.LegalEntityID, LedgerID: g.LedgerID, Account: account,
			PeriodFrom: from.Format(periodLayout), PeriodTo: to.Format(periodLayout),
		})
		if err != nil {
			return nil, fmt.Errorf("%w: tax account %s: %w", ErrRead, account, err)
		}
		out[key] = signed(key, balance)
	}
	return out, nil
}

func signed(key string, b ledgerclient.Balance) decimal.Decimal {
	if strings.HasSuffix(key, "|"+domain.DirInput) {
		return b.Net()
	}
	return b.Net().Neg()
}
