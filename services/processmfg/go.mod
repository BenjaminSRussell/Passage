module github.com/BenjaminSRussell/passage/services/processmfg

go 1.26.0

require (
	github.com/BenjaminSRussell/passage/platform v0.0.0-00010101000000-000000000000
	github.com/go-chi/chi/v5 v5.5.0
	github.com/jackc/pgx/v5 v5.12.0
	github.com/shopspring/decimal v1.4.0
	golang.org/x/image v0.47.0
	pgregory.net/rapid v1.3.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/BenjaminSRussell/passage/platform => ../../platform/go
