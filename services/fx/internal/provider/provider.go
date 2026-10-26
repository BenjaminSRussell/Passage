package provider

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
)

const (
	maxDecimals     = domain.MaxRateScale
	maxExponentSpan = 1000
	maxBody         = 4 << 20
)

var (
	ErrDisabled  = errors.New("provider disabled")
	ErrMalformed = errors.New("malformed provider payload")
	maxRate      = decimal.NewFromInt(domain.MaxRate)
)

type RatePoint struct {
	From              string
	To                string
	ValidFrom         time.Time
	Rate              decimal.Decimal
	ProviderTimestamp *time.Time
	PayloadHash       [32]byte
}

type Provider interface {
	Name() string
	Fetch(ctx context.Context, rateType string, date time.Time) ([]RatePoint, error)
}

func hashOf(record string) [32]byte { return sha256.Sum256([]byte(record)) }

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

func validCurrency(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, c := range code {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

func parseRate(raw string, strict bool) (decimal.Decimal, error) {
	rate, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("rate %q is not a number", raw)
	}
	if rate.Sign() <= 0 {
		return decimal.Decimal{}, errors.New("rate must be greater than zero")
	}
	if rate.Exponent() > maxDecimals || rate.Exponent() < -maxExponentSpan {
		return decimal.Decimal{}, errors.New("rate is out of range")
	}
	if rate.Exponent() < -maxDecimals {
		if strict {
			return decimal.Decimal{}, fmt.Errorf("rate has more than %d decimals", maxDecimals)
		}
		rate = rate.Round(maxDecimals)
		if rate.Sign() <= 0 {
			return decimal.Decimal{}, errors.New("rate must be greater than zero")
		}
	}
	if rate.GreaterThan(maxRate) {
		return decimal.Decimal{}, errors.New("rate is out of range")
	}
	return rate, nil
}

func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func fetchBody(ctx context.Context, client *http.Client, name, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", name, stripURL(err))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", name, stripURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: unexpected status %d", name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", name, stripURL(err))
	}
	return body, nil
}
