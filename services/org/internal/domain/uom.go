package domain

import "github.com/shopspring/decimal"

const (
	divisionPrecision = 24
	DisplayPrecision  = 12
)

type Conversion struct {
	From, To, Class string
	Factor, Offset  decimal.Decimal
}

type step struct {
	c       Conversion
	reverse bool
}

func (s step) apply(q decimal.Decimal) decimal.Decimal {
	if s.reverse {
		return q.Sub(s.c.Offset).DivRound(s.c.Factor, divisionPrecision)
	}
	return q.Mul(s.c.Factor).Add(s.c.Offset)
}

func (s step) next() string {
	if s.reverse {
		return s.c.From
	}
	return s.c.To
}

func neighbours(convs []Conversion, unit string) []step {
	var out []step
	for _, c := range convs {
		if c.Factor.IsZero() {
			continue
		}
		if c.From == unit {
			out = append(out, step{c, false})
		}
		if c.To == unit {
			out = append(out, step{c, true})
		}
	}
	return out
}

func Convert(convs []Conversion, from, to string, qty decimal.Decimal) (decimal.Decimal, error) {
	if from == to {
		return qty, nil
	}
	first := neighbours(convs, from)
	for _, s := range first {
		if s.next() == to {
			return s.apply(qty), nil
		}
	}
	for _, s := range first {
		for _, s2 := range neighbours(convs, s.next()) {
			if s2.c.Class == s.c.Class && s2.next() == to {
				return s2.apply(s.apply(qty)), nil
			}
		}
	}
	return decimal.Zero, &Error{CodeNoPath, "no conversion path from " + from + " to " + to}
}

func Display(qty decimal.Decimal, precision int32) decimal.Decimal { return qty.Round(precision) }

func StandardConversions() []Conversion {
	rows := []struct{ from, to, class, factor, offset string }{
		{"kg", "g", "mass", "1000", "0"},
		{"lb", "kg", "mass", "0.45359237", "0"},
		{"t", "kg", "mass", "1000", "0"},
		{"m", "cm", "length", "100", "0"},
		{"km", "m", "length", "1000", "0"},
		{"in", "mm", "length", "25.4", "0"},
		{"ft", "in", "length", "12", "0"},
		{"L", "mL", "volume", "1000", "0"},
		{"gal", "L", "volume", "3.785411784", "0"},
		{"C", "K", "temperature", "1", "273.15"},
		{"C", "F", "temperature", "1.8", "32"},
		{"h", "min", "time", "60", "0"},
		{"min", "s", "time", "60", "0"},
	}
	out := make([]Conversion, len(rows))
	for i, r := range rows {
		out[i] = Conversion{From: r.from, To: r.to, Class: r.class, Factor: decimal.RequireFromString(r.factor), Offset: decimal.RequireFromString(r.offset)}
	}
	return out
}
