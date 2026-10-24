package store

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/product/internal/domain"
)

func parseDecimal(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}

func classConversions() []domain.Conversion {
	return []domain.Conversion{
		{From: "CASE", To: "EA", Factor: decimal.NewFromInt(24), Class: "count"},
		{From: "BOX", To: "EA", Factor: decimal.NewFromInt(12), Class: "count"},
		{From: "KG", To: "G", Factor: decimal.NewFromInt(1000), Class: "mass"},
	}
}
