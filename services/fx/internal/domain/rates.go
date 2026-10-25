package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type Path string

const (
	PathDirect       Path = "direct"
	PathInverse      Path = "inverse"
	PathTriangulated Path = "triangulated"
)

var (
	ErrRateMissing = errors.New("LEDGER_RATE_MISSING")
	ErrFutureRate  = errors.New("FX_RATE_DATE_FUTURE")
	ErrInvalidRate = errors.New("FX_RATE_INVALID")
)

type RateType struct {
	Code         string
	AllowInverse bool
}

type Rate struct {
	ID           string
	Type         string
	From         string
	To           string
	ValidFrom    time.Time
	Value        decimal.Decimal
	SupersededBy string
}

type Resolution struct {
	Path    Path
	Factors []decimal.Decimal
}

type Book struct {
	now   time.Time
	types map[string]RateType
	rates []Rate
}

func NewBook(now time.Time, types ...RateType) *Book {
	b := &Book{now: day(now), types: make(map[string]RateType, len(types))}
	for _, t := range types {
		b.types[t.Code] = t
	}
	return b
}

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func (b *Book) Add(r Rate) error {
	if _, ok := b.types[r.Type]; !ok {
		return fmt.Errorf("%w: unknown rate type %q", ErrInvalidRate, r.Type)
	}
	if !validCurrency(r.From) || !validCurrency(r.To) || r.From == r.To {
		return fmt.Errorf("%w: pair %q/%q", ErrInvalidRate, r.From, r.To)
	}
	if r.Value.Sign() <= 0 {
		return fmt.Errorf("%w: value must be positive", ErrInvalidRate)
	}
	if r.ID == "" {
		return fmt.Errorf("%w: id required", ErrInvalidRate)
	}
	for _, o := range b.rates {
		if o.ID == r.ID {
			return fmt.Errorf("%w: duplicate id %q", ErrInvalidRate, r.ID)
		}
	}
	r.ValidFrom = day(r.ValidFrom)
	if r.ValidFrom.After(b.now) {
		return fmt.Errorf("%w: valid from %s", ErrFutureRate, r.ValidFrom.Format(time.DateOnly))
	}
	for i := range b.rates {
		o := &b.rates[i]
		if o.SupersededBy == "" && o.Type == r.Type && o.From == r.From && o.To == r.To && o.ValidFrom.Equal(r.ValidFrom) {
			o.SupersededBy = r.ID
		}
	}
	r.SupersededBy = ""
	b.rates = append(b.rates, r)
	return nil
}

func (b *Book) lookup(typ, from, to string, date time.Time) (decimal.Decimal, bool) {
	var best *Rate
	for i := range b.rates {
		r := &b.rates[i]
		if r.SupersededBy != "" || r.Type != typ || r.From != from || r.To != to || r.ValidFrom.After(date) {
			continue
		}
		if best == nil || r.ValidFrom.After(best.ValidFrom) {
			best = r
		}
	}
	if best == nil {
		return decimal.Decimal{}, false
	}
	return best.Value, true
}

func (b *Book) leg(typ, from, to string, date time.Time) (decimal.Decimal, Path, bool) {
	if v, ok := b.lookup(typ, from, to, date); ok {
		return v, PathDirect, true
	}
	if b.types[typ].AllowInverse {
		if v, ok := b.lookup(typ, to, from, date); ok {
			return decimal.NewFromInt(1).Div(v), PathInverse, true
		}
	}
	return decimal.Decimal{}, "", false
}

func (b *Book) Resolve(typ, from, to string, date time.Time, refCurrency string) (Resolution, error) {
	date = day(date)
	if date.After(b.now) {
		return Resolution{}, fmt.Errorf("%w: %s", ErrFutureRate, date.Format(time.DateOnly))
	}
	missing := fmt.Errorf("%w: type %s pair %s/%s date %s", ErrRateMissing, typ, from, to, date.Format(time.DateOnly))
	if _, ok := b.types[typ]; !ok {
		return Resolution{}, missing
	}
	if from == to {
		return Resolution{Path: PathDirect, Factors: []decimal.Decimal{decimal.NewFromInt(1)}}, nil
	}
	if v, p, ok := b.leg(typ, from, to, date); ok {
		return Resolution{Path: p, Factors: []decimal.Decimal{v}}, nil
	}
	if refCurrency != "" && refCurrency != from && refCurrency != to {
		first, _, ok1 := b.leg(typ, from, refCurrency, date)
		second, _, ok2 := b.leg(typ, refCurrency, to, date)
		if ok1 && ok2 {
			return Resolution{Path: PathTriangulated, Factors: []decimal.Decimal{first, second}}, nil
		}
	}
	return Resolution{}, missing
}
