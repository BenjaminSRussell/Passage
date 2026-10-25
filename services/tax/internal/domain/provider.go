package domain

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultProviderTimeout = 2 * time.Second
	clientErrorFloor       = 400
	serverErrorFloor       = 500
)

type Address struct {
	Line1      string `json:"line1"`
	City       string `json:"city"`
	Region     string `json:"region"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

type TaxProvider interface {
	Name() string
	Calculate(ctx context.Context, doc Document) (Result, error)
	Commit(ctx context.Context, documentRef string) error
	Void(ctx context.Context, documentRef string) error
	ValidateAddress(ctx context.Context, address Address) error
}

type ProviderError struct {
	Status int
	Err    error
}

func (e *ProviderError) Error() string { return fmt.Sprintf("provider status %d: %v", e.Status, e.Err) }

func (e *ProviderError) Unwrap() error { return e.Err }

type FallbackNotice struct{ Provider, Reason string }

func CalculateWithFailover(ctx context.Context, p TaxProvider, doc Document, cfg Config, timeout time.Duration) (Result, *FallbackNotice, error) {
	if timeout <= 0 {
		timeout = DefaultProviderTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := p.Calculate(callCtx, doc)
		done <- outcome{res, err}
	}()
	var out outcome
	select {
	case out = <-done:
	case <-callCtx.Done():
		out.err = callCtx.Err()
	}
	if out.err == nil {
		return out.res, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, nil, err
	}
	var pe *ProviderError
	if errors.As(out.err, &pe) && pe.Status >= clientErrorFloor && pe.Status < serverErrorFloor {
		return Result{}, nil, out.err
	}
	res, err := Calculate(doc, cfg)
	if err != nil {
		return Result{}, nil, err
	}
	res.Fallback = true
	res.Warnings = append(res.Warnings, Warning{Code: CodeProviderFallback,
		Detail: fmt.Sprintf("Provider %s unavailable; internal engine used. Document flagged for recommit.", p.Name())})
	return res, &FallbackNotice{Provider: p.Name(), Reason: out.err.Error()}, nil
}
