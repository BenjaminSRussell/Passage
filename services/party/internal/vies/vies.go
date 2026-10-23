package vies

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

//go:embed testdata/vies-recorded.json
var recorded []byte

type record struct {
	Country string `json:"country"`
	Number  string `json:"number"`
	Valid   bool   `json:"valid"`
}

type Fixture struct{ results map[string]bool }

func NewFixture(r io.Reader) (*Fixture, error) {
	var records []record
	if err := json.NewDecoder(r).Decode(&records); err != nil {
		return nil, fmt.Errorf("decode recorded VIES responses: %w", err)
	}
	results := make(map[string]bool, len(records))
	for _, rec := range records {
		results[rec.Country+rec.Number] = rec.Valid
	}
	return &Fixture{results: results}, nil
}

func Recorded() (*Fixture, error) { return NewFixture(bytes.NewReader(recorded)) }

func (f *Fixture) Check(_ context.Context, country, number string) (domain.Status, error) {
	valid, ok := f.results[country+number]
	switch {
	case !ok:
		return domain.StatusUnverified, nil
	case valid:
		return domain.StatusValid, nil
	default:
		return domain.StatusInvalid, nil
	}
}

type Unavailable struct{}

func (Unavailable) Check(context.Context, string, string) (domain.Status, error) {
	return domain.StatusUnverified, nil
}
