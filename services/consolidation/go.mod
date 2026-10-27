module github.com/BenjaminSRussell/passage/services/consolidation

go 1.25.0

require (
	github.com/BenjaminSRussell/passage/platform v0.0.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/shopspring/decimal v1.4.0
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
