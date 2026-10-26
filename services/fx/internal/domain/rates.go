package domain

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// Path says how a Resolution was reached.
type Path string

const (
	// PathDirect is a stored rate for the exact pair (or the identity for equal currencies).
	PathDirect Path = "direct"
	// PathInverse is the reciprocal of a stored rate for the reversed pair.
	PathInverse Path = "inverse"
	// PathTriangulated chains two legs through the reference currency.
	PathTriangulated Path = "triangulated"
)

const (
	// MaxRate is the largest accepted rate value; rates are accepted in (0, MaxRate].
	MaxRate = 1_000_000_000
	// MaxRateScale is the largest number of decimal places an accepted rate may carry.
	MaxRateScale = 10
)

var maxRate = decimal.NewFromInt(MaxRate)

var (
	// ErrRateMissing means no rate resolves for the requested pair and date.
	ErrRateMissing = errors.New("LEDGER_RATE_MISSING")
	// ErrFutureRate means the requested or supplied rate date is after the book's as-of day.
	ErrFutureRate = errors.New("FX_RATE_DATE_FUTURE")
	// ErrInvalidRate means a rate or its inputs are malformed.
	ErrInvalidRate = errors.New("FX_RATE_INVALID")
	// ErrInvalidCurrency is returned for any currency code that is not three uppercase A-Z letters.
	ErrInvalidCurrency = fmt.Errorf("%w: invalid currency code", ErrInvalidRate)
)

// RateType names a family of rates and whether its pairs may be inverted.
type RateType struct {
	Code         string
	AllowInverse bool
}

// Rate is one versioned exchange rate row, valid from ValidFrom (a UTC day).
type Rate struct {
	ID           string
	Type         string
	From         string
	To           string
	ValidFrom    time.Time
	Value        decimal.Decimal
	SupersededBy string
}

// Hop is one resolved leg of a Resolution.
type Hop struct {
	From      string
	To        string
	Rate      decimal.Decimal
	ValidFrom time.Time
}

// Resolution is the outcome of Book.Resolve. Factors holds one rate per hop (one for
// direct and inverse paths, two for triangulated), to be multiplied in order.
type Resolution struct {
	Path    Path
	Factors []decimal.Decimal
	Hops    []Hop
}

type seriesKey struct{ typ, from, to string }

// Book is an append-only in-memory rate book. Lookups are binary searches over
// the per-pair series of current rows, never scans of the full history.
type Book struct {
	now    time.Time
	types  map[string]RateType
	rates  []Rate
	ids    map[string]struct{}
	series map[seriesKey][]int
}

// NewBook creates a book as of the UTC day of now with the given rate types.
func NewBook(now time.Time, types ...RateType) *Book {
	b := &Book{
		now:    day(now),
		types:  make(map[string]RateType, len(types)),
		ids:    map[string]struct{}{},
		series: map[seriesKey][]int{},
	}
	for _, t := range types {
		b.types[t.Code] = t
	}
	return b
}

func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func checkCurrency(c string) error {
	if len(c) == 3 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z' && c[2] >= 'A' && c[2] <= 'Z' {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInvalidCurrency, c)
}

// Add appends a rate. A current row of the same type, pair and day is flagged as
// superseded by it. Rates must be positive, at most MaxRate and carry at most
// MaxRateScale decimals.
func (b *Book) Add(r Rate) error {
	if _, ok := b.types[r.Type]; !ok {
		return fmt.Errorf("%w: unknown rate type %q", ErrInvalidRate, r.Type)
	}
	if err := checkCurrency(r.From); err != nil {
		return err
	}
	if err := checkCurrency(r.To); err != nil {
		return err
	}
	if r.From == r.To {
		return fmt.Errorf("%w: pair %q/%q", ErrInvalidRate, r.From, r.To)
	}
	if r.Value.Sign() <= 0 || r.Value.GreaterThan(maxRate) || r.Value.Exponent() < -MaxRateScale {
		return fmt.Errorf("%w: value must be in (0, %d] with at most %d decimals", ErrInvalidRate, MaxRate, MaxRateScale)
	}
	if r.ID == "" {
		return fmt.Errorf("%w: id required", ErrInvalidRate)
	}
	if _, dup := b.ids[r.ID]; dup {
		return fmt.Errorf("%w: duplicate id %q", ErrInvalidRate, r.ID)
	}
	r.ValidFrom = day(r.ValidFrom)
	if r.ValidFrom.After(b.now) {
		return fmt.Errorf("%w: valid from %s", ErrFutureRate, r.ValidFrom.Format(time.DateOnly))
	}
	r.SupersededBy = ""
	key := seriesKey{r.Type, r.From, r.To}
	cur := b.series[key]
	pos, found := slices.BinarySearchFunc(cur, r.ValidFrom, func(i int, d time.Time) int { return b.rates[i].ValidFrom.Compare(d) })
	b.rates = append(b.rates, r)
	idx := len(b.rates) - 1
	if found {
		b.rates[cur[pos]].SupersededBy = r.ID
		cur[pos] = idx
	} else {
		b.series[key] = slices.Insert(cur, pos, idx)
	}
	b.ids[r.ID] = struct{}{}
	return nil
}

func (b *Book) lookup(typ, from, to string, date time.Time) (*Rate, bool) {
	cur := b.series[seriesKey{typ, from, to}]
	pos, found := slices.BinarySearchFunc(cur, date, func(i int, d time.Time) int { return b.rates[i].ValidFrom.Compare(d) })
	if found {
		return &b.rates[cur[pos]], true
	}
	if pos == 0 {
		return nil, false
	}
	return &b.rates[cur[pos-1]], true
}

func (b *Book) leg(typ, from, to string, date time.Time) (Hop, Path, bool) {
	if r, ok := b.lookup(typ, from, to, date); ok {
		return Hop{From: from, To: to, Rate: r.Value, ValidFrom: r.ValidFrom}, PathDirect, true
	}
	if b.types[typ].AllowInverse {
		if r, ok := b.lookup(typ, to, from, date); ok {
			return Hop{From: from, To: to, Rate: decimal.NewFromInt(1).Div(r.Value), ValidFrom: r.ValidFrom}, PathInverse, true
		}
	}
	return Hop{}, "", false
}

func resolution(p Path, hops ...Hop) Resolution {
	factors := make([]decimal.Decimal, len(hops))
	for i, h := range hops {
		factors[i] = h.Rate
	}
	return Resolution{Path: p, Factors: factors, Hops: hops}
}

// Resolve finds the rate from one currency to another for a date: exact pair, then
// the inverse when the rate type allows it, then triangulation through refCurrency.
// It returns ErrFutureRate for dates after the book's as-of day and ErrRateMissing
// when nothing resolves.
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
		return resolution(PathDirect, Hop{From: from, To: to, Rate: decimal.NewFromInt(1), ValidFrom: date}), nil
	}
	if h, p, ok := b.leg(typ, from, to, date); ok {
		return resolution(p, h), nil
	}
	if refCurrency != "" && refCurrency != from && refCurrency != to {
		first, _, ok1 := b.leg(typ, from, refCurrency, date)
		second, _, ok2 := b.leg(typ, refCurrency, to, date)
		if ok1 && ok2 {
			return resolution(PathTriangulated, first, second), nil
		}
	}
	return Resolution{}, missing
}
