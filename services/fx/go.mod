module github.com/BenjaminSRussell/passage/services/fx

go 1.23

require (
	github.com/BenjaminSRussell/passage/platform v0.0.0
	github.com/shopspring/decimal v1.4.0
	pgregory.net/rapid v1.1.0
)

replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
