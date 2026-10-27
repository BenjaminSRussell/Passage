module github.com/BenjaminSRussell/passage/services/intercompany

go 1.23

require (
	github.com/BenjaminSRussell/passage/platform v0.0.0
	github.com/go-chi/chi/v5 v5.2.1
	github.com/shopspring/decimal v1.4.0
	pgregory.net/rapid v1.1.0
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
